//go:build client && !server

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/applock"
	"github.com/danielckv/agentic-plan-forge/internal/center"
	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	webui "github.com/danielckv/agentic-plan-forge/web"
)

// Budgets for the command center. The ensure path runs inside an agent hook,
// so its whole job must fit well inside the two seconds the hook contracts
// allow, with the probe measured in milliseconds on loopback.
const (
	CenterProbeTimeout = 250 * time.Millisecond
	CenterEnsureBound  = 1500 * time.Millisecond
	centerStartupPoll  = 25 * time.Millisecond
	// centerOpenCooldown stops several terminals starting at once from each
	// opening a tab: the first to win the guard records the attempt, and the
	// rest see it and stand down. A browser cold start fits comfortably inside.
	centerOpenCooldown = 20 * time.Second
)

// CenterOptions configures the daemon and the ensure path.
type CenterOptions struct {
	Port int
	Repo string
	Open bool
}

// CenterResult is the outcome of running or ensuring the daemon.
type CenterResult struct {
	PID           int
	Port          int
	URL           string
	ActiveClients int
	AlreadyUp     bool
	Opened        bool
	ExitCode      int
	Message       string
}

// centerDir is the daemon's state directory under the cache, created 0700.
func centerDir() (string, error) {
	cache, err := config.EnsureCache()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "center")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func centerLockPath() (string, error) {
	dir, err := centerDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.pid"), nil
}

// centerSecretPath is in the home directory, not the cache: it is a
// credential, and it sits beside relay-token.json for the same reason.
func centerSecretPath() (string, error) {
	home, err := config.EnsureHome()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "center")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.token"), nil
}

// readCenterSecret returns the running daemon's token.
func readCenterSecret() (string, error) {
	path, err := centerSecretPath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func writeCenterSecret(secret string) error {
	path, err := centerSecretPath()
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(secret+"\n"), 0o600)
}

// CenterHolder reports the live daemon, if one is running.
func CenterHolder() (applock.Record, bool) {
	path, err := centerLockPath()
	if err != nil {
		return applock.Record{}, false
	}
	return applock.Holder(path)
}

// RunCenter serves the command center in the foreground until ctx ends. It
// takes the single-instance lock first, so a second daemon reports the first
// rather than fighting it for the port.
func RunCenter(ctx context.Context, opts CenterOptions) CenterResult {
	lockPath, err := centerLockPath()
	if err != nil {
		return CenterResult{ExitCode: ExitFailure, Message: notice(err)}
	}
	if held, ok := applock.Holder(lockPath); ok {
		return CenterResult{
			PID: held.PID, Port: held.Port, AlreadyUp: true,
			URL: centerURL(held.Port), ExitCode: ExitOK,
		}
	}
	rec, won, err := applock.Acquire(lockPath, applock.Record{Key: "center", Port: opts.Port})
	if err != nil {
		return CenterResult{ExitCode: ExitFailure, Message: notice(err)}
	}
	if !won {
		return CenterResult{
			PID: rec.PID, Port: rec.Port, AlreadyUp: true,
			URL: centerURL(rec.Port), ExitCode: ExitOK,
		}
	}
	defer applock.Release(lockPath)

	secret, err := cryptoSecret()
	if err != nil {
		return CenterResult{ExitCode: ExitFailure, Message: notice(err)}
	}
	srv, err := center.New(center.Config{Port: opts.Port, Assets: webui.Assets(), Secret: secret})
	if err != nil {
		// A port already in use with no live holder is a real conflict with
		// another program. Moving to a different port would break every
		// bookmark, so it is reported instead.
		if isAddrInUse(err) {
			return CenterResult{
				ExitCode: ExitFailure,
				Message: fmt.Sprintf(
					"the command center port %d is in use by another program; set center_port in the config or pass --port",
					portOrDefault(opts.Port)),
			}
		}
		return CenterResult{ExitCode: ExitFailure, Message: notice(err)}
	}
	if err := writeCenterSecret(secret); err != nil {
		_ = srv.Close()
		return CenterResult{ExitCode: ExitFailure, Message: notice(err)}
	}
	rec.State = applock.StateRunning
	rec.Port = srv.Port()
	if err := applock.Write(lockPath, rec); err != nil {
		_ = srv.Close()
		return CenterResult{ExitCode: ExitFailure, Message: notice(err)}
	}
	if err := srv.Run(ctx); err != nil {
		return CenterResult{ExitCode: ExitFailure, Message: notice(err)}
	}
	return CenterResult{PID: os.Getpid(), Port: srv.Port(), URL: srv.URL(), ExitCode: ExitOK}
}

func portOrDefault(p int) int {
	if p <= 0 {
		return center.DefaultPort
	}
	return p
}

func centerURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", portOrDefault(port))
}

func isAddrInUse(err error) bool {
	return err != nil && strings.Contains(err.Error(), "address already in use")
}

func cryptoSecret() (string, error) { return center.NewSecret() }

