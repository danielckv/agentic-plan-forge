//go:build client && !server

package app

import (
	"strings"
	"testing"
)

// The composer has two ways to re-read the brief, and they must not both run.
// The automatic path reads four times and stops; the manual button reads once
// and switches the automatic path off.

func briefOfWords(n int) string {
	return strings.TrimSpace(strings.Repeat("word ", n))
}

// gateFor returns the executor's gate after offering it the text the way the
// debounce loop does.
func gateFor(exec *executor, text string) bool {
	exec.mu.Lock()
	defer exec.mu.Unlock()
	return exec.gate.due(text)
}

// A short edit earns no call, and the fourth read is the last one.
func TestExecutorRefreshesTheBriefOnlyWhileTheGateAllows(t *testing.T) {
	exec := newExecutor(executorConfig{Repo: t.TempDir(), Logger: quietLogger()})

	// Below the threshold: no read, however many edits arrive.
	for _, n := range []int{0, 10, 50, 99} {
		if gateFor(exec, briefOfWords(n)) {
			t.Fatalf("%d words earned a background read", n)
		}
	}
	// The four reads, and the steps between them.
	for _, tc := range []struct {
		words int
		want  bool
	}{
		{100, true},
		{129, false},
		{130, true},
		{189, false},
		{190, true},
		{309, false},
		{310, true},
		{2000, false},
	} {
		if got := gateFor(exec, briefOfWords(tc.words)); got != tc.want {
			t.Fatalf("%d words: due = %v, want %v", tc.words, got, tc.want)
		}
	}
	st := exec.Status().Refresh
	if st.Refreshes != maxAutomaticRefreshes {
		t.Fatalf("status reports %d refreshes, want %d", st.Refreshes, maxAutomaticRefreshes)
	}
	if st.Automatic {
		t.Errorf("status still reports an automatic read after four: %+v", st)
	}
	if st.AutomaticOff {
		t.Error("automaticOff is true, but nobody pressed Refresh")
	}
}

// The manual button reads once and switches the automatic path off, so the two
// mechanisms never re-read the same document.
func TestManualRefreshDisablesTheAutomaticPath(t *testing.T) {
	exec := newExecutor(executorConfig{Repo: t.TempDir(), Logger: quietLogger()})

	exec.PressRefresh()
	exec.mu.Lock()
	manual := exec.gate.manual
	exec.mu.Unlock()
	if !manual {
		t.Fatal("the press did not switch the automatic path off")
	}
	// The press is answered immediately, not debounced.
	select {
	case immediate := <-exec.normTrigger:
		if !immediate {
			t.Fatal("the press scheduled a debounced read, want an immediate one")
		}
	default:
		t.Fatal("the press scheduled no read")
	}
	// A press also asks for the model-backed impact pass, so the press does
	// everything the automatic path would have done, once.
	exec.mu.Lock()
	modelImpact := exec.modelImpact
	exec.mu.Unlock()
	if !modelImpact {
		t.Error("the press did not request the model-backed impact pass")
	}

	// Every later background edit is refused.
	for _, n := range []int{100, 400, 4000} {
		if gateFor(exec, briefOfWords(n)) {
			t.Fatalf("%d words earned an automatic read after a manual one", n)
		}
	}
	if st := exec.Status().Refresh; st.Automatic || !st.AutomaticOff {
		t.Errorf("status = %+v, want the automatic path off", st)
	}
}

// The press is honest about the phase: before the first interpretation the
// gate still reports what it will do, so the page can label the button before
// anyone has typed anything.
func TestRefreshStateBeforeTheFirstRead(t *testing.T) {
	exec := newExecutor(executorConfig{Repo: t.TempDir(), Logger: quietLogger()})
	st := exec.Status().Refresh
	if !st.Automatic {
		t.Error("the first automatic read should be pending on a fresh session")
	}
	if st.MaxRefreshes != maxAutomaticRefreshes {
		t.Errorf("maxRefreshes = %d, want %d", st.MaxRefreshes, maxAutomaticRefreshes)
	}
	if st.WordsRemaining != firstRefreshWords-0 {
		t.Errorf("wordsRemaining = %d, want %d", st.WordsRemaining, firstRefreshWords)
	}
}

// A manual refresh does not move the automatic count: that counter is a record
// of what the automatic path did, and the note in the window reports it.
func TestManualRefreshLeavesTheAutomaticCountAlone(t *testing.T) {
	rm := newRoom("session", nil, func([]byte) error { return nil }, quietLogger(), "")
	defer rm.close()
	exec := newExecutor(executorConfig{Auth: soloAuthority{rm}, Logger: quietLogger(), Repo: t.TempDir()})
	gateFor(exec, briefOfWords(100))
	exec.mu.Lock()
	before := exec.gate.refreshed
	exec.mu.Unlock()
	exec.PressRefresh()
	exec.mu.Lock()
	after := exec.gate.refreshed
	exec.mu.Unlock()
	if after != before {
		t.Fatalf("automatic count moved from %d to %d on a manual press", before, after)
	}
}
