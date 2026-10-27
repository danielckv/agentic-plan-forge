package recipe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hashFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// goOnlyFacts matches a tree with go.mod and no other runner.
func goOnlyFacts(root string) Facts {
	return Facts{
		Root: root, Revision: "abc123", GoMod: true,
		Lockfiles: []string{"go.sum"}, Port: 8080,
	}
}

func TestDeriveGoOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.27\n")
	writeFile(t, root, "go.sum", "")

	f, err := Detect(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !f.GoMod {
		t.Fatal("Detect missed go.mod")
	}
	f.Revision = "abc123"
	r, err := Derive(f)
	if !errors.Is(err, ErrNoRunCommand) {
		t.Fatalf("Derive err = %v, want ErrNoRunCommand (no run asset in a bare Go tree)", err)
	}
	if got := strings.Join(r.Build.Argv, " "); got != "go build ./..." {
		t.Errorf("build = %q, want go build ./...", got)
	}
	if got := strings.Join(r.Test.Argv, " "); got != "go test ./..." {
		t.Errorf("test = %q, want go test ./...", got)
	}
	for _, s := range r.Scripts {
		if s.Generated {
			t.Errorf("script %s marked generated; nothing is generated", s.Path)
		}
	}
	if err := r.Validate(); err == nil {
		// A recipe without a run command carries no port; Validate rejects
		// port 0, so confirm the failure names the port rather than silently
		// accepting an unconfigured run.
		t.Error("Validate accepted a recipe with no run command and port 0")
	}
	// Once the caller configures run explicitly, the recipe validates.
	r.Run = Command{ID: "run", Argv: []string{"./app"}, TimeoutSeconds: 60}
	if err := r.Validate(); err != nil {
		t.Errorf("Validate with explicit run: %v", err)
	}
}

