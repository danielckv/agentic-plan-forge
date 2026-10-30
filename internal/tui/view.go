//go:build client && !server

// Package tui renders a Plan-Forge session in a terminal.
//
// It is a frontend, not a second authority: it speaks the same loopback
// WebSocket handshake as the browser (subprotocol plan-forge.v1, an auth frame
// first, then WSEnvelope frames) and sends the same authorized commands. No new
// protocol, no new endpoint, and no browser requirement: a machine with no
// display can run a whole session from a terminal.
//
// The split is deliberate. `view` is pure: it folds envelopes into a Snapshot
// and renders that snapshot to a string, so every layout, filter and empty state
// is testable without a terminal. `Run` owns the socket, the keystrokes and the
// screen, and does nothing a test would want to reach into.
package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// Tab names one pane of the terminal window. The plan's five views; a key
// selects each, so the terminal is a state-equivalent list of what the browser
// draws, never an attempted 3D render.
type Tab string

const (
	TabBrief     Tab = "brief"
	TabAgents    Tab = "agents"
	TabPlan      Tab = "plan"
	TabQuestions Tab = "questions"
	TabSettings  Tab = "settings"
)

// Tabs in display order: the order a session moves through them, so Tab key
// order matches the work.
var Tabs = []Tab{TabBrief, TabAgents, TabPlan, TabQuestions, TabSettings}

// AgentRow is one avatar's current state, as the terminal shows it.
type AgentRow struct {
	ID      string
	Name    string
	Status  string
	Cycle   int
	Host    string
	Model   string
	Summary string
	// Kind is permanent, specialist or temporary, so a reader can tell a
	// run-scoped gap agent from the fixed roster.
	Kind string
}

// QuestionRow is one decision or ambiguity waiting for the owner.
type QuestionRow struct {
	ID   string
	Kind string // decision | ambiguity
	// InputRevision is the document revision the card opened at. The authority
	// rejects an answer that does not echo it (STALE_RUN).
	InputRevision uint64
	Prompt        string
	Options       []string
	// Recommended names the recommended option id, empty when none.
	Recommended string
}

// Snapshot is everything the terminal renders: the whole window state, derived
// from frames and never from a model's prose. A renderer that needs a value not
// here is showing something the authority never sent.
type Snapshot struct {
	Phase     relay.Phase
	Role      relay.Role
	Members   []string
	Cycle     int
	DocWords  int
	DocTitle  string
	Agents    []AgentRow
	Plan      []string
	Questions []QuestionRow
	Findings  []string
	// BaselineRev is the document revision the run was frozen on; APPROVE must
	// echo it or the authority answers STALE_BASELINE.
	BaselineRev uint64
	// SynthesisRev and SynthesisHash identify the locked spec. APPROVE must
	// echo both; a mismatch is STALE_SYNTHESIS.
	SynthesisRev  uint64
	SynthesisHash string
	// MemberID names this window's member, which the authority sends.
	MemberID string
	// Readiness is the compiled verdict when the spec has been locked.
	Readiness string
	// Artifacts are the files a finished run wrote, relative to the repo.
	Artifacts []string
	// Banner is a one-line status the window shows above the panes: a failed
	// stage, a lost lease, a reconnect. Empty when nothing needs saying.
	Banner string
	// Err is the last protocol or authority error, cleared by a new snapshot.
	Err string
	// SessionID is the room this window joined. The snapshot payload carries no
	// session id, so Apply copies it from the envelope: a command that names
	// none is rejected by the authority as an invalid frame.
	SessionID string
	// RunID is the run the commands act on. A snapshot carries it when a run
	// has started; the commands that need it say so rather than sending empty.
	RunID string
}

// runID is the run commands address, empty before a run starts.
func (s Snapshot) runID() string { return s.RunID }

// sessionID is the session a command belongs to; the authority rejects a frame
// that names none.
func (s Snapshot) sessionID() string { return s.SessionID }

