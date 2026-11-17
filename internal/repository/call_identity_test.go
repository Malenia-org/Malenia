package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestImportedHeaderGetDoesNotResolveToLocalMuxGet(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/identity\n\ngo 1.23\n",
		"identity.go": `package identity
import "net/http"
type Mux struct{}
func (*Mux) Get() string { return "router" }
func ReadHeader(h http.Header) string { return h.Get("Content-Type") }
`,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := (Analyzer{}).Analyze(context.Background(), root, "repo", "rev", "head")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, node := range snapshot.Nodes {
		names[node.ID] = node.Name
	}
	for _, edge := range snapshot.Relationships {
		if edge.Type == "CALLS" && edge.Metadata["provenance"] == "verified_static_call" && names[edge.SourceID] == "ReadHeader" && names[edge.TargetID] == "Get" {
			t.Fatal("external http.Header.Get was mapped to local Mux.Get")
		}
	}
}