func TestDeriveNodeReusesPackageScripts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "package.json", `{"scripts":{"build":"vite build","test":"vitest run","start":"vite preview"}}`)
	writeFile(t, root, "package-lock.json", "{}")

	f, err := Detect(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !f.PackageJSON || f.PackageScripts["build"] != "vite build" {
		t.Fatalf("Detect lost scripts: %+v", f.PackageScripts)
	}
	f.Revision = "abc123"
	r, err := Derive(f)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if got := strings.Join(r.Build.Argv, " "); got != "npm run build" {
		t.Errorf("build = %q, want npm run build unchanged", got)
	}
	if got := strings.Join(r.Test.Argv, " "); got != "npm run test" {
		t.Errorf("test = %q, want npm run test unchanged", got)
	}
	if got := strings.Join(r.Run.Argv, " "); got != "npm run start" {
		t.Errorf("run = %q, want npm run start", got)
	}
	if err := r.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestDeriveMakefileTargetsWin(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.27\n")
	writeFile(t, root, "package.json", `{"scripts":{"build":"vite build","test":"vitest run"}}`)
	writeFile(t, root, "Makefile", ".PHONY: build test\nbuild:\n\tgo build ./...\ntest:\n\tgo test ./...\n")

	f, err := Detect(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	f.Revision = "abc123"
	r, err := Derive(f)
	if !errors.Is(err, ErrNoRunCommand) {
		t.Fatalf("Derive err = %v, want ErrNoRunCommand (no run asset)", err)
	}
	if got := strings.Join(r.Build.Argv, " "); got != "make build" {
		t.Errorf("build = %q, want make build", got)
	}
	if got := strings.Join(r.Test.Argv, " "); got != "make test" {
		t.Errorf("test = %q, want make test", got)
	}
	if r.Build.SourceRef != "Makefile target 'build'" {
		t.Errorf("SourceRef = %q", r.Build.SourceRef)
	}
}

func TestDeriveMissingTestCommandIsErrorNotFakePass(t *testing.T) {
	// The named failure mode: a project with no test command must yield
	// ErrNoTestCommand, never a command that pretends testing happened.
	root := t.TempDir()
	writeFile(t, root, "Makefile", ".PHONY: build\nbuild:\n\techo built\n")

	f, err := Detect(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	f.Revision = "abc123"
	r, err := Derive(f)
	if !errors.Is(err, ErrNoTestCommand) {
		t.Fatalf("Derive err = %v, want ErrNoTestCommand", err)
	}
	if r.Build.ID != "" {
		t.Errorf("build = %+v, want zero recipe when test cannot be derived", r.Build)
	}
	_ = r // caller renders "not configured"
}

func TestDeriveMissingRunCommand(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.27\n")

	f, err := Detect(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	f.Revision = "abc123"
	r, err := Derive(f)
	if !errors.Is(err, ErrNoRunCommand) {
		t.Fatalf("Derive err = %v, want ErrNoRunCommand", err)
	}
	if r.Build.ID != "build" {
		t.Errorf("build = %+v, want the build step preserved", r.Build)
	}
	if r.Run.ID != "" {
		t.Errorf("run = %+v, want unset so caller renders not configured", r.Run)
	}
}

func TestDeriveComposeRunWhenNoStartScript(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "package.json", `{"scripts":{"build":"vite build","test":"vitest run"}}`)
	writeFile(t, root, "docker-compose.yml", "services:\n  app:\n    build: .\n")

	f, err := Detect(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	f.Revision = "abc123"
	r, err := Derive(f)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if got := strings.Join(r.Run.Argv, " "); got != "docker compose up --build" {
		t.Errorf("run = %q, want docker compose up --build", got)
	}
}

func TestHashStableAcrossMapOrdering(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.27\n")
	writeFile(t, root, "go.sum", "")
	writeFile(t, root, "package.json", `{"scripts":{"build":"x","test":"y","start":"z","lint":"w"}}`)

	a := goOnlyFacts(root)
	a.PackageJSON = true
	a.PackageScripts = map[string]string{"build": "x", "test": "y", "start": "z", "lint": "w"}
	b := goOnlyFacts(root)
	b.PackageJSON = true
	b.PackageScripts = map[string]string{"lint": "w", "start": "z", "test": "y", "build": "x"}
	a.Revision = "abc123"
	ra, err := Derive(a)
	if err != nil {
		t.Fatalf("Derive a: %v", err)
	}
	b.Revision = "abc123"
	rb, err := Derive(b)
	if err != nil {
		t.Fatalf("Derive b: %v", err)
	}
	ha := mustHash(t, ra)
	hb := mustHash(t, rb)
	if ha != hb {
		t.Errorf("hash depends on map order: %s vs %s", ha, hb)
	}
	// A changed command changes the hash.
	rb.Test.Argv = []string{"go", "test", "-count=1", "./..."}
	hc := mustHash(t, rb)
	if hc == hb {
		t.Error("hash unchanged after test command change")
	}
}

func mustHash(t *testing.T, r Recipe) string {
	t.Helper()
	h, err := r.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	return h
}

func TestValidateRejects(t *testing.T) {
	base := func() Recipe {
		return Recipe{
			SchemaVersion: SchemaVersion, Revision: "abc",
			Build: Command{ID: "build", Argv: []string{"go", "build", "./..."}, TimeoutSeconds: 600},
			Test:  Command{ID: "test", Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 600},
		}
	}
	cases := []struct {
		name string
		mut  func(*Recipe)
		want string
	}{
		{"empty argv", func(r *Recipe) { r.Build.Argv = nil }, "empty argv"},
		{"timeout 0", func(r *Recipe) { r.Test.TimeoutSeconds = 0 }, "outside 1..3600"},
		{"timeout 3601", func(r *Recipe) { r.Test.TimeoutSeconds = 3601 }, "outside 1..3600"},
		{"run without a port", func(r *Recipe) {
			r.Run = Command{ID: "run", Argv: []string{"./app"}, TimeoutSeconds: 60}
			r.ReadinessProbe = []string{"tcp", "8080"}
		}, "port"},
		{"port 70000", func(r *Recipe) { r.RuntimePort = 70000 }, "port"},
		{"duplicate IDs", func(r *Recipe) { r.Run = r.Build }, "duplicate command ID"},
		{"env value", func(r *Recipe) { r.EnvNames = []string{"GITHUB_TOKEN=ghp_x"} }, "value"},
		{"command env value", func(r *Recipe) { r.Build.Env = []string{"KEY=secret"} }, "value"},
		{"traversal path", func(r *Recipe) { r.ArtifactPaths = []string{"../escape"} }, "escapes"},
		{"absolute path", func(r *Recipe) { r.Sources = []SourceRef{{Path: "/etc/passwd", SHA256: "x"}} }, "absolute"},
		{"bad schema", func(r *Recipe) { r.SchemaVersion = "v9" }, "schema version"},
		{"empty revision", func(r *Recipe) { r.Revision = "" }, "empty revision"},
		{"unsorted sources", func(r *Recipe) {
			r.Sources = []SourceRef{{Path: "b.md", SHA256: "x"}, {Path: "a.md", SHA256: "y"}}
		}, "not sorted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base()
			tc.mut(&r)
			err := r.Validate()
			if err == nil {
				t.Fatalf("Validate succeeded, want %q failure", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	// The base recipe itself must validate.
	if err := base().Validate(); err != nil {
		t.Errorf("base recipe should validate: %v", err)
	}
}

func TestValidateAcceptsSecretNameWithoutValue(t *testing.T) {
	r := Recipe{
		SchemaVersion: SchemaVersion, Revision: "abc",
		Build:    Command{ID: "build", Argv: []string{"make", "build"}, TimeoutSeconds: 10},
		Test:     Command{ID: "test", Argv: []string{"make", "test"}, TimeoutSeconds: 10},
		EnvNames: []string{"API_KEY", "GITHUB_TOKEN"},
	}
	if err := r.Validate(); err != nil {
		t.Errorf("names without values should validate: %v", err)
	}
}

func TestCheckFresh(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.27\n")
	writeFile(t, root, "go.sum", "lock-content-v1")
	writeFile(t, root, "scripts/gen.sh", "#!/bin/sh\ntrue\n")
	writeFile(t, root, "package.json", `{"scripts":{"start":"node server.js"}}`)

	f := goOnlyFacts(root)
	f.PackageJSON = true
	f.PackageScripts = map[string]string{"start": "node server.js"}
	f.Revision = "abc123"
	r, err := Derive(f)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	r.Sources = []SourceRef{{Path: "go.mod", SHA256: hashFile(t, root, "go.mod")}}
	r.Lockfiles = []SourceRef{{Path: "go.sum", SHA256: hashFile(t, root, "go.sum")}}
	r.Scripts = []Script{{Path: "scripts/gen.sh", SHA256: hashFile(t, root, "scripts/gen.sh")}}

	if err := r.CheckFresh(ctx, root); err != nil {
		t.Errorf("unchanged tree should be fresh: %v", err)
	}

	// Modified lockfile is stale.
	writeFile(t, root, "go.sum", "lock-content-v2")
	err = r.CheckFresh(ctx, root)
	if !errors.Is(err, ErrStale) || !strings.Contains(err.Error(), "go.sum") {
		t.Errorf("modified lockfile: err = %v, want ErrStale naming go.sum", err)
	}
	writeFile(t, root, "go.sum", "lock-content-v1")

	// Deleted script is stale.
	if err := os.Remove(filepath.Join(root, "scripts/gen.sh")); err != nil {
		t.Fatal(err)
	}
	err = r.CheckFresh(ctx, root)
	if !errors.Is(err, ErrStale) || !strings.Contains(err.Error(), "scripts/gen.sh") {
		t.Errorf("deleted script: err = %v, want ErrStale naming scripts/gen.sh", err)
	}

	// Clean again after restoring.
	writeFile(t, root, "scripts/gen.sh", "#!/bin/sh\ntrue\n")
	if err := r.CheckFresh(ctx, root); err != nil {
		t.Errorf("restored tree should be fresh: %v", err)
	}
}

func TestCheckFreshSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.27\n")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	r := Recipe{SchemaVersion: SchemaVersion, Sources: []SourceRef{{Path: "link/secret", SHA256: "x"}}}
	err := r.CheckFresh(context.Background(), root)
	if !errors.Is(err, ErrStale) {
		t.Errorf("symlink traversal: err = %v, want ErrStale", err)
	}
}

func TestDetectRejectsTraversalAndMissingDocs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.27\n")

	if _, err := Detect(context.Background(), root, []string{"../escape.md"}); err == nil {
		t.Error("Detect accepted a traversal doc path")
	}
	if _, err := Detect(context.Background(), root, []string{"/etc/hostname"}); err == nil {
		t.Error("Detect accepted an absolute doc path")
	}
	if _, err := Detect(context.Background(), root, []string{"docs/missing.md"}); err == nil {
		t.Error("Detect accepted a missing doc path")
	}
	if _, err := Detect(context.Background(), root, nil); err != nil {
		t.Errorf("Detect on plain tree: %v", err)
	}
}

func TestDeriveContradictoryFacts(t *testing.T) {
	// Package scripts present but no package.json: refuse to guess.
	f := Facts{
		Root: t.TempDir(), Revision: "abc",
		PackageScripts: map[string]string{"build": "vite build"},
	}
	if _, err := Derive(f); !errors.Is(err, ErrFacts) {
		t.Fatalf("Derive err = %v, want ErrFacts", err)
	}
}

func TestDeriveWebPackageScripts(t *testing.T) {
	// A web/ package.json with no root package.json uses --prefix.
	root := t.TempDir()
	writeFile(t, root, "web/package.json", `{"scripts":{"build":"vite build","test":"vitest run"}}`)
	writeFile(t, root, "web/package-lock.json", "{}")

	f, err := Detect(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	f.Revision = "abc123"
	r, err := Derive(f)
	if !errors.Is(err, ErrNoRunCommand) {
		t.Fatalf("Derive err = %v, want ErrNoRunCommand (no start script)", err)
	}
	if got := strings.Join(r.Build.Argv, " "); got != "npm run build" {
		t.Errorf("build = %q, want npm run build", got)
	}
	if r.Build.Dir != "web" {
		t.Errorf("build dir = %q, want web", r.Build.Dir)
	}
}
