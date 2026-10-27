//go:build client && !server

package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/recipe"
)

// The executor runs the approved recipe inside a prepared sandbox.
//
// It exists so the sandbox never improvises: the commands come from the stored
// recipe, they are sent as argv arrays rather than shell strings, and each one
// produces a receipt with its exact exit code. A step with no time left on the
// lease is a failure, never a silent skip, and a missing check is reported as
// missing rather than as a pass.

// leaseMargin is how much of the lease is kept back from a command's own
// deadline, so a step cannot run into expiry and lose its receipt.
const leaseMargin = 30 * time.Second

// maxExecBody bounds one command response.
const maxExecBody = 4 << 20

// Executor drives one sandbox through the provider's control API.
type Executor struct {
	// Provider issues the lease; the executor only runs commands in it.
	Provider Provider
	HTTP     *http.Client
	BaseURL  string
	APIKey   string
}

func (e *Executor) client() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (e *Executor) baseURL() string {
	if strings.TrimSpace(e.BaseURL) == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(e.BaseURL, "/")
}

// do performs one control request. The key rides only in the header, and a
// failure names the path and status: a provider error body can echo scope
// information, so it is never copied into the message.
func (e *Executor) do(ctx context.Context, method, urlPath string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("sandbox exec: encode %s: %w", urlPath, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.baseURL()+urlPath, reader)
	if err != nil {
		return fmt.Errorf("sandbox exec: build %s: %w", urlPath, err)
	}
	if e.APIKey != "" {
		req.Header.Set("X-API-Key", e.APIKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client().Do(req)
	if err != nil {
		return fmt.Errorf("sandbox exec: %s: %w", urlPath, err)
	}
	defer drainClose(resp.Body)
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case resp.StatusCode >= 400:
		return fmt.Errorf("sandbox exec: %s answered %s", urlPath, resp.Status)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxExecBody)).Decode(out); err != nil {
		return fmt.Errorf("sandbox exec: decode %s: %w", urlPath, err)
	}
	return nil
}

// Upload writes an archive into the sandbox under dir. The directory must stay
// inside the sandbox root: a caller cannot place files outside it.
func (e *Executor) Upload(ctx context.Context, sandboxID string, archive []byte, dir string) error {
	if !validSandboxID(sandboxID) {
		return fmt.Errorf("sandbox exec: %q is not a sandbox id", sandboxID)
	}
	clean, derr := safeSandboxDir(dir)
	if derr != nil {
		return derr
	}
	if len(archive) > maxExecBody {
		return errors.New("sandbox exec: archive is larger than the transfer bound")
	}
	body := map[string]any{"dir": clean, "archive": archive}
	return e.do(ctx, http.MethodPost, "/v2/sandboxes/"+sandboxID+"/files", body, nil)
}

