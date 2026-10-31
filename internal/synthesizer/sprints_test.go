package synthesizer_test

import (
	"encoding/json"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/synthesizer"
)

// sized is task() with the human estimate pinned to an exact PERT value, so a
// capacity test can say what it means. PERT of (h, h, h) is h.
func sized(id string, hours float64, deps ...string) contracts.TechnicalTask {
	t := task(id, "E1", "ws-a", deps...)
	t.HumanEstimate = contracts.Estimate{Optimistic: hours, Likely: hours, Pessimistic: hours, Basis: "b"}
	t.AgentEstimate = contracts.Estimate{Optimistic: hours * 6, Likely: hours * 6, Pessimistic: hours * 6, Basis: "b"}
	return t
}

// twentyHourSprints is a sprint holding exactly 20 hours, so a test can place
// a known number of known-size tasks in it.
var twentyHourSprints = synthesizer.SprintOptions{
	SprintDays: 2, WorkdayHours: 10, TeamSize: 1, Horizon: 3,
}

// sprintOf returns the sprint number an item landed in, or -1.
func sprintOf(board synthesizer.SprintBoard, id string) int {
	for _, it := range board.Backlog.Items {
		if it.TaskID == id {
			return synthesizer.BacklogSprint
		}
	}
	for _, s := range board.Sprints {
		for _, it := range s.Items {
			if it.TaskID == id {
				return s.Number
			}
		}
	}
	return -1
}

func itemOf(t *testing.T, board synthesizer.SprintBoard, id string) synthesizer.SprintItem {
	t.Helper()
	all := append([]synthesizer.SprintItem{}, board.Backlog.Items...)
	for _, s := range board.Sprints {
		all = append(all, s.Items...)
	}
	for _, it := range all {
		if it.TaskID == id {
			return it
		}
	}
	t.Fatalf("%s is on no sprint and not in the backlog", id)
	return synthesizer.SprintItem{}
}

// The whole point of deriving sprints from the task graph: a dependency is
// never committed after the work that needs it.
func TestAutoFillNeverSchedulesATaskBeforeItsDependency(t *testing.T) {
	tasks := []contracts.TechnicalTask{
		sized("T-1", 8),
		sized("T-2", 8, "T-1"),
		sized("T-3", 8, "T-2"),
		sized("T-4", 8, "T-1"),
		sized("T-5", 8, "T-3", "T-4"),
	}
	board, err := synthesizer.BuildSprints(tasks, twentyHourSprints)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		at := sprintOf(board, task.ID)
		for _, dep := range task.DependsOn {
			depAt := sprintOf(board, dep)
			if depAt > at {
				t.Errorf("%s is in sprint %d but depends on %s in sprint %d", task.ID, at, dep, depAt)
			}
		}
	}
	if len(board.Warnings) != 0 {
		t.Errorf("an auto-filled board must warn about nothing, got %v", board.Warnings)
	}
}

// A sprint holds what it has capacity for and no more.
func TestAutoFillRespectsCapacity(t *testing.T) {
	var tasks []contracts.TechnicalTask
	for _, id := range []string{"T-1", "T-2", "T-3", "T-4", "T-5", "T-6"} {
		tasks = append(tasks, sized(id, 8))
	}
	board, err := synthesizer.BuildSprints(tasks, twentyHourSprints)
	if err != nil {
		t.Fatal(err)
	}
	if board.Capacity != 20 {
		t.Fatalf("capacity = %v, want 20 (2 days x 10 hours x 1 person)", board.Capacity)
	}
	for _, s := range board.Sprints {
		if s.Committed > s.Capacity {
			t.Errorf("%s commits %v against a capacity of %v", s.Name, s.Committed, s.Capacity)
		}
		if len(s.Items) > 2 {
			t.Errorf("%s holds %d items of 8 hours in 20 hours", s.Name, len(s.Items))
		}
	}
	if board.Sprints[0].Committed != 16 {
		t.Errorf("sprint 1 commits %v, want two 8-hour tasks", board.Sprints[0].Committed)
	}
}

