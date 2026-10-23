//go:build client && !server

package avatars

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

// The Technical Architect stage answers in parts. One call that produced the
// whole draft had to read the repository graph, the documentation drift and the
// project context, and then write a workstream DAG, the interface contracts,
// two diagrams and one documentation task per confirmed drift item. On a
// repository with real drift that call cannot finish inside
// contracts.MaxGenerateTimeout, and a run died at `architect stage failed:
// context deadline exceeded`.
//
// The parts are:
//
//  1. the design: the summary, the constraints, the workstreams and the
//     contracts. The specialists wait for this part and for nothing else.
//  2. the diagrams, drawn from the design alone: no graph, no drift.
//  3. the documentation tasks, one call per batch of drift items: no graph.
//
// Parts 2 and 3 do not depend on each other and run concurrently, bounded by
// the same MaxParallel limit as the specialists. Each part is checkpointed, so
// a retry re-runs the part that failed and keeps the parts that finished. The
// assembled draft is validated as a whole before it is published: nothing
// reaches the specialists, the critic or the synthesizer that the single call
// would not also have had to satisfy.
const (
	// docBatchItems bounds how many drift items one documentation-task call
	// answers. Each item costs one task of roughly 500 bytes of JSON.
	docBatchItems = 20
	// docBatchBytes bounds the drift evidence of one documentation-task call.
	docBatchBytes = 24 << 10
)

// docBatchRec is one finished batch of documentation tasks.
type docBatchRec struct {
	Draft draftRec
	// Prefix is the task id prefix the batch was told to use, which keeps task
	// ids unique across batches.
	Prefix string
	// Items are the drift item ids the batch answered.
	Items []string
}

// docBatch is one planned batch of documentation tasks.
type docBatch struct {
	index  int
	prefix string
	items  []contracts.DocDriftItem
}

func (b docBatch) itemIDs() []string {
	out := make([]string, 0, len(b.items))
	for _, it := range b.items {
		out = append(out, it.ID)
	}
	return out
}

// planDocBatches splits the confirmed drift items into batches. Items that
// could not be verified against the repository graph need no task and are left
// out; their count travels with every batch so the model knows they exist.
func planDocBatches(items []contracts.DocDriftItem) []docBatch {
	var confirmed []contracts.DocDriftItem
	for _, it := range items {
		if it.DriftType != contracts.DriftUnverifiable {
			confirmed = append(confirmed, it)
		}
	}
	var out []docBatch
	for len(confirmed) > 0 {
		n := min(docBatchItems, len(confirmed))
		for n > 1 && jsonSize(confirmed[:n]) > docBatchBytes {
			n--
		}
		out = append(out, docBatch{
			index:  len(out) + 1,
			prefix: fmt.Sprintf("T-DOC-%d-", len(out)+1),
			items:  confirmed[:n],
		})
		confirmed = confirmed[n:]
	}
	return out
}

// unverifiableCount counts the drift items that need no documentation task.
func unverifiableCount(items []contracts.DocDriftItem) int {
	n := 0
	for _, it := range items {
		if it.DriftType == contracts.DriftUnverifiable {
			n++
		}
	}
	return n
}

// --- payloads ---------------------------------------------------------------------------------

type architectDiagramPayload struct {
	Cycle       int                           `json:"cycle"`
	Summary     string                        `json:"architecture_summary"`
	Constraints []string                      `json:"architecture_constraints"`
	Workstreams []contracts.Workstream        `json:"workstreams"`
	Contracts   []contracts.InterfaceContract `json:"contracts"`
	Stories     []contracts.Story             `json:"stories"`
}

type architectDocsPayload struct {
	Cycle              int                       `json:"cycle"`
	Batch              int                       `json:"batch"`
	Batches            int                       `json:"batches"`
	IDPrefix           string                    `json:"id_prefix"`
	Summary            string                    `json:"architecture_summary"`
	Workstreams        []contracts.Workstream    `json:"workstreams"`
	DocDrift           []contracts.DocDriftItem  `json:"documentation_drift"`
	UnverifiableItems  int                       `json:"unverifiable_drift_items"`
	Context            *contracts.ProjectContext `json:"project_context,omitempty"`
	EvidenceNotes      []string                  `json:"evidence_notes,omitempty"`
	PreviousBatchTasks []string                  `json:"task_ids_already_used,omitempty"`
}

func (p *architectDocsPayload) addNote(note string) { p.EvidenceNotes = append(p.EvidenceNotes, note) }

// trim gives up the project context. The drift items are the batch's subject
// and are never cut: an item with no task fails the batch.
func (p *architectDocsPayload) trim() (string, bool) {
	if c, note, ok := trimContext(p.Context); ok {
		p.Context = c
		return note, true
	}
	return "", false
}

// --- the stage --------------------------------------------------------------------------------

