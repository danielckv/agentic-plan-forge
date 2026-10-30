//go:build client && !server

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// Frontend names where a session is presented.
//
// The executor, the authority and the wire protocol are identical for both: a
// frontend is a view of one session, never a second authority. The distinction
// exists only to decide whether a browser is required, which the plan states
// explicitly, and to describe the session honestly in the state file.
type Frontend string

// Frontends a session can be presented in.
const (
	// FrontendBrowser opens the embedded UI in the user's browser. Default.
	FrontendBrowser Frontend = ""
	// FrontendTerminal draws the session in this terminal, with no browser.
	FrontendTerminal Frontend = "terminal"
)

// Terminal reports whether this frontend is the terminal one.
func (f Frontend) Terminal() bool { return f == FrontendTerminal }

// ParseFrontend validates a --frontend value. Empty and "browser" both select
// the browser, because that is the default a caller who says nothing gets.
func ParseFrontend(raw string) (Frontend, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "browser", "web":
		return FrontendBrowser, nil
	case "terminal", "tty", "tui":
		return FrontendTerminal, nil
	default:
		return "", fmt.Errorf("unknown --frontend %q (supported: browser, terminal)", raw)
	}
}

// terminalCredentials mints the one-time nonce a terminal frontend redeems and
// returns the origin and the bridge token. It goes through the same
// POST /api/session handshake the page uses, so a terminal is authenticated by
// exactly the code path a browser is: one handshake, two frontends.
func terminalCredentials(bridge *relay.Bridge) (origin, token string, err error) {
	launchURL, err := bridge.LaunchURL()
	if err != nil {
		return "", "", fmt.Errorf("mint a launch url: %w", err)
	}
	_, nonce, ok := strings.Cut(launchURL, "#n=")
	if !ok || nonce == "" {
		return "", "", fmt.Errorf("launch url carries no nonce")
	}
	body, _ := json.Marshal(map[string]string{"nonce": nonce})
	req, err := http.NewRequest(http.MethodPost, bridge.Origin()+"/api/session", strings.NewReader(string(body)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", bridge.Origin())
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("redeem the launch nonce: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("POST /api/session answered %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", err
	}
	if out.Token == "" {
		return "", "", fmt.Errorf("the bridge returned an empty token")
	}
	return bridge.Origin(), out.Token, nil
}

// windowOutcome adapts a frontend handle to the channel StartWindow would have
// returned: one Result when the frontend goes away for good.
func windowOutcome(ctx context.Context, win windowHandle) <-chan Result {
	out := make(chan Result, 1)
	go func() {
		defer close(out)
		select {
		case err := <-win.Done():
			if err != nil {
				out <- resultFor(PhaseFailed, err.Error(), nil)
				return
			}
			out <- resultFor(PhaseCancelled, "the terminal frontend closed without an approval", nil)
		case <-ctx.Done():
		}
	}()
	return out
}
