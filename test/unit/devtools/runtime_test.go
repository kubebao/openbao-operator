package devtools_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func bashPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func sourceScript(t *testing.T, name, root string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "..", name))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, name)
	write(t, path, string(contents), 0755)
	return path
}

func TestAPIReferencePublication(t *testing.T) {
	for _, name := range []string{"missing perl", "failed perl", "success"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			script := sourceScript(t, "hack/docs/generate-api-reference.sh", root)
			published := filepath.Join(root, "website/generated/api-reference.md")
			write(t, published, "previous reference\n", 0644)
			tool := filepath.Join(root, "crd-ref-docs")
			write(t, tool, "#!"+bashPath(t)+`
for argument in "$@"; do
 case "$argument" in --output-path=*) output="${argument#*=}" ;; esac
done
cat > "$output" <<'RAW'
# API Reference
## Packages
### Resource Types
- [OpenBaoCluster](#openbaocluster)

#### OpenBaoCluster
<Example>
RAW
`, 0755)
			env := append(os.Environ(), "CRD_REF_DOCS_BIN="+tool)
			switch name {
			case "missing perl":
				bin := filepath.Join(root, "minimal-bin")
				dirname, err := exec.LookPath("dirname")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(bin, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dirname, filepath.Join(bin, "dirname")); err != nil {
					t.Fatal(err)
				}
				env = append(env, "PATH="+bin)
			case "failed perl":
				bin := filepath.Join(root, "failure-bin")
				write(t, filepath.Join(bin, "perl"), "#!"+bashPath(t)+"\nexit 23\n", 0755)
				env = append(env, "PATH="+bin+":"+os.Getenv("PATH"))
			}
			cmd := exec.Command(bashPath(t), script)
			cmd.Env = env
			log, err := cmd.CombinedOutput()
			got, readErr := os.ReadFile(published)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if name == "success" {
				if err != nil {
					t.Fatalf("generate: %v\n%s", err, log)
				}
				if !strings.Contains(string(got), "&lt;Example&gt;") {
					t.Fatalf("missing escaped generated content: %s", got)
				}
			} else {
				if err == nil {
					t.Fatalf("generator accepted %s", name)
				}
				if string(got) != "previous reference\n" {
					t.Fatalf("failure replaced previous reference: %s", got)
				}
				if name == "missing perl" && !strings.Contains(string(log), "perl is required") {
					t.Fatalf("missing prerequisite diagnostic: %s", log)
				}
			}
			leftovers, err := filepath.Glob(published + ".tmp.*")
			if err != nil || len(leftovers) > 0 {
				t.Fatalf("temporary output retained: %v, %v", leftovers, err)
			}
		})
	}
}

func TestASTGrepInstaller(t *testing.T) {
	for _, name := range []string{"native", "explicit loader", "bad checksum"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			script := sourceScript(t, "hack/dev/install-ast-grep.sh", root)
			binary := "#!" + bashPath(t) + "\n"
			if name == "explicit loader" {
				binary += "[[ ${AST_GREP_TEST_LOADED:-} == yes ]] || exit 127\n"
			}
			binary += "echo 'ast-grep 0.42.3'\n"
			var archive bytes.Buffer
			zw := zip.NewWriter(&archive)
			file, err := zw.Create("ast-grep")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write([]byte(binary)); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			archivePath := filepath.Join(root, "release.zip")
			if err := os.WriteFile(archivePath, archive.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(script)
			if err != nil {
				t.Fatal(err)
			}
			if name != "bad checksum" {
				// Substitute only the fixture archive digest in this isolated copy.
				data = bytes.ReplaceAll(data,
					[]byte("4191ac4247d183c502778e740a68b7cf45fe477b6423c43b8b8d6e732ba3b333"),
					[]byte(fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))))
			}
			if err := os.WriteFile(script, data, 0755); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(root, "fake-bin")
			write(t, filepath.Join(bin, "uname"), "#!"+bashPath(t)+
				"\nif [[ $1 == -s ]]; then echo Linux; else echo x86_64; fi\n", 0755)
			write(t, filepath.Join(bin, "curl"), "#!"+bashPath(t)+`
while [[ $# -gt 0 ]]; do
 if [[ $1 == --output ]]; then cp "$AST_GREP_TEST_ARCHIVE" "$2"; exit; fi
 shift
done
exit 2
`, 0755)
			destination := filepath.Join(root, "bin/ast-grep")
			write(t, destination, "previous tool\n", 0755)
			env := append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"),
				"AST_GREP_VERSION=0.42.3", "AST_GREP_DESTINATION="+destination,
				"AST_GREP_TEST_ARCHIVE="+archivePath, "AST_GREP_LINUX_LOADER=", "AST_GREP_LINUX_LIBRARY_PATH=")
			if name == "explicit loader" {
				loader := filepath.Join(root, "loader")
				write(t, loader, "#!"+bashPath(t)+
					"\n[[ $1 == --library-path ]] || exit 2\nshift 2\n"+
					"export AST_GREP_TEST_LOADED=yes\nexec \"$@\"\n", 0755)
				env = append(env, "AST_GREP_LINUX_LOADER="+loader, "AST_GREP_LINUX_LIBRARY_PATH="+root)
			}
			cmd := exec.Command(bashPath(t), script)
			cmd.Env = env
			log, err := cmd.CombinedOutput()
			if name == "bad checksum" {
				if err == nil || !strings.Contains(string(log), "checksum mismatch") {
					t.Fatalf("checksum accepted: %v\n%s", err, log)
				}
				got, err := os.ReadFile(destination)
				if err != nil || string(got) != "previous tool\n" {
					t.Fatalf("prior tool changed: %q, %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("install: %v\n%s", err, log)
			}
			got, err := exec.Command(destination, "--version").CombinedOutput()
			if err != nil || strings.TrimSpace(string(got)) != "ast-grep 0.42.3" {
				t.Fatalf("installed tool: %v\n%s", err, got)
			}
		})
	}
}
