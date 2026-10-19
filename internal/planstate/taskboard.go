//go:build client && !server

package planstate

import (
	"context"
	"sort"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
)

// Board columns (C5).
const (
	ColumnTodo       = "todo"
	ColumnInProgress = "in_progress"
	ColumnDone       = "done"
	ColumnDrifted    = "drifted"
)

// TargetEvidence is one task target with its structural verdict.
type TargetEvidence struct {
	Path      string `json:"path"`
	Operation string `json:"operation"`
	SymbolID  string `json:"symbol_id"`
	Verified  bool   `json:"verified"`
}

// TaskVerification counts a task's verification commands and those with a
// current successful receipt.
type TaskVerification struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
}

// EstimateEvidence is the likely value and basis of an estimate.
type EstimateEvidence struct {
	Likely float64 `json:"likely"`
	Basis  string  `json:"basis"`
}

// TaskEvidence is the per-task view of the plan. Every string is sanitized.
type TaskEvidence struct {
	ID                 string           `json:"id"`
	EpicID             string           `json:"epic_id"`
	StoryID            string           `json:"story_id"`
	WorkstreamID       string           `json:"workstream_id"`
	Title              string           `json:"title"`
	DependsOn          []string         `json:"depends_on"`
	Column             string           `json:"column"`
	Blocked            bool             `json:"blocked"`
	Targets            []TargetEvidence `json:"targets"`
	Verification       TaskVerification `json:"verification"`
	AcceptanceCriteria []string         `json:"acceptance_criteria"`
	HumanEstimate      EstimateEvidence `json:"human_estimate"`
	AgentEstimate      EstimateEvidence `json:"agent_estimate"`
}

// TaskBoardResult is the plan status plus per-task evidence. An unapproved
// manifest is never evaluated: Status.Approved is false and Tasks is empty.
type TaskBoardResult struct {
	Status PlanStatus     `json:"status"`
	Tasks  []TaskEvidence `json:"tasks"`
}

func cleanStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, sanitize.Terminal(s))
	}
	return out
}

// TaskBoard evaluates every task of the approved plan against the repository
// and receipts, using the evidence functions CheckStateWith uses. The plan
// status and every task come from one bounded scan, one manifest read and one
// receipt read, so totals and columns always agree.
//
// Column rules: drifted when a drift reason concerns one of the task's targets
// or verification commands; done when every structural target is verified,
// every verification command has a current successful receipt and the task has
// at least one structural target or command; in_progress when some evidence
// exists but not all; todo otherwise. Blocked is true when any dependency is
// not done (an unknown dependency counts as not done).
//
// When the plan is drifted before any evidence can be trusted (invalid paths,
// hash mismatch), tasks are listed as drifted without evidence.
func TaskBoard(ctx context.Context, repo string, opts CheckOptions) (TaskBoardResult, error) {
	var seen checkEvidence
	st, err := checkStateWith(ctx, repo, opts, &seen)
	if err != nil {
		return TaskBoardResult{}, err
	}
	res := TaskBoardResult{Status: st, Tasks: []TaskEvidence{}}
	res.Status.Title = sanitize.Terminal(st.Title)
	res.Status.ApprovalID = sanitize.Terminal(st.ApprovalID)
	res.Status.Pending = cleanStrings(st.Pending)
	res.Status.Notes = cleanStrings(st.Notes)
	for i := range res.Status.Drift {
		res.Status.Drift[i].Detail = sanitize.Terminal(res.Status.Drift[i].Detail)
	}
	if !st.Present || !st.Approved {
		return res, nil
	}
	m := seen.art.manifest
	tasks := append([]contracts.TechnicalTask(nil), m.TaskDAG...)
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })

	// No graph means the status check stopped before scanning (invalid
	// manifest, spec/manifest hash mismatch or invalid target path): nothing
	// the manifest says is trusted, so its tasks are listed as drifted without
	// evidence.
	g, rcpts := seen.g, seen.receipts
	untrusted := g == nil

	for _, t := range tasks {
		te := TaskEvidence{
			ID: sanitize.Terminal(t.ID), EpicID: sanitize.Terminal(t.EpicID), StoryID: sanitize.Terminal(t.StoryID),
			WorkstreamID: sanitize.Terminal(t.WorkstreamID), Title: sanitize.Terminal(t.Title),
			DependsOn: cleanStrings(t.DependsOn), Targets: []TargetEvidence{},
			Verification:       TaskVerification{Total: len(t.Verification)},
			AcceptanceCriteria: cleanStrings(t.AcceptanceCriteria),
			HumanEstimate:      EstimateEvidence{Likely: t.HumanEstimate.Likely, Basis: sanitize.Terminal(t.HumanEstimate.Basis)},
			AgentEstimate:      EstimateEvidence{Likely: t.AgentEstimate.Likely, Basis: sanitize.Terminal(t.AgentEstimate.Basis)},
		}
		if untrusted {
			for _, tg := range t.Targets {
				te.Targets = append(te.Targets, TargetEvidence{
					Path: sanitize.Terminal(tg.Path), Operation: sanitize.Terminal(string(tg.Operation)),
					SymbolID: sanitize.Terminal(tg.SymbolID),
				})
			}
			te.Column = ColumnDrifted
			res.Tasks = append(res.Tasks, te)
			continue
		}
		all := evaluateStructure(st.Repo, g, t.Targets)
		for _, tg := range t.Targets {
			one := evaluateStructure(st.Repo, g, []contracts.Target{tg})
			te.Targets = append(te.Targets, TargetEvidence{
				Path: sanitize.Terminal(tg.Path), Operation: sanitize.Terminal(string(tg.Operation)),
				SymbolID: sanitize.Terminal(tg.SymbolID), Verified: one.total > 0 && one.verified == one.total && len(one.drift) == 0,
			})
		}
		one := m
		one.TaskDAG = []contracts.TechnicalTask{t}
		bh := evaluateBehavior(one, rcpts, g.WorktreeHash)
		te.Verification.Passed = bh.successes

		complete := all.verified == all.total && bh.successes == len(t.Verification)
		switch {
		case len(all.drift) > 0 || len(bh.drift) > 0:
			te.Column = ColumnDrifted
		case complete && all.total+len(t.Verification) > 0:
			te.Column = ColumnDone
		case all.verified > 0 || bh.successes > 0:
			te.Column = ColumnInProgress
		default:
			te.Column = ColumnTodo
		}
		res.Tasks = append(res.Tasks, te)
	}

	done := map[string]bool{}
	for _, te := range res.Tasks {
		done[te.ID] = te.Column == ColumnDone
	}
	for i := range res.Tasks {
		for _, dep := range res.Tasks[i].DependsOn {
			if !done[dep] {
				res.Tasks[i].Blocked = true
				break
			}
		}
	}
	return res, nil
}
