//go:build client && !server

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/planstate"
)

// hookGuardBound is the whole budget for one PreToolUse decision. It is
// tighter than the hook budget because the guard sits in front of a tool call
// the user is waiting on: the only work is reading two files.
const hookGuardBound = 1 * time.Second

// planDir is the committed-plan directory. An agent editing it by hand is
// editing the thing it is supposed to be implementing against.
const planDir = "docs/plans/.plan"

// guardSessionRE bounds the session id used in a cache path. The id arrives
// from the hook payload, so it never reaches the filesystem unchecked.
var guardSessionRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// guardBaseline is what this session last saw. The digest, not the spec, is
// what decides: a plan is re-approved under a new digest, so comparing digests
// catches every change without reading the spec at all.
type guardBaseline struct {
	ManifestSHA256 string `json:"manifest_sha256"`
	At             string `json:"at"`
}

// runHookClaudeGuard implements `plan-forge hook claude-guard`, the managed
// Claude Code PreToolUse hook.
//
// It always exits 0 and decides through the JSON contract, never through exit
// code 2. Three outputs are deliberately never emitted:
//
//   - permissionDecision "allow" would skip the user's own permission prompt,
//     so a guard that allowed would quietly weaken their settings. The allow
//     path prints nothing.
//   - "defer" is honoured only in a non-interactive single-call turn, so it is
//     a no-op that can confuse the tool result.
//   - a top-level decision/reason pair is deprecated for this event.
//
// It denies at most once per drift. The first PreToolUse of a session records
// the baseline and never blocks; a changed digest denies once and immediately
// rewrites the baseline, so the next call proceeds. A guard that can block
// twice for one cause deadlocks the agent, which is worse than not guarding.
func runHookClaudeGuard(ctx context.Context, stdin io.Reader, stdout io.Writer, bound time.Duration) int {
	raw, ok := readHookStdin(ctx, stdin, bound)
	if !ok {
		return exitOK
	}
	var in claudeHookInput
	if json.Unmarshal(raw, &in) != nil {
		return exitOK
	}
	if in.Event != "PreToolUse" {
		return exitOK
	}
	repo := claudeHookRepo(in.Cwd)
	if repo == "" {
		return exitOK
	}

	// Unplanned work is never blocked: a repository with no approved plan is
	// the normal case, not a fault. This is a file read, never an AST scan.
	snap, err := planstate.Inspect(repo)
	if err != nil || snap.ManifestSHA256 == "" || !snap.Approved {
		return exitOK
	}

	if reason := guardReason(repo, in, snap.ManifestSHA256); reason != "" {
		writeGuardDeny(stdout, reason)
	}
	return exitOK
}

// guardReason returns the deny reason, or "" to allow.
func guardReason(repo string, in claudeHookInput, manifestSHA string) string {
	if path := claudeEditedPath(in); path != "" && underPlanDir(repo, path) {
		return "Plan-Forge: " + planDir + " holds the approved plan and its manifest. " +
			"Edit the implementation instead, or re-run the forge to change the plan."
	}

	path, err := guardBaselinePath(repo, in.SessionID)
	if err != nil {
		return "" // nowhere to remember: allow rather than block on every call
	}
	prev, had := readGuardBaseline(path)
	if !had {
		// The first tool call of a session establishes the baseline. It never
		// blocks: the agent has not been told anything yet to be stale about.
		writeGuardBaseline(path, manifestSHA)
		return ""
	}
	if prev.ManifestSHA256 == manifestSHA {
		return ""
	}
	// Rewrite before returning, so this drift denies exactly once even if the
	// agent retries immediately.
	writeGuardBaseline(path, manifestSHA)
	return fmt.Sprintf(
		"Plan-Forge: the approved plan changed since this session started (manifest %s -> %s). "+
			"Re-read %s/synthesized_spec.md and run plan-forge validate --repo %s before editing. "+
			"This blocks once; your next tool call proceeds.",
		shortSHA(prev.ManifestSHA256), shortSHA(manifestSHA), planDir, hookField(repo))
}

// underPlanDir reports whether an edited path falls inside the committed plan
// directory. A relative path is resolved against the repository.
func underPlanDir(repo, path string) bool {
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo, path)
	}
	rel, err := filepath.Rel(repo, filepath.Clean(path))
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	return rel == planDir || strings.HasPrefix(rel, planDir+"/")
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// writeGuardDeny emits the PreToolUse deny contract. Every interpolated string
// has already passed hookField, so no control sequence reaches the transcript.
func writeGuardDeny(stdout io.Writer, reason string) {
	out, err := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": hookField(reason),
		},
	})
	if err != nil {
		return
	}
	_, _ = stdout.Write(append(out, '\n'))
}

// guardBaselinePath is the per-session baseline file, under the cache and
// keyed by the repository digest so two repositories never collide.
func guardBaselinePath(repo, session string) (string, error) {
	if !guardSessionRE.MatchString(session) {
		return "", fmt.Errorf("hook: unusable session id")
	}
	cache, err := config.EnsureCache()
	if err != nil {
		return "", err
	}
	real := repo
	if resolved, rerr := filepath.EvalSymlinks(repo); rerr == nil {
		real = resolved
	}
	sum := sha256.Sum256([]byte(real))
	dir := filepath.Join(cache, "guard", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, session+".json"), nil
}

func readGuardBaseline(path string) (guardBaseline, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return guardBaseline{}, false
	}
	var b guardBaseline
	if json.Unmarshal(raw, &b) != nil || b.ManifestSHA256 == "" {
		return guardBaseline{}, false
	}
	return b, true
}

// writeGuardBaseline records the digest this session has been told about. A
// failure is swallowed: the cost is one extra deny later, never a blocked
// agent now.
func writeGuardBaseline(path, manifestSHA string) {
	raw, err := json.Marshal(guardBaseline{
		ManifestSHA256: manifestSHA,
		At:             time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o600)
}
