package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const maxLookups = 5

var fullGitRevision = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type codeLookup struct {
	Kind  string `json:"kind"`
	Query string `json:"query"`
	Line  int    `json:"line"`
}

// AnalyzeWithCode adds a bounded batch of source lookups before final analysis.
// Values from the model are Git object paths or literal search terms.
func (c *Client) AnalyzeWithCode(ctx context.Context, in Input, repo, revision string) (Report, error) {
	if !fullGitRevision.MatchString(revision) {
		return Report{}, fmt.Errorf("code retrieval requires a full commit ID")
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if gitOutputPrefix(ctx, repo, 32, "cat-file", "-t", revision) != "commit\n" {
		return c.Analyze(ctx, in)
	}

	var plan struct {
		Lookups []codeLookup `json:"lookups"`
	}
	planCtx, endPlan := context.WithTimeout(ctx, 20*time.Second)
	_, err := c.complete(planCtx, evidencePrompt(Input{
		Classification: in.Classification, Command: in.Command, Context: in.Context,
		Evidence: CompactEvidence(in.Evidence, in.Context, 10<<10),
	}, "Choose at most five read-only Go code lookups to clarify this CI failure. Look outside the initial candidates where useful. Use read with a repository-relative .go path and a one-based line number, or search with a literal Go symbol or code fragment and line 0. Search matches are textual leads, including possible callers, rather than resolved call edges. Return a lookups array."), "code_lookups", map[string]any{
		"type": "object", "required": []string{"lookups"}, "properties": map[string]any{
			"lookups": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "required": []string{"kind", "query", "line"}, "properties": map[string]any{
					"kind":  map[string]any{"type": "string", "enum": []string{"read", "search"}},
					"query": map[string]any{"type": "string"}, "line": map[string]any{"type": "integer"},
				},
			}},
		},
	}, &plan)
	endPlan()
	if err != nil {
		return c.Analyze(ctx, in)
	}
	var retrieved []Evidence
	seen := map[string]bool{}
	for _, lookup := range plan.Lookups[:min(len(plan.Lookups), maxLookups)] {
		detail := retrieveCode(ctx, repo, revision, lookup)
		if detail == "" || seen[detail] {
			continue
		}
		seen[detail] = true
		sum := sha256.Sum256([]byte(detail))
		retrieved = append(retrieved, Evidence{ID: fmt.Sprintf("E-code-%x", sum[:8]), Kind: "code_retrieval", Detail: redact(detail)})
	}
	if len(retrieved) == 0 {
		return c.Analyze(ctx, in)
	}
	// Reserve evidence budget for retrieval without losing the original failure.
	in.Evidence = CompactEvidence(in.Evidence, in.Context, 14<<10)
	in.Evidence = append(in.Evidence, retrieved...)
	report, err := c.Analyze(ctx, in)
	if err == nil {
		for _, item := range CompactEvidence(in.Evidence, in.Context, 0) {
			if item.Kind == "code_retrieval" {
				report.Retrieved = append(report.Retrieved, item)
			}
		}
	}
	return report, err
}

func retrieveCode(ctx context.Context, repo, revision string, lookup codeLookup) string {
	if len(lookup.Query) == 0 || len(lookup.Query) > 180 || strings.IndexFunc(lookup.Query, unicode.IsControl) >= 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	switch lookup.Kind {
	case "search":
		out := gitOutputPrefix(ctx, repo, 1400, "grep", "-n", "-F", "--no-textconv", "-e", lookup.Query, revision, "--", "*.go")
		if out != "" {
			return fmt.Sprintf("literal matches at %s for %q:\n%s", revision, lookup.Query, out)
		}
	case "read":
		file := lookup.Query
		if lookup.Line < 1 || lookup.Line > 100_000 || path.Clean(file) != file ||
			strings.HasPrefix(file, "/") || strings.HasPrefix(file, "../") ||
			strings.ContainsAny(file, "\\:") || !strings.HasSuffix(file, ".go") {
			return ""
		}
		raw := gitOutputPrefix(ctx, repo, 512<<10, "cat-file", "blob", revision+":"+file)
		lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
		if raw == "" || lookup.Line > len(lines) {
			return ""
		}
		var out strings.Builder
		fmt.Fprintf(&out, "%s:%d at %s\n", file, lookup.Line, revision)
		for i := max(0, lookup.Line-5); i < min(len(lines), lookup.Line+4) && out.Len() < 1350; i++ {
			line := lines[i]
			if len(line) > 160 {
				line = line[:160] + "…"
			}
			out.WriteString(strconv.Itoa(i+1) + "| " + line + "\n")
		}
		return out.String()
	}
	return ""
}

func gitOutputPrefix(ctx context.Context, repo string, limit int64, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "--no-pager"}, args...)...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	pipe, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return ""
	}
	data, readErr := io.ReadAll(io.LimitReader(pipe, limit+1))
	_ = pipe.Close()
	err = cmd.Wait()
	if readErr != nil || ctx.Err() != nil || (err != nil && int64(len(data)) <= limit) {
		return ""
	}
	if int64(len(data)) > limit {
		data = data[:limit]
		if end := strings.LastIndexByte(string(data), '\n'); end >= 0 {
			data = data[:end+1]
		}
	}
	return string(data)
}
