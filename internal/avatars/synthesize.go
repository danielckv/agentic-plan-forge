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

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

// errBox lets atomic.Value hold errors of different concrete types.
type errBox struct{ err error }

type taskBrief struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	StoryID string `json:"story_id,omitempty"`
}

type specialistBrief struct {
	WorkstreamID string      `json:"workstream_id"`
	Summary      string      `json:"summary"`
	Tasks        []taskBrief `json:"tasks"`
}

type synthesisSchedule struct {
	HoursPerDay float64 `json:"hours_per_day"`
	MaxParallel int     `json:"max_parallel"`
}

// synthesisPayload is everything the Lead Synthesizer sees: the surviving
// validated design in brief, the resolved decisions and the coverage notes. It
// carries no draft the compiler would not also compile.
type synthesisPayload struct {
	Product       contracts.ProductPRD          `json:"product"`
	Epics         []contracts.Epic              `json:"epics"`
	Stories       []contracts.Story             `json:"stories"`
	Summary       string                        `json:"architecture_summary"`
	Constraints   []string                      `json:"architecture_constraints"`
	Workstreams   []contracts.Workstream        `json:"workstreams"`
	Contracts     []contracts.InterfaceContract `json:"contracts"`
	DocTasks      []taskBrief                   `json:"documentation_tasks"`
	Specialists   []specialistBrief             `json:"specialists"`
	Critiques     []contracts.Critique          `json:"critiques"`
	Decisions     []contracts.Decision          `json:"resolved_decisions"`
	Coverage      contracts.Coverage            `json:"coverage"`
	DocDriftIDs   []string                      `json:"documentation_drift_ids"`
	Schedule      synthesisSchedule             `json:"schedule"`
	AcceptedCount int                           `json:"accepted_tradeoffs"`
}

func briefs(tasks []contracts.TechnicalTask) []taskBrief {
	out := make([]taskBrief, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskBrief{ID: t.ID, Title: t.Title, StoryID: t.StoryID})
	}
	return out
}

// Synthesize runs the Lead Synthesizer stage over a converged design. It
// receives only current validated artifacts and resolved decisions, and its
// strict output can lock narrative, constraints, caveats and schedule
// configuration only: the document has no task or contract fields, so it cannot
// invent work independently of the surviving designs. The returned input
// carries the lock inside the architecture document and has passed the
// compiler's full validation (traceability, dependencies, targets, coverage).
func Synthesize(ctx context.Context, in RunInput, si contracts.SynthesisInput, gen contracts.Generator, emit func(contracts.PublicEvent) error) (contracts.SynthesisInput, error) {
	if gen == nil {
		return contracts.SynthesisInput{}, errors.New("avatars: a generator is required")
	}
	if strings.TrimSpace(in.RunID) == "" {
		return contracts.SynthesisInput{}, errors.New("avatars: RunID is required")
	}
	o := newOrchestrator(in, gen, emit, nil)
	o.st = newRunState()
	cycle := max(1, o.store.Cycle(in.RunID))
	sp := stageSpec{
		avatar: relay.StageSynthesizer, role: contracts.RoleSynthesizer, slot: contracts.SlotMain,
		system: SynthesizerSystemPrompt(), schema: synthesizer.SchemaSynthesizer, cycle: cycle, status: statusThinking,
		summary: "Locking the surviving design and drafting the executive narrative.",
		step:    "master spec",
	}

	docs, err := synthesizer.Validate(si)
	if err != nil {
		return contracts.SynthesisInput{}, o.failStage(ctx, sp, err)
	}
	payload := mustJSON(o.synthesisPayload(si, docs))
	doc, _, err := runStage(o, ctx, sp, fixedInput(payload), parseSynthesisDoc)
	if err != nil {
		return contracts.SynthesisInput{}, err
	}
	// Schedule configuration is frozen into the run, never taken from the model.
	doc.HoursPerDay, doc.MaxParallel, doc.StartDate = in.HoursPerDay, in.parallel(), ""

	arch := docs.Architect
	arch.Synthesis = &doc
	out := si
	out.Architecture = mustJSON(arch)
	if _, err := synthesizer.Validate(out); err != nil {
		return contracts.SynthesisInput{}, o.failStage(ctx, sp, err)
	}
	if err := o.thought(ctx, relay.StageSynthesizer, cycle, statusSynced, "Design locked; ready to compile the specification."); err != nil {
		return contracts.SynthesisInput{}, err
	}
	out, err = o.reviewCandidate(ctx, cycle, out)
	if err != nil {
		return contracts.SynthesisInput{}, err
	}
	// The review stage can leave a blocking finding open: one bounded repair is
	// all it gets, and a repair that does not close the finding must fail the
	// stage rather than publish a plan whose own gates refuse it. The reasons
	// come from the readiness verdict, so the message names what is missing.
	if rd := synthesizer.ReadinessOf(out); rd.Blocked() {
		return contracts.SynthesisInput{}, o.failStage(ctx, sp, &synthesizer.ValidationError{
			Stage: "synthesis", Problems: rd.Blockers,
		})
	} else if err := o.thought(ctx, relay.StageSynthesizer, cycle, statusSynced,
		fmt.Sprintf("Candidate published with verdict %s: %d waived, %d integrated, %d repaired finding(s).",
			rd.Verdict, len(rd.Waived), len(rd.Integrated), len(rd.Repaired))); err != nil {
		return contracts.SynthesisInput{}, err
	}
	return out, nil
}