// Work past the horizon is not quietly squeezed in: it waits in the backlog.
func TestWorkPastTheHorizonStaysInTheBacklog(t *testing.T) {
	var tasks []contracts.TechnicalTask
	for _, id := range []string{"T-1", "T-2", "T-3", "T-4"} {
		tasks = append(tasks, sized(id, 18))
	}
	opts := twentyHourSprints
	opts.Horizon = 2
	board, err := synthesizer.BuildSprints(tasks, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Sprints) != 2 {
		t.Fatalf("board has %d sprints, want the horizon of 2", len(board.Sprints))
	}
	if len(board.Backlog.Items) != 2 {
		t.Errorf("backlog holds %d items, want the 2 that do not fit", len(board.Backlog.Items))
	}
	if board.Backlog.Committed != 36 {
		t.Errorf("backlog commits %v, want 36", board.Backlog.Committed)
	}
}

// Work that depends on uncommitted work is itself uncommitted. Committing it
// would be a promise the board cannot keep.
func TestWorkDependingOnTheBacklogStaysInTheBacklog(t *testing.T) {
	tasks := []contracts.TechnicalTask{
		sized("T-1", 18),
		sized("T-2", 18),
		sized("T-3", 1, "T-2"), // tiny, but its dependency never got committed
	}
	opts := twentyHourSprints
	opts.Horizon = 1
	board, err := synthesizer.BuildSprints(tasks, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := sprintOf(board, "T-2"); got != synthesizer.BacklogSprint {
		t.Fatalf("T-2 landed in sprint %d; the test needs it in the backlog", got)
	}
	if got := sprintOf(board, "T-3"); got != synthesizer.BacklogSprint {
		t.Errorf("T-3 is in sprint %d although T-2 is in the backlog", got)
	}
}

// The owner outranks the arithmetic: a moved task goes where it was put, and
// the board says it was moved.
func TestAnOverrideIsHonouredAndMarked(t *testing.T) {
	tasks := []contracts.TechnicalTask{sized("T-1", 8), sized("T-2", 8)}
	opts := twentyHourSprints
	opts.Placements = map[string]int{"T-2": 3}
	board, err := synthesizer.BuildSprints(tasks, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := sprintOf(board, "T-2"); got != 3 {
		t.Fatalf("T-2 is in sprint %d, want the sprint it was moved to", got)
	}
	if !itemOf(t, board, "T-2").Moved {
		t.Error("a moved item must say so, so an edit is visible as an edit")
	}
	if itemOf(t, board, "T-1").Moved {
		t.Error("an auto-filled item must not claim it was moved")
	}
}

// An override that puts work before the thing it depends on is allowed, named
// on the item, and reported at the top of the board.
func TestAnOverrideThatBreaksOrderIsReported(t *testing.T) {
	tasks := []contracts.TechnicalTask{sized("T-1", 8), sized("T-2", 8, "T-1")}
	opts := twentyHourSprints
	opts.Placements = map[string]int{"T-1": 3, "T-2": 1}
	board, err := synthesizer.BuildSprints(tasks, opts)
	if err != nil {
		t.Fatal(err)
	}
	item := itemOf(t, board, "T-2")
	if len(item.BlockedBy) != 1 || item.BlockedBy[0] != "T-1" {
		t.Errorf("T-2 blocked_by = %v, want [T-1]", item.BlockedBy)
	}
	if len(board.Warnings) != 1 {
		t.Fatalf("warnings = %v, want one", board.Warnings)
	}
}

// An override past the capacity is honoured and the sprint says it is over.
func TestAnOverSubscribedSprintSaysSo(t *testing.T) {
	tasks := []contracts.TechnicalTask{sized("T-1", 18), sized("T-2", 18)}
	opts := twentyHourSprints
	opts.Placements = map[string]int{"T-1": 1, "T-2": 1}
	board, err := synthesizer.BuildSprints(tasks, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !board.Sprints[0].Over {
		t.Error("36 hours in a 20-hour sprint must be marked over")
	}
	if len(board.Warnings) == 0 {
		t.Error("an over-subscribed sprint must be reported")
	}
}

// One task larger than a whole sprint still lands, in a sprint of its own,
// rather than looping forever past a capacity it can never meet.
func TestATaskLargerThanASprintStillLands(t *testing.T) {
	board, err := synthesizer.BuildSprints([]contracts.TechnicalTask{sized("T-1", 50)}, twentyHourSprints)
	if err != nil {
		t.Fatal(err)
	}
	if got := sprintOf(board, "T-1"); got != 1 {
		t.Errorf("T-1 is in sprint %d, want sprint 1", got)
	}
	if !board.Sprints[0].Over {
		t.Error("a 50-hour task in a 20-hour sprint must be marked over")
	}
}

// Switching basis switches the unit, the capacity and every total. Both
// estimates stay on every item, so the page can switch without a round trip.
func TestAgentBasisCountsMinutes(t *testing.T) {
	tasks := []contracts.TechnicalTask{sized("T-1", 8), sized("T-2", 8)}
	opts := twentyHourSprints
	opts.Basis = synthesizer.BasisAgent
	opts.MaxParallel = 1
	board, err := synthesizer.BuildSprints(tasks, opts)
	if err != nil {
		t.Fatal(err)
	}
	if board.Unit != "minutes" {
		t.Errorf("unit = %q, want minutes", board.Unit)
	}
	if board.Capacity != 1200 {
		t.Errorf("capacity = %v, want 1200 minutes (2 days x 10 hours x 60)", board.Capacity)
	}
	if board.Sprints[0].Committed != 96 {
		t.Errorf("sprint 1 commits %v agent minutes, want 96", board.Sprints[0].Committed)
	}
	item := itemOf(t, board, "T-1")
	if item.HumanHours != 8 || item.AgentMinutes != 48 {
		t.Errorf("item carries human %v / agent %v, want both estimates", item.HumanHours, item.AgentMinutes)
	}
}

// The same plan and options always produce the same board, byte for byte.
func TestTheBoardIsDeterministic(t *testing.T) {
	tasks := []contracts.TechnicalTask{
		sized("T-3", 5), sized("T-1", 5), sized("T-4", 5, "T-1"), sized("T-2", 5, "T-1"),
	}
	first, err := synthesizer.BuildSprints(tasks, twentyHourSprints)
	if err != nil {
		t.Fatal(err)
	}
	second, err := synthesizer.BuildSprints(tasks, twentyHourSprints)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Errorf("two builds of one plan differ:\n%s\n%s", a, b)
	}
}

// A dependency cycle is refused rather than arranged into a board that cannot
// be worked.
func TestACycleIsRefused(t *testing.T) {
	tasks := []contracts.TechnicalTask{
		sized("T-1", 4, "T-2"),
		sized("T-2", 4, "T-1"),
	}
	if _, err := synthesizer.BuildSprints(tasks, twentyHourSprints); err == nil {
		t.Fatal("a dependency cycle must be refused")
	}
}

// An empty plan is an empty board, not an error: a run can be opened before
// there is anything to arrange.
func TestAnEmptyPlanIsAnEmptyBoard(t *testing.T) {
	board, err := synthesizer.BuildSprints(nil, synthesizer.SprintOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Backlog.Items) != 0 || len(board.Sprints) != 0 {
		t.Errorf("empty plan produced %d sprints and %d backlog items", len(board.Sprints), len(board.Backlog.Items))
	}
	if board.Disclaimer == "" {
		t.Error("the board must carry the estimate disclaimer even when empty")
	}
}

// Defaults are the documented ones, and the bounded options are clamped.
func TestOptionsDefaultAndClamp(t *testing.T) {
	board, err := synthesizer.BuildSprints([]contracts.TechnicalTask{sized("T-1", 1)}, synthesizer.SprintOptions{
		Horizon: 99, MaxParallel: 99,
	})
	if err != nil {
		t.Fatal(err)
	}
	if board.Basis != synthesizer.BasisHuman || board.Unit != "hours" {
		t.Errorf("basis defaults to %q/%q, want human/hours", board.Basis, board.Unit)
	}
	if board.SprintDays != synthesizer.DefaultSprintDays || board.TeamSize != synthesizer.DefaultTeamSize {
		t.Errorf("sprint defaults = %d days, team %d", board.SprintDays, board.TeamSize)
	}
	if board.Horizon != synthesizer.MaxHorizon {
		t.Errorf("horizon = %d, want it clamped to %d", board.Horizon, synthesizer.MaxHorizon)
	}
	if board.MaxParallel != synthesizer.MaxScheduleParallel {
		t.Errorf("max_parallel = %d, want it clamped to %d", board.MaxParallel, synthesizer.MaxScheduleParallel)
	}
}
