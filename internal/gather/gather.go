//go:build client && !server

// Package gather collects what a repository already says about itself before
// a plan starts: excerpts from its project notes, specs, plans and decision
// records, and where the branch stands. It is deterministic and read-only. It
// never calls a model, never writes inside the repository and never re-indexes.
//
// Sources, in order:
//
//  1. The project-memory recall index, a SQLite FTS5 database at
//     <git-common-dir>/agents-local/recall.db, written by the `agents` CLI.
//  2. The legacy index at .agents/index/recall.db.
//  3. A direct read of the files those indexes cover: .agents/STATE.md,
//     .agents/lanes, .agents/sessions, docs/specs, docs/plans, docs/adrs and
//     specs.
//
// The first source that holds rows for this working tree answers. Git state
// (branch, HEAD, newest commit subjects) is added whatever the source.
package gather

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

const (
	// DefaultBudget bounds the marshalled ProjectContext, in bytes.
	DefaultBudget = 64 << 10
	// DefaultMaxHits bounds the number of excerpts.
	DefaultMaxHits = 12
	// DefaultTimeout bounds one Collect call.
	DefaultTimeout = 5 * time.Second

	// chunkChars is the size of one excerpt, matching the recall index.
	chunkChars = 1200
	// maxQueryTerms is how many terms an FTS5 query carries.
	maxQueryTerms = 12
	// maxScanTerms is how many terms the direct file read scores against.
	maxScanTerms = 40
	// maxRecent bounds the commit subjects reported.
	maxRecent = 20
)

// Options configure one Collect call.
type Options struct {
	// Repo is the absolute repository root.
	Repo string
	// Text is the requirements text. Query terms come from it; with no terms
	// there are no excerpts, only Git state.
	Text string
	// Budget bounds the marshalled result in bytes. Zero selects DefaultBudget.
	Budget int
	// MaxHits bounds the excerpts. Zero selects DefaultMaxHits.
	MaxHits int
}

// Collect gathers the project context. It returns an error only for an empty
// Repo; every source failure becomes a note and the next source is tried.
func Collect(ctx context.Context, opts Options) (contracts.ProjectContext, error) {
	out := contracts.ProjectContext{Source: contracts.ContextSourceNone, Hits: []contracts.ContextHit{}}
	if strings.TrimSpace(opts.Repo) == "" {
		return out, os.ErrInvalid
	}
	if opts.Budget <= 0 {
		opts.Budget = DefaultBudget
	}
	if opts.MaxHits <= 0 {
		opts.MaxHits = DefaultMaxHits
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	commonDir := ""
	if g, err := ast.NewGit(opts.Repo); err == nil {
		if b, ok := g.Branch(ctx); ok {
			out.Branch = b
		}
		if h, ok := g.Head(ctx); ok {
			out.Head = h[:12]
		}
		if subjects, err := g.RecentSubjects(ctx); err == nil {
			// A repeated subject (an automated sync commit) says nothing new.
			seen := map[string]bool{}
			for _, s := range subjects {
				if !seen[s] {
					seen[s] = true
					out.Recent = append(out.Recent, s)
				}
			}
		}
		if dir, err := g.CommonDir(ctx); err == nil {
			commonDir = dir
		}
	}

	terms := QueryTerms(opts.Text, maxScanTerms)
	if len(terms) > 0 {
		hits, source, notes := search(ctx, opts.Repo, commonDir, terms, opts.MaxHits)
		out.Source, out.Hits = source, hits
		out.Notes = append(out.Notes, notes...)
	} else {
		out.Notes = append(out.Notes, "no requirements text to match yet")
	}
	scrub(&out, opts.Repo)
	fitBudget(&out, opts.Budget)
	return out, nil
}

// search tries each source in order and returns the first that holds rows for
// this working tree.
func search(ctx context.Context, repo, commonDir string, terms []string, limit int) ([]contracts.ContextHit, string, []string) {
	var notes []string
	worktree := filepath.Base(repo)
	if commonDir != "" {
		db := filepath.Join(commonDir, "agents-local", "recall.db")
		hits, ok, err := queryRecall(ctx, db, worktree, terms, limit)
		switch {
		case err != nil:
			notes = append(notes, "recall index unreadable: "+err.Error())
		case ok:
			return hits, contracts.ContextSourceRecall, notes
		}
	}
	legacy := filepath.Join(repo, ".agents", "index", "recall.db")
	hits, ok, err := queryRecall(ctx, legacy, "", terms, limit)
	switch {
	case err != nil:
		notes = append(notes, "legacy recall index unreadable: "+err.Error())
	case ok:
		return hits, contracts.ContextSourceLegacyRecall, notes
	}
	hits, found := scanFiles(ctx, repo, terms, limit)
	if !found {
		return []contracts.ContextHit{}, contracts.ContextSourceNone, notes
	}
	return hits, contracts.ContextSourceFiles, notes
}

// QueryTerms derives search terms from free text: the words ast.KeywordsFromText
// keeps (four or more letters or digits, no stop words), ordered by how often
// they occur, then alphabetically, at most limit.
func QueryTerms(text string, limit int) []string {
	keywords := ast.KeywordsFromText(text)
	if len(keywords) == 0 {
		return nil
	}
	keep := make(map[string]bool, len(keywords))
	for _, k := range keywords {
		keep[k] = true
	}
	counts := map[string]int{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if keep[w] {
			counts[w]++
		}
	}
	sort.SliceStable(keywords, func(i, j int) bool {
		if counts[keywords[i]] != counts[keywords[j]] {
			return counts[keywords[i]] > counts[keywords[j]]
		}
		return keywords[i] < keywords[j]
	})
	if len(keywords) > limit {
		keywords = keywords[:limit]
	}
	return keywords
}

// maxPerPath bounds the excerpts taken from one file, so one long document
// cannot crowd out every other source.
const maxPerPath = 3

// rankHits orders hits best first, with a stable tie-break, keeps at most
// maxPerPath per file, and keeps limit.
func rankHits(hits []contracts.ContextHit, limit int) []contracts.ContextHit {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Path != hits[j].Path {
			return hits[i].Path < hits[j].Path
		}
		return hits[i].Text < hits[j].Text
	})
	perPath := map[string]int{}
	out := make([]contracts.ContextHit, 0, min(len(hits), limit))
	for _, h := range hits {
		if len(out) == limit {
			break
		}
		if perPath[h.Path] == maxPerPath {
			continue
		}
		perPath[h.Path]++
		out = append(out, h)
	}
	return out
}

