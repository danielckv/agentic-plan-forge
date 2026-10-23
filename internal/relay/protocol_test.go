package relay_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

func validEnvelope(t *testing.T) relay.WSEnvelope {
	t.Helper()
	payload, err := json.Marshal(relay.AvatarThoughtPayload{
		RunID: "run-1", AvatarID: "pm", Cycle: 1, Status: "Drafting", PublicSummary: "Drafting epics",
	})
	if err != nil {
		t.Fatal(err)
	}
	return relay.WSEnvelope{
		Version:   contracts.ProtocolVersion,
		SessionID: "sess-1",
		SenderID:  "member-1",
		EventID:   "evt-1",
		EventType: relay.EventAvatarThought,
		Seq:       42,
		Revision:  7,
		Epoch:     3,
		Timestamp: time.Unix(0, 0).UTC(),
		Payload:   payload,
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	env := validEnvelope(t)
	raw, err := relay.Encode(env)
	if err != nil {
		t.Fatal(err)
	}
	got, err := relay.DecodeEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Seq != env.Seq || got.EventType != env.EventType || got.SessionID != env.SessionID {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if string(got.Payload) != string(env.Payload) {
		t.Fatal("round trip altered the payload")
	}
}

func TestCountersAreDecimalStrings(t *testing.T) {
	raw, err := relay.Encode(validEnvelope(t))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"seq", "revision", "epoch"} {
		value := string(doc[key])
		if !strings.HasPrefix(value, `"`) {
			t.Fatalf("%s = %s, want a quoted decimal string so JavaScript cannot lose precision", key, value)
		}
	}
}

func TestPayloadsAreObjectsNotBase64(t *testing.T) {
	raw, err := relay.Encode(validEnvelope(t))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(doc.Payload)), "{") {
		t.Fatalf("payload = %s, want a JSON object rather than a base64 string", doc.Payload)
	}
}

func TestDecodeRejectsBadFrames(t *testing.T) {
	good, err := relay.Encode(validEnvelope(t))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		raw  string
	}{
		{"unsupported version", strings.Replace(string(good), `"version":1`, `"version":2`, 1)},
		{"unknown event type", strings.Replace(string(good), `"AVATAR_THOUGHT"`, `"NOT_REAL"`, 1)},
		{"unknown field", strings.Replace(string(good), `"seq"`, `"unexpected_field":1,"seq"`, 1)},
		{"trailing json", string(good) + `{"extra":true}`},
		{"empty", ``},
		{"missing session", strings.Replace(string(good), `"sess-1"`, `""`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := relay.DecodeEnvelope([]byte(tc.raw)); err == nil {
				t.Fatalf("expected %s to be rejected", tc.name)
			}
		})
	}
}

func TestDecodeRejectsOversizedFrame(t *testing.T) {
	big := make([]byte, relay.MaxFrameBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	_, err := relay.DecodeEnvelope(big)
	var bound *contracts.ErrBound
	if err == nil {
		t.Fatal("expected an oversized frame to be rejected")
	}
	if !strings.Contains(err.Error(), "frame_bytes") {
		t.Fatalf("error should name the bound, got %v", err)
	}
	_ = bound
}

func TestEventTypesAreRegistered(t *testing.T) {
	for _, eventType := range []relay.WSEventType{
		relay.EventTextDelta, relay.EventPageTransition, relay.EventCycleStart,
		relay.EventAvatarThought, relay.EventAvatarDraft, relay.EventAvatarCritique,
		relay.EventDecisionPrompt, relay.EventDecisionResolve, relay.EventPlanApproved,
		relay.EventIngestAppend, relay.EventRunAssigned, relay.EventTextReject,
	} {
		if !eventType.Valid() {
			t.Errorf("%s is not registered", eventType)
		}
	}
	if relay.WSEventType("NOPE").Valid() {
		t.Error("an invented event type was accepted")
	}
}

func TestDocumentAndFrameBoundsAreOrdered(t *testing.T) {
	if relay.MaxDocumentBytes >= relay.MaxFrameBytes {
		t.Fatalf("document bound %d must stay below the frame bound %d so a snapshot always fits", relay.MaxDocumentBytes, relay.MaxFrameBytes)
	}
	if relay.MaxInsertBytes > relay.MaxDocumentBytes {
		t.Fatalf("insert bound %d must not exceed the document bound %d", relay.MaxInsertBytes, relay.MaxDocumentBytes)
	}
	if relay.MaxIngestAppendBytes > relay.MaxDocumentBytes {
		t.Fatalf("ingest append bound %d must not exceed the document bound %d", relay.MaxIngestAppendBytes, relay.MaxDocumentBytes)
	}
}

func TestClampTimeoutAndOutput(t *testing.T) {
	req := contracts.GenerateRequest{}
	if got := req.ClampTimeout(); got != contracts.DefaultGenerateTimeout {
		t.Fatalf("unset timeout = %v, want the default %v", got, contracts.DefaultGenerateTimeout)
	}
	req.Timeout = contracts.MaxGenerateTimeout + time.Minute
	if got := req.ClampTimeout(); got != contracts.MaxGenerateTimeout {
		t.Fatalf("oversized timeout = %v, want it clamped to %v", got, contracts.MaxGenerateTimeout)
	}
	req.Timeout = 10 * time.Minute
	if got := req.ClampTimeout(); got != 10*time.Minute {
		t.Fatalf("in-range timeout = %v, want it preserved", got)
	}
	req.Timeout = 30 * time.Second
	if got := req.ClampTimeout(); got != 30*time.Second {
		t.Fatalf("in-range timeout = %v, want it preserved", got)
	}
	req.MaxOutput = 1 << 40
	if got := req.ClampMaxOutput(); got != contracts.MaxStageOutputBytes {
		t.Fatalf("oversized max output = %d, want %d", got, contracts.MaxStageOutputBytes)
	}
}
