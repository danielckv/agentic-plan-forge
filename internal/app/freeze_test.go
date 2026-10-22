//go:build client && !server

package app

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
	"github.com/danielckv/agentic-plan-forge/internal/session"
)

// Confirming the context must never be blocked by what the user wrote. The
// text routinely holds long opaque strings: repository paths, branch names,
// commit hashes, test names. A content scan once refused all of them as
// possible secrets, and the window had no way to clear the refusal.
func TestFreezeAcceptsAnyComposedText(t *testing.T) {
	brief := strings.Join([]string{
		"# Export invoices",
		"",
		"Build on github.com/danielckv/agentic-plan-forge at h-18d50664e1b5--feat-brainstorm-intake.",
		"Keep TestGenerateRefusesAPayloadOverTheBound green.",
		"Example key from the vendor docs: AKIAIOSFODNN7EXAMPLE",
		"Commit 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		"",
	}, "\n")

	var mu sync.Mutex
	var frames []relay.WSEnvelope
	rm := newRoom("session", nil, func(raw []byte) error {
		var env relay.WSEnvelope
		if err := json.Unmarshal(raw, &env); err == nil {
			mu.Lock()
			frames = append(frames, env)
			mu.Unlock()
		}
		return nil
	}, quietLogger(), brief)
	defer rm.close()
	exec := newExecutor(executorConfig{Auth: soloAuthority{rm}, Logger: quietLogger()})
	wireSolo(rm, exec)

	raw, err := json.Marshal(relay.WSEnvelope{
		Version: contracts.ProtocolVersion, SessionID: "session", EventID: "freeze-1",
		EventType: relay.EventFreezeRequest, Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	rm.HandleUI(raw)

	mu.Lock()
	defer mu.Unlock()
	for _, f := range frames {
		if f.EventType == relay.EventError {
			t.Fatalf("FREEZE_REQUEST was refused: %s", f.Payload)
		}
	}
	if phase := rm.reducer.Snapshot().Phase; phase != session.PhaseDeliberation {
		t.Fatalf("phase after FREEZE_REQUEST = %s, want %s", phase, session.PhaseDeliberation)
	}
}
