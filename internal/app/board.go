//go:build client && !server

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/board"
	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/planstate"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
)

// Board timings (contract C5).
const (
	boardOverlayPoll   = time.Second
	boardEvidenceEvery = 5 * time.Second
	boardScanTimeout   = 10 * time.Second
	boardMaxBody       = 8 << 10
	boardLaunchPath    = "/board"
)

// ErrNoApprovedPlan reports a repository with no manifest, or one whose
// manifest is not approved on this machine.
var ErrNoApprovedPlan = errors.New("no approved plan in this repository (run /plan-forge first)")

// BoardMessageNoPlan is the C4 text printed when the board has no approved plan.
const BoardMessageNoPlan = "board: no approved plan in this repository (run /plan-forge first)"

// BoardMessageHost is the C4 text printed for a host the board does not serve.
const BoardMessageHost = "board supports the claude and omp hosts"

// ValidBoardHost reports whether host may open the board: claude, omp or empty.
func ValidBoardHost(host string) bool {
	switch host {
	case "", "claude", "omp":
		return true
	}
	return false
}

// BoardOptions configures one board window session.
type BoardOptions struct {
	// Repo is the repository root or any directory inside it. Empty means the
	// working directory.
	Repo string
	// Host is the invoking host kind (claude, omp or empty).
	Host   string
	Config config.Config
	// Detached is true for the __board-child: nothing is printed to a terminal
	// and the window-startup wait outlasts the parent's.
	Detached bool
	// LaunchWindow replaces the browser tab; tests only.
	LaunchWindow   func(url string) (windowHandle, error)
	Logger         *slog.Logger
	Stdout, Stderr io.Writer

	// Timing seams; zero selects the contract values.
	Grace         time.Duration
	StartupWait   time.Duration
	OverlayPoll   time.Duration
	EvidenceEvery time.Duration
	ScanTimeout   time.Duration

	// taskBoard replaces planstate.TaskBoard; tests only.
	taskBoard func(ctx context.Context, repo string, opts planstate.CheckOptions) (planstate.TaskBoardResult, error)
}

// --- snapshot (contracts C5 and C11) -----------------------------------------------------------

// BoardTask is one card of the board: the plan evidence merged with the
// coordinator's live column plus the machine-local overlay (priority, notes and
// the latest harness activity). Column and Blocked are the coordinator's, not
// the structural evidence's. Discovered is true for an accepted task
// discovery, which is not part of the approved manifest.
type BoardTask struct {
	planstate.TaskEvidence
	Priority             string         `json:"priority"`
	Notes                []board.Note   `json:"notes"`
	Live                 board.Activity `json:"live"`
	AwaitingVerification bool           `json:"awaiting_verification"`
	Discovered           bool           `json:"discovered"`
}

// BoardCoordinator is the coordinator block of the snapshot: the tasks the
// harness is working on (newest first), the "Up next" queue and the newest
// overlay timestamp that can change either.
type BoardCoordinator struct {
	Active    []string `json:"active"`
	NextUp    []string `json:"next_up"`
	UpdatedAt string   `json:"updated_at"`
}

// BoardConsumer is how far one agent session has consumed the event log.
type BoardConsumer struct {
	ID                string `json:"id"`
	DeliveredRevision uint64 `json:"delivered_revision"`
	SeenAt            string `json:"seen_at"`
}

// BoardSnapshot is the C5 document, extended by C11, served by GET /api/board
// and pushed on the bridge socket. Discoveries holds every discovery, newest
// first, whatever its status.
type BoardSnapshot struct {
	Repo           string                  `json:"repo"`
	Title          string                  `json:"title"`
	ManifestSHA256 string                  `json:"manifest_sha256"`
	Approved       bool                    `json:"approved"`
	Status         string                  `json:"status"`
	Verified       int                     `json:"verified"`
	Total          int                     `json:"total"`
	Revision       uint64                  `json:"revision"`
	Host           string                  `json:"host"`
	EvaluatedAt    string                  `json:"evaluated_at"`
	Drift          []planstate.DriftReason `json:"drift"`
	Tasks          []BoardTask             `json:"tasks"`
	Consumers      []BoardConsumer         `json:"consumers"`
	Discoveries    []board.Discovery       `json:"discoveries"`
	Coordinator    BoardCoordinator        `json:"coordinator"`
}

