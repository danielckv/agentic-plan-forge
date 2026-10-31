//go:build client && !server

package avatars

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

const (
	// maxDecisionRounds bounds how often interface conflicts are put to the user
	// in one cycle: a round that does not settle them is a failed stage.
	maxDecisionRounds = 3
	adoptPrefix       = "adopt:"
	optionAccept      = "accept"
	optionPause       = "pause"
	// optionRecord takes the plan with its open blockers written onto it,
	// rather than ending the run with no plan at all.
	optionRecord = "record"
	maxCardRunes = 300
)

// promptDecision records a decision as pending and opens its card. The
// authority records the input revision the card expects.
func (o *orchestrator) promptDecision(ctx context.Context, d contracts.Decision, contractID string) error {
	o.mu.Lock()
	o.st.Pending = append(o.st.Pending, d)
	if contractID != "" {
		o.st.DecisionContracts[d.ID] = contractID
	}
	o.mu.Unlock()
	o.save()
	return o.emitEvent(ctx, relay.EventDecisionPrompt, relay.DecisionPromptPayload{
		DecisionID: d.ID, RunID: o.in.RunID, Question: d.Question, Options: d.Options, RecommendedOptionID: d.RecommendedOptionID,
	})
}

func (o *orchestrator) nextSeq() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.st.Seq++
	return o.st.Seq
}

// awaitPending blocks until every pending card is resolved or ctx ends. The
// first resolution of a card wins: a repeat, an unknown card and an option the
// card never offered are ignored (the authority already rejected them).
func (o *orchestrator) awaitPending(ctx context.Context) error {
	for {
		o.mu.Lock()
		waiting := len(o.st.Pending)
		o.mu.Unlock()
		if waiting == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			o.save()
			return ctx.Err()
		case r, ok := <-o.decisions:
			if !ok {
				return errors.New("avatars: the decision channel closed with decision cards pending")
			}
			o.recordResolution(r)
		}
	}
}

func (o *orchestrator) recordResolution(r contracts.Decision) {
	o.mu.Lock()
	idx := -1
	for i, p := range o.st.Pending {
		if p.ID == r.ID {
			idx = i
		}
	}
	if idx < 0 {
		o.mu.Unlock()
		return
	}
	d := o.st.Pending[idx]
	offered := false
	for _, opt := range d.Options {
		if opt.ID == r.ResolvedOptionID {
			offered = true
		}
	}
	if !offered {
		o.mu.Unlock()
		return
	}
	d.ResolvedOptionID, d.ResolvedBy = r.ResolvedOptionID, r.ResolvedBy
	if r.InputRevision != 0 {
		d.InputRevision = r.InputRevision
	}
	at := o.in.now().UTC()
	if r.ResolvedAt != nil {
		at = r.ResolvedAt.UTC()
	}
	d.ResolvedAt = &at
	o.st.Pending = append(o.st.Pending[:idx:idx], o.st.Pending[idx+1:]...)
	o.st.Decisions = append(o.st.Decisions, d)
	o.mu.Unlock()
	o.save()
}

// --- interface conflicts ----------------------------------------------------------------------

// conflicts returns the interface findings where producer and consumer both
// name the right contract version but declare different shapes: two valid
// alternatives, which is the one case that needs a human. Broken claims are
// not decisions; they are repaired.
func (o *orchestrator) conflicts() []synthesizer.InterfaceIssue {
	o.mu.Lock()
	arch, specs := o.arch, o.specSliceLocked()
	o.mu.Unlock()
	var out []synthesizer.InterfaceIssue
	for _, is := range synthesizer.CompareInterfaces(arch, specs) {
		if is.Kind == synthesizer.IssueConflict {
			out = append(out, is)
		}
	}
	return out
}

func claimFor(s synthesizer.SpecialistDoc, contractID string) (synthesizer.InterfaceClaim, bool) {
	for _, cl := range append(append([]synthesizer.InterfaceClaim(nil), s.Implements...), s.Consumes...) {
		if cl.ContractID == contractID {
			return cl, true
		}
	}
	return synthesizer.InterfaceClaim{}, false
}

func describeClaim(cl synthesizer.InterfaceClaim) string {
	errs := strings.Join(cl.Errors, ", ")
	if errs == "" {
		errs = "none declared"
	}
	text := fmt.Sprintf("input: %s; output: %s; errors: %s", orDash(cl.InputSchema), orDash(cl.OutputSchema), errs)
	return sanitize.Truncate(sanitize.Terminal(text), maxCardRunes)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unspecified"
	}
	return s
}

