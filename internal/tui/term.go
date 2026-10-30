//go:build client && !server

package tui

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sync"

	"golang.org/x/term"
)

// terminal is the real screen: raw mode, alternate buffer, cursor hidden.
//
// The discipline that matters on a real tty:
//   - raw mode is set once and restored on every exit path, twice-safe, so a
//     crash never leaves the reader with a broken shell;
//   - the alternate buffer is entered so the session does not scroll away, and
//     left on restore so the shell's scrollback is untouched;
//   - every Draw is a single write of a full frame with the cursor homed first,
//     so a partially written frame can never interleave with another;
//   - resize arrives as a SIGWINCH value on a channel, not by polling.
type terminal struct {
	in  *os.File
	out *os.File

	mu     sync.Mutex
	once   sync.Once
	resize chan int
	old    *term.State
	reader *bufio.Reader
}

// stdTerminal binds the process's own stdin and stdout.
func stdTerminal() Terminal {
	t := &terminal{in: os.Stdin, out: os.Stdout, resize: make(chan int, 4)}
	t.start()
	return t
}

// start enters raw mode and the alternate buffer, installs the resize channel,
// and hides the cursor.
func (t *terminal) start() {
	if !term.IsTerminal(int(t.in.Fd())) {
		return
	}
	if st, err := term.MakeRaw(int(t.in.Fd())); err == nil {
		t.old = st
	}
	t.reader = bufio.NewReaderSize(t.in, 64)
	// Alternate buffer + hide cursor + clear.
	fmt.Fprint(t.out, "\x1b[?1049h\x1b[?25l\x1b[2J")
	t.watchResize()
}

// watchResize forwards SIGWINCH widths onto the channel.
func (t *terminal) watchResize() {
	ch := make(chan os.Signal, 1)
	notifyResize(ch)
	go func() {
		for range ch {
			select {
			case t.resize <- t.Width():
			default: // a pending resize is enough; the width is read fresh
			}
		}
	}()
}

// Draw writes one frame: cursor home, clear to end of screen, then the text.
func (t *terminal) Draw(frame string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Clear from home so a shorter frame cannot leave the previous frame's tail.
	fmt.Fprint(t.out, "\x1b[H\x1b[2J", frame)
}

// Width is the current column count, bounded so a silly size cannot break the
// layout arithmetic.
func (t *terminal) Width() int {
	w, _, err := term.GetSize(int(t.out.Fd()))
	if err != nil || w <= 0 {
		return int(DefaultWidth)
	}
	if w < int(MinWidth) {
		return int(MinWidth)
	}
	return w
}

// Resize exposes the width channel.
func (t *terminal) Resize() <-chan int { return t.resize }

// ReadKey reads one key press, interpreting the escape sequences a terminal
// sends for the arrow keys, Tab and Enter.
func (t *terminal) ReadKey(ctx context.Context) (keyMsg, error) {
	if t.reader == nil {
		return keyMsg{}, context.Canceled
	}
	type read struct {
		b   byte
		err error
	}
	// A blocking byte read cannot be cancelled directly; the ctx check between
	// bytes is what keeps Ctrl-C and a closed stdin from wedging the loop.
	if err := ctx.Err(); err != nil {
		return keyMsg{}, err
	}
	b, err := t.reader.ReadByte()
	if err != nil {
		return keyMsg{}, err
	}
	switch b {
	case 0x03, 0x04: // Ctrl-C, Ctrl-D
		return keyMsg{Special: "esc"}, nil
	case '\r', '\n':
		return keyMsg{Special: "enter"}, nil
	case '\t':
		return keyMsg{Special: "tab"}, nil
	case 0x13: // Ctrl-S
		return keyMsg{Special: "ctrl-s"}, nil
	case 0x7f, 0x08: // DEL and Backspace
		return keyMsg{Special: "backspace"}, nil
	case 0x1b:
		// Either a bare Escape or the start of a sequence.
		if t.reader.Buffered() < 2 {
			return keyMsg{Special: "esc"}, nil
		}
		seq := make([]byte, 2)
		if _, err := t.reader.Read(seq); err != nil {
			return keyMsg{Special: "esc"}, nil
		}
		switch string(seq) {
		case "[A":
			return keyMsg{Special: "up"}, nil
		case "[B":
			return keyMsg{Special: "down"}, nil
		case "[Z":
			return keyMsg{Special: "backtab"}, nil
		}
		return keyMsg{Special: "esc"}, nil
	}
	if b < 0x20 {
		return keyMsg{}, nil // other control keys are not bound
	}
	return keyMsg{Rune: rune(b)}, nil
}

// Restore leaves the alternate buffer, shows the cursor and returns the terminal
// to cooked mode. Safe to call twice, and safe on a tty that was never raw.
func (t *terminal) Restore() {
	t.once.Do(func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		fmt.Fprint(t.out, "\x1b[?25h\x1b[?1049l")
		if t.old != nil {
			_ = term.Restore(int(t.in.Fd()), t.old)
		}
	})
}

// notifyResize reports window-size changes. It is a variable so the platform
// helper can be replaced in a test.
var notifyResize = func(ch chan os.Signal) { notifyResizePlatform(ch) }
