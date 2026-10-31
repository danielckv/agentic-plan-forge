package synthesizer

import (
	"fmt"
	"sort"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Readiness verdicts. A plan is ready only when every gate in the plan's
// readiness sentence holds; a plan carrying findings that were recorded rather
// than fixed, or integrated by the Technical Leader without a repair, is
// provisional; a plan with an unresolved blocking finding or an unanswered
// material decision is blocked and is never compiled.
const (
	ReadinessReady       = "ready"
	ReadinessProvisional = "provisional"
	ReadinessBlocked     = "blocked"
)

// Finding resolution references. Each names how a finding was closed, and they
// are not interchangeable: a human waiver, a leader integration and a repair
// carry different residual risk and the readiness section says which is which.
const (
	// RepairedRefPrefix marks a finding the one bounded repair pass answered by
	// re-drafting the sections it named.
	RepairedRefPrefix = "repaired:"
	// IntegratedRefPrefix marks a finding the Technical Leader integrated
	// without a repair, with a rationale. No human waived it.
	IntegratedRefPrefix = "integrated:"
)

// IsRepaired reports whether a finding was closed by the bounded repair pass.
func IsRepaired(c contracts.Critique) bool {
	return hasRefPrefix(c.ResolutionRef, RepairedRefPrefix)
}

// IsIntegrated reports whether the Technical Leader closed a finding without a
// repair. The rationale follows the prefix.
func IsIntegrated(c contracts.Critique) bool {
	return hasRefPrefix(c.ResolutionRef, IntegratedRefPrefix)
}

// Readiness is the deterministic verdict on one candidate. It states what the
// plan is worth before the reader meets the design it judges, and it never
// claims more than the input proves:
//
//   - Verdict is ready, provisional or blocked.
//   - Blockers names every reason the plan cannot publish: an unresolved
//     CRITICAL or HIGH finding, a MEDIUM finding neither fixed nor recorded, or
//     an unanswered material decision. Validate gates on the same strings, so
//     the verdict and the refusal to compile cannot disagree.
//   - Waived lists the findings the owner recorded rather than fixed, and
//     Integrated lists the findings the leader closed without a repair. Both
//     are listed in full in the readiness section; neither is ever dropped.
//   - Repaired lists the findings the one bounded repair pass answered.
//   - AcceptedTradeoffs counts findings a draft records as an accepted tradeoff
//     with a rationale. They are listed in their own section.
//   - VerificationSpecified lists the declared verification command IDs.
//     VerificationExecuted is what ran. Compilation runs nothing, so a compiled
//     plan reports an empty executed list and the spec says so in words.
//   - EvidenceGaps names what the repository snapshot could not cover, so a
//     timed-out, truncated or unsupported scan is never read as proof.
type Readiness struct {
	Verdict               string
	Blockers              []string
	Waived                []contracts.Critique
	Integrated            []contracts.Critique
	Repaired              []contracts.Critique
	AcceptedTradeoffs     int
	Decisions             []string
	VerificationSpecified []string
	VerificationExecuted  []string
	EvidenceGaps          []string
}

// Blocked reports whether the verdict forbids publication.
func (r Readiness) Blocked() bool { return r.Verdict == ReadinessBlocked }

// ReadinessOf assesses a candidate without compiling it. A candidate whose stage
// documents do not decode is blocked, never silently ready.
func ReadinessOf(in contracts.SynthesisInput) Readiness {
	docs, err := DecodeInput(in)
	return assess(in, docs, err)
}

// assess builds the verdict from already-decoded documents. derr, when set,
// means the documents did not decode and the candidate is blocked on that.
func assess(in contracts.SynthesisInput, docs Docs, derr error) Readiness {
	r := Readiness{
		Verdict:               ReadinessReady,
		Blockers:              []string{},
		Waived:                []contracts.Critique{},
		Integrated:            []contracts.Critique{},
		Repaired:              []contracts.Critique{},
		Decisions:             []string{},
		EvidenceGaps:          []string{},
		VerificationSpecified: []string{},
		// Compilation runs no command, so nothing is executed by definition.
		VerificationExecuted: []string{},
	}
	var accepted map[string]bool
	if derr != nil {
		r.Blockers = append(r.Blockers, derr.Error())
	} else {
		accepted = AcceptedIDs(docs)
		for _, t := range AllTasks(docs.Architect, docs.Specialists) {
			for _, v := range t.Verification {
				if len(v.Argv) > 0 {
					r.VerificationSpecified = append(r.VerificationSpecified, v.ID)
				}
			}
		}
		sort.Strings(r.VerificationSpecified)
	}

	blockers, found, tradeoffs := critiqueGates(in, accepted)
	r.Blockers = append(r.Blockers, blockers...)
	r.Waived, r.Integrated, r.Repaired = found.waived, found.integrated, found.repaired
	r.AcceptedTradeoffs = tradeoffs

	for _, d := range in.Decisions {
		if d.ResolvedOptionID == "" {
			r.Blockers = append(r.Blockers, fmt.Sprintf("decision %s is unresolved", d.ID))
			continue
		}
		r.Decisions = append(r.Decisions, d.ID)
	}
	sort.Strings(r.Decisions)
	r.EvidenceGaps = evidenceGaps(in.Graph)

	switch {
	case len(r.Blockers) > 0:
		r.Verdict = ReadinessBlocked
	case len(r.Waived) > 0 || len(r.Integrated) > 0:
		// A human waiver or a leader integration leaves known residual risk in
		// the plan. It is produced and it is never called ready.
		r.Verdict = ReadinessProvisional
	default:
		r.Verdict = ReadinessReady
	}
	return r
}

// closedFindings buckets the resolved findings by how they were closed.
type closedFindings struct {
	waived     []contracts.Critique
	integrated []contracts.Critique
	repaired   []contracts.Critique
}

// critiqueGates applies the findings gate. It returns the blocking conditions,
// the resolved findings by how they were closed, and how many findings are
// recorded as accepted tradeoffs. Validate gates on the same function, so a plan
// that compiles is exactly a plan whose readiness is not blocked.
func critiqueGates(in contracts.SynthesisInput, accepted map[string]bool) (blockers []string, found closedFindings, acceptedN int) {
	for _, c := range in.Critiques {
		if c.Status == contracts.CritiqueResolved {
			switch {
			case c.IsRecorded():
				found.waived = append(found.waived, c)
			case IsIntegrated(c):
				found.integrated = append(found.integrated, c)
			case IsRepaired(c):
				found.repaired = append(found.repaired, c)
			case hasRefPrefix(c.ResolutionRef, "accepted:") || accepted[c.ID]:
				acceptedN++
			}
			continue
		}
		switch c.Severity {
		case contracts.SeverityCritical, contracts.SeverityHigh:
			blockers = append(blockers, fmt.Sprintf("critique %s (%s) is still open", c.ID, c.Severity))
		default:
			if accepted[c.ID] || hasRefPrefix(c.ResolutionRef, "accepted:") {
				acceptedN++
				continue
			}
			blockers = append(blockers, fmt.Sprintf("critique %s (MEDIUM) is neither resolved nor recorded as an accepted tradeoff", c.ID))
		}
	}
	sort.Strings(blockers)
	sortByID(found.waived)
	sortByID(found.integrated)
	sortByID(found.repaired)
	return blockers, found, acceptedN
}

// evidenceGaps names what the bounded repository snapshot could not cover. Each
// gap is a fact about the input, never an inference about the code.
func evidenceGaps(g contracts.RepoContextGraph) []string {
	var out []string
	if g.Coverage.TimedOut {
		out = append(out, "The repository scan timed out; the snapshot is partial and its absence claims are unverified.")
	}
	if GraphTruncated(g) && !g.Coverage.TimedOut {
		out = append(out, "The repository snapshot was truncated; a symbol missing from it is not proof that the symbol is absent.")
	}
	if g.Coverage.UnsupportedFiles > 0 {
		out = append(out, fmt.Sprintf("%d file(s) are in a language the scanner cannot parse; no symbol was extracted from them, so claims about them are unverified.", g.Coverage.UnsupportedFiles))
	}
	sort.Strings(out)
	return out
}

func sortByID(list []contracts.Critique) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].ID < list[j].ID })
}

func hasRefPrefix(ref, prefix string) bool {
	return len(ref) >= len(prefix) && ref[:len(prefix)] == prefix
}
