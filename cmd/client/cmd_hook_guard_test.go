//go:build client && !server

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/planstate"
	"github.com/danielckv/agentic-plan-forge/internal/storage"
)

// guardPayload renders a PreToolUse hook payload.
func guardPayload(t *testing.T, repo, session, tool, path string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse",
		"session_id":      session,
		"cwd":             repo,
		"tool_name":       tool,
		"tool_input":      map[string]any{"file_path": path},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// runGuard runs the guard against a payload and returns its exit code and the
// decision it printed, if any.
func runGuard(t *testing.T, payload string) (int, map[string]any) {
	t.Helper()
	var out bytes.Buffer
	code := runHookClaudeGuard(context.Background(), strings.NewReader(payload), &out, hookGuardBound)
	if out.Len() == 0 {
		return code, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not JSON: %q", out.String())
	}
	return code, decoded
}

// decisionOf pulls permissionDecision out of a guard decision.
func decisionOf(t *testing.T, got map[string]any) (string, string) {
	t.Helper()
	spec, ok := got["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("no hookSpecificOutput in %v", got)
	}
	if spec["hookEventName"] != "PreToolUse" {
		t.Errorf("hookEventName = %v", spec["hookEventName"])
	}
	decision, _ := spec["permissionDecision"].(string)
	reason, _ := spec["permissionDecisionReason"].(string)
	return decision, reason
}

// The guard must never block work in a repository the forge has not planned.
func TestGuardIsSilentWithoutAnApprovedPlan(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("PLAN_FORGE_HOME", filepath.Join(t.TempDir(), "home"))
	code, got := runGuard(t, guardPayload(t, repo, "s1", "Edit", filepath.Join(repo, "main.go")))
	if code != exitOK {
		t.Errorf("exit = %d, want 0", code)
	}
	if got != nil {
		t.Errorf("an unplanned repository was given a decision: %v", got)
	}
}

func TestGuardIgnoresEveryOtherEvent(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("PLAN_FORGE_HOME", filepath.Join(t.TempDir(), "home"))
	for _, event := range []string{"PostToolUse", "UserPromptSubmit", "SessionStart", ""} {
		raw, err := json.Marshal(map[string]any{
			"hook_event_name": event, "session_id": "s1", "cwd": repo, "tool_name": "Edit",
		})
		if err != nil {
			t.Fatal(err)
		}
		code, got := runGuard(t, string(raw))
		if code != exitOK || got != nil {
			t.Errorf("%q: exit %d, decision %v", event, code, got)
		}
	}
}

// Whatever arrives on stdin, the guard exits 0 and stays quiet: a hook that
// fails must not be the reason a tool call fails.
func TestGuardAlwaysExitsZero(t *testing.T) {
	t.Setenv("PLAN_FORGE_HOME", filepath.Join(t.TempDir(), "home"))
	for name, payload := range map[string]string{
		"empty":       "",
		"not json":    "{",
		"null":        "null",
		"no event":    `{"session_id":"s1"}`,
		"wrong types": `{"hook_event_name":5,"session_id":[1]}`,
		"oversized":   `{"hook_event_name":"PreToolUse","cwd":"` + strings.Repeat("x", hookClaudeMaxStdin) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			code, got := runGuard(t, payload)
			if code != exitOK {
				t.Errorf("exit = %d, want 0", code)
			}
			if got != nil {
				t.Errorf("decision from a broken payload: %v", got)
			}
		})
	}
}

