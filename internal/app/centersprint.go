//go:build client && !server

package app

import (
	"context"
	"errors"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/board"
	"github.com/danielckv/agentic-plan-forge/internal/center"
	"github.com/danielckv/agentic-plan-forge/internal/planstate"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
)

// centerSprintTimeout bounds one repository's evidence scan. The command
// center refreshes on its own schedule rather than inside an agent hook, so it
// can afford a real scan, but a pathological repository must not stall the
// response for every other one.
const centerSprintTimeout = 10 * time.Second

// CenterSprint derives the plan view of one repository for the command center.
//
// It is the same computation the board window performs, reused rather than
// reimplemented: planstate.TaskBoard turns the approved manifest plus the
// repository's AST and verification receipts into per-task evidence, and
// board.Coordinate folds the machine-local overlay over it to produce the live
// columns and the queue. Deriving "what should begin next" anywhere else would
// be a second ranking that could disagree with the board.
func CenterSprint(ctx context.Context, repoPath string) (center.SprintView, error) {
	ctx, cancel := context.WithTimeout(ctx, centerSprintTimeout)
	defer cancel()

	ev, err := planstate.TaskBoard(ctx, repoPath, planstate.CheckOptions{})
	if err != nil {
		return center.SprintView{}, err
	}
	if !ev.Status.Present || !ev.Status.Approved {
		return center.SprintView{}, center.ErrNoPlan
	}

	ov, err := loadCenterOverlay(ev.Status.ManifestSHA256)
	if err != nil {
		// An unreadable overlay costs the live columns, not the whole view:
		// the structural evidence alone is still worth showing.
		ov = board.Overlay{}
	}

	order := make([]string, 0, len(ev.Tasks))
	for _, te := range ev.Tasks {
		order = append(order, te.ID)
	}
	co := board.Coordinate(ev.Tasks, ov, order)

	view := center.SprintView{
		Title:     sanitize.Terminal(ev.Status.Title),
		Approved:  true,
		Tasks:     make([]center.SprintTask, 0, len(ev.Tasks)),
		Active:    orEmpty(co.Active),
		NextUp:    orEmpty(co.NextUp),
		Blocked:   []string{},
		Awaiting:  []string{},
		Verified:  ev.Status.Verified,
		Total:     ev.Status.Total,
		Drifted:   len(ev.Status.Drift) > 0,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	for _, te := range ev.Tasks {
		column := te.Column
		if live, ok := co.Columns[te.ID]; ok {
			column = live
		}
		met := 0
		for _, t := range te.Targets {
			if t.Verified {
				met++
			}
		}
		view.Tasks = append(view.Tasks, center.SprintTask{
			ID:         te.ID,
			Title:      sanitize.Terminal(te.Title),
			EpicID:     te.EpicID,
			StoryID:    te.StoryID,
			Column:     column,
			Priority:   string(ov.Priority(te.ID)),
			Blocked:    co.Blocked[te.ID],
			DependsOn:  orEmpty(te.DependsOn),
			Targets:    len(te.Targets),
			TargetsMet: met,
			Verified:   te.Verification.Passed,
		})
		if co.Blocked[te.ID] {
			view.Blocked = append(view.Blocked, te.ID)
		}
		if co.AwaitingVerification[te.ID] {
			view.Awaiting = append(view.Awaiting, te.ID)
		}
	}
	return view, nil
}

// loadCenterOverlay reads the board overlay for a manifest without creating it.
func loadCenterOverlay(sha string) (board.Overlay, error) {
	if sha == "" {
		return board.Overlay{}, errors.New("app: empty manifest digest")
	}
	store, err := board.Open(sha)
	if err != nil {
		return board.Overlay{}, err
	}
	return store.Load()
}

func orEmpty(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
