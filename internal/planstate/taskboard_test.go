//go:build client && !server

package planstate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/planstate"
	"github.com/danielckv/agentic-plan-forge/internal/storage"
)

func boardTask(id, title string, deps []string, targets []contracts.Target, v contracts.VerificationCommand) contracts.TechnicalTask {
	return contracts.TechnicalTask{
		ID: id, EpicID: "E1", StoryID: "S1", WorkstreamID: "billing", Title: title, Steps: []string{"do it"},
		DependsOn: deps, Targets: targets, AcceptanceCriteria: []string{"it works"},
		Verification:  []contracts.VerificationCommand{v},
		HumanEstimate: contracts.Estimate{Optimistic: 1, Likely: 4, Pessimistic: 6, Basis: "similar work"},
		AgentEstimate: contracts.Estimate{Optimistic: 5, Likely: 12, Pessimistic: 20, Basis: "one file"},
	}
}

func addTarget(path string) contracts.Target {
	return contracts.Target{Path: path, Operation: contracts.OpAdd}
}

func taskReceipt(t *testing.T, repo, taskID string, v contracts.VerificationCommand) {
	t.Helper()
	fp, err := planstate.Fingerprint(context.Background(), repo, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = storage.WriteReceipt(context.Background(), repo, storage.Receipt{
		TaskID: taskID, VerificationID: v.ID, CommandSHA256: planstate.CommandHash(v), SourceFingerprint: fp,
		ExitCode: 0, ExpectedExitCode: v.ExpectedExitCode, OutputSHA256: "00", At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func byID(res planstate.TaskBoardResult, id string) planstate.TaskEvidence {
	for _, te := range res.Tasks {
		if te.ID == id {
			return te
		}
	}
	return planstate.TaskEvidence{}
}

func TestTaskBoardColumns(t *testing.T) {
	isolate(t)
	repo := newRepo(t)
	m := structuralManifest(t, repo, verification("V-0", "true"))

	v1, v2, v3, v4 := verification("V-1", "true"), verification("V-2", "true"), verification("V-3", "true"), verification("V-4", "true")
	hostile := "export\x1b]0;pwned\x07 invoices"
	m.TaskDAG = []contracts.TechnicalTask{
		boardTask("T-1", hostile, nil, []contracts.Target{addTarget("internal/billing/todo.go")}, v1),
		boardTask("T-2", "in progress", nil, []contracts.Target{addTarget("internal/billing/progress.go")}, v2),
		boardTask("T-3", "done", nil, []contracts.Target{addTarget("internal/billing/done.go")}, v3),
		boardTask("T-4", "drifted", nil, []contracts.Target{{Path: "internal/billing/gone.go", Operation: contracts.OpModify}}, v4),
		boardTask("T-5", "blocked by todo", []string{"T-1"}, nil, verification("V-5", "true")),
		boardTask("T-6", "after done", []string{"T-3"}, nil, verification("V-6", "true")),
	}
	m.Targets = nil
	for _, task := range m.TaskDAG {
		m.Targets = append(m.Targets, task.Targets...)
	}
	m.VerificationCommands = []contracts.VerificationCommand{v1, v2, v3, v4}
	commit(t, repo, m, specText, true)

	write(t, repo, "internal/billing/progress.go", "package billing\n", 0o644)
	write(t, repo, "internal/billing/done.go", "package billing\n", 0o644)
	taskReceipt(t, repo, "T-3", v3)

	res, err := planstate.TaskBoard(context.Background(), repo, planstate.CheckOptions{})
	if err != nil {
		t.Fatalf("TaskBoard: %v", err)
	}
	if !res.Status.Approved || len(res.Tasks) != 6 {
		t.Fatalf("approved=%v tasks=%d", res.Status.Approved, len(res.Tasks))
	}
	want := map[string]string{
		"T-1": planstate.ColumnTodo, "T-2": planstate.ColumnInProgress, "T-3": planstate.ColumnDone,
		"T-4": planstate.ColumnDrifted, "T-5": planstate.ColumnTodo, "T-6": planstate.ColumnTodo,
	}
	for id, col := range want {
		if got := byID(res, id).Column; got != col {
			t.Errorf("%s column = %q, want %q", id, got, col)
		}
	}
	if !byID(res, "T-5").Blocked {
		t.Error("T-5 depends on a todo task and must be blocked")
	}
	if byID(res, "T-6").Blocked || byID(res, "T-1").Blocked {
		t.Error("T-6 (dependency done) and T-1 (no dependency) must not be blocked")
	}

	t3 := byID(res, "T-3")
	if t3.Verification.Total != 1 || t3.Verification.Passed != 1 || len(t3.Targets) != 1 || !t3.Targets[0].Verified {
		t.Errorf("T-3 evidence: %+v", t3)
	}
	t2 := byID(res, "T-2")
	if t2.Verification.Passed != 0 || !t2.Targets[0].Verified {
		t.Errorf("T-2 evidence: %+v", t2)
	}
	if byID(res, "T-1").Targets[0].Verified {
		t.Error("T-1 target does not exist and must not be verified")
	}
	if t3.EpicID != "E1" || t3.StoryID != "S1" || t3.WorkstreamID != "billing" ||
		t3.HumanEstimate.Likely != 4 || t3.AgentEstimate.Basis != "one file" ||
		len(t3.AcceptanceCriteria) != 1 || t3.Targets[0].Operation != "add" {
		t.Errorf("T-3 metadata: %+v", t3)
	}
	if title := byID(res, "T-1").Title; strings.ContainsAny(title, "\x1b\x07") || !strings.Contains(title, "pwned") {
		t.Errorf("hostile title must be sanitized, not dropped: %q", title)
	}
	if res.Tasks[0].DependsOn == nil || res.Tasks[0].Targets == nil {
		t.Error("slices must be non-nil")
	}
}

func TestTaskBoardUnapprovedHasNoTasks(t *testing.T) {
	isolate(t)
	repo := newRepo(t)
	commit(t, repo, structuralManifest(t, repo, verification("V-1", "true")), specText, false)

	res, err := planstate.TaskBoard(context.Background(), repo, planstate.CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status.Approved || len(res.Tasks) != 0 || res.Tasks == nil {
		t.Fatalf("unapproved manifest must yield approved=false and an empty task list: %+v", res)
	}
}

func TestTaskBoardNoManifest(t *testing.T) {
	isolate(t)
	res, err := planstate.TaskBoard(context.Background(), newRepo(t), planstate.CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status.Present || len(res.Tasks) != 0 {
		t.Fatalf("no manifest, no tasks: %+v", res)
	}
}

// One evaluation takes one bounded scan, and the plan status and the per-task
// evidence both come from it. The repository changes right after that scan: a
// second scan would see the finished work and disagree with the status.
func TestTaskBoardScansOnce(t *testing.T) {
	isolate(t)
	repo := newRepo(t)
	v := verification("V-1", "true")
	commit(t, repo, structuralManifest(t, repo, v), specText, true)

	scans, restore := planstate.InstrumentScan(func() { completeStructure(t, repo) })
	defer restore()

	res, err := planstate.TaskBoard(context.Background(), repo, planstate.CheckOptions{})
	if err != nil {
		t.Fatalf("TaskBoard: %v", err)
	}
	if got := scans(); got != 1 {
		t.Fatalf("TaskBoard scanned %d times, want 1", got)
	}
	verified := 0
	for _, tg := range byID(res, "T-1").Targets {
		if tg.Verified {
			verified++
		}
	}
	// The graph predates the work: not every target is verified yet, and the
	// status counts exactly the targets the task cards show as verified.
	if res.Status.Total != 3 || res.Status.Verified != verified || verified == res.Status.Total {
		t.Errorf("status verified %d/%d but %d task targets verified: totals and columns must share one graph",
			res.Status.Verified, res.Status.Total, verified)
	}
	if col := byID(res, "T-1").Column; col == planstate.ColumnDone {
		t.Errorf("T-1 column = %q, but the graph predates the work", col)
	}

	// The next evaluation sees the finished work, again through a single scan.
	res, err = planstate.TaskBoard(context.Background(), repo, planstate.CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := scans(); got != 2 {
		t.Errorf("two evaluations scanned %d times, want 2", got)
	}
	if res.Status.Verified != 3 || res.Status.Total != 3 {
		t.Errorf("second evaluation status = %d/%d, want 3/3", res.Status.Verified, res.Status.Total)
	}
	for _, tg := range byID(res, "T-1").Targets {
		if !tg.Verified {
			t.Errorf("second evaluation: target %s not verified", tg.Path)
		}
	}
}

// A plan that is not trusted is never scanned, by the status check or the board.
func TestTaskBoardUntrustedPlanIsNotScanned(t *testing.T) {
	isolate(t)
	repo := newRepo(t)
	m := structuralManifest(t, repo, verification("V-1", "true"))
	m.Targets[0].Path = "../outside.go"
	m.TaskDAG[0].Targets = m.Targets
	commit(t, repo, m, specText, true)

	scans, restore := planstate.InstrumentScan(nil)
	defer restore()
	res, err := planstate.TaskBoard(context.Background(), repo, planstate.CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := scans(); got != 0 {
		t.Errorf("an untrusted plan was scanned %d times", got)
	}
	if res.Status.Status != planstate.StatusDrifted || len(res.Tasks) != 1 || res.Tasks[0].Column != planstate.ColumnDrifted {
		t.Errorf("untrusted plan result = %+v", res)
	}
}
