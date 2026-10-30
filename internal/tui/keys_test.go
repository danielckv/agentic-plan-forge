//go:build client && !server

package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/danielckv/agentic-plan-forge/internal/collab"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// The tests here drive the real key path — handleKey — against a socket that
// enforces the handshake and reports what it received. Testing command() in
// isolation missed two real defects: an empty session id, and digits being
// eaten by the tab shortcut. Both are only visible when a key is pressed.

// authBridge is a loopback WebSocket that enforces the browser's handshake and
// records every command it is sent.
type authBridge struct {
	t      *testing.T
	token  string
	frames chan relay.WSEnvelope

	mu       sync.Mutex
	commands []relay.WSEnvelope
	authOK   chan struct{}
}

func newAuthBridge(t *testing.T, token string) (*authBridge, *httptest.Server) {
	b := &authBridge{t: t, token: token, frames: make(chan relay.WSEnvelope, 16), authOK: make(chan struct{}, 1)}
	srv := httptest.NewServer(http.HandlerFunc(b.handle))
	t.Cleanup(func() {
		srv.Close()
		close(b.frames)
	})
	return b, srv
}

func (f *authBridge) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ws" {
		http.NotFound(w, r)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		Subprotocols:       []string{relay.WSSubprotocol},
	})
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	ctx := context.Background()

	_, data, err := c.Read(ctx)
	if err != nil {
		return
	}
	var auth struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	if json.Unmarshal(data, &auth) != nil || auth.Type != "auth" || auth.Token != f.token {
		_ = c.Close(websocket.StatusPolicyViolation, "authentication failed")
		return
	}
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"auth.ok"}`))
	f.authOK <- struct{}{}

	go func() {
		for env := range f.frames {
			raw, _ := json.Marshal(env)
			if err := c.Write(ctx, websocket.MessageText, raw); err != nil {
				return
			}
		}
	}()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		env, derr := relay.DecodeEnvelope(data)
		if derr != nil {
			// A frame the authority would refuse never reaches the recorder:
			// that is the point of decoding here with the same decoder.
			continue
		}
		f.mu.Lock()
		f.commands = append(f.commands, env)
		f.mu.Unlock()
	}
}

func (f *authBridge) sent() []relay.WSEnvelope {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]relay.WSEnvelope(nil), f.commands...)
}

// waitCommand blocks until a command of the given type arrives.
func (f *authBridge) waitCommand(t *testing.T, want relay.WSEventType) relay.WSEnvelope {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range f.sent() {
			if c.EventType == want {
				return c
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no %s command arrived; got %v", want, f.sent())
	return relay.WSEnvelope{}
}

// envelope builds a frame the authority would really send: version, session and
// event id are all required by DecodeEnvelope.
func envelope(t *testing.T, typ relay.WSEventType, payload any) relay.WSEnvelope {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return relay.WSEnvelope{
		Version: contracts.ProtocolVersion, SessionID: "sess-live", EventID: "ev-" + string(typ),
		EventType: typ, Payload: raw,
	}
}

// startView dials a bridge and folds one real snapshot, so the view carries the
// session id and counters a command needs.
func startView(t *testing.T, token string) (*authBridge, *socket, View, *httptest.Server) {
	t.Helper()
	b, srv := newAuthBridge(t, token)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	sock, err := dial(ctx, Options{Origin: "http://" + strings.TrimPrefix(srv.URL, "http://"), Token: token})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(sock.close)
	select {
	case <-b.authOK:
	case <-time.After(5 * time.Second):
		t.Fatal("never authenticated")
	}
	var view View
	view.Snapshot = view.Snapshot.Apply(envelope(t, relay.EventStateSnapshot, map[string]any{
		"member_id": "owner", "role": "owner", "phase": "DELIBERATION",
		"run_id": "run-live", "baseline_rev": "7", "synthesis_rev": "0",
		"draft_hashes": map[string]string{},
	}))
	return b, sock, view, srv
}

// The session id comes from the envelope, never from the payload. Without it
// every command is an invalid frame at the authority.
func TestSnapshotCarriesTheEnvelopeSessionID(t *testing.T) {
	var s Snapshot
	s = s.Apply(envelope(t, relay.EventStateSnapshot, map[string]any{
		"role": "owner", "phase": "DELIBERATION", "run_id": "run-1", "baseline_rev": "7",
	}))
	if s.SessionID != "sess-live" {
		t.Fatalf("session id = %q, want the envelope's", s.SessionID)
	}
	// A later non-snapshot frame keeps it.
	s = s.Apply(envelope(t, relay.EventAvatarThought, map[string]any{"avatar_id": "pm", "status": "Thinking"}))
	if s.SessionID != "sess-live" {
		t.Fatalf("session id lost after a thought frame: %q", s.SessionID)
	}
	if s.BaselineRev != 7 || s.RunID != "run-1" {
		t.Fatalf("counters not decoded: %+v", s)
	}
}

// On the questions pane a digit answers the selected question. It must not be
// swallowed by the tab shortcut, or no decision could ever be answered.
func TestDigitAnswersTheSelectedQuestion(t *testing.T) {
	b, sock, view, _ := startView(t, "tok")
	view.Tab = TabQuestions
	view.Snapshot = view.Snapshot.Apply(envelope(t, relay.EventDecisionPrompt, map[string]any{
		"decision_id": "d1", "run_id": "run-live", "input_revision": "4",
		"question": "Where should the export be written?",
		"options": []map[string]string{
			{"id": "stream", "label": "Stream the download"},
			{"id": "stage", "label": "Stage the file"},
		},
		"recommended_option_id": "stage",
	}))
	if len(view.Snapshot.Questions) != 1 {
		t.Fatalf("question not decoded: %+v", view.Snapshot.Questions)
	}

	next, err := handleKey(view, keyMsg{Rune: '2'}, sock)
	if err != nil {
		t.Fatalf("handleKey: %v", err)
	}
	if next.Tab != TabQuestions {
		t.Fatal("the digit switched tabs instead of answering")
	}
	cmd := b.waitCommand(t, relay.EventDecisionResolve)
	var p struct {
		DecisionID    string `json:"decision_id"`
		InputRevision string `json:"input_revision"`
		OptionID      string `json:"option_id"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.DecisionID != "d1" || p.OptionID != "stage" {
		t.Fatalf("payload = %+v", p)
	}
	if p.InputRevision != "4" {
		t.Fatalf("input_revision = %q, want the card's 4 (a stale value is rejected)", p.InputRevision)
	}
	if cmd.SessionID != "sess-live" {
		t.Fatalf("command session id = %q", cmd.SessionID)
	}
	if cmd.EventID == "" {
		t.Fatal("command carries no event id")
	}
}