// buildBoardSnapshot combines plan evidence, the overlay and session facts. A
// result for a different manifest, or for a manifest that is no longer
// approved, is reported as an unapproved, empty board: nothing in it is
// trusted and no overlay edit applies to it.
//
// The columns, the blocked flags, the awaiting-verification flags and the
// coordinator block come from board.Coordinate over the evidence tasks (in the
// order the evidence lists them, the manifest order) and the overlay; the
// accepted task discoveries it returns join the plan's tasks at the end.
func buildBoardSnapshot(repoName, title, sha, host string, ev planstate.TaskBoardResult, evaluatedAt time.Time, ov board.Overlay) BoardSnapshot {
	snap := BoardSnapshot{
		Repo: sanitize.Terminal(repoName), Title: title, ManifestSHA256: sha, Host: host,
		Status: string(planstate.StatusUnknown), Revision: ov.Revision,
		EvaluatedAt: evaluatedAt.UTC().Format(time.RFC3339),
		Drift:       []planstate.DriftReason{}, Tasks: []BoardTask{}, Consumers: []BoardConsumer{},
		Discoveries: []board.Discovery{},
		Coordinator: BoardCoordinator{Active: []string{}, NextUp: []string{}},
	}
	if ev.Status.Present && ev.Status.Approved && ev.Status.ManifestSHA256 == sha {
		snap.Approved = true
		if ev.Status.Title != "" {
			snap.Title = ev.Status.Title
		}
		snap.Status = string(ev.Status.Status)
		snap.Verified, snap.Total = ev.Status.Verified, ev.Status.Total
		if ev.Status.Drift != nil {
			snap.Drift = append(snap.Drift, ev.Status.Drift...)
		}
		order := make([]string, 0, len(ev.Tasks))
		for _, te := range ev.Tasks {
			order = append(order, te.ID)
		}
		co := board.Coordinate(ev.Tasks, ov, order)
		card := func(te planstate.TaskEvidence, discovered bool) BoardTask {
			if te.DependsOn == nil {
				te.DependsOn = []string{}
			}
			if te.Targets == nil {
				te.Targets = []planstate.TargetEvidence{}
			}
			for i := range te.Targets {
				te.Targets[i].Operation = defaultTargetOperation(te.Targets[i].Operation)
			}
			if te.AcceptanceCriteria == nil {
				te.AcceptanceCriteria = []string{}
			}
			if column, ok := co.Columns[te.ID]; ok {
				te.Column = column
			}
			te.Blocked = co.Blocked[te.ID]
			t := BoardTask{
				TaskEvidence: te, Priority: string(ov.Priority(te.ID)), Notes: []board.Note{},
				Live: ov.Activity[te.ID], AwaitingVerification: co.AwaitingVerification[te.ID], Discovered: discovered,
			}
			t.Notes = append(t.Notes, ov.Tasks[te.ID].Notes...)
			return t
		}
		for _, te := range ev.Tasks {
			snap.Tasks = append(snap.Tasks, card(te, false))
		}
		for _, te := range co.ExtraTasks {
			snap.Tasks = append(snap.Tasks, card(te, true))
		}
		snap.Coordinator = BoardCoordinator{
			Active: append([]string{}, co.Active...), NextUp: append([]string{}, co.NextUp...), UpdatedAt: co.UpdatedAt,
		}
		snap.Discoveries = newestFirst(ov.Discoveries)
	}
	ids := make([]string, 0, len(ov.Cursors))
	for id := range ov.Cursors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c := ov.Cursors[id]
		snap.Consumers = append(snap.Consumers, BoardConsumer{ID: id, DeliveredRevision: c.Seq, SeenAt: c.SeenAt})
	}
	return snap
}

// defaultTargetOperation replaces an empty target operation with "modify".
// The web validator rejects "" (it only accepts add|modify|remove|document),
// so a producer that forgets the operation would freeze the board UI; the
// snapshot builder is the last line of defence.
func defaultTargetOperation(op string) string {
	if op == "" {
		return contracts.OpModify
	}
	return op
}