func (o *orchestrator) architectSpec(c int, status, summary string) stageSpec {
	return stageSpec{
		avatar: relay.StageArchitect, role: contracts.RoleArchitect, slot: contracts.SlotMain,
		model: o.in.Models.Main, schema: synthesizer.SchemaArchitect, cycle: c,
		status: status, summary: summary,
	}
}

// runArchitect produces the architect draft of this cycle from its parts and
// publishes the assembled, validated result.
func (o *orchestrator) runArchitect(ctx context.Context, c int) error {
	o.mu.Lock()
	prev, oldArch := o.st.Arch, o.arch
	o.mu.Unlock()

	design, err := o.architectDesign(ctx, c, prev)
	if err != nil {
		return err
	}

	batches := o.planBatches()
	diagrams, docs, err := o.architectRest(ctx, c, design, batches)
	if err != nil {
		return err
	}

	merged := design
	merged.Diagrams = diagrams
	merged.DocTasks = docs
	doc, raw, perr := o.parseArchitect(mustJSON(merged))
	if perr != nil {
		// Every part passed its own validation, so a problem here is a
		// disagreement between parts, and no single part can be blamed for it.
		// The parts are dropped, so a retry asks for all of them again rather
		// than re-assembling the same draft and failing the same way.
		o.mu.Lock()
		o.st.ArchDesign, o.st.ArchDiagrams, o.st.ArchDocs = draftRec{}, draftRec{}, nil
		o.mu.Unlock()
		o.save()
		sp := o.architectSpec(c, statusThinking, "")
		return o.failStage(ctx, sp, fmt.Errorf("the architect parts do not fit together: %w", perr))
	}

	n := o.nextRev(relay.StageArchitect)
	o.mu.Lock()
	o.arch = doc
	o.st.Arch = draftRec{Raw: raw, Cycle: c, Rev: n}
	if prev.present() {
		o.invalidateByContractsLocked(oldArch, doc)
	}
	o.mu.Unlock()
	o.save()
	return o.publishDraft(ctx, relay.StageArchitect, synthesizer.SchemaArchitect, c, raw, n)
}

// architectDesign runs the design part, or adopts the checkpointed one when
// this cycle has not invalidated it.
func (o *orchestrator) architectDesign(ctx context.Context, c int, prev draftRec) (synthesizer.ArchitectDoc, error) {
	o.mu.Lock()
	kept := o.st.ArchDesign
	decisions := append([]contracts.Decision{}, o.st.Decisions...)
	o.mu.Unlock()
	if kept.present() && !prev.Stale {
		if doc, err := synthesizer.DecodeArchitect(kept.Raw); err == nil {
			return doc, nil
		}
	}

	sp := o.architectSpec(c, statusThinking,
		"Part 1 of 3: mapping the stories onto module boundaries, contracts and a workstream DAG.")
	sp.system = ArchitectDesignSystemPrompt()
	var rev *revisionInput
	if prev.present() {
		rev = &revisionInput{PreviousDraft: prev.Raw, RequiredChanges: o.requiredFor("")}
		sp.status = statusRevising
		sp.summary = "Part 1 of 3: revising the design for the open critiques; unaffected drafts are kept."
	}
	in, err := fitPayload(sp.avatar, &architectPayload{
		Cycle: c, PM: o.pm, TechnicalAppendix: o.in.PRD.TechnicalAppendix, Graph: o.graphForPrompt(),
		Context: o.contextForPrompt(), EvidenceNotes: o.evidenceNotes(), Decisions: decisions, Revision: rev,
	})
	if err != nil {
		return synthesizer.ArchitectDoc{}, o.failStage(ctx, sp, err)
	}
	doc, raw, err := runStage(o, ctx, sp, in, o.parseArchitectDesign)
	if err != nil {
		return synthesizer.ArchitectDoc{}, err
	}
	o.mu.Lock()
	o.st.ArchDesign = draftRec{Raw: raw, Cycle: c}
	// The kept diagrams drew the design this one replaces, so they are drawn
	// again. The documentation batches answer documentation drift, not the
	// design, and are kept unless a critique names one of their tasks.
	o.st.ArchDiagrams = draftRec{}
	o.mu.Unlock()
	o.save()
	return doc, nil
}