// On any other pane the digits still switch panes.
func TestDigitsSwitchTabsAwayFromQuestions(t *testing.T) {
	view := View{Tab: TabAgents}
	next, err := handleKey(view, keyMsg{Rune: '3'}, nil)
	if err != nil {
		t.Fatalf("handleKey: %v", err)
	}
	if next.Tab != TabPlan {
		t.Fatalf("tab = %q, want plan", next.Tab)
	}
}

// Approval echoes the baseline, revision and hash the reducer compares. Guessed
// values are refused as STALE_*, so a missing value is reported before sending.
func TestApprovalEchoesTheRealRevisions(t *testing.T) {
	b, sock, view, _ := startView(t, "tok")
	view.Snapshot = view.Snapshot.Apply(envelope(t, relay.EventAvatarDraft, map[string]any{
		"avatar_id": "synthesizer", "draft_revision": "3", "content_hash": "cafe1234",
		"complete": true, "schema_id": "synthesis.v1",
	}))
	if view.Snapshot.SynthesisHash != "cafe1234" || view.Snapshot.SynthesisRev != 3 {
		t.Fatalf("synthesis identity not tracked: %+v", view.Snapshot)
	}

	next, err := handleKey(view, keyMsg{Rune: 'a'}, sock)
	if err != nil {
		t.Fatalf("handleKey: %v", err)
	}
	if strings.Contains(next.Notice, "no locked synthesis") {
		t.Fatalf("approval refused despite a locked synthesis: %s", next.Notice)
	}

	cmd := b.waitCommand(t, relay.EventApprove)
	var p struct {
		BaselineRev   string `json:"baseline_rev"`
		RunID         string `json:"run_id"`
		SynthesisRev  string `json:"synthesis_rev"`
		ContentSHA256 string `json:"content_sha256"`
	}
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.BaselineRev != "7" || p.RunID != "run-live" || p.SynthesisRev != "3" || p.ContentSHA256 != "cafe1234" {
		t.Fatalf("approve payload = %+v", p)
	}
}