// reviewCandidate runs the post-synthesis review pass over the assembled
// candidate. The Code Reviewer and the Red-Team Critic judge the same
// immutable candidate concurrently, both on JUDGE; the Technical Leader then
// integrates the merged findings and directs one bounded repair pass over the
// sections they name. One pass, never a cycle: a finding the pass does not fix
// stays open and the plan publishes as provisional.
func (o *orchestrator) reviewCandidate(ctx context.Context, cycle int, si contracts.SynthesisInput) (contracts.SynthesisInput, error) {
	// The one-shot Synthesize path builds this orchestrator from the input
	// alone: decode the candidate back into the fields the payload builders
	// and the target validation read.
	o.mu.Lock()
	var pm synthesizer.PMDoc
	if err := json.Unmarshal(si.PM, &pm); err != nil {
		o.mu.Unlock()
		return si, &StageError{Stage: relay.StageCodeReviewer, Err: fmt.Errorf("candidate PM doc: %w", err)}
	}
	var arch synthesizer.ArchitectDoc
	if err := json.Unmarshal(si.Architecture, &arch); err != nil {
		o.mu.Unlock()
		return si, &StageError{Stage: relay.StageCodeReviewer, Err: fmt.Errorf("candidate architecture: %w", err)}
	}
	o.pm, o.arch = pm, arch
	for _, raw := range si.Specialists {
		var s synthesizer.SpecialistDoc
		if err := json.Unmarshal(raw, &s); err == nil && s.WorkstreamID != "" {
			o.specs[s.WorkstreamID] = s
		}
	}
	o.mu.Unlock()

	// The reviewer sees the same brief shape as the barrier critic, but its
	// target is the assembled candidate: synthesis narrative, workstreams,
	// contracts and tasks with the repository graph for evidence.
	known := map[string]bool{"architect": true, "synthesis": true}
	for _, w := range o.arch.Workstreams {
		known[w.ID] = true
	}
	for _, k := range o.arch.Contracts {
		known[k.ID] = true
	}
	for _, s := range o.specSlice() {
		for _, t := range s.Tasks {
			known[t.ID] = true
		}
	}
	knownTargets := sortedKeys(known)

	spReviewer := o.codeReviewerSpec(cycle)
	pReviewer, err := fitPayload(spReviewer.avatar, reviewspec(cycle, knownTargets, o))
	if err != nil {
		return si, o.failStage(ctx, spReviewer, err)
	}
	payloads := map[string]*stageInput{relay.StageCodeReviewer: pReviewer}
	// Both reviews run on the same immutable candidate. First fill the critic
	// payload with the assembled candidate rather than the in-flight drafts.
	spCritic := o.criticSpec(cycle, true)
	pCritic := &criticPayload{
		Cycle: cycle, Barrier: true, Architect: o.arch, Specialists: o.specSlice(), Stories: o.pm.Stories,
		Graph: o.graphForPrompt(), Context: o.contextForPrompt(), EvidenceNotes: o.evidenceNotes(),
		KnownTargets: knownTargets, Prior: append([]contracts.Critique{}, o.st.Critiques...),
		Deterministic: o.findings(), Decisions: append([]contracts.Decision{}, o.st.Decisions...),
	}
	pCriticSynth, err := fitPayload(spCritic.avatar, pCritic)
	if err != nil {
		return si, o.failStage(ctx, spCritic, err)
	}
	payloads[relay.StageCritic] = pCriticSynth

	docs := map[string]json.RawMessage{}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, avatar := range []string{relay.StageCodeReviewer, relay.StageCritic} {
		wg.Add(1)
		go func(i int, avatar string) {
			defer wg.Done()
			sp := o.specFor(avatar, cycle)
			doc, _, err := runStage(o, ctx, sp, payloads[avatar], o.parseFindings(avatar))
			if err != nil {
				errs[i] = err
				return
			}
			docs[avatar] = doc
		}(i, avatar)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return si, err
		}
	}

	// Merge: reviewer and critic findings join the authoritative set; a
	// deterministic finding no longer reproduced resolves as in the barrier.
	//
	// The candidate's own findings are the prior set. Synthesize builds this
	// orchestrator from the input alone, so o.st has no critique history: without
	// seeding it, the review stage would silently discard every finding the
	// deliberation recorded, including the ones the owner waived. A waived
	// finding must reach the readiness section of the published plan.
	det := o.findings()
	o.mu.Lock()
	o.st.Critiques = append([]contracts.Critique{}, si.Critiques...)
	merged := mergeCritiques(o.st.Critiques, append(
		critsOf(docs[relay.StageCritic]), critsOf(docs[relay.StageCodeReviewer])...,
	), det, o.acceptedLocked())
	o.st.Critiques = merged
	o.mu.Unlock()
	o.save()

	return o.repairPass(ctx, cycle, si, merged)
}