// Apply folds one envelope into the snapshot. It returns the same value it was
// given, so a caller can chain: `snap = snap.Apply(env)`.
//
// Unknown event types are ignored on purpose: the protocol grows, and a terminal
// that refused to render because it met a frame it did not know would be worse
// than one that shows what it understands.
func (s Snapshot) Apply(env relay.WSEnvelope) Snapshot {
	// The session id lives on the envelope, never in a payload. Carrying it
	// forward here is what makes every later command addressable.
	if env.SessionID != "" {
		s.SessionID = env.SessionID
	}
	switch env.EventType {
	case relay.EventHello, relay.EventStateSnapshot:
		if snap, ok := decodeSnapshot(env.Payload); ok {
			// A snapshot is authoritative: it replaces, never merges. The
			// envelope's session id survives because it is not in the payload.
			snap.SessionID = s.SessionID
			snap.Banner = s.Banner
			snap.Err = ""
			return snap
		}
	case relay.EventPageTransition:
		var p struct {
			From relay.Phase `json:"from"`
			To   relay.Phase `json:"to"`
		}
		if json.Unmarshal(env.Payload, &p) == nil && p.To != "" {
			s.Phase = p.To
		}
	case relay.EventCycleStart:
		var p struct {
			Cycle int `json:"cycle"`
		}
		if json.Unmarshal(env.Payload, &p) == nil && p.Cycle > 0 {
			s.Cycle = p.Cycle
		}
	case relay.EventAvatarThought:
		var p relay.AvatarThoughtPayload
		if json.Unmarshal(env.Payload, &p) == nil && p.AvatarID != "" {
			s = s.setAgent(AgentRow{
				ID: p.AvatarID, Name: agentName(p.AvatarID), Status: p.Status,
				Cycle: p.Cycle, Host: p.Host, Model: p.Model, Summary: p.PublicSummary,
				Kind: agentKind(p.AvatarID),
			})
			if p.Status == "Failed" {
				s.Banner = fmt.Sprintf("%s failed: %s", agentName(p.AvatarID), p.PublicSummary)
			}
		}
	case relay.EventAvatarDraft:
		var p relay.AvatarDraftPayload
		if json.Unmarshal(env.Payload, &p) == nil && p.AvatarID != "" {
			// The synthesizer's hash is what APPROVE must echo back.
			if p.AvatarID == "synthesizer" && p.ContentHash != "" {
				s.SynthesisHash = p.ContentHash
				if p.DraftRevision > s.SynthesisRev {
					s.SynthesisRev = p.DraftRevision
				}
			}
			s = s.setAgent(AgentRow{ID: p.AvatarID, Name: agentName(p.AvatarID), Status: "Synced", Kind: agentKind(p.AvatarID)})
		}
	case relay.EventAvatarCritique:
		var p struct {
			Issues []struct {
				Severity       string   `json:"severity"`
				TargetIDs      []string `json:"target_ids"`
				RequiredChange string   `json:"required_change"`
			} `json:"issues"`
		}
		if json.Unmarshal(env.Payload, &p) == nil {
			s.Findings = s.Findings[:0]
			for _, is := range p.Issues {
				s.Findings = append(s.Findings, fmt.Sprintf("%s %s: %s",
					is.Severity, strings.Join(is.TargetIDs, ","), is.RequiredChange))
			}
		}
	case relay.EventDecisionPrompt:
		var p relay.DecisionPromptPayload
		if json.Unmarshal(env.Payload, &p) == nil && p.DecisionID != "" {
			row := QuestionRow{
				ID: p.DecisionID, Kind: "decision", InputRevision: p.InputRevision,
				Prompt: p.Question, Recommended: p.RecommendedOptionID,
			}
			for _, o := range p.Options {
				label := o.Label
				if label == "" {
					label = o.ID
				}
				row.Options = append(row.Options, o.ID+": "+label)
			}
			s = s.upsertQuestion(row)
		}

	case relay.EventDecisionResolve, relay.EventAlreadyResolved:
		var p struct {
			ID string `json:"decision_id"`
		}
		if json.Unmarshal(env.Payload, &p) == nil && p.ID != "" {
			s = s.removeQuestion(p.ID)
		}
	case relay.EventPlanApproved:
		s.Phase = relay.PhaseApproved
		s.Banner = "Approved. The plan is frozen and written to docs/plans/.plan/."
	case relay.EventArtifactSaved, relay.EventArtifactSynced:
		var p struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		if json.Unmarshal(env.Payload, &p) == nil {
			path := p.Path
			if path == "" {
				path = p.Name
			}
			if path != "" && !contains(s.Artifacts, path) {
				s.Artifacts = append(s.Artifacts, path)
				sort.Strings(s.Artifacts)
			}
		}
	case relay.EventError:
		var p struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(env.Payload, &p) == nil {
			s.Err = strings.TrimSpace(p.Code + " " + p.Message)
		}
	}
	return s
}