// Without a locked synthesis the keystroke explains itself rather than sending a
// frame the authority would reject.
func TestApprovalRefusesWithoutASynthesis(t *testing.T) {
	view := View{}
	view.Snapshot = view.Snapshot.Apply(envelope(t, relay.EventStateSnapshot, map[string]any{
		"role": "owner", "phase": "DELIBERATION", "run_id": "run-live", "baseline_rev": "7",
	}))
	next, err := handleKey(view, keyMsg{Rune: 'a'}, nil)
	if err != nil {
		t.Fatalf("handleKey: %v", err)
	}
	if !strings.Contains(next.Notice, "no locked synthesis") {
		t.Fatalf("notice = %q, want a refusal", next.Notice)
	}
}

// Every command the terminal can send names the session and carries an event id,
// because the authority strict-decodes each frame.
func TestEveryCommandIsAWellFormedEnvelope(t *testing.T) {
	view := View{}
	view.Snapshot = Snapshot{SessionID: "sess-live", RunID: "run-1", BaselineRev: 2, SynthesisRev: 5, SynthesisHash: "abc"}
	cases := []struct {
		event   relay.WSEventType
		payload any
	}{
		{relay.EventApprove, map[string]string{"baseline_rev": "2", "run_id": "run-1", "synthesis_rev": "5", "content_sha256": "abc"}},
		{relay.EventDecisionResolve, map[string]string{"decision_id": "d", "input_revision": "1", "option_id": "o"}},
		{relay.EventCancel, nil},
		{relay.EventRetryStage, map[string]string{"run_id": "run-1"}},
		{relay.EventSynthesizeRequest, map[string]string{"run_id": "run-1"}},
	}
	for _, c := range cases {
		env, err := command(view.Snapshot.sessionID(), c.event, c.payload)
		if err != nil {
			t.Fatalf("%s: %v", c.event, err)
		}
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := relay.DecodeEnvelope(raw); err != nil {
			t.Errorf("%s would be refused by the authority: %v", c.event, err)
		}
	}
}