// repairPass runs the Technical Leader once. It integrates the merged findings
// and repairs only the sections the blocking findings name, then re-runs the
// deterministic validators over the repaired candidate.
//
// Every finding is accounted for, and how it was closed is recorded on the
// finding itself, because the readiness verdict reads those references:
//
//   - repaired: the repair pass re-drafted the sections it named and the
//     structural validators passed on the result.
//   - integrated: the leader closed it without a repair and stated a rationale.
//     No human waived it and the plan is not ready while it stands.
//   - still open: neither happened. A blocking finding left open makes the
//     candidate blocked, and Synthesize refuses it with the reasons rather than
//     publishing a plan that claims to be verifiable.
func (o *orchestrator) repairPass(ctx context.Context, cycle int, si contracts.SynthesisInput, merged []contracts.Critique) (contracts.SynthesisInput, error) {
	if len(blockingOpen(merged)) == 0 {
		return o.publishFindings(ctx, cycle, si, merged)
	}
	sp := o.techLeadSpec(cycle)
	resolutions, _, err := runStage(o, ctx, sp, mustFit(&techLeadPayload{
		Cycle: cycle, Findings: merged, Candidate: candidateBrief(si),
	}), parseTechLead(merged))
	if err != nil {
		return si, err
	}
	touched := map[string]bool{}
	closed := map[string]string{}
	for _, r := range resolutions.Resolutions {
		if !r.Blocks {
			// A non-blocking resolution is an integration: the leader states why
			// the finding does not need a repair.
			closed[r.FindingID] = synthesizer.IntegratedRefPrefix + strings.TrimSpace(r.Rationale)
			continue
		}
		closed[r.FindingID] = synthesizer.RepairedRefPrefix + strings.Join(r.Sections, ",")
		for _, sec := range r.Sections {
			touched[sec] = true
		}
	}

	// One bounded repair: revise only the touched workstreams on MAIN.
	for _, s := range o.specSlice() {
		if !touched[s.WorkstreamID] {
			continue
		}
		if err := o.runSpecialist(ctx, cycle, s.WorkstreamID, nil); err != nil {
			return si, err
		}
	}

	// Re-run the deterministic validators over the repaired candidate. A
	// structural defect the repair introduced or left behind is recomputed here,
	// so "repaired" is not the leader's word alone.
	det := o.findings()
	o.mu.Lock()
	next := mergeCritiques(closedOpen(o.st.Critiques, closed), nil, det, o.acceptedLocked())
	o.st.Critiques = next
	o.mu.Unlock()
	o.save()

	out, err := o.publishFindings(ctx, cycle, si, next)
	if err != nil {
		return si, err
	}
	var remaining []string
	for _, c := range blockingOpen(next) {
		remaining = append(remaining, c.ID)
	}
	if len(remaining) > 0 {
		return si, &StageError{Stage: relay.StageTechLead, Err: &synthesizer.ValidationError{
			Stage: "synthesis", Problems: []string{
				"the one repair pass did not close " + strings.Join(remaining, ", ") +
					"; the candidate is blocked, not ready. Revise the named sections or start a new run.",
			},
		}}
	}
	return out, o.thought(ctx, relay.StageTechLead, cycle, statusSynced,
		fmt.Sprintf("Repair pass complete: %d section(s) revised, %d finding(s) integrated; the verdict is readiness.", len(touched), len(resolutions.Resolutions)-len(touched)))
}