// A stdin that never closes must time out into an allow, not hang the agent.
func TestGuardAllowsWhenStdinStalls(t *testing.T) {
	t.Setenv("PLAN_FORGE_HOME", filepath.Join(t.TempDir(), "home"))
	done := make(chan int, 1)
	go func() {
		var out bytes.Buffer
		done <- runHookClaudeGuard(context.Background(), stalledReader{}, &out, 50*time.Millisecond)
	}()
	select {
	case code := <-done:
		if code != exitOK {
			t.Errorf("exit = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the guard did not return within its bound")
	}
}

// stalledReader never returns and never closes.
type stalledReader struct{}

func (stalledReader) Read([]byte) (int, error) { select {} }

func TestGuardRefusesAnEditInsideThePlanDirectory(t *testing.T) {
	for name, rel := range map[string]string{
		"the spec":     planDir + "/synthesized_spec.md",
		"the manifest": planDir + "/plan_manifest.json",
		"nested":       planDir + "/history/x/plan_manifest.json",
	} {
		t.Run(name, func(t *testing.T) {
			if !underPlanDir("/repo", "/repo/"+rel) {
				t.Errorf("%s was not recognised as plan state", rel)
			}
			if !underPlanDir("/repo", rel) {
				t.Errorf("%s was not recognised when relative", rel)
			}
		})
	}
	for name, rel := range map[string]string{
		"implementation":   "internal/app/board.go",
		"a sibling prefix": "docs/plans/.planning/notes.md",
		"the parent":       "docs/plans/PLAN.md",
		"an escape":        "../elsewhere/.plan/x.md",
	} {
		t.Run(name, func(t *testing.T) {
			if underPlanDir("/repo", "/repo/"+rel) {
				t.Errorf("%s was wrongly treated as plan state", rel)
			}
		})
	}
}

// The reason must name the remedy and say the block clears itself, so an agent
// reading it knows to continue rather than retry blindly.
func TestGuardDenyReasonNamesTheRemedy(t *testing.T) {
	reason := guardDriftReasonForTest("aaaaaaaabbbb", "ccccccccdddd", "/repo")
	for _, want := range []string{"aaaaaaaa", "cccccccc", "plan-forge validate", "blocks once"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q does not mention %q", reason, want)
		}
	}
}

// guardDriftReasonForTest renders the drift reason without touching disk.
func guardDriftReasonForTest(prev, now, repo string) string {
	return "Plan-Forge: the approved plan changed since this session started (manifest " +
		shortSHA(prev) + " -> " + shortSHA(now) + "). Re-read " + planDir +
		"/synthesized_spec.md and run plan-forge validate --repo " + repo +
		" before editing. This blocks once; your next tool call proceeds."
}

