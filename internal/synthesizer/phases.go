package synthesizer

import (
	"sort"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Phase is one wave of the implementation: every task whose dependencies are
// satisfied by earlier phases, so the tasks inside a phase can be worked at
// the same time and the phases themselves are strictly ordered.
//
// A phase is derived, never authored. The Lead Synthesizer cannot invent work
// (its document carries narrative only), and a model-written phase list would
// have to be checked against the task graph anyway — a phase that depended on
// a later one would be worse than no phases at all. Computing it here means it
// is correct by construction and the same plan always yields the same phases.
type Phase struct {
	Number int `json:"number"`
	// Title names what the phase delivers, taken from the epic most of its
	// tasks belong to.
	Title         string   `json:"title"`
	TaskIDs       []string `json:"task_ids"`
	WorkstreamIDs []string `json:"workstream_ids,omitempty"`
	EpicIDs       []string `json:"epic_ids,omitempty"`
	// HumanHours and AgentMinutes are the summed PERT estimates of the phase's
	// tasks, in the units the task estimates already use.
	HumanHours   float64 `json:"human_hours"`
	AgentMinutes float64 `json:"agent_minutes"`
	// Gate lists the verification commands that must pass before the next
	// phase starts. It is what makes a phase independently shippable.
	Gate []contracts.VerificationCommand `json:"gate,omitempty"`
}

// Phases groups tasks into dependency waves.
//
// The level of a task is 0 when it depends on nothing and otherwise one more
// than the deepest task it depends on, so each phase is an antichain: no task
// in a phase depends on another task in the same phase. Ordering within a
// phase follows task id, which topoOrder already uses to break ties, so the
// output is byte-identical for identical input.
func Phases(tasks []contracts.TechnicalTask, epics []contracts.Epic) ([]Phase, error) {
	if len(tasks) == 0 {
		return nil, nil
	}
	byID := make(map[string]contracts.TechnicalTask, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	order, err := topoOrder(byID)
	if err != nil {
		return nil, err
	}

	// topoOrder guarantees every dependency appears before its dependant, so
	// one pass is enough to settle each level.
	level := make(map[string]int, len(order))
	deepest := 0
	for _, id := range order {
		best := 0
		for _, dep := range byID[id].DependsOn {
			// A dependency outside the set cannot raise the level. Validate
			// has already rejected unknown references by the time a plan is
			// compiled; this keeps the function total if it is called earlier.
			if got, ok := level[dep]; ok && got+1 > best {
				best = got + 1
			}
		}
		level[id] = best
		if best > deepest {
			deepest = best
		}
	}

	titles := make(map[string]string, len(epics))
	for _, e := range epics {
		titles[e.ID] = e.Title
	}

	phases := make([]Phase, 0, deepest+1)
	for n := 0; n <= deepest; n++ {
		phase := Phase{Number: n + 1}
		workstreams := map[string]bool{}
		epicCount := map[string]int{}
		for _, id := range order {
			if level[id] != n {
				continue
			}
			task := byID[id]
			phase.TaskIDs = append(phase.TaskIDs, id)
			phase.HumanHours += PERT(task.HumanEstimate)
			phase.AgentMinutes += PERT(task.AgentEstimate)
			phase.Gate = append(phase.Gate, task.Verification...)
			if task.WorkstreamID != "" {
				workstreams[task.WorkstreamID] = true
			}
			if task.EpicID != "" {
				epicCount[task.EpicID]++
			}
		}
		if len(phase.TaskIDs) == 0 {
			continue
		}
		phase.WorkstreamIDs = sortedKeys(workstreams)
		phase.EpicIDs = sortedCountKeys(epicCount)
		phase.Title = phaseTitle(epicCount, titles)
		phases = append(phases, phase)
	}
	return phases, nil
}

// phaseTitle names a phase after the epic most of its tasks serve. Ties break
// on the epic id so the title is stable.
func phaseTitle(counts map[string]int, titles map[string]string) string {
	best, bestCount := "", 0
	for _, id := range sortedCountKeys(counts) {
		if counts[id] > bestCount {
			best, bestCount = id, counts[id]
		}
	}
	if title := titles[best]; title != "" {
		return title
	}
	if best != "" {
		return best
	}
	return "Implementation"
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedCountKeys(counts map[string]int) []string {
	out := make([]string, 0, len(counts))
	for k := range counts {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
