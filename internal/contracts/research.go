package contracts

import "time"

// Research is what a detached batch learned about the questions a planning
// session left open. A deliberation stage spends one bounded call on evidence
// that already exists; a research batch spends minutes or hours on evidence
// that does not, and writes what it found to disk for the next run to read.
//
// Nothing here is a verified fact. A finding is a model's claim with the exact
// source text it rests on attached, and the avatars are told to read it that
// way.

// Research question kinds. A batch never invents a question: every kind names
// a residue an earlier session already left behind.
const (
	// ResearchAmbiguity is a PRD ambiguity the interpreter raised and nobody
	// resolved.
	ResearchAmbiguity = "AMBIGUITY"
	// ResearchConflict is an impact link whose verdict is conflicts_with: a
	// requirement that contradicts a decision the team recorded.
	ResearchConflict = "CONFLICT"
	// ResearchUnverifiable is a drift item that a coverage gap made unprovable.
	ResearchUnverifiable = "UNVERIFIABLE"
	// ResearchCoverageGap is a file the scanner could not read, parse or finish.
	ResearchCoverageGap = "COVERAGE_GAP"
	// ResearchOpenDecision is a decision a paused run is still waiting on.
	ResearchOpenDecision = "OPEN_DECISION"
	// ResearchAsked is a question the user typed.
	ResearchAsked = "ASKED"
)

// Research question statuses.
const (
	// ResearchOpen is a question the batch has not answered yet.
	ResearchOpen = "OPEN"
	// ResearchAnswered means at least one finding claims to answer it.
	ResearchAnswered = "ANSWERED"
	// ResearchExhausted means the batch spent its budget without an answer.
	ResearchExhausted = "EXHAUSTED"
	// ResearchSkipped means the sources have not changed since the last batch
	// asked this question, so asking again would buy nothing.
	ResearchSkipped = "SKIPPED"
)

// Research finding sources: where the quoted evidence came from.
const (
	ResearchSourceCode = "code"
	ResearchSourceDoc  = "doc"
	ResearchSourceGit  = "git"
	// ResearchSourceWeb is evidence fetched from outside the machine. It is the
	// only source that leaves the repository, it is off unless the batch was
	// started with --web, and it carries its URL.
	ResearchSourceWeb = "web"
)

// Research batch states.
const (
	ResearchIdle    = "idle"
	ResearchRunning = "running"
	ResearchDone    = "done"
	ResearchStopped = "stopped"
	ResearchFailed  = "failed"
)

// Stop reasons, in the order the batch checks them.
const (
	StopTimeLimit     = "time limit"
	StopCallLimit     = "call limit"
	StopEvidenceLimit = "evidence limit"
	StopCancelled     = "cancelled"
	StopExhausted     = "no questions left"
)

// ResearchQuestion is one thing the plan does not know, derived from a residue
// an earlier session left on disk. Paths come from that residue, never from a
// model.
type ResearchQuestion struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Question is one sentence, in the words of the residue it came from.
	Question string `json:"question"`
	// Origin is the identifier of the residue: an ambiguity id, a drift item
	// id, an impact link id or a decision id. Empty when the user asked.
	Origin string `json:"origin,omitempty"`
	// Paths are the repository-relative files the question is about.
	Paths  []string `json:"paths,omitempty"`
	Status string   `json:"status"`
	// Rank orders the questions against the requirements text. Higher first.
	Rank float64 `json:"rank"`
	// Calls is how many provider calls this question has cost so far.
	Calls int `json:"calls"`
	// SourceDigest identifies the evidence the last answered call was shown, so
	// a later batch can skip a question whose sources have not moved.
	SourceDigest string `json:"source_digest,omitempty"`
}

// ResearchFinding is one claim with the source text it rests on.
//
// Path, Line and Quote are resolved from the candidate the model was shown,
// the way an ImpactLink resolves them: the model answers with an identifier,
// so it cannot name a file that does not exist.
type ResearchFinding struct {
	ID         string `json:"id"`
	QuestionID string `json:"question_id"`
	Source     string `json:"source"`
	// Claim is one sentence stating what the evidence shows.
	Claim string `json:"claim"`
	Path  string `json:"path,omitempty"`
	Line  int    `json:"line,omitempty"`
	// URL is set on a web finding and on no other kind.
	URL string `json:"url,omitempty"`
	// Quote is the exact source text, clipped. It is evidence, not a claim.
	Quote      string `json:"quote,omitempty"`
	Confidence string `json:"confidence,omitempty"`
	// Answers reports that this finding closes its question rather than only
	// bearing on it.
	Answers bool `json:"answers"`
	// Dropped means the user removed it; a dropped finding never reaches a
	// stage payload.
	Dropped   bool      `json:"dropped,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// ResearchReceipt is what a batch spent and why it stopped. Every number is
// observed, never estimated.
type ResearchReceipt struct {
	Calls     int `json:"calls"`
	CallLimit int `json:"call_limit"`
	// ElapsedSeconds and TimeLimitSeconds are wall clock.
	ElapsedSeconds   int `json:"elapsed_seconds"`
	TimeLimitSeconds int `json:"time_limit_seconds"`
	// EvidenceBytes is the marshalled size of the findings kept.
	EvidenceBytes  int    `json:"evidence_bytes"`
	EvidenceBudget int    `json:"evidence_budget_bytes"`
	StopReason     string `json:"stop_reason,omitempty"`
	// Usage is the provider accounting for this batch, one row per host, model
	// and role.
	Usage UsageReport `json:"usage"`
}

// ResearchReport is the whole batch: the questions, the findings and the
// receipt. It is what `plan-forge research --status`, GET /api/research and
// the next run all read.
type ResearchReport struct {
	SchemaVersion int       `json:"schema_version"`
	State         string    `json:"state"`
	BatchID       string    `json:"batch_id,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	EndedAt       time.Time `json:"ended_at,omitempty"`
	// PRDHash and WorktreeHash identify the inputs the questions were derived
	// from, so findings are never shown for a plan or a tree they predate.
	PRDHash      string `json:"prd_hash,omitempty"`
	WorktreeHash string `json:"worktree_hash,omitempty"`
	// Web reports that this batch was allowed to fetch from outside the
	// machine.
	Web       bool               `json:"web"`
	Questions []ResearchQuestion `json:"questions"`
	Findings  []ResearchFinding  `json:"findings"`
	Receipt   ResearchReceipt    `json:"receipt"`
	// Notes explain a question that was dropped, a candidate the model named
	// that did not exist, or a source that could not be read.
	Notes []string `json:"notes,omitempty"`
}

// Kept returns the findings that reach a stage: everything the user has not
// dropped.
func (r ResearchReport) Kept() []ResearchFinding {
	out := make([]ResearchFinding, 0, len(r.Findings))
	for _, f := range r.Findings {
		if !f.Dropped {
			out = append(out, f)
		}
	}
	return out
}

// OpenQuestions counts the questions no finding has answered.
func (r ResearchReport) OpenQuestions() int {
	n := 0
	for _, q := range r.Questions {
		if q.Status == ResearchOpen || q.Status == ResearchExhausted {
			n++
		}
	}
	return n
}