func (o *orchestrator) conflictDecision(c int, is synthesizer.InterfaceIssue) contracts.Decision {
	o.mu.Lock()
	producer := ""
	for _, k := range o.arch.Contracts {
		if k.ID == is.ContractID {
			producer = k.OwnerWorkstream
		}
	}
	claims := map[string]synthesizer.InterfaceClaim{}
	for _, ws := range is.Workstreams {
		if cl, ok := claimFor(o.specs[ws], is.ContractID); ok {
			claims[ws] = cl
		}
	}
	o.mu.Unlock()
	opts := make([]contracts.DecisionOption, 0, len(is.Workstreams))
	recommended := is.Workstreams[0]
	for _, ws := range is.Workstreams {
		other := ""
		for _, w := range is.Workstreams {
			if w != ws {
				other = w
			}
		}
		opts = append(opts, contracts.DecisionOption{
			ID:       adoptPrefix + ws,
			Label:    sanitize.Truncate(sanitize.Terminal("Adopt the "+ws+" declaration"), maxCardRunes),
			Tradeoff: describeClaim(claims[ws]),
			Impact:   sanitize.Truncate(sanitize.Terminal("Workstream "+other+" is re-drafted to match; no other workstream changes."), maxCardRunes),
		})
		if ws == producer {
			recommended = ws
		}
	}
	return contracts.Decision{
		ID: fmt.Sprintf("dec-iface-%s-c%d-%d", is.ContractID, c, o.nextSeq()),
		Question: sanitize.Truncate(sanitize.Terminal(fmt.Sprintf(
			"Workstreams %s declare different schemas or error cases for contract %s. Which declaration becomes authoritative?",
			strings.Join(is.Workstreams, " and "), is.ContractID,
		)), maxCardRunes*2),
		Options:             opts,
		RecommendedOptionID: adoptPrefix + recommended,
	}
}

// resolveConflicts opens a decision card for every interface conflict between
// valid alternatives, waits for the owner or an editor to resolve them, and
// re-drafts the workstream whose declaration lost so it matches the chosen one.
// It finishes before the critic barrier, so the barrier reviews the drafts that
// result from the decisions.
func (o *orchestrator) resolveConflicts(ctx context.Context, c int) error {
	if err := o.awaitPending(ctx); err != nil {
		return err
	}
	for _, id := range o.unappliedConflictDecisions() {
		if err := o.applyConflict(ctx, c, id); err != nil {
			return err
		}
	}
	for round := 0; ; round++ {
		conflicts := o.conflicts()
		if len(conflicts) == 0 {
			return nil
		}
		if round >= maxDecisionRounds {
			ws := conflicts[0].Workstreams[len(conflicts[0].Workstreams)-1]
			return &StageError{
				Stage: relay.StageSpecialistPrefix + ws,
				Err:   fmt.Errorf("interface conflict on contract %s is unresolved after %d decision rounds", conflicts[0].ContractID, maxDecisionRounds),
			}
		}
		var opened []string
		for _, is := range conflicts {
			d := o.conflictDecision(c, is)
			if err := o.promptDecision(ctx, d, is.ContractID); err != nil {
				return err
			}
			opened = append(opened, d.ID)
		}
		if err := o.awaitPending(ctx); err != nil {
			return err
		}
		for _, id := range opened {
			if err := o.applyConflict(ctx, c, id); err != nil {
				return err
			}
		}
	}
}

func (o *orchestrator) unappliedConflictDecisions() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	for _, d := range o.st.Decisions {
		if _, ok := o.st.DecisionContracts[d.ID]; ok && !o.st.Applied[d.ID] {
			out = append(out, d.ID)
		}
	}
	sort.Strings(out)
	return out
}

