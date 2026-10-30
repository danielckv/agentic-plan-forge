//go:build client && !server

package avatars

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

// deterministicPrefix marks critiques produced by the orchestrator's own
// structural checks rather than by the critic. The critic cannot use it.
const deterministicPrefix = "X-"

// Run executes the staged deliberation for one run and returns the converged,
// validated design for the synthesizer.
//
// Stage order inside a cycle: Technical Architect, then the Domain Specialists
// of the non-empty workstreams (at most min(MaxParallel, 4) provider calls at a
// time, an in-stage critic pass included), then the deterministic interface
// comparison and any decision cards, then a mandatory critic barrier over the
// complete current drafts. The Product Manager runs once per run, first, from
// BuildPMInput only. At most MaxCycles (capped at 3) automatic cycles run; a
// run that still has blocking issues after the last one pauses with
// *PausedError and never starts another automatic cycle.
//
// emit receives the public events; the session authority wraps them. decisions
// delivers resolved decision cards. Failures of one stage return *StageError
// and keep every finished draft in the checkpoint, so a retry re-runs only the
// failed stage. A cancelled ctx stops every provider call, waits for it to end
// and leaves the last valid checkpoint resumable.
func Run(ctx context.Context, in RunInput, gen contracts.Generator, emit func(contracts.PublicEvent) error, decisions <-chan contracts.Decision) (contracts.SynthesisInput, error) {
	if gen == nil {
		return contracts.SynthesisInput{}, errors.New("avatars: a generator is required")
	}
	if strings.TrimSpace(in.RunID) == "" {
		return contracts.SynthesisInput{}, errors.New("avatars: RunID is required")
	}
	o := newOrchestrator(in, gen, emit, decisions)
	return o.run(ctx)
}

func newOrchestrator(in RunInput, gen contracts.Generator, emit func(contracts.PublicEvent) error, decisions <-chan contracts.Decision) *orchestrator {
	store := in.Checkpoints
	if store == nil {
		store = defaultCheckpoints
	}
	par := in.parallel()
	return &orchestrator{
		in: in, gen: gen, sink: emit, decisions: decisions, store: store,
		maxC: in.maxCycles(), par: par, sem: make(chan struct{}, par),
		maxGapAgents: in.gapAgents(),
		specs:        map[string]synthesizer.SpecialistDoc{},
	}
}

func (o *orchestrator) run(ctx context.Context) (contracts.SynthesisInput, error) {
	if !o.in.NoNarrator {
		o.tell = newNarrator(o)
		o.tell.start(ctx)
		defer o.tell.close()
	}
	o.pmIn = BuildPMInput(o.in.PRD.Product)
	if err := o.restore(ctx); err != nil {
		return contracts.SynthesisInput{}, err
	}
	if o.st.Paused {
		// A paused run never gains a hidden extra cycle; the user starts a new
		// run or revises the baseline.
		return contracts.SynthesisInput{}, &PausedError{Cycle: o.st.CyclesDone, Blockers: pausedBlockers(o.st.Critiques)}
	}
	start := max(1, o.in.StartCycle, o.st.Cycle)
	start = min(start, o.maxC)
	for c := start; c <= o.maxC; c++ {
		converged, err := o.cycle(ctx, c)
		if err != nil {
			o.endRunOnFailure(ctx, c, err)
			return contracts.SynthesisInput{}, err
		}
		if !converged {
			continue
		}
		si := o.synthesisInput()
		if _, err := synthesizer.Validate(si); err != nil {
			verr := &StageError{Stage: "validation", Err: err}
			o.endRunOnFailure(ctx, c, verr)
			return contracts.SynthesisInput{}, verr
		}
		return si, nil
	}
	// Unreachable: the final cycle either converges or pauses.
	return contracts.SynthesisInput{}, &PausedError{Cycle: o.maxC, Blockers: pausedBlockers(o.st.Critiques)}
}

// endRunOnFailure closes a run that stopped on a failed stage: every avatar
// that never ran gets a terminal status, so no card waits for work that will
// not come. A canceled context and an emit failure are not stage failures and
// leave the board alone; the window already knows the session ended.
func (o *orchestrator) endRunOnFailure(ctx context.Context, cycle int, err error) {
	if ctx.Err() != nil {
		return
	}
	var stage *StageError
	if !errors.As(err, &stage) {
		return
	}
	o.cancelPending(ctx, cycle, "The run stopped: the "+stage.Stage+" stage failed. Retry it or revise the baseline.")
}

