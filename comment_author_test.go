package ceaser_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCommentAuthorIsolation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is needed to exercise the workflow's actual publication script")
	}
	content := readWorkflow(t, ".github/workflows/ci-diagnose.yml")
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatal(err)
	}
	publish := mapKey(mapKey(doc.Content[0], "jobs"), "publish")
	var script string
	for _, step := range mapKey(publish, "steps").Content {
		name := mapKey(step, "name")
		if name != nil && name.Value == "Publish bounded diagnosis comment" {
			script = mapKey(mapKey(step, "with"), "script").Value
		}
	}
	if script == "" {
		t.Fatal("publication script missing")
	}
	for _, tc := range []struct {
		name, bot, existing string
		update              bool
	}{
		{"default author", "github-actions[bot]", "github-actions[bot]", true},
		{"custom author", "ceaseer-bot[bot]", "ceaseer-bot[bot]", true},
		{"other bot", "ceaseer-bot[bot]", "github-actions[bot]", false},
		{"human marker spoof", "ceaseer-bot[bot]", "contributor", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body := filepath.Join(dir, "comment.md")
			if err := os.WriteFile(body, []byte("Diagnosis"), 0600); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(script)
			harness := `const calls = [];
const github = {
  paginate: async () => [{id: 42, user: {login: process.env.EXISTING_AUTHOR}, body: '<!-- ceaser-diagnosis -->\nOld report'}],
  rest: {issues: {
    listComments: () => {},
    updateComment: async p => calls.push({operation: 'update', ...p}),
    createComment: async p => calls.push({operation: 'create', ...p})
  }}
};
const core = {setFailed: message => {throw new Error(message)}};
const context = {repo: {owner: 'caeser-org', repo: 'ceaser'}};
const AsyncFunction = Object.getPrototypeOf(async function(){}).constructor;
new AsyncFunction('require', 'github', 'core', 'context', ` + string(encoded) + `)(require, github, core, context)
 .then(() => process.stdout.write(JSON.stringify(calls)))
 .catch(error => {console.error(error); process.exit(1)});`
			cmd := exec.Command(node, "-e", harness)
			cmd.Env = append(os.Environ(), "PR_NUMBER=25", "COMMENT_PATH="+body,
				"COMMENT_BOT_LOGIN="+tc.bot, "EXISTING_AUTHOR="+tc.existing)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("script failed: %v: %s", err, output)
			}
			var calls []struct {
				Operation string `json:"operation"`
				Body      string `json:"body"`
			}
			if err := json.Unmarshal(output, &calls); err != nil {
				t.Fatal(err)
			}
			want := "create"
			if tc.update {
				want = "update"
			}
			if len(calls) != 1 || calls[0].Operation != want || !strings.Contains(calls[0].Body, "Diagnosis") {
				t.Fatalf("incorrect author-bound publication: %s", output)
			}
		})
	}
}

func TestCommentAppKeyIsPublicationOnly(t *testing.T) {
	content := readWorkflow(t, ".github/workflows/ci-diagnose.yml")
	publish := strings.Index(content, "\n  publish:")
	if publish < 0 {
		t.Fatal("publication job missing")
	}
	if strings.Contains(content[:publish], "secrets.CEASER_APP_PRIVATE_KEY") {
		t.Fatal("App private key must not be available to diagnosis or model analysis")
	}
	if !strings.Contains(content[publish:], "permission-issues: write") {
		t.Fatal("comment token must request only the required issue-comment permission")
	}
}
