//go:build client && !server

package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/danielckv/agentic-plan-forge/internal/recipe"
)

// The execution recipe is the project's own build knowledge, captured once.
//
// An approved plan says what to build but not how this repository is built. A
// prepared sandbox must run reviewed commands, not a model's guess in the
// moment, so the recipe is derived from the repository's real assets (its
// manifests, lockfiles, CI and scripts), stored beside the approved plan, and
// reused on the next run while every hashed input is unchanged.

// recipeFileName is where the approved recipe lives, inside the plan directory.
const recipeFileName = "execution_recipe.json"

// maxRecipeBytes bounds the stored recipe.
const maxRecipeBytes = 1 << 20

// RepositoryRecipe derives the recipe for one repository revision. reused is
// true when an unchanged prior recipe was read back instead of re-deriving.
// ErrNoRunCommand is returned alongside a usable recipe: build and test exist,
// run does not, and the caller shows that instead of inventing a preview.
func RepositoryRecipe(ctx context.Context, repo, revision string, docs []string) (recipe.Recipe, bool, error) {
	if cached, ok := LoadStoredRecipe(repo); ok && cached.Revision == revision {
		if err := cached.CheckFresh(ctx, repo); err == nil {
			return cached, true, nil
		}
		// A changed input invalidates the record; it is re-derived below.
	}
	facts, err := recipe.Detect(ctx, repo, docs)
	if err != nil {
		return recipe.Recipe{}, false, err
	}
	facts.Revision = revision
	r, err := recipe.Derive(facts)
	if err != nil && !errors.Is(err, recipe.ErrNoRunCommand) {
		return recipe.Recipe{}, false, err
	}
	if err == nil {
		if verr := r.Validate(); verr != nil {
			return recipe.Recipe{}, false, verr
		}
	}
	return r, false, err
}

// StoreRecipe writes the recipe beside the approved plan artifacts, so a later
// run reuses it without re-deriving the commands.
func StoreRecipe(repo string, r recipe.Recipe) (string, error) {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	if len(raw) > maxRecipeBytes {
		return "", errors.New("execution recipe is larger than its bound")
	}
	dir := filepath.Join(repo, filepath.FromSlash(".plan"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, recipeFileName)
	tmp, err := os.CreateTemp(dir, ".execution_recipe-*.json")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(name, path); err != nil {
		return "", err
	}
	return path, nil
}

// LoadStoredRecipe reads the approved recipe, if one was stored. A malformed
// file is reported as absent: a recipe that cannot be read is re-derived, never
// trusted.
func LoadStoredRecipe(repo string) (recipe.Recipe, bool) {
	path := filepath.Join(repo, filepath.FromSlash(".plan"), recipeFileName)
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > maxRecipeBytes {
		return recipe.Recipe{}, false
	}
	var r recipe.Recipe
	if err := json.Unmarshal(raw, &r); err != nil {
		return recipe.Recipe{}, false
	}
	if r.SchemaVersion != recipe.SchemaVersion {
		return recipe.Recipe{}, false
	}
	return r, true
}

// RecipeView is what the implementation page shows: the commands, where each
// one came from, and whether the project has a step at all.
type RecipeView struct {
	Available bool           `json:"available"`
	Reused    bool           `json:"reused"`
	Stored    string         `json:"stored,omitempty"`
	Problem   string         `json:"problem,omitempty"`
	Recipe    *recipe.Recipe `json:"recipe,omitempty"`
}

// CurrentRecipe returns the recipe state for the window.
func (s *appSession) CurrentRecipe(ctx context.Context) RecipeView {
	r, reused, err := RepositoryRecipe(ctx, s.repo, s.revision(), nil)
	view := RecipeView{Available: err == nil, Reused: reused, Recipe: &r}
	switch {
	case errors.Is(err, recipe.ErrNoBuildCommand):
		view.Problem = "This project has no build command Plan Forge can find. Add one before a sandbox runs."
	case errors.Is(err, recipe.ErrNoTestCommand):
		view.Problem = "This project has no test command Plan Forge can find. Tests are not configured, not passed."
	case errors.Is(err, recipe.ErrNoRunCommand):
		// Build and test exist; run does not. That is a real state, not a fault.
		view.Problem = "This project has no run command, so a preview cannot start. Build and test are recorded."
		view.Available = true
	case err != nil:
		view.Problem = err.Error()
	}
	if view.Available && !reused {
		if path, serr := StoreRecipe(s.repo, r); serr == nil {
			view.Stored = path
		}
	}
	return view
}

// GET /api/recipe — the build, test and run commands Plan Forge would execute
// in a prepared sandbox, and where each came from. A project with no command
// reports that instead of a fabricated pass.
func (s *appSession) handleRecipe(w http.ResponseWriter, r *http.Request) {
	if s.repo == "" {
		writeJSON(w, http.StatusOK, RecipeView{Problem: "this session has no repository"})
		return
	}
	writeJSON(w, http.StatusOK, s.CurrentRecipe(r.Context()))
}

// revision is the revision the recipe is derived for. A changed tree therefore
// invalidates the stored recipe instead of silently reusing it.
func (s *appSession) revision() string {
	if s.exec == nil {
		return ""
	}
	s.exec.mu.Lock()
	defer s.exec.mu.Unlock()
	if s.exec.graph == nil {
		return ""
	}
	return s.exec.graph.GitCommitHash
}
