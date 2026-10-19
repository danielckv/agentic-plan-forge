//go:build client && !server

// Package center is the command center daemon: one long-lived loopback server
// that shows every live harness session, the sprint hierarchy and the local
// configuration, in an ordinary browser tab (contracts C14 and C15).
//
// It is a second surface beside the per-session window bridge in
// internal/relay, not a replacement for it. The bridge stays exactly as it is:
// an ephemeral port, a single-use launch nonce and one UI socket, launched into
// a dedicated Chromium window for the duration of one forge or board session.
// The command center outlives any one session, is reached at a fixed port, and
// fans out to several tabs.
//
// A fixed, guessable port is weaker than an ephemeral one, so every mitigation
// that still applies is carried over verbatim from the bridge: it binds
// loopback only, requires an exact Host (DNS rebinding), requires an exact
// Origin on every non-GET and on the event stream (cross-site requests), sends
// the same Content-Security-Policy and no-store headers on every response
// including errors, compares every credential in constant time, and keeps its
// state 0600. What the fixed port does leave open is recorded in C15: a
// same-machine process can bind the port while the daemon is down, and any
// process running as this user can read the daemon token. The model is
// same-user implies full trust; these controls defend against the network and
// against other browser origins, not against local code running as you.
package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/livestore"
)

// DefaultPort is the port the command center binds unless configured
// otherwise. It is fixed on purpose: a bookmarked tab must keep working, so
// the daemon never silently falls back to another port.
const DefaultPort = 8420

// CSP is the Content-Security-Policy sent with every response. It is the
// bridge's policy unchanged: scripts and connections stay on this origin,
// styles may be inline because the UI toolkit injects them, and nothing may
// frame the page or submit a form.
const CSP = "default-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; " +
	"form-action 'none'; frame-ancestors 'none'"

// Request bounds.
const (
	maxIngestBytes  = 8 << 10
	maxTicketBytes  = 512
	maxSessionBytes = 1 << 10
	readHeaderGrace = 5 * time.Second
	shutdownGrace   = 5 * time.Second
)

// Config configures a Server.
type Config struct {
	// Port is the loopback port to bind. Zero means DefaultPort; a test passes
	// -1 to ask for an ephemeral port.
	Port int
	// Assets is the embedded web bundle. A nil FS serves no UI, which is what
	// a test that only exercises the API wants.
	Assets fs.FS
	// LiveDir is the livestore directory. Empty means the machine's cache.
	LiveDir string
	// Secret is the daemon token. Empty means one is generated.
	Secret string
	// Sprint derives the plan view of one repository: its tasks by live
	// column and the coordinator's verdict on what to begin next.
	//
	// It is injected rather than imported because computing it needs the AST
	// scanner and so needs cgo, and this package must keep type-checking under
	// a CGO_ENABLED=0 cross-vet. A nil Sprint means no plan view, which is what
	// a test that only exercises the API wants.
	Sprint SprintFunc
}

// SprintFunc returns the plan view of the repository at an absolute path.
// ErrNoPlan means the repository has no approved plan, which is a normal
// answer and not a failure.
type SprintFunc func(ctx context.Context, repoPath string) (SprintView, error)

// ErrNoPlan reports a repository the forge has never approved a plan for.
var ErrNoPlan = errors.New("center: no approved plan")

// SprintTask is one planned task as the command center shows it.
type SprintTask struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	EpicID     string   `json:"epic_id,omitempty"`
	StoryID    string   `json:"story_id,omitempty"`
	Column     string   `json:"column"`
	Priority   string   `json:"priority,omitempty"`
	Blocked    bool     `json:"blocked,omitempty"`
	DependsOn  []string `json:"depends_on,omitempty"`
	Targets    int      `json:"targets"`
	Verified   int      `json:"verified"`
	TargetsMet int      `json:"targets_met"`
}

