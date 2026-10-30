//go:build client && !server

package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// raw renders without a terminal, so every layout rule is testable.
func raw(v View, w Width) string { return Render(v, w) }

// sampleView is a session mid-deliberation with a temporary agent and one
// waiting decision: the state a reader most needs to read correctly.
func sampleView() View {
	return View{
		Tab: TabAgents,
		Snapshot: Snapshot{
			Phase: relay.PhaseDeliberation, Role: relay.RoleOwner, Cycle: 2,
			DocTitle: "Invoice export", DocWords: 412,
			Agents: []AgentRow{
				{ID: "architect", Name: "Technical Architect", Status: "Drafting", Cycle: 2, Host: "claude", Model: "opus", Kind: "permanent"},
				{ID: "gap:security", Name: "Temporary · security", Status: "Thinking", Kind: "temporary", Summary: "Naming the trust boundaries the export crosses."},
				{ID: "pm", Name: "Product Manager", Status: "Synced", Kind: "permanent"},
			},
			Plan:      []string{"E1 Export invoices", "S1 Finance exports a range"},
			Questions: []QuestionRow{{ID: "d1", Kind: "decision", Prompt: "Where should the export be written?", Options: []string{"stream: Stream the download", "stage: Stage the file"}, Recommended: "stage"}},
		},
	}
}

// Every line fits the width. A frame that wraps breaks the cursor arithmetic and
// scrolls the panes out of place.
func TestNoLineExceedsTheWidth(t *testing.T) {
	for _, w := range []Width{60, 80, 120, 200} {
		for _, tab := range Tabs {
			v := sampleView()
			v.Tab = tab
			out := raw(v, w)
			for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
				if n := utf8.RuneCountInString(line); n > int(w) {
					t.Errorf("width %d tab %s line %d is %d runes: %q", w, tab, i, n, line)
				}
			}
		}
	}
}

// A zero or tiny width still renders something: a caller that cannot measure the
// terminal must not get a panic or a blank screen.
func TestNarrowWidthStillRenders(t *testing.T) {
	v := sampleView()
	for _, w := range []Width{0, 1, 20, MinWidth - 1} {
		out := raw(v, w)
		if strings.TrimSpace(out) == "" {
			t.Fatalf("width %d rendered nothing", w)
		}
	}
}

// The header names the phase and the cycle, which is where a reader looks first.
func TestHeaderShowsPhaseAndCycle(t *testing.T) {
	out := raw(sampleView(), 100)
	first := strings.SplitN(out, "\n", 2)[0]
	for _, want := range []string{"Invoice export", "DELIBERATION", "cycle 2", "owner"} {
		if !strings.Contains(first, want) {
			t.Errorf("header lacks %q: %s", want, first)
		}
	}
}

// The footer carries the pending count, so a reader knows to look without
// changing panes.
func TestFooterCountsOpenQuestions(t *testing.T) {
	out := raw(sampleView(), 100)
	if !strings.Contains(out, "1 open question(s)") {
		t.Fatalf("footer does not count the question:\n%s", out)
	}
}

// The agents pane marks a temporary agent and shows where it runs.
func TestAgentsPaneMarksTemporaryAndPlacement(t *testing.T) {
	out := raw(sampleView(), 120)
	if !strings.Contains(out, "Temporary · security (temporary)") {
		t.Errorf("temporary agent not marked:\n%s", out)
	}
	if !strings.Contains(out, "[claude/opus]") {
		t.Errorf("placement not shown:\n%s", out)
	}
	if !strings.Contains(out, "Naming the trust boundaries") {
		t.Errorf("agent summary missing:\n%s", out)
	}
}

// The questions pane marks the recommendation and still requires an explicit
// choice: a recommendation is a hint, never an answer.
func TestQuestionsPaneMarksRecommendation(t *testing.T) {
	v := sampleView()
	v.Tab = TabQuestions
	out := raw(v, 120)
	if !strings.Contains(out, "[decision]") {
		t.Errorf("kind missing:\n%s", out)
	}
	if !strings.Contains(out, "* 2) stage: Stage the file") {
		t.Errorf("recommended option not marked:\n%s", out)
	}
	if !strings.Contains(out, "a hint rather than an answer") {
		t.Errorf("recommendation not described as a hint:\n%s", out)
	}
	if !strings.Contains(out, "Press the option number to answer") {
		t.Errorf("answering instructions missing:\n%s", out)
	}
}

// Empty panes say why they are empty rather than showing a bare heading, so a
// reader can tell "nothing yet" from "nothing rendered".
func TestEmptyPanesExplainThemselves(t *testing.T) {
	var v View
	for _, tab := range Tabs {
		v.Tab = tab
		out := raw(v, 100)
		if !strings.Contains(out, "── ") {
			t.Errorf("tab %s has no section heading", tab)
		}
		if strings.Count(out, "\n") < 4 {
			t.Errorf("tab %s rendered almost nothing: %q", tab, out)
		}
	}
}

// The help pane lists the keys, including the ones that send commands.
func TestHelpListsEveryKey(t *testing.T) {
	v := sampleView()
	v.Help = true
	out := raw(v, 120)
	for _, key := range []string{"1 – 5", "Tab / Shift-Tab", "↑ / ↓", "Enter", "approve", "synthesize", "retry", "cancel", "quit"} {
		if !strings.Contains(out, key) {
			t.Errorf("help lacks %q:\n%s", key, out)
		}
	}
}

// A long provider error is truncated with a marker, never silently cut.
func TestLongTextIsTruncatedWithAMarker(t *testing.T) {
	v := View{Snapshot: Snapshot{Banner: strings.Repeat("x", 400)}}
	first := strings.SplitN(raw(v, 80), "\n", 2)[0]
	if !utf8.ValidString(first) {
		t.Fatal("truncation broke UTF-8")
	}
	if !strings.Contains(raw(v, 80), "…") {
		t.Error("truncated line carries no marker")
	}
}

// A banner and an error both appear above the panes, because they are the two
// things a reader must not miss.
func TestBannerAndErrorLeadTheFrame(t *testing.T) {
	v := sampleView()
	v.Snapshot.Banner = "the synthesizer timed out"
	v.Snapshot.Err = "STALE_RUN"
	out := raw(v, 100)
	bannerAt := strings.Index(out, "the synthesizer timed out")
	errAt := strings.Index(out, "STALE_RUN")
	agentsAt := strings.Index(out, "── Agents")
	if bannerAt < 0 || errAt < 0 {
		t.Fatalf("banner or error missing:\n%s", out)
	}
	if bannerAt > agentsAt || errAt > agentsAt {
		t.Error("banner or error is below the pane heading")
	}
}
