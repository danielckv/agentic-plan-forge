// Package sandbox holds the managed-sandbox provider adapter and lifecycle
// state machine for the prepared-VM build/test/preview flow. Planning never
// creates a sandbox; an owner-authorized launch does, and every lease it
// returns is reconciled by operation identity rather than retried blindly.
//
// The package imports the standard library only. It must never import host,
// delivery or UI code: the provider boundary stays a plain net/http client.
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PreviewDomainSuffix is the provider domain an authenticated preview URL
// must end with. E2B publishes previews only under its own hosts; anything
// else is an attacker URL, not a preview.
const PreviewDomainSuffix = ".e2b.app"

// Bounds on one sandbox lease. The plan fixes the default at 60 minutes and
// the provider cap at 3600 seconds; a shorter floor keeps a mistyped 0 from
// creating a sandbox that dies before the bundle transfers.
const (
	MinTimeoutSeconds = 60
	MaxTimeoutSeconds = 3600
)

// ErrAmbiguous reports a transport failure whose outcome is unknown: the
// provider may or may not have created a paid sandbox. The caller MUST
// reconcile by OperationID through InspectOperation before ever creating
// again.
var ErrAmbiguous = errors.New("sandbox create outcome unknown; reconcile by operation id")

// ErrSandboxNotFound reports that the provider knows the sandbox no longer
// exists. Kill treats it as success: removing an absent sandbox is done.
var ErrSandboxNotFound = errors.New("sandbox not found")

// ErrUnauthorized reports rejected or missing provider credentials. The key
// itself never appears in the error text.
var ErrUnauthorized = errors.New("sandbox provider rejected the credentials")

// Provider is the one managed-sandbox adapter in the codebase. Concrete
// implementations keep provider credentials server-side and outside the guest.
type Provider interface {
	Create(ctx context.Context, req CreateRequest) (Lease, error)
	Kill(ctx context.Context, sandboxID string) error
	Inspect(ctx context.Context, sandboxID string) (Info, error)
	PreviewURL(sandboxID string, port int) (string, error)
	// InspectOperation resolves an operation ID recorded before an ambiguous
	// create into the lease the provider actually issued, or
	// ErrSandboxNotFound when no sandbox was created under it.
	InspectOperation(ctx context.Context, operationID string) (Lease, error)
}

// CreateRequest describes one sandbox lease. Metadata carries the caller's
// reconciliation identity; the provider stores it opaquely.
type CreateRequest struct {
	Template           string
	TimeoutSeconds     int
	VCPU               int
	MemoryMB           int
	Metadata           map[string]string
	AllowPublicTraffic bool
	OperationID        string
}

// Lease is the provider's answer to a create.
type Lease struct {
	SandboxID    string
	OperationID  string
	TrafficToken string
	ExpiresAt    time.Time
}

// Info is the provider's view of a live sandbox.
type Info struct {
	SandboxID string
	State     string
	ExpiresAt time.Time
}

// State names a persisted lease lifecycle state.
type State string

// Lease lifecycle states.
const (
	StatePreparing State = "preparing"
	StateBuilding  State = "building"
	StateTesting   State = "testing"
	StateStarting  State = "starting"
	StateReady     State = "ready"
	StateFailed    State = "failed"
	StateStopping  State = "stopping"
	StateStopped   State = "stopped"
	StateExpired   State = "expired"
	StateUnknown   State = "unknown"
)

// Terminal reports whether the state ends the lease's life; a terminal state
// never runs commands again.
func (s State) Terminal() bool {
	switch s {
	case StateFailed, StateStopped, StateExpired:
		return true
	default:
		return false
	}
}

// Valid reports whether the string is one of the known states.
func (s State) Valid() bool {
	switch s {
	case StatePreparing, StateBuilding, StateTesting, StateStarting,
		StateReady, StateFailed, StateStopping, StateStopped,
		StateExpired, StateUnknown:
		return true
	default:
		return false
	}
}

// Normalize maps any raw persisted or provider string onto a known State.
// An unrecognized value becomes StateUnknown, never a fabricated progress
// state: an ambiguous truth must stay visibly ambiguous.
func Normalize(raw string) State {
	s := State(strings.ToLower(strings.TrimSpace(raw)))
	if s.Valid() {
		return s
	}
	return StateUnknown
}

// HTTPProvider talks to the documented sandbox REST lifecycle with a plain
// HTTP client. APIKey stays inside the provider: no method logs it, returns
// it, or embeds it in an error.
type HTTPProvider struct {
	BaseURL string // https only, e.g. https://api.e2b.dev
	APIKey  string
	HTTP    *http.Client
}

// Client returns the transport used for provider calls, falling back to the
// default client.
func (p HTTPProvider) Client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return http.DefaultClient
}