// discoveryNumber returns n for a discovery id D-<n>, and 0 for anything else.
func discoveryNumber(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "D-"))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// newestFirst copies the discoveries ordered by time, newest first; equal
// times put the higher id first. Paths is never nil.
func newestFirst(in []board.Discovery) []board.Discovery {
	out := make([]board.Discovery, len(in))
	copy(out, in)
	for i := range out {
		if out[i].Paths == nil {
			out[i].Paths = []string{}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].At != out[j].At {
			return out[i].At > out[j].At
		}
		return discoveryNumber(out[i].ID) > discoveryNumber(out[j].ID)
	})
	return out
}

// --- single-instance lock ----------------------------------------------------------------------

// boardLock is the pid file of the board window of one repository.
type boardLock struct {
	PID       int       `json:"pid"`
	State     string    `json:"state"` // starting | running
	Repo      string    `json:"repo"`
	UpdatedAt time.Time `json:"updated_at"`
}

const (
	boardStarting = "starting"
	boardRunning  = "running"
)

// boardDir is config.EnsureCache()/board, created 0700.
func boardDir() (string, error) {
	cache, err := config.EnsureCache()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "board")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// boardKey is the hex SHA-256 of the symlink-free repository path.
func boardKey(repo string) string {
	real := repo
	if r, err := filepath.EvalSymlinks(repo); err == nil {
		real = r
	}
	sum := sha256.Sum256([]byte(real))
	return hex.EncodeToString(sum[:])
}

func boardLockPath(repo string) (string, error) {
	dir, err := boardDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, boardKey(repo)+".pid"), nil
}

func readBoardLock(path string) (boardLock, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return boardLock{}, false
	}
	var l boardLock
	if json.Unmarshal(raw, &l) != nil || l.PID <= 0 {
		return boardLock{}, false
	}
	return l, true
}

func writeBoardLock(path string, l boardLock) error {
	l.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return atomicWrite(path, append(raw, '\n'))
}

// acquireBoardLock takes the per-repository lock for this process. A live
// holder other than this process yields (pid, false, nil); a dead or
// unreadable holder is stale and replaced under a flock-protected guard that
// re-reads the lock first, so two contenders never both take over a dead
// holder and a fresh holder is never removed. The pid file is published with a
// hard link, so a reader never sees a partial file and exactly one contender
// wins.
func acquireBoardLock(path, repo string) (int, bool, error) {
	raw, err := json.Marshal(boardLock{PID: os.Getpid(), State: boardStarting, Repo: repo, UpdatedAt: time.Now().UTC()})
	if err != nil {
		return 0, false, err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return 0, false, err
	}
	defer os.Remove(tmp)
	for range 4 {
		err := os.Link(tmp, path)
		if err == nil {
			return os.Getpid(), true, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return 0, false, err
		}
		l, ok := readBoardLock(path)
		if ok && l.PID != os.Getpid() && PIDAlive(l.PID) {
			return l.PID, false, nil
		}
		// Stale: its process is gone.
		if afterStaleCheck != nil {
			afterStaleCheck()
		}
		if err := removeStaleBoardLock(path, l, ok); err != nil {
			return 0, false, err
		}
	}
	return 0, false, errors.New("could not take the board lock")
}

// afterStaleCheck runs between deciding a board lock is stale and taking the
// guard to remove it; tests only.
var afterStaleCheck func()