// restore loads a compatible checkpoint or starts a fresh state. A checkpoint
// made for another PRD revision, product content, graph or model slot is
// discarded: stale success is revalidated, never replayed.
func (o *orchestrator) restore(ctx context.Context) error {
	pmHash := sha256Hex(o.pmIn)
	if cp, fromDisk := o.store.load(o.in.RunID); cp != nil {
		if cp.compatible(o.in, pmHash) && o.adopt(cp) {
			if !fromDisk {
				// This process published these drafts already.
				return nil
			}
			return o.republish(ctx)
		}
		o.store.Forget(o.in.RunID)
		if err := o.thought(ctx, relay.StagePM, 1, statusStale,
			"The saved drafts no longer match the approved baseline, repository graph, project context or model slots; re-running from the start."); err != nil {
			return err
		}
	}
	st := newRunState()
	st.PRDRevision, st.GraphHash, st.ContextHash, st.PMHash = o.in.PRD.Revision, o.in.GraphHash, o.in.ContextHash, pmHash
	st.Main, st.Judge = o.in.Models.Main, o.in.Models.Judge
	o.st = st
	return nil
}

// adopt decodes a checkpoint's drafts into the working documents.
func (o *orchestrator) adopt(cp *runState) bool {
	var pm synthesizer.PMDoc
	var arch synthesizer.ArchitectDoc
	specs := map[string]synthesizer.SpecialistDoc{}
	var err error
	if cp.PM.present() {
		if pm, err = synthesizer.DecodePM(cp.PM.Raw); err != nil {
			return false
		}
	}
	if cp.Arch.present() {
		if arch, err = synthesizer.DecodeArchitect(cp.Arch.Raw); err != nil {
			return false
		}
	}
	for ws, rec := range cp.Specs {
		d, err := synthesizer.DecodeSpecialist(rec.Raw)
		if err != nil {
			return false
		}
		synthesizer.AlignSpecialistClaims(&d, arch)
		rec.Raw = mustJSON(d)
		cp.Specs[ws] = rec
		specs[ws] = d
	}
	o.st, o.pm, o.arch, o.specs = cp, pm, arch, specs
	return true
}

// republish sends the adopted drafts to the window. A resumed run does not
// re-run the stages it already paid for, so without this the board would show
// nothing for them: the epics, the architecture and the finished workstreams
// come back exactly as they were saved.
func (o *orchestrator) republish(ctx context.Context) error {
	cycle := max(o.st.Cycle, 1)
	type adopted struct {
		avatar string
		schema string
		rec    draftRec
	}
	list := []adopted{
		{relay.StagePM, synthesizer.SchemaPM, o.st.PM},
		{relay.StageArchitect, synthesizer.SchemaArchitect, o.st.Arch},
	}
	wss := make([]string, 0, len(o.st.Specs))
	for ws := range o.st.Specs {
		wss = append(wss, ws)
	}
	sort.Strings(wss)
	for _, ws := range wss {
		list = append(list, adopted{relay.StageSpecialistPrefix + ws, synthesizer.SchemaSpecialist, o.st.Specs[ws]})
	}
	for _, a := range list {
		if !a.rec.present() || a.rec.Stale {
			continue
		}
		if err := o.publishDraft(ctx, a.avatar, a.schema, cycle, a.rec.Raw, a.rec.Rev); err != nil {
			return err
		}
	}
	if len(o.st.Critiques) > 0 {
		if err := o.publishCritique(ctx, cycle, o.st.Critiques); err != nil {
			return err
		}
	}
	return nil
}

// cycle runs one full design/review pass and reports whether the run converged.
func (o *orchestrator) cycle(ctx context.Context, c int) (bool, error) {
	o.mu.Lock()
	o.st.Cycle = c
	o.mu.Unlock()
	o.save()
	if err := o.cycleStart(ctx, c); err != nil {
		return false, err
	}
	if !o.st.PM.present() {
		if err := o.runPM(ctx, c); err != nil {
			return false, err
		}
	}
	o.applyPlan(c)
	// The architect's diagrams and documentation batches run beside the
	// specialists: the specialists need the design part and nothing else. The
	// rest is always joined, including when the specialists fail, so no
	// provider call outlives the cycle that started it.
	var rest *archRest
	if !o.st.Arch.present() || o.st.Arch.Stale {
		var err error
		if rest, err = o.startArchitect(ctx, c); err != nil {
			return false, err
		}
	}
	specErr := o.runSpecialists(ctx, c)
	if rest != nil {
		// An architect failure outranks a specialist one: the specialists were
		// planning against a design that turned out not to assemble.
		if err := o.finishArchitect(ctx, c, rest); err != nil {
			return false, err
		}
	}
	// Admission runs after the architect's design part lands: the proposals
	// arrive inside it. Emitted before scheduling so the workspace shows the
	// agent before its first call.
	if specErr == nil {
		if err := o.admitAndRunGaps(ctx, c); err != nil {
			return false, err
		}
	}
	if specErr != nil {
		return false, specErr
	}
	if err := o.resolveConflicts(ctx, c); err != nil {
		return false, err
	}
	if err := o.criticBarrier(ctx, c); err != nil {
		return false, err
	}
	issues := o.critiqueSnapshot()
	if err := o.publishCritique(ctx, c, issues); err != nil {
		return false, err
	}
	o.mu.Lock()
	o.st.CyclesDone = c
	o.mu.Unlock()
	o.save()

	blockers, mediums := blockingOpen(issues), mediumOpen(issues)
	switch {
	case len(blockers) == 0 && len(mediums) == 0:
		return true, nil
	case len(blockers) == 0:
		// MEDIUM findings never blocked a plan, yet convergence used to demand
		// zero of them, so one non-blocking nit bought another full cycle:
		// re-drafting the architect and up to four specialists, roughly ten
		// provider calls, to settle something nobody was going to reject. Only
		// a blocker is worth a cycle now. The owner still chooses through
		// settleMediums, which offers the same revision it always did.
		return o.settleMediums(ctx, c, mediums)
	case c < o.maxC:
		return false, nil
	default:
		// Not o.pause any more. A capped run with blockers open could neither
		// cycle again nor synthesize, and the only way out discarded every
		// avatar's work — so the run ended with no plan. The owner now decides
		// once whether to take the plan with the findings recorded on it, and
		// choosing to pause still reaches o.pause from inside settleBlockers.
		return o.settleBlockers(ctx, c, blockers)
	}
}

