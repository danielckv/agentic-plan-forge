//go:build client && !server

package ast

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// requireGit skips tests that need a git executable and a POSIX shell.
func requireGit(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the sample repository script needs a POSIX shell")
	}
	for _, bin := range []string{"git", "sh"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
}

// isolateGit keeps the developer's git configuration out of the tests: no
// global excludes, no hooks path, no identity.
func isolateGit(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
}

// makeSampleRepo builds tests/fixtures/make-sample-repo.sh into a fresh
// directory and returns its real path.
func makeSampleRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	isolateGit(t)
	dir := filepath.Join(t.TempDir(), "sample-repo")
	script, err := filepath.Abs(filepath.Join("..", "..", "tests", "fixtures", "make-sample-repo.sh"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", script, dir).CombinedOutput()
	if err != nil {
		t.Fatalf("make-sample-repo.sh: %v\n%s", err, out)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// gitRun runs git directly, outside the restricted wrapper, to prepare or
// inspect a test repository.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// writeFiles creates files below dir. An empty string makes an empty file.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// timeAgo is a modification time far enough in the past to be trusted.
func timeAgo() time.Time { return time.Now().Add(-time.Hour) }

// ageFiles sets every file's modification time well before the scan, so the
// racy-timestamp guard does not distrust the cache.
func ageFiles(t *testing.T, root string, rels ...string) {
	t.Helper()
	old := timeAgo()
	for _, rel := range rels {
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(rel)), old, old); err != nil {
			t.Fatal(err)
		}
	}
}

// parseFixture parses src as the file rel and returns what the extractor found.
func parseFixture(t *testing.T, rel string, src []byte) (fileExtract, int) {
	t.Helper()
	set, err := compileLanguages()
	if err != nil {
		t.Fatalf("compileLanguages: %v", err)
	}
	defer set.Close()
	pool := newParserPool()
	defer pool.Close()
	qc := ts.NewQueryCursor()
	defer qc.Close()
	cl := set.forPath(rel)
	if cl == nil {
		t.Fatalf("no grammar for %s", rel)
	}
	ex, errNodes, status, perr := parseAndExtract(context.Background(), pool, cl, qc, DefaultParseTimeout, rel, src)
	if perr != nil || status != parseOK {
		t.Fatalf("parse %s: status %d, error %v", rel, status, perr)
	}
	return ex, errNodes
}

func symbolNames(syms []contracts.Symbol) []string {
	out := make([]string, 0, len(syms))
	for _, s := range syms {
		out = append(out, s.QualifiedName)
	}
	return out
}

func hasString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func hasPrefixString(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
