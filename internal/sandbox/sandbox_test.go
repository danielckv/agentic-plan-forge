package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// providerServer is a test double of the documented sandbox REST lifecycle.
// It records the requests it saw and answers with canned responses.
type providerServer struct {
	mu sync.Mutex

	srv *httptest.Server

	createBodies []map[string]any
	createCount  int
	// existingLeases maps operation id -> lease. A create that arrives for a
	// known operation id answers 409, never a second lease: the provider
	// side of "duplicate operation ids do not create twice".
	existingLeases map[string]Lease

	createStatus  int
	killStatuses  []int
	killCount     int
	inspectStatus int
	inspectInfo   Info

	// failCreateWith makes the handler hang until the client's context is
	// done, producing the transport-level ambiguity a real network failure
	// produces.
	hangCreate bool
	// garbageCreate answers 200 with a truncated body.
	garbageCreate bool
	// sawAPIKey records the key header of the last request.
	sawAPIKey string
}

func newProviderServer(t *testing.T) *providerServer {
	t.Helper()
	ps := &providerServer{
		existingLeases: map[string]Lease{},
		createStatus:   http.StatusCreated,
		inspectStatus:  http.StatusOK,
	}
	ps.srv = httptest.NewServer(http.HandlerFunc(ps.serveHTTP))
	t.Cleanup(ps.srv.Close)
	return ps
}