// pause records the cap and reports the open blockers. The authority pauses the
// run itself on the critique that reached the cap.
func (o *orchestrator) pause(ctx context.Context, c int, blockers []contracts.Critique) error {
	o.mu.Lock()
	o.st.Paused = true
	o.mu.Unlock()
	o.save()
	_ = o.thought(ctx, relay.StageCritic, c, statusStale,
		fmt.Sprintf("Paused after cycle %d: %d blocking issue(s) remain open. Retry the stage or revise the baseline.", c, len(blockers)))
	return &PausedError{Cycle: c, Blockers: blockers}
}

func (o *orchestrator) critiqueSnapshot() []contracts.Critique {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]contracts.Critique{}, o.st.Critiques...)
}

// --- critique helpers -------------------------------------------------------------------------

func openCritiques(list []contracts.Critique) []contracts.Critique {
	var out []contracts.Critique
	for _, c := range list {
		if c.Status != contracts.CritiqueResolved {
			out = append(out, c)
		}
	}
	return out
}

func blockingOpen(list []contracts.Critique) []contracts.Critique {
	var out []contracts.Critique
	for _, c := range openCritiques(list) {
		if c.Severity == contracts.SeverityCritical || c.Severity == contracts.SeverityHigh {
			out = append(out, c)
		}
	}
	return out
}

func mediumOpen(list []contracts.Critique) []contracts.Critique {
	var out []contracts.Critique
	for _, c := range openCritiques(list) {
		if c.Severity == contracts.SeverityMedium {
			out = append(out, c)
		}
	}
	return out
}

// pausedBlockers lists what keeps a paused run from synthesis.
func pausedBlockers(list []contracts.Critique) []contracts.Critique {
	if b := blockingOpen(list); len(b) > 0 {
		return b
	}
	return mediumOpen(list)
}

// --- Product Manager --------------------------------------------------------------------------

func (o *orchestrator) runPM(ctx context.Context, c int) error {
	sp := stageSpec{
		avatar: relay.StagePM, role: contracts.RoleProductManager, slot: contracts.SlotMain,
		system: PMSystemPrompt(), schema: synthesizer.SchemaPM, cycle: c, status: statusThinking,
		summary: "Drafting epics and user stories from the approved product requirements.",
		step:    "epics and stories",
	}
	doc, raw, err := runStage(o, ctx, sp, fixedInput(o.pmIn), o.parsePM)
	if err != nil {
		return err
	}
	rev := o.nextRev(relay.StagePM)
	o.mu.Lock()
	o.pm = doc
	o.st.PM = draftRec{Raw: raw, Cycle: c, Rev: rev}
	o.mu.Unlock()
	o.save()
	return o.publishDraft(ctx, relay.StagePM, synthesizer.SchemaPM, c, raw, rev)
}

// --- revision planning ------------------------------------------------------------------------

// classifyLocked maps a critique target to the drafts it invalidates: the
// architect (its own draft, a contract or a documentation task), one
// workstream (itself or a task it owns), or every specialist (a story, which
// concerns the whole design). The caller holds o.mu.
func (o *orchestrator) classifyLocked(target string) (arch bool, ws []string) {
	if target == "architect" {
		return true, nil
	}
	for _, k := range o.arch.Contracts {
		if k.ID == target {
			return true, nil
		}
	}
	for _, t := range o.arch.DocTasks {
		if t.ID == target {
			return true, nil
		}
	}
	for _, w := range o.arch.Workstreams {
		if w.ID == target {
			return false, []string{w.ID}
		}
	}
	for id, s := range o.specs {
		for _, t := range s.Tasks {
			if t.ID == target {
				return false, []string{id}
			}
		}
	}
	for _, s := range o.pm.Stories {
		if s.ID == target {
			return false, synthesizer.SpecialistWorkstreams(o.arch)
		}
	}
	return false, synthesizer.SpecialistWorkstreams(o.arch)
}