// safeRel reports whether p is a clean repository-relative path.
func safeRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || filepath.IsAbs(p) || strings.Contains(p, "\\") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "" {
			return false
		}
	}
	return true
}

// headingOf returns the first Markdown heading line of an excerpt, without its
// leading hashes, or "".
func headingOf(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
	}
	return ""
}

// clip bounds an excerpt to chunkChars runes.
func clip(text string) string {
	text = strings.TrimSpace(text)
	if r := []rune(text); len(r) > chunkChars {
		return string(r[:chunkChars]) + "…"
	}
	return text
}

// scrub rewrites the absolute repository root and home directory inside the
// excerpts, so no machine path reaches a model or a relay.
func scrub(c *contracts.ProjectContext, repo string) {
	var pairs []string
	if repo != "" {
		pairs = append(pairs, repo+string(filepath.Separator), "", repo, ".")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && home != "/" {
		pairs = append(pairs, home, "~")
	}
	if len(pairs) == 0 {
		return
	}
	r := strings.NewReplacer(pairs...)
	for i := range c.Hits {
		c.Hits[i].Text = r.Replace(c.Hits[i].Text)
		c.Hits[i].Heading = r.Replace(c.Hits[i].Heading)
	}
	for i := range c.Recent {
		c.Recent[i] = r.Replace(c.Recent[i])
	}
	for i := range c.Notes {
		c.Notes[i] = r.Replace(c.Notes[i])
	}
}

// fitBudget drops the lowest-ranked excerpts, then the oldest commit subjects,
// until the marshalled context fits budget.
func fitBudget(c *contracts.ProjectContext, budget int) {
	size := func() int {
		raw, err := json.Marshal(c)
		if err != nil {
			return budget + 1
		}
		return len(raw)
	}
	for size() > budget && len(c.Hits) > 0 {
		c.Hits = c.Hits[:len(c.Hits)-1]
		c.Truncated = true
	}
	for size() > budget && len(c.Recent) > 0 {
		c.Recent = c.Recent[:len(c.Recent)-1]
		c.Truncated = true
	}
	if len(c.Recent) > maxRecent {
		c.Recent = c.Recent[:maxRecent]
	}
}
