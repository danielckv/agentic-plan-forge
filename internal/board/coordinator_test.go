//go:build client && !server && cgo

package board

import (
	"reflect"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/planstate"
)

func evTask(id, column string, deps ...string) planstate.TaskEvidence {
	return planstate.TaskEvidence{ID: id, Title: "title " + id, Column: column, DependsOn: deps}
}

func act(state ActivityState, at string) Activity {
	return Activity{State: state, By: "omp:x", Source: SourceReport, At: at}
}

func overlayWith(activity map[string]Activity) Overlay {
	o := empty(testSHA)
	for id, a := range activity {
		o.Activity[id] = a
	}
	return o
}

func TestCoordinateColumnRules(t *testing.T) {
	const at = "2026-10-19T10:00:00Z"
	cases := []struct {
		name     string
		evidence string
		activity *Activity
		column   string
		awaiting bool
	}{
		{"todo", "todo", nil, "todo", false},
		{"in_progress evidence", "in_progress", nil, "in_progress", false},
		{"done evidence", "done", nil, "done", false},
		{"drifted evidence", "drifted", nil, "drifted", false},
		{"activity active on todo", "todo", &Activity{State: ActivityActive, At: at}, "in_progress", false},
		{"activity done on todo", "todo", &Activity{State: ActivityDone, At: at}, "done", true},
		{"activity done on in_progress", "in_progress", &Activity{State: ActivityDone, At: at}, "done", true},
		{"activity blocked on todo stays todo", "todo", &Activity{State: ActivityBlocked, At: at}, "todo", false},
		{"activity blocked on in_progress stays in_progress", "in_progress", &Activity{State: ActivityBlocked, At: at}, "in_progress", false},
		{"drifted beats activity done", "drifted", &Activity{State: ActivityDone, At: at}, "drifted", false},
		{"drifted beats activity active", "drifted", &Activity{State: ActivityActive, At: at}, "drifted", false},
		{"done evidence beats activity active", "done", &Activity{State: ActivityActive, At: at}, "done", false},
		{"done evidence is not awaiting", "done", &Activity{State: ActivityDone, At: at}, "done", false},
	}
	for _, c := range cases {
		ov := empty(testSHA)
		if c.activity != nil {
			ov.Activity["T-1"] = *c.activity
		}
		got := Coordinate([]planstate.TaskEvidence{evTask("T-1", c.evidence)}, ov, nil)
		if got.Columns["T-1"] != c.column || got.AwaitingVerification["T-1"] != c.awaiting {
			t.Errorf("%s: column %q awaiting %v, want %q %v", c.name, got.Columns["T-1"], got.AwaitingVerification["T-1"], c.column, c.awaiting)
		}
	}
}

func TestCoordinateBlockedUsesLiveColumns(t *testing.T) {
	const at = "2026-10-19T10:00:00Z"
	tasks := []planstate.TaskEvidence{
		evTask("T-1", "todo"),
		evTask("T-2", "todo", "T-1"), // dependency not done: blocked
		evTask("T-3", "todo", "T-4"), // dependency done only by activity: unblocked
		evTask("T-4", "todo"),
		evTask("T-5", "todo", "T-ghost"),    // unknown dependency: blocked
		evTask("T-6", "todo"),               // activity blocked: blocked
		evTask("T-7", "done", "T-1"),        // a done task with an open dependency is flagged like planstate does
		evTask("T-8", "todo", "T-9", "T-1"), // one open dependency is enough
		evTask("T-9", "done"),
		evTask("T-10", "todo", "T-11"), // dependency drifted: blocked
		evTask("T-11", "drifted"),
	}
	ov := overlayWith(map[string]Activity{
		"T-4": act(ActivityDone, at),
		"T-6": act(ActivityBlocked, at),
	})
	got := Coordinate(tasks, ov, nil)
	want := map[string]bool{
		"T-1": false, "T-2": true, "T-3": false, "T-4": false, "T-5": true, "T-6": true,
		"T-7": true, "T-8": true, "T-9": false, "T-10": true, "T-11": false,
	}
	if !reflect.DeepEqual(got.Blocked, want) {
		t.Fatalf("blocked = %v\nwant %v", got.Blocked, want)
	}
	if !got.AwaitingVerification["T-4"] || got.Columns["T-4"] != "done" {
		t.Fatalf("T-4 = %q awaiting %v", got.Columns["T-4"], got.AwaitingVerification["T-4"])
	}
}