// affectedLocked returns whether the architect must be revised and which
// workstreams the open critiques invalidate.
func (o *orchestrator) affectedLocked(open []contracts.Critique) (bool, map[string]bool) {
	rerunArch := false
	ws := map[string]bool{}
	for _, c := range open {
		for _, t := range c.TargetIDs {
			a, w := o.classifyLocked(t)
			rerunArch = rerunArch || a
			for _, id := range w {
				ws[id] = true
			}
		}
	}
	return rerunArch, ws
}

// applyPlan invalidates exactly the drafts the open critiques name. Unrelated
// validated drafts are kept. It runs once per cycle.
func (o *orchestrator) applyPlan(c int) {
	o.mu.Lock()
	if o.st.Planned >= c {
		o.mu.Unlock()
		return
	}
	o.st.Planned = c
	if open := openCritiques(o.st.Critiques); len(open) > 0 && o.st.Arch.present() {
		rerunArch, ws := o.affectedLocked(open)
		if rerunArch {
			a := o.st.Arch
			a.Stale = true
			o.st.Arch = a
		}
		for id := range ws {
			if rec, ok := o.st.Specs[id]; ok {
				rec.Stale = true
				o.st.Specs[id] = rec
			}
		}
	}
	o.mu.Unlock()
	o.save()
}

// requiredFor lists the open critiques that concern the given workstream (or,
// when ws is empty, the architect).
func (o *orchestrator) requiredFor(ws string) []contracts.Critique {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []contracts.Critique
	for _, c := range openCritiques(o.st.Critiques) {
		hit := false
		for _, t := range c.TargetIDs {
			a, w := o.classifyLocked(t)
			if ws == "" && a || ws != "" && containsString(w, ws) {
				hit = true
			}
		}
		if hit {
			out = append(out, c)
		}
	}
	return out
}

// --- Technical Architect ----------------------------------------------------------------------
//
// The stage itself runs in parts; see architect.go.

// invalidateByContractsLocked implements "re-run the architect only if shared
// contracts change, then the affected specialists": a specialist is stale when
// a contract it owns or consumes changed or its workstream definition changed,
// and a draft of a workstream the new design no longer has is dropped.
func (o *orchestrator) invalidateByContractsLocked(old, cur synthesizer.ArchitectDoc) {
	oldHash, newHash := map[string]string{}, map[string]string{}
	involved := map[string][]string{}
	note := func(k contracts.InterfaceContract) {
		involved[k.ID] = append(involved[k.ID], append([]string{k.OwnerWorkstream}, k.ConsumerWorkstreams...)...)
	}
	for _, k := range old.Contracts {
		oldHash[k.ID] = synthesizer.ContractHash(k)
		note(k)
	}
	for _, k := range cur.Contracts {
		newHash[k.ID] = synthesizer.ContractHash(k)
		note(k)
	}
	stale := map[string]bool{}
	for id, h := range newHash {
		if oldHash[id] != h {
			for _, ws := range involved[id] {
				stale[ws] = true
			}
		}
	}
	for id := range oldHash {
		if _, still := newHash[id]; !still {
			for _, ws := range involved[id] {
				stale[ws] = true
			}
		}
	}
	oldWS := map[string]string{}
	for _, w := range old.Workstreams {
		raw, _ := json.Marshal(w)
		oldWS[w.ID] = string(raw)
	}
	for _, w := range cur.Workstreams {
		raw, _ := json.Marshal(w)
		if prev, ok := oldWS[w.ID]; ok && prev != string(raw) {
			stale[w.ID] = true
		}
	}
	keep := map[string]bool{}
	for _, id := range synthesizer.SpecialistWorkstreams(cur) {
		keep[id] = true
	}
	for id, rec := range o.st.Specs {
		switch {
		case !keep[id]:
			delete(o.st.Specs, id)
			delete(o.specs, id)
		case stale[id]:
			rec.Stale = true
			o.st.Specs[id] = rec
		}
	}
}

// --- Domain Specialists -----------------------------------------------------------------------

