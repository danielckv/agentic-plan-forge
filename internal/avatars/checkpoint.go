//go:build client && !server

package avatars

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// maxCheckpointRuns bounds how many runs a store remembers.
const maxCheckpointRuns = 16

// draftRec is one validated draft, kept as bytes so a checkpoint is immutable.
type draftRec struct {
	Raw json.RawMessage
	// Cycle is the cycle the draft was produced in.
	Cycle int
	// Rev is the draft revision published for the avatar.
	Rev uint64
	// Stale marks a draft invalidated by a critique, a contract change or a
	// decision. A stale draft is re-run; it is never reused.
	Stale bool
}

func (d draftRec) present() bool { return len(d.Raw) > 0 }

// runState is the resumable state of one run. It is saved after every
// validated draft, critique and decision, so a cancelled or failed run can be
// retried without redoing completed (and billed) work.
type runState struct {
	// Identity: resume revalidates all of it and re-runs on any mismatch.
	PRDRevision uint64
	GraphHash   string
	ContextHash string
	PMHash      string
	Main        string
	Judge       string

	// Cycle is the cycle in progress, Planned the last cycle whose revision plan
	// was applied and CyclesDone the last cycle whose critic barrier finished.
	Cycle      int
	Planned    int
	CyclesDone int
	// Paused reports that the cycle cap was reached with blocking issues. A
	// paused run never starts another automatic cycle.
	Paused bool

	PM    draftRec
	Arch  draftRec
	Specs map[string]draftRec

	// Critiques is the authoritative set, valid only for CriticHash in
	// CriticCycle.
	Critiques   []contracts.Critique
	CriticHash  string
	CriticCycle int

	Decisions []contracts.Decision
	Pending   []contracts.Decision
	// DecisionContracts maps an interface decision to its contract and Applied
	// records the ones already carried out.
	DecisionContracts map[string]string
	Applied           map[string]bool
	Seq               int

	Revs map[string]uint64
}

func newRunState() *runState {
	return &runState{
		Specs:             map[string]draftRec{},
		DecisionContracts: map[string]string{},
		Applied:           map[string]bool{},
		Revs:              map[string]uint64{},
	}
}

func (s *runState) clone() *runState {
	c := *s
	c.Specs = make(map[string]draftRec, len(s.Specs))
	for k, v := range s.Specs {
		c.Specs[k] = v
	}
	c.DecisionContracts = make(map[string]string, len(s.DecisionContracts))
	for k, v := range s.DecisionContracts {
		c.DecisionContracts[k] = v
	}
	c.Applied = make(map[string]bool, len(s.Applied))
	for k, v := range s.Applied {
		c.Applied[k] = v
	}
	c.Revs = make(map[string]uint64, len(s.Revs))
	for k, v := range s.Revs {
		c.Revs[k] = v
	}
	c.Critiques = append([]contracts.Critique(nil), s.Critiques...)
	c.Decisions = append([]contracts.Decision(nil), s.Decisions...)
	c.Pending = append([]contracts.Decision(nil), s.Pending...)
	return &c
}

// CheckpointStore keeps the resumable state of recent runs in memory. State
// never leaves the process: it holds model drafts, which are retained only for
// the life of the session.
type CheckpointStore struct {
	mu    sync.Mutex
	runs  map[string]*runState
	order []string
}

// NewCheckpointStore returns an empty store.
func NewCheckpointStore() *CheckpointStore {
	return &CheckpointStore{runs: map[string]*runState{}}
}

// defaultCheckpoints serves runs whose RunInput names no store.
var defaultCheckpoints = NewCheckpointStore()

func (c *CheckpointStore) load(runID string) *runState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.runs[runID]; ok {
		return s.clone()
	}
	return nil
}

func (c *CheckpointStore) save(runID string, s *runState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.runs[runID]; !ok {
		c.order = append(c.order, runID)
		for len(c.order) > maxCheckpointRuns {
			delete(c.runs, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.runs[runID] = s.clone()
}

// Forget drops the checkpoint of a run.
func (c *CheckpointStore) Forget(runID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.runs, runID)
	for i, id := range c.order {
		if id == runID {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

// Cycle reports the cycle a run reached, or zero when it has no checkpoint.
func (c *CheckpointStore) Cycle(runID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.runs[runID]; ok {
		return s.Cycle
	}
	return 0
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// compatible reports whether a checkpoint was made for exactly this PRD
// revision, product content, graph and model slots.
func (s *runState) compatible(in RunInput, pmHash string) bool {
	return s.PRDRevision == in.PRD.Revision && s.GraphHash == in.GraphHash && s.ContextHash == in.ContextHash && s.PMHash == pmHash &&
		s.Main == in.Models.Main && s.Judge == in.Models.Judge
}

// setHash identifies the complete current draft set plus the decisions that
// shaped it. A critique set is certifying only for the hash it was produced
// for: any later draft or decision changes the hash and demands a new barrier.
func (s *runState) setHash() string {
	h := sha256.New()
	h.Write(s.Arch.Raw)
	ids := make([]string, 0, len(s.Specs))
	for id := range s.Specs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		h.Write([]byte{0})
		h.Write([]byte(id))
		h.Write([]byte{0})
		h.Write(s.Specs[id].Raw)
	}
	decs := append([]contracts.Decision(nil), s.Decisions...)
	sort.Slice(decs, func(i, j int) bool { return decs[i].ID < decs[j].ID })
	for _, d := range decs {
		h.Write([]byte{1})
		h.Write([]byte(d.ID + "=" + d.ResolvedOptionID))
	}
	return hex.EncodeToString(h.Sum(nil))
}