// applyConflict carries out one resolved conflict decision. It is idempotent:
// a conflict that no longer exists is just marked applied.
func (o *orchestrator) applyConflict(ctx context.Context, c int, decisionID string) error {
	o.mu.Lock()
	var d contracts.Decision
	for _, x := range o.st.Decisions {
		if x.ID == decisionID {
			d = x
		}
	}
	contractID := o.st.DecisionContracts[decisionID]
	o.mu.Unlock()
	if d.ID == "" || !strings.HasPrefix(d.ResolvedOptionID, adoptPrefix) {
		return nil
	}
	winner := strings.TrimPrefix(d.ResolvedOptionID, adoptPrefix)
	loser := ""
	for _, opt := range d.Options {
		if ws := strings.TrimPrefix(opt.ID, adoptPrefix); ws != winner {
			loser = ws
		}
	}
	mark := func() {
		o.mu.Lock()
		o.st.Applied[decisionID] = true
		o.mu.Unlock()
		o.save()
	}
	if loser == "" || !o.stillConflicts(contractID, winner, loser) {
		mark()
		return nil
	}
	o.mu.Lock()
	winnerClaim, ok := claimFor(o.specs[winner], contractID)
	o.mu.Unlock()
	if !ok {
		mark()
		return nil
	}
	adopt := &adoptInput{DecisionID: decisionID, ContractID: contractID, Claim: winnerClaim}
	if err := o.runSpecialist(ctx, c, loser, adopt); err != nil {
		return err
	}
	if o.stillConflicts(contractID, winner, loser) {
		// The model did not follow the decision. The human choice is binding, so
		// the claim is aligned deterministically rather than asked for again.
		if err := o.alignClaim(ctx, c, loser, contractID, winnerClaim); err != nil {
			return err
		}
	}
	mark()
	return nil
}

func (o *orchestrator) stillConflicts(contractID, a, b string) bool {
	for _, is := range o.conflicts() {
		if is.ContractID == contractID && containsString(is.Workstreams, a) && containsString(is.Workstreams, b) {
			return true
		}
	}
	return false
}

// alignClaim rewrites the loser's claim for one contract to the winner's
// declaration and publishes the new draft revision.
func (o *orchestrator) alignClaim(ctx context.Context, c int, ws, contractID string, win synthesizer.InterfaceClaim) error {
	n := o.nextRev(relay.StageSpecialistPrefix + ws)
	o.mu.Lock()
	doc := o.specs[ws]
	apply := func(list []synthesizer.InterfaceClaim) {
		for i := range list {
			if list[i].ContractID == contractID {
				list[i].InputSchema, list[i].OutputSchema = win.InputSchema, win.OutputSchema
				list[i].Errors = append([]string(nil), win.Errors...)
			}
		}
	}
	doc.Implements = append([]synthesizer.InterfaceClaim(nil), doc.Implements...)
	doc.Consumes = append([]synthesizer.InterfaceClaim(nil), doc.Consumes...)
	apply(doc.Implements)
	apply(doc.Consumes)
	raw := mustJSON(doc)
	o.specs[ws] = doc
	o.st.Specs[ws] = draftRec{Raw: raw, Cycle: c, Rev: n}
	o.mu.Unlock()
	o.save()
	return o.publishDraft(ctx, relay.StageSpecialistPrefix+ws, synthesizer.SchemaSpecialist, c, raw, n)
}

// --- MEDIUM findings at the cap ---------------------------------------------------------------

func (o *orchestrator) decisionWithPrefix(prefix string) (resolved, pending *contracts.Decision) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i := range o.st.Decisions {
		if strings.HasPrefix(o.st.Decisions[i].ID, prefix) {
			d := o.st.Decisions[i]
			resolved = &d
		}
	}
	for i := range o.st.Pending {
		if strings.HasPrefix(o.st.Pending[i].ID, prefix) {
			d := o.st.Pending[i]
			pending = &d
		}
	}
	return resolved, pending
}

// settleMediums handles the one case where only MEDIUM findings survive the
// last automatic cycle. A MEDIUM finding must be fixed or recorded as an
// accepted tradeoff, never dropped, so the user is asked once: accept them all
// as documented tradeoffs, or pause. Accepting records the decision as each
// finding's resolution.
func (o *orchestrator) settleMediums(ctx context.Context, c int, mediums []contracts.Critique) (bool, error) {
	prefix := fmt.Sprintf("dec-medium-c%d-", c)
	resolved, pending := o.decisionWithPrefix(prefix)
	if resolved == nil && pending == nil {
		d := contracts.Decision{
			ID: fmt.Sprintf("%s%d", prefix, o.nextSeq()),
			Question: fmt.Sprintf("%d MEDIUM finding(s) remain after the final automatic cycle. Accept them as documented tradeoffs, or pause for manual revision?",
				len(mediums)),
			Options: []contracts.DecisionOption{
				{
					ID: optionAccept, Label: "Accept as documented tradeoffs",
					Tradeoff: "The plan proceeds and each finding is recorded as an accepted tradeoff with this decision as its rationale.",
					Impact:   "No extra scope or effort; the residual risk is listed in the plan's coverage caveats.",
				},
				{
					ID: optionPause, Label: "Pause for manual revision",
					Tradeoff: "Synthesis stays disabled until the baseline is revised or a new run is started.",
					Impact:   "Adds a new deliberation run and its effort.",
				},
			},
			RecommendedOptionID: optionAccept,
		}
		if err := o.promptDecision(ctx, d, ""); err != nil {
			return false, err
		}
	}
	if err := o.awaitPending(ctx); err != nil {
		return false, err
	}
	resolved, _ = o.decisionWithPrefix(prefix)
	if resolved == nil || resolved.ResolvedOptionID != optionAccept {
		return false, o.pause(ctx, c, mediums)
	}
	o.mu.Lock()
	for i := range o.st.Critiques {
		cr := &o.st.Critiques[i]
		if cr.Status != contracts.CritiqueResolved && cr.Severity == contracts.SeverityMedium {
			cr.Status, cr.ResolutionRef = contracts.CritiqueResolved, "accepted:"+resolved.ID
		}
	}
	o.mu.Unlock()
	o.save()
	if err := o.publishCritique(ctx, c, o.critiqueSnapshot()); err != nil {
		return false, err
	}
	return true, nil
}