func (p HTTPProvider) validate() error {
	if p.BaseURL == "" {
		return errors.New("sandbox provider: BaseURL is required")
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return fmt.Errorf("sandbox provider: parse BaseURL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("sandbox provider: BaseURL must be https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("sandbox provider: BaseURL has no host")
	}
	if p.APIKey == "" {
		return errors.New("sandbox provider: APIKey is required")
	}
	return nil
}

// Create reserves one sandbox. The private-traffic flag travels in the
// request body rather than leaning on a provider default, so the wire always
// states the isolation the owner confirmed.
func (p HTTPProvider) Create(ctx context.Context, req CreateRequest) (Lease, error) {
	if err := p.validate(); err != nil {
		return Lease{}, err
	}
	if req.Template == "" {
		return Lease{}, errors.New("sandbox create: template is required")
	}
	if req.TimeoutSeconds < MinTimeoutSeconds || req.TimeoutSeconds > MaxTimeoutSeconds {
		return Lease{}, fmt.Errorf("sandbox create: timeout %d outside [%d, %d] seconds",
			req.TimeoutSeconds, MinTimeoutSeconds, MaxTimeoutSeconds)
	}
	if req.OperationID == "" {
		return Lease{}, errors.New("sandbox create: operation id is required for reconciliation")
	}
	body := map[string]any{
		"template": req.Template,
		"timeout":  req.TimeoutSeconds,
		"vcpu":     req.VCPU,
		"memoryMB": req.MemoryMB,
		"metadata": req.Metadata,
		"network":  map[string]bool{"allowPublicTraffic": req.AllowPublicTraffic},
	}
	return p.createOnce(ctx, req, body)
}

// createOnce posts the create and classifies the outcome. Any transport-level
// failure after the request may have been sent — timeout, reset connection,
// unreadable body — is ErrAmbiguous: the caller reconciles, it does not retry.
func (p HTTPProvider) createOnce(ctx context.Context, req CreateRequest, body map[string]any) (Lease, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return Lease{}, fmt.Errorf("sandbox create: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/v2/sandboxes", bytes.NewReader(payload))
	if err != nil {
		return Lease{}, fmt.Errorf("sandbox create: build request: %w", err)
	}
	p.authorize(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")
	// Reconciliation identity travels with the request, so a duplicate click
	// resolves to the first sandbox rather than a second paid one.
	httpReq.Header.Set("Idempotency-Key", req.OperationID)

	resp, err := p.Client().Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// The caller's own deadline fired: creating was cancelled, not
			// lost in flight.
			return Lease{}, fmt.Errorf("sandbox create: %w", ctxErr)
		}
		return Lease{}, fmt.Errorf("%w: %s", ErrAmbiguous, errText(err))
	}
	defer drainClose(resp.Body)

	switch {
	case resp.StatusCode == http.StatusConflict:
		// The provider already issued a lease for this operation ID. The
		// original one is authoritative; creating again would be a second
		// paid VM.
		return p.InspectOperation(ctx, req.OperationID)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Lease{}, ErrUnauthorized
	case resp.StatusCode >= 400:
		return Lease{}, fmt.Errorf("sandbox create: provider returned %s", resp.Status)
	}
	var lease Lease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&lease); err != nil {
		// A body that half-arrived is exactly the ambiguous case: the
		// sandbox may exist while the answer is unreadable.
		return Lease{}, fmt.Errorf("%w: decode create response: %s", ErrAmbiguous, errText(err))
	}
	if lease.SandboxID == "" {
		return Lease{}, fmt.Errorf("%w: create response carried no sandbox id", ErrAmbiguous)
	}
	if lease.OperationID == "" {
		lease.OperationID = req.OperationID
	}
	return lease, nil
}

// InspectOperation looks a previously issued operation ID up, so an
// ambiguous create recovers the lease it may have created.
func (p HTTPProvider) InspectOperation(ctx context.Context, operationID string) (Lease, error) {
	if err := p.validate(); err != nil {
		return Lease{}, err
	}
	if operationID == "" {
		return Lease{}, errors.New("sandbox inspect operation: operation id is required")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.BaseURL+"/v2/operations/"+url.PathEscape(operationID)+"/sandbox", nil)
	if err != nil {
		return Lease{}, fmt.Errorf("sandbox inspect operation: build request: %w", err)
	}
	p.authorize(httpReq)

	resp, err := p.Client().Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Lease{}, fmt.Errorf("sandbox inspect operation: %w", ctxErr)
		}
		// Still ambiguous: the operation may have created a sandbox.
		return Lease{}, fmt.Errorf("%w: %s", ErrAmbiguous, errText(err))
	}
	defer drainClose(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Lease{}, ErrSandboxNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return Lease{}, ErrUnauthorized
	default:
		return Lease{}, fmt.Errorf("sandbox inspect operation: provider returned %s", resp.Status)
	}
	var lease Lease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&lease); err != nil {
		return Lease{}, fmt.Errorf("sandbox inspect operation: decode response: %w", err)
	}
	if lease.SandboxID == "" {
		return Lease{}, errors.New("sandbox inspect operation: response carried no sandbox id")
	}
	if lease.OperationID == "" {
		lease.OperationID = operationID
	}
	return lease, nil
}

