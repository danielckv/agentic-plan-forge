//go:build client && !server

package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/danielckv/agentic-plan-forge/internal/storage"
)

// runCheckpointSink keeps the newest deliberation checkpoint in
// docs/plans/.plan/run.json. Drafts cost model calls, so an unfinished run
// survives a crash, a cancelled window and a restart: the next run over the
// same baseline adopts the drafts instead of paying for them again. The
// orchestrator revalidates the checkpoint against the PRD revision, the
// repository graph, the project context and the model slots before adopting
// it, so a stale file is discarded rather than replayed.
//
// The file is removed when a plan is approved, and when a checkpoint stops
// matching the baseline.
type runCheckpointSink struct {
	repo string

	mu     sync.Mutex
	store  *storage.RunStore
	snap   storage.RunSnapshot
	loaded bool
	// failed records a bind error that blocks writing, so a broken plan
	// directory is reported once per call rather than retried in a loop. A
	// damaged run file is not such an error: it is preserved and removed, and
	// the run carries on with a warning.
	failed error
	// notice explains what was moved aside, once, so the window can say why a
	// previous run was not resumed.
	notice string
}

func newRunCheckpointSink(repo string) *runCheckpointSink {
	return &runCheckpointSink{repo: repo}
}

// bind loads the store once, which also reads any unfinished run. A damaged
// record is quarantined by the store: the run continues, the next Put writes a
// fresh file, and Notice explains what happened. Any other load failure is kept
// as failed, because saving over an unreadable plan directory would report
// success for work that was never written.
func (s *runCheckpointSink) bind() (*storage.RunStore, error) {
	if s.loaded {
		return s.store, s.failed
	}
	s.loaded = true
	store := storage.NewRunStore()
	snap, err := store.Load(context.Background(), s.repo)
	if err != nil {
		var quarantined *storage.QuarantineError
		if errors.As(err, &quarantined) {
			s.store, s.snap, s.failed = store, storage.RunSnapshot{}, nil
			s.notice = "the previous unfinished run could not be read and was kept at " +
				quarantined.Path + "; this run starts from a clean record"
			return store, nil
		}
		s.store, s.failed = store, err
		return store, err
	}
	s.store, s.snap = store, snap
	return store, nil
}

// Put replaces the unfinished run on disk.
func (s *runCheckpointSink) Put(runID string, state json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.bind()
	if err != nil {
		return err
	}
	snap := storage.RunSnapshot{RunID: runID, State: state}
	if err := store.Save(context.Background(), snap); err != nil {
		return err
	}
	s.snap = snap
	return nil
}

// Get returns the unfinished run, whatever run id asks for it: a restart
// assigns a new run id, and the caller revalidates identity before adopting.
func (s *runCheckpointSink) Get() (string, json.RawMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.bind(); err != nil {
		return "", nil, false
	}
	if !s.snap.Present() {
		return "", nil, false
	}
	return s.snap.RunID, s.snap.State, true
}

// Drop removes the unfinished run.
func (s *runCheckpointSink) Drop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.bind()
	if err != nil {
		return err
	}
	s.snap = storage.RunSnapshot{}
	return store.Clear(context.Background())
}

// Notice reports a damaged record that was preserved on load, so the window can
// say why a previous run was not resumed. It is empty when nothing was wrong.
func (s *runCheckpointSink) Notice() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notice
}