// planBatches drops the checkpointed batches an open critique names, and
// returns the batches of this cycle. A batch that still answers the same drift
// items and that no critique attacks is kept: the documentation it describes
// did not change because the design did.
func (o *orchestrator) planBatches() []docBatch {
	batches := planDocBatches(o.in.DocDrift)
	targeted := map[string]bool{}
	for _, cr := range o.requiredFor("") {
		for _, t := range cr.TargetIDs {
			targeted[t] = true
		}
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	kept := map[string]docBatchRec{}
	for _, rec := range o.st.ArchDocs {
		if !rec.Draft.present() {
			continue
		}
		tasks, err := docTasksOf(rec.Draft.Raw)
		if err != nil {
			continue
		}
		stale := false
		for _, t := range tasks {
			if targeted[t.ID] {
				stale = true
				break
			}
		}
		if !stale {
			kept[rec.Prefix] = rec
		}
	}
	fresh := make([]docBatchRec, 0, len(batches))
	for _, b := range batches {
		rec, ok := kept[b.prefix]
		if ok && equalStrings(rec.Items, b.itemIDs()) {
			fresh = append(fresh, rec)
			continue
		}
		fresh = append(fresh, docBatchRec{Prefix: b.prefix, Items: b.itemIDs()})
	}
	o.st.ArchDocs = fresh
	return batches
}

// architectRest runs the diagram part and the documentation batches that are
// still outstanding, concurrently, and returns the assembled parts.
func (o *orchestrator) architectRest(ctx context.Context, c int, design synthesizer.ArchitectDoc, batches []docBatch) (synthesizer.DiagramSet, []contracts.TechnicalTask, error) {
	o.mu.Lock()
	diagramsDone := o.st.ArchDiagrams
	recs := append([]docBatchRec(nil), o.st.ArchDocs...)
	o.mu.Unlock()

	var todo []int
	for i, b := range batches {
		if i < len(recs) && recs[i].Draft.present() && equalStrings(recs[i].Items, b.itemIDs()) {
			continue
		}
		todo = append(todo, i)
	}
	runDiagrams := !diagramsDone.present()

	sem := make(chan struct{}, o.par)
	errs := make([]error, len(batches)+1)
	results := make([]json.RawMessage, len(batches))
	var diagramRaw json.RawMessage
	var failed atomic.Bool
	var wg sync.WaitGroup

	launch := func(slot int, run func() (json.RawMessage, error)) bool {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return false
		}
		if failed.Load() {
			<-sem
			return false
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			raw, err := run()
			if err != nil {
				errs[slot] = err
				failed.Store(true)
				return
			}
			if slot == 0 {
				diagramRaw = raw
				return
			}
			results[slot-1] = raw
		}()
		return true
	}

	started := true
	if runDiagrams {
		started = launch(0, func() (json.RawMessage, error) { return o.architectDiagrams(ctx, c, design) })
	}
	for _, i := range todo {
		if !started {
			break
		}
		started = launch(i+1, func() (json.RawMessage, error) {
			return o.architectDocBatch(ctx, c, design, batches[i], len(batches))
		})
	}
	wg.Wait()

	// Every part that finished is checkpointed before any failure is reported,
	// so a retry pays for the part that failed and for nothing else.
	o.mu.Lock()
	if runDiagrams && len(diagramRaw) > 0 {
		o.st.ArchDiagrams = draftRec{Raw: diagramRaw, Cycle: c}
	}
	for _, i := range todo {
		if len(results[i]) == 0 {
			continue
		}
		o.st.ArchDocs[i] = docBatchRec{
			Draft: draftRec{Raw: results[i], Cycle: c}, Prefix: batches[i].prefix, Items: batches[i].itemIDs(),
		}
	}
	diagramsRaw, docRecs := o.st.ArchDiagrams.Raw, append([]docBatchRec(nil), o.st.ArchDocs...)
	o.mu.Unlock()
	o.save()

	if err := ctx.Err(); err != nil {
		return synthesizer.DiagramSet{}, nil, err
	}
	for _, err := range errs {
		if err != nil {
			return synthesizer.DiagramSet{}, nil, err
		}
	}

	diagrams, err := diagramsOf(diagramsRaw)
	if err != nil {
		return synthesizer.DiagramSet{}, nil, err
	}
	var tasks []contracts.TechnicalTask
	for _, rec := range docRecs {
		got, derr := docTasksOf(rec.Draft.Raw)
		if derr != nil {
			return synthesizer.DiagramSet{}, nil, derr
		}
		tasks = append(tasks, got...)
	}
	return diagrams, tasks, nil
}

// architectDiagrams runs the diagram part over the decided design.
func (o *orchestrator) architectDiagrams(ctx context.Context, c int, design synthesizer.ArchitectDoc) (json.RawMessage, error) {
	sp := o.architectSpec(c, statusDrafting, "Part 2 of 3: drawing the architecture and sequence diagrams of the design.")
	sp.system = ArchitectDiagramsSystemPrompt()
	o.mu.Lock()
	stories := o.pm.Stories
	o.mu.Unlock()
	payload := mustJSON(&architectDiagramPayload{
		Cycle: c, Summary: design.Summary, Constraints: design.Constraints,
		Workstreams: design.Workstreams, Contracts: design.Contracts, Stories: stories,
	})
	_, raw, err := runStage(o, ctx, sp, fixedInput(payload), parseArchitectDiagrams)
	return raw, err
}

