//go:build client && !server

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

func writeBrief(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	return path
}

func TestReadBriefLoadsTheAgreedBrief(t *testing.T) {
	path := writeBrief(t, "# Brief\n\nShip the thing.\n")
	got, err := readBrief(path, relay.ModeSolo)
	if err != nil {
		t.Fatalf("readBrief: %v", err)
	}
	if !strings.Contains(got, "Ship the thing.") {
		t.Fatalf("readBrief returned %q", got)
	}
}

func TestReadBriefIsOptional(t *testing.T) {
	got, err := readBrief("", relay.ModeSolo)
	if err != nil || got != "" {
		t.Fatalf("readBrief(\"\") = %q, %v; want empty and no error", got, err)
	}
}

// A remote session takes its document from the relay. Accepting --brief there
// would drop it silently, so it is refused with a reason.
func TestReadBriefRefusedInRemoteMode(t *testing.T) {
	path := writeBrief(t, "# Brief\n")
	_, err := readBrief(path, relay.ModeRemote)
	if err == nil {
		t.Fatal("readBrief accepted a brief for a remote session")
	}
	if !strings.Contains(err.Error(), "remote") {
		t.Fatalf("error %q does not explain the remote boundary", err)
	}
}

func TestReadBriefRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"empty", "   \n\t\n", "empty"},
		{"not utf-8", "\xff\xfe\x00bad", "UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := readBrief(writeBrief(t, tc.body), relay.ModeSolo); err == nil {
				t.Fatal("readBrief accepted it")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestReadBriefRejectsAnOversizedFile(t *testing.T) {
	path := writeBrief(t, strings.Repeat("x", maxBriefBytes+1))
	_, err := readBrief(path, relay.ModeSolo)
	if err == nil {
		t.Fatal("readBrief accepted a file over the bound")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error %q does not state the limit", err)
	}
}

func TestReadBriefRejectsAMissingFile(t *testing.T) {
	if _, err := readBrief(filepath.Join(t.TempDir(), "nope.md"), relay.ModeSolo); err == nil {
		t.Fatal("readBrief accepted a missing file")
	}
}

func TestReadBriefRejectsADirectory(t *testing.T) {
	if _, err := readBrief(t.TempDir(), relay.ModeSolo); err == nil {
		t.Fatal("readBrief accepted a directory")
	}
}
