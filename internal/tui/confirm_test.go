//go:build client && !server

package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// scriptTerminal feeds a recorded key sequence and keeps every frame it was
// asked to draw, so a test can drive a prompt exactly the way the terminal
// decoder would and inspect what the reader saw.
type scriptTerminal struct {
	keys   []keyMsg
	frames []string
}

func (t *scriptTerminal) Draw(frame string)  { t.frames = append(t.frames, frame) }
func (t *scriptTerminal) Width() int         { return 100 }
func (t *scriptTerminal) Resize() <-chan int { return nil }
func (t *scriptTerminal) Restore()           {}

func (t *scriptTerminal) ReadKey(context.Context) (keyMsg, error) {
	if len(t.keys) == 0 {
		return keyMsg{}, errors.New("script exhausted")
	}
	k := t.keys[0]
	t.keys = t.keys[1:]
	return k, nil
}

// typed turns a string into the rune keys the decoder would deliver.
func typed(s string) []keyMsg {
	out := make([]keyMsg, 0, len(s))
	for _, r := range s {
		out = append(out, keyMsg{Rune: r})
	}
	return out
}

func special(names ...string) []keyMsg {
	out := make([]keyMsg, 0, len(names))
	for _, n := range names {
		out = append(out, keyMsg{Special: n})
	}
	return out
}

func confirmScript(t *testing.T, keys ...keyMsg) (bool, *scriptTerminal) {
	t.Helper()
	term := &scriptTerminal{keys: keys}
	yes, err := Confirm(context.Background(), term, "Edit configuration now?")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	return yes, term
}

func TestConfirmDefaultsToNo(t *testing.T) {
	yes, term := confirmScript(t, keyMsg{Special: "enter"})
	if yes {
		t.Fatal("Enter on the initial highlight must answer no")
	}
	if !strings.Contains(term.frames[0], "❯ No") {
		t.Fatalf("initial frame highlights No:\n%s", term.frames[0])
	}
}

func TestConfirmArrowSelectsYes(t *testing.T) {
	for _, name := range []string{"down", "up", "tab", "backtab"} {
		keys := append(special(name), keyMsg{Special: "enter"})
		if yes, _ := confirmScript(t, keys...); !yes {
			t.Fatalf("%s then Enter must answer yes", name)
		}
	}
}

func TestConfirmArrowTogglesBack(t *testing.T) {
	keys := append(special("down", "up"), keyMsg{Special: "enter"})
	if yes, _ := confirmScript(t, keys...); yes {
		t.Fatal("down then up must land back on No")
	}
}

func TestConfirmDirectAnswers(t *testing.T) {
	if yes, _ := confirmScript(t, keyMsg{Rune: 'y'}); !yes {
		t.Fatal("y must answer yes")
	}
	if yes, _ := confirmScript(t, keyMsg{Rune: 'n'}); yes {
		t.Fatal("n must answer no")
	}
}

func TestConfirmQuitMeansNo(t *testing.T) {
	for _, k := range []keyMsg{{Special: "esc"}, {Rune: 'q'}} {
		if yes, _ := confirmScript(t, k); yes {
			t.Fatalf("%+v must answer no", k)
		}
	}
}

func TestConfirmFrameLayout(t *testing.T) {
	frame := confirmFrame("Edit configuration now?", []string{"No", "Yes"}, 1)
	if !strings.Contains(frame, "Edit configuration now?") {
		t.Fatal("frame lacks the question")
	}
	if !strings.Contains(frame, "❯ Yes") || !strings.Contains(frame, "\x1b[7m") {
		t.Fatalf("frame does not highlight the selection:\n%s", frame)
	}
}