// removeStaleBoardLock deletes the lock file that acquireBoardLock found stale
// (stale, readable is what it read). It serializes takeovers with the
// <lock>.guard flock and re-reads the lock first: a lock that no longer names
// the same stale pid was replaced meanwhile and is left alone, and the caller
// re-evaluates it.
func removeStaleBoardLock(path string, stale boardLock, readable bool) error {
	unlock, err := lockBoardGuard(path + ".guard")
	if err != nil {
		return err
	}
	defer unlock()
	cur, ok := readBoardLock(path)
	if ok != readable || cur.PID != stale.PID {
		return nil
	}
	if ok && cur.PID != os.Getpid() && PIDAlive(cur.PID) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func releaseBoardLock(path string) {
	if l, ok := readBoardLock(path); ok && l.PID == os.Getpid() {
		_ = os.Remove(path)
	}
}

// BoardHolder returns the pid of the live board window of repo, if any.
func BoardHolder(repo string) (int, bool) {
	path, err := boardLockPath(repo)
	if err != nil {
		return 0, false
	}
	if l, ok := readBoardLock(path); ok && PIDAlive(l.PID) {
		return l.PID, true
	}
	return 0, false
}

// --- preparation -------------------------------------------------------------------------------

// PrepareBoard resolves repo and returns the repository root, the manifest
// digest and the sanitized plan title. It fails with ErrNoApprovedPlan when the
// repository has no approved plan on this machine.
func PrepareBoard(ctx context.Context, repo string) (root, manifestSHA, title string, err error) {
	root, err = ResolveRepo(ctx, repo)
	if err != nil {
		return "", "", "", err
	}
	snap, err := planstate.Inspect(root)
	if err != nil {
		return "", "", "", err
	}
	if snap.ManifestSHA256 == "" || !snap.Approved {
		return "", "", "", ErrNoApprovedPlan
	}
	return root, snap.ManifestSHA256, snap.Title, nil
}

// --- server ------------------------------------------------------------------------------------

type boardServer struct {
	repo, repoName, title, sha, host string
	store                            *board.Store
	bridge                           *relay.Bridge
	log                              *slog.Logger
	scanTimeout                      time.Duration
	// taskBoard replaces planstate.TaskBoard; tests only.
	taskBoard func(ctx context.Context, repo string, opts planstate.CheckOptions) (planstate.TaskBoardResult, error)
	// send replaces bridge.Send; tests only.
	send func(frame []byte) error

	// wg tracks the watch loop and the evaluations it starts; RunBoard waits for it.
	wg sync.WaitGroup

	busy atomic.Bool // single-flight evidence evaluation

	// pushMu serializes push end to end: an older snapshot can never be sent
	// after a newer one.
	pushMu sync.Mutex

	mu          sync.Mutex
	evidence    planstate.TaskBoardResult
	evaluatedAt time.Time
	lastKey     string
}

func (s *boardServer) snapshot() (BoardSnapshot, error) {
	ov, err := s.store.Load()
	if err != nil {
		return BoardSnapshot{}, err
	}
	s.mu.Lock()
	ev, at := s.evidence, s.evaluatedAt
	s.mu.Unlock()
	return buildBoardSnapshot(s.repoName, s.title, s.sha, s.host, ev, at, ov), nil
}

// evaluate re-reads the plan evidence. Concurrent calls collapse: the caller
// that loses the race reports (false, nil) and does nothing.
func (s *boardServer) evaluate(ctx context.Context) (bool, error) {
	if !s.busy.CompareAndSwap(false, true) {
		return false, nil
	}
	defer s.busy.Store(false)
	ectx, cancel := context.WithTimeout(ctx, s.scanTimeout+2*time.Second)
	defer cancel()
	taskBoard := s.taskBoard
	if taskBoard == nil {
		taskBoard = planstate.TaskBoard
	}
	res, err := taskBoard(ectx, s.repo, planstate.CheckOptions{ScanTimeout: s.scanTimeout})
	if err != nil {
		return true, err
	}
	s.mu.Lock()
	s.evidence, s.evaluatedAt = res, time.Now()
	s.mu.Unlock()
	return true, nil
}

// push sends the current snapshot to the window. Unless force is set it sends
// only when the serialized snapshot (ignoring evaluated_at, which moves on
// every evaluation) differs from the last one sent. pushMu is held from the
// snapshot build through the send and the lastKey update, so concurrent callers
// send in the order they built.
func (s *boardServer) push(force bool) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	snap, err := s.snapshot()
	if err != nil {
		s.log.Warn("board: could not read the overlay", "err", err)
		return
	}
	keyed := snap
	keyed.EvaluatedAt = ""
	key, err := json.Marshal(keyed)
	if err != nil {
		return
	}
	s.mu.Lock()
	same := string(key) == s.lastKey
	s.mu.Unlock()
	if same && !force {
		return
	}
	frame, err := json.Marshal(struct {
		Type     string        `json:"type"`
		Snapshot BoardSnapshot `json:"snapshot"`
	}{"board.snapshot", snap})
	if err != nil {
		return
	}
	send := s.send
	if send == nil {
		send = s.bridge.Send
	}
	if err := send(frame); err != nil {
		return // no window yet; the connect callback sends the full state
	}
	s.mu.Lock()
	s.lastKey = string(key)
	s.mu.Unlock()
}

