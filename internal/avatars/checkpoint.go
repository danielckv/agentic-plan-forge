//go:build client && !server

package avatars

import (
	"bytes"
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

	// The architect stage answers in parts (see architect.go). A part that
	// finished is kept, so a retry re-runs only the part that failed.
	ArchDesign   draftRec
	ArchDiagrams draftRec
	ArchDocs     []docBatchRec

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
	c.ArchDocs = append([]docBatchRec(nil), s.ArchDocs...)
	c.Critiques = append([]contracts.Critique(nil), s.Critiques...)
	c.Decisions = append([]contracts.Decision(nil), s.Decisions...)
	c.Pending = append([]contracts.Decision(nil), s.Pending...)
	return &c
}

// CheckpointSink persists the newest checkpoint beyond the process, so drafts
// that were already paid for survive a crash or a restart. One repository
// keeps one unfinished run: Put replaces it, Get returns it whatever run id
// asks (the orchestrator revalidates identity before adopting it) and Drop
// removes it. A sink that fails is logged by its owner and never fails a run.
type CheckpointSink interface {
	Put(runID string, state json.RawMessage) error
	Get() (runID string, state json.RawMessage, ok bool)
	Drop() error
}

// CheckpointStore keeps the resumable state of recent runs in memory, and,
// when it has a sink, the newest one on disk as well. State holds model
// drafts: in memory it lives for the session, on disk until the plan is
// approved or the baseline changes.
type CheckpointStore struct {
	mu    sync.Mutex
	runs  map[string]*runState
	order []string
	sink  CheckpointSink
	// onSinkError reports a sink failure. Nil ignores it: a checkpoint that
	// cannot be written costs a re-run, never a failed stage.
	onSinkError func(error)
}

// NewCheckpointStore returns an empty store.
func NewCheckpointStore() *CheckpointStore {
	return &CheckpointStore{runs: map[string]*runState{}}
}

// Persist attaches a sink and adopts the run it holds, if any. It is called
// once, before a run starts.
func (c *CheckpointStore) Persist(sink CheckpointSink, onError func(error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sink, c.onSinkError = sink, onError
}

// sinkFailed reports a sink error without failing the run.
func (c *CheckpointStore) sinkFailed(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	fn := c.onSinkError
	c.mu.Unlock()
	if fn != nil {
		fn(err)
	}
}

// defaultCheckpoints serves runs whose RunInput names no store.
var defaultCheckpoints = NewCheckpointStore()

// load returns the checkpoint for a run: the in-memory one first, then the
// persisted one. The persisted checkpoint is offered to any run id, because a
// restart assigns a new one; the caller revalidates it against the baseline,
// the graph, the project context and the model slots before adopting it.
// It also reports whether the checkpoint came from the sink, which means this
// process has not published those drafts yet.
func (c *CheckpointStore) load(runID string) (*runState, bool) {
	c.mu.Lock()
	if s, ok := c.runs[runID]; ok {
		defer c.mu.Unlock()
		return s.clone(), false
	}
	sink := c.sink
	c.mu.Unlock()
	if sink == nil {
		return nil, false
	}
	_, raw, ok := sink.Get()
	if !ok || len(raw) == 0 {
		return nil, false
	}
	st := newRunState()
	if err := json.Unmarshal(raw, st); err != nil {
		c.sinkFailed(err)
		c.sinkFailed(sink.Drop())
		return nil, false
	}
	st.afterDecode()
	return st, true
}

// afterDecode repairs what a JSON round trip changes. A draft that was never
// made marshals as null and comes back as the four bytes "null", which would
// look like a present draft and skip the stage that must still run.
func (s *runState) afterDecode() {
	s.PM.Raw = definedRaw(s.PM.Raw)
	s.Arch.Raw = definedRaw(s.Arch.Raw)
	s.ArchDesign.Raw = definedRaw(s.ArchDesign.Raw)
	s.ArchDiagrams.Raw = definedRaw(s.ArchDiagrams.Raw)
	docs := s.ArchDocs[:0]
	for _, rec := range s.ArchDocs {
		rec.Draft.Raw = definedRaw(rec.Draft.Raw)
		if !rec.Draft.present() {
			continue
		}
		docs = append(docs, rec)
	}
	s.ArchDocs = docs
	if s.Specs == nil {
		s.Specs = map[string]draftRec{}
	}
	for ws, rec := range s.Specs {
		rec.Raw = definedRaw(rec.Raw)
		if !rec.present() {
			delete(s.Specs, ws)
			continue
		}
		s.Specs[ws] = rec
	}
	if s.DecisionContracts == nil {
		s.DecisionContracts = map[string]string{}
	}
	if s.Applied == nil {
		s.Applied = map[string]bool{}
	}
	if s.Revs == nil {
		s.Revs = map[string]uint64{}
	}
}

// definedRaw maps a JSON null, and anything blank, to no draft at all.
func definedRaw(r json.RawMessage) json.RawMessage {
	if t := bytes.TrimSpace(r); len(t) == 0 || string(t) == "null" {
		return nil
	}
	return r
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
	sink := c.sink
	if sink == nil {
		return
	}
	raw, err := json.Marshal(s)
	if err != nil {
		c.onSinkError0(err)
		return
	}
	if err := sink.Put(runID, raw); err != nil {
		c.onSinkError0(err)
	}
}

// onSinkError0 reports an error while the lock is already held.
func (c *CheckpointStore) onSinkError0(err error) {
	if c.onSinkError != nil {
		c.onSinkError(err)
	}
}

// Forget drops the checkpoint of a run, in memory and on disk. It is called
// when a checkpoint no longer matches the baseline, and when an approved plan
// makes the run history irrelevant.
func (c *CheckpointStore) Forget(runID string) {
	c.mu.Lock()
	delete(c.runs, runID)
	for i, id := range c.order {
		if id == runID {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	sink := c.sink
	c.mu.Unlock()
	if sink != nil {
		c.sinkFailed(sink.Drop())
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
