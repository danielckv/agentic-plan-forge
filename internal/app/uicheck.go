//go:build client && !server

// Package app composes the Plan Forge client: the loopback bridge and dedicated
// window, the in-process Solo authority (or the relay connection in Remote
// mode), the executor that drives ingestion, grounding, the avatar pipeline and
// approval, and the session-state file the detached-run protocol relies on.
//
// cmd/client constructs these services and maps their results onto exit codes;
// it never imports web/ or the lower-level packages directly for session work.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
	webui "github.com/danielckv/agentic-plan-forge/web"
)

// UIAssets returns the embedded UI rooted at its index.html.
func UIAssets() fs.FS { return webui.Assets() }

var hashedAssetRe = regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)

// CheckUI is the release smoke test behind the hidden `serve-ui --check`
// subcommand: it serves the embedded UI on 127.0.0.1:0, fetches "/" and one
// hashed asset from itself, and returns nil only when both answer 200 with the
// bridge's security headers.
func CheckUI(ctx context.Context) error {
	bridge, err := relay.NewBridge(relay.BridgeConfig{Assets: UIAssets()})
	if err != nil {
		return err
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- bridge.Serve(sctx) }()
	defer bridge.Close()

	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	base := "http://" + bridge.Addr()

	index, status, header, err := fetch(sctx, client, base+"/")
	if err != nil {
		return fmt.Errorf("GET /: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET / answered %d", status)
	}
	if header.Get("Content-Security-Policy") != relay.BridgeCSP {
		return errors.New("GET / is missing the Content-Security-Policy header")
	}
	m := hashedAssetRe.FindSubmatch(index)
	if m == nil {
		return errors.New("index.html references no hashed asset under /assets/")
	}
	asset := string(m[1])
	if strings.Contains(asset, "..") {
		return fmt.Errorf("refusing suspicious asset path %q", asset)
	}
	body, status, _, err := fetch(sctx, client, base+asset)
	if err != nil {
		return fmt.Errorf("GET %s: %w", asset, err)
	}
	if status != http.StatusOK || len(body) == 0 {
		return fmt.Errorf("GET %s answered %d with %d bytes", asset, status, len(body))
	}
	select {
	case err := <-serveErr:
		if err != nil {
			return err
		}
	default:
	}
	return nil
}

func fetch(ctx context.Context, c *http.Client, rawURL string) ([]byte, int, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return body, resp.StatusCode, resp.Header, err
}