func (s *boardServer) routes() {
	s.bridge.Handle("GET /api/board", s.handleGet)
	s.bridge.Handle("POST /api/board/task", s.handleTask)
	s.bridge.Handle("POST /api/board/discovery", s.handleDiscovery)
}

func (s *boardServer) handleGet(w http.ResponseWriter, _ *http.Request) {
	snap, err := s.snapshot()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "the board overlay could not be read")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

type boardTaskRequest struct {
	ID         string  `json:"id"`
	Priority   *string `json:"priority"`
	Note       *string `json:"note"`
	DeleteNote *string `json:"delete_note"`
}

func (s *boardServer) handleTask(w http.ResponseWriter, r *http.Request) {
	var req boardTaskRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, boardMaxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		writeAPIError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	actions := 0
	for _, set := range []bool{req.Priority != nil, req.Note != nil, req.DeleteNote != nil} {
		if set {
			actions++
		}
	}
	if actions != 1 {
		writeAPIError(w, http.StatusBadRequest, "exactly one of priority, note and delete_note is required")
		return
	}
	if !board.ValidTaskID(req.ID) {
		writeAPIError(w, http.StatusBadRequest, board.ErrTaskID.Error())
		return
	}
	var prio board.Priority
	if req.Priority != nil {
		p, ok := board.ParsePriority(*req.Priority)
		if !ok {
			writeAPIError(w, http.StatusBadRequest, board.ErrPriority.Error())
			return
		}
		prio = p
	}
	// The board only edits the plan it opened with, and only while that plan
	// stays approved on this machine.
	cur, err := planstate.Inspect(s.repo)
	if err != nil || cur.ManifestSHA256 != s.sha || !cur.Approved {
		writeAPIError(w, http.StatusConflict, "the plan changed or is no longer approved; reopen the board")
		return
	}
	known := false
	for _, t := range cur.Manifest.TaskDAG {
		if t.ID == req.ID {
			known = true
			break
		}
	}
	if !known {
		// An accepted task discovery is a board task too.
		if ov, err := s.store.Load(); err == nil {
			for _, d := range ov.Discoveries {
				if d.ID == req.ID && d.Kind == board.DiscoveryTask && d.Status == board.DiscoveryAccepted {
					known = true
					break
				}
			}
		}
	}
	if !known {
		writeAPIError(w, http.StatusNotFound, "unknown task")
		return
	}
	switch {
	case req.Priority != nil:
		_, err = s.store.SetPriority(req.ID, prio)
	case req.Note != nil:
		_, err = s.store.AddNote(req.ID, *req.Note)
	default:
		_, err = s.store.DeleteNote(req.ID, *req.DeleteNote)
	}
	if err != nil {
		switch {
		case req.DeleteNote != nil && (errors.Is(err, board.ErrUnknownTask) || errors.Is(err, board.ErrNoteNotFound)):
			// The task is in the manifest (checked above); the overlay holds no such note.
			writeAPIError(w, http.StatusNotFound, "unknown note")
		case errors.Is(err, board.ErrPriority), errors.Is(err, board.ErrTaskID), errors.Is(err, board.ErrTooLarge),
			errors.Is(err, board.ErrNoteEmpty), errors.Is(err, board.ErrNoteTooLong), errors.Is(err, board.ErrNoteLimit),
			errors.Is(err, board.ErrNoteNotFound), errors.Is(err, board.ErrUnknownTask):
			writeAPIError(w, http.StatusBadRequest, sanitize.Terminal(err.Error()))
		default:
			s.log.Warn("board: edit failed", "err", err)
			writeAPIError(w, http.StatusInternalServerError, "the board overlay could not be written")
		}
		return
	}
	snap, err := s.snapshot()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "the board overlay could not be read")
		return
	}
	writeJSON(w, http.StatusOK, snap)
	s.push(false)
}