// decodeSnapshot reads a STATE_SNAPSHOT payload into a Snapshot.
//
// The payload is relay.StateSnapshotPayload: identity, counters and per-avatar
// draft hashes. It carries no document text, no agent rows and no plan — those
// arrive as their own frames — so this decodes only what is really there. This
// matters: an earlier version read invented fields, which produced an empty
// session id and made every command fail.
func decodeSnapshot(raw json.RawMessage) (Snapshot, bool) {
	var p relay.StateSnapshotPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Snapshot{}, false
	}
	return Snapshot{
		Phase:         p.Phase,
		Role:          p.Role,
		MemberID:      p.MemberID,
		RunID:         p.RunID,
		BaselineRev:   p.BaselineRev,
		SynthesisRev:  p.SynthesisRev,
		SynthesisHash: p.DraftHashes["synthesizer"],
	}, true
}

// setAgent replaces or appends one agent row, keeping the list sorted so the
// screen does not reshuffle under the reader between frames.
func (s Snapshot) setAgent(row AgentRow) Snapshot {
	for i := range s.Agents {
		if s.Agents[i].ID == row.ID {
			// A frame may omit a field the previous one carried; keep it.
			if row.Host == "" {
				row.Host = s.Agents[i].Host
			}
			if row.Model == "" {
				row.Model = s.Agents[i].Model
			}
			s.Agents[i] = row
			return s
		}
	}
	s.Agents = append(s.Agents, row)
	sort.SliceStable(s.Agents, func(i, j int) bool { return s.Agents[i].ID < s.Agents[j].ID })
	return s
}

// upsertQuestion adds or replaces one question, keyed by id.
func (s Snapshot) upsertQuestion(row QuestionRow) Snapshot {
	for i := range s.Questions {
		if s.Questions[i].ID == row.ID {
			s.Questions[i] = row
			return s
		}
	}
	s.Questions = append(s.Questions, row)
	return s
}

// removeQuestion drops one answered question.
func (s Snapshot) removeQuestion(id string) Snapshot {
	out := s.Questions[:0]
	for _, q := range s.Questions {
		if q.ID != id {
			out = append(out, q)
		}
	}
	s.Questions = out
	return s
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// agentName is the label for an avatar id, including the run-scoped lanes.
func agentName(id string) string {
	if after, ok := strings.CutPrefix(id, "specialist:"); ok {
		return "Specialist · " + after
	}
	if after, ok := strings.CutPrefix(id, "gap:"); ok {
		return "Temporary · " + after
	}
	if n, ok := permanentNames[id]; ok {
		return n
	}
	return id
}

// permanentNames labels the fixed roster. A role the terminal does not know is
// shown by its id rather than dropped: the authority decides who exists.
var permanentNames = map[string]string{
	"pm":            "Product Manager",
	"architect":     "Technical Architect",
	"critic":        "Red-Team Critic",
	"synthesizer":   "Lead Synthesizer",
	"code_reviewer": "Code Reviewer",
	"tech_lead":     "Technical Leader",
	"narrator":      "Narrator",
}

// agentKind classifies an agent id for the Agents pane.
func agentKind(id string) string {
	switch {
	case strings.HasPrefix(id, "gap:"):
		return "temporary"
	case strings.HasPrefix(id, "specialist:"):
		return "specialist"
	default:
		return "permanent"
	}
}
