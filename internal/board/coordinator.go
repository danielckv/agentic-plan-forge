//go:build client && !server && cgo

// This file imports planstate, whose AST dependency (tree-sitter) needs cgo, so
// it carries the cgo constraint: a CGO_ENABLED=0 cross-vet of the rest of the
// package (GOOS=windows go vet) still type-checks the store and its lock files.
// Every real client build has cgo.

package board

import (
	"sort"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/planstate"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
)

// MaxNextUp is the length of the coordinator's "Up next" queue.
const MaxNextUp = 5

// Coordination is the coordinator's view of the board: the live column of every
// task, merged from the structural evidence and the harness activity (contract
// C9). Columns, Blocked and AwaitingVerification hold one entry per task,
// including the accepted task discoveries, which are also returned as
// ExtraTasks so a server can render them next to the plan's tasks.
type Coordination struct {
	// Columns maps a task id to its live column: todo, in_progress, done or
	// drifted.
	Columns map[string]string `json:"columns"`
	// Blocked maps a task id to whether it waits on a dependency that is not
	// done on the live columns, or the harness reported it blocked.
	Blocked map[string]bool `json:"blocked"`
	// AwaitingVerification marks tasks the harness reported done while the
	// structural evidence is not done yet.
	AwaitingVerification map[string]bool `json:"awaiting_verification"`
	// Active lists the tasks the harness is working on, newest activity first.
	Active []string `json:"active"`
	// NextUp is the queue of tasks to start, at most MaxNextUp.
	NextUp []string `json:"next_up"`
	// UpdatedAt is the newest overlay timestamp that can change the
	// coordination; it is empty when the overlay holds none.
	UpdatedAt string `json:"updated_at"`
	// ExtraTasks are the accepted task discoveries as board tasks (id D-<n>),
	// in id order, with Column and Blocked already live.
	ExtraTasks []planstate.TaskEvidence `json:"-"`
}

func priorityRank(p Priority) int {
	switch p {
	case PriorityHigh:
		return 0
	case PriorityLow:
		return 2
	}
	return 1
}

// liveColumn merges the structural column of a task with its harness activity,
// in this order: drifted evidence is drifted; done evidence is done; activity
// done is done and awaits verification; activity active or in-progress evidence
// is in progress; anything else is todo.
func liveColumn(evidence string, act Activity, hasAct bool) (column string, awaiting bool) {
	switch {
	case evidence == planstate.ColumnDrifted:
		return planstate.ColumnDrifted, false
	case evidence == planstate.ColumnDone:
		return planstate.ColumnDone, false
	case hasAct && act.State == ActivityDone:
		return planstate.ColumnDone, true
	case hasAct && act.State == ActivityActive, evidence == planstate.ColumnInProgress:
		return planstate.ColumnInProgress, false
	}
	return planstate.ColumnTodo, false
}