// The brief can be written from the terminal. Before this, the pane said to
// write it in the browser, so a headless machine could only run with --brief.
func TestBriefCanBeTypedAndAppended(t *testing.T) {
	b, sock, view, _ := startView(t, "tok")
	// INGESTION is the only phase the authority accepts document edits in.
	view.Snapshot = view.Snapshot.Apply(envelope(t, relay.EventStateSnapshot, map[string]any{
		"member_id": "owner", "role": "owner", "phase": "INGESTION", "run_id": "run-1",
	}))
	view.Snapshot = view.Snapshot.Apply(envelope(t, relay.EventTextSnapshot, map[string]any{
		"document_id": contracts.DocumentIDPRD, "version": "4",
		"text": "Existing requirement.\n", "sha256": "abc",
	}))
	if view.Snapshot.DocVersion != 4 || view.Snapshot.DocText == "" {
		t.Fatalf("document not tracked: version %d, %d bytes", view.Snapshot.DocVersion, len(view.Snapshot.DocText))
	}

	view.Tab = TabBrief
	next, err := handleKey(view, keyMsg{Rune: 'e'}, sock)
	if err != nil {
		t.Fatalf("enter edit mode: %v", err)
	}
	if !next.Editing {
		t.Fatal("e did not open the editor")
	}

	for _, r := range "Add MFA." {
		if next, err = handleKey(next, keyMsg{Rune: r}, sock); err != nil {
			t.Fatalf("typing %q: %v", r, err)
		}
	}
	if next.Draft != "Add MFA." {
		t.Fatalf("draft = %q", next.Draft)
	}
	// A letter that is a command outside edit mode must be plain text inside it.
	next, err = handleKey(next, keyMsg{Rune: 'q'}, sock)
	if err != nil {
		t.Fatalf("typing q: %v", err)
	}
	if next.quit {
		t.Fatal("q quit the session while the brief was being typed")
	}
	if next.Draft != "Add MFA.q" {
		t.Fatalf("draft = %q, want the q kept as text", next.Draft)
	}
	next, err = handleKey(next, keyMsg{Special: "backspace"}, sock)
	if err != nil {
		t.Fatalf("backspace: %v", err)
	}
	if next.Draft != "Add MFA." {
		t.Fatalf("draft = %q after backspace", next.Draft)
	}

	next, err = handleKey(next, keyMsg{Special: "ctrl-s"}, sock)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if next.Editing || next.Draft != "" {
		t.Fatalf("editor did not close after saving: editing=%v draft=%q", next.Editing, next.Draft)
	}

	cmd := b.waitCommand(t, relay.EventTextDelta)
	var p relay.TextDeltaPayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.DocumentID != contracts.DocumentIDPRD || p.BaseVersion != 4 {
		t.Fatalf("delta targets %q at base %d, want the prd at 4", p.DocumentID, p.BaseVersion)
	}
	// The authority rejects a push whose client id does not start with the
	// pushing member's id followed by a colon, so the form matters, not just
	// that it is non-empty.
	if len(p.Updates) != 1 || !strings.HasPrefix(p.Updates[0].ClientID, p.Updates[0].ClientID[:0]+"owner:") {
		t.Fatalf("updates = %+v, want a client id prefixed \"owner:\"", p.Updates)
	}
	// The change set must be the collab authority's own encoding, so replay it
	// through that encoder with the same inputs and require the same bytes.
	want, err := collab.AppendChange(22, "Add MFA.")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(p.Updates[0].Changes); got != string(want) {
		t.Fatalf("changes = %s, want the authority's %s", got, want)
	}
	// An append splits on newlines, which one flat string would not.
	multi, err := collab.AppendChange(22, "line one\nline two")
	if err != nil {
		t.Fatal(err)
	}
	if want := "[22,[0,\"line one\",\"line two\"]]"; string(multi) != want {
		t.Fatalf("multi-line append = %s, want %s", multi, want)
	}
}

// The editor refuses once the document is frozen, because the authority refuses
// a document edit outside INGESTION.
func TestBriefIsReadOnlyAfterFreeze(t *testing.T) {
	view := View{Tab: TabBrief}
	view.Snapshot.Phase = relay.PhaseDeliberation
	next, err := handleKey(view, keyMsg{Rune: 'e'}, nil)
	if err != nil {
		t.Fatalf("handleKey: %v", err)
	}
	if next.Editing {
		t.Fatal("the editor opened after the document was frozen")
	}
}

// An empty draft sends nothing rather than a change set the authority rejects.
func TestEmptyBriefSendsNothing(t *testing.T) {
	b, sock, view, _ := startView(t, "tok")
	view.Tab = TabBrief
	view.Editing = true
	view.Draft = "   \n\n"
	next, err := handleKey(view, keyMsg{Special: "ctrl-s"}, sock)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !strings.Contains(next.Notice, "Nothing to add") {
		t.Fatalf("notice = %q", next.Notice)
	}
	if got := b.sent(); len(got) != 0 {
		t.Fatalf("sent %d frames for an empty brief", len(got))
	}
}

// Esc cancels without sending.
func TestEscCancelsTheBrief(t *testing.T) {
	view := View{Tab: TabBrief, Editing: true, Draft: "half typed"}
	next, err := handleKey(view, keyMsg{Special: "esc"}, nil)
	if err != nil {
		t.Fatalf("handleKey: %v", err)
	}
	if next.Editing {
		t.Fatal("esc did not close the editor")
	}
	if !strings.Contains(next.Notice, "cancelled") {
		t.Fatalf("notice = %q", next.Notice)
	}
}
