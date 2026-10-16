package synthesizer

import (
	"container/heap"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Schedule defaults. They mirror the configuration defaults so that compiling
// before any configuration exists is still deterministic.
const (
	DefaultHoursPerDay = 9.0
	DefaultMaxParallel = 4
	// MaxScheduleParallel is the hard concurrency ceiling shared with the
	// orchestrator.
	MaxScheduleParallel = 4
	// DefaultStartDate is the Monday the human calendar starts on when the
	// caller configures none. Only relative durations are meaningful; the date
	// is fixed so compilation never reads the clock.
	DefaultStartDate = "2026-01-05"
)

// EstimateDisclaimer is shown wherever an estimate appears.
const EstimateDisclaimer = "model estimate, not measured"

// ScheduleOptions configures BuildScheduleWith.
type ScheduleOptions struct {
	// HoursPerDay is the productive human workday length. Zero selects 9.
	HoursPerDay float64
	// MaxParallel bounds concurrent agent tasks. Zero selects 4; the value is
	// clamped to 1-4.
	MaxParallel int
	// Start is the first working day of the human calendar. Zero selects
	// DefaultStartDate.
	Start time.Time
}

// ScheduledTask is one task placed on one schedule.
type ScheduledTask struct {
	ID           string  `json:"id"`
	WorkstreamID string  `json:"workstream_id"`
	Title        string  `json:"title"`
	Expected     float64 `json:"expected"`
	Optimistic   float64 `json:"optimistic"`
	Pessimistic  float64 `json:"pessimistic"`
	// Start and End are offsets from the schedule start, in hours (human) or
	// minutes (agent).
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Basis string  `json:"basis"`
}

// Side is one of the two schedules.
type Side struct {
	// Unit is "hours" for the human side and "minutes" for the agent side.
	Unit string `json:"unit"`
	// Effort is the PERT-expected effort summed over all tasks.
	Effort            float64 `json:"effort"`
	EffortOptimistic  float64 `json:"effort_optimistic"`
	EffortPessimistic float64 `json:"effort_pessimistic"`
	// Makespan is the elapsed time from the first start to the last finish. It
	// is reported separately from Effort: with parallel agents it is smaller.
	Makespan float64 `json:"makespan"`
	// Days is Effort divided by hours per day (human side only).
	Days         float64         `json:"days,omitempty"`
	MakespanDays float64         `json:"makespan_days,omitempty"`
	Tasks        []ScheduledTask `json:"tasks"`
}

// DualSchedule holds both schedules and the assumptions they rest on.
type DualSchedule struct {
	HoursPerDay float64  `json:"hours_per_day"`
	MaxParallel int      `json:"max_parallel"`
	StartDate   string   `json:"start_date"`
	Human       Side     `json:"human"`
	Agent       Side     `json:"agent"`
	Assumptions []string `json:"assumptions"`
}

// HumanHeadline renders the human total, for example "Human: 4.5 days (40.5 hrs)".
func (d DualSchedule) HumanHeadline() string {
	return fmt.Sprintf("Human: %s days (%s hrs)", fmtNum(d.Human.Days), fmtNum(d.Human.Effort))
}

// AgentHeadline renders the agent total.
func (d DualSchedule) AgentHeadline() string {
	return fmt.Sprintf("Coding Agent: ~%s mins effort, %s mins elapsed", fmtNum(d.Agent.Effort), fmtNum(d.Agent.Makespan))
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func fmtNum(v float64) string { return strconv.FormatFloat(round2(v), 'f', -1, 64) }

func finiteNonNegative(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }

func validateEstimate(who, unit string, e contracts.Estimate) error {
	for _, f := range []struct {
		name string
		v    float64
	}{{"optimistic", e.Optimistic}, {"likely", e.Likely}, {"pessimistic", e.Pessimistic}} {
		if !finiteNonNegative(f.v) {
			return fmt.Errorf("%s %s estimate (%s) must be finite and non-negative", who, f.name, unit)
		}
	}
	if !(e.Optimistic <= e.Likely && e.Likely <= e.Pessimistic) {
		return fmt.Errorf("%s estimate (%s) must satisfy optimistic <= likely <= pessimistic", who, unit)
	}
	if strings.TrimSpace(e.Basis) == "" {
		return fmt.Errorf("%s estimate (%s) needs an explicit basis", who, unit)
	}
	return nil
}

// ValidateEstimates requires finite, non-negative, ordered three-point
// estimates with an explicit basis for both human hours and agent minutes.
func ValidateEstimates(t contracts.TechnicalTask) error {
	if err := validateEstimate("task "+t.ID+" human", "hours", t.HumanEstimate); err != nil {
		return err
	}
	return validateEstimate("task "+t.ID+" agent", "minutes", t.AgentEstimate)
}

// PERT returns the expected effort (optimistic + 4*likely + pessimistic) / 6.
func PERT(e contracts.Estimate) float64 {
	return (e.Optimistic + 4*e.Likely + e.Pessimistic) / 6
}

// BuildSchedule builds both schedules with default options.
func BuildSchedule(tasks []contracts.TechnicalTask, hoursPerDay float64) (DualSchedule, error) {
	return BuildScheduleWith(tasks, ScheduleOptions{HoursPerDay: hoursPerDay})
}

// BuildScheduleWith builds the human schedule (one developer, topological order
// with task-ID tie-break) and the agent schedule (up to MaxParallel tasks, one
// active task per workstream), reporting summed effort separately from elapsed
// makespan. It never applies a fixed human/agent speed ratio.
func BuildScheduleWith(tasks []contracts.TechnicalTask, opts ScheduleOptions) (DualSchedule, error) {
	hpd := opts.HoursPerDay
	if hpd == 0 {
		hpd = DefaultHoursPerDay
	}
	if !finiteNonNegative(hpd) || hpd <= 0 || hpd > 24 {
		return DualSchedule{}, fmt.Errorf("hours per day must be in (0, 24], got %v", opts.HoursPerDay)
	}
	par := opts.MaxParallel
	if par <= 0 {
		par = DefaultMaxParallel
	}
	par = min(par, MaxScheduleParallel)
	start := opts.Start
	if start.IsZero() {
		start, _ = time.Parse("2006-01-02", DefaultStartDate)
	}
	start = nextWeekday(time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC))

	byID := map[string]contracts.TechnicalTask{}
	for _, t := range tasks {
		if t.ID == "" {
			return DualSchedule{}, errors.New("task with empty id")
		}
		if _, dup := byID[t.ID]; dup {
			return DualSchedule{}, fmt.Errorf("duplicate task id %q", t.ID)
		}
		if err := ValidateEstimates(t); err != nil {
			return DualSchedule{}, err
		}
		byID[t.ID] = t
	}
	for _, t := range tasks {
		for _, d := range t.DependsOn {
			if _, ok := byID[d]; !ok {
				return DualSchedule{}, fmt.Errorf("task %s depends on unknown task %q", t.ID, d)
			}
		}
	}
	order, err := topoOrder(byID)
	if err != nil {
		return DualSchedule{}, err
	}

	out := DualSchedule{HoursPerDay: hpd, MaxParallel: par, StartDate: start.Format("2006-01-02")}
	out.Human = Side{Unit: "hours", Tasks: []ScheduledTask{}}
	out.Agent = Side{Unit: "minutes", Tasks: []ScheduledTask{}}

	// Human: one developer, strictly serial in topological order.
	var clock float64
	for _, id := range order {
		t := byID[id]
		exp := PERT(t.HumanEstimate)
		out.Human.Tasks = append(out.Human.Tasks, ScheduledTask{
			ID: id, WorkstreamID: t.WorkstreamID, Title: t.Title,
			Expected: exp, Optimistic: t.HumanEstimate.Optimistic, Pessimistic: t.HumanEstimate.Pessimistic,
			Start: clock, End: clock + exp, Basis: t.HumanEstimate.Basis,
		})
		clock += exp
		out.Human.Effort += exp
		out.Human.EffortOptimistic += t.HumanEstimate.Optimistic
		out.Human.EffortPessimistic += t.HumanEstimate.Pessimistic
	}
	out.Human.Makespan = clock
	out.Human.Days = out.Human.Effort / hpd
	out.Human.MakespanDays = out.Human.Makespan / hpd

	// Agent: list scheduling over declared dependencies.
	agent, err := scheduleAgent(byID, order, par)
	if err != nil {
		return DualSchedule{}, err
	}
	out.Agent.Tasks = agent.tasks
	out.Agent.Makespan = agent.makespan
	for _, id := range order {
		t := byID[id]
		out.Agent.Effort += PERT(t.AgentEstimate)
		out.Agent.EffortOptimistic += t.AgentEstimate.Optimistic
		out.Agent.EffortPessimistic += t.AgentEstimate.Pessimistic
	}

	out.Human.Effort, out.Human.EffortOptimistic, out.Human.EffortPessimistic =
		round2(out.Human.Effort), round2(out.Human.EffortOptimistic), round2(out.Human.EffortPessimistic)
	out.Human.Makespan, out.Human.Days, out.Human.MakespanDays =
		round2(out.Human.Makespan), round2(out.Human.Days), round2(out.Human.MakespanDays)
	out.Agent.Effort, out.Agent.EffortOptimistic, out.Agent.EffortPessimistic =
		round2(out.Agent.Effort), round2(out.Agent.EffortOptimistic), round2(out.Agent.EffortPessimistic)
	out.Agent.Makespan = round2(out.Agent.Makespan)
	out.Assumptions = CalendarAssumptions(out)
	return out, nil
}

// CalendarAssumptions states what the schedules assume. The text is part of the
// compiled plan: a schedule without its assumptions is not displayed.
func CalendarAssumptions(d DualSchedule) []string {
	return []string{
		fmt.Sprintf("Human calendar: one developer, 09:00-18:00 Monday-Friday, %s productive hours per working day, no implied holidays, first working day %s.", fmtNum(d.HoursPerDay), d.StartDate),
		"Human days = summed PERT hours / hours per day; PERT expected effort = (optimistic + 4 x likely + pessimistic) / 6.",
		"Human Gantt: each date column is one productive working day; bars are proportional to effort and one developer works tasks one after another.",
		fmt.Sprintf("Agent schedule: elapsed minutes with no workday conversion, up to %d concurrent tasks, at most one active task per workstream to prevent shared-file concurrency, declared task dependencies respected.", d.MaxParallel),
		"Summed effort and elapsed makespan are reported separately: parallel agent work shortens makespan, not effort.",
		"No fixed human/agent speed ratio is applied; each side uses its own per-task estimates and basis.",
		"All figures are a " + EstimateDisclaimer + " unless backed by recorded measurements.",
	}
}

// ----------------------------------------------------------------- ordering

type idHeap []string

func (h idHeap) Len() int           { return len(h) }
func (h idHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h idHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *idHeap) Push(x any)        { *h = append(*h, x.(string)) }
func (h *idHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// topoOrder returns task IDs in dependency order, breaking ties by task ID.
func topoOrder(byID map[string]contracts.TechnicalTask) ([]string, error) {
	indeg := map[string]int{}
	rev := map[string][]string{}
	for id, t := range byID {
		indeg[id] += 0
		for _, d := range t.DependsOn {
			indeg[id]++
			rev[d] = append(rev[d], id)
		}
	}
	h := &idHeap{}
	for id, n := range indeg {
		if n == 0 {
			*h = append(*h, id)
		}
	}
	heap.Init(h)
	order := make([]string, 0, len(byID))
	for h.Len() > 0 {
		id := heap.Pop(h).(string)
		order = append(order, id)
		next := rev[id]
		sort.Strings(next)
		for _, m := range next {
			indeg[m]--
			if indeg[m] == 0 {
				heap.Push(h, m)
			}
		}
	}
	if len(order) != len(byID) {
		return nil, errors.New("task dependency cycle")
	}
	return order, nil
}

type agentResult struct {
	tasks    []ScheduledTask
	makespan float64
}

// scheduleAgent simulates list scheduling: at each event time the ready tasks
// (dependencies finished, workstream idle) start in task-ID order while a worker
// is free.
func scheduleAgent(byID map[string]contracts.TechnicalTask, order []string, parallel int) (agentResult, error) {
	type running struct {
		id  string
		end float64
	}
	done := map[string]float64{}
	started := map[string]bool{}
	busyWS := map[string]bool{}
	var active []running
	var placed []ScheduledTask
	var now float64
	ids := sortedCopy(order)
	for len(done) < len(byID) {
		for {
			if len(active) >= parallel {
				break
			}
			picked := ""
			for _, id := range ids {
				if started[id] {
					continue
				}
				t := byID[id]
				if busyWS[t.WorkstreamID] {
					continue
				}
				ready := true
				for _, d := range t.DependsOn {
					fin, ok := done[d]
					if !ok || fin > now {
						ready = false
						break
					}
				}
				if ready {
					picked = id
					break
				}
			}
			if picked == "" {
				break
			}
			t := byID[picked]
			exp := PERT(t.AgentEstimate)
			started[picked] = true
			busyWS[t.WorkstreamID] = true
			active = append(active, running{picked, now + exp})
			placed = append(placed, ScheduledTask{
				ID: picked, WorkstreamID: t.WorkstreamID, Title: t.Title,
				Expected: exp, Optimistic: t.AgentEstimate.Optimistic, Pessimistic: t.AgentEstimate.Pessimistic,
				Start: now, End: now + exp, Basis: t.AgentEstimate.Basis,
			})
		}
		if len(active) == 0 {
			return agentResult{}, errors.New("agent schedule deadlock: unsatisfiable dependencies")
		}
		next := active[0].end
		for _, a := range active[1:] {
			next = math.Min(next, a.end)
		}
		now = next
		remaining := active[:0]
		for _, a := range active {
			if a.end <= now {
				done[a.id] = a.end
				busyWS[byID[a.id].WorkstreamID] = false
			} else {
				remaining = append(remaining, a)
			}
		}
		active = remaining
	}
	var makespan float64
	for _, p := range placed {
		makespan = math.Max(makespan, p.End)
	}
	sort.SliceStable(placed, func(i, j int) bool {
		if placed[i].Start != placed[j].Start {
			return placed[i].Start < placed[j].Start
		}
		return placed[i].ID < placed[j].ID
	})
	for i := range placed {
		placed[i].Expected = round2(placed[i].Expected)
		placed[i].Start = round2(placed[i].Start)
		placed[i].End = round2(placed[i].End)
	}
	return agentResult{tasks: placed, makespan: makespan}, nil
}

func nextWeekday(d time.Time) time.Time {
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// addWorkingDays returns the date n working days (Monday-Friday) after start,
// where start itself is a working day.
func addWorkingDays(start time.Time, n int) time.Time {
	d := start
	for n > 0 {
		d = d.AddDate(0, 0, 1)
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			n--
		}
	}
	return d
}
