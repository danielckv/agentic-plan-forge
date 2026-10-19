//go:build client && !server

package center

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/livestore"
)

const testRepo = "/tmp/plan-forge-center-test-repo"

func newServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Config{
		Port:    -1,
		LiveDir: filepath.Join(t.TempDir(), "live"),
		Assets: fstest.MapFS{
			"index.html":    {Data: []byte("<!doctype html><div id=root></div>")},
			"assets/app.js": {Data: []byte("console.log(1)")},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("the server did not stop")
		}
	})
	return s
}

// plainClient is used for every non-streaming request. Keep-alive is off so a
// response whose body a case never reads cannot hold a connection open past
// the server's shutdown grace.
var plainClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

// do issues a request with the correct Host, and whatever headers the case
// under test wants. The body is drained and closed before returning, so the
// connection is finished with by the time the caller inspects the response.
func do(t *testing.T, s *Server, method, path string, headers map[string]string, body string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, s.URL()+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := plainClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(buf))
	return resp
}

// session opens a tab the way the CLI does: mint a ticket with the daemon
// token, then redeem it.
func session(t *testing.T, s *Server) string {
	t.Helper()
	resp := do(t, s, "POST", "/api/center/ticket", map[string]string{
		"Authorization": "Bearer " + s.Secret(),
	}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ticket: status %d", resp.StatusCode)
	}
	var minted struct{ Ticket, URL string }
	if err := json.NewDecoder(resp.Body).Decode(&minted); err != nil {
		t.Fatal(err)
	}
	red := do(t, s, "POST", "/api/session", map[string]string{
		"Origin":       s.URL(),
		"Content-Type": "application/json",
	}, fmt.Sprintf(`{"ticket":%q}`, minted.Ticket))
	if red.StatusCode != http.StatusOK {
		t.Fatalf("redeem: status %d", red.StatusCode)
	}
	var got struct{ Token string }
	if err := json.NewDecoder(red.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Token == "" {
		t.Fatal("empty session token")
	}
	return got.Token
}

// The auth matrix. Every route is tried with every wrong header, because this
// one table is what stops the fixed port from becoming an open door.
func TestAuthMatrix(t *testing.T) {
	s := newServer(t)
	tok := session(t, s)
	good := s.URL()

	cases := []struct {
		name    string
		method  string
		path    string
		headers map[string]string
		body    string
		want    int
	}{
		{"ping needs nothing", "GET", "/api/center/ping", nil, "", 200},
		{"ping refuses a foreign Origin", "GET", "/api/center/ping", map[string]string{"Origin": "https://evil.test"}, "", 403},

		{"live needs a token", "GET", "/api/center/live", nil, "", 401},
		{"live refuses a daemon token", "GET", "/api/center/live", map[string]string{"Authorization": "Bearer " + s.Secret()}, "", 401},
		{"live accepts a session token", "GET", "/api/center/live", map[string]string{"Authorization": "Bearer " + tok}, "", 200},
		{"live refuses a foreign Origin", "GET", "/api/center/live", map[string]string{"Authorization": "Bearer " + tok, "Origin": "https://evil.test"}, "", 403},
		{"live refuses a cross-site fetch", "GET", "/api/center/live", map[string]string{"Authorization": "Bearer " + tok, "Sec-Fetch-Site": "cross-site"}, "", 403},

		{"status needs a token", "GET", "/api/center/status", nil, "", 401},
		{"events needs a token", "GET", "/api/center/events", nil, "", 401},

		{"ingest refuses a session token", "POST", "/api/ingest", map[string]string{"Authorization": "Bearer " + tok}, "{}", 401},
		{"ingest refuses no credential", "POST", "/api/ingest", nil, "{}", 401},
		{"ingest refuses a browser", "POST", "/api/ingest", map[string]string{"Authorization": "Bearer " + s.Secret(), "Origin": good}, "{}", 403},
		{"ingest refuses any Origin at all", "POST", "/api/ingest", map[string]string{"Authorization": "Bearer " + s.Secret(), "Origin": "https://evil.test"}, "{}", 403},

		{"ticket refuses a session token", "POST", "/api/center/ticket", map[string]string{"Authorization": "Bearer " + tok}, "", 401},
		{"ticket refuses a browser", "POST", "/api/center/ticket", map[string]string{"Authorization": "Bearer " + s.Secret(), "Origin": good}, "", 403},

		{"session refuses a bare tab", "POST", "/api/session", map[string]string{"Origin": good}, "{}", 403},
		{"session refuses no Origin", "POST", "/api/session", nil, "{}", 403},
		{"session refuses an unknown ticket", "POST", "/api/session", map[string]string{"Origin": good}, `{"ticket":"deadbeef"}`, 403},

		{"the bundle needs nothing", "GET", "/", nil, "", 200},
		{"a client route serves the bundle", "GET", "/live", nil, "", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, s, tc.method, tc.path, tc.headers, tc.body)
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if got := resp.Header.Get("Content-Security-Policy"); got != CSP {
				t.Errorf("CSP = %q", got)
			}
			if got := resp.Header.Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q", got)
			}
			for h, want := range map[string]string{
				"X-Content-Type-Options":     "nosniff",
				"Referrer-Policy":            "no-referrer",
				"Cross-Origin-Opener-Policy": "same-origin",
			} {
				if got := resp.Header.Get(h); got != want {
					t.Errorf("%s = %q, want %q", h, got, want)
				}
			}
		})
	}
}