// publishFindings writes the authoritative finding set onto the candidate, so
// the readiness verdict and the compiler read the same critiques the review
// stage actually produced. A finding closed without a repair keeps the
// reference that says how: waived, integrated or repaired.
func (o *orchestrator) publishFindings(ctx context.Context, cycle int, si contracts.SynthesisInput, crit []contracts.Critique) (contracts.SynthesisInput, error) {
	si.Critiques = nonNilCritiques(crit)
	if err := o.publishCritique(ctx, cycle, si.Critiques); err != nil {
		return si, err
	}
	return si, nil
}

// closedOpen applies the leader's closures to the current findings. A finding
// the leader did not name keeps its state: nothing is dropped silently.
func closedOpen(list []contracts.Critique, closed map[string]string) []contracts.Critique {
	out := make([]contracts.Critique, len(list))
	copy(out, list)
	for i := range out {
		ref, ok := closed[out[i].ID]
		if !ok || out[i].Status == contracts.CritiqueResolved {
			continue
		}
		out[i].Status, out[i].ResolutionRef = contracts.CritiqueResolved, ref
	}
	return out
}

func (o *orchestrator) synthesisPayload(si contracts.SynthesisInput, docs synthesizer.Docs) synthesisPayload {
	specs := make([]specialistBrief, 0, len(docs.Specialists))
	accepted := len(docs.Architect.Accepted)
	for _, s := range docs.Specialists {
		specs = append(specs, specialistBrief{WorkstreamID: s.WorkstreamID, Summary: s.Summary, Tasks: briefs(s.Tasks)})
		accepted += len(s.Accepted)
	}
	var driftIDs []string
	for _, d := range si.DocDrift {
		driftIDs = append(driftIDs, d.ID)
	}
	sort.Strings(driftIDs)
	return synthesisPayload{
		Product: si.PRD.Product, Epics: docs.PM.Epics, Stories: docs.PM.Stories,
		Summary: docs.Architect.Summary, Constraints: nonNil(docs.Architect.Constraints),
		Workstreams: docs.Architect.Workstreams, Contracts: docs.Architect.Contracts,
		DocTasks: briefs(docs.Architect.DocTasks), Specialists: specs,
		Critiques: nonNilCritiques(si.Critiques), Decisions: nonNilDecisions(si.Decisions),
		Coverage: si.Graph.Coverage, DocDriftIDs: nonNil(driftIDs), AcceptedCount: accepted,
		Schedule: synthesisSchedule{HoursPerDay: o.in.HoursPerDay, MaxParallel: o.in.parallel()},
	}
}

func nonNilCritiques(in []contracts.Critique) []contracts.Critique {
	if in == nil {
		return []contracts.Critique{}
	}
	return in
}

func nonNilDecisions(in []contracts.Decision) []contracts.Decision {
	if in == nil {
		return []contracts.Decision{}
	}
	return in
}
