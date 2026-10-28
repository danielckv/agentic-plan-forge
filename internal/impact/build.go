//go:build client && !server

package impact

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Build returns the impact map for one interpretation.
//
// It assembles a bounded evidence pack per epic, asks the MAIN model slot to
// judge the candidates in batches, and resolves the answer back against the
// packs: an entry naming an identifier that was not offered is dropped, so no
// path in the result was authored by a model.
//
// Build never returns a blank map. No generator, a model error, an unparsable
// reply or an exhausted deadline all return the lexical map with State set and
// the cause in Notes, alongside the error for the caller to log. The window
// shows what is true and says which it is; it is never left with a hole.
func Build(ctx context.Context, in Input) (contracts.ImpactMap, error) {
	fallback := Lexical(in)
	if err := ctx.Err(); err != nil {
		return fallback, err
	}

	packs, notes := buildPacks(ctx, in)
	if err := ctx.Err(); err != nil {
		return fallback, err
	}

	key := Key(prdHash(in.PRD), fallback.WorktreeHash, contextHash(in.Context), packsDigest(packs))
	if in.ReadCache {
		if m, ok := load(in.Repo, key, fallback.WorktreeHash); ok {
			m.Revision = fallback.Revision
			m.PRDRevision = in.PRD.Revision
			return m, nil
		}
	}
	if in.Gen == nil {
		fallback.Notes = append(fallback.Notes, "no model host is available, so the links are keyword matches")
		return fallback, nil
	}

	m, err := runBatches(ctx, in, packs, fallback)
	if err != nil {
		return fallback, err
	}
	m.Notes = append(notes, m.Notes...)
	finish(&m)

	if in.WriteCache {
		if serr := store(in.Repo, cacheEntry{
			Key: key, PRDHash: prdHash(in.PRD), WorktreeHash: fallback.WorktreeHash,
			ContextHash: contextHash(in.Context), PacksDigest: packsDigest(packs),
			Prompt: PromptVersion, Extractor: ExtractorVersion, SchemaID: MapSchemaID,
			CreatedAt: in.now().UTC(), Map: m,
		}); serr != nil {
			// A read-only checkout still gets its map; it just pays again next
			// time. The user is told rather than left wondering.
			m.Notes = append(m.Notes, "the map was not cached: "+serr.Error())
		}
	}
	return m, nil
}

// maxBatchParallel bounds how many batch calls are open at once. Batches are
// independent — each covers its own epics — so they run together rather than
// one after another, which is what made a wide repository's map the slowest
// stage. The bound matches config.DefaultMaxParallel.
const maxBatchParallel = 4

// batchResult is one batch's answer, kept at the batch's own index so the
// assembled map does not depend on which call finished first.
type batchResult struct {
	links map[string][]contracts.ImpactLink
	notes []string
	err   error
}

// runBatches calls the model once per batch and assembles the result. One
// batch that cannot be answered costs its own epics their model links and
// nothing more.
//
// The calls run concurrently and are folded back in batch order, so the notes
// and the links read the same whatever order the hosts answer in.
func runBatches(ctx context.Context, in Input, packs []pack, fallback contracts.ImpactMap) (contracts.ImpactMap, error) {
	batches := batchPacks(packs, MaxEvidenceChars)
	results := make([]batchResult, len(batches))
	sem := make(chan struct{}, maxBatchParallel)
	var wg sync.WaitGroup

	for i, b := range batches {
		wg.Add(1)
		go func(i int, b []pack) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			got, bnotes, err := callWithRetries(ctx, in, b, i+1, len(batches))
			results[i] = batchResult{links: got, notes: bnotes, err: err}
		}(i, b)
	}
	wg.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return contracts.ImpactMap{}, ctxErr
	}

	links := map[string][]contracts.ImpactLink{}
	var notes []string
	answered := map[string]bool{}

	for i, b := range batches {
		notes = append(notes, results[i].notes...)
		if results[i].err != nil {
			notes = append(notes, batchNote(b, results[i].err))
			continue
		}
		for _, p := range b {
			answered[p.EpicID] = true
		}
		for epicID, ls := range results[i].links {
			links[epicID] = append(links[epicID], ls...)
		}
	}

	if len(answered) == 0 {
		fallback.Notes = append(fallback.Notes, notes...)
		return fallback, nil
	}
	return assemble(in, packs, links, answered, fallback, notes), nil
}