// A name that resolves to loopback still arrives carrying its own Host, which
// is what the Host check refuses.
func TestForeignHostIsRefused(t *testing.T) {
	s := newServer(t)
	req, err := http.NewRequest("GET", s.URL()+"/api/center/ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "attacker.test"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestATicketIsSingleUse(t *testing.T) {
	s := newServer(t)
	resp := do(t, s, "POST", "/api/center/ticket", map[string]string{"Authorization": "Bearer " + s.Secret()}, "")
	var minted struct{ Ticket, URL string }
	if err := json.NewDecoder(resp.Body).Decode(&minted); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(minted.URL, "#t="+minted.Ticket) {
		t.Errorf("url = %q, want the ticket in the fragment", minted.URL)
	}
	body := fmt.Sprintf(`{"ticket":%q}`, minted.Ticket)
	first := do(t, s, "POST", "/api/session", map[string]string{"Origin": s.URL()}, body)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first redeem: %d", first.StatusCode)
	}
	second := do(t, s, "POST", "/api/session", map[string]string{"Origin": s.URL()}, body)
	if second.StatusCode != http.StatusForbidden {
		t.Errorf("second redeem = %d, want 403", second.StatusCode)
	}
}

func TestAnExpiredTicketIsRefused(t *testing.T) {
	s := newServer(t)
	at := time.Now()
	s.tickets.now = func() time.Time { return at }
	tkt, err := s.tickets.mint()
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(ticketTTL + time.Second)
	if err := s.tickets.redeem(tkt); err == nil {
		t.Fatal("an expired ticket was accepted")
	}
	// Expiry must also spend it, so a leaked ticket cannot be retried.
	at = at.Add(-2 * ticketTTL)
	if err := s.tickets.redeem(tkt); err == nil {
		t.Fatal("an expired ticket was still redeemable afterwards")
	}
}

func TestTicketPoolIsBounded(t *testing.T) {
	s := newServer(t)
	for i := range maxTickets {
		if _, err := s.tickets.mint(); err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
	}
	if _, err := s.tickets.mint(); err == nil {
		t.Fatal("the pool minted past its cap")
	}
	resp := do(t, s, "POST", "/api/center/ticket", map[string]string{"Authorization": "Bearer " + s.Secret()}, "")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", resp.StatusCode)
	}
}

// readFrames reads server-sent events off a live stream until n events arrive
// or the deadline passes. Comments (heartbeats) are skipped.
func readFrames(t *testing.T, body io.Reader, n int) []string {
	t.Helper()
	sc := bufio.NewScanner(body)
	var out []string
	var cur []string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if len(cur) > 0 {
				out = append(out, strings.Join(cur, "\n"))
				cur = nil
				if len(out) == n {
					return out
				}
			}
		case strings.HasPrefix(line, ":"):
		default:
			cur = append(cur, line)
		}
	}
	return out
}

