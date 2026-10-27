//go:build client && !server

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sync"

	"github.com/danielckv/agentic-plan-forge/internal/recipe"
)

// execServer is a control-plane double: it records the requests it saw and
// answers with the codes a real provider can return.
type execServer struct {
	srv *httptest.Server
	mu  sync.Mutex
	// requests, paths and auth record what the provider saw.
	requests []map[string]any
	paths    []string
	auth     []string
	status   int
	body     string
}

func newExecServer(t *testing.T) *execServer {
	t.Helper()
	es := &execServer{status: http.StatusOK, body: `{"exit_code":0}`}
	es.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var decoded map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&decoded)
		}
		es.mu.Lock()
		es.paths = append(es.paths, r.URL.Path)
		es.auth = append(es.auth, r.Header.Get("X-API-Key"))
		es.requests = append(es.requests, decoded)
		code, body := es.status, es.body
		es.mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(es.srv.Close)
	return es
}

func (es *execServer) executor() *Executor {
	return &Executor{BaseURL: es.srv.URL, APIKey: "secret-key-xyz", HTTP: es.srv.Client()}
}

// A command is sent as an argv array, never a shell string, so a project's own
// arguments cannot be reinterpreted by a shell inside the sandbox.
func TestRunSendsAnArgvArray(t *testing.T) {
	es := newExecServer(t)
	e := es.executor()
	cmd := recipe.Command{ID: "build", Argv: []string{"go", "build", "./...; rm -rf /"}, Dir: "web", TimeoutSeconds: 120}

	receipt, err := e.Run(context.Background(), "sb-1", cmd, time.Minute)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if receipt.Kind != ReceiptBuild {
		t.Errorf("kind = %q, want build", receipt.Kind)
	}
	es.mu.Lock()
	sent := es.requests[0]
	argv, ok := sent["argv"].([]any)
	if !ok {
		t.Fatalf("argv was not an array: %#v", sent["argv"])
	}
	if len(argv) != 3 {
		t.Errorf("argv = %v, want three separate arguments", argv)
	}
	if cwd, _ := sent["cwd"].(string); !strings.HasPrefix(cwd, "/home/user/") {
		t.Errorf("cwd = %q, want a sandbox-relative working directory", cwd)
	}
	auth := es.auth[0]
	es.mu.Unlock()
	if auth != "secret-key-xyz" {
		t.Errorf("the key did not travel in the header")
	}
}

// The command's own timeout is clamped by the lease, so a step cannot outlive
// the sandbox and lose its receipt.
func TestRunClampsToTheLease(t *testing.T) {
	es := newExecServer(t)
	e := es.executor()
	cmd := recipe.Command{ID: "test", Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 1800}

	if _, err := e.Run(context.Background(), "sb-1", cmd, 90*time.Second); err != nil {
		t.Fatalf("Run: %v", err)
	}
	es.mu.Lock()
	got, _ := es.requests[0]["timeout"].(float64)
	es.mu.Unlock()
	if int(got) != 90 {
		t.Errorf("timeout = %v, want the remaining lease (90)", got)
	}
}

// A step with no time left fails without contacting the provider: a sandbox
// that is about to expire must not start work.
func TestRunWithoutTimeLeftFailsBeforeARequest(t *testing.T) {
	es := newExecServer(t)
	e := es.executor()
	if _, err := e.Run(context.Background(), "sb-1", recipe.Command{ID: "build", Argv: []string{"go", "build"}}, 0); err == nil {
		t.Fatal("a step with no time left was started")
	}
	es.mu.Lock()
	defer es.mu.Unlock()
	if len(es.requests) != 0 {
		t.Error("a step with no time left reached the provider")
	}
}

// A rejected credential is the package's own sentinel, and the error never
// echoes the key.
func TestRunReportsUnauthorizedWithoutTheKey(t *testing.T) {
	es := newExecServer(t)
	es.status = http.StatusUnauthorized
	e := es.executor()
	_, err := e.Run(context.Background(), "sb-1", recipe.Command{ID: "build", Argv: []string{"go", "build"}}, time.Minute)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), "secret-key-xyz") {
		t.Fatalf("the error leaked the key: %v", err)
	}
}

