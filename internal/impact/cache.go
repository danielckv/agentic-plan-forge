//go:build client && !server

package impact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// CachePath is the repository-relative location of the impact-map cache. It
// sits beside the parse cache inside the gitignored .plan directory.
const CachePath = "docs/plans/.plan/cache/impact.json"

const (
	cacheFileName = "impact.json"
	// cacheVersion invalidates the whole file when this shape changes.
	cacheVersion = 1
	// maxCacheBytes is the largest cache file read or written.
	maxCacheBytes = 4 << 20
	// maxCacheEntries is how many maps are kept, newest first.
	maxCacheEntries = 8
)

// cacheFile is the content of CachePath.
type cacheFile struct {
	Version int                   `json:"version"`
	Entries map[string]cacheEntry `json:"entries"`
}

// cacheEntry is one recorded map, with the inputs it was built from written
// out beside it. The key already covers them; recording them separately lets a
// stale entry be dropped positively rather than left to rot.
type cacheEntry struct {
	Key          string              `json:"key"`
	PRDHash      string              `json:"prd_hash"`
	WorktreeHash string              `json:"worktree_hash"`
	ContextHash  string              `json:"context_hash"`
	PacksDigest  string              `json:"packs_digest"`
	Prompt       int                 `json:"prompt_version"`
	Extractor    int                 `json:"extractor_version"`
	SchemaID     string              `json:"schema_id"`
	CreatedAt    time.Time           `json:"created_at"`
	Map          contracts.ImpactMap `json:"map"`
}

// Key is the content identity of one map pass.
//
// Six things decide whether a stored map still answers: the interpreted
// requirements, the working tree, the project notes, the assembled evidence,
// the prompt and the evidence assembly. Move any one and the key moves, so the
// map is rebuilt; move none and the pass costs nothing.
//
// The context hash is separate from the worktree hash on purpose. The scanner
// never reads .agents, so an edited lane note or STATE.md leaves the worktree
// hash untouched; hashing the gathered context is what notices it.
func Key(prdHash, worktreeHash, contextHash, packsDigest string) string {
	h := sha256.New()
	for _, part := range []string{
		"impact.v1", prdHash, worktreeHash, contextHash, packsDigest,
		fmt.Sprintf("prompt=%d", PromptVersion),
		fmt.Sprintf("extractor=%d", ExtractorVersion),
		MapSchemaID,
	} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// prdHash identifies one interpretation. Revision is zeroed first: two
// sessions that reach the same interpretation share one cached map, and a
// re-interpretation that changes nothing does not rebuild it.
func prdHash(n contracts.NormalizedPRD) string {
	n.Revision = 0
	return hashJSON(n)
}

// contextHash identifies the project notes, specs and decision records the
// pass was matched against.
func contextHash(c contracts.ProjectContext) string {
	return hashJSON(c)
}

func hashJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// load returns the map stored under key.
//
// It is deliberately strict: a missing file, an unreadable one, a file over
// maxCacheBytes, a version mismatch, a symlinked path component, or an entry
// whose recorded worktree hash no longer matches all yield no map rather than
// an error. A stale map is never shown as fresh; the pass simply runs again.
func load(root, key, worktree string) (contracts.ImpactMap, bool) {
	cf, ok := readCache(root)
	if !ok {
		return contracts.ImpactMap{}, false
	}
	e, ok := cf.Entries[key]
	switch {
	case !ok,
		e.Key != key,
		e.Prompt != PromptVersion,
		e.Extractor != ExtractorVersion,
		e.SchemaID != MapSchemaID,
		e.WorktreeHash != worktree:
		return contracts.ImpactMap{}, false
	}
	return e.Map, true
}

// readCache reads and validates the cache file. Every failure is a miss.
func readCache(root string) (cacheFile, bool) {
	dir, err := ast.CacheDir(root, false)
	if err != nil {
		return cacheFile{}, false
	}
	path := filepath.Join(dir, cacheFileName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxCacheBytes {
		return cacheFile{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return cacheFile{}, false
	}
	defer f.Close()
	var cf cacheFile
	if err := json.NewDecoder(io.LimitReader(f, maxCacheBytes)).Decode(&cf); err != nil {
		return cacheFile{}, false
	}
	if cf.Version != cacheVersion || cf.Entries == nil {
		return cacheFile{}, false
	}
	return cf, true
}

// store records e under its key, drops every entry built from another working
// tree, keeps the newest maxCacheEntries and replaces the file atomically.
//
// A failure here never fails a pass: the caller turns it into a note. A
// read-only checkout still gets its map, it just pays for it again next time.
func store(root string, e cacheEntry) error {
	dir, err := ast.CacheDir(root, true)
	if err != nil {
		return err
	}
	cf, ok := readCache(root)
	if !ok {
		cf = cacheFile{Version: cacheVersion, Entries: map[string]cacheEntry{}}
	}
	cf.Entries[e.Key] = e
	prune(&cf, e.WorktreeHash)

	raw, err := json.Marshal(&cf)
	if err != nil {
		return fmt.Errorf("cannot encode the impact cache")
	}
	if len(raw) > maxCacheBytes {
		// One oversized map would make every later read a miss. Keep only the
		// newest entry and try again; if that still does not fit, give up.
		cf.Entries = map[string]cacheEntry{e.Key: e}
		if raw, err = json.Marshal(&cf); err != nil || len(raw) > maxCacheBytes {
			return fmt.Errorf("the impact map is too large to cache")
		}
	}
	return writeAtomic(dir, cacheFileName, raw)
}

// prune drops entries from another working tree, then the oldest, keeping at
// most maxCacheEntries.
func prune(cf *cacheFile, worktree string) {
	for k, e := range cf.Entries {
		if e.WorktreeHash != worktree {
			delete(cf.Entries, k)
		}
	}
	if len(cf.Entries) <= maxCacheEntries {
		return
	}
	keys := make([]string, 0, len(cf.Entries))
	for k := range cf.Entries {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := cf.Entries[keys[i]].CreatedAt, cf.Entries[keys[j]].CreatedAt
		if a.Equal(b) {
			return keys[i] < keys[j]
		}
		return a.After(b)
	})
	for _, k := range keys[maxCacheEntries:] {
		delete(cf.Entries, k)
	}
}

// writeAtomic replaces name inside dir through a temporary file in the same
// directory, so a reader never sees a half-written cache.
func writeAtomic(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, name+"-*.tmp")
	if err != nil {
		return fmt.Errorf("cannot create a temporary cache file")
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cannot write the cache file")
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cannot replace the cache file")
	}
	return nil
}
