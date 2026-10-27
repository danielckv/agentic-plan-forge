//go:build client && !server

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/recipe"
)

func writeTestFile(t *testing.T, repo, rel, body string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The recipe is derived from the repository's real assets. A project with a Go
// module and web scripts must get the commands it already uses, not invented
// ones.
func TestRepositoryRecipeUsesTheProjectsOwnCommands(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "go.mod", "module example.com/demo\n\ngo 1.27\n")
	writeTestFile(t, repo, "web/package.json", `{"scripts":{"build":"vite build","test":"vitest run","start":"vite preview"}}`)

	r, reused, err := RepositoryRecipe(context.Background(), repo, "rev-1", nil)
	if err != nil && !errors.Is(err, recipe.ErrNoRunCommand) {
		t.Fatalf("RepositoryRecipe: %v", err)
	}
	if reused {
		t.Error("a first derivation reported a reuse")
	}
	if len(r.Build.Argv) == 0 || len(r.Test.Argv) == 0 {
		t.Fatalf("recipe has no build or test command: %+v", r)
	}
	if r.Revision != "rev-1" {
		t.Errorf("revision = %q, want the one asked for", r.Revision)
	}
}

// A project with no build assets reports that, rather than a passing check.
func TestRecipeNeverInventsACommand(t *testing.T) {
	_, err := RepositoryRecipe(context.Background(), t.TempDir(), "r", nil)
	if err == nil {
		t.Fatal("a project with no build assets produced a recipe")
	}
	if !errors.Is(err, recipe.ErrNoBuildCommand) && !errors.Is(err, recipe.ErrFacts) {
		t.Errorf("error = %v, want a not-configured sentinel", err)
	}
}

// A recipe that cannot be parsed is ignored, never trusted: the next run
// re-derives instead of running commands nobody reviewed.
func TestStoredRecipeMalformedIsIgnored(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "docs/plans/.plan/execution_recipe.json", "{not json")
	if _, ok := LoadStoredRecipe(repo); ok {
		t.Fatal("a malformed recipe was adopted")
	}
}

// A recipe is reused only while its hashed inputs are unchanged. A changed
// lockfile invalidates it, so a green receipt cannot outlive the code.
func TestStoredRecipeIsReusedThenGoesStale(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "go.mod", "module example.com/demo\n\ngo 1.27\n")
	writeTestFile(t, repo, "go.sum", "example.com/x v1.0.0 h1:abc\n")

	r, _, err := RepositoryRecipe(context.Background(), repo, "rev-1", nil)
	if err != nil && !errors.Is(err, recipe.ErrNoRunCommand) {
		t.Fatalf("RepositoryRecipe: %v", err)
	}
	if _, err := StoreRecipe(repo, r); err != nil {
		t.Fatalf("StoreRecipe: %v", err)
	}

	cached, ok := LoadStoredRecipe(repo)
	if !ok {
		t.Fatal("the stored recipe is not readable")
	}
	if err := cached.CheckFresh(context.Background(), repo); err != nil {
		t.Fatalf("an unchanged tree reported a stale recipe: %v", err)
	}

	writeTestFile(t, repo, "go.sum", "example.com/x v1.0.1 h1:def\n")
	if err := cached.CheckFresh(context.Background(), repo); !errors.Is(err, recipe.ErrStale) {
		t.Fatalf("a changed lockfile reported %v, want ErrStale", err)
	}

	_, reused, err := RepositoryRecipe(context.Background(), repo, "rev-1", nil)
	if reused {
		t.Error("a stale recipe was reused")
	}
	if err != nil && !errors.Is(err, recipe.ErrNoRunCommand) {
		t.Errorf("re-derivation failed: %v", err)
	}
}

// The stored recipe is private: it names the commands a sandbox will run.
func TestStoredRecipeIsOwnerOnly(t *testing.T) {
	repo := t.TempDir()
	writeTestFile(t, repo, "go.mod", "module example.com/demo\n\ngo 1.27\n")
	r, _, err := RepositoryRecipe(context.Background(), repo, "rev-1", nil)
	if err != nil && !errors.Is(err, recipe.ErrNoRunCommand) {
		t.Fatalf("RepositoryRecipe: %v", err)
	}
	path, err := StoreRecipe(repo, r)
	if err != nil {
		t.Fatalf("StoreRecipe: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}
