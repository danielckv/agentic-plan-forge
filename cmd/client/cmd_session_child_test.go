//go:build client && !server

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/app"
	"github.com/danielckv/agentic-plan-forge/internal/config"
)

func childTestHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PLAN_FORGE_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, ".plan-forge-home"), nil, 0o600); err != nil {
		t.Fatalf("marker: %v", err)
	}
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// The child must fan one record out to both files: the day log next to
// config.json, and the per-session log `plan-forge wait` reads. PR #36 wired
// the day file but reassigned the logger to the session file, so detached
// runs reached only one.
func TestChildLoggerWritesBothFiles(t *testing.T) {
	home := childTestHome(t)
	sessionID := app.NewSessionID()

	logger := newChildLogger(sessionID)
	if logger == nil {
		t.Fatal("newChildLogger = nil, want a working logger")
	}
	logger.Info("child fanout probe", "stage", "test")

	dayPath := filepath.Join(home, "logs", config.LogFileName(time.Now()))
	day := readFile(t, dayPath)
	if !strings.Contains(day, "child fanout probe") {
		t.Fatalf("day log lacks the record:\n%s", day)
	}
	session := readFile(t, mustSessionLog(t, sessionID))
	if !strings.Contains(session, "child fanout probe") {
		t.Fatalf("session log lacks the record:\n%s", session)
	}
	for _, raw := range []string{day, session} {
		for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
			if !strings.Contains(line, "level=INFO") {
				t.Fatalf("record not slog TextHandler format: %q", line)
			}
		}
	}
}

// With no PLAN_FORGE_HOME the day log may be unopenable; the child still logs
// to the session file instead of dropping the record entirely.
func TestChildLoggerFallsBackToSessionFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PLAN_FORGE_HOME", filepath.Join(dir, "owned")) // no marker: home refused
	sessionID := app.NewSessionID()

	logger := newChildLogger(sessionID)
	if logger == nil {
		t.Fatal("newChildLogger = nil with a session log available")
	}
	logger.Info("fallback probe")
	if got := readFile(t, mustSessionLog(t, sessionID)); !strings.Contains(got, "fallback probe") {
		t.Fatalf("session log lacks the record:\n%s", got)
	}
}

// A day log with no session state must still be written to: the log pair is
// independent, one failure must not silence the other.
func TestChildLoggerSurvivesUnopenableSessionPath(t *testing.T) {
	home := childTestHome(t)
	sessionID := app.NewSessionID()
	// Put a directory where the session log file goes: OpenFile fails, the
	// day file must carry the record anyway.
	sessionDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(filepath.Join(sessionDir, sessionID+".log"), 0o700); err != nil {
		t.Fatalf("plant dir: %v", err)
	}

	logger := newChildLogger(sessionID)
	if logger == nil {
		t.Fatal("newChildLogger = nil with a day log available")
	}
	logger.Info("day-only probe")
	dayPath := filepath.Join(home, "logs", config.LogFileName(time.Now()))
	if got := readFile(t, dayPath); !strings.Contains(got, "day-only probe") {
		t.Fatalf("day log lacks the record:\n%s", got)
	}
}

func mustSessionLog(t *testing.T, sessionID string) string {
	t.Helper()
	path, err := app.LogPath(sessionID)
	if err != nil {
		t.Fatalf("LogPath: %v", err)
	}
	return path
}

// gitRepo commits one file into a fresh repository under t.TempDir, so the
// child gets past app.ResolveRepo and reaches the phase logging of a run.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# probe\n"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "probe@example.com"},
		{"config", "user.name", "Probe"},
		{"add", "README.md"},
		{"commit", "-q", "-m", "seed"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	return dir
}

// End to end: a real detached child process writes its run record into both
// files, not just the per-session one. The repo is a real Git work tree so the
// child gets past repository resolution and logs its run record; the context
// deadline stops the run afterwards on machines where a host resolves and the
// child would otherwise wait for a window.
func TestDetachedChildProcessWritesDayLog(t *testing.T) {
	home := childTestHome(t)
	sessionID := app.NewSessionID()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	code := runSessionChild(ctx, []string{
		"--session", sessionID,
		"--repo", gitRepo(t),
	})
	if code == 0 {
		t.Fatal("child exited 0 without an approval, want failure")
	}

	dayPath := filepath.Join(home, "logs", config.LogFileName(time.Now()))
	deadline := time.Now().Add(10 * time.Second)
	var day, session string
	for time.Now().Before(deadline) {
		day = readFile(t, dayPath)
		session = readFile(t, mustSessionLog(t, sessionID))
		if strings.Contains(day, sessionID) || strings.Contains(session, sessionID) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(day, sessionID) {
		t.Fatalf("day log never received the run record:\n%s", day)
	}
	if !strings.Contains(session, sessionID) {
		t.Fatalf("session log never received the run record:\n%s", session)
	}
}
