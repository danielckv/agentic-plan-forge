package synthesizer

import (
	"fmt"
	"sort"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// A sprint board is the plan arranged as work to commit to, rather than as a
// document to read. Phases (phases.go) answer "what may run at the same
// time"; sprints answer "what fits in the next two weeks". Both are derived
// from the same task graph, so neither can disagree with it.
//
// The board is computed, then edited. Moving an item records an override; the
// rest is recomputed from the graph every time, so an edit never silently
// rots as the plan changes.

// Sprint bases. The unit differs because the estimates do: human work is
// measured in hours and agent work in minutes, as ScheduledTask already has
// it.
const (
	BasisHuman = "human"
	BasisAgent = "agent"
)

// Sprint board defaults.
const (
	DefaultSprintDays = 10
	DefaultTeamSize   = 1
	// DefaultHorizon is how many sprints the auto-fill commits to. Work past
	// it stays in the backlog, which is the honest place for it: a plan is a
	// forecast, and a forecast six sprints out is a guess about a guess.
	DefaultHorizon = 3
	// MaxHorizon bounds the computation and the page.
	MaxHorizon = 12
	// BacklogSprint is the sprint number meaning "not committed".
	BacklogSprint = 0
)

// SprintOptions configures BuildSprints. A zero value selects every default.
type SprintOptions struct {
	// Basis is BasisHuman or BasisAgent. Empty selects BasisHuman.
	Basis string
	// SprintDays is the sprint length in working days. Zero selects 10.
	SprintDays int
	// WorkdayHours is the productive workday. Zero selects DefaultHoursPerDay.
	WorkdayHours float64
	// TeamSize is how many people work a human sprint. Zero selects 1.
	TeamSize int
	// MaxParallel is how many agents work an agent sprint. Zero selects
	// DefaultMaxParallel; the value is clamped to 1..MaxScheduleParallel.
	MaxParallel int
	// Horizon is how many sprints the auto-fill may use. Zero selects 3.
	Horizon int
	// Placements are the owner's own moves: task id to sprint number, where
	// BacklogSprint means the backlog. A task named here is placed there
	// whatever the capacity says, because the owner outranks the arithmetic.
	Placements map[string]int
}

// SprintItem is one technical task on the board.
type SprintItem struct {
	TaskID       string `json:"task_id"`
	EpicID       string `json:"epic_id,omitempty"`
	StoryID      string `json:"story_id,omitempty"`
	WorkstreamID string `json:"workstream_id,omitempty"`
	Title        string `json:"title"`
	// EpicTitle and StoryTitle are filled by the caller that has the compiled
	// plan, so a card can be read without a second request.
	EpicTitle  string `json:"epic_title,omitempty"`
	StoryTitle string `json:"story_title,omitempty"`
	// HumanHours and AgentMinutes are the PERT estimates, both always
	// present so the page can switch basis without asking the server again.
	HumanHours   float64  `json:"human_hours"`
	AgentMinutes float64  `json:"agent_minutes"`
	DependsOn    []string `json:"depends_on,omitempty"`
	// Targets is how many files or symbols the task expects to touch.
	Targets int `json:"targets"`
	// Moved is true when this placement came from an override, not from the
	// auto-fill. The page marks those so an edit is visible as an edit.
	Moved bool `json:"moved,omitempty"`
	// BlockedBy lists dependencies that land in a later sprint than this item,
	// or in the backlog while this item is committed. Only an override can
	// produce one.
	BlockedBy []string `json:"blocked_by,omitempty"`
}

// Sprint is one bucket of items. Number 0 is the backlog.
type Sprint struct {
	Number       int          `json:"number"`
	Name         string       `json:"name"`
	Items        []SprintItem `json:"items"`
	HumanHours   float64      `json:"human_hours"`
	AgentMinutes float64      `json:"agent_minutes"`
	// Committed is the effort in the board's basis unit, and Capacity is what
	// the sprint holds. The backlog reports Committed with no Capacity.
	Committed float64 `json:"committed"`
	Capacity  float64 `json:"capacity,omitempty"`
	// Over is true when Committed exceeds Capacity, which only an override
	// can cause.
	Over bool `json:"over,omitempty"`
}

// SprintBoard is the whole board: the committed sprints and the backlog.
type SprintBoard struct {
	Basis        string   `json:"basis"`
	Unit         string   `json:"unit"`
	SprintDays   int      `json:"sprint_days"`
	WorkdayHours float64  `json:"workday_hours"`
	TeamSize     int      `json:"team_size"`
	MaxParallel  int      `json:"max_parallel"`
	Horizon      int      `json:"horizon"`
	Capacity     float64  `json:"capacity"`
	Sprints      []Sprint `json:"sprints"`
	Backlog      Sprint   `json:"backlog"`
	// Warnings name every ordering the board cannot honour. Empty is the
	// normal state; an auto-filled board never produces one.
	Warnings []string `json:"warnings,omitempty"`
	// Disclaimer repeats what every estimate on the board rests on.
	Disclaimer string `json:"disclaimer"`
}

// normalized returns opts with every zero field replaced by its default and
// every bounded field clamped.
func (o SprintOptions) normalized() SprintOptions {
	out := o
	if out.Basis != BasisAgent {
		out.Basis = BasisHuman
	}
	if out.SprintDays <= 0 {
		out.SprintDays = DefaultSprintDays
	}
	if out.WorkdayHours <= 0 {
		out.WorkdayHours = DefaultHoursPerDay
	}
	if out.TeamSize <= 0 {
		out.TeamSize = DefaultTeamSize
	}
	if out.MaxParallel <= 0 {
		out.MaxParallel = DefaultMaxParallel
	}
	if out.MaxParallel > MaxScheduleParallel {
		out.MaxParallel = MaxScheduleParallel
	}
	if out.Horizon <= 0 {
		out.Horizon = DefaultHorizon
	}
	if out.Horizon > MaxHorizon {
		out.Horizon = MaxHorizon
	}
	return out
}

// capacity is what one sprint holds, in the basis unit.
func (o SprintOptions) capacity() float64 {
	if o.Basis == BasisAgent {
		// Agent estimates are minutes, and agents work the whole sprint.
		return float64(o.SprintDays) * o.WorkdayHours * 60 * float64(o.MaxParallel)
	}
	return float64(o.SprintDays) * o.WorkdayHours * float64(o.TeamSize)
}

func (o SprintOptions) unit() string {
	if o.Basis == BasisAgent {
		return "minutes"
	}
	return "hours"
}

// BuildSprints arranges tasks into sprints and a backlog.
//
// Tasks are walked in topological order, so a dependency is always placed
// before its dependants. Each task takes the earliest sprint that is not
// before any of its dependencies and still has room; work that does not fit
// inside the horizon stays in the backlog, and so does anything depending on
// backlog work. An override places its task where the owner put it and the
// walk continues around it.
//
// The same plan and the same options always produce the same board.
func BuildSprints(tasks []contracts.TechnicalTask, opts SprintOptions) (SprintBoard, error) {
	opts = opts.normalized()
	board := SprintBoard{
		Basis:        opts.Basis,
		Unit:         opts.unit(),
		SprintDays:   opts.SprintDays,
		WorkdayHours: opts.WorkdayHours,
		TeamSize:     opts.TeamSize,
		MaxParallel:  opts.MaxParallel,
		Horizon:      opts.Horizon,
		Capacity:     round2(opts.capacity()),
		Disclaimer:   EstimateDisclaimer,
		Sprints:      []Sprint{},
		Backlog:      Sprint{Number: BacklogSprint, Name: "Backlog", Items: []SprintItem{}},
	}
	if len(tasks) == 0 {
		return board, nil
	}

	byID := make(map[string]contracts.TechnicalTask, len(tasks))
	for _, t := range tasks {
		if err := ValidateEstimates(t); err != nil {
			return SprintBoard{}, err
		}
		byID[t.ID] = t
	}
	order, err := topoOrder(byID)
	if err != nil {
		return SprintBoard{}, err
	}

	capacity := opts.capacity()
	placed := make(map[string]int, len(order))
	used := make([]float64, opts.Horizon+1) // index 0 unused: the backlog is unbounded

	for _, id := range order {
		task := byID[id]
		cost := effortIn(opts.Basis, task)

		if want, ok := opts.Placements[id]; ok {
			target := want
			if target < BacklogSprint || target > opts.Horizon {
				target = BacklogSprint
			}
			placed[id] = target
			if target != BacklogSprint {
				used[target] += cost
			}
			continue
		}

		// A task can start no earlier than the sprint after its last
		// dependency starts — same sprint is allowed, since a sprint is a
		// bucket of work rather than a single worker.
		earliest := 1
		blocked := false
		for _, dep := range task.DependsOn {
			at, known := placed[dep]
			if !known {
				continue // Validate rejects unknown references before this runs.
			}
			if at == BacklogSprint {
				blocked = true
				break
			}
			if at > earliest {
				earliest = at
			}
		}
		if blocked {
			placed[id] = BacklogSprint
			continue
		}

		target := BacklogSprint
		for n := earliest; n <= opts.Horizon; n++ {
			if used[n]+cost <= capacity || used[n] == 0 {
				// used[n] == 0 admits one task larger than a whole sprint
				// rather than looping forever past it. The sprint then reports
				// Over, which is the truth.
				target = n
				break
			}
		}
		placed[id] = target
		if target != BacklogSprint {
			used[target] += cost
		}
	}

	return assemble(board, byID, order, placed, opts), nil
}

// assemble turns placements into the board, in topological order within each
// sprint, and records every dependency the placement cannot honour.
func assemble(board SprintBoard, byID map[string]contracts.TechnicalTask, order []string, placed map[string]int, opts SprintOptions) SprintBoard {
	buckets := make([][]SprintItem, opts.Horizon+1)
	var warnings []string

	for _, id := range order {
		task := byID[id]
		at := placed[id]
		item := SprintItem{
			TaskID:       task.ID,
			EpicID:       task.EpicID,
			StoryID:      task.StoryID,
			WorkstreamID: task.WorkstreamID,
			Title:        task.Title,
			HumanHours:   round2(PERT(task.HumanEstimate)),
			AgentMinutes: round2(PERT(task.AgentEstimate)),
			DependsOn:    task.DependsOn,
			Targets:      len(task.Targets),
		}
		if _, ok := opts.Placements[id]; ok {
			item.Moved = true
		}
		for _, dep := range task.DependsOn {
			depAt, known := placed[dep]
			if !known {
				continue
			}
			late := depAt > at
			if at != BacklogSprint && depAt == BacklogSprint {
				late = true
			}
			if late {
				item.BlockedBy = append(item.BlockedBy, dep)
			}
		}
		if len(item.BlockedBy) > 0 {
			sort.Strings(item.BlockedBy)
			warnings = append(warnings, fmt.Sprintf("%s is scheduled before %s, which it depends on",
				item.TaskID, joinIDs(item.BlockedBy)))
		}
		buckets[at] = append(buckets[at], item)
	}

	board.Backlog.Items = buckets[BacklogSprint]
	if board.Backlog.Items == nil {
		board.Backlog.Items = []SprintItem{}
	}
	board.Backlog.HumanHours, board.Backlog.AgentMinutes = totals(board.Backlog.Items)
	board.Backlog.Committed = round2(sumIn(opts.Basis, board.Backlog.Items))

	capacity := opts.capacity()
	for n := 1; n <= opts.Horizon; n++ {
		sprint := Sprint{
			Number:   n,
			Name:     fmt.Sprintf("Sprint %d", n),
			Items:    buckets[n],
			Capacity: round2(capacity),
		}
		if sprint.Items == nil {
			sprint.Items = []SprintItem{}
		}
		sprint.HumanHours, sprint.AgentMinutes = totals(sprint.Items)
		sprint.Committed = round2(sumIn(opts.Basis, sprint.Items))
		sprint.Over = sprint.Committed > sprint.Capacity
		if sprint.Over {
			warnings = append(warnings, fmt.Sprintf("%s holds %s %s against a capacity of %s",
				sprint.Name, fmtNum(sprint.Committed), opts.unit(), fmtNum(sprint.Capacity)))
		}
		board.Sprints = append(board.Sprints, sprint)
	}
	board.Warnings = warnings
	return board
}

// effortIn returns one task's expected effort in the basis unit.
func effortIn(basis string, t contracts.TechnicalTask) float64 {
	if basis == BasisAgent {
		return PERT(t.AgentEstimate)
	}
	return PERT(t.HumanEstimate)
}

func sumIn(basis string, items []SprintItem) float64 {
	var total float64
	for _, it := range items {
		if basis == BasisAgent {
			total += it.AgentMinutes
			continue
		}
		total += it.HumanHours
	}
	return total
}

func totals(items []SprintItem) (hours, minutes float64) {
	for _, it := range items {
		hours += it.HumanHours
		minutes += it.AgentMinutes
	}
	return round2(hours), round2(minutes)
}

func joinIDs(ids []string) string {
	switch len(ids) {
	case 0:
		return ""
	case 1:
		return ids[0]
	}
	return fmt.Sprintf("%s and %s", joinIDs(ids[:len(ids)-1]), ids[len(ids)-1])
}