func (ps *providerServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.sawAPIKey = r.Header.Get("X-API-Key")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v2/sandboxes":
		ps.serveCreate(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v2/sandboxes/"):
		ps.serveKill(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/operations/"):
		ps.serveInspectOperation(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/sandboxes/"):
		ps.serveInspect(w, r)
	default:
		http.Error(w, "unexpected route", http.StatusNotFound)
	}
}

func (ps *providerServer) serveCreate(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	ps.createBodies = append(ps.createBodies, body)
	ps.createCount++
	if ps.hangCreate {
		<-r.Context().Done()
		return
	}
	if ps.garbageCreate {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sandboxID": "sbx-half`))
		return
	}
	if ps.createStatus >= 400 {
		w.WriteHeader(ps.createStatus)
		return
	}
	opID := r.Header.Get("Idempotency-Key")
	if lease, ok := ps.existingLeases[opID]; ok {
		// Already issued: answering with a second lease would bill twice.
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(lease)
		return
	}
	lease := Lease{
		SandboxID:    "sbx-" + opID,
		OperationID:  opID,
		TrafficToken: "tok-" + opID,
		ExpiresAt:    time.Now().Add(60 * time.Minute).UTC(),
	}
	ps.existingLeases[opID] = lease
	w.WriteHeader(ps.createStatus)
	_ = json.NewEncoder(w).Encode(lease)
}

func (ps *providerServer) serveKill(w http.ResponseWriter, _ *http.Request) {
	ps.killCount++
	status := http.StatusNoContent
	if ps.killCount <= len(ps.killStatuses) {
		status = ps.killStatuses[ps.killCount-1]
	}
	w.WriteHeader(status)
}

func (ps *providerServer) serveInspectOperation(w http.ResponseWriter, r *http.Request) {
	opID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/operations/"), "/sandbox")
	lease, ok := ps.existingLeases[opID]
	if !ok {
		http.Error(w, "no such operation", http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(lease)
}

func (ps *providerServer) serveInspect(w http.ResponseWriter, _ *http.Request) {
	if ps.inspectStatus >= 400 {
		w.WriteHeader(ps.inspectStatus)
		return
	}
	_ = json.NewEncoder(w).Encode(ps.inspectInfo)
}

// provider builds an HTTPProvider pointed at the test server. The key is
// real-looking so the test can assert it never leaks, not that it parses.
func (ps *providerServer) provider() HTTPProvider {
	return HTTPProvider{BaseURL: ps.srv.URL, APIKey: "secret-key-0123456789", HTTP: ps.srv.Client()}
}

// validRequest is a create request the provider accepts.
func validRequest() CreateRequest {
	return CreateRequest{
		Template:       "pf-go-node",
		TimeoutSeconds: 3600,
		VCPU:           2,
		MemoryMB:       4096,
		Metadata:       map[string]string{"approval": "a-1"},
		OperationID:    "op-1",
	}
}

// Create must send the private-traffic flag, the recorded bounds and the
// operation identity; the key must ride only in the header.
func TestCreateSendsPrivateTrafficFlagAndKey(t *testing.T) {
	ps := newProviderServer(t)
	p := ps.provider()

	lease, err := p.Create(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if lease.SandboxID != "sbx-op-1" {
		t.Fatalf("sandbox id = %q, want %q", lease.SandboxID, "sbx-op-1")
	}
	if lease.OperationID != "op-1" {
		t.Fatalf("operation id = %q, want %q", lease.OperationID, "op-1")
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.createCount != 1 {
		t.Fatalf("create count = %d, want 1", ps.createCount)
	}
	body := ps.createBodies[0]
	network, ok := body["network"].(map[string]any)
	if !ok {
		t.Fatalf("request body carries no network object: %v", body)
	}
	allow, ok := network["allowPublicTraffic"].(bool)
	if !ok || allow {
		t.Fatalf("allowPublicTraffic = %v, want false on the wire", network["allowPublicTraffic"])
	}
	if body["template"] != "pf-go-node" {
		t.Fatalf("template = %v, want pf-go-node", body["template"])
	}
	if body["timeout"] != float64(3600) {
		t.Fatalf("timeout = %v, want 3600", body["timeout"])
	}
	if body["vcpu"] != float64(2) || body["memoryMB"] != float64(4096) {
		t.Fatalf("resources = vcpu %v memoryMB %v, want 2 / 4096", body["vcpu"], body["memoryMB"])
	}
	if ps.sawAPIKey != "secret-key-0123456789" {
		t.Fatalf("API key header = %q, want the configured key", ps.sawAPIKey)
	}
}

// An explicit opt-in must reach the wire; the flag is the request's, not a
// provider default the caller can be surprised by.
func TestCreateSendsExplicitPublicTrafficOptIn(t *testing.T) {
	ps := newProviderServer(t)
	p := ps.provider()
	req := validRequest()
	req.AllowPublicTraffic = true

	if _, err := p.Create(context.Background(), req); err != nil {
		t.Fatalf("create: %v", err)
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	network := ps.createBodies[0]["network"].(map[string]any)
	if network["allowPublicTraffic"] != true {
		t.Fatalf("allowPublicTraffic = %v, want true when the caller opted in", network["allowPublicTraffic"])
	}
}

// Timeouts outside 60..3600 are refused before anything leaves the process.
func TestCreateRejectsTimeoutsOutOfBounds(t *testing.T) {
	ps := newProviderServer(t)
	p := ps.provider()

	for _, timeout := range []int{0, -60, 59, 3601, 100000} {
		req := validRequest()
		req.TimeoutSeconds = timeout
		lease, err := p.Create(context.Background(), req)
		if err == nil {
			t.Fatalf("timeout %d: create succeeded, want rejection", timeout)
		}
		if !strings.Contains(err.Error(), "outside [60, 3600]") {
			t.Fatalf("timeout %d: error %q does not state the bound", timeout, err)
		}
		if lease != (Lease{}) {
			t.Fatalf("timeout %d: lease %v returned alongside error", timeout, lease)
		}
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.createCount != 0 {
		t.Fatalf("create count = %d after rejected requests, want 0", ps.createCount)
	}
}

// Non-https endpoints would carry the API key in the clear and are refused.
func TestCreateRejectsNonHTTPSBaseURL(t *testing.T) {
	for _, baseURL := range []string{"http://api.e2b.dev", "ftp://api.e2b.dev", "", "not a url://x"} {
		p := HTTPProvider{BaseURL: baseURL, APIKey: "k"}
		_, err := p.Create(context.Background(), validRequest())
		if err == nil {
			t.Fatalf("BaseURL %q: create succeeded, want rejection", baseURL)
		}
		if !strings.Contains(err.Error(), "sandbox provider") {
			t.Fatalf("BaseURL %q: error %q does not name the provider config", baseURL, err)
		}
	}
}

// Plain HTTP is tolerated only on the local machine. A remote host carries the
// API key over the network, so it stays HTTPS only even when the scheme looks
// deliberate.
func TestPlainHTTPIsAllowedOnlyOnLoopback(t *testing.T) {
	for _, baseURL := range []string{
		"http://127.0.0.1:9999", "http://localhost:9999", "http://[::1]:9999",
	} {
		if !isLoopbackHost(mustHost(t, baseURL)) {
			t.Errorf("%q was not recognised as loopback", baseURL)
		}
	}
	for _, baseURL := range []string{
		"http://api.e2b.dev", "http://192.168.0.10:8080", "http://10.0.0.1", "http://provider.e2b.dev",
	} {
		p := HTTPProvider{BaseURL: baseURL, APIKey: "k"}
		if _, err := p.Create(context.Background(), validRequest()); err == nil {
			t.Errorf("%q: create succeeded over plain HTTP", baseURL)
		}
	}
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Hostname()
}

// A transport failure whose outcome cannot be seen is ErrAmbiguous, and the
// caller reconciles by operation ID instead of creating again.
func TestAmbiguousTransportFailureReturnsErrAmbiguous(t *testing.T) {
	ps := newProviderServer(t)
	ps.hangCreate = true
	p := ps.provider()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := p.Create(ctx, validRequest())
	if err == nil {
		t.Fatal("create on a hung server succeeded, want error")
	}
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("error = %v, want it to wrap ErrAmbiguous", err)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("error %q leaks the API key", err)
	}

	// The caller's own cancellation is NOT ambiguity: nothing was cancelled
	// mid-flight on the wire that the caller did not itself cancel.
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	_, err2 := p.Create(ctx2, validRequest())
	if err2 == nil {
		t.Fatal("create on cancelled context succeeded, want error")
	}
	if errors.Is(err2, ErrAmbiguous) {
		t.Fatalf("caller-cancelled create reported %v, want plain cancellation", err2)
	}
}

// Only a context that was already finished can prove nothing was sent. A
// deadline that fires while the request is in flight may have created a paid
// sandbox, so it stays ambiguous and the caller reconciles by operation id.
func TestDeadlineInFlightIsAmbiguousButDeadBeforeSendIsNot(t *testing.T) {
	ps := newProviderServer(t)
	p := ps.provider()

	// Already finished: no request leaves, so nothing can have been created.
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Create(dead, validRequest()); err == nil || errors.Is(err, ErrAmbiguous) {
		t.Fatalf("error = %v, want a plain cancellation", err)
	}
	ps.mu.Lock()
	sent := ps.createCount
	ps.mu.Unlock()
	if sent != 0 {
		t.Fatalf("%d requests reached the provider after a dead context", sent)
	}

	// Hangs until the deadline: the request did leave, so this is ambiguous.
	ps.hangCreate = true
	ctx, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if _, err := p.Create(ctx, validRequest()); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("error = %v, want ErrAmbiguous", err)
	}
}

// A truncated 200 body is ambiguous too: the sandbox may exist while the
// answer is unreadable.
func TestTruncatedCreateResponseIsAmbiguous(t *testing.T) {
	ps := newProviderServer(t)
	ps.garbageCreate = true
	p := ps.provider()

	_, err := p.Create(context.Background(), validRequest())
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("error = %v, want ErrAmbiguous on truncated body", err)
	}
}

// A duplicate create for a known operation id resolves to the original
// lease: one click, one paid sandbox.
func TestDuplicateOperationIDDoesNotCreateTwice(t *testing.T) {
	ps := newProviderServer(t)
	p := ps.provider()

	first, err := p.Create(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := p.Create(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("duplicate create: %v", err)
	}
	if first.SandboxID != second.SandboxID {
		t.Fatalf("duplicate created sandbox %q beside %q", second.SandboxID, first.SandboxID)
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.existingLeases) != 1 {
		t.Fatalf("provider holds %d leases, want 1", len(ps.existingLeases))
	}
	// The client-side path: 409 -> InspectOperation, no second create body.
	if len(ps.createBodies) != 2 {
		t.Fatalf("create bodies = %d, want 2 (one original, one conflict probe)", len(ps.createBodies))
	}
}

// Reconciliation by operation id finds the lease an ambiguous create left
// behind, or reports its absence without ambiguity.
func TestInspectOperationRecoversLease(t *testing.T) {
	ps := newProviderServer(t)
	p := ps.provider()

	if _, err := p.InspectOperation(context.Background(), "never-created"); !errors.Is(err, ErrSandboxNotFound) {
		t.Fatalf("error = %v, want ErrSandboxNotFound for unknown operation", err)
	}

	ps.mu.Lock()
	ps.existingLeases["op-9"] = Lease{SandboxID: "sbx-9", OperationID: "op-9", ExpiresAt: time.Now().Add(time.Hour)}
	ps.mu.Unlock()

	lease, err := p.InspectOperation(context.Background(), "op-9")
	if err != nil {
		t.Fatalf("inspect operation: %v", err)
	}
	if lease.SandboxID != "sbx-9" || lease.OperationID != "op-9" {
		t.Fatalf("lease = %+v, want sandbox sbx-9 / operation op-9", lease)
	}
	if _, err := p.InspectOperation(context.Background(), ""); err == nil {
		t.Fatal("empty operation id accepted, want rejection")
	}
}

// Kill succeeds twice in a row: the second sandbox is already absent.
func TestKillIsIdempotent(t *testing.T) {
	ps := newProviderServer(t)
	ps.killStatuses = []int{http.StatusNoContent, http.StatusNotFound}
	p := ps.provider()

	if err := p.Kill(context.Background(), "sbx-1"); err != nil {
		t.Fatalf("first kill: %v", err)
	}
	if err := p.Kill(context.Background(), "sbx-1"); err != nil {
		t.Fatalf("second kill on absent sandbox: %v, want idempotent success", err)
	}
	if err := p.Kill(context.Background(), ""); err == nil {
		t.Fatal("empty sandbox id accepted, want rejection")
	}
}

// Inspect reports the provider's view and distinguishes absence from error.
func TestInspectMapsProviderResponses(t *testing.T) {
	ps := newProviderServer(t)
	p := ps.provider()
	ps.inspectInfo = Info{SandboxID: "sbx-1", State: "running", ExpiresAt: time.Now().Add(time.Minute)}

	info, err := p.Inspect(context.Background(), "sbx-1")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if info.SandboxID != "sbx-1" || info.State != "running" {
		t.Fatalf("info = %+v, want sandbox sbx-1 running", info)
	}

	ps.inspectStatus = http.StatusNotFound
	if _, err := p.Inspect(context.Background(), "sbx-1"); !errors.Is(err, ErrSandboxNotFound) {
		t.Fatalf("error = %v, want ErrSandboxNotFound", err)
	}

	ps.inspectStatus = http.StatusInternalServerError
	if _, err := p.Inspect(context.Background(), "sbx-1"); err == nil || errors.Is(err, ErrSandboxNotFound) {
		t.Fatalf("error = %v, want a plain provider error for 500", err)
	}
}

// PreviewURL accepts only the provider's own https preview hosts.
func TestPreviewURLRejectsAttackerHostsAndSchemes(t *testing.T) {
	p := HTTPProvider{BaseURL: "https://api.e2b.dev", APIKey: "k"}

	urlStr, err := p.PreviewURL("sbx-abc123", 3000)
	if err != nil {
		t.Fatalf("preview url: %v", err)
	}
	if !strings.HasPrefix(urlStr, "https://sbx-abc123-3000.e2b.app") {
		t.Fatalf("preview url = %q, want a provider host", urlStr)
	}

	for _, tc := range []struct {
		sandboxID string
		port      int
		reason    string
	}{
		{"evil.com#x", 3000, "fragment injection"},
		{"../../attacker", 3000, "path traversal"},
		{"sbx with space", 3000, "whitespace"},
		{"", 3000, "empty id"},
		{"sbx-1", 0, "zero port"},
		{"sbx-1", -1, "negative port"},
		{"sbx-1", 65536, "oversized port"},
	} {
		if _, err := p.PreviewURL(tc.sandboxID, tc.port); err == nil {
			t.Fatalf("%s: PreviewURL(%q, %d) succeeded, want rejection", tc.reason, tc.sandboxID, tc.port)
		}
	}
}

// The API key must never appear in any error the provider returns, whatever
// the server answers.
func TestErrorsNeverLeakTheAPIKey(t *testing.T) {
	ps := newProviderServer(t)
	p := ps.provider()
	ps.createStatus = http.StatusInternalServerError
	ps.inspectStatus = http.StatusInternalServerError
	ps.killStatuses = []int{http.StatusInternalServerError}

	req := validRequest()
	req.OperationID = ""
	if _, err := p.Create(context.Background(), req); err != nil && strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("create error leaks the key: %v", err)
	}
	req = validRequest()
	if _, err := p.Create(context.Background(), req); err != nil && strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("create 500 error leaks the key: %v", err)
	}
	if err := p.Kill(context.Background(), "sbx-1"); err != nil && strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("kill error leaks the key: %v", err)
	}
	if _, err := p.Inspect(context.Background(), "sbx-1"); err != nil && strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("inspect error leaks the key: %v", err)
	}
}

// A response larger than the bounded reader must not be trusted past the
// decode: the provider cannot force unbounded reads.
func TestOversizedProviderResponseIsRejected(t *testing.T) {
	ps := newProviderServer(t)
	p := ps.provider()
	ps.mu.Lock()
	ps.createStatus = http.StatusCreated
	ps.mu.Unlock()
	// Replace the handler with one that streams far past the bound.
	huge := newLeaseFlood(maxBodyBytes * 4)
	ps.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(huge)
	})

	_, err := p.Create(context.Background(), validRequest())
	if err == nil {
		t.Fatal("create on a flooding server succeeded, want error")
	}
}

// newLeaseFlood builds a JSON lease padded with junk to size bytes.
func newLeaseFlood(size int) []byte {
	lease := Lease{SandboxID: "sbx-flood", OperationID: "op-flood"}
	base, _ := json.Marshal(lease)
	pad := make([]byte, size)
	for i := range pad {
		pad[i] = 'x'
	}
	return []byte(fmt.Sprintf("%s%s", base, pad))
}