// runSpecialists drafts every missing or stale workstream. At most
// min(MaxParallel, 4) provider calls run at once, counting the in-stage critic.
// A failed specialist stops new launches, lets in-flight calls finish so their
// drafts reach the checkpoint, and returns the first failure in stage order.
func (o *orchestrator) runSpecialists(ctx context.Context, c int) error {
	o.mu.Lock()
	need := synthesizer.SpecialistWorkstreams(o.arch)
	keep := map[string]bool{}
	for _, id := range need {
		keep[id] = true
	}
	for id := range o.st.Specs {
		if !keep[id] {
			delete(o.st.Specs, id)
			delete(o.specs, id)
		}
	}
	var run []string
	for _, id := range need {
		if rec, ok := o.st.Specs[id]; !ok || !rec.present() || rec.Stale {
			run = append(run, id)
		}
	}
	o.early = nil
	o.mu.Unlock()
	if len(run) == 0 {
		return nil
	}
	o.save()

	sem := o.sem
	errs := make([]error, len(run))
	var failed atomic.Bool
	var wg sync.WaitGroup

launch:
	for i, ws := range run {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break launch
		}
		if failed.Load() {
			<-sem
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := o.runSpecialist(ctx, c, ws, nil); err != nil {
				errs[i] = err
				failed.Store(true)
			}
		}()
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return err
	}
	for _, err := range errs {
		var emitErr *emitError
		if errors.As(err, &emitErr) {
			return err
		}
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// workstreamOf returns the declared workstream, if any.
func workstreamOf(a synthesizer.ArchitectDoc, id string) (contracts.Workstream, bool) {
	for _, w := range a.Workstreams {
		if w.ID == id {
			return w, true
		}
	}
	return contracts.Workstream{}, false
}

func (o *orchestrator) runSpecialist(ctx context.Context, c int, ws string, adopt *adoptInput) error {
	o.mu.Lock()
	prev := o.st.Specs[ws]
	arch, pm := o.arch, o.pm
	decisions := append([]contracts.Decision{}, o.st.Decisions...)
	o.mu.Unlock()

	w, ok := workstreamOf(arch, ws)
	if !ok {
		return &StageError{Stage: relay.StageSpecialistPrefix + ws, Err: fmt.Errorf("workstream %q is not declared by the architect", ws)}
	}
	var relevant []contracts.InterfaceContract
	for _, k := range arch.Contracts {
		if k.OwnerWorkstream == ws || containsString(k.ConsumerWorkstreams, ws) || containsString(w.ContractIDs, k.ID) {
			relevant = append(relevant, k)
		}
	}
	sp := stageSpec{
		avatar: relay.StageSpecialistPrefix + ws, role: contracts.RoleSpecialist, lane: ws, slot: contracts.SlotMain,
		system: SpecialistSystemPrompt(), schema: synthesizer.SchemaSpecialist, cycle: c, status: statusThinking,
		summary: fmt.Sprintf("Drafting concrete files, signatures and steps for workstream %s.", ws),
		step:    "workstream " + ws,
	}
	var rev *revisionInput
	if prev.present() {
		rev = &revisionInput{PreviousDraft: prev.Raw, RequiredChanges: o.requiredFor(ws)}
		sp.status = statusRevising
		sp.summary = fmt.Sprintf("Revising workstream %s for the open findings.", ws)
	}
	payload, err := fitPayload(sp.avatar, &specialistPayload{
		Cycle: c, WorkstreamID: ws, Workstream: w, Workstreams: arch.Workstreams, Summary: arch.Summary,
		Constraints: nonNil(arch.Constraints), Contracts: relevant, Epics: pm.Epics, Stories: pm.Stories,
		Graph: o.graphForWorkstream(w, pm.Stories), Context: o.contextForPrompt(), EvidenceNotes: o.evidenceNotes(),
		Decisions: decisions, Adopt: adopt, Revision: rev,
	})
	if err != nil {
		return o.failStage(ctx, sp, err)
	}
	doc, raw, err := runStage(o, ctx, sp, payload, o.parseSpecialist(ws))
	if err != nil {
		return err
	}
	n := o.nextRev(sp.avatar)
	o.mu.Lock()
	o.specs[ws] = doc
	o.st.Specs[ws] = draftRec{Raw: raw, Cycle: c, Rev: n}
	o.mu.Unlock()
	o.save()
	return o.publishDraft(ctx, sp.avatar, synthesizer.SchemaSpecialist, c, raw, n)
}

func (o *orchestrator) specSlice() []synthesizer.SpecialistDoc {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.specSliceLocked()
}

func (o *orchestrator) specSliceLocked() []synthesizer.SpecialistDoc {
	ids := make([]string, 0, len(o.specs))
	for id := range o.specs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]synthesizer.SpecialistDoc, 0, len(ids))
	for _, id := range ids {
		out = append(out, o.specs[id])
	}
	return out
}

// --- Red-Team Critic --------------------------------------------------------------------------

func (o *orchestrator) knownTargetsLocked() map[string]bool {
	known := map[string]bool{"architect": true}
	for _, w := range o.arch.Workstreams {
		known[w.ID] = true
	}
	for _, k := range o.arch.Contracts {
		known[k.ID] = true
	}
	for _, t := range o.arch.DocTasks {
		known[t.ID] = true
	}
	for _, s := range o.specs {
		for _, t := range s.Tasks {
			known[t.ID] = true
		}
	}
	for _, s := range o.pm.Stories {
		known[s.ID] = true
	}
	return known
}

// acceptedLocked maps critique IDs that a current draft records as an accepted
// tradeoff (with a rationale) to the avatar that accepted them.
func (o *orchestrator) acceptedLocked() map[string]string {
	out := map[string]string{}
	for _, a := range o.arch.Accepted {
		if strings.TrimSpace(a.Rationale) != "" {
			out[a.CritiqueID] = relay.StageArchitect
		}
	}
	for id, s := range o.specs {
		for _, a := range s.Accepted {
			if strings.TrimSpace(a.Rationale) != "" {
				out[a.CritiqueID] = relay.StageSpecialistPrefix + id
			}
		}
	}
	return out
}