// settleBlockers is the last gate of a capped run, and the reason a run can no
// longer end with nothing.
//
// A run that reached the cycle cap with CRITICAL or HIGH findings open used to
// dead-end: the authority refused another cycle ("run is paused at the cycle
// cap") and refused synthesis (CodeBlockersOpen), and the only way out threw
// the whole run away and restarted it at cycle 1. Three cycles of avatar work
// bought nothing and the owner never saw a plan.
//
// The findings are real, so they are not dropped. They are recorded: the owner
// decides once whether to take the plan with them written on it, and the spec
// carries a readiness verdict naming every one. This mirrors settleMediums,
// which already does exactly this for MEDIUM findings.
//
// Every open critique is settled here, not only the blocking ones, because the
// plan is gated twice: the authority counts CRITICAL/HIGH into openBlockers,
// and the compiler's Validate rejects any unresolved critique, with a MEDIUM
// needing an "accepted:" reference. Settling only the blockers would clear the
// first gate and fail the second.
func (o *orchestrator) settleBlockers(ctx context.Context, c int, blockers []contracts.Critique) (bool, error) {
	prefix := fmt.Sprintf("dec-blockers-c%d-", c)
	resolved, pending := o.decisionWithPrefix(prefix)
	if resolved == nil && pending == nil {
		d := contracts.Decision{
			ID: fmt.Sprintf("%s%d", prefix, o.nextSeq()),
			Question: fmt.Sprintf("%d blocking finding(s) are still open after the final automatic cycle. Record them on the plan and produce it, or pause for manual revision?",
				len(blockers)),
			Options: []contracts.DecisionOption{
				{
					ID: optionRecord, Label: "Record them and produce the plan",
					Tradeoff: "The plan is produced and marked provisional. Every open finding is written into its readiness section with the evidence and the change it asks for.",
					Impact:   "You get a complete plan to work from; the listed findings stay unresolved and visible until a later run closes them.",
				},
				{
					ID: optionPause, Label: "Pause for manual revision",
					Tradeoff: "No plan is produced. The run stops here and waits for the baseline to be revised or a new run to be started.",
					Impact:   "This cycle's work is kept, but nothing is delivered until you come back to it.",
				},
			},
			RecommendedOptionID: optionRecord,
		}
		if err := o.promptDecision(ctx, d, ""); err != nil {
			return false, err
		}
	}
	if err := o.awaitPending(ctx); err != nil {
		return false, err
	}
	resolved, _ = o.decisionWithPrefix(prefix)
	if resolved == nil || resolved.ResolvedOptionID != optionRecord {
		return false, o.pause(ctx, c, blockers)
	}
	o.mu.Lock()
	for i := range o.st.Critiques {
		cr := &o.st.Critiques[i]
		if cr.Status == contracts.CritiqueResolved {
			continue
		}
		// The prefix is load-bearing. RecordedRefPrefix marks a finding the
		// owner took knowingly, and is what the readiness section lists;
		// "accepted:" is the reference the compiler already requires of an
		// unresolved MEDIUM.
		if cr.Severity == contracts.SeverityCritical || cr.Severity == contracts.SeverityHigh {
			cr.Status, cr.ResolutionRef = contracts.CritiqueResolved, contracts.RecordedRefPrefix+resolved.ID
			continue
		}
		cr.Status, cr.ResolutionRef = contracts.CritiqueResolved, "accepted:"+resolved.ID
	}
	o.mu.Unlock()
	o.save()
	if err := o.publishCritique(ctx, c, o.critiqueSnapshot()); err != nil {
		return false, err
	}
	return true, nil
}