// The property the window bridge structurally cannot provide: one record
// posted by a hook reaches every open tab.
func TestOneRecordReachesEveryOpenTab(t *testing.T) {
	s := newServer(t)
	tok := session(t, s)

	const tabs = 3
	bodies := make([]io.ReadCloser, 0, tabs)
	for i := range tabs {
		req, err := http.NewRequest("GET", s.URL()+"/api/center/events", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Origin", s.URL())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("tab %d: %v", i, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("tab %d: status %d", i, resp.StatusCode)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		bodies = append(bodies, resp.Body)
	}

	// Wait for every tab to be registered before pushing.
	deadline := time.Now().Add(3 * time.Second)
	for s.ActiveClients() < tabs && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := s.ActiveClients(); got != tabs {
		t.Fatalf("active clients = %d, want %d", got, tabs)
	}

	resp := do(t, s, "POST", "/api/ingest", map[string]string{"Authorization": "Bearer " + s.Secret()},
		fmt.Sprintf(`{"repo":%q,"session_id":"claude:t1","harness":"claude-code","branch":"main","kind":"session_start"}`, testRepo))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ingest status = %d", resp.StatusCode)
	}

	for i, body := range bodies {
		// The first frame is the snapshot sent on connect, the second is the
		// push the ingest produced.
		frames := readFrames(t, body, 2)
		if len(frames) != 2 {
			t.Fatalf("tab %d received %d frames, want 2", i, len(frames))
		}
		if !strings.Contains(frames[1], "claude:t1") {
			t.Errorf("tab %d did not receive the record: %s", i, frames[1])
		}
	}
}

func TestTheStreamOpensWithASnapshot(t *testing.T) {
	s := newServer(t)
	tok := session(t, s)
	req, _ := http.NewRequest("GET", s.URL()+"/api/center/events", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Origin", s.URL())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("content type = %q", got)
	}
	frames := readFrames(t, resp.Body, 1)
	if len(frames) != 1 || !strings.Contains(frames[0], "event: snapshot") {
		t.Fatalf("first frame = %v, want a snapshot", frames)
	}
}