// discoveryIDRE is the id of a discovery: D-<n>, n >= 1, no leading zero.
var discoveryIDRE = regexp.MustCompile(`^D-[1-9][0-9]{0,18}$`)

type boardDiscoveryRequest struct {
	ID       string  `json:"id"`
	Action   string  `json:"action"`
	Priority *string `json:"priority"`
}

// handleDiscovery accepts or dismisses a harness discovery on the user's
// behalf. It answers 400 for a malformed request, 409 when the approved plan
// changed since the board opened, 404 for an id the overlay does not hold and
// 200 with the new snapshot otherwise.
func (s *boardServer) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	var req boardDiscoveryRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, boardMaxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() {
		writeAPIError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !discoveryIDRE.MatchString(req.ID) {
		writeAPIError(w, http.StatusBadRequest, "id must be a discovery id such as D-1")
		return
	}
	var accept bool
	prio := board.PriorityNormal
	switch req.Action {
	case "accept":
		accept = true
		if req.Priority != nil && *req.Priority != "" {
			p, ok := board.ParsePriority(*req.Priority)
			if !ok {
				writeAPIError(w, http.StatusBadRequest, board.ErrPriority.Error())
				return
			}
			prio = p
		}
	case "dismiss":
		if req.Priority != nil {
			writeAPIError(w, http.StatusBadRequest, "priority only applies to accept")
			return
		}
	default:
		writeAPIError(w, http.StatusBadRequest, "action must be accept or dismiss")
		return
	}
	cur, err := planstate.Inspect(s.repo)
	if err != nil || cur.ManifestSHA256 != s.sha || !cur.Approved {
		writeAPIError(w, http.StatusConflict, "the plan changed or is no longer approved; reopen the board")
		return
	}
	if _, err := s.store.ResolveDiscovery(req.ID, accept, prio); err != nil {
		switch {
		case errors.Is(err, board.ErrUnknownDiscovery):
			writeAPIError(w, http.StatusNotFound, "unknown discovery")
		case errors.Is(err, board.ErrPriority):
			writeAPIError(w, http.StatusBadRequest, sanitize.Terminal(err.Error()))
		default:
			s.log.Warn("board: discovery resolution failed", "err", err)
			writeAPIError(w, http.StatusInternalServerError, "the board overlay could not be written")
		}
		return
	}
	snap, err := s.snapshot()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "the board overlay could not be read")
		return
	}
	writeJSON(w, http.StatusOK, snap)
	s.push(false)
}

// watch pushes overlay changes (polled) and re-evaluated evidence until ctx ends.
func (s *boardServer) watch(ctx context.Context, overlayPoll, evidenceEvery time.Duration) {
	poll := time.NewTicker(overlayPoll)
	defer poll.Stop()
	evid := time.NewTicker(evidenceEvery)
	defer evid.Stop()
	sig := s.overlaySignature()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			if now := s.overlaySignature(); now != sig {
				sig = now
				s.push(false)
			}
		case <-evid.C:
			if s.busy.Load() {
				continue
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				ran, err := s.evaluate(ctx)
				switch {
				case err != nil && ctx.Err() == nil:
					s.log.Warn("board: evidence evaluation failed", "err", err)
				case ran && err == nil:
					s.push(false)
				}
			}()
		}
	}
}

func (s *boardServer) overlaySignature() string {
	fi, err := os.Stat(s.store.Path())
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", fi.ModTime().UnixNano(), fi.Size())
}

