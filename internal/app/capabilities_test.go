//go:build client && !server

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/config"
)

// Probing runs every installed host CLI, which costs seconds. The unpinned
// view is the same for every test that wants one, so it is computed once.
var (
	plainOnce sync.Once
	plainView CapabilitiesView
)

func plainCapabilities(t *testing.T) CapabilitiesView {
	t.Helper()
	plainOnce.Do(func() {
		plainView = Capabilities(context.Background(), config.Config{}, t.TempDir(), "test")
	})
	return plainView
}

// The panel renders every list, so none of them may be null: a nil slice
// marshals to `null` and the window's `.map` throws on it.
func TestCapabilitiesNeverReportsANullList(t *testing.T) {
	view := plainCapabilities(t)
	if view.Hosts == nil || view.Tools == nil || view.Skills == nil || view.Hooks == nil || view.Pinned == nil {
		t.Fatalf("a list is nil: %+v", view)
	}
}

// The same rule on a machine with no host installed, which is the case that
// actually broke. `InspectSkillRoots` and `InspectHooks` return a nil slice
// when they have nothing to report, and assigning that over the empty one put
// `null` on the wire. It passed on a developer machine with three hosts signed
// in and failed on the first CI runner, which has none — so PATH is emptied
// here rather than trusting whatever the machine happens to have.
func TestCapabilitiesNeverReportsANullListWithNoHostsInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	view := Capabilities(context.Background(), config.Config{}, t.TempDir(), "test")
	if view.Hosts == nil || view.Tools == nil || view.Skills == nil || view.Hooks == nil || view.Pinned == nil || view.CLIs == nil {
		t.Fatalf("a list is nil with no hosts installed: %+v", view)
	}
	for _, h := range view.Hosts {
		if h.Found {
			t.Errorf("host %q reported found with an empty PATH", h.Host)
		}
	}
	// Every list must survive a JSON round trip as an array, never as null:
	// that is the shape the window actually consumes.
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"hosts", "tools", "skills", "hooks", "pinned", "clis"} {
		if bytes.Contains(raw, []byte(`"`+key+`":null`)) {
			t.Errorf("%q marshalled to null", key)
		}
	}
}

// Every tool says what it is for. A row that reports `agentbox: missing` and
// nothing else tells the owner to install something without saying why.
func TestEveryToolSaysWhatItIsFor(t *testing.T) {
	view := plainCapabilities(t)
	if len(view.Tools) == 0 {
		t.Fatal("no tools were probed")
	}
	for _, tool := range view.Tools {
		if tool.Name == "" {
			t.Error("a tool has no name")
		}
		if len(tool.Purpose) < 20 {
			t.Errorf("tool %q has no useful purpose text: %q", tool.Name, tool.Purpose)
		}
		if !tool.Found && tool.Remedy == "" {
			t.Errorf("tool %q is missing and offers no remedy", tool.Name)
		}
	}
}

// git is what makes the worktree, so a build cannot run without it. The test
// machine has git, which is also what the delivery tests need.
func TestGitIsMarkedEssential(t *testing.T) {
	view := plainCapabilities(t)
	for _, tool := range view.Tools {
		if tool.Name == "git" {
			if !tool.Essential {
				t.Error("git is not marked essential")
			}
			return
		}
	}
	t.Fatal("git was not probed")
}

// A pin naming a host that cannot serve must be reported. This is the fault
// ADR 0012 traced: the configuration said agy, every avatar went elsewhere,
// and nothing on screen said the choice had been dropped.
func TestAPinOnAHostThatCannotServeIsReported(t *testing.T) {
	// "nosuchhost" is not a registered adapter, so it can never serve.
	cfg := config.Config{MainHost: "nosuchhost"}
	view := Capabilities(context.Background(), cfg, t.TempDir(), "test")
	if len(view.Pinned) == 0 {
		t.Fatalf("a pin on an unusable host was not reported; hosts=%+v", view.Hosts)
	}
	if got := view.Pinned[0]; got != "main_host = nosuchhost" {
		t.Fatalf("pinned[0] = %q", got)
	}
}

// An unset pin is not a problem and must not be reported as one.
func TestNoPinIsNotReported(t *testing.T) {
	view := plainCapabilities(t)
	if len(view.Pinned) != 0 {
		t.Fatalf("pinned = %v with nothing configured", view.Pinned)
	}
}

// Serves is never true for a host that is not signed in, whatever preflight
// said: the window colours the row on this one field.
func TestAHostThatIsNotReadyNeverServes(t *testing.T) {
	view := plainCapabilities(t)
	for _, h := range view.Hosts {
		if h.Serves && !h.Ready {
			t.Errorf("host %q serves while not ready", h.Host)
		}
		if h.Serves && !h.Found {
			t.Errorf("host %q serves while not installed", h.Host)
		}
	}
}
