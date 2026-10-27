package app

import (
	"strings"
	"testing"
)

// The composer refreshes itself at most four times: a first read past 100
// words, then re-reads at 30, 60 and 120 more words. The count is the property
// that keeps a long brief from costing a call per paragraph.

func words(n int) string {
	return strings.TrimSpace(strings.Repeat("word ", n))
}

func TestRefreshGateWaitsForTheFirstHundredWords(t *testing.T) {
	var g refreshGate
	for _, n := range []int{0, 1, 40, 99} {
		if g.due(words(n)) {
			t.Errorf("%d words earned a refresh, want none before %d", n, firstRefreshWords)
		}
	}
	if g.refreshed != 0 {
		t.Fatalf("refreshed = %d after refusals, want 0", g.refreshed)
	}
	if !g.due(words(100)) {
		t.Fatalf("%d words earned no refresh", firstRefreshWords)
	}
	if g.refreshed != 1 {
		t.Fatalf("refreshed = %d, want 1", g.refreshed)
	}
}

// The steps are 30, 60 and 120 words, so four refreshes carry the writer to
// 310 words and then stop.
func TestRefreshGateStepsAndStops(t *testing.T) {
	var g refreshGate
	if !g.due(words(100)) {
		t.Fatal("100 words earned no first refresh")
	}
	// Below the next step: nothing is due.
	if g.due(words(129)) {
		t.Error("29 more words earned a refresh, want none until 30")
	}
	if !g.due(words(130)) {
		t.Fatal("130 words earned no second refresh")
	}
	if g.due(words(189)) {
		t.Error("59 more words earned a refresh, want none before 60")
	}
	if !g.due(words(190)) {
		t.Fatal("190 words earned no third refresh")
	}
	if g.due(words(309)) {
		t.Error("119 more words earned a refresh, want none before 120")
	}
	if !g.due(words(310)) {
		t.Fatal("310 words earned no fourth refresh")
	}
	// Four is the limit, whatever arrives after it.
	for _, n := range []int{400, 1000, 5000} {
		if g.due(words(n)) {
			t.Errorf("%d words earned a fifth refresh, want the automatic path stopped", n)
		}
	}
	if g.refreshed != maxAutomaticRefreshes {
		t.Fatalf("refreshed = %d, want %d", g.refreshed, maxAutomaticRefreshes)
	}
}

// A brief that opens long gets its first read at once: the threshold is a
// length, not a quantity of typing.
func TestRefreshGateRefreshesALongOpeningAtOnce(t *testing.T) {
	var g refreshGate
	if !g.due(words(250)) {
		t.Fatal("a 250-word brief earned no first refresh")
	}
	// The next one is measured from there, not from empty.
	if g.due(words(300)) {
		t.Error("a refresh was due 50 words after the first, want none until 30 more and then 60")
	}
	if !g.due(words(310)) {
		t.Fatal("60 more words earned no second refresh")
	}
}

// A manual refresh switches the automatic path off. Two mechanisms re-reading
// the same document would bill twice and race.
func TestManualRefreshStopsTheAutomaticPath(t *testing.T) {
	var g refreshGate
	g.press()
	for _, n := range []int{100, 200, 400, 5000} {
		if g.due(words(n)) {
			t.Errorf("%d words refreshed automatically after a manual refresh", n)
		}
	}
	st := g.state(words(5000))
	if st.Automatic {
		t.Error("state still reports an automatic refresh after a manual one")
	}
	if !st.AutomaticOff {
		t.Error("state does not say the owner switched the automatic path off")
	}
	if st.Refreshes != 0 {
		t.Errorf("refreshes = %d after a manual refresh, want the automatic count untouched", st.Refreshes)
	}
}

// The window needs to tell "about to read this" from "finished, waiting for
// you", so the state carries both facts.
func TestRefreshStateSaysWhatRemains(t *testing.T) {
	var g refreshGate
	st := g.state(words(40))
	if !st.Automatic {
		t.Error("the first refresh should still be pending")
	}
	if st.WordsRemaining != firstRefreshWords-40 {
		t.Errorf("wordsRemaining = %d, want %d", st.WordsRemaining, firstRefreshWords-40)
	}
	if st.MaxRefreshes != maxAutomaticRefreshes {
		t.Errorf("maxRefreshes = %d, want %d", st.MaxRefreshes, maxAutomaticRefreshes)
	}

	g.due(words(100))
	st = g.state(words(100))
	if st.WordsRemaining != stepWords30 {
		t.Errorf("wordsRemaining = %d after the first refresh, want the next step %d", st.WordsRemaining, stepWords30)
	}

	// Spent: automatic false, but not because the owner switched it off.
	for _, n := range []int{130, 190, 310} {
		g.due(words(n))
	}
	st = g.state(words(900))
	if st.Automatic {
		t.Error("automatic is still reported true after four refreshes")
	}
	if st.AutomaticOff {
		t.Error("automaticOff is true, but the owner never switched it off")
	}
	if st.Refreshes != maxAutomaticRefreshes || st.WordsRemaining != 0 {
		t.Errorf("state = %+v, want four refreshes and nothing remaining", st)
	}
}

func TestCountWords(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"spaces only", "   \n\t ", 0},
		{"one", "hello", 1},
		{"a heading and a paragraph", "# Title\n\nTwo words here.\n", 5},
		{"markdown punctuation still counts as words", "- [ ] a task\n", 4},
	} {
		if got := CountWords(tc.text); got != tc.want {
			t.Errorf("%s: CountWords = %d, want %d", tc.name, got, tc.want)
		}
	}
}
