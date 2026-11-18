package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type retrievalTransport func(*http.Request) (*http.Response, error)

func (f retrievalTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestAnalyzeWithCodeReadsPinnedRevisionBeyondInitialEvidence(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, data)
		}
		return strings.TrimSpace(string(data))
	}
	git("init", "-q")
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("producer.go", "package example\nfunc producer() int { return 41 }\n")
	git("add", "producer.go")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "fixture")
	revision := git("rev-parse", "HEAD")
	write("producer.go", "package example\nfunc producer() int { return 99 }\n")
	write("untracked.go", "package example\nfunc producer() int { return 100 }\n")

	calls := 0
	client := &Client{Endpoint: "https://gateway.example.test", Model: "fixture", HTTP: &http.Client{Transport: retrievalTransport(func(req *http.Request) (*http.Response, error) {
		var request struct {
			Messages []struct{ Content string }
		}
		if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		prompt := request.Messages[0].Content
		calls++
		result := `{"lookups":[{"kind":"read","query":"producer.go","line":2},{"kind":"search","query":"producer","line":0},{"kind":"read","query":"../producer.go","line":2},{"kind":"read","query":"untracked.go","line":2}]}`
		if calls == 2 {
			if !strings.Contains(prompt, "return 41") || strings.Contains(prompt, "return 99") || strings.Contains(prompt, "return 100") ||
				!strings.Contains(prompt, "original failure") {
				t.Fatalf("incorrect evidence passed to final analysis: %s", prompt)
			}
			id := regexp.MustCompile(`E-code-[a-f0-9]+`).FindString(prompt)
			result = `{"hypotheses":[{"statement":"Inspect producer.","affected_component":"producer.go:2","supporting_evidence_ids":["` + id + `"],"confidence":0.5}]}`
		}
		envelope, _ := json.Marshal(map[string]any{
			"model": "fixture", "choices": []any{map[string]any{"message": map[string]string{"content": result}}},
		})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(envelope)))}, nil
	})}}
	report, err := client.AnalyzeWithCode(context.Background(), Input{Evidence: []Evidence{
		{ID: "E-original", Kind: "original_ci_log", Detail: "original failure"},
	}}, repo, revision)
	if err != nil || calls != 2 || len(report.Retrieved) != 2 || len(report.Hypotheses) != 1 {
		t.Fatalf("unexpected retrieval result (%d calls): %#v %v", calls, report, err)
	}
}