// safeSandboxDir accepts a path inside the sandbox. An empty value means the
// working directory the template starts in.
func safeSandboxDir(dir string) (string, error) {
	if dir == "" || dir == "." {
		return "/home/user", nil
	}
	if strings.Contains(dir, `\`) || path.IsAbs(dir) {
		return "", fmt.Errorf("sandbox exec: directory %q must be sandbox-relative", dir)
	}
	clean := path.Clean(dir)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("sandbox exec: directory %q escapes the sandbox", dir)
	}
	return "/home/user/" + clean, nil
}

// Run executes one recorded command and returns its receipt. deadline is the
// time the command may take; it is further bounded by the lease.
func (e *Executor) Run(ctx context.Context, sandboxID string, cmd recipe.Command, deadline time.Duration) (Receipt, error) {
	if !validSandboxID(sandboxID) {
		return Receipt{}, fmt.Errorf("sandbox exec: %q is not a sandbox id", sandboxID)
	}
	if len(cmd.Argv) == 0 {
		return Receipt{}, errors.New("sandbox exec: a command needs an argv")
	}
	if deadline <= 0 {
		return Receipt{}, fmt.Errorf("sandbox exec: %s has no time left on the lease", cmd.ID)
	}
	started := time.Now().UTC()
	body := map[string]any{
		// An argv array, never a shell string: the sandbox must not interpret
		// metacharacters a project put in an argument.
		"argv":    cmd.Argv,
		"cwd":     commandDir(cmd.Dir),
		"timeout": int(deadline / time.Second),
	}
	if len(cmd.Env) > 0 {
		body["env"] = cmd.Env
	}
	var out struct {
		ExitCode int `json:"exit_code"`
	}
	if err := e.do(ctx, http.MethodPost, "/v2/sandboxes/"+sandboxID+"/commands", body, &out); err != nil {
		return Receipt{}, err
	}
	return Receipt{
		Kind:      ReceiptKind(cmd.ID),
		Command:   strings.Join(cmd.Argv, " "),
		ExitCode:  out.ExitCode,
		StartedAt: started,
		EndedAt:   time.Now().UTC(),
	}, nil
}

// commandDir keeps a command's working directory inside the sandbox.
func commandDir(dir string) string {
	clean, err := safeSandboxDir(dir)
	if err != nil {
		return "/home/user"
	}
	return clean
}

// Ready runs the recorded readiness probe. An open port is not readiness: the
// probe's own exit code decides.
func (e *Executor) Ready(ctx context.Context, sandboxID string, probe []string, deadline time.Duration) (Receipt, error) {
	if len(probe) == 0 {
		return Receipt{}, errors.New("sandbox exec: readiness needs a recorded probe")
	}
	return e.Run(ctx, sandboxID, recipe.Command{ID: "readiness", Argv: probe, Dir: ".", TimeoutSeconds: int(deadline / time.Second)}, deadline)
}

// RunRecipe runs build, then test, then readiness. It stops at the first
// failure and returns every receipt it collected, so the window can show exactly
// how far the run got.
func (e *Executor) RunRecipe(ctx context.Context, sandboxID string, r recipe.Recipe, leaseExpiry, now time.Time) ([]Receipt, error) {
	if !validSandboxID(sandboxID) {
		return nil, fmt.Errorf("sandbox exec: %q is not a sandbox id", sandboxID)
	}
	remaining := leaseExpiry.Sub(now) - leaseMargin
	if remaining <= 0 {
		return nil, errors.New("sandbox exec: the lease has no time left for a command")
	}

	var receipts []Receipt
	steps := []recipe.Command{r.Build, r.Test}
	for _, step := range steps {
		if step.ID == "" {
			// Build and test are required; a recipe without one is a caller bug.
			return receipts, fmt.Errorf("sandbox exec: the recipe has no %q command", step.ID)
		}
		budget := stepBudget(step, remaining)
		receipt, err := e.Run(ctx, sandboxID, step, budget)
		if err != nil {
			return []Receipt{receipt}, err
		}
		receipts = append(receipts, receipt)
		if receipt.ExitCode != 0 {
			return receipts, fmt.Errorf("sandbox exec: %s exited %d", step.ID, receipt.ExitCode)
		}
	}

	if r.Run.ID == "" || len(r.ReadinessProbe) == 0 {
		// Build and test passed; the run step is not configured. That is a state
		// the caller reports, not a failure of the code.
		return receipts, nil
	}
	readiness, err := e.Ready(ctx, sandboxID, r.ReadinessProbe, remaining)
	if err != nil {
		return append(receipts, readiness), err
	}
	receipts = append(receipts, readiness)
	if readiness.ExitCode != 0 {
		return receipts, fmt.Errorf("sandbox exec: readiness exited %d", readiness.ExitCode)
	}
	return receipts, nil
}

// stepBudget bounds one step by its own timeout and by what the lease has left.
func stepBudget(step recipe.Command, remaining time.Duration) time.Duration {
	budget := time.Duration(step.TimeoutSeconds) * time.Second
	if budget <= 0 || budget > remaining {
		return remaining
	}
	return budget
}
