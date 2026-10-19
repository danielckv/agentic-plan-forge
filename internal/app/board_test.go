//go:build client && !server

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/danielckv/agentic-plan-forge/internal/board"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/planstate"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/storage"
)

const boardSpec = "# Invoice export\n\n## Executive summary\n\nExports invoices.\n"

func boardWrite(t *testing.T, repo, rel, body string) {
	t.Helper()
	p := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// boardCommit writes a two-task plan titled title into repo and, when approved,
// records its approval in the local ledger. It returns the manifest hash.
func boardCommit(t *testing.T, repo, title string, approved bool) string {
	t.Helper()
	targets := []contracts.Target{{Path: "internal/billing/export.go", Operation: contracts.OpAdd}}
	m := contracts.PlanManifest{
		SchemaVersion: contracts.SchemaVersion, ApprovalID: "approval-1", Title: title,
		CreatedAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		Targets:   targets, VerificationCommands: []contracts.VerificationCommand{}, EstimateAssumptions: []string{},
		SpecSHA256: storage.SHA256Hex([]byte(boardSpec)),
		TaskDAG: []contracts.TechnicalTask{
			{
				ID: "T-1", EpicID: "E1", WorkstreamID: "billing", Title: "Export invoices", Steps: []string{"do it"},
				Targets: targets, AcceptanceCriteria: []string{"it works"},
				HumanEstimate: contracts.Estimate{Optimistic: 1, Likely: 2, Pessimistic: 3, Basis: "similar work"},
				AgentEstimate: contracts.Estimate{Optimistic: 5, Likely: 8, Pessimistic: 12, Basis: "one file"},
			},
			{
				ID: "T-2", EpicID: "E1", WorkstreamID: "billing", Title: "Document export", Steps: []string{"write"},
				DependsOn: []string{"T-1"}, AcceptanceCriteria: []string{"documented"},
			},
		},
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	boardWrite(t, repo, planstate.SpecRel, boardSpec)
	boardWrite(t, repo, planstate.ManifestRel, string(raw))
	sum := storage.SHA256Hex(raw)
	if approved {
		if err := planstate.RecordApproval(repo, sum, m.ApprovalID); err != nil {
			t.Fatal(err)
		}
	}
	return sum
}

// boardPlan isolates PLAN_FORGE_HOME and returns a repository with an approved plan.
func boardPlan(t *testing.T) (repo, sha string) {
	t.Helper()
	t.Setenv("PLAN_FORGE_HOME", t.TempDir())
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	boardWrite(t, dir, "internal/billing/invoice.go", "package billing\n\nfunc ListInvoices() {}\n")
	return dir, boardCommit(t, dir, "Invoice export", true)
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func decodeSnapshot(t *testing.T, raw []byte) BoardSnapshot {
	t.Helper()
	var snap BoardSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("snapshot is not JSON: %v\n%s", err, raw)
	}
	return snap
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func assertKeys(t *testing.T, what string, m map[string]any, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := keysOf(m); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s keys = %v, want %v", what, got, want)
	}
}

// The snapshot carries exactly the C5 keys, every collection is a JSON array
// (never null) and the consumers mirror the overlay cursors.
func TestBoardSnapshotShape(t *testing.T) {
	repo, sha := boardPlan(t)
	ev, err := planstate.TaskBoard(context.Background(), repo, planstate.CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := board.Open(sha)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetPriority("T-1", board.PriorityHigh); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddNote("T-2", "use the ledger"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Pull("claude:abc", false); err != nil {
		t.Fatal(err)
	}
	ov, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	snap := buildBoardSnapshot("repo", "Invoice export", sha, "claude", ev, time.Now(), ov)
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("null")) {
		t.Errorf("snapshot contains null: %s", raw)
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	assertKeys(t, "snapshot", top, "repo", "title", "manifest_sha256", "approved", "status", "verified", "total",
		"revision", "host", "evaluated_at", "drift", "tasks", "consumers", "discoveries", "coordinator")
	assertKeys(t, "coordinator", top["coordinator"].(map[string]any), "active", "next_up", "updated_at")
	if got := top["discoveries"].([]any); len(got) != 0 {
		t.Errorf("discoveries = %v, want none", got)
	}
	if top["approved"] != true || top["manifest_sha256"] != sha || top["host"] != "claude" || top["revision"] != float64(2) {
		t.Errorf("snapshot scalars wrong: %v", top)
	}
	tasks := top["tasks"].([]any)
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(tasks))
	}
	t1 := tasks[0].(map[string]any)
	assertKeys(t, "task", t1, "id", "epic_id", "story_id", "workstream_id", "title", "column", "blocked", "depends_on",
		"priority", "targets", "verification", "acceptance_criteria", "human_estimate", "agent_estimate", "notes",
		"live", "awaiting_verification", "discovered")
	assertKeys(t, "live", t1["live"].(map[string]any), "state", "by", "source", "detail", "at")
	if t1["awaiting_verification"] != false || t1["discovered"] != false {
		t.Errorf("T-1 flags = %v", t1)
	}
	if t1["id"] != "T-1" || t1["priority"] != "high" || t1["column"] != "todo" {
		t.Errorf("T-1 = %v", t1)
	}
	assertKeys(t, "target", t1["targets"].([]any)[0].(map[string]any), "path", "operation", "symbol_id", "verified")
	assertKeys(t, "verification", t1["verification"].(map[string]any), "total", "passed")
	assertKeys(t, "estimate", t1["human_estimate"].(map[string]any), "likely", "basis")
	t2 := tasks[1].(map[string]any)
	if t2["blocked"] != true || len(t2["notes"].([]any)) != 1 || t2["priority"] != "normal" {
		t.Errorf("T-2 = %v", t2)
	}
	assertKeys(t, "note", t2["notes"].([]any)[0].(map[string]any), "id", "at", "text")
	consumers := top["consumers"].([]any)
	if len(consumers) != 1 {
		t.Fatalf("consumers = %v", consumers)
	}
	c := consumers[0].(map[string]any)
	assertKeys(t, "consumer", c, "id", "delivered_revision", "seen_at")
	if c["id"] != "claude:abc" || c["delivered_revision"] != float64(2) {
		t.Errorf("consumer = %v", c)
	}

	// A plan that is not the one the board opened with is an empty, unapproved board.
	stale := buildBoardSnapshot("repo", "Invoice export", strings.Repeat("a", 64), "", ev, time.Now(), board.Overlay{})
	rawStale, _ := json.Marshal(stale)
	if bytes.Contains(rawStale, []byte("null")) || stale.Approved || len(stale.Tasks) != 0 {
		t.Errorf("stale snapshot = %s", rawStale)
	}
}

func snapTask(t *testing.T, snap BoardSnapshot, id string) BoardTask {
	t.Helper()
	for _, task := range snap.Tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("task %s is not on the snapshot: %+v", id, snap.Tasks)
	return BoardTask{}
}

// The snapshot's columns, blocked flags and coordinator block come from the
// coordinator, and the accepted task discoveries join the board (contract C11).
func TestBoardSnapshotCoordinatorAndDiscoveries(t *testing.T) {
	repo, sha := boardPlan(t)
	ev, err := planstate.TaskBoard(context.Background(), repo, planstate.CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := board.Open(sha)
	if err != nil {
		t.Fatal(err)
	}
	build := func() BoardSnapshot {
		t.Helper()
		ov, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		snap := buildBoardSnapshot("repo", "Invoice export", sha, "claude", ev, time.Now(), ov)
		raw, _ := json.Marshal(snap)
		if bytes.Contains(raw, []byte("null")) {
			t.Fatalf("snapshot contains null: %s", raw)
		}
		return snap
	}
	must := func(_ board.Overlay, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// Nothing happened yet: T-1 heads the queue, T-2 waits on it.
	snap := build()
	if got := snap.Coordinator.NextUp; len(got) != 1 || got[0] != "T-1" || len(snap.Coordinator.Active) != 0 {
		t.Errorf("idle coordinator = %+v, want next_up [T-1]", snap.Coordinator)
	}
	if t1 := snapTask(t, snap, "T-1"); t1.Live.State != "" || t1.AwaitingVerification || t1.Discovered || t1.Column != "todo" {
		t.Errorf("idle T-1 = %+v", t1)
	}

	// The harness starts T-1: the card moves to in_progress with its live record.
	must(store.RecordActivity("claude:abc", "T-1", board.ActivityActive, board.SourceReport, "working on it"))
	snap = build()
	t1 := snapTask(t, snap, "T-1")
	if t1.Column != "in_progress" || t1.Live.State != board.ActivityActive || t1.Live.By != "claude:abc" ||
		t1.Live.Source != board.SourceReport || t1.Live.Detail != "working on it" || t1.Live.At == "" || t1.AwaitingVerification {
		t.Errorf("active T-1 = %+v", t1)
	}
	if got := snap.Coordinator.Active; len(got) != 1 || got[0] != "T-1" || len(snap.Coordinator.NextUp) != 0 || snap.Coordinator.UpdatedAt == "" {
		t.Errorf("active coordinator = %+v", snap.Coordinator)
	}
	if !snapTask(t, snap, "T-2").Blocked {
		t.Error("T-2 must stay blocked while T-1 is in progress")
	}

	// The harness reports T-1 done before the evidence agrees: done, awaiting verification, T-2 unblocked.
	must(store.RecordActivity("claude:abc", "T-1", board.ActivityDone, board.SourceReport, ""))
	snap = build()
	t1, t2 := snapTask(t, snap, "T-1"), snapTask(t, snap, "T-2")
	if t1.Column != "done" || !t1.AwaitingVerification || t1.Live.State != board.ActivityDone {
		t.Errorf("done T-1 = %+v", t1)
	}
	if t2.Blocked {
		t.Errorf("T-2 is still blocked after T-1 is done: %+v", t2)
	}
	if got := snap.Coordinator.NextUp; len(got) != 1 || got[0] != "T-2" || len(snap.Coordinator.Active) != 0 {
		t.Errorf("coordinator after done = %+v, want next_up [T-2]", snap.Coordinator)
	}

	// An accepted task discovery is a board task; a proposed path discovery is not.
	d1, _, err := store.AddDiscovery("omp:x", board.DiscoveryInput{
		Kind: board.DiscoveryTask, Title: "Handle empty input", ParentTaskID: "T-1",
		Paths: []string{"internal/billing/retry.go"}, Reason: "found while exporting",
	})
	if err != nil {
		t.Fatal(err)
	}
	must(store.ResolveDiscovery(d1.ID, true, board.PriorityHigh))
	d2, _, err := store.AddDiscovery("claude:abc", board.DiscoveryInput{Kind: board.DiscoveryPath, Paths: []string{"docs/other.md"}, ParentTaskID: "T-1"})
	if err != nil {
		t.Fatal(err)
	}
	snap = build()
	if len(snap.Tasks) != 3 || snap.Tasks[2].ID != d1.ID {
		t.Fatalf("tasks = %+v, want the accepted discovery appended last", snap.Tasks)
	}
	dt := snap.Tasks[2]
	if !dt.Discovered || dt.Priority != "high" || dt.Column != "todo" || dt.Blocked || dt.Title != "Handle empty input" ||
		len(dt.DependsOn) != 1 || dt.DependsOn[0] != "T-1" || len(dt.Targets) != 1 || dt.Targets[0].Path != "internal/billing/retry.go" {
		t.Errorf("discovered task = %+v", dt)
	}
	if snapTask(t, snap, "T-1").Discovered || snapTask(t, snap, "T-2").Discovered {
		t.Error("a plan task is marked discovered")
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"operation":""`)) {
		t.Errorf("snapshot contains an empty target operation (the web validator rejects it): %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"operation":"modify"`)) {
		t.Errorf("discovered task target operation is not modify: %s", raw)
	}
	if got := snap.Coordinator.NextUp; len(got) != 2 || got[0] != d1.ID || got[1] != "T-2" {
		t.Errorf("next_up = %v, want [%s T-2] (high priority first)", got, d1.ID)
	}
	if len(snap.Discoveries) != 2 || snap.Discoveries[0].ID != d2.ID || snap.Discoveries[1].ID != d1.ID {
		t.Fatalf("discoveries = %+v, want newest first [%s %s]", snap.Discoveries, d2.ID, d1.ID)
	}
	if got := snap.Discoveries[0]; got.Status != board.DiscoveryProposed || got.Kind != board.DiscoveryPath || got.ParentTaskID != "T-1" || got.By != "claude:abc" {
		t.Errorf("path discovery = %+v", got)
	}
	if got := snap.Discoveries[1]; got.Status != board.DiscoveryAccepted || got.Priority != board.PriorityHigh || got.Reason != "found while exporting" {
		t.Errorf("task discovery = %+v", got)
	}

	// Blocked by the harness.
	must(store.RecordActivity("claude:abc", "T-2", board.ActivityBlocked, board.SourceReport, "needs a decision"))
	snap = build()
	if t2 = snapTask(t, snap, "T-2"); !t2.Blocked || t2.Live.State != board.ActivityBlocked {
		t.Errorf("blocked T-2 = %+v", t2)
	}
	if got := snap.Coordinator.NextUp; len(got) != 1 || got[0] != d1.ID {
		t.Errorf("next_up with T-2 blocked = %v, want [%s]", got, d1.ID)
	}

	// A plan that is not the one the board opened with shows none of it.
	ov, _ := store.Load()
	stale := buildBoardSnapshot("repo", "Invoice export", strings.Repeat("a", 64), "", ev, time.Now(), ov)
	rawStale, _ := json.Marshal(stale)
	if bytes.Contains(rawStale, []byte("null")) || len(stale.Discoveries) != 0 || len(stale.Coordinator.NextUp) != 0 || len(stale.Coordinator.Active) != 0 {
		t.Errorf("stale snapshot = %s", rawStale)
	}
}

// --- HTTP routes -------------------------------------------------------------------------------

type boardHarness struct {
	t     *testing.T
	srv   *boardServer
	token string
	repo  string
	sha   string
	store *board.Store
}

func newBoardHarness(t *testing.T) *boardHarness {
	t.Helper()
	repo, sha := boardPlan(t)
	store, err := board.Open(sha)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := relay.NewBridge(relay.BridgeConfig{LaunchPath: boardLaunchPath})
	if err != nil {
		t.Fatal(err)
	}
	srv := &boardServer{
		repo: repo, repoName: filepath.Base(repo), title: "Invoice export", sha: sha, host: "claude",
		store: store, bridge: bridge, log: quietLogger(), scanTimeout: 10 * time.Second,
	}
	if _, err := srv.evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.routes()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = bridge.Serve(ctx) }()
	t.Cleanup(func() { cancel(); bridge.Close() })
	launch, err := bridge.LaunchURL()
	if err != nil {
		t.Fatal(err)
	}
	_, nonce, _ := strings.Cut(launch, "#n=")
	return &boardHarness{t: t, srv: srv, repo: repo, sha: sha, store: store, token: redeemNonce(t, bridge.Origin(), nonce)}
}

func redeemNonce(t *testing.T, origin, nonce string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"nonce": nonce})
	req, _ := http.NewRequest(http.MethodPost, origin+"/api/session", bytes.NewReader(body))
	req.Header.Set("Origin", origin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Token string `json:"token"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || out.Token == "" {
		t.Fatalf("redeem nonce: status %d", resp.StatusCode)
	}
	return out.Token
}

func (h *boardHarness) do(method, path, body string, auth bool) (int, []byte) {
	h.t.Helper()
	origin := h.srv.bridge.Origin()
	req, err := http.NewRequest(method, origin+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/json")
	if auth {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (h *boardHarness) post(body string) (int, []byte) {
	return h.do(http.MethodPost, "/api/board/task", body, true)
}

func TestBoardAPIRequiresAuth(t *testing.T) {
	h := newBoardHarness(t)
	if code, _ := h.do(http.MethodGet, "/api/board", "", false); code != http.StatusUnauthorized {
		t.Errorf("GET /api/board without a token = %d, want 401", code)
	}
	if code, _ := h.do(http.MethodPost, "/api/board/task", `{"id":"T-1","priority":"high"}`, false); code != http.StatusUnauthorized {
		t.Errorf("POST /api/board/task without a token = %d, want 401", code)
	}
	if ov, _ := h.store.Load(); ov.Revision != 0 {
		t.Errorf("an unauthenticated POST changed the overlay: revision %d", ov.Revision)
	}
	code, raw := h.do(http.MethodGet, "/api/board", "", true)
	if code != http.StatusOK {
		t.Fatalf("GET /api/board = %d: %s", code, raw)
	}
	if snap := decodeSnapshot(t, raw); !snap.Approved || len(snap.Tasks) != 2 || snap.Host != "claude" || snap.Repo != filepath.Base(h.repo) {
		t.Errorf("GET /api/board snapshot = %s", raw)
	}
}

func TestBoardAPIRejectsBadInput(t *testing.T) {
	h := newBoardHarness(t)
	cases := map[string]string{
		"empty object":      `{}`,
		"no action":         `{"id":"T-1"}`,
		"two actions":       `{"id":"T-1","priority":"high","note":"x"}`,
		"three actions":     `{"id":"T-1","priority":"high","note":"x","delete_note":"n-1"}`,
		"unknown field":     `{"id":"T-1","priority":"high","extra":1}`,
		"not json":          `priority=high`,
		"trailing data":     `{"id":"T-1","priority":"high"} {}`,
		"bad priority":      `{"id":"T-1","priority":"urgent"}`,
		"empty note":        `{"id":"T-1","note":"   "}`,
		"missing id":        `{"priority":"high"}`,
		"malformed task id": `{"id":"T 1","priority":"high"}`,
		"oversized body":    `{"id":"T-1","note":"` + strings.Repeat("x", 9<<10) + `"}`,
	}
	for name, body := range cases {
		if code, raw := h.post(body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, code, raw)
		}
	}
	if ov, _ := h.store.Load(); ov.Revision != 0 {
		t.Errorf("a rejected edit changed the overlay: revision %d", ov.Revision)
	}
	if code, _ := h.post(`{"id":"T-99","priority":"high"}`); code != http.StatusNotFound {
		t.Errorf("unknown task = %d, want 404", code)
	}
	// A manifest task the overlay holds no note for is an unknown note (404),
	// not an unknown task.
	for _, body := range []string{`{"id":"T-1","delete_note":"n-9"}`, `{"id":"T-1","note":"keep"}`, `{"id":"T-1","delete_note":"n-9"}`} {
		code, raw := h.post(body)
		wantDelete := strings.Contains(body, "delete_note")
		switch {
		case wantDelete && (code != http.StatusNotFound || !strings.Contains(string(raw), "unknown note") || strings.Contains(string(raw), "unknown task")):
			t.Errorf("%s = %d (%s), want 404 unknown note", body, code, raw)
		case !wantDelete && code != http.StatusOK:
			t.Errorf("%s = %d (%s), want 200", body, code, raw)
		}
	}
}

func TestBoardAPIEditRoundTrip(t *testing.T) {
	h := newBoardHarness(t)
	find := func(snap BoardSnapshot, id string) BoardTask {
		for _, task := range snap.Tasks {
			if task.ID == id {
				return task
			}
		}
		t.Fatalf("task %s missing", id)
		return BoardTask{}
	}

	code, raw := h.post(`{"id":"T-1","priority":"high"}`)
	if code != http.StatusOK {
		t.Fatalf("priority = %d: %s", code, raw)
	}
	snap := decodeSnapshot(t, raw)
	if find(snap, "T-1").Priority != "high" || snap.Revision != 1 {
		t.Errorf("after priority: %s", raw)
	}

	code, raw = h.post(`{"id":"T-2","note":"  use the existing ledger table  "}`)
	if code != http.StatusOK {
		t.Fatalf("note = %d: %s", code, raw)
	}
	snap = decodeSnapshot(t, raw)
	notes := find(snap, "T-2").Notes
	if len(notes) != 1 || notes[0].Text != "use the existing ledger table" || notes[0].ID == "" {
		t.Fatalf("notes after add = %+v", notes)
	}
	noteID := notes[0].ID

	// The edit is visible to GET and to a `board pull` consumer.
	code, raw = h.do(http.MethodGet, "/api/board", "", true)
	if code != http.StatusOK || find(decodeSnapshot(t, raw), "T-2").Notes[0].Text != "use the existing ledger table" {
		t.Errorf("GET after note = %d: %s", code, raw)
	}
	events, _, err := h.store.Pull("claude:round", false)
	if err != nil || len(events) != 2 || events[0].Kind != board.KindPriority || events[1].Kind != board.KindContext {
		t.Errorf("pull = %+v, %v", events, err)
	}

	code, raw = h.post(`{"id":"T-2","delete_note":"` + noteID + `"}`)
	if code != http.StatusOK {
		t.Fatalf("delete_note = %d: %s", code, raw)
	}
	snap = decodeSnapshot(t, raw)
	if got := find(snap, "T-2").Notes; len(got) != 0 || snap.Revision != 3 {
		t.Errorf("after delete: revision %d notes %+v", snap.Revision, got)
	}
	if len(snap.Consumers) != 1 || snap.Consumers[0].ID != "claude:round" || snap.Consumers[0].DeliveredRevision != 2 {
		t.Errorf("consumers = %+v", snap.Consumers)
	}
}

func TestBoardAPIConflictWhenPlanChanges(t *testing.T) {
	h := newBoardHarness(t)
	// A different manifest (also unapproved) replaces the plan the board opened with.
	boardCommit(t, h.repo, "Invoice export v2", false)
	if code, raw := h.post(`{"id":"T-1","priority":"high"}`); code != http.StatusConflict {
		t.Errorf("changed manifest = %d, want 409 (%s)", code, raw)
	}
	if ov, _ := h.store.Load(); ov.Revision != 0 {
		t.Errorf("a conflicting edit changed the overlay: revision %d", ov.Revision)
	}
	// The original manifest is back but its approval is gone: restore it unapproved.
	t.Setenv("PLAN_FORGE_HOME", t.TempDir())
	boardCommit(t, h.repo, "Invoice export", false)
	if code, raw := h.post(`{"id":"T-1","priority":"high"}`); code != http.StatusConflict {
		t.Errorf("unapproved manifest = %d, want 409 (%s)", code, raw)
	}
}

func (h *boardHarness) postDiscovery(body string) (int, []byte) {
	return h.do(http.MethodPost, "/api/board/discovery", body, true)
}

// seedDiscoveries stores one proposed task discovery (D-1) and one proposed
// path discovery (D-2), both reported by an OMP session.
func (h *boardHarness) seedDiscoveries() {
	h.t.Helper()
	if _, _, err := h.store.AddDiscovery("omp:x", board.DiscoveryInput{Kind: board.DiscoveryTask, Title: "Handle empty input", ParentTaskID: "T-1"}); err != nil {
		h.t.Fatal(err)
	}
	if _, _, err := h.store.AddDiscovery("omp:x", board.DiscoveryInput{Kind: board.DiscoveryPath, Paths: []string{"docs/other.md"}}); err != nil {
		h.t.Fatal(err)
	}
}

func snapDiscovery(t *testing.T, snap BoardSnapshot, id string) board.Discovery {
	t.Helper()
	for _, d := range snap.Discoveries {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("discovery %s is not on the snapshot: %+v", id, snap.Discoveries)
	return board.Discovery{}
}

func TestBoardDiscoveryRouteStatusCodes(t *testing.T) {
	h := newBoardHarness(t)
	h.seedDiscoveries()
	if code, _ := h.do(http.MethodPost, "/api/board/discovery", `{"id":"D-1","action":"dismiss"}`, false); code != http.StatusUnauthorized {
		t.Errorf("POST /api/board/discovery without a token = %d, want 401", code)
	}
	bad := map[string]string{
		"empty object":          `{}`,
		"no action":             `{"id":"D-1"}`,
		"unknown action":        `{"id":"D-1","action":"approve"}`,
		"missing id":            `{"action":"accept"}`,
		"task id":               `{"id":"T-1","action":"accept"}`,
		"zero id":               `{"id":"D-0","action":"accept"}`,
		"leading zero id":       `{"id":"D-01","action":"accept"}`,
		"malformed id":          `{"id":"D 1","action":"dismiss"}`,
		"bad priority":          `{"id":"D-1","action":"accept","priority":"urgent"}`,
		"priority with dismiss": `{"id":"D-1","action":"dismiss","priority":"high"}`,
		"unknown field":         `{"id":"D-1","action":"dismiss","extra":1}`,
		"not json":              `action=accept`,
		"trailing data":         `{"id":"D-1","action":"dismiss"} {}`,
		"oversized body":        `{"id":"D-1","action":"accept","priority":"` + strings.Repeat("x", 9<<10) + `"}`,
	}
	for name, body := range bad {
		if code, raw := h.postDiscovery(body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, code, raw)
		}
	}
	ov, _ := h.store.Load()
	if ov.Revision != 2 || ov.Discoveries[0].Status != board.DiscoveryProposed || ov.Discoveries[1].Status != board.DiscoveryProposed {
		t.Errorf("a rejected request changed the overlay: %+v", ov)
	}
	for _, body := range []string{`{"id":"D-99","action":"accept","priority":"high"}`, `{"id":"D-99","action":"dismiss"}`} {
		if code, raw := h.postDiscovery(body); code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 (%s)", body, code, raw)
		}
	}

	// The plan changes under the open board: nothing is written.
	boardCommit(t, h.repo, "Invoice export v2", false)
	if code, raw := h.postDiscovery(`{"id":"D-1","action":"accept","priority":"high"}`); code != http.StatusConflict {
		t.Errorf("changed manifest = %d, want 409 (%s)", code, raw)
	}
	if ov, _ := h.store.Load(); ov.Revision != 2 || ov.Discoveries[0].Status != board.DiscoveryProposed {
		t.Errorf("a conflicting request changed the overlay: %+v", ov)
	}
}

func TestBoardDiscoveryRouteRoundTrip(t *testing.T) {
	h := newBoardHarness(t)
	h.seedDiscoveries()

	// Accept the task discovery with a priority: it becomes a board card.
	code, raw := h.postDiscovery(`{"id":"D-1","action":"accept","priority":"high"}`)
	if code != http.StatusOK {
		t.Fatalf("accept = %d: %s", code, raw)
	}
	snap := decodeSnapshot(t, raw)
	card := snapTask(t, snap, "D-1")
	if !card.Discovered || card.Priority != "high" || card.Column != "todo" || card.Title != "Handle empty input" {
		t.Errorf("accepted card = %+v", card)
	}
	if d := snapDiscovery(t, snap, "D-1"); d.Status != board.DiscoveryAccepted || d.Priority != board.PriorityHigh {
		t.Errorf("accepted discovery = %+v", d)
	}
	if snap.Revision != 3 || len(snap.Discoveries) != 2 {
		t.Errorf("revision %d, %d discoveries", snap.Revision, len(snap.Discoveries))
	}
	events, _, err := h.store.Pull("claude:round", false)
	if err != nil || len(events) != 3 {
		t.Fatalf("pull = %+v, %v", events, err)
	}
	if ev := events[2]; ev.Kind != board.KindDiscoveryAccepted || ev.TaskID != "D-1" || ev.Value != "D-1:high" || ev.By != "user" {
		t.Errorf("accept event = %+v", ev)
	}

	// An accepted discovery is a task the user may re-prioritise.
	if code, raw := h.post(`{"id":"D-1","priority":"low"}`); code != http.StatusOK {
		t.Fatalf("priority on an accepted discovery = %d: %s", code, raw)
	} else if card := snapTask(t, decodeSnapshot(t, raw), "D-1"); card.Priority != "low" {
		t.Errorf("card after priority = %+v", card)
	}

	// Dismissing takes the card off the board and keeps the record.
	code, raw = h.postDiscovery(`{"id":"D-1","action":"dismiss"}`)
	if code != http.StatusOK {
		t.Fatalf("dismiss = %d: %s", code, raw)
	}
	snap = decodeSnapshot(t, raw)
	if d := snapDiscovery(t, snap, "D-1"); d.Status != board.DiscoveryDismissed {
		t.Errorf("dismissed discovery = %+v", d)
	}
	for _, task := range snap.Tasks {
		if task.ID == "D-1" {
			t.Errorf("a dismissed discovery is still a card: %+v", task)
		}
	}
	events, _, _ = h.store.Pull("claude:round", false)
	if len(events) == 0 || events[len(events)-1].Kind != board.KindDiscoveryDismissed || events[len(events)-1].By != "user" {
		t.Errorf("dismiss events = %+v", events)
	}
	if code, _ := h.post(`{"id":"D-1","priority":"high"}`); code != http.StatusNotFound {
		t.Errorf("priority on a dismissed discovery = %d, want 404", code)
	}

	// A path discovery accepted without a priority is normal and adds no card.
	code, raw = h.postDiscovery(`{"id":"D-2","action":"accept"}`)
	if code != http.StatusOK {
		t.Fatalf("accept path = %d: %s", code, raw)
	}
	snap = decodeSnapshot(t, raw)
	if d := snapDiscovery(t, snap, "D-2"); d.Status != board.DiscoveryAccepted || d.Priority != board.PriorityNormal {
		t.Errorf("accepted path discovery = %+v", d)
	}
	if len(snap.Tasks) != 2 {
		t.Errorf("a path discovery added a card: %d tasks", len(snap.Tasks))
	}
	// Resolving it to the state it already has changes nothing.
	rev := snap.Revision
	code, raw = h.postDiscovery(`{"id":"D-2","action":"accept"}`)
	if code != http.StatusOK || decodeSnapshot(t, raw).Revision != rev {
		t.Errorf("repeated accept = %d, revision %d, want 200 and %d", code, decodeSnapshot(t, raw).Revision, rev)
	}
}

// --- window session ----------------------------------------------------------------------------

type fakeBoardWindow struct{ done chan error }

func (f *fakeBoardWindow) Done() <-chan error      { return f.done }
func (f *fakeBoardWindow) Terminate(time.Duration) {}

// uiClient plays the board page: it redeems the launch nonce, opens the bridge
// socket, authenticates and forwards every text frame it receives.
type uiClient struct {
	frames chan string
	closeC func()
}

func launchUI(t *testing.T, launchURL string, urls chan<- string) *uiClient {
	t.Helper()
	urls <- launchURL
	u, err := neturl.Parse(launchURL)
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + u.Host
	token := redeemNonce(t, origin, strings.TrimPrefix(u.Fragment, "n="))
	ctx, cancel := context.WithCancel(context.Background())
	conn, _, err := websocket.Dial(ctx, "ws://"+u.Host+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {origin}}, Subprotocols: []string{relay.WSSubprotocol},
	})
	if err != nil {
		cancel()
		t.Fatalf("dial: %v", err)
	}
	auth, _ := json.Marshal(map[string]string{"type": "auth", "token": token})
	if err := conn.Write(ctx, websocket.MessageText, auth); err != nil {
		cancel()
		t.Fatalf("auth: %v", err)
	}
	c := &uiClient{frames: make(chan string, 64), closeC: func() { _ = conn.Close(websocket.StatusNormalClosure, "bye"); cancel() }}
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			c.frames <- string(data)
		}
	}()
	t.Cleanup(c.closeC)
	return c
}

// next returns the first snapshot frame accepted by ok within timeout.
func (c *uiClient) next(t *testing.T, timeout time.Duration, ok func(BoardSnapshot) bool) BoardSnapshot {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case raw := <-c.frames:
			var f struct {
				Type     string        `json:"type"`
				Snapshot BoardSnapshot `json:"snapshot"`
			}
			if json.Unmarshal([]byte(raw), &f) == nil && f.Type == "board.snapshot" && ok(f.Snapshot) {
				return f.Snapshot
			}
		case <-deadline:
			t.Fatal("no matching board.snapshot frame arrived")
		}
	}
}

func TestRunBoardPushesChangesAndExitsWhenWindowCloses(t *testing.T) {
	repo, sha := boardPlan(t)
	urls := make(chan string, 1)
	clients := make(chan *uiClient, 1)
	win := &fakeBoardWindow{done: make(chan error)}
	result := make(chan Result, 1)
	go func() {
		result <- RunBoard(context.Background(), BoardOptions{
			Repo: repo, Host: "claude", Logger: quietLogger(),
			Grace: 200 * time.Millisecond, StartupWait: 20 * time.Second,
			OverlayPoll: 50 * time.Millisecond, EvidenceEvery: 200 * time.Millisecond,
			LaunchWindow: func(url string) (windowHandle, error) {
				go func() { clients <- launchUI(t, url, urls) }()
				return win, nil
			},
		})
	}()

	var launch string
	select {
	case launch = <-urls:
	case <-time.After(30 * time.Second):
		t.Fatal("the window was never launched")
	}
	if !strings.Contains(launch, "/board#n=") {
		t.Errorf("launch URL %q does not open the board view", launch)
	}
	ui := <-clients

	first := ui.next(t, 10*time.Second, func(BoardSnapshot) bool { return true })
	if !first.Approved || len(first.Tasks) != 2 || first.Tasks[0].Column != "todo" || first.Tasks[0].Priority != "normal" {
		t.Fatalf("first snapshot = %+v", first)
	}
	if holder, ok := BoardHolder(repo); !ok || holder != os.Getpid() {
		t.Errorf("board lock holder = %d, %v; want this process", holder, ok)
	}

	// An edit made by another process (a hook, the CLI) reaches the window through the overlay poll.
	other, err := board.Open(sha)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.SetPriority("T-1", board.PriorityHigh); err != nil {
		t.Fatal(err)
	}
	ui.next(t, 10*time.Second, func(s BoardSnapshot) bool { return s.Tasks[0].Priority == "high" && s.Revision == 1 })

	// New evidence in the repository reaches it through the evidence loop.
	boardWrite(t, repo, "internal/billing/export.go", "package billing\n\nfunc Export() {}\n")
	ui.next(t, 20*time.Second, func(s BoardSnapshot) bool { return s.Tasks[0].Column != "todo" })

	// Closing the window ends the board with exit 0 and frees the lock.
	ui.closeC()
	select {
	case res := <-result:
		if res.ExitCode != ExitOK {
			t.Errorf("RunBoard after window close = %+v, want exit 0", res)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RunBoard did not end after the window closed")
	}
	if _, ok := BoardHolder(repo); ok {
		t.Error("the board lock outlived the board")
	}
}

func TestRunBoardPrerequisites(t *testing.T) {
	t.Setenv("PLAN_FORGE_HOME", t.TempDir())
	empty, _ := filepath.EvalSymlinks(t.TempDir())
	res := RunBoard(context.Background(), BoardOptions{Repo: empty, Logger: quietLogger()})
	if res.ExitCode != ExitPrerequisite || res.Message != BoardMessageNoPlan {
		t.Errorf("no plan: %+v", res)
	}
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	boardWrite(t, repo, "a.go", "package a\n")
	boardCommit(t, repo, "Unapproved", false)
	if res := RunBoard(context.Background(), BoardOptions{Repo: repo, Logger: quietLogger()}); res.ExitCode != ExitPrerequisite {
		t.Errorf("unapproved plan: %+v", res)
	}
	if res := RunBoard(context.Background(), BoardOptions{Repo: repo, Host: "agy"}); res.ExitCode != ExitUsage || res.Message != BoardMessageHost {
		t.Errorf("--host agy: %+v", res)
	}
}

func TestBoardSingleInstanceLock(t *testing.T) {
	repo, _ := boardPlan(t)
	path, err := boardLockPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	// A live holder other than this process (a real child, not this test's parent): the board is already open.
	holder := spawnLiveHolder(t)
	if err := writeBoardLock(path, boardLock{PID: holder, State: boardRunning, Repo: repo}); err != nil {
		t.Fatal(err)
	}
	res := RunBoard(context.Background(), BoardOptions{Repo: repo, Logger: quietLogger()})
	if res.ExitCode != ExitOK || !strings.Contains(res.Message, "already open") {
		t.Errorf("RunBoard with a live holder = %+v", res)
	}
	det := StartBoardDetached(context.Background(), BoardOptions{Repo: repo})
	if !det.AlreadyOpen || det.PID != holder || det.ExitCode != ExitOK {
		t.Errorf("StartBoardDetached with a live holder = %+v", det)
	}
	if l, ok := readBoardLock(path); !ok || l.PID != holder {
		t.Errorf("the live holder's lock was disturbed: %+v %v", l, ok)
	}

	// A holder that is gone is stale and replaced.
	if err := writeBoardLock(path, boardLock{PID: 2147483646, State: boardRunning, Repo: repo}); err != nil {
		t.Fatal(err)
	}
	pid, held, err := acquireBoardLock(path, repo)
	if err != nil || !held || pid != os.Getpid() {
		t.Errorf("acquire over a stale lock = %d %v %v", pid, held, err)
	}
	releaseBoardLock(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("release left the lock behind: %v", err)
	}
}

// TestBoardLockHolderHelper is the sleeping process spawnLiveHolder starts; it
// does nothing in a normal test run.
func TestBoardLockHolderHelper(t *testing.T) {
	if os.Getenv("PLAN_FORGE_TEST_LOCK_HOLDER") != "1" {
		t.Skip("helper process of the board lock tests")
	}
	time.Sleep(time.Minute)
}

// spawnLiveHolder starts a sleeping child process, killed when the test ends,
// and returns its pid: a live process other than this one and its parent.
func spawnLiveHolder(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestBoardLockHolderHelper$")
	cmd.Env = append(os.Environ(), "PLAN_FORGE_TEST_LOCK_HOLDER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the live holder: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// Between one contender's stale check and its removal another contender can
// finish its own takeover. The first must notice, leave the new holder's lock
// alone and report that holder.
func TestBoardLockTakeoverRereadsUnderGuard(t *testing.T) {
	repo, _ := boardPlan(t)
	path, err := boardLockPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	holder := spawnLiveHolder(t)
	if err := writeBoardLock(path, boardLock{PID: 2147483646, State: boardRunning, Repo: repo}); err != nil {
		t.Fatal(err)
	}
	afterStaleCheck = func() {
		afterStaleCheck = nil
		if err := writeBoardLock(path, boardLock{PID: holder, State: boardRunning, Repo: repo}); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterStaleCheck = nil })

	pid, held, err := acquireBoardLock(path, repo)
	if err != nil || held || pid != holder {
		t.Errorf("acquire = pid %d held %v err %v; want the new holder %d and no takeover", pid, held, err, holder)
	}
	if l, ok := readBoardLock(path); !ok || l.PID != holder {
		t.Errorf("the new holder's lock was removed: %+v %v", l, ok)
	}
}

// The guard is a cross-handle exclusive lock: a second taker waits for the first.
func TestBoardGuardSerializes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock.guard")
	unlock, err := lockBoardGuard(path)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		u, err := lockBoardGuard(path)
		if err != nil {
			t.Error(err)
			return
		}
		close(acquired)
		u()
	}()
	select {
	case <-acquired:
		t.Fatal("a second guard was taken while the first was held")
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("the guard was not released")
	}
}

// --- review fixes: push order, first evaluation, evaluation goroutines, OnConnect ---------------

// An older snapshot is never sent after a newer one: a push that builds while
// another push is mid-send waits for it.
func TestBoardPushIsSerialized(t *testing.T) {
	h := newBoardHarness(t)
	var (
		mu       sync.Mutex
		revs     []uint64
		calls    atomic.Int32
		entered  = make(chan struct{})
		second   = make(chan struct{})
		release  = make(chan struct{})
		done1    = make(chan struct{})
		done2    = make(chan struct{})
		recorded = func(frame []byte) {
			var f struct {
				Snapshot BoardSnapshot `json:"snapshot"`
			}
			if err := json.Unmarshal(frame, &f); err != nil {
				t.Error(err)
			}
			mu.Lock()
			revs = append(revs, f.Snapshot.Revision)
			mu.Unlock()
		}
	)
	h.srv.send = func(frame []byte) error {
		switch calls.Add(1) {
		case 1:
			close(entered)
			<-release
		case 2:
			close(second)
		}
		recorded(frame)
		return nil
	}

	go func() { h.srv.push(true); close(done1) }()
	<-entered
	if _, err := h.store.SetPriority("T-1", board.PriorityHigh); err != nil {
		t.Fatal(err)
	}
	go func() { h.srv.push(false); close(done2) }()
	// Unserialized, the second push sends while the first is stuck in its send.
	select {
	case <-second:
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	<-done1
	<-done2

	mu.Lock()
	defer mu.Unlock()
	if len(revs) != 2 || revs[0] != 0 || revs[1] != 1 {
		t.Errorf("snapshot revisions sent = %v, want [0 1]: an older snapshot followed a newer one", revs)
	}
}

// syncBuffer is an io.Writer safe for a logger used from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startBoardUI runs RunBoard against a fake window and returns the page that
// connected to it and the channel RunBoard's result arrives on.
func startBoardUI(t *testing.T, repo string, tweak func(*BoardOptions)) (*uiClient, <-chan Result) {
	t.Helper()
	urls := make(chan string, 1)
	clients := make(chan *uiClient, 1)
	win := &fakeBoardWindow{done: make(chan error)}
	result := make(chan Result, 1)
	opts := BoardOptions{
		Repo: repo, Host: "claude", Logger: quietLogger(),
		Grace: 200 * time.Millisecond, StartupWait: 20 * time.Second,
		OverlayPoll: 50 * time.Millisecond, EvidenceEvery: 100 * time.Millisecond,
		LaunchWindow: func(url string) (windowHandle, error) {
			go func() { clients <- launchUI(t, url, urls) }()
			return win, nil
		},
	}
	if tweak != nil {
		tweak(&opts)
	}
	go func() { result <- RunBoard(context.Background(), opts) }()
	select {
	case <-urls:
	case r := <-result:
		t.Fatalf("RunBoard ended before the window launched: %+v", r)
	case <-time.After(30 * time.Second):
		t.Fatal("the window was never launched")
	}
	return <-clients, result
}

// A first evaluation that cannot finish does not abort the board: the window
// opens on an UNKNOWN, empty snapshot, the failure is logged and the evidence
// loop retries.
func TestRunBoardFirstEvaluationFailureStillOpens(t *testing.T) {
	cases := map[string]error{
		"unknown status": planstate.ErrStatusUnknown,
		"scan deadline":  fmt.Errorf("scan: %w", context.DeadlineExceeded),
	}
	for name, firstErr := range cases {
		t.Run(name, func(t *testing.T) {
			repo, sha := boardPlan(t)
			logs := &syncBuffer{}
			gate := make(chan struct{})
			var calls atomic.Int32
			ui, result := startBoardUI(t, repo, func(o *BoardOptions) {
				o.Logger = slog.New(slog.NewTextHandler(logs, nil))
				o.taskBoard = func(ctx context.Context, repo string, opts planstate.CheckOptions) (planstate.TaskBoardResult, error) {
					if calls.Add(1) == 1 {
						return planstate.TaskBoardResult{}, firstErr
					}
					select {
					case <-gate:
					case <-ctx.Done():
						return planstate.TaskBoardResult{}, ctx.Err()
					}
					return planstate.TaskBoard(ctx, repo, opts)
				}
			})

			first := ui.next(t, 10*time.Second, func(BoardSnapshot) bool { return true })
			if first.Status != string(planstate.StatusUnknown) || first.Approved || first.Tasks == nil || len(first.Tasks) != 0 ||
				first.ManifestSHA256 != sha || first.Title != "Invoice export" {
				t.Errorf("first snapshot = %+v, want an UNKNOWN empty board of this plan", first)
			}
			if !strings.Contains(logs.String(), "first evidence evaluation") || !strings.Contains(logs.String(), firstErr.Error()) {
				t.Errorf("the failed first evaluation was not logged: %q", logs.String())
			}

			close(gate)
			next := ui.next(t, 20*time.Second, func(s BoardSnapshot) bool { return s.Approved })
			if len(next.Tasks) != 2 {
				t.Errorf("after the retry the board has %d tasks, want 2", len(next.Tasks))
			}
			ui.closeC()
			select {
			case res := <-result:
				if res.ExitCode != ExitOK {
					t.Errorf("RunBoard after window close = %+v, want exit 0", res)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("RunBoard did not end after the window closed")
			}
		})
	}
}

// RunBoard does not return while an evidence evaluation it started is running.
func TestRunBoardWaitsForEvaluations(t *testing.T) {
	repo, _ := boardPlan(t)
	var calls, inFlight atomic.Int32
	started := make(chan struct{}, 1)
	ui, result := startBoardUI(t, repo, func(o *BoardOptions) {
		o.taskBoard = func(ctx context.Context, repo string, opts planstate.CheckOptions) (planstate.TaskBoardResult, error) {
			if calls.Add(1) == 1 {
				return planstate.TaskBoard(ctx, repo, opts)
			}
			inFlight.Add(1)
			defer inFlight.Add(-1)
			select {
			case started <- struct{}{}:
			default:
			}
			time.Sleep(800 * time.Millisecond) // ignores ctx on purpose: bounded work RunBoard must outwait
			return planstate.TaskBoard(ctx, repo, opts)
		}
	})
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the evidence loop never started an evaluation")
	}
	ui.closeC()
	select {
	case res := <-result:
		if res.ExitCode != ExitOK {
			t.Errorf("RunBoard after window close = %+v, want exit 0", res)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RunBoard did not end after the window closed")
	}
	if n := inFlight.Load(); n != 0 {
		t.Errorf("RunBoard returned with %d evaluation(s) still running", n)
	}
}

// A slow OnConnect (a push waiting on the board lock) must not hold up the
// window supervisor.
func TestStartWindowOnConnectDoesNotBlockSupervisor(t *testing.T) {
	bridge, err := relay.NewBridge(relay.BridgeConfig{Assets: UIAssets(), LaunchPath: boardLaunchPath})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = bridge.Serve(ctx) }()
	t.Cleanup(func() { cancel(); bridge.Close() })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	connected := make(chan struct{}, 1)
	urls := make(chan string, 1)
	clients := make(chan *uiClient, 1)
	win := &fakeBoardWindow{done: make(chan error)}
	_, outcome, err := startWindow(ctx, windowParams{
		Bridge: bridge, Grace: 100 * time.Millisecond, StartupWait: 20 * time.Second,
		Launch: func(url string) (windowHandle, error) {
			go func() { clients <- launchUI(t, url, urls) }()
			return win, nil
		},
		OnConnect: func() {
			select {
			case connected <- struct{}{}:
			default:
			}
			<-release
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("OnConnect was never called")
	}
	ui := <-clients
	ui.closeC()
	select {
	case r := <-outcome:
		if r.Phase != PhaseCancelled {
			t.Errorf("outcome = %+v, want the window-closed result", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a blocked OnConnect stalled the window supervisor")
	}
}

// A store write from another process (a hook, `board activity`) reaches the
// window through the 1 s overlay poll alone: the evidence loop is parked.
func TestRunBoardPushesHarnessWritesWithinAboutASecond(t *testing.T) {
	repo, sha := boardPlan(t)
	ui, result := startBoardUI(t, repo, func(o *BoardOptions) {
		o.OverlayPoll = 0 // the contract's 1 s
		o.EvidenceEvery = time.Hour
	})
	ui.next(t, 10*time.Second, func(BoardSnapshot) bool { return true })
	other, err := board.Open(sha)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if _, err := other.RecordActivity("claude:abc", "T-1", board.ActivityActive, board.SourceFileEdit, "internal/billing/export.go"); err != nil {
		t.Fatal(err)
	}
	got := ui.next(t, 4*time.Second, func(s BoardSnapshot) bool { return s.Tasks[0].Live.State == board.ActivityActive })
	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
		t.Errorf("harness activity took %v to reach the window, want about 1s", elapsed)
	}
	if got.Tasks[0].Column != "in_progress" || len(got.Coordinator.Active) != 1 || got.Coordinator.Active[0] != "T-1" {
		t.Errorf("pushed snapshot = %+v", got)
	}

	if _, _, err := other.AddDiscovery("claude:abc", board.DiscoveryInput{Kind: board.DiscoveryPath, Paths: []string{"docs/other.md"}, ParentTaskID: "T-1"}); err != nil {
		t.Fatal(err)
	}
	pushed := ui.next(t, 4*time.Second, func(s BoardSnapshot) bool { return len(s.Discoveries) == 1 })
	if d := pushed.Discoveries[0]; d.ID != "D-1" || d.Status != board.DiscoveryProposed || d.Paths[0] != "docs/other.md" {
		t.Errorf("pushed discovery = %+v", d)
	}

	ui.closeC()
	select {
	case res := <-result:
		if res.ExitCode != ExitOK {
			t.Errorf("RunBoard after window close = %+v, want exit 0", res)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RunBoard did not end after the window closed")
	}
}
