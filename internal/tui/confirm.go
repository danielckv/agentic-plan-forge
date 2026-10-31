//go:build client && !server

package tui

import (
	"context"
	"fmt"
	"strings"
)

// StdTerminal exposes the process's own terminal for one-shot prompts that run
// outside a session: the install-time question and the configuration editor
// have no bridge to dial, so they bind stdin and stdout directly.
func StdTerminal() Terminal { return stdTerminal() }

// startable is the real terminal's extra seam: a prompt enters raw mode and
// the alternate buffer before it reads keys, exactly the way Run does. A test
// terminal has no mode to set and simply lacks the method.
type startable interface{ start() }

// Confirm asks one yes/no question with an arrow-key selector and reports the
// answer. Up and down (or Tab) move the highlight, Enter chooses it, y and n
// answer directly, and q or Escape always means no. The highlight starts on
// No: a prompt that can install an editor session on an accidental Enter is a
// prompt that asks a question it should not.
func Confirm(ctx context.Context, term Terminal, question string) (bool, error) {
	if s, ok := term.(startable); ok {
		s.start()
		defer term.Restore()
	}
	options := []string{"No", "Yes"}
	selected := 0
	term.Draw(confirmFrame(question, options, selected))
	for {
		k, err := term.ReadKey(ctx)
		if err != nil {
			return false, err
		}
		switch {
		case k.Special == "up" || k.Special == "down" ||
			k.Special == "tab" || k.Special == "backtab":
			selected = 1 - selected
		case k.Special == "enter":
			return selected == 1, nil
		case k.Special == "esc" || k.Rune == 'q':
			return false, nil
		case k.Rune == 'y':
			return true, nil
		case k.Rune == 'n':
			return false, nil
		default:
			continue
		}
		term.Draw(confirmFrame(question, options, selected))
	}
}

// confirmFrame renders the selector. It is pure so a test can assert the
// layout without a terminal.
func confirmFrame(question string, options []string, selected int) string {
	var b strings.Builder
	b.WriteString("\n  " + question + "\n\n")
	for i, opt := range options {
		if i == selected {
			fmt.Fprintf(&b, "  \x1b[7m ❯ %s \x1b[0m\n", opt)
		} else {
			fmt.Fprintf(&b, "    %s\n", opt)
		}
	}
	b.WriteString("\n  ↑/↓ choose · Enter select · y/n answer · q/Esc no\n")
	return b.String()
}