func (o *orchestrator) criticSpec(c int, barrier bool) stageSpec {
	summary := "Attacking the complete current drafts for defects, races, security gaps and schema mismatches."
	step := "barrier review"
	if !barrier {
		summary = "Early review of the frozen architect draft and the specialist drafts completed so far."
		step = "early review"
	}
	return stageSpec{
		avatar: relay.StageCritic, role: contracts.RoleCritic, slot: contracts.SlotJudge,
		system: CriticSystemPrompt(), schema: synthesizer.SchemaCritic, cycle: c, status: statusCritiquing, summary: summary,
		step: step,
	}
}

// codeReviewerSpec reviews the assembled candidate's code claims. It runs on
// JUDGE beside the Red-Team Critic and cannot agree with the draft by
// construction.
func (o *orchestrator) codeReviewerSpec(c int) stageSpec {
	return stageSpec{
		avatar: relay.StageCodeReviewer, role: contracts.RoleCodeReviewer, slot: contracts.SlotJudge,
		system: CodeReviewerSystemPrompt(), schema: synthesizer.SchemaCodeReviewer, cycle: c, status: statusCritiquing,
		summary: "Reviewing the assembled candidate's code claims against the repository evidence.",
		step:    "code review",
	}
}

// techLeadSpec integrates the merged findings into the candidate and directs
// the one bounded repair pass over the sections the findings name.
func (o *orchestrator) techLeadSpec(c int) stageSpec {
	return stageSpec{
		avatar: relay.StageTechLead, role: contracts.RoleTechLead, slot: contracts.SlotMain,
		system: TechLeadSystemPrompt(), schema: synthesizer.SchemaTechLead, cycle: c, status: statusRevising,
		summary: "Integrating the review findings and directing the one repair pass.",
		step:    "integrate findings",
	}
}

// techLeadPayload is what the Technical Leader sees: the merged findings and
// a brief of the assembled candidate.
type techLeadPayload struct {
	Cycle     int                  `json:"cycle"`
	Findings  []contracts.Critique `json:"findings"`
	Candidate candidateBriefShape  `json:"candidate"`

	notes []string
}

func (p *techLeadPayload) trim() (string, bool) { return "", false }
func (p *techLeadPayload) addNote(n string)     { p.notes = append(p.notes, n) }

// candidateBriefShape names the plan sections the repair can touch.
type candidateBriefShape struct {
	Summary     string   `json:"summary"`
	Workstreams []string `json:"workstreams"`
	Contracts   []string `json:"contracts"`
	Tasks       []string `json:"tasks"`
}

func candidateBrief(si contracts.SynthesisInput) candidateBriefShape {
	var arch synthesizer.ArchitectDoc
	_ = json.Unmarshal(si.Architecture, &arch)
	out := candidateBriefShape{Summary: arch.Summary, Workstreams: []string{}, Contracts: []string{}, Tasks: []string{}}
	for _, w := range arch.Workstreams {
		out.Workstreams = append(out.Workstreams, w.ID)
	}
	for _, k := range arch.Contracts {
		out.Contracts = append(out.Contracts, k.ID)
	}
	for _, s := range oSpecs(si) {
		for _, t := range s.Tasks {
			out.Tasks = append(out.Tasks, t.ID)
		}
	}
	return out
}

