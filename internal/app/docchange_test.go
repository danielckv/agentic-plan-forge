//go:build client && !server

package app

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/ast"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/impact"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// Typing in the composer is how most requirements arrive. Each accepted edit
// must schedule a re-interpretation, or the epics never appear: only uploads
// used to.
func TestTypedEditSchedulesInterpretation(t *testing.T) {
	rm := newRoom("session", nil, func([]byte) error { return nil }, quietLogger(), "")
	defer rm.close()
	calls := 0
	rm.docChanged = func() { calls++ }

	payload, err := json.Marshal(relay.TextDeltaPayload{
		DocumentID: contracts.DocumentIDPRD, BaseVersion: 0,
		Updates: []relay.Update{{ClientID: ownerMemberID + ":0", Changes: json.RawMessage(`[[0,"Export invoices"]]`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(relay.WSEnvelope{
		Version: contracts.ProtocolVersion, SessionID: "session", EventID: "delta-1",
		EventType: relay.EventTextDelta, Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	rm.HandleUI(raw)

	if text, _ := rm.Doc(); text != "Export invoices" {
		t.Fatalf("document = %q, want the typed text", text)
	}
	if calls != 1 {
		t.Fatalf("docChanged called %d times after an accepted edit, want 1", calls)
	}

	// A rejected edit (stale base) changes nothing and schedules nothing.
	rm.HandleUI(raw)
	if calls != 1 {
		t.Errorf("docChanged called %d times after a rejected edit, want still 1", calls)
	}
}

// A brief seeded before the window opens is interpreted without waiting for
// an edit.
func TestSeededBriefIsInterpretedAtStart(t *testing.T) {
	rm := newRoom("session", nil, func([]byte) error { return nil }, quietLogger(), "# Brief\n\nExport invoices.\n")
	defer rm.close()
	exec := newExecutor(executorConfig{Auth: soloAuthority{rm}, Logger: quietLogger(), Repo: t.TempDir()})
	exec.scheduleInitialInterpretation("# Brief\n\nExport invoices.\n")
	select {
	case <-exec.normTrigger:
	default:
		t.Fatal("a seeded brief scheduled no interpretation")
	}
	exec.scheduleInitialInterpretation("   ")
	select {
	case <-exec.normTrigger:
		t.Fatal("an empty document scheduled an interpretation")
	default:
	}
}

// In a remote session the owner's own typing comes back as TEXT_ACK and a
// collaborator's as TEXT_UPDATES. Both must schedule a re-interpretation;
// only uploads used to.
func TestRemoteLinkSchedulesInterpretationOnEveryAcceptedChange(t *testing.T) {
	changed := make(chan struct{}, 4)
	l := &remoteLink{
		cfg:    remoteLinkConfig{DocChanged: func() { changed <- struct{}{} }},
		events: newPump(), mirror: newFrameLog(), local: map[string][]byte{},
		pending: map[string]chan relay.WSEnvelope{}, uiEvents: map[string]bool{},
		hello: map[string]helloRequest{}, emitted: map[string]bool{}, leaseCh: make(chan struct{}, 1),
	}
	defer l.close()
	for i, typ := range []relay.WSEventType{relay.EventTextAck, relay.EventTextUpdates} {
		l.onEnvelope(relay.WSEnvelope{
			Version: contracts.ProtocolVersion, SessionID: "s", EventID: "e", EventType: typ,
			Seq: uint64(i + 1), Payload: json.RawMessage(`{"document_id":"prd","version":"1","updates":[]}`),
		})
		select {
		case <-changed:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s scheduled no interpretation", typ)
		}
	}
}

type countingIngestionGen struct {
	calls atomic.Int32
	body  string
}

func (g *countingIngestionGen) GenerateStream(_ context.Context, _ contracts.GenerateRequest, _ func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	g.calls.Add(1)
	return contracts.GenerationResult{Content: json.RawMessage(g.body)}, nil
}

func TestAmbiguityResolutionsReuseNormalizedBaseWithoutModelCalls(t *testing.T) {
	const rawNorm = `{"schema_version":1,"product":{"title":"Billing","problem":"Reconcile invoices","goals":["Export CSV"],"actors":["Analyst"],"flows":["Download CSV"],"requirements":["Export invoices"],"acceptance_criteria":["Returns CSV"],"constraints":[],"open_questions":[]},"technical_appendix":"","ambiguities":[{"id":"a1","text":"Retry failed exports","question":"Product or technical?"},{"id":"a2","text":"Index invoice_date","question":"Product or technical?"}],"warnings":[]}`
	gen := &countingIngestionGen{body: rawNorm}
	var impactCalls atomic.Int32

	rm := newRoom("session", nil, func([]byte) error { return nil }, quietLogger(), "Export invoices.")
	defer rm.close()
	exec := newExecutor(executorConfig{
		Auth:   soloAuthority{rm},
		Gen:    gen,
		Logger: quietLogger(),
		Repo:   t.TempDir(),
		Scan: func(context.Context, ast.ScanOptions) (*contracts.RepoContextGraph, error) {
			return &contracts.RepoContextGraph{SchemaVersion: contracts.SchemaVersion}, nil
		},
		Impact: func(context.Context, impact.Input) (contracts.ImpactMap, error) {
			impactCalls.Add(1)
			return readyMap("model"), nil
		},
	})
	exec.graph = &contracts.RepoContextGraph{SchemaVersion: contracts.SchemaVersion}
	exec.lastScan = time.Now()
	close(exec.scanDone)

	norm1, err := exec.interpret(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(norm1.Ambiguities) != 2 {
		t.Fatalf("open ambiguities = %d, want 2", len(norm1.Ambiguities))
	}
	if got := gen.calls.Load(); got != 1 {
		t.Fatalf("ingestion calls = %d, want 1", got)
	}

	for _, id := range []string{norm1.Ambiguities[0].ID, norm1.Ambiguities[1].ID} {
		raw, _ := json.Marshal(relay.AmbiguityResolvePayload{AmbiguityID: id, Choice: contracts.AmbiguityProduct})
		exec.handle(relay.WSEnvelope{EventType: relay.EventAmbiguityResolve, SenderID: ownerMemberID, Payload: raw})
		select {
		case imm := <-exec.normTrigger:
			if !imm {
				t.Errorf("ambiguity resolution triggered debounced normalization, want immediate")
			}
		default:
			t.Fatal("ambiguity resolution did not trigger interpretation")
		}
		normAfter, err := exec.interpret(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if normAfter.Revision <= norm1.Revision {
			t.Errorf("revision = %d, want > %d", normAfter.Revision, norm1.Revision)
		}
	}

	if got := gen.calls.Load(); got != 1 {
		t.Errorf("ingestion calls after 2 ambiguity resolutions = %d, want still 1", got)
	}
	if got := impactCalls.Load(); got != 0 {
		t.Errorf("model impact calls during background interpret = %d, want 0", got)
	}
}
