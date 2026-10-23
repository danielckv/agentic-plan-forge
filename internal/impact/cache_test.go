//go:build client && !server

package impact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

func entry(t *testing.T, root string) cacheEntry {
	t.Helper()
	return cacheEntry{
		Key:          Key("prd", "wt", "ctx", "packs"),
		PRDHash:      "prd",
		WorktreeHash: "wt",
		ContextHash:  "ctx",
		PacksDigest:  "packs",
		Prompt:       PromptVersion,
		Extractor:    ExtractorVersion,
		SchemaID:     MapSchemaID,
		CreatedAt:    time.Now().UTC(),
		Map: contracts.ImpactMap{
			SchemaVersion: contracts.SchemaVersion, State: contracts.ImpactReady,
			Summary: "This work touches 1 part of the project: 1 change.",
			Whole:   []contracts.ImpactLink{}, Epics: []contracts.ImpactEpic{}, Conflicts: []contracts.ImpactLink{},
		},
	}
}

// TestKeyMovesWithEveryInputAndNothingElse is the gate on the cache: six
// things decide whether a stored map still answers, and each must be able to
// invalidate it on its own.
func TestKeyMovesWithEveryInputAndNothingElse(t *testing.T) {
	base := Key("prd", "wt", "ctx", "packs")
	for name, got := range map[string]string{
		"the interpreted requirements": Key("prd2", "wt", "ctx", "packs"),
		"the working tree":             Key("prd", "wt2", "ctx", "packs"),
		"the project notes":            Key("prd", "wt", "ctx2", "packs"),
		"the assembled evidence":       Key("prd", "wt", "ctx", "packs2"),
	} {
		if got == base {
			t.Errorf("%s changed and the key did not", name)
		}
	}
	if Key("prd", "wt", "ctx", "packs") != base {
		t.Error("the key is not stable for the same inputs")
	}
	// The prompt and the extractor are compile-time constants, so they are
	// checked where they are used: a recorded entry from another version is a
	// miss. See TestLoadRefusesAnEntryFromAnotherVersion.
}

func TestPRDHashIgnoresTheRevision(t *testing.T) {
	a := contracts.NormalizedPRD{Revision: 3, Product: contracts.ProductPRD{Title: "Billing export"}}
	b := a
	b.Revision = 9
	if prdHash(a) != prdHash(b) {
		t.Error("two sessions reaching the same interpretation must share one cached map")
	}
	b.Product.Title = "Something else"
	if prdHash(a) == prdHash(b) {
		t.Error("a different interpretation must not reuse the map")
	}
}

func TestStoreAndLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	e := entry(t, root)
	if err := store(root, e); err != nil {
		t.Fatalf("store: %v", err)
	}

	got, ok := load(root, e.Key, "wt")
	if !ok {
		t.Fatal("a map stored moments ago did not load")
	}
	if got.Summary != e.Map.Summary || got.State != contracts.ImpactReady {
		t.Errorf("loaded map = %+v", got)
	}

	if _, ok := load(root, e.Key, "other-worktree"); ok {
		t.Error("a map built from another working tree was served as fresh")
	}
	if _, ok := load(root, "no-such-key", "wt"); ok {
		t.Error("an unknown key hit")
	}
}

func TestStoreCreatesTheGitignoreAndLeavesNoTempFile(t *testing.T) {
	root := t.TempDir()
	if err := store(root, entry(t, root)); err != nil {
		t.Fatalf("store: %v", err)
	}

	ignore := filepath.Join(root, "docs", "plans", ".plan", ".gitignore")
	raw, err := os.ReadFile(ignore)
	if err != nil {
		t.Fatalf("the cache was written without the .gitignore that keeps it out of commits: %v", err)
	}
	if !strings.HasPrefix(string(raw), "*\n") {
		t.Errorf(".gitignore = %q", raw)
	}

	dir := filepath.Join(root, "docs", "plans", ".plan", "cache")
	names, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if strings.HasSuffix(n.Name(), ".tmp") {
			t.Errorf("a temporary file was left behind: %s", n.Name())
		}
	}
}