func TestCoordinateActiveIsNewestFirst(t *testing.T) {
	tasks := []planstate.TaskEvidence{
		evTask("T-1", "todo"), evTask("T-2", "todo"), evTask("T-3", "in_progress"),
		evTask("T-4", "done"), evTask("T-5", "todo"), evTask("T-6", "todo"),
	}
	ov := overlayWith(map[string]Activity{
		"T-1":     act(ActivityActive, "2026-10-19T10:00:00Z"),
		"T-2":     act(ActivityActive, "2026-10-19T10:05:00Z"),
		"T-3":     act(ActivityDone, "2026-10-19T10:09:00Z"),   // not active
		"T-4":     act(ActivityActive, "2026-10-19T10:08:00Z"), // done by evidence: not "active now"
		"T-5":     act(ActivityActive, "2026-10-19T10:05:00Z"), // ties with T-2: manifest order
		"T-6":     act(ActivityBlocked, "2026-10-19T10:07:00Z"),
		"T-ghost": act(ActivityActive, "2026-10-19T10:30:00Z"), // not a task of the board
	})
	got := Coordinate(tasks, ov, nil)
	if want := []string{"T-2", "T-5", "T-1"}; !reflect.DeepEqual(got.Active, want) {
		t.Fatalf("active = %v, want %v", got.Active, want)
	}
	if got.Columns["T-1"] != "in_progress" || got.Columns["T-3"] != "done" {
		t.Fatalf("columns = %v", got.Columns)
	}
	if got := Coordinate(nil, empty(testSHA), nil); got.Active == nil || got.NextUp == nil || got.ExtraTasks == nil || len(got.Active)+len(got.NextUp) != 0 {
		t.Fatalf("empty coordination = %+v", got)
	}
}

func TestCoordinateNextUpOrdering(t *testing.T) {
	tasks := []planstate.TaskEvidence{
		evTask("T-1", "todo"),
		evTask("T-2", "todo"),
		evTask("T-3", "todo"),
		evTask("T-4", "todo"),
		evTask("T-5", "done"),        // done: out
		evTask("T-6", "drifted"),     // drifted: out
		evTask("T-7", "todo", "T-1"), // blocked: out
		evTask("T-8", "todo"),        // active: out
		evTask("T-9", "in_progress"), // partial evidence, nobody on it: eligible
		evTask("T-10", "todo"),
		evTask("T-11", "todo"),
	}
	ov := overlayWith(map[string]Activity{"T-8": act(ActivityActive, "2026-10-19T10:00:00Z")})
	ov.Tasks["T-3"] = TaskOverlay{Priority: PriorityHigh}
	ov.Tasks["T-4"] = TaskOverlay{Priority: PriorityLow}
	ov.Tasks["T-10"] = TaskOverlay{Priority: PriorityHigh}
	ov.Tasks["T-5"] = TaskOverlay{Priority: PriorityHigh}

	// Without a manifest order, the order of tasks decides.
	got := Coordinate(tasks, ov, nil)
	if want := []string{"T-3", "T-10", "T-1", "T-2", "T-9"}; !reflect.DeepEqual(got.NextUp, want) {
		t.Fatalf("next_up = %v, want %v", got.NextUp, want)
	}
	// A manifest order reorders inside a priority.
	order := []string{"T-10", "T-2", "T-1", "T-3", "T-4", "T-9"}
	got = Coordinate(tasks, ov, order)
	if want := []string{"T-10", "T-3", "T-2", "T-1", "T-9"}; !reflect.DeepEqual(got.NextUp, want) {
		t.Fatalf("next_up with order = %v, want %v", got.NextUp, want)
	}
	// The queue holds five at most.
	for i := range 6 {
		tasks = append(tasks, evTask("X-"+string(rune('a'+i)), "todo"))
	}
	if got = Coordinate(tasks, ov, order); len(got.NextUp) != MaxNextUp {
		t.Fatalf("next_up = %v", got.NextUp)
	}
}

