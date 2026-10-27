//go:build client && !server

package sandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testSource is a one-file archive for tests that only care about sequencing.
func testSource() SourceArchive {
	return SourceArchive{Data: []byte("not a real archive"), Files: 1, Bytes: 10}
}

func writeSourceFile(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// names lists what a packed archive carries.
func names(t *testing.T, data []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var out []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		out = append(out, hdr.Name)
	}
	return out
}

// The archive carries the project's source and leaves out what a build does not
// need: version control, dependencies, build output and host state. .git is the
// largest thing in most repositories and the least useful to a build.
func TestPackageSourceKeepsSourceAndDropsTheRest(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, root, "go.mod", "module example.com/demo\n")
	writeSourceFile(t, root, "main.go", "package main\n")
	writeSourceFile(t, root, "internal/app/app.go", "package app\n")
	writeSourceFile(t, root, "scripts/build.sh", "#!/bin/sh\n")
	// Everything below must not reach a rented VM.
	writeSourceFile(t, root, ".git/config", "[remote]\n")
	writeSourceFile(t, root, "node_modules/left-pad/index.js", "module.exports=1\n")
	writeSourceFile(t, root, "dist/bundle.js", "built\n")
	writeSourceFile(t, root, ".env", "SECRET=hunter2\n")
	writeSourceFile(t, root, "deploy/server.pem", "-----BEGIN KEY-----\n")
	writeSourceFile(t, root, "run.log", "log\n")
	writeSourceFile(t, root, ".agents/STATE.md", "state\n")

	got, err := PackageSource(SourceOptions{Root: root})
	if err != nil {
		t.Fatalf("PackageSource: %v", err)
	}
	list := strings.Join(names(t, got.Data), " ")
	for _, want := range []string{"go.mod", "main.go", "internal/app/app.go", "scripts/build.sh"} {
		if !strings.Contains(list, want) {
			t.Errorf("archive is missing %q: %s", want, list)
		}
	}
	for _, refuse := range []string{".git/", "node_modules/", "dist/", ".env", ".pem", "run.log", ".agents/"} {
		if strings.Contains(list, refuse) {
			t.Errorf("archive carries %q, which a build must never receive: %s", refuse, list)
		}
	}
	if got.Files != 4 {
		t.Errorf("packed %d files, want 4: %s", got.Files, list)
	}
	if len(got.Skipped) == 0 {
		t.Error("nothing was reported as skipped, so the window cannot explain the archive")
	}
}

// The same tree produces the same archive, so a hash of it identifies the
// revision that was built. Timestamps and read order must not leak in.
func TestPackageSourceIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, root, "b.go", "package b\n")
	writeSourceFile(t, root, "a.go", "package a\n")

	first, err := PackageSource(SourceOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	second, err := PackageSource(SourceOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Data, second.Data) {
		t.Fatal("two packs of the same tree differ, so the archive cannot identify a revision")
	}
	list := names(t, first.Data)
	if len(list) != 2 || list[0] != "a.go" || list[1] != "b.go" {
		t.Errorf("entries = %v, want them sorted", list)
	}
}

// An include path keeps a file an exclusion would otherwise drop, so a recipe
// that runs a script inside a skipped directory still has that script.
func TestPackageSourceIncludeOverridesExclusion(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, root, "main.go", "package main\n")
	writeSourceFile(t, root, "build/gen.sh", "#!/bin/sh\n")

	without, err := PackageSource(SourceOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(names(t, without.Data), " "), "build/gen.sh") {
		t.Fatal("the excluded directory was packed without an include")
	}

	with, err := PackageSource(SourceOptions{Root: root, Include: []string{"build/gen.sh"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(names(t, with.Data), " "), "build/gen.sh") {
		t.Error("the explicitly included script was still dropped")
	}
}

// An include that escapes the repository is refused, not silently cleaned: a
// caller asking for ../etc/passwd is a bug worth seeing.
func TestPackageSourceRefusesEscapingInclude(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, root, "main.go", "package main\n")

	for _, bad := range []string{"../outside", "/etc/passwd", "a/../../b"} {
		if _, err := PackageSource(SourceOptions{Root: root, Include: []string{bad}}); err == nil {
			t.Errorf("include %q was accepted", bad)
		}
	}
}

// A repository with nothing packable says so rather than uploading an empty
// archive that fails on the VM for an unexplained reason.
func TestPackageSourceRefusesAnEmptyRepository(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, root, ".git/config", "[core]\n")

	if _, err := PackageSource(SourceOptions{Root: root}); err == nil {
		t.Fatal("an empty repository produced an archive")
	}
}

// A symlink is never packed, because it can point outside the checkout.
func TestPackageSourceSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	writeSourceFile(t, root, "main.go", "package main\n")
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "leak.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := PackageSource(SourceOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(names(t, got.Data), " "), "leak.txt") {
		t.Fatal("a symlink was packed")
	}
	var reported bool
	for _, skip := range got.Skipped {
		if skip.Path == "leak.txt" {
			reported = true
		}
	}
	if !reported {
		t.Error("the refused symlink is not named in the report")
	}
}