// SprintView is the coordinator's output for one repository.
//
// NextUp is the point of it: the live view states the task to begin now rather
// than leaving a reader to infer it from a list, and it re-ranks as soon as a
// discovery is accepted or new evidence lands.
type SprintView struct {
	RepoKey   string       `json:"repo_key"`
	RepoName  string       `json:"repo_name"`
	Title     string       `json:"title,omitempty"`
	Approved  bool         `json:"approved"`
	Tasks     []SprintTask `json:"tasks"`
	Active    []string     `json:"active"`
	NextUp    []string     `json:"next_up"`
	Blocked   []string     `json:"blocked,omitempty"`
	Awaiting  []string     `json:"awaiting_verification,omitempty"`
	Verified  int          `json:"verified"`
	Total     int          `json:"total"`
	Drifted   bool         `json:"drifted,omitempty"`
	UpdatedAt string       `json:"updated_at"`
}

// Server is a running command center.
type Server struct {
	cfg     Config
	host    string
	origin  string
	port    int
	secret  string
	ln      net.Listener
	srv     *http.Server
	hub     *hub
	tickets *ticketPool
	tokens  *sessionTokens
	liveDir string
	started time.Time
}

// New binds the port and prepares the server without serving. Binding here
// rather than in Run means a port already in use is reported to the caller
// that can still do something about it.
func New(cfg Config) (*Server, error) {
	port := cfg.Port
	switch {
	case port == 0:
		port = DefaultPort
	case port < 0:
		port = 0 // ephemeral, for tests
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	bound := ln.Addr().(*net.TCPAddr).Port

	secret := cfg.Secret
	if secret == "" {
		if secret, err = cryptoHex(daemonBytes); err != nil {
			_ = ln.Close()
			return nil, err
		}
	}
	liveDir := cfg.LiveDir
	if liveDir == "" {
		if liveDir, err = livestore.Dir(); err != nil {
			_ = ln.Close()
			return nil, err
		}
	}

	s := &Server{
		cfg:     cfg,
		host:    fmt.Sprintf("127.0.0.1:%d", bound),
		origin:  fmt.Sprintf("http://127.0.0.1:%d", bound),
		port:    bound,
		secret:  secret,
		ln:      ln,
		hub:     newHub(),
		tickets: newTicketPool(),
		tokens:  newSessionTokens(),
		liveDir: liveDir,
		started: time.Now(),
	}
	s.srv = &http.Server{
		Handler:           s.guard(s.routes()),
		ReadHeaderTimeout: readHeaderGrace,
	}
	return s, nil
}

// Port is the bound port.
func (s *Server) Port() int { return s.port }

// Secret is the daemon token a local caller must present.
func (s *Server) Secret() string { return s.secret }

// URL is the address a browser should open.
func (s *Server) URL() string { return s.origin }

// ActiveClients is how many tabs are streaming. `center ensure` reads it to
// decide whether opening another one would be a duplicate.
func (s *Server) ActiveClients() int { return s.hub.active() }

// Run serves until ctx is cancelled, then drains in-flight requests.
func (s *Server) Run(ctx context.Context) error {
	errs := make(chan error, 1)
	go func() {
		err := s.srv.Serve(s.ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errs <- err
	}()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		stop, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		_ = s.srv.Shutdown(stop)
		return <-errs
	}
}

// Close stops the server without waiting for Run.
func (s *Server) Close() error { return s.srv.Close() }

// routes is the whole HTTP surface. Only ping and the static bundle are
// unauthenticated; every endpoint that returns data requires a Bearer token,
// and every endpoint that changes state requires one and an exact Origin.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/center/ping", s.handlePing)
	mux.HandleFunc("POST /api/session", s.handleSession)
	mux.HandleFunc("GET /api/center/events", s.authed(s.handleEvents))
	mux.HandleFunc("GET /api/center/live", s.authed(s.handleLive))
	mux.HandleFunc("GET /api/center/sprint", s.authed(s.handleSprint))
	mux.HandleFunc("GET /api/center/status", s.authed(s.handleStatus))
	mux.HandleFunc("GET /api/center/clients", s.daemonOnly(s.handleClients))
	mux.HandleFunc("POST /api/center/ticket", s.daemonOnly(s.handleTicket))
	mux.HandleFunc("POST /api/ingest", s.daemonOnly(s.handleIngest))
	mux.HandleFunc("/", s.handleStatic)
	return mux
}

