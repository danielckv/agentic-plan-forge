//go:build client && !server

package tui

import (
	"context"
	"sync"
	"time"
)

// Start runs the terminal frontend and returns a handle the session supervises
// like a window.
//
// The session treats the two frontends the same way: a handle that reports when
// it goes away and can be terminated. That symmetry is deliberate — a terminal
// that dies ends the session the same way a closed tab does, so the executor's
// lifecycle has one shape regardless of where the session was drawn.
func Start(ctx context.Context, opts Options) (Handle, error) {
	if opts.Tick <= 0 {
		opts.Tick = time.Second
	}
	runCtx, cancel := context.WithCancel(ctx)
	h := &handle{done: make(chan error, 1), cancel: cancel}
	go func() {
		defer close(h.done)
		err := Run(runCtx, opts)
		if err != nil && runCtx.Err() == nil {
			h.done <- err
			return
		}
		// A terminal closed by the reader is a normal end: nil, not an error.
		h.done <- nil
	}()
	return h, nil
}

// Handle is what a caller supervises. It matches app.FrontendHandle.
type Handle interface {
	Done() <-chan error
	Terminate(after time.Duration)
}

// handle is one running terminal frontend.
type handle struct {
	done   chan error
	cancel context.CancelFunc
	once   sync.Once
}

// Done reports the frontend's end: nil when the reader quit, an error when the
// socket failed.
func (h *handle) Done() <-chan error { return h.done }

// Terminate stops the frontend after the given delay, the same contract the
// window handle offers.
func (h *handle) Terminate(after time.Duration) {
	if after > 0 {
		time.Sleep(after)
	}
	h.once.Do(h.cancel)
}
