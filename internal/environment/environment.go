// Package environment contains repository environment adapters. Adapters only
// describe how a repository is prepared and tested; diagnosis remains owned by
// internal/diagnosis and execution remains owned by internal/execution.
package environment

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Command []string

type Metadata struct {
	Name, Language, BuildSystem, PackageManager, RuntimeVersion string
	Evidence                                                    []string
}

// Capability describes the depth an environment adapter can provide without
// coupling the diagnosis engine to a language or test framework.
type Capability struct {
	Adapter     string   `json:"adapter"`
	Language    string   `json:"language,omitempty"`
	Level       string   `json:"level"`
	Features    []string `json:"features,omitempty"`
	Limitations []string `json:"limitations,omitempty"`
}

type capabilityProvider interface {
	Capability() Capability
}

type Adapter interface {
	Name() string
	Detect(root string) (Metadata, bool, error)
	Prepare(root string) []Command
	DiscoverTestCommand(root string) (Command, bool)
}

type Registry struct{ adapters []Adapter }

// SelectTestCommand prioritizes explicit configuration, then CI evidence, and
// finally a conservative adapter default.
func (r Registry) SelectTestCommand(root string, explicit, ci Command) (Command, string, error) {
	if len(explicit) > 0 {
		return explicit, "configuration", nil
	}
	if len(ci) > 0 {
		return ci, "ci", nil
	}
	a, _, err := r.Adapter(root)
	if err != nil {
		return nil, "", err
	}
	if cmd, ok := a.DiscoverTestCommand(root); ok {
		return cmd, "adapter", nil
	}
	return nil, "", fmt.Errorf("%s detected, but no test command could be selected; configure command explicitly", a.Name())
}

func NewRegistry() Registry {
	return Registry{adapters: []Adapter{GoAdapter{}, JavaAdapter{}, NodeAdapter{}, PythonAdapter{}, RustAdapter{}, BazelAdapter{}}}
}

// Capabilities returns an honest support matrix for the environments detected
// in a repository. Absence is not interpreted as unsupported source code.
func (r Registry) Capabilities(root string) ([]Capability, error) {
	var out []Capability
	for _, a := range r.adapters {
		_, ok, err := a.Detect(root)
		if err != nil {
			return nil, fmt.Errorf("%s detection: %w", a.Name(), err)
		}
		if !ok {
			continue
		}
		if provider, ok := a.(capabilityProvider); ok {
			out = append(out, provider.Capability())
		}
	}
	return out, nil
}