func TestTheHubRefusesMoreThanItsCap(t *testing.T) {
	h := newHub()
	for i := range MaxUIClients {
		if _, err := h.add(); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if _, err := h.add(); err == nil {
		t.Fatal("the hub accepted a client past its cap")
	}
}

// A client that stopped reading is dropped with a final overflow frame rather
// than blocking everyone else.
func TestASlowClientIsDroppedWithAnOverflowFrame(t *testing.T) {
	h := newHub()
	c, err := h.add()
	if err != nil {
		t.Fatal(err)
	}
	for range clientQueue {
		c.out <- frame("x")
	}
	h.broadcast("snapshot", map[string]string{"a": "b"})
	select {
	case <-c.done:
	default:
		t.Fatal("the slow client was not dropped")
	}
	if h.active() != 0 {
		t.Error("a dropped client still counts as active")
	}
	var sawOverflow bool
	for len(c.out) > 0 {
		if strings.Contains(string(<-c.out), "overflow") {
			sawOverflow = true
		}
	}
	if !sawOverflow {
		t.Error("no overflow frame was queued before the drop")
	}
}

func TestActiveClientsIgnoresAStalledTab(t *testing.T) {
	h := newHub()
	at := time.Now()
	h.now = func() time.Time { return at }
	c, err := h.add()
	if err != nil {
		t.Fatal(err)
	}
	if h.active() != 1 {
		t.Fatal("a fresh client does not count as active")
	}
	at = at.Add(staleFactor*heartbeatEvery + time.Second)
	if h.active() != 0 {
		t.Error("a tab that has not flushed for two heartbeats still counts")
	}
	c.touch(at)
	if h.active() != 1 {
		t.Error("a tab that flushed again does not count")
	}
}

// A newline inside a payload would end the event early if it were not prefixed.
func TestFrameEncodingPrefixesEveryDataLine(t *testing.T) {
	got := string(encodeFrame(7, "snapshot", []byte("a\nb")))
	want := "id: 7\nevent: snapshot\ndata: a\ndata: b\n\n"
	if got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestIngestRecordsAndRejects(t *testing.T) {
	s := newServer(t)
	auth := map[string]string{"Authorization": "Bearer " + s.Secret()}
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"records a session": {fmt.Sprintf(`{"repo":%q,"session_id":"omp:a","harness":"omp","kind":"tool"}`, testRepo), 204},
		"needs a repo":      {`{"session_id":"omp:a","harness":"omp","kind":"tool"}`, 400},
		"bad session id":    {fmt.Sprintf(`{"repo":%q,"session_id":"nope","harness":"omp","kind":"tool"}`, testRepo), 422},
		"bad kind":          {fmt.Sprintf(`{"repo":%q,"session_id":"omp:a","harness":"omp","kind":"zzz"}`, testRepo), 422},
		"unknown field":     {fmt.Sprintf(`{"repo":%q,"nope":1}`, testRepo), 400},
		"not json":          {`{`, 400},
	} {
		t.Run(name, func(t *testing.T) {
			resp := do(t, s, "POST", "/api/ingest", auth, tc.body)
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}

	tok := session(t, s)
	resp := do(t, s, "GET", "/api/center/live", map[string]string{"Authorization": "Bearer " + tok}, "")
	var snap Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Repos) != 1 {
		t.Fatalf("repos = %d, want 1", len(snap.Repos))
	}
	if _, ok := snap.Repos[0].Sessions["omp:a"]; !ok {
		t.Errorf("session missing from the snapshot: %+v", snap.Repos[0])
	}
	// The repository's absolute path must not reach the browser.
	raw, _ := json.Marshal(snap)
	if bytes.Contains(raw, []byte(testRepo)) {
		t.Error("the snapshot carries the absolute repository path")
	}
}

func TestAnOversizedIngestBodyIsRefused(t *testing.T) {
	s := newServer(t)
	big := fmt.Sprintf(`{"repo":%q,"session_id":"omp:a","harness":"omp","kind":"tool","detail":%q}`,
		testRepo, strings.Repeat("x", maxIngestBytes*2))
	resp := do(t, s, "POST", "/api/ingest", map[string]string{"Authorization": "Bearer " + s.Secret()}, big)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestStaticServesTheBundleAndRefusesAMissingAsset(t *testing.T) {
	s := newServer(t)
	for path, want := range map[string]int{
		"/":              200,
		"/live":          200,
		"/sprint":        200,
		"/setup":         200,
		"/assets/app.js": 200,
		"/assets/no.js":  404,
	} {
		resp := do(t, s, "GET", path, nil, "")
		if resp.StatusCode != want {
			t.Errorf("%s = %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestSessionTokensExpireAndAreBounded(t *testing.T) {
	s := newSessionTokens()
	at := time.Now()
	s.now = func() time.Time { return at }
	tok, err := s.issue()
	if err != nil {
		t.Fatal(err)
	}
	if !s.valid(tok) {
		t.Fatal("a fresh token is invalid")
	}
	if s.valid("") || s.valid("not-a-token") {
		t.Error("an unknown token was accepted")
	}
	at = at.Add(sessionTTL + time.Minute)
	if s.valid(tok) {
		t.Error("an expired token is still valid")
	}

	at = time.Now()
	for range maxSessions + 4 {
		if _, err := s.issue(); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	n := len(s.entries)
	s.mu.Unlock()
	if n > maxSessions {
		t.Errorf("entries = %d, want at most %d", n, maxSessions)
	}
}

func TestLiveStoreSnapshotSurvivesACorruptDocument(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "live")
	store, err := livestore.OpenAt(dir, testRepo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(livestore.Record{
		SessionID: "claude:ok", Harness: "claude-code", Kind: livestore.KindTool,
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAt(filepath.Join(dir, "corrupt.json"), "{not json"); err != nil {
		t.Fatal(err)
	}
	docs, err := livestore.LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(docs) != 1 {
		t.Errorf("docs = %d, want the one good document", len(docs))
	}
}

// writeFileAt is a test helper: os.WriteFile with the directory already there.
func writeFileAt(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}