// architectDocBatch runs one batch of documentation tasks.
func (o *orchestrator) architectDocBatch(ctx context.Context, c int, design synthesizer.ArchitectDoc, b docBatch, total int) (json.RawMessage, error) {
	sp := o.architectSpec(c, statusDrafting,
		fmt.Sprintf("Part 3 of 3: writing documentation tasks for %d stale documents (batch %d of %d).", len(b.items), b.index, total))
	sp.system = ArchitectDocTasksSystemPrompt()
	in, err := fitPayload(sp.avatar, &architectDocsPayload{
		Cycle: c, Batch: b.index, Batches: total, IDPrefix: b.prefix, Summary: design.Summary,
		Workstreams: design.Workstreams, DocDrift: b.items, UnverifiableItems: unverifiableCount(o.in.DocDrift),
		Context: o.contextForPrompt(), EvidenceNotes: o.evidenceNotes(),
	})
	if err != nil {
		return nil, o.failStage(ctx, sp, err)
	}
	_, raw, err := runStage(o, ctx, sp, in, o.parseArchitectDocTasks(b))
	return raw, err
}

// --- part parsing -----------------------------------------------------------------------------

// parseArchitectDesign validates the design part against the repository
// grounding. The diagrams and the documentation tasks belong to other parts and
// must be absent here.
func (o *orchestrator) parseArchitectDesign(raw json.RawMessage) (synthesizer.ArchitectDoc, json.RawMessage, error) {
	d, err := synthesizer.DecodeArchitect(raw)
	if err != nil {
		return d, nil, err
	}
	var problems []string
	if len(d.DocTasks) > 0 {
		problems = append(problems, "doc_tasks belong to the documentation part of this stage; leave them out")
	}
	if len(d.Diagrams.Architecture.Nodes) > 0 || len(d.Diagrams.Sequence.Participants) > 0 {
		problems = append(problems, "diagrams belong to the diagram part of this stage; leave them out")
	}
	synthesizer.NormalizeContracts(&d)
	if verr := d.ValidateDesign(o.designContext()); verr != nil {
		problems = append(problems, problemsOf(verr)...)
	}
	if len(problems) > 0 {
		return d, nil, &synthesizer.ValidationError{Stage: "architect", Problems: problems}
	}
	return d, mustJSON(d), nil
}

// parseArchitectDiagrams validates the diagram part on its own.
func parseArchitectDiagrams(raw json.RawMessage) (synthesizer.DiagramSet, json.RawMessage, error) {
	d, err := synthesizer.DecodeArchitect(raw)
	if err != nil {
		return d.Diagrams, nil, err
	}
	if verr := d.ValidateDiagrams(); verr != nil {
		return d.Diagrams, nil, verr
	}
	return d.Diagrams, mustJSON(synthesizer.ArchitectDoc{Diagrams: d.Diagrams}), nil
}

// parseArchitectDocTasks validates one batch of documentation tasks: every item
// of the batch is answered, and every task id carries the batch prefix, so two
// batches cannot produce the same id.
func (o *orchestrator) parseArchitectDocTasks(b docBatch) func(json.RawMessage) ([]contracts.TechnicalTask, json.RawMessage, error) {
	return func(raw json.RawMessage) ([]contracts.TechnicalTask, json.RawMessage, error) {
		d, err := synthesizer.DecodeArchitect(raw)
		if err != nil {
			return nil, nil, err
		}
		var problems []string
		for _, t := range d.DocTasks {
			if !strings.HasPrefix(t.ID, b.prefix) {
				problems = append(problems, fmt.Sprintf("task id %q must start with the id_prefix %q", t.ID, b.prefix))
			}
		}
		doc := synthesizer.ArchitectDoc{DocTasks: d.DocTasks}
		if verr := doc.ValidateDocTasks(o.designContext(), b.items); verr != nil {
			problems = append(problems, problemsOf(verr)...)
		}
		if len(problems) > 0 {
			return nil, nil, &synthesizer.ValidationError{Stage: "architect", Problems: problems}
		}
		return d.DocTasks, mustJSON(doc), nil
	}
}

// docTasksOf reads the documentation tasks back out of a checkpointed batch.
func docTasksOf(raw json.RawMessage) ([]contracts.TechnicalTask, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	d, err := synthesizer.DecodeArchitect(raw)
	if err != nil {
		return nil, err
	}
	return d.DocTasks, nil
}

// diagramsOf reads the diagrams back out of the checkpointed diagram part.
func diagramsOf(raw json.RawMessage) (synthesizer.DiagramSet, error) {
	if len(raw) == 0 {
		return synthesizer.DiagramSet{}, errors.New("the architect diagrams are missing")
	}
	d, err := synthesizer.DecodeArchitect(raw)
	if err != nil {
		return synthesizer.DiagramSet{}, err
	}
	return d.Diagrams, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