// --- RunBoard ----------------------------------------------------------------------------------

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// RunBoard opens a browser tab on the Board view of the repository's
// approved plan and serves it until the window closes. Closing the window is a
// normal end: exit code 0.
func RunBoard(ctx context.Context, opts BoardOptions) Result {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	if !ValidBoardHost(opts.Host) {
		return Result{Phase: PhaseFailed, ExitCode: ExitUsage, Message: BoardMessageHost}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	repo, sha, title, err := PrepareBoard(ctx, opts.Repo)
	if errors.Is(err, ErrNoApprovedPlan) {
		return resultFor(PhasePrereq, BoardMessageNoPlan, nil)
	}
	if err != nil {
		return classify(err)
	}
	lockPath, err := boardLockPath(repo)
	if err != nil {
		return classify(err)
	}
	pid, held, err := acquireBoardLock(lockPath, repo)
	if err != nil {
		return classify(err)
	}
	if !held {
		return Result{Phase: PhaseRunning, ExitCode: ExitOK, Message: fmt.Sprintf("board already open (pid %d)", pid)}
	}
	defer releaseBoardLock(lockPath)

	store, err := board.Open(sha)
	if err != nil {
		return classify(err)
	}
	srv := &boardServer{
		repo: repo, repoName: filepath.Base(repo), title: title, sha: sha, host: opts.Host,
		store: store, log: opts.Logger, scanTimeout: orDefault(opts.ScanTimeout, boardScanTimeout), taskBoard: opts.taskBoard,
	}
	// The first evidence is read before the window opens so GET /api/board is
	// complete from the first request. A scan that cannot finish (unknown
	// status, scan timeout) must not stop the board: the window opens on an
	// UNKNOWN, empty snapshot and the evidence loop retries.
	if _, err := srv.evaluate(ctx); err != nil {
		switch {
		case ctx.Err() != nil:
			return Result{Phase: PhaseCancelled, ExitCode: ExitCancelled, Message: "the board was interrupted"}
		case errors.Is(err, planstate.ErrStatusUnknown), errors.Is(err, context.DeadlineExceeded):
			opts.Logger.Warn("board: the first evidence evaluation did not finish; opening with unknown status and retrying", "err", err)
		default:
			return classify(err)
		}
	}

	bridge, err := relay.NewBridge(relay.BridgeConfig{Assets: UIAssets(), Logger: opts.Logger, LaunchPath: boardLaunchPath})
	if err != nil {
		return classify(err)
	}
	srv.bridge = bridge
	defer bridge.Close()
	srv.routes()
	go func() { _ = bridge.Serve(ctx) }()

	win, outcome, err := startWindow(ctx, windowParams{
		Bridge: bridge, Launch: opts.LaunchWindow, BrowserPath: opts.Config.BrowserPath, Logger: opts.Logger,
		Detached: opts.Detached, Grace: opts.Grace, StartupWait: opts.StartupWait,
		OnRunning: func() {
			if err := writeBoardLock(lockPath, boardLock{PID: os.Getpid(), State: boardRunning, Repo: repo}); err != nil {
				opts.Logger.Warn("board: could not record the running state", "err", err)
			}
		},
		OnConnect: func() { srv.push(true) },
	})
	if err != nil {
		return classify(err)
	}
	defer win.Terminate(0)
	if !opts.Detached && opts.Stderr != nil {
		fmt.Fprintln(opts.Stderr, "Plan-Forge board opened. Close the window to exit.")
	}
	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()
		srv.watch(ctx, orDefault(opts.OverlayPoll, boardOverlayPoll), orDefault(opts.EvidenceEvery, boardEvidenceEvery))
	}()
	// Runs first on every return: stop the watch loop and wait for the
	// evaluations in flight (bounded by the scan timeout) before the window,
	// the bridge and the lock are torn down.
	defer func() {
		cancel()
		srv.wg.Wait()
	}()

	select {
	case r := <-outcome:
		if r.Phase == PhaseCancelled {
			return Result{Phase: PhaseCancelled, ExitCode: ExitOK}
		}
		return r
	case err := <-bridge.Fatal():
		return resultFor(PhaseFailed, notice(err), nil)
	case <-ctx.Done():
		return Result{Phase: PhaseCancelled, ExitCode: ExitCancelled, Message: "the board was interrupted"}
	}
}

// --- detached start ----------------------------------------------------------------------------

// BoardDetachResult is the parent's outcome when starting a detached board.
type BoardDetachResult struct {
	PID         int
	AlreadyOpen bool
	ExitCode    int
	Message     string
}