// Kill deletes the sandbox. A sandbox the provider no longer has is gone;
// that is the outcome Kill wanted, so it reports success.
func (p HTTPProvider) Kill(ctx context.Context, sandboxID string) error {
	if err := p.validate(); err != nil {
		return err
	}
	if sandboxID == "" {
		return errors.New("sandbox kill: sandbox id is required")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		p.BaseURL+"/v2/sandboxes/"+url.PathEscape(sandboxID), nil)
	if err != nil {
		return fmt.Errorf("sandbox kill: build request: %w", err)
	}
	p.authorize(httpReq)

	resp, err := p.Client().Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("sandbox kill: %w", ctxErr)
		}
		return fmt.Errorf("sandbox kill: %s", errText(err))
	}
	defer drainClose(resp.Body)

	switch {
	case resp.StatusCode == http.StatusOK, resp.StatusCode == http.StatusNoContent:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return nil // already absent: idempotent success
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	default:
		return fmt.Errorf("sandbox kill: provider returned %s", resp.Status)
	}
}

// Inspect reads the provider's current view of a sandbox.
func (p HTTPProvider) Inspect(ctx context.Context, sandboxID string) (Info, error) {
	if err := p.validate(); err != nil {
		return Info{}, err
	}
	if sandboxID == "" {
		return Info{}, errors.New("sandbox inspect: sandbox id is required")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.BaseURL+"/v2/sandboxes/"+url.PathEscape(sandboxID), nil)
	if err != nil {
		return Info{}, fmt.Errorf("sandbox inspect: build request: %w", err)
	}
	p.authorize(httpReq)

	resp, err := p.Client().Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Info{}, fmt.Errorf("sandbox inspect: %w", ctx.Err())
		}
		return Info{}, fmt.Errorf("sandbox inspect: %s", errText(err))
	}
	defer drainClose(resp.Body)

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound:
		return Info{}, ErrSandboxNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Info{}, ErrUnauthorized
	default:
		return Info{}, fmt.Errorf("sandbox inspect: provider returned %s", resp.Status)
	}
	var info Info
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&info); err != nil {
		return Info{}, fmt.Errorf("sandbox inspect: decode response: %w", err)
	}
	if info.SandboxID == "" {
		info.SandboxID = sandboxID
	}
	return info, nil
}

// PreviewURL builds the authenticated preview address for one port. Only
// https hosts under the provider's own domain qualify; anything else is a
// URL an attacker crafted, not a preview.
func (p HTTPProvider) PreviewURL(sandboxID string, port int) (string, error) {
	if sandboxID == "" {
		return "", errors.New("sandbox preview url: sandbox id is required")
	}
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("sandbox preview url: port %d outside [1, 65535]", port)
	}
	host := sandboxID + "-" + strconv.Itoa(port) + PreviewDomainSuffix
	// The sandbox id is untrusted input reaching a URL host. Accept only
	// [a-z0-9-] in it: fragments, slashes, whitespace, dots and everything
	// else an attacker needs to aim the URL elsewhere are gone.
	if !validSandboxID(sandboxID) {
		return "", fmt.Errorf("sandbox preview url: sandbox id %q is not a plain provider id", sandboxID)
	}
	if !strings.HasSuffix(host, PreviewDomainSuffix) {
		return "", fmt.Errorf("sandbox preview url: host %q is not a %q preview host", host, PreviewDomainSuffix)
	}
	u := url.URL{Scheme: "https", Host: host}
	if u.Host != host {
		return "", fmt.Errorf("sandbox preview url: host %q did not survive URL encoding", host)
	}
	return u.String(), nil
}

// validSandboxID reports whether the id is a plain provider identifier:
// ASCII letters, digits and hyphens, 1..128 long, not starting or ending
// with a hyphen.
func validSandboxID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	if id[0] == '-' || id[len(id)-1] == '-' {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}

// authorize attaches the provider credentials. The key exists only in the
// header: never in logs, errors or returned values.
func (p HTTPProvider) authorize(req *http.Request) {
	req.Header.Set("X-API-Key", p.APIKey)
}

// maxBodyBytes bounds one provider response. A hostile or broken provider
// cannot exhaust memory through a decode call.
const maxBodyBytes = 1 << 20

// drainClose reads the remainder and closes the body so the connection is
// reusable, ignoring the outcome: the response was already classified.
func drainClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxBodyBytes))
	_ = body.Close()
}

// errText flattens a transport error into bounded text. A hostile server or
// intermediary must not be able to pour unbounded bytes into window logs.
func errText(err error) string {
	msg := err.Error()
	if len(msg) > 256 {
		msg = msg[:256]
	}
	return msg
}