// Coordinate computes the live board from the evidence tasks and the overlay.
// It is pure and deterministic: it reads no clock, and UpdatedAt comes from the
// overlay's own timestamps.
//
// Accepted task discoveries join the plan as tasks D-<n> in column todo — or
// the column their activity gives — depending on their parent task when one is
// set. The queue NextUp holds the tasks that are not done, not drifted, not
// blocked and not active, ordered by priority (high, normal, low), then by
// manifestOrder, with accepted discoveries after the plan's tasks of the same
// priority. Ids missing from manifestOrder follow in the order of tasks.
func Coordinate(tasks []planstate.TaskEvidence, ov Overlay, manifestOrder []string) Coordination {
	all := make([]planstate.TaskEvidence, 0, len(tasks)+len(ov.Discoveries))
	isExtra := map[string]bool{}
	seen := map[string]bool{}
	for _, t := range tasks {
		if seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		all = append(all, t)
	}
	var extras []Discovery
	for _, d := range ov.Discoveries {
		if d.Kind == DiscoveryTask && d.Status == DiscoveryAccepted && !seen[d.ID] {
			extras = append(extras, d)
		}
	}
	sort.Slice(extras, func(i, j int) bool {
		a, _ := discoveryNum(extras[i].ID)
		b, _ := discoveryNum(extras[j].ID)
		return a < b
	})
	for _, d := range extras {
		if isExtra[d.ID] {
			continue
		}
		seen[d.ID], isExtra[d.ID] = true, true
		row := planstate.TaskEvidence{
			ID:                 d.ID,
			Title:              sanitize.Truncate(sanitize.Terminal(d.Title), MaxDiscoveryTitleRunes),
			DependsOn:          []string{},
			Column:             planstate.ColumnTodo,
			Targets:            []planstate.TargetEvidence{},
			AcceptanceCriteria: []string{},
		}
		if d.ParentTaskID != "" {
			row.DependsOn = []string{d.ParentTaskID}
		}
		for _, p := range d.Paths {
			// Discovered work touches files the plan did not name, so "modify"
			// is the honest operation; an empty operation fails web validation.
			row.Targets = append(row.Targets, planstate.TargetEvidence{Path: p, Operation: contracts.OpModify})
		}
		all = append(all, row)
	}

	pos := make(map[string]int, len(all))
	for _, id := range manifestOrder {
		if _, dup := pos[id]; !dup {
			pos[id] = len(pos)
		}
	}
	for _, t := range all {
		if _, ok := pos[t.ID]; !ok {
			pos[t.ID] = len(pos)
		}
	}

	c := Coordination{
		Columns:              make(map[string]string, len(all)),
		Blocked:              make(map[string]bool, len(all)),
		AwaitingVerification: make(map[string]bool, len(all)),
		Active:               []string{},
		NextUp:               []string{},
		ExtraTasks:           []planstate.TaskEvidence{},
	}
	for _, t := range all {
		act, has := ov.Activity[t.ID]
		c.Columns[t.ID], c.AwaitingVerification[t.ID] = liveColumn(t.Column, act, has)
	}
	for _, t := range all {
		blocked := false
		if act, ok := ov.Activity[t.ID]; ok && act.State == ActivityBlocked {
			blocked = true
		}
		for _, dep := range t.DependsOn {
			if c.Columns[dep] != planstate.ColumnDone {
				blocked = true
				break
			}
		}
		c.Blocked[t.ID] = blocked
	}

	active := map[string]bool{}
	for _, t := range all {
		if act, ok := ov.Activity[t.ID]; ok && act.State == ActivityActive && c.Columns[t.ID] != planstate.ColumnDone {
			active[t.ID] = true
			c.Active = append(c.Active, t.ID)
		}
	}
	sort.SliceStable(c.Active, func(i, j int) bool {
		a, b := ov.Activity[c.Active[i]].At, ov.Activity[c.Active[j]].At
		if a != b {
			return a > b
		}
		return pos[c.Active[i]] < pos[c.Active[j]]
	})

	for _, t := range all {
		col := c.Columns[t.ID]
		if col == planstate.ColumnDone || col == planstate.ColumnDrifted || c.Blocked[t.ID] || active[t.ID] {
			continue
		}
		c.NextUp = append(c.NextUp, t.ID)
	}
	sort.SliceStable(c.NextUp, func(i, j int) bool {
		a, b := c.NextUp[i], c.NextUp[j]
		if ra, rb := priorityRank(ov.Priority(a)), priorityRank(ov.Priority(b)); ra != rb {
			return ra < rb
		}
		if isExtra[a] != isExtra[b] {
			return !isExtra[a]
		}
		return pos[a] < pos[b]
	})
	if len(c.NextUp) > MaxNextUp {
		c.NextUp = c.NextUp[:MaxNextUp]
	}

	for _, t := range all {
		if !isExtra[t.ID] {
			continue
		}
		row := t
		row.Column = c.Columns[t.ID]
		row.Blocked = c.Blocked[t.ID]
		c.ExtraTasks = append(c.ExtraTasks, row)
	}
	c.UpdatedAt = newestStamp(ov)
	return c
}

// newestStamp returns the newest of the overlay's activity, discovery and event
// timestamps, or "" when it holds none that parse.
func newestStamp(ov Overlay) string {
	best, bestAt := "", time.Time{}
	consider := func(s string) {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return
		}
		if best == "" || t.After(bestAt) {
			best, bestAt = s, t
		}
	}
	for _, a := range ov.Activity {
		consider(a.At)
	}
	for _, d := range ov.Discoveries {
		consider(d.At)
	}
	for _, e := range ov.Events {
		consider(e.At)
	}
	return best
}
