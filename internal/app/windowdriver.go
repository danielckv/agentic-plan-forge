//go:build client && !server

package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/window"
)

// windowParams configures startWindow.
type windowParams struct {
	Bridge      *relay.Bridge
	Launch      func(url string) (windowHandle, error)
	BrowserPath string
	Logger      *slog.Logger
	Detached    bool
	Grace       time.Duration
	StartupWait time.Duration
	// OnRunning is called once, when the window first connects and authenticates.
	OnRunning func()
	// OnWindowGone is called whenever the window's socket closes.
	OnWindowGone func()
}

// startWindow launches the dedicated window against the bridge and supervises
// it. The returned channel receives one Result when the window goes away for
// good (closed without coming back, or never connected); it receives nothing
// when the caller's context ends first.
func startWindow(ctx context.Context, p windowParams) (windowHandle, <-chan Result, error) {
	launch := p.Launch
	if launch == nil {
		launch = func(url string) (windowHandle, error) {
			w, err := window.Launch(window.Options{URL: url, BrowserPath: p.BrowserPath, Logger: p.Logger})
			if err != nil {
				return nil, err
			}
			return w, nil
		}
	}
	url, err := p.Bridge.LaunchURL()
	if err != nil {
		return nil, nil, err
	}
	win, err := launch(url)
	if err != nil {
		return nil, nil, err
	}

	// The window socket's events feed the supervisor, the state file and the
	// authority. A channel has one reader, so tee it.
	sup := make(chan bool, 16)
	var first sync.Once
	go func() {
		defer close(sup)
		for up := range p.Bridge.UIEvents() {
			if up {
				first.Do(func() {
					if p.OnRunning != nil {
						p.OnRunning()
					}
				})
			} else if p.OnWindowGone != nil {
				p.OnWindowGone()
			}
			select {
			case sup <- up:
			case <-ctx.Done():
				return
			}
		}
	}()

	grace, startup := p.Grace, p.StartupWait
	if grace <= 0 {
		grace = window.DefaultGrace
	}
	if startup <= 0 {
		startup = window.DefaultStartupWait
		if p.Detached {
			// The parent gives up on a window that has not connected at 30 s and
			// records startup_timeout itself; outlast it so that path is the one taken.
			startup += 15 * time.Second
		}
	}
	outcome := make(chan Result, 1)
	go func() {
		o, serr := window.Supervise(ctx, window.Signals{UI: sup, Process: win.Done()}, grace, startup)
		switch o {
		case window.OutcomeWindowGone:
			outcome <- resultFor(PhaseCancelled, "the Plan-Forge window was closed without an approval", nil)
		case window.OutcomeNeverConnected:
			outcome <- resultFor(PhaseFailed, notice(serr), nil)
		}
	}()
	return win, outcome, nil
}