// --- responses ---------------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// decodeBounded reads at most n bytes of JSON. A body over the cap is an error
// rather than a truncated decode.
func decodeBounded(w http.ResponseWriter, r *http.Request, n int64, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, n+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return false
	}
	return true
}

// --- handlers ----------------------------------------------------------------------------------

// handlePing answers without a credential so a local process can tell whether
// the daemon is up. It names the service and nothing else: no version, no pid,
// no repository, because any process on this machine can reach it.
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(r, false) {
		writeError(w, http.StatusForbidden, "unexpected Origin")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"service": "plan-forge-center"})
}

type sessionRequest struct {
	Ticket string `json:"ticket"`
}

// handleSession redeems a launch ticket for a session token.
//
// A tab opened by hand, with no ticket, is refused and parks on a screen
// naming the command to run. That is deliberate: a token this endpoint would
// mint without proof the caller holds the 0600 daemon token is a token any
// page that can guess the port could mint.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(r, true) {
		writeError(w, http.StatusForbidden, "unexpected Origin")
		return
	}
	var req sessionRequest
	if !decodeBounded(w, r, maxSessionBytes, &req) {
		return
	}
	if req.Ticket == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "this tab was not opened by plan-forge",
			"hint":  "run: plan-forge center ensure --open",
		})
		return
	}
	if err := s.tickets.redeem(req.Ticket); err != nil {
		writeError(w, http.StatusForbidden, "this link has already been used or has expired")
		return
	}
	tok, err := s.tokens.issue()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not start a session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": tok})
}

// handleClients tells the local CLI how many tabs are streaming, which is what
// `center ensure` reads to decide whether opening another would be a
// duplicate. It is daemon-authenticated because the answer is operational
// detail, and it carries the Bearer header like every other data GET.
func (s *Server) handleClients(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]int{"active_clients": s.hub.active(), "port": s.port})
}

// NewSecret mints a daemon token. The caller persists it at 0600 so a local
// CLI can prove it is not a browser.
func NewSecret() (string, error) { return cryptoHex(daemonBytes) }

// handleTicket mints a launch ticket for the CLI that is about to open a tab.
func (s *Server) handleTicket(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if r.ContentLength > 0 && !decodeBounded(w, r, maxTicketBytes, &body) {
		return
	}
	t, err := s.tickets.mint()
	if errors.Is(err, ErrTicketsBusy) {
		writeError(w, http.StatusTooManyRequests, "too many unopened tabs are already pending")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not mint a ticket")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"ticket": t,
		"url":    s.origin + "/live#t=" + t,
	})
}

// handleStatus reports what the daemon is doing. It is authenticated because
// it names repositories.
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	docs, err := livestore.LoadAll(s.liveDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the live store")
		return
	}
	repos := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		repos = append(repos, map[string]any{
			"repo_key": d.RepoKey, "repo_name": d.RepoName, "sessions": len(d.Sessions),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"port":           s.port,
		"active_clients": s.hub.active(),
		"uptime_seconds": int(time.Since(s.started).Seconds()),
		"repos":          repos,
	})
}