// StartBoardDetached spawns `__board-child` in its own session, waits up to 30
// seconds for its window to connect and reports the pid. A live board of the
// same repository is reported as AlreadyOpen and nothing is spawned.
func StartBoardDetached(ctx context.Context, opts BoardOptions) BoardDetachResult {
	if !ValidBoardHost(opts.Host) {
		return BoardDetachResult{ExitCode: ExitUsage, Message: BoardMessageHost}
	}
	repo, _, _, err := PrepareBoard(ctx, opts.Repo)
	if errors.Is(err, ErrNoApprovedPlan) {
		return BoardDetachResult{ExitCode: ExitPrerequisite, Message: BoardMessageNoPlan}
	}
	if err != nil {
		res := classify(err)
		return BoardDetachResult{ExitCode: res.ExitCode, Message: res.Message}
	}
	if pid, ok := BoardHolder(repo); ok {
		return BoardDetachResult{PID: pid, AlreadyOpen: true}
	}
	lockPath, err := boardLockPath(repo)
	if err != nil {
		return BoardDetachResult{ExitCode: ExitFailure, Message: notice(err)}
	}
	exe, err := os.Executable()
	if err != nil {
		return BoardDetachResult{ExitCode: ExitFailure, Message: notice(err)}
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	logFile, err := os.OpenFile(strings.TrimSuffix(lockPath, ".pid")+".log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return BoardDetachResult{ExitCode: ExitFailure, Message: notice(err)}
	}
	args := []string{"__board-child", "--repo", repo}
	if opts.Host != "" {
		args = append(args, "--host", opts.Host)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = repo
	cmd.Stdout, cmd.Stderr = logFile, logFile
	detachAttrs(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return BoardDetachResult{ExitCode: ExitFailure, Message: notice(err)}
	}
	_ = logFile.Close()
	child := cmd.Process.Pid
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }() // reaps the child so PID checks stay honest

	bound := opts.StartupWait
	if bound <= 0 {
		bound = detachedStartupBound
	}
	deadline := time.Now().Add(bound)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if l, ok := readBoardLock(lockPath); ok {
			switch {
			case l.PID == child && l.State == boardRunning:
				return BoardDetachResult{PID: child}
			case l.PID != child && PIDAlive(l.PID):
				return BoardDetachResult{PID: l.PID, AlreadyOpen: true}
			}
		}
		select {
		case <-exited:
			if l, ok := readBoardLock(lockPath); ok && l.PID != child && PIDAlive(l.PID) {
				return BoardDetachResult{PID: l.PID, AlreadyOpen: true}
			}
			return BoardDetachResult{ExitCode: ExitFailure, Message: "the board exited before its window connected"}
		case <-ctx.Done():
			killGroup(cmd)
			return BoardDetachResult{ExitCode: ExitCancelled, Message: "interrupted while waiting for the window"}
		case <-ticker.C:
		}
		if time.Now().After(deadline) {
			killGroup(cmd)
			return BoardDetachResult{ExitCode: ExitFailure, Message: "startup_timeout: the Plan-Forge board window did not connect within 30s"}
		}
	}
}

// BoardTitleIndex maps task ids to their manifest titles and lists the ids in
// manifest order; `board pull` renders deltas from it.
func BoardTitleIndex(m contracts.PlanManifest) (titles map[string]string, order []string) {
	titles = make(map[string]string, len(m.TaskDAG))
	order = make([]string, 0, len(m.TaskDAG))
	for _, t := range m.TaskDAG {
		titles[t.ID] = t.Title
		order = append(order, t.ID)
	}
	return titles, order
}

// BoardTitles is BoardTitleIndex plus the titles of the overlay's discoveries,
// keyed by D-<n>, so a delta can name a discovery. Manifest ids win over a
// discovery that happens to share one. order lists the manifest ids only.
func BoardTitles(m contracts.PlanManifest, ov board.Overlay) (titles map[string]string, order []string) {
	titles, order = BoardTitleIndex(m)
	for _, d := range ov.Discoveries {
		if _, taken := titles[d.ID]; !taken {
			titles[d.ID] = d.Title
		}
	}
	return titles, order
}