func (r Registry) Detect(root string) ([]Metadata, error) {
	var out []Metadata
	for _, a := range r.adapters {
		m, ok, err := a.Detect(root)
		if err != nil {
			return nil, fmt.Errorf("%s detection: %w", a.Name(), err)
		}
		if ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func (r Registry) Adapter(root string) (Adapter, Metadata, error) {
	for _, a := range r.adapters {
		m, ok, err := a.Detect(root)
		if err != nil {
			return nil, Metadata{}, err
		}
		if ok {
			return a, m, nil
		}
	}
	return nil, Metadata{}, errors.New("no supported environment detected")
}

func fileExists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}
func firstLine(root, name, pattern string) string {
	f, err := os.Open(filepath.Join(root, name))
	if err != nil {
		return ""
	}
	defer f.Close()
	re := regexp.MustCompile(pattern)
	s := bufio.NewScanner(f)
	for s.Scan() {
		if m := re.FindStringSubmatch(strings.TrimSpace(s.Text())); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}
func hasAny(root string, names ...string) string {
	for _, n := range names {
		if fileExists(root, n) {
			return n
		}
	}
	return ""
}

type GoAdapter struct{}

func (GoAdapter) Name() string { return "go" }
func (GoAdapter) Capability() Capability {
	return Capability{Adapter: "go", Language: "Go", Level: "deep", Features: []string{"test discovery", "AST symbols", "verified calls", "race evidence", "goroutine lifecycle", "synchronization inspection"}}
}
func (GoAdapter) Detect(root string) (Metadata, bool, error) {
	if !fileExists(root, "go.mod") {
		return Metadata{}, false, nil
	}
	return Metadata{Name: "go", Language: "Go", BuildSystem: "go", RuntimeVersion: firstLine(root, "go.mod", `^go\s+(.+)$`), Evidence: []string{"go.mod"}}, true, nil
}
func (GoAdapter) Prepare(string) []Command { return []Command{{"go", "mod", "download"}} }
func (GoAdapter) DiscoverTestCommand(string) (Command, bool) {
	return Command{"go", "test", "./..."}, true
}

type JavaAdapter struct{}

func (JavaAdapter) Name() string { return "jvm" }
func (JavaAdapter) Capability() Capability {
	return Capability{Adapter: "jvm", Language: "Java/Kotlin", Level: "structural", Features: []string{"Maven/Gradle detection", "test discovery", "JUnit evidence", "runtime exception locations", "source symbols"}, Limitations: []string{"thread ownership and JVM happens-before verification are not yet implemented"}}
}
func (JavaAdapter) Detect(root string) (Metadata, bool, error) {
	manifest := hasAny(root, "pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts")
	if manifest == "" {
		return Metadata{}, false, nil
	}
	build := "Gradle"
	manager := "gradle"
	if manifest == "pom.xml" {
		build, manager = "Maven", "maven"
	}
	return Metadata{Name: "jvm", Language: "Java/Kotlin", BuildSystem: build, PackageManager: manager, Evidence: []string{manifest}}, true, nil
}
func (JavaAdapter) Prepare(string) []Command { return nil }
func (JavaAdapter) DiscoverTestCommand(root string) (Command, bool) {
	if fileExists(root, "pom.xml") {
		if fileExists(root, "mvnw") {
			return Command{"./mvnw", "test"}, true
		}
		return Command{"mvn", "test"}, true
	}
	if fileExists(root, "gradlew") {
		return Command{"./gradlew", "test"}, true
	}
	if hasAny(root, "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts") != "" {
		return Command{"gradle", "test"}, true
	}
	return nil, false
}

type NodeAdapter struct{}

func (NodeAdapter) Name() string { return "node" }
func (NodeAdapter) Capability() Capability {
	return Capability{Adapter: "node", Language: "JavaScript/TypeScript", Level: "structural", Features: []string{"test discovery", "Jest/Vitest evidence", "runtime locations", "source symbols"}, Limitations: []string{"promise and event-loop ordering are not yet verified"}}
}
func (NodeAdapter) Detect(root string) (Metadata, bool, error) {
	if !fileExists(root, "package.json") {
		return Metadata{}, false, nil
	}
	lock := hasAny(root, "pnpm-lock.yaml", "yarn.lock", "package-lock.json", "npm-shrinkwrap.json")
	pm := "npm"
	if lock == "pnpm-lock.yaml" {
		pm = "pnpm"
	}
	if lock == "yarn.lock" {
		pm = "yarn"
	}
	return Metadata{Name: "node", Language: "Node.js", PackageManager: pm, Evidence: []string{"package.json", lock}}, true, nil
}
func (a NodeAdapter) Prepare(root string) []Command {
	m, _, _ := a.Detect(root)
	switch m.PackageManager {
	case "pnpm":
		return []Command{{"pnpm", "install", "--frozen-lockfile"}}
	case "yarn":
		return []Command{{"yarn", "install", "--frozen-lockfile"}}
	default:
		return []Command{{"npm", "ci"}}
	}
}
func (NodeAdapter) DiscoverTestCommand(root string) (Command, bool) {
	b, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil, false
	}
	var p struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(b, &p) != nil {
		return nil, false
	}
	if _, ok := p.Scripts["test"]; ok {
		return Command{"npm", "test"}, true
	}
	return nil, false
}

type PythonAdapter struct{}

func (PythonAdapter) Name() string { return "python" }
func (PythonAdapter) Capability() Capability {
	return Capability{Adapter: "python", Language: "Python", Level: "structural", Features: []string{"pytest discovery", "traceback evidence", "source symbols"}, Limitations: []string{"fixture and async-task lifecycle are not yet verified"}}
}
func (PythonAdapter) Detect(root string) (Metadata, bool, error) {
	m := hasAny(root, "pyproject.toml", "requirements.txt", "setup.py", "Pipfile")
	if m == "" {
		return Metadata{}, false, nil
	}
	pm := "pip"
	if fileExists(root, "poetry.lock") {
		pm = "poetry"
	}
	if fileExists(root, "Pipfile.lock") {
		pm = "pipenv"
	}
	return Metadata{Name: "python", Language: "Python", PackageManager: pm, Evidence: []string{m}}, true, nil
}
func (a PythonAdapter) Prepare(root string) []Command {
	m, _, _ := a.Detect(root)
	switch m.PackageManager {
	case "poetry":
		return []Command{{"poetry", "install"}}
	case "pipenv":
		return []Command{{"pipenv", "install", "--deploy"}}
	default:
		if fileExists(root, "requirements.txt") {
			return []Command{{"python", "-m", "pip", "install", "-r", "requirements.txt"}}
		}
		return nil
	}
}
func (PythonAdapter) DiscoverTestCommand(root string) (Command, bool) {
	if fileExists(root, "pytest.ini") || fileExists(root, "tox.ini") || fileExists(root, "tests") {
		return Command{"python", "-m", "pytest", "-q"}, true
	}
	return nil, false
}

type RustAdapter struct{}

func (RustAdapter) Name() string { return "rust" }
func (RustAdapter) Capability() Capability {
	return Capability{Adapter: "rust", Language: "Rust", Level: "structural", Features: []string{"Cargo test discovery", "compiler diagnostics", "source symbols"}, Limitations: []string{"Tokio task and synchronization semantics are not yet verified"}}
}
func (RustAdapter) Detect(root string) (Metadata, bool, error) {
	if !fileExists(root, "Cargo.toml") {
		return Metadata{}, false, nil
	}
	v := firstLine(root, "rust-toolchain.toml", `channel\s*=\s*["']([^"']+)`)
	return Metadata{Name: "rust", Language: "Rust", BuildSystem: "Cargo", RuntimeVersion: v, Evidence: []string{"Cargo.toml"}}, true, nil
}
func (RustAdapter) Prepare(string) []Command                   { return []Command{{"cargo", "fetch"}} }
func (RustAdapter) DiscoverTestCommand(string) (Command, bool) { return Command{"cargo", "test"}, true }

type BazelAdapter struct{}

func (BazelAdapter) Name() string { return "bazel" }
func (BazelAdapter) Capability() Capability {
	return Capability{Adapter: "bazel", Level: "environment", Features: []string{"workspace detection"}, Limitations: []string{"an exact Bazel target must come from CI evidence or configuration"}}
}
func (BazelAdapter) Detect(root string) (Metadata, bool, error) {
	m := hasAny(root, "MODULE.bazel", "WORKSPACE", "WORKSPACE.bazel")
	if m == "" {
		return Metadata{}, false, nil
	}
	return Metadata{Name: "bazel", BuildSystem: "Bazel", RuntimeVersion: firstLine(root, ".bazelversion", `^\s*(.+?)\s*$`), Evidence: []string{m}}, true, nil
}
func (BazelAdapter) Prepare(string) []Command                   { return nil }
func (BazelAdapter) DiscoverTestCommand(string) (Command, bool) { return nil, false }