func TestGuardDecisionIsDenyAndNeverAllowOrDefer(t *testing.T) {
	var out bytes.Buffer
	writeGuardDeny(&out, "because")
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %q", out.String())
	}
	decision, reason := decisionOf(t, got)
	if decision != "deny" {
		t.Errorf("decision = %q, want deny", decision)
	}
	if reason != "because" {
		t.Errorf("reason = %q", reason)
	}
	// allow would skip the user's permission prompt; defer is honoured only in
	// a non-interactive single-call turn. Neither may ever be emitted.
	body := out.String()
	for _, forbidden := range []string{`"allow"`, `"defer"`, `"decision"`, `"reason"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("output carries %s: %s", forbidden, body)
		}
	}
}

// The baseline is per session and per repository, and it only ever holds a
// digest: nothing from the payload reaches the path unchecked.
func TestGuardBaselineRefusesAnUnusableSessionID(t *testing.T) {
	t.Setenv("PLAN_FORGE_HOME", filepath.Join(t.TempDir(), "home"))
	repo := t.TempDir()
	for _, session := range []string{"", "../escape", "a/b", strings.Repeat("x", 129), "has space"} {
		if _, err := guardBaselinePath(repo, session); err == nil {
			t.Errorf("session %q was accepted into a path", session)
		}
	}
	path, err := guardBaselinePath(repo, "abc-123.def")
	if err != nil {
		t.Fatalf("a valid session was refused: %v", err)
	}
	if filepath.Base(path) != "abc-123.def.json" {
		t.Errorf("path = %q", path)
	}
}

func TestGuardBaselineRoundTrips(t *testing.T) {
	t.Setenv("PLAN_FORGE_HOME", filepath.Join(t.TempDir(), "home"))
	path, err := guardBaselinePath(t.TempDir(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if _, had := readGuardBaseline(path); had {
		t.Error("a baseline existed before anything was written")
	}
	writeGuardBaseline(path, "digest-one")
	got, had := readGuardBaseline(path)
	if !had || got.ManifestSHA256 != "digest-one" {
		t.Fatalf("baseline = %+v had=%v", got, had)
	}
	if got.At == "" {
		t.Error("the baseline carries no timestamp")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want 0600", perm)
	}

	// A file that is not a baseline reads as absent rather than as a digest.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, had := readGuardBaseline(path); had {
		t.Error("an unreadable baseline was treated as present")
	}
}

// The whole contract, against a real approved plan: the first call of a
// session establishes the baseline and never blocks, an unchanged plan is
// silent, and a changed plan denies exactly once.
func TestGuardDeniesOncePerDrift(t *testing.T) {
	repo, _ := pullRepo(t, true)
	payload := guardPayload(t, repo, "s1", "Edit", filepath.Join(repo, "internal/billing/export.go"))

	if code, got := runGuard(t, payload); code != exitOK || got != nil {
		t.Fatalf("the first call blocked: exit %d, decision %v", code, got)
	}
	if code, got := runGuard(t, payload); code != exitOK || got != nil {
		t.Fatalf("an unchanged plan blocked: exit %d, decision %v", code, got)
	}

	// Re-approve under a different digest, as a mid-session re-forge does.
	driftRepo(t, repo)

	code, got := runGuard(t, payload)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	if got == nil {
		t.Fatal("the changed plan was not refused")
	}
	decision, reason := decisionOf(t, got)
	if decision != "deny" {
		t.Errorf("decision = %q, want deny", decision)
	}
	if !strings.Contains(reason, "changed since this session started") {
		t.Errorf("reason = %q", reason)
	}

	// And it clears itself: the very next call proceeds, so a retry loop is
	// impossible.
	if code, got := runGuard(t, payload); code != exitOK || got != nil {
		t.Fatalf("the guard blocked twice for one drift: exit %d, decision %v", code, got)
	}
}

// A second session gets its own baseline and is not blocked by the first
// session's history.
func TestGuardBaselineIsPerSession(t *testing.T) {
	repo, _ := pullRepo(t, true)
	first := guardPayload(t, repo, "s1", "Edit", filepath.Join(repo, "a.go"))
	if _, got := runGuard(t, first); got != nil {
		t.Fatalf("the first call blocked: %v", got)
	}
	driftRepo(t, repo)
	if _, got := runGuard(t, first); got == nil {
		t.Fatal("the drift was not refused for the session that saw the old plan")
	}
	second := guardPayload(t, repo, "s2", "Edit", filepath.Join(repo, "a.go"))
	if _, got := runGuard(t, second); got != nil {
		t.Errorf("a fresh session was blocked by another session's history: %v", got)
	}
}

// An edit aimed at the approved artifacts is refused on its own, with no
// baseline involved.
func TestGuardRefusesEditingTheApprovedPlanItself(t *testing.T) {
	repo, _ := pullRepo(t, true)
	payload := guardPayload(t, repo, "s1", "Edit", filepath.Join(repo, planstate.SpecRel))
	_, got := runGuard(t, payload)
	if got == nil {
		t.Fatal("editing the approved spec was allowed")
	}
	decision, reason := decisionOf(t, got)
	if decision != "deny" {
		t.Errorf("decision = %q", decision)
	}
	if !strings.Contains(reason, "approved plan") {
		t.Errorf("reason = %q", reason)
	}
}

// driftRepo re-approves the repository's manifest under a new digest, the way
// a mid-session re-forge does.
func driftRepo(t *testing.T, repo string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(planstate.ManifestRel))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["title"] = "Invoice export, revised"
	next, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	next = append(next, '\n')
	if err := os.WriteFile(path, next, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := planstate.RecordApproval(repo, storage.SHA256Hex(next), "approval-1"); err != nil {
		t.Fatal(err)
	}
}