// handleLive returns the current snapshot of every repository the daemon
// knows about. The repository is identified by its key and base name only, so
// an absolute machine path never reaches the browser.
func (s *Server) handleLive(w http.ResponseWriter, _ *http.Request) {
	snap, err := s.snapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the live store")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// Snapshot is what a tab renders.
type Snapshot struct {
	At    string          `json:"at"`
	Repos []livestore.Doc `json:"repos"`
}

func (s *Server) snapshot() (Snapshot, error) {
	docs, err := livestore.LoadAll(s.liveDir)
	if err != nil {
		return Snapshot{}, err
	}
	public := make([]livestore.Doc, 0, len(docs))
	for _, d := range docs {
		public = append(public, d.Public())
	}
	return Snapshot{At: time.Now().UTC().Format(time.RFC3339), Repos: public}, nil
}

// handleSprint returns the plan view of every repository that has one. A
// repository with no approved plan is omitted rather than reported as an
// error: working without a plan is the normal case this whole surface exists
// to make visible.
func (s *Server) handleSprint(w http.ResponseWriter, r *http.Request) {
	views := []SprintView{}
	if s.cfg.Sprint != nil {
		docs, err := livestore.LoadAll(s.liveDir)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not read the live store")
			return
		}
		for _, d := range docs {
			if d.RepoPath == "" {
				continue
			}
			view, err := s.cfg.Sprint(r.Context(), d.RepoPath)
			if err != nil {
				continue
			}
			view.RepoKey, view.RepoName = d.RepoKey, d.RepoName
			views = append(views, view)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"at":    time.Now().UTC().Format(time.RFC3339),
		"repos": views,
	})
}

// handleEvents is the event stream. It pushes the current snapshot first, so a
// tab that has just connected is never blank while it waits for something to
// happen.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	c, err := s.hub.add()
	if errors.Is(err, ErrHubFull) {
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusTooManyRequests, "too many open tabs")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not open the stream")
		return
	}
	defer s.hub.remove(c)

	if snap, err := s.snapshot(); err == nil {
		if data, err := json.Marshal(snap); err == nil {
			select {
			case c.out <- encodeFrame(0, "snapshot", data):
			default:
			}
		}
	}
	s.hub.stream(w, r, c)
}

// IngestRequest is one telemetry record from a local hook.
type IngestRequest struct {
	Repo       string   `json:"repo"`
	SessionID  string   `json:"session_id"`
	Harness    string   `json:"harness"`
	Developer  string   `json:"developer,omitempty"`
	Branch     string   `json:"branch,omitempty"`
	Mode       string   `json:"mode,omitempty"`
	TaskID     string   `json:"task_id,omitempty"`
	Tool       string   `json:"tool,omitempty"`
	Command    string   `json:"command,omitempty"`
	DirtyFiles []string `json:"dirty_files,omitempty"`
	Kind       string   `json:"kind"`
	Detail     string   `json:"detail,omitempty"`
}

// handleIngest records one report and pushes the new snapshot to every tab.
//
// It answers before the tabs have rendered anything: the caller is a hook with
// a budget measured in milliseconds, and the only thing it needs to know is
// that the record was durable.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	var req IngestRequest
	if !decodeBounded(w, r, maxIngestBytes, &req) {
		return
	}
	if strings.TrimSpace(req.Repo) == "" {
		writeError(w, http.StatusBadRequest, "repo is required")
		return
	}
	store, err := livestore.OpenAt(s.liveDir, req.Repo)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unusable repository path")
		return
	}
	mode := livestore.Mode(req.Mode)
	if mode != livestore.ModePlanned {
		mode = livestore.ModeUnplanned
	}
	if _, err := store.Record(livestore.Record{
		SessionID: req.SessionID, Harness: req.Harness, Developer: req.Developer,
		Branch: req.Branch, Mode: mode, TaskID: req.TaskID, Tool: req.Tool,
		Command: req.Command, DirtyFiles: req.DirtyFiles, Kind: req.Kind, Detail: req.Detail,
	}); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)

	if snap, err := s.snapshot(); err == nil {
		s.hub.broadcast("snapshot", snap)
	}
}

// handleStatic serves the embedded bundle. A path with no extension falls back
// to index.html, which is what makes /live, /sprint and /setup load without
// any server-side routing.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if !s.originOK(r, false) {
		writeError(w, http.StatusForbidden, "unexpected Origin")
		return
	}
	if s.cfg.Assets == nil {
		writeError(w, http.StatusNotFound, "no UI is embedded in this build")
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	data, err := fs.ReadFile(s.cfg.Assets, name)
	if err != nil {
		if path.Ext(name) != "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		name = "index.html"
		if data, err = fs.ReadFile(s.cfg.Assets, name); err != nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}