// A failing build stops the sequence before the tests run: paying for a test
// suite on a tree that does not compile is waste, and the receipt says why.
func TestRunRecipeStopsAfterAFailingBuild(t *testing.T) {
	es := newExecServer(t)
	es.body = `{"exit_code":2}`
	e := es.executor()
	r := recipe.Recipe{
		SchemaVersion: recipe.SchemaVersion,
		Build:         recipe.Command{ID: "build", Argv: []string{"go", "build", "./..."}, TimeoutSeconds: 600},
		Test:          recipe.Command{ID: "test", Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 600},
	}
	receipts, err := e.RunRecipe(context.Background(), "sb-1", testSource(), r, time.Now().Add(time.Hour), time.Now())
	if err == nil {
		t.Fatal("a failing build reported success")
	}
	if len(receipts) != 1 || receipts[0].Kind != ReceiptBuild || receipts[0].ExitCode != 2 {
		t.Fatalf("receipts = %+v, want the build's own exit code", receipts)
	}
	es.mu.Lock()
	defer es.mu.Unlock()
	// The upload is a request too, so count the command endpoint rather than
	// every path: one build ran and the test suite never started.
	commands := 0
	for _, path := range es.paths {
		if strings.HasSuffix(path, "/commands") {
			commands++
		}
	}
	if commands != 1 {
		t.Errorf("%d commands ran, want only the build: %v", commands, es.paths)
	}
}

// Readiness is required only when a run command exists, and its receipt is part
// of the evidence.
func TestRunRecipeRequiresReadinessOnlyWithARunStep(t *testing.T) {
	es := newExecServer(t)
	e := es.executor()
	base := recipe.Recipe{
		SchemaVersion: recipe.SchemaVersion,
		Build:         recipe.Command{ID: "build", Argv: []string{"go", "build", "./..."}, TimeoutSeconds: 60},
		Test:          recipe.Command{ID: "test", Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 60},
	}
	receipts, err := e.RunRecipe(context.Background(), "sb-1", testSource(), base, time.Now().Add(time.Hour), time.Now())
	if err != nil {
		t.Fatalf("RunRecipe without a run step: %v", err)
	}
	if len(receipts) != 2 {
		t.Errorf("receipts = %d, want build and test only", len(receipts))
	}

	withRun := base
	withRun.Run = recipe.Command{ID: "run", Argv: []string{"npm", "run", "start"}, TimeoutSeconds: 60}
	withRun.ReadinessProbe = []string{"curl", "-fsS", "http://127.0.0.1:8080"}
	receipts, err = e.RunRecipe(context.Background(), "sb-1", testSource(), withRun, time.Now().Add(time.Hour), time.Now())
	if err != nil {
		t.Fatalf("RunRecipe with a run step: %v", err)
	}
	if len(receipts) != 3 || receipts[2].Kind != ReceiptReadiness {
		t.Fatalf("receipts = %+v, want a readiness receipt", receipts)
	}
}

// An expired lease is reported before any command runs.
func TestRunRecipeRefusesAnExpiredLease(t *testing.T) {
	e := newExecServer(t).executor()
	now := time.Now()
	r := recipe.Recipe{Build: recipe.Command{ID: "build", Argv: []string{"go", "build"}, TimeoutSeconds: 60}}
	if _, err := e.RunRecipe(context.Background(), "sb-1", testSource(), r, now.Add(10*time.Second), now); err == nil {
		t.Fatal("an expired lease ran a command")
	}
}

// A directory outside the sandbox is refused, so a bundle cannot be written
// over the control plane's own paths.
func TestUploadRefusesADirectoryOutsideTheSandbox(t *testing.T) {
	e := newExecServer(t).executor()
	for _, dir := range []string{"/etc", "../escape", "a/../../b"} {
		if err := e.Upload(context.Background(), "sb-1", []byte("x"), dir); err == nil {
			t.Errorf("directory %q was accepted", dir)
		}
	}
}

// An oversized archive is refused before it is sent.
func TestUploadRefusesAnOversizedArchive(t *testing.T) {
	e := newExecServer(t).executor()
	big := make([]byte, maxExecBody+1)
	if err := e.Upload(context.Background(), "sb-1", big, ""); err == nil {
		t.Fatal("an oversized archive was sent")
	}
}