func TestCoordinateAcceptedDiscoveriesBecomeTasks(t *testing.T) {
	tasks := []planstate.TaskEvidence{evTask("T-1", "todo"), evTask("T-2", "todo")}
	ov := empty(testSHA)
	ov.Discoveries = []Discovery{
		{ID: "D-1", Kind: DiscoveryTask, Title: "accepted child", Paths: []string{"a/b.go"}, ParentTaskID: "T-1", Status: DiscoveryAccepted, Priority: PriorityHigh, At: "2026-10-19T09:00:00Z"},
		{ID: "D-2", Kind: DiscoveryTask, Title: "proposed", Status: DiscoveryProposed, Priority: PriorityNormal, At: "2026-10-19T09:01:00Z"},
		{ID: "D-3", Kind: DiscoveryTask, Title: "dismissed", Status: DiscoveryDismissed, Priority: PriorityNormal, At: "2026-10-19T09:02:00Z"},
		{ID: "D-4", Kind: DiscoveryPath, Title: "a path", Paths: []string{"x.go"}, Status: DiscoveryAccepted, Priority: PriorityNormal, At: "2026-10-19T09:03:00Z"},
		{ID: "D-5", Kind: DiscoveryTask, Title: "free\x1b[0m task", Status: DiscoveryAccepted, Priority: PriorityNormal, At: "2026-10-19T09:04:00Z"},
		{ID: "D-6", Kind: DiscoveryTask, Title: "worked on", ParentTaskID: "", Status: DiscoveryAccepted, Priority: PriorityLow, At: "2026-10-19T09:05:00Z"},
	}
	ov.Tasks["D-1"] = TaskOverlay{Priority: PriorityHigh}
	ov.Tasks["D-6"] = TaskOverlay{Priority: PriorityLow}
	ov.Activity["D-6"] = act(ActivityActive, "2026-10-19T09:10:00Z")

	got := Coordinate(tasks, ov, []string{"T-1", "T-2"})
	ids := []string{}
	for _, x := range got.ExtraTasks {
		ids = append(ids, x.ID)
	}
	if want := []string{"D-1", "D-5", "D-6"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("extra tasks = %v, want %v", ids, want)
	}
	d1 := got.ExtraTasks[0]
	if d1.Title != "accepted child" || !reflect.DeepEqual(d1.DependsOn, []string{"T-1"}) || d1.Column != "todo" || !d1.Blocked {
		t.Fatalf("D-1 = %+v", d1)
	}
	if len(d1.Targets) != 1 || d1.Targets[0].Path != "a/b.go" || d1.Targets[0].Verified {
		t.Fatalf("D-1 targets = %+v", d1.Targets)
	}
	if d1.Targets[0].Operation != "modify" {
		t.Fatalf("D-1 target operation = %q, want modify (the web validator rejects \"\")", d1.Targets[0].Operation)
	}
	if d1.AcceptanceCriteria == nil || d1.Targets == nil {
		t.Fatal("extra task slices must not be nil")
	}
	d5 := got.ExtraTasks[1]
	if d5.Title != "free [0m task" || len(d5.DependsOn) != 0 || d5.Column != "todo" || d5.Blocked {
		t.Fatalf("D-5 = %+v", d5)
	}
	d6 := got.ExtraTasks[2]
	if d6.Column != "in_progress" || d6.Blocked {
		t.Fatalf("D-6 = %+v", d6)
	}
	if got.Columns["D-1"] != "todo" || got.Columns["D-6"] != "in_progress" || got.Columns["D-2"] != "" || got.Columns["D-4"] != "" {
		t.Fatalf("columns = %v", got.Columns)
	}
	if want := []string{"D-6"}; !reflect.DeepEqual(got.Active, want) {
		t.Fatalf("active = %v", got.Active)
	}
	// D-1 is blocked on T-1; D-5 is free but behind the plan's tasks.
	if want := []string{"T-1", "T-2", "D-5"}; !reflect.DeepEqual(got.NextUp, want) {
		t.Fatalf("next_up = %v, want %v", got.NextUp, want)
	}

	// Once the parent is done, the child is unblocked and its priority puts it first.
	tasks[0].Column = "done"
	got = Coordinate(tasks, ov, []string{"T-1", "T-2"})
	if want := []string{"D-1", "T-2", "D-5"}; !reflect.DeepEqual(got.NextUp, want) {
		t.Fatalf("next_up after parent done = %v, want %v", got.NextUp, want)
	}
	if got.ExtraTasks[0].Blocked {
		t.Fatal("D-1 still blocked")
	}

	// A discovery whose id matches a plan task does not shadow it.
	tasks = append(tasks, evTask("D-1", "todo"))
	if got = Coordinate(tasks, ov, nil); len(got.ExtraTasks) != 2 {
		t.Fatalf("extra tasks = %+v", got.ExtraTasks)
	}
}

func TestCoordinateIsPureAndDeterministic(t *testing.T) {
	tasks := []planstate.TaskEvidence{
		evTask("T-1", "todo"), evTask("T-2", "todo", "T-1"), evTask("T-3", "in_progress"),
	}
	ov := overlayWith(map[string]Activity{
		"T-1": act(ActivityActive, "2026-10-19T10:00:00Z"),
		"T-3": act(ActivityDone, "2026-10-19T10:02:00Z"),
	})
	ov.Discoveries = []Discovery{{ID: "D-1", Kind: DiscoveryTask, Title: "x", Status: DiscoveryAccepted, Priority: PriorityHigh, At: "2026-10-19T10:03:00Z"}}
	ov.Events = []Event{{Seq: 1, At: "2026-10-19T10:04:00Z", TaskID: "T-1", Kind: KindPriority, Value: "high"}}
	snapshot := append([]planstate.TaskEvidence(nil), tasks...)
	order := []string{"T-3", "T-2", "T-1"}

	a := Coordinate(tasks, ov, order)
	for range 20 {
		if b := Coordinate(tasks, ov, order); !reflect.DeepEqual(a, b) {
			t.Fatalf("not deterministic:\n%+v\n%+v", a, b)
		}
	}
	if !reflect.DeepEqual(tasks, snapshot) {
		t.Fatalf("input tasks mutated: %+v", tasks)
	}
	if a.UpdatedAt != "2026-10-19T10:04:00Z" {
		t.Fatalf("updated_at = %q, want the newest overlay timestamp", a.UpdatedAt)
	}
	ov.Activity["T-1"] = act(ActivityActive, "2026-10-19T10:30:00+00:00")
	if got := Coordinate(tasks, ov, order); got.UpdatedAt != "2026-10-19T10:30:00+00:00" {
		t.Fatalf("updated_at = %q", got.UpdatedAt)
	}
	if got := Coordinate(tasks, empty(testSHA), nil); got.UpdatedAt != "" {
		t.Fatalf("updated_at of an empty overlay = %q", got.UpdatedAt)
	}
}
