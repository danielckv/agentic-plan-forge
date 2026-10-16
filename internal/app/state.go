//go:build client && !server

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/config"
)

// SessionPhase is the lifecycle value recorded in a session-state file. It is
// the handle `plan-forge wait` polls.
type SessionPhase string

// Lifecycle values. starting and running are non-terminal.
const (
	PhaseStarting  SessionPhase = "starting"
	PhaseRunning   SessionPhase = "running"
	PhaseApproved  SessionPhase = "approved"
	PhaseCancelled SessionPhase = "cancelled"
	PhaseFailed    SessionPhase = "failed"
	PhasePrereq    SessionPhase = "prereq"
)

// Terminal reports whether no further transition follows.
func (p SessionPhase) Terminal() bool {
	switch p {
	case PhaseApproved, PhaseCancelled, PhaseFailed, PhasePrereq:
		return true
	}
	return false
}

// Exit codes the session lifecycle maps onto. They match cmd/client.
const (
	ExitOK           = 0
	ExitFailure      = 1
	ExitUsage        = 2
	ExitPrerequisite = 3
	ExitWaitTimeout  = 75
	ExitCancelled    = 130
)

// ExitCodeFor maps a terminal lifecycle value to its documented exit code.
// Non-terminal values have no exit code and report ExitFailure.
func ExitCodeFor(p SessionPhase) int {
	switch p {
	case PhaseApproved:
		return ExitOK
	case PhaseCancelled:
		return ExitCancelled
	case PhasePrereq:
		return ExitPrerequisite
	default:
		return ExitFailure
	}
}

// SessionState is the contents of PlanForgeCache()/sessions/<session_id>.json
// (0600, replaced atomically). It never holds a secret, an invite URL or prompt
// text. In Remote mode RelaySessionID additionally records the relay-issued
// 128-bit id.
type SessionState struct {
	SessionID      string       `json:"session_id"`
	RelaySessionID string       `json:"relay_session_id,omitempty"`
	PID            int          `json:"pid"`
	State          SessionPhase `json:"state"`
	ExitCode       int          `json:"exit_code"`
	ArtifactPaths  []string     `json:"artifact_paths"`
	// Message is a one-line, secret-free explanation of a terminal state that
	// `wait` prints to stderr (for example the missing prerequisite).
	Message   string    `json:"message,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// sessionsDir returns PlanForgeCache()/sessions, created 0700.
func sessionsDir() (string, error) {
	cache, err := config.EnsureCache()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func checkSessionID(id string) error {
	if !ValidSessionID(id) {
		return fmt.Errorf("session id %q is not a valid identifier", id)
	}
	return nil
}

// StatePath is PlanForgeCache()/sessions/<id>.json.
func StatePath(id string) (string, error) {
	if err := checkSessionID(id); err != nil {
		return "", err
	}
	dir, err := sessionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".json"), nil
}

// LogPath is PlanForgeCache()/sessions/<id>.log.
func LogPath(id string) (string, error) {
	if err := checkSessionID(id); err != nil {
		return "", err
	}
	dir, err := sessionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".log"), nil
}

// WriteState records st atomically: a same-directory temporary file (0600) is
// synced and renamed over the state file, so a reader never sees a partial
// document.
func WriteState(st SessionState) error {
	path, err := StatePath(st.SessionID)
	if err != nil {
		return err
	}
	if st.ArtifactPaths == nil {
		st.ArtifactPaths = []string{}
	}
	st.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicWrite(path, append(raw, '\n'))
}

// ErrNoSession reports that no state file exists for a session id.
var ErrNoSession = errors.New("no such session")

// ReadState loads the state file of a session.
func ReadState(id string) (SessionState, error) {
	path, err := StatePath(id)
	if err != nil {
		return SessionState{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return SessionState{}, ErrNoSession
	}
	if err != nil {
		return SessionState{}, err
	}
	var st SessionState
	if err := json.Unmarshal(raw, &st); err != nil {
		return SessionState{}, fmt.Errorf("session state is unreadable: %w", err)
	}
	return st, nil
}

// atomicWrite replaces path with data (0600) through a same-directory
// temporary file.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// --- session credentials ----------------------------------------------------------------

// Credential is PlanForgeHome()/sessions/<session_id>.cred (0600, directory
// 0700): the relay origin and secrets for one Remote session. It is deleted when
// the session ends and is never copied into state files, logs or JSON output.
type Credential struct {
	SessionID      string `json:"session_id"`
	Origin         string `json:"origin"`
	RelaySessionID string `json:"relay_session_id"`
	Role           string `json:"role"` // owner | member
	// Secret is the owner secret (owner) or member secret (member).
	Secret string `json:"secret"`
	// MemberID is the relay-issued member id of a joined member.
	MemberID string `json:"member_id,omitempty"`
	// InviteSecret is the invite minted at creation, owner only.
	InviteSecret string `json:"invite_secret,omitempty"`
}

func credPath(id string) (string, error) {
	if err := checkSessionID(id); err != nil {
		return "", err
	}
	home, err := config.EnsureHome()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".cred"), nil
}

// SaveCredential stores the credential atomically with owner-only permissions.
func SaveCredential(c Credential) error {
	path, err := credPath(c.SessionID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return atomicWrite(path, append(raw, '\n'))
}

// LoadCredential reads a stored credential.
func LoadCredential(id string) (Credential, error) {
	path, err := credPath(id)
	if err != nil {
		return Credential{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Credential{}, ErrNoSession
	}
	if err != nil {
		return Credential{}, err
	}
	var c Credential
	if err := json.Unmarshal(raw, &c); err != nil {
		return Credential{}, fmt.Errorf("session credential is unreadable: %w", err)
	}
	return c, nil
}

// DeleteCredential removes a stored credential, ignoring a missing file.
func DeleteCredential(id string) {
	path, err := credPath(id)
	if err != nil {
		return
	}
	_ = os.Remove(path)
}