// callWithRetries answers one batch, degrading the way ADR 0004 established
// for normalization: a batch that outran the call timeout is halved and both
// halves retried; a single pack that outran it is shrunk once; a pack that
// still outruns it is given up on, and its epic keeps its keyword links.
func callWithRetries(ctx context.Context, in Input, batch []pack, index, count int) (map[string][]contracts.ImpactLink, []string, error) {
	w, err := callBatch(ctx, in.Gen, batch, index, count)
	if err == nil {
		links, notes := resolve(batch, w)
		return links, notes, nil
	}
	if !overranCallTimeout(ctx, err) {
		return nil, nil, err
	}

	if halves, ok := halveBatch(batch); ok {
		links := map[string][]contracts.ImpactLink{}
		var notes []string
		for _, half := range halves {
			got, hnotes, herr := callWithRetries(ctx, in, half, index, count)
			notes = append(notes, hnotes...)
			if herr != nil {
				return nil, notes, herr
			}
			for epicID, ls := range got {
				links[epicID] = append(links[epicID], ls...)
			}
		}
		return links, notes, nil
	}

	smaller, ok := shrinkPack(batch[0])
	if !ok {
		return nil, nil, err
	}
	w, err = callBatch(ctx, in.Gen, []pack{smaller}, index, count)
	if err != nil {
		return nil, nil, err
	}
	links, notes := resolve([]pack{smaller}, w)
	notes = append(notes, fmt.Sprintf("%q was mapped against half its candidates: the full list outran the call timeout", packName(batch[0])))
	return links, notes, nil
}

// assemble builds the published map: model links for every epic a batch
// answered, keyword links for the rest.
func assemble(in Input, packs []pack, links map[string][]contracts.ImpactLink, answered map[string]bool, fallback contracts.ImpactMap, notes []string) contracts.ImpactMap {
	m := newMap(in, contracts.ImpactReady)
	m.Notes = notes

	for _, p := range packs {
		got := capLinks(links[p.EpicID], MaxCodeLinksPerEpic+MaxDocLinksPerEpic)
		if !answered[p.EpicID] {
			got = lexicalOf(fallback, p.EpicID)
		}
		if p.EpicID == "" {
			m.Whole = capLinks(got, MaxWholeLinks)
			continue
		}
		m.Epics = append(m.Epics, contracts.ImpactEpic{EpicID: p.EpicID, Title: p.Title, Links: got})
	}
	// An epic the evidence pass cut keeps its keyword links rather than
	// vanishing from the rail.
	for _, e := range fallback.Epics {
		if !hasEpic(m, e.EpicID) {
			m.Epics = append(m.Epics, e)
		}
	}
	return m
}

func hasEpic(m contracts.ImpactMap, id string) bool {
	for _, e := range m.Epics {
		if e.EpicID == id {
			return true
		}
	}
	return false
}

// lexicalOf returns the keyword links the fallback map holds for one epic.
func lexicalOf(fallback contracts.ImpactMap, epicID string) []contracts.ImpactLink {
	if epicID == "" {
		return fallback.Whole
	}
	for _, e := range fallback.Epics {
		if e.EpicID == epicID {
			return e.Links
		}
	}
	return []contracts.ImpactLink{}
}

// capLinks bounds one epic's links so a generous reply cannot fill the rail.
// It keeps the order the model returned: it ranked them.
func capLinks(links []contracts.ImpactLink, limit int) []contracts.ImpactLink {
	if links == nil {
		return []contracts.ImpactLink{}
	}
	if len(links) <= limit {
		return links
	}
	return links[:limit:limit]
}

// overranCallTimeout reports a provider call that hit its own deadline, as
// opposed to a caller who cancelled the run. A cancelled run must never be
// mistaken for a size problem and retried.
func overranCallTimeout(ctx context.Context, err error) bool {
	return ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded)
}

func packName(p pack) string {
	if p.Title != "" {
		return p.Title
	}
	if p.EpicID != "" {
		return p.EpicID
	}
	return "the requirements as a whole"
}

func batchNote(batch []pack, err error) string {
	names := make([]string, 0, len(batch))
	for _, p := range batch {
		names = append(names, packName(p))
	}
	return fmt.Sprintf("%v kept keyword matches: the model could not read the project for them (%v)", names, err)
}