// oSpecs decodes the specialist raw docs of a synthesis input.
func oSpecs(si contracts.SynthesisInput) []synthesizer.SpecialistDoc {
	out := make([]synthesizer.SpecialistDoc, 0, len(si.Specialists))
	for _, raw := range si.Specialists {
		var s synthesizer.SpecialistDoc
		if json.Unmarshal(raw, &s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// reviewspec builds the code reviewer's payload: the assembled candidate in
// brief plus the repository graph, so code claims can be checked.
func reviewspec(cycle int, knownTargets []string, o *orchestrator) *criticPayload {
	return &criticPayload{
		Cycle: cycle, Barrier: true, Architect: o.arch, Specialists: o.specSlice(), Stories: o.pm.Stories,
		Graph: o.graphForPrompt(), Context: o.contextForPrompt(), EvidenceNotes: o.evidenceNotes(),
		KnownTargets: knownTargets, Decisions: append([]contracts.Decision{}, o.st.Decisions...),
	}
}

// specFor returns the stage spec of one review avatar.
func (o *orchestrator) specFor(avatar string, cycle int) stageSpec {
	if avatar == relay.StageCodeReviewer {
		return o.codeReviewerSpec(cycle)
	}
	return o.criticSpec(cycle, true)
}

// parseFindings decodes either reviewer's output, validating critique ids
// against the known targets and the severities the shape allows.
func (o *orchestrator) parseFindings(avatar string) func(json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	return func(raw json.RawMessage) (json.RawMessage, json.RawMessage, error) {
		known := o.knownTargetsLocked()
		known["synthesis"] = true
		doc, norm, err := parseCritic(known)(raw)
		if err != nil {
			return nil, norm, err
		}
		return mustJSON(doc), norm, nil
	}
}

// parseTechLead validates the integration record: every finding gets exactly
// one resolution and a repair without a rationale is rejected.
func parseTechLead(findings []contracts.Critique) func(json.RawMessage) (synthesizer.TechLeadDoc, json.RawMessage, error) {
	return func(raw json.RawMessage) (synthesizer.TechLeadDoc, json.RawMessage, error) {
		d, err := synthesizer.DecodeTechLead(raw)
		if err != nil {
			return d, nil, err
		}
		byID := map[string]bool{}
		for _, f := range findings {
			byID[f.ID] = true
		}
		var problems []string
		seen := map[string]bool{}
		for _, r := range d.Resolutions {
			switch {
			case !byID[r.FindingID]:
				problems = append(problems, fmt.Sprintf("resolution names unknown finding %q", r.FindingID))
			case seen[r.FindingID]:
				problems = append(problems, fmt.Sprintf("duplicate resolution for finding %q", r.FindingID))
			case r.Blocks && len(r.Sections) == 0:
				problems = append(problems, fmt.Sprintf("blocking resolution for %q names the sections it repairs", r.FindingID))
			case !r.Blocks && strings.TrimSpace(r.Rationale) == "":
				problems = append(problems, fmt.Sprintf("non-blocking resolution for %q states a rationale", r.FindingID))
			}
			seen[r.FindingID] = true
		}
		for _, f := range findings {
			if !seen[f.ID] {
				problems = append(problems, fmt.Sprintf("finding %s has no resolution", f.ID))
			}
		}
		if len(problems) > 0 {
			return d, nil, &synthesizer.ValidationError{Stage: "tech_lead", Problems: problems}
		}
		return d, raw, nil
	}
}

// mustFit fits a payload or panics: the payload shapes here are built from
// already-validated material, so a fit failure is a programmer error.
func mustFit[T trimmable](p T) *stageInput {
	in, err := fitPayload("", p)
	if err != nil {
		panic(err)
	}
	return in
}

// critsOf decodes a stored reviewer doc back into its critiques.
func critsOf(raw json.RawMessage) []contracts.Critique {
	if len(raw) == 0 {
		return nil
	}
	d, err := synthesizer.DecodeCritic(raw)
	if err != nil {
		return nil
	}
	return d.Critiques
}

func detID(parts ...string) string {
	return deterministicPrefix + sha256Hex([]byte(strings.Join(parts, "\x00")))[:10]
}

// findings runs the deterministic checks over the current drafts and returns
// each structural defect as an open HIGH finding. A broken interface claim, a
// uncovered story, a dependency cycle or a shared-target conflict invalidates
// the affected draft exactly like a critic blocker. Valid interface
// alternatives are not defects: they become decision cards instead.
func (o *orchestrator) findings() []contracts.Critique {
	o.mu.Lock()
	arch, pm, specs := o.arch, o.pm, o.specSliceLocked()
	o.mu.Unlock()
	var out []contracts.Critique
	add := func(targets []string, evidence, change string) {
		sort.Strings(targets)
		out = append(out, contracts.Critique{
			ID: detID(append([]string{evidence}, targets...)...), Severity: contracts.SeverityHigh, TargetIDs: targets,
			Evidence: evidence, Impact: "Structural defect found by deterministic validation; the plan cannot compile while it stands.",
			RequiredChange: change, Status: contracts.CritiqueOpen,
		})
	}
	for _, is := range synthesizer.CompareInterfaces(arch, specs) {
		if is.Kind == synthesizer.IssueBroken {
			add(append([]string(nil), is.Workstreams...), "interface "+is.ContractID+": "+is.Detail,
				"Declare the claim against the frozen contract version hash with matching schemas and error cases.")
		}
	}
	for _, p := range synthesizer.PlanProblems(pm, arch, specs) {
		add(append([]string(nil), p.Targets...), p.Message, "Revise the affected draft so the defect no longer holds: "+p.Message)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// mergeCritiques builds the authoritative critique set. The critic's output
// replaces earlier critic findings, but a prior finding the critic omitted stays
// open: nothing is dropped silently. Deterministic findings are recomputed each
// barrier, and a prior one no longer reproduced is recorded as resolved. A
// MEDIUM finding a draft accepts with a rationale is recorded as accepted.
func mergeCritiques(prior, fresh, det []contracts.Critique, accepted map[string]string) []contracts.Critique {
	byID := map[string]contracts.Critique{}
	for _, c := range fresh {
		byID[c.ID] = c
	}
	detIDs := map[string]bool{}
	for _, c := range det {
		detIDs[c.ID] = true
	}
	for _, p := range prior {
		if strings.HasPrefix(p.ID, deterministicPrefix) {
			if !detIDs[p.ID] && p.Status != contracts.CritiqueResolved {
				p.Status = contracts.CritiqueResolved
				p.ResolutionRef = "revised"
			}
			if _, now := byID[p.ID]; !now && !detIDs[p.ID] {
				byID[p.ID] = p
			}
			continue
		}
		if _, ok := byID[p.ID]; !ok {
			byID[p.ID] = p
		}
	}
	for _, c := range det {
		byID[c.ID] = c
	}
	out := make([]contracts.Critique, 0, len(byID))
	for _, c := range byID {
		if c.Status == "" {
			c.Status = contracts.CritiqueOpen
		}
		if c.Status == contracts.CritiqueOpen && c.Severity == contracts.SeverityMedium {
			if who, ok := accepted[c.ID]; ok {
				c.Status = contracts.CritiqueResolved
				c.ResolutionRef = "accepted:" + who
			}
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// criticBarrier is the mandatory review of the complete current draft set. It
// runs whenever any draft or decision changed since the last review, so an
// earlier snapshot can never certify later drafts.
func (o *orchestrator) criticBarrier(ctx context.Context, c int) error {
	o.mu.Lock()
	hash := o.st.setHash()
	current := o.st.CriticHash == hash && o.st.CriticCycle == c
	o.mu.Unlock()
	if current {
		return nil
	}
	det := o.findings()

	o.mu.Lock()
	known := o.knownTargetsLocked()
	prior := append([]contracts.Critique{}, o.st.Critiques...)
	p := &criticPayload{
		Cycle: c, Barrier: true, Architect: o.arch, Specialists: o.specSliceLocked(), Stories: o.pm.Stories,
		Graph: o.graphForPrompt(), Context: o.contextForPrompt(), EvidenceNotes: o.evidenceNotes(),
		KnownTargets: sortedKeys(known), Prior: prior, Deterministic: det,
		Earlier: append([]contracts.Critique{}, o.early...), Decisions: append([]contracts.Decision{}, o.st.Decisions...),
	}
	o.mu.Unlock()

	sp := o.criticSpec(c, true)
	payload, err := fitPayload(sp.avatar, p)
	if err != nil {
		return o.failStage(ctx, sp, err)
	}
	doc, _, err := runStage(o, ctx, sp, payload, parseCritic(known))
	if err != nil {
		return err
	}
	o.mu.Lock()
	merged := mergeCritiques(prior, doc.Critiques, det, o.acceptedLocked())
	o.st.Critiques = merged
	o.st.CriticHash, o.st.CriticCycle = hash, c
	o.mu.Unlock()
	o.save()
	open, blocking := len(openCritiques(merged)), len(blockingOpen(merged))
	return o.thought(ctx, relay.StageCritic, c, statusSynced,
		fmt.Sprintf("Reviewed the complete current drafts: %d finding(s) open, %d blocking.", open, blocking))
}

// synthesisInput assembles the validated material for the synthesizer. A
// completed gap agent's deliverable joins the specialist documents, so the
// fused plan carries the expertise the architect asked for. Its lifecycle is
// recorded here: fused when the deliverable reached the synthesis input,
// retired when it did not, with the reason kept on the descriptor.
func (o *orchestrator) synthesisInput() contracts.SynthesisInput {
	o.mu.Lock()
	defer o.mu.Unlock()
	ids := make([]string, 0, len(o.st.Specs))
	for id := range o.st.Specs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	specs := make([]json.RawMessage, 0, len(ids)+len(o.st.GapDocs))
	for _, id := range ids {
		specs = append(specs, append(json.RawMessage(nil), o.st.Specs[id].Raw...))
	}
	gapIDs := make([]string, 0, len(o.st.GapDocs))
	for id := range o.st.GapDocs {
		rec := o.st.GapDocs[id]
		if !rec.present() {
			continue
		}
		gapIDs = append(gapIDs, id)
	}
	sort.Strings(gapIDs)
	for _, id := range gapIDs {
		specs = append(specs, append(json.RawMessage(nil), o.st.GapDocs[id].Raw...))
		o.setGapStatus(id, contracts.GapFused)
	}
	// An agent that completed but whose draft is gone, and any agent still
	// admitted or failed, is retired with the reason kept for the run history.
	for i := range o.st.Gaps {
		g := &o.st.Gaps[i]
		if g.Status == contracts.GapCompleted && g.Reason == "" {
			if rec, ok := o.st.GapDocs[g.ID]; !ok || !rec.present() {
				g.Status = contracts.GapRetired
				g.Reason = "deliverable was not validated in time for synthesis"
			}
		}
	}
	crit := append([]contracts.Critique(nil), o.st.Critiques...)
	sort.SliceStable(crit, func(i, j int) bool { return crit[i].ID < crit[j].ID })
	decs := append([]contracts.Decision(nil), o.st.Decisions...)
	sort.SliceStable(decs, func(i, j int) bool { return decs[i].ID < decs[j].ID })
	return contracts.SynthesisInput{
		PRD: o.in.PRD, PM: append(json.RawMessage(nil), o.st.PM.Raw...), Architecture: append(json.RawMessage(nil), o.st.Arch.Raw...),
		Specialists: specs, Critiques: crit, Decisions: decs, Graph: o.in.Graph, DocDrift: o.in.DocDrift,
	}
}
