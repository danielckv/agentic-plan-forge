//go:build client && !server

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/recipe"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
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
	Available bool   `json:"available"`
	Reused    bool   `json:"reused"`
	Stored    string `json:"stored,omitempty"`
	Problem   string `json:"problem,omitempty"`
	// Hash is the recipe's own digest, so a launch can pin the exact commands the
	// owner reviewed.
	Hash   string         `json:"hash,omitempty"`
	Recipe *recipe.Recipe `json:"recipe,omitempty"`
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
	if view.Available {
		if hash, herr := r.Hash(); herr == nil {
			view.Hash = hash
		}
	}
	if view.Available && !reused {
		if path, serr := StoreRecipe(s.repo, r); serr == nil {
			view.Stored = path
		}
	}
	return view
}

// bundleView is what a documents-only export would write, before anything is
// written. The window shows this list so the owner sees every path it would
// touch.
type BundleView struct {
	Available bool             `json:"available"`
	Problem   string           `json:"problem,omitempty"`
	Files     []bundleFileView `json:"files"`
}

type bundleFileView struct {
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

// DocumentBundle builds the plan, specification and decision records for an
// approved SynthesisInput without touching disk. It is the "documents only" path:
// it needs no sandbox, no provider key and no network.
func DocumentBundle(input contracts.SynthesisInput, meta contracts.ApprovalMeta, opts synthesizer.BundleOptions) (BundleView, error) {
	bundle, err := synthesizer.BuildBundle(input, meta, opts)
	if err != nil {
		return BundleView{Available: false, Problem: err.Error(), Files: []bundleFileView{}}, err
	}
	files := make([]bundleFileView, 0, len(bundle.Entries))
	for _, entry := range bundle.Entries {
		files = append(files, bundleFileView{Path: entry.Path, Kind: string(entry.Kind), Title: entry.Title})
	}
	return BundleView{Available: true, Files: files}, nil
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

// GET /api/documents — the plan, specification and decision records the export
// would write, and where. A project with nothing to export says why.
func (s *appSession) handleDocuments(w http.ResponseWriter, r *http.Request) {
	if s.exec == nil {
		writeJSON(w, http.StatusOK, BundleView{Available: false,
			Problem: "this session has no deliberation to export", Files: []bundleFileView{}})
		return
	}
	writeJSON(w, http.StatusOK, s.DocumentsView(r.Context()))
}

// DocumentsView is the approved plan as repository documents. It needs no
// provider and no sandbox: the documents-only path is the default, and a launch
// is an extra step the owner confirms separately.
func (s *appSession) DocumentsView(ctx context.Context) BundleView {
	if s.exec == nil {
		return BundleView{Available: false, Problem: "this session has no deliberation to export", Files: []bundleFileView{}}
	}
	input, meta, ok := s.exec.approvedInput()
	if !ok {
		return BundleView{Available: false,
			Problem: "Nothing to export yet: the plan has to be approved first.", Files: []bundleFileView{}}
	}
	view, err := DocumentBundle(input, meta, synthesizer.BundleOptions{NextADRNumber: s.nextADRNumber()})
	if err != nil {
		return BundleView{Available: false, Problem: err.Error(), Files: []bundleFileView{}}
	}
	return view
}

// nextADRNumber is one past the highest decision-record number the repository
// already uses, so an export never reuses or renumbers one.
func (s *appSession) nextADRNumber() int {
	entries, err := os.ReadDir(filepath.Join(s.repo, "docs", "adrs"))
	if err != nil {
		return 1
	}
	highest := 0
	for _, entry := range entries {
		name := entry.Name()
		if len(name) < 4 {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(name[:4], "%04d", &n); err == nil && n > highest {
			highest = n
		}
	}
	return highest + 1
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
