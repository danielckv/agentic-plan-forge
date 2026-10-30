//go:build client && !server

package avatars

import (
	"context"
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

// Gap lifecycle transitions the orchestrator owns. A proposal the server
// rejects never schedules; a rejected or failed agent stays inspectable in the
// run history with its reason.
//
// proposed → admitted → queued → running → completed → fused → retired
//
//	rejected            ↘ failed        ↗
//	                         cancelled
const maxGapProposals = 12

// AdmitGaps verifies the architect's gap proposals against the run state and
// admits at most cfg.MaxGapAgents of them, in proposal order. Admission checks
// each plan requirement: referenced requirements exist, the deliverable is a
// concrete artifact, the parent workstream exists, and no admitted or required
// owner already covers the same normalized scope.
func AdmitGaps(arch synthesizer.ArchitectDoc, pm synthesizer.PMDoc, maxAgents int) (admitted, rejected []contracts.GapAgentDescriptor) {
	proposals := arch.ProposedGaps
	if len(proposals) == 0 {
		return nil, nil
	}
	// Every proposal past the per-run bound is recorded as rejected with its
	// reason: nothing is silently truncated, and the owner can see what the
	// model asked for. A zero cap still reports each proposal as rejected, so a
	// run with the limit turned off explains itself.
	overflow := make([]contracts.GapAgentDescriptor, 0)
	if len(proposals) > maxGapProposals {
		for _, rest := range proposals[maxGapProposals:] {
			rest.Status = contracts.GapRejected
			rest.Reason = fmt.Sprintf("more than %d proposals in one reply", maxGapProposals)
			overflow = append(overflow, rest)
		}
		proposals = proposals[:maxGapProposals]
	}
	if maxAgents <= 0 {
		for _, g := range proposals {
			g.Status = contracts.GapRejected
			g.Reason = "gap agents are disabled by the run's runtime limit"
			rejected = append(rejected, g)
		}
		return nil, append(rejected, overflow...)
	}

	reqs := requirementSet(pm)
	owned := ownedScopes(arch)
	admittedIDs := map[string]bool{}
	parentIDs := map[string]bool{}
	for _, w := range arch.Workstreams {
		parentIDs[w.ID] = true
	}

	for i := range proposals {
		g := proposals[i]
		if reason := gapProblem(g, reqs, parentIDs, owned, admittedIDs); reason != "" {
			g.Status = contracts.GapRejected
			g.Reason = reason
			rejected = append(rejected, g)
			continue
		}
		g.Status = contracts.GapAdmitted
		admitted = append(admitted, g)
		admittedIDs[g.ID] = true
		owned[normalizeScope(g.Specialty)] = true
		if len(admitted) >= maxAgents {
			// The cap is reached: every later proposal is rejected for the cap,
			// still checked for its own defects first so the reason names the
			// first real problem when there is one.
			for _, rest := range proposals[i+1:] {
				rest.Status = contracts.GapRejected
				if reason := gapProblem(rest, reqs, parentIDs, owned, admittedIDs); reason != "" {
					rest.Reason = reason
				} else {
					rest.Reason = fmt.Sprintf("the run admits at most %d agents", maxAgents)
				}
				rejected = append(rejected, rest)
			}
			break
		}
	}
	return admitted, append(rejected, overflow...)
}

// gapProblem returns the first admission problem with the proposal, or "" when
// it passes. Every check is deterministic and server-side.
func gapProblem(g contracts.GapAgentDescriptor, reqs, parents, owned map[string]bool, admitted map[string]bool) string {
	if strings.TrimSpace(g.ID) == "" {
		return "descriptor has no id"
	}
	if admitted[g.ID] {
		return fmt.Sprintf("duplicate id %q", g.ID)
	}
	if strings.TrimSpace(g.Specialty) == "" {
		return "descriptor has no specialty"
	}
	if strings.TrimSpace(g.Purpose) == "" {
		return "descriptor has no purpose"
	}
	if strings.TrimSpace(g.Deliverable) == "" {
		return "descriptor names no deliverable"
	}
	if len(g.RequirementIDs) == 0 {
		return "descriptor references no requirements"
	}
	for _, r := range g.RequirementIDs {
		if !reqs[r] {
			return fmt.Sprintf("requirement %q does not exist", r)
		}
	}
	if g.ParentID != "" && !parents[g.ParentID] {
		return fmt.Sprintf("parent workstream %q is not declared", g.ParentID)
	}
	scope := normalizeScope(g.Specialty)
	if owned[scope] {
		return fmt.Sprintf("scope %q is already owned by a workstream or admitted agent", scope)
	}
	return ""
}

// requirementSet collects the valid requirement IDs of the PM draft. Stories
// trace requirements; epics group stories and carry no requirement list.
func requirementSet(pm synthesizer.PMDoc) map[string]bool {
	out := map[string]bool{}
	for _, s := range pm.Stories {
		for _, r := range s.RequirementIDs {
			out[r] = true
		}
	}
	return out
}

// ownedScopes marks the expertise scopes the required workstream owners
// already cover, from their declared workstream roles and IDs.
func ownedScopes(arch synthesizer.ArchitectDoc) map[string]bool {
	out := map[string]bool{}
	for _, w := range arch.Workstreams {
		out[normalizeScope(w.Role)] = true
		out[normalizeScope(w.ID)] = true
	}
	return out
}

// normalizeScope folds a scope label for duplicate comparison: lowercase,
// trimmed, hyphens and spaces folded, common qualifiers dropped.
func normalizeScope(s string) string {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.NewReplacer("-", " ", "_", " ").Replace(t)
	fields := strings.Fields(t)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f == "specialist" || f == "expertise" || f == "expert" {
			continue
		}
		out = append(out, f)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// admitAndRunGaps admits the architect's proposals and runs the admitted
// agents beside the specialists, under the same semaphore. A proposal is
// admitted, rejected or dropped by the cap; the status event for each lands
// before any provider call, so the workspace shows the agent before it works.
// Admission is idempotent per run: a resumed run does not re-admit.
func (o *orchestrator) admitAndRunGaps(ctx context.Context, c int) error {
	o.mu.Lock()
	if o.st.GapsAdmitted {
		admitted := resumableGaps(o.st.Gaps)
		o.mu.Unlock()
		return o.runGapAgents(ctx, c, admitted)
	}
	admitted, rejected := AdmitGaps(o.arch, o.pm, o.maxGapAgents)
	if len(admitted) == 0 && len(rejected) == 0 {
		o.mu.Unlock()
		return nil
	}
	if o.st.Gaps == nil {
		o.st.Gaps = []contracts.GapAgentDescriptor{}
	}
	o.st.Gaps = append(o.st.Gaps, rejected...)
	o.st.Gaps = append(o.st.Gaps, admitted...)
	o.st.GapsAdmitted = true
	o.mu.Unlock()
	o.save()

	// One thought per decision: admitted agents appear in the workspace,
	// rejected ones say why. The architect stays the author of record.
	for _, g := range admitted {
		_ = o.thought(ctx, gapStage(g.ID), c, statusThinking, fmt.Sprintf("Admitted temporary agent %q (%s): %s", g.ID, g.Specialty, g.Purpose))
	}
	for _, g := range rejected {
		_ = o.thought(ctx, gapStage(g.ID), c, statusThinking, fmt.Sprintf("Rejected temporary agent %q: %s", g.ID, g.Reason))
	}
	return o.runGapAgents(ctx, c, admitted)
}

// resumableGaps filters stored descriptors to the ones a resumed run may
// still execute. A rejected, retired or cancelled agent never runs again
// without an explicit retry, and neither does a failed one: its failure is
// recorded for the run history.
func resumableGaps(gaps []contracts.GapAgentDescriptor) []contracts.GapAgentDescriptor {
	out := make([]contracts.GapAgentDescriptor, 0, len(gaps))
	for _, g := range gaps {
		switch g.Status {
		case contracts.GapAdmitted, contracts.GapQueued, contracts.GapRunning, contracts.GapCompleted:
			out = append(out, g)
		}
	}
	return out
}

// runGapAgents drafts every admitted agent that has not completed, under the
// shared semaphore. The agent drafts into its parent workstream lane when it
// has one, so its deliverable joins the normal fusion; the synthesis lane
// covers an agent with no parent.
func (o *orchestrator) runGapAgents(ctx context.Context, c int, admitted []contracts.GapAgentDescriptor) error {
	o.mu.Lock()
	var run []contracts.GapAgentDescriptor
	for _, g := range admitted {
		if rec, ok := o.st.GapDocs[g.ID]; !ok || !rec.present() {
			o.setGapStatus(g.ID, contracts.GapQueued)
			g.Status = contracts.GapQueued
			run = append(run, g)
		}
	}
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
	for i, g := range run {
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
			if err := o.runGapAgent(ctx, c, g); err != nil {
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
		if err != nil {
			var emitErr *emitError
			if !errors.As(err, &emitErr) {
				return err
			}
			return err
		}
	}
	return nil
}

// runGapAgent runs one temporary agent through the specialist stage: the same
// prompt shape, the same schema, scoped to its descriptor. The payload omits
// the architect's gap proposals, so an agent can never propose another agent.
func (o *orchestrator) runGapAgent(ctx context.Context, c int, g contracts.GapAgentDescriptor) error {
	o.mu.Lock()
	arch, pm := o.arch, o.pm
	decisions := append([]contracts.Decision{}, o.st.Decisions...)
	o.setGapStatus(g.ID, contracts.GapRunning)
	o.mu.Unlock()
	o.save()

	archNoGaps := arch
	archNoGaps.ProposedGaps = nil

	lane := g.ParentID
	if lane == "" {
		lane = "synthesis"
	}
	w, _ := workstreamOf(arch, g.ParentID)
	sp := stageSpec{
		avatar: gapStage(g.ID), role: contracts.RoleGapAgent, lane: lane, slot: contracts.SlotMain,
		system: SpecialistSystemPrompt(), schema: synthesizer.SchemaGapAgent, cycle: c, status: statusThinking,
		summary: fmt.Sprintf("Temporary agent %q (%s) is drafting its deliverable: %s", g.ID, g.Specialty, g.Deliverable),
		step:    "gap " + g.ID,
	}
	payload, err := fitPayload(sp.avatar, &gapAgentPayload{
		Cycle: c, Gap: g, Lane: lane,
		Workstreams: arch.Workstreams, Summary: arch.Summary, Constraints: nonNil(arch.Constraints),
		Contracts: arch.Contracts, Epics: pm.Epics, Stories: pm.Stories,
		Graph: o.graphForWorkstream(w, pm.Stories), Context: o.contextForPrompt(), EvidenceNotes: o.evidenceNotes(),
		Decisions: decisions,
	})
	if err != nil {
		o.markGap(g.ID, contracts.GapFailed)
		return o.failStage(ctx, sp, err)
	}
	_, raw2, err := runStage(o, ctx, sp, payload, o.parseGapAgent(g))
	if err != nil {
		o.markGap(g.ID, contracts.GapFailed)
		return err
	}
	n := o.nextRev(sp.avatar)
	o.mu.Lock()
	if o.st.GapDocs == nil {
		o.st.GapDocs = map[string]draftRec{}
	}
	o.st.GapDocs[g.ID] = draftRec{Raw: raw2, Cycle: c, Rev: n}
	o.setGapStatus(g.ID, contracts.GapCompleted)
	o.mu.Unlock()
	o.save()
	return o.publishDraft(ctx, sp.avatar, synthesizer.SchemaGapAgent, c, raw2, n)
}

// markGap records a terminal failure for one agent.
func (o *orchestrator) markGap(id, status string) {
	o.mu.Lock()
	o.setGapStatus(id, status)
	o.mu.Unlock()
	o.save()
}

// setGapStatus writes one agent's status. Callers hold o.mu.
func (o *orchestrator) setGapStatus(id, status string) {
	for i := range o.st.Gaps {
		if o.st.Gaps[i].ID == id {
			o.st.Gaps[i].Status = status
			return
		}
	}
}

// gapStage names the stage of one gap agent.
func gapStage(id string) string { return relay.StageGapAgentPrefix + id }

// lastGapProblem recomputes why one proposal was rejected, for the thought.
func (o *orchestrator) lastGapProblem(arch synthesizer.ArchitectDoc, pm synthesizer.PMDoc, g contracts.GapAgentDescriptor) string {
	admittedIDs := map[string]bool{}
	for _, a := range o.st.Gaps {
		if a.Status == contracts.GapAdmitted || a.Status == contracts.GapQueued || a.Status == contracts.GapRunning || a.Status == contracts.GapCompleted {
			admittedIDs[a.ID] = true
		}
	}
	if reason := gapProblem(g, requirementSet(pm), parentSet(arch), ownedScopes(arch), admittedIDs); reason != "" {
		return reason
	}
	return "the per-run admission limit"
}

// parentSet collects the declared workstream IDs.
func parentSet(arch synthesizer.ArchitectDoc) map[string]bool {
	out := map[string]bool{}
	for _, w := range arch.Workstreams {
		out[w.ID] = true
	}
	return out
}
