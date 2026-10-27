package app

import "strings"

// The composer re-reads the brief while it is being written, so the plan rail
// and the impact map follow the text without a model call per keystroke.
//
// The policy is the owner's:
//
//   - Nothing happens until the document passes firstRefreshWords.
//   - After that, a refresh is due once at least stepWords more words arrived
//     since the last one, and the writer has paused.
//   - The steps are 30, 60 and 120 words, and the automatic path stops after
//     maxAutomaticRefreshes. Four refreshes is what a brief needs: a first read
//     at 100 words, then a re-read at 130, 190 and 310.
//   - A manual refresh switches the automatic path off for the session. Two
//     mechanisms re-reading the same document would bill twice and race each
//     other, and a writer who presses Refresh has taken control of it.

// The automatic refresh policy.
const (
	// firstRefreshWords is the length at which a first automatic refresh is
	// worth its cost. Below it the writer is sketching, and an interpretation
	// would describe a document they are about to replace.
	firstRefreshWords = 100
	// maxAutomaticRefreshes bounds the automatic path per session.
	maxAutomaticRefreshes = 4
	// stepWords are the word increases that earn refreshes 2, 3 and 4.
	stepWords30  = 30
	stepWords60  = 60
	stepWords120 = 120
)

// RefreshState is what the window shows about the automatic path, so a writer
// can tell "it is about to read this" from "it has finished and is waiting for
// you".
type RefreshState struct {
	// Automatic is false once the refreshes are spent or a manual refresh took
	// over. It is also false below the first threshold.
	Automatic bool `json:"automatic"`
	// AutomaticOff is true only when the owner switched the path off by hand,
	// so the window can say why rather than implying the document is short.
	AutomaticOff bool `json:"automaticOff"`
	// Refreshes and MaxRefreshes are the automatic count for this session.
	Refreshes    int `json:"refreshes"`
	MaxRefreshes int `json:"maxRefreshes"`
	// WordsRemaining is how many more words earn the next automatic refresh.
	WordsRemaining int `json:"wordsRemaining"`
}

// refreshGate decides whether an automatic interpretation is due. It is not
// safe for concurrent use: the executor holds its own lock.
type refreshGate struct {
	// refreshed counts automatic refreshes in this session.
	refreshed int
	// lastWords is the word count at the last automatic refresh.
	lastWords int
	// manual records that the owner pressed Refresh, which switches the
	// automatic path off for the rest of the session.
	manual bool
}

// due reports whether this text earns an automatic refresh, and records the
// decision so the caller must act on a true answer. The pause that keeps a
// refresh from landing mid-keystroke is the executor's debounce, which is why
// this takes no clock: one place decides when the writer has stopped typing.
func (g *refreshGate) due(text string) bool {
	if g.manual || g.refreshed >= maxAutomaticRefreshes {
		return false
	}
	words := CountWords(text)
	if words < firstRefreshWords {
		return false
	}
	// The first refresh is governed by the threshold alone; later ones by the
	// word increase since the previous refresh.
	if g.refreshed > 0 && words-g.lastWords < g.nextStep() {
		return false
	}
	g.refreshed++
	g.lastWords = words
	return true
}

// nextStep is the word increase that earns the next refresh. The step grows
// with each refresh, so a longer brief costs no more calls than a short one.
func (g *refreshGate) nextStep() int {
	switch g.refreshed {
	case 1:
		return stepWords30
	case 2:
		return stepWords60
	default:
		return stepWords120
	}
}

// state describes the gate for the window.
func (g *refreshGate) state(text string) RefreshState {
	out := RefreshState{
		Automatic:    !g.manual && g.refreshed < maxAutomaticRefreshes,
		AutomaticOff: g.manual,
		Refreshes:    g.refreshed,
		MaxRefreshes: maxAutomaticRefreshes,
	}
	if !out.Automatic {
		return out
	}
	words := CountWords(text)
	next := firstRefreshWords
	if g.refreshed > 0 {
		next = g.lastWords + g.nextStep()
	}
	if next > words {
		out.WordsRemaining = next - words
	}
	return out
}

// press records a manual refresh, which switches the automatic path off. The
// automatic count is left alone: it is a record of what the automatic path did,
// not of how many times the document was read.
func (g *refreshGate) press() {
	g.manual = true
}

// CountWords counts whitespace-separated words. Markdown punctuation and fences
// are not stripped: the count only decides when to spend a call, and a stable
// approximation is what matters.
func CountWords(text string) int {
	return len(strings.Fields(text))
}
