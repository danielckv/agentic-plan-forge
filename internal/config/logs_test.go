package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PLAN_FORGE_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, markerName), nil, 0o600); err != nil {
		t.Fatalf("marker: %v", err)
	}
	return dir
}

func TestLogFileName(t *testing.T) {
	now := time.Date(2026, 10, 31, 3, 45, 0, 0, time.Local)
	got := LogFileName(now)
	if got != "plan-forge-2026-10-31.log" {
		t.Fatalf("LogFileName = %q", got)
	}
}

func TestLogDirIsUnderHome(t *testing.T) {
	home := testHome(t)
	dir, err := LogDir()
	if err != nil {
		t.Fatalf("LogDir: %v", err)
	}
	if dir != filepath.Join(home, "logs") {
		t.Fatalf("LogDir = %q, want %q", dir, filepath.Join(home, "logs"))
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("log path is not a directory (mode %s)", info.Mode())
	}
	if mode := info.Mode().String(); mode != "drwx------" {
		t.Fatalf("log dir mode = %s, want drwx------", mode)
	}
}

func TestOpenLogFileAppendsAndIsOwnerOnly(t *testing.T) {
	testHome(t)
	now := time.Date(2026, 10, 31, 3, 45, 0, 0, time.Local)
	f1, err := OpenLogFile(now)
	if err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}
	if _, err := f1.WriteString("first"); err != nil {
		t.Fatalf("write: %v", err)
	}
	f1.Close()
	f2, err := OpenLogFile(now)
	if err != nil {
		t.Fatalf("OpenLogFile again: %v", err)
	}
	defer f2.Close()
	if _, err := f2.WriteString("second"); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(f2.Name())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(raw) != "firstsecond" {
		t.Fatalf("content = %q, want appended", raw)
	}
	info, err := os.Stat(f2.Name())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().String(); mode != "-rw-------" {
		t.Fatalf("log file mode = %s, want -rw-------", mode)
	}
}

func TestOpenLogFileRemovesExpiredFiles(t *testing.T) {
	home := testHome(t)
	logs := filepath.Join(home, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	now := time.Date(2026, 10, 31, 3, 45, 0, 0, time.Local)
	// Two days back: 2026-10-29 is exactly at the cutoff day — cutoff is
	// midnight of now-48h, so 10-29 must survive and 10-28 must go.
	stale := filepath.Join(logs, "plan-forge-2026-10-28.log")
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatalf("write stale: %v", err)
	}
	edge := filepath.Join(logs, "plan-forge-2026-10-29.log")
	if err := os.WriteFile(edge, []byte("edge"), 0o600); err != nil {
		t.Fatalf("write edge: %v", err)
	}
	foreign := filepath.Join(logs, "other-tool.log")
	if err := os.WriteFile(foreign, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write foreign: %v", err)
	}
	malformed := filepath.Join(logs, "plan-forge-not-a-date.log")
	if err := os.WriteFile(malformed, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write malformed: %v", err)
	}
	f, err := OpenLogFile(now)
	if err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}
	defer f.Close()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale log survived: %v", err)
	}
	if _, err := os.Stat(edge); err != nil {
		t.Fatalf("edge log removed: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign log removed: %v", err)
	}
	if _, err := os.Stat(malformed); err != nil {
		t.Fatalf("malformed log removed: %v", err)
	}
}

func TestLatestLogsSorted(t *testing.T) {
	home := testHome(t)
	logs := filepath.Join(home, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"plan-forge-2026-10-30.log", "plan-forge-2026-10-29.log"} {
		if err := os.WriteFile(filepath.Join(logs, name), nil, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	got, err := LatestLogs()
	if err != nil {
		t.Fatalf("LatestLogs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("LatestLogs len = %d, want 2", len(got))
	}
	if !strings.HasSuffix(got[0], "2026-10-29.log") {
		t.Fatalf("first = %q, want oldest first", got[0])
	}
}
