package ceaser_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the actual installer with local download fixtures, including the
// requirement that a corrupt or ambiguous checksum never reaches execution.
func TestReleaseInstaller(t *testing.T) {
	for _, mode := range []string{"valid", "tampered", "missing", "duplicate", "invalid-version", "unsupported-platform"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0755); err != nil {
				t.Fatal(err)
			}
			write := func(path, content string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), 0755); err != nil {
					t.Fatal(err)
				}
			}
			asset := "#!/usr/bin/env bash\ntouch \"$FIXTURE_DIR/executed\"\necho 'ceaser v7'\n"
			digest := sha256.Sum256([]byte(asset))
			checksum := fmt.Sprintf("%x  ceaser_linux_amd64\n", digest)
			switch mode {
			case "tampered":
				asset += "# changed after signing\n"
			case "missing":
				checksum = fmt.Sprintf("%x  other_binary\n", digest)
			case "duplicate":
				checksum += checksum
			}
			write(filepath.Join(dir, "ceaser_linux_amd64"), asset)
			write(filepath.Join(dir, "SHA256SUMS"), checksum)
			write(filepath.Join(bin, "curl"), `#!/usr/bin/env bash
set -euo pipefail
url=''
while (($#)); do
  case "$1" in
    https://*) url="$1"; shift ;;
    -o) cp "$FIXTURE_DIR/${url##*/}" "$2"; exit ;;
    *) shift ;;
  esac
done
exit 2
`)
			platform := "Linux"
			if mode == "unsupported-platform" {
				platform = "Darwin"
			}
			write(filepath.Join(bin, "uname"), "#!/usr/bin/env bash\nif [[ $1 == -s ]]; then echo "+platform+"; else echo x86_64; fi\n")
			version := "v7"
			if mode == "invalid-version" {
				version = "v7/../../unexpected"
			}
			cmd := exec.Command("bash", ".github/scripts/install-ceaser.sh")
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FIXTURE_DIR="+dir, "RUNNER_TEMP="+dir, "CEASER_VERSION="+version,
				"GITHUB_PATH="+filepath.Join(dir, "path"), "GITHUB_OUTPUT="+filepath.Join(dir, "output"))
			out, err := cmd.CombinedOutput()
			_, executed := os.Stat(filepath.Join(dir, "executed"))
			if mode != "valid" {
				if err == nil || !os.IsNotExist(executed) {
					t.Fatalf("invalid installation must fail before execution: err=%v, executed=%v, output=%s", err, executed, out)
				}
				return
			}
			if err != nil || executed != nil {
				t.Fatalf("valid installation failed: %v, executed=%v, output=%s", err, executed, out)
			}
			output, err := os.ReadFile(filepath.Join(dir, "output"))
			if err != nil {
				t.Fatal(err)
			}
			binary := strings.TrimSpace(strings.TrimPrefix(string(output), "binary-path="))
			if !filepath.IsAbs(binary) || filepath.Base(binary) != "ceaser" {
				t.Fatalf("missing installed binary output: %s", output)
			}
		})
	}
}