func TestLoadRefusesAnEntryFromAnotherVersion(t *testing.T) {
	root := t.TempDir()
	e := entry(t, root)
	e.Prompt = PromptVersion + 1
	if err := store(root, e); err != nil {
		t.Fatal(err)
	}
	if _, ok := load(root, e.Key, "wt"); ok {
		t.Error("a map from another prompt version was served")
	}
}

func TestReadCacheRefusesAHostileOrBrokenFile(t *testing.T) {
	for name, write := range map[string]func(t *testing.T, root string){
		"no cache at all": func(*testing.T, string) {},
		"unknown version": func(t *testing.T, root string) {
			writeCacheFile(t, root, []byte(`{"version":99,"entries":{}}`))
		},
		"not JSON": func(t *testing.T, root string) {
			writeCacheFile(t, root, []byte("not json at all"))
		},
		"over the size cap": func(t *testing.T, root string) {
			writeCacheFile(t, root, []byte(`{"version":1,"entries":{},"pad":"`+strings.Repeat("x", maxCacheBytes)+`"}`))
		},
		"a symlinked cache directory": func(t *testing.T, root string) {
			plan := filepath.Join(root, "docs", "plans", ".plan")
			if err := os.MkdirAll(plan, 0o755); err != nil {
				t.Fatal(err)
			}
			elsewhere := t.TempDir()
			if err := os.Symlink(elsewhere, filepath.Join(plan, "cache")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root)
			if _, ok := readCache(root); ok {
				t.Error("a cache that cannot be trusted was read")
			}
		})
	}
}

func writeCacheFile(t *testing.T, root string, data []byte) {
	t.Helper()
	dir := filepath.Join(root, "docs", "plans", ".plan", "cache")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, cacheFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStoreKeepsOnlyTheNewestEntries(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	for i := 0; i < maxCacheEntries+4; i++ {
		e := entry(t, root)
		e.Key = Key("prd"+strconv.Itoa(i), "wt", "ctx", "packs")
		e.CreatedAt = now.Add(time.Duration(i) * time.Minute)
		if err := store(root, e); err != nil {
			t.Fatalf("store %d: %v", i, err)
		}
	}
	cf, ok := readCache(root)
	if !ok {
		t.Fatal("the cache did not read back")
	}
	if len(cf.Entries) != maxCacheEntries {
		t.Errorf("entries = %d, want the %d cap", len(cf.Entries), maxCacheEntries)
	}
	oldest := Key("prd0", "wt", "ctx", "packs")
	if _, present := cf.Entries[oldest]; present {
		t.Error("the oldest entry survived the cap")
	}
}

func TestStoreDropsEntriesFromAnotherWorkingTree(t *testing.T) {
	root := t.TempDir()
	old := entry(t, root)
	if err := store(root, old); err != nil {
		t.Fatal(err)
	}

	moved := entry(t, root)
	moved.WorktreeHash = "wt-after-an-edit"
	moved.Key = Key("prd", "wt-after-an-edit", "ctx", "packs")
	if err := store(root, moved); err != nil {
		t.Fatal(err)
	}

	cf, _ := readCache(root)
	if _, present := cf.Entries[old.Key]; present {
		t.Error("an entry from the previous working tree was kept; dead entries would accumulate")
	}
	if len(cf.Entries) != 1 {
		t.Errorf("entries = %d, want only the current working tree's", len(cf.Entries))
	}
}

func TestCachePathMatchesWhereStoreWrites(t *testing.T) {
	root := t.TempDir()
	if err := store(root, entry(t, root)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(CachePath))); err != nil {
		t.Errorf("CachePath is %q but store wrote elsewhere: %v", CachePath, err)
	}
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(CachePath)))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Error("the cache file is not valid JSON")
	}
}