// EnsureCenter makes sure the daemon is up and, when asked, opens one tab.
//
// It is what a session-start hook calls, so it never blocks: the probe and the
// spawn together are bounded by CenterEnsureBound and every failure is silent.
// A tab is opened only when no tab is already streaming, and only when no
// other process opened one inside the cooldown, so three terminals starting
// together still produce one tab.
func EnsureCenter(ctx context.Context, opts CenterOptions) CenterResult {
	ctx, cancel := context.WithTimeout(ctx, CenterEnsureBound)
	defer cancel()

	rec, up := CenterHolder()
	spawned := false
	if !up {
		started, err := startCenterDetached(ctx, opts)
		if err != nil {
			return CenterResult{ExitCode: ExitOK, Message: notice(err)}
		}
		rec, spawned = started, true
	}

	res := CenterResult{
		PID: rec.PID, Port: rec.Port, URL: centerURL(rec.Port),
		AlreadyUp: !spawned, ExitCode: ExitOK,
	}
	secret, err := readCenterSecret()
	if err != nil {
		return res
	}
	clients, err := centerClients(ctx, rec.Port, secret)
	if err != nil {
		return res
	}
	res.ActiveClients = clients
	if !opts.Open || clients > 0 {
		return res
	}
	if !claimCenterOpen() {
		return res
	}
	url, err := centerTicketURL(ctx, rec.Port, secret, opts.Repo)
	if err != nil {
		return res
	}
	if err := openInBrowser(url); err == nil {
		res.Opened = true
	}
	return res
}

// centerClients asks the daemon how many tabs are streaming.
func centerClients(ctx context.Context, port int, secret string) (int, error) {
	var body struct {
		ActiveClients int `json:"active_clients"`
	}
	if err := centerGetJSON(ctx, port, "/api/center/clients", secret, &body); err != nil {
		return 0, err
	}
	return body.ActiveClients, nil
}

// centerTicketURL mints a single-use launch ticket and returns the URL to open.
func centerTicketURL(ctx context.Context, port int, secret, repo string) (string, error) {
	var body struct {
		URL string `json:"url"`
	}
	if err := centerPostJSON(ctx, port, "/api/center/ticket", secret, nil, &body); err != nil {
		return "", err
	}
	if body.URL == "" {
		return "", errors.New("the daemon returned no launch url")
	}
	return body.URL, nil
}

// loopbackClient refuses to dial anything but the daemon's own address, so a
// hijacked hostname cannot turn a hook into an outbound request.
func loopbackClient(port int) *http.Client {
	want := fmt.Sprintf("127.0.0.1:%d", port)
	return &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if addr != want {
					return nil, fmt.Errorf("refusing to dial %s", addr)
				}
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}
}

func centerGetJSON(ctx context.Context, port int, path, secret string, out any) error {
	return centerDo(ctx, port, http.MethodGet, path, secret, nil, out)
}

func centerPostJSON(ctx context.Context, port int, path, secret string, in, out any) error {
	return centerDo(ctx, port, http.MethodPost, path, secret, in, out)
}

func centerDo(ctx context.Context, port int, method, path, secret string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, CenterProbeTimeout)
	defer cancel()
	var body *strings.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(raw))
	} else {
		body = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, method, centerURL(port)+path, body)
	if err != nil {
		return err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := loopbackClient(port).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("center: %s %s returned %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// claimCenterOpen records that this process is about to open a tab and reports
// whether it won the claim. The check and the write happen under the daemon
// lock's guard file, so it is atomic across processes rather than a read
// followed by a hopeful write.
func claimCenterOpen() bool {
	dir, err := centerDir()
	if err != nil {
		return false
	}
	lockPath, err := centerLockPath()
	if err != nil {
		return false
	}
	unlock, err := applock.LockGuard(lockPath + ".guard")
	if err != nil {
		return false
	}
	defer unlock()

	stamp := filepath.Join(dir, "opened.json")
	if raw, err := os.ReadFile(stamp); err == nil {
		var last struct {
			At time.Time `json:"at"`
		}
		if json.Unmarshal(raw, &last) == nil && time.Since(last.At) < centerOpenCooldown {
			return false
		}
	}
	raw, err := json.Marshal(map[string]any{"at": time.Now().UTC(), "by": os.Getpid()})
	if err != nil {
		return false
	}
	return os.WriteFile(stamp, raw, 0o600) == nil
}

// openInBrowser hands the URL to the user's default browser. The command
// center is a tab by design, unlike the forge window, so this is the OS opener
// rather than the dedicated-window launcher.
func openInBrowser(raw string) error {
	safe, err := relay.ValidateOpenURL(raw)
	if err != nil {
		return err
	}
	return relay.OpenURLArgv(safe)
}

// startCenterDetached spawns the daemon in its own session and waits for it to
// report running. A daemon another process started meanwhile is used as is.
func startCenterDetached(ctx context.Context, opts CenterOptions) (applock.Record, error) {
	lockPath, err := centerLockPath()
	if err != nil {
		return applock.Record{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return applock.Record{}, err
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	dir, err := centerDir()
	if err != nil {
		return applock.Record{}, err
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return applock.Record{}, err
	}
	args := []string{"__center-child"}
	if opts.Port > 0 {
		args = append(args, "--port", fmt.Sprint(opts.Port))
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	detachAttrs(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return applock.Record{}, err
	}
	_ = logFile.Close()
	// Release rather than Wait: the daemon outlives this process by design.
	_ = cmd.Process.Release()

	ticker := time.NewTicker(centerStartupPoll)
	defer ticker.Stop()
	for {
		if rec, ok := applock.Holder(lockPath); ok && rec.State == applock.StateRunning {
			return rec, nil
		}
		select {
		case <-ctx.Done():
			return applock.Record{}, errors.New("the command center did not start in time")
		case <-ticker.C:
		}
	}
}
