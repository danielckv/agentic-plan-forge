//go:build client && !server

package relay

// The client side of the relay: REST calls, the authenticated WebSocket dial,
// the reconnecting session that speaks protocol.go, and the egress guard that
// keeps local paths off the wire.
//
// Credential discipline. The relay token is attached only to requests whose
// origin equals the relay origin this client was built for; an invite secret is
// sent only to the host named in the invite. Every client refuses redirects, so
// a relay (or a man in the middle) cannot bounce an Authorization header to
// another host. Secrets are passed as arguments and never logged.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// WSSubprotocol is the WebSocket subprotocol both ends must negotiate.
const WSSubprotocol = "plan-forge.v1"

// Client limits and reconnect timings.
const (
	clientQueueDepth = 256
	clientWriteLimit = 5 * time.Second
	httpTimeout      = 30 * time.Second
	maxErrorBody     = 512
	backoffMin       = 250 * time.Millisecond
	backoffMax       = 5 * time.Second
)

// Errors that stop reconnection: retrying cannot fix them.
var (
	ErrRelayAuth       = errors.New("the relay rejected the credential (invite expired or revoked, or you were removed)")
	ErrRoomClosed      = errors.New("the session is closed")
	ErrRoomFull        = errors.New("the session is full")
	ErrProtocolVersion = errors.New("the relay speaks a different protocol version; update Plan-Forge")
	ErrRemoved         = errors.New("you were removed from the session")
	// ErrNotConnected is returned by Send while the socket is down; the caller's
	// durable outbox retries after the state changes back to open.
	ErrNotConnected  = errors.New("not connected to the relay")
	ErrSendQueueFull = errors.New("the relay send queue is full")
	// ErrLocalPath is wrapped by EgressError.
	ErrLocalPath = errors.New("payload contains a local filesystem path")
)

// --- endpoint --------------------------------------------------------------------

// Endpoint is a validated relay origin.
type Endpoint struct {
	Scheme string // "https", or "http" for explicit loopback development
	Host   string // host or host:port
}

func isLoopbackHost(hostport string) bool {
	h := hostport
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		h = host
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

// ParseRelayURL validates a relay URL. HTTPS/WSS only, except plaintext to an
// explicit loopback host for development. Userinfo, query, fragment and any
// path beyond "/" are refused: the origin is all that is configured.
func ParseRelayURL(raw string) (Endpoint, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Endpoint{}, fmt.Errorf("relay URL is not valid: %w", err)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return Endpoint{}, errors.New("relay URL must be an origin without credentials, path, query or fragment")
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		return Endpoint{Scheme: "https", Host: u.Host}, nil
	case "http", "ws":
		if isLoopbackHost(u.Host) {
			return Endpoint{Scheme: "http", Host: u.Host}, nil
		}
		return Endpoint{}, errors.New("plaintext relay URLs are allowed only for loopback development; use https")
	}
	return Endpoint{}, fmt.Errorf("relay URL scheme %q is not https or wss", u.Scheme)
}

// Origin is scheme://host, the identity credentials are bound to.
func (e Endpoint) Origin() string { return e.Scheme + "://" + e.Host }

// URL builds an absolute HTTP URL under the origin.
func (e Endpoint) URL(path string) string { return e.Origin() + path }

// WSURL builds the WebSocket URL for path and a raw query.
func (e Endpoint) WSURL(path, rawQuery string) string {
	scheme := "wss"
	if e.Scheme == "http" {
		scheme = "ws"
	}
	u := scheme + "://" + e.Host + path
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	return u
}

var (
	sessionHexRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	secretHexRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// FormatInviteURL renders https://<relay-host>/join/<relay_session_id>#<secret>.
// The fragment never reaches the relay.
func FormatInviteURL(e Endpoint, sessionID, inviteSecret string) string {
	return e.URL("/join/"+sessionID) + "#" + inviteSecret
}

// ParseInviteURL splits an invite URL into the relay it names, the session and
// the invite secret.
func ParseInviteURL(raw string) (Endpoint, string, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Endpoint{}, "", "", errors.New("that is not a valid invite URL")
	}
	ep, err := ParseRelayURL(u.Scheme + "://" + u.Host)
	if err != nil {
		return Endpoint{}, "", "", err
	}
	id := strings.TrimPrefix(u.Path, "/join/")
	if u.User != nil || u.RawQuery != "" || !strings.HasPrefix(u.Path, "/join/") || !sessionHexRe.MatchString(id) || !secretHexRe.MatchString(u.Fragment) {
		return Endpoint{}, "", "", errors.New("invite URL must look like https://<relay>/join/<session>#<secret>")
	}
	return ep, id, u.Fragment, nil
}

// --- REST --------------------------------------------------------------------------

// NewHTTPClient returns the HTTP client for relay calls: TLS 1.2 minimum, a
// request timeout, and redirects refused so an Authorization header can never
// follow a redirect to another host.
func NewHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &http.Client{
		Timeout:       httpTimeout,
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// StatusError is a non-2xx relay answer.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("relay answered %d: %s", e.Status, e.Message)
}

// RelayClient talks to one relay origin.
type RelayClient struct {
	ep   Endpoint
	http *http.Client
}

// NewRelayClient builds a client for ep. A nil httpClient gets NewHTTPClient.
func NewRelayClient(ep Endpoint, httpClient *http.Client) *RelayClient {
	if httpClient == nil {
		httpClient = NewHTTPClient()
	} else {
		cp := *httpClient
		cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		httpClient = &cp
	}
	return &RelayClient{ep: ep, http: httpClient}
}

// Endpoint reports the origin this client is bound to.
func (c *RelayClient) Endpoint() Endpoint { return c.ep }

func (c *RelayClient) do(ctx context.Context, method, path, secret string, body io.Reader, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.ep.URL(path), body)
	if err != nil {
		return nil, err
	}
	if secret != "" {
		// Bind the credential to its origin even though URLs are built here:
		// the check is what keeps a future refactor from leaking a token.
		if req.URL.Scheme+"://"+req.URL.Host != c.ep.Origin() {
			return nil, errors.New("refusing to send a credential to a different origin")
		}
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	return c.http.Do(req)
}

func readStatusError(resp *http.Response) error {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return &StatusError{Status: resp.StatusCode, Message: strings.TrimSpace(string(msg))}
}

func (c *RelayClient) doJSON(ctx context.Context, method, path, secret string, in, out any, wantStatus ...int) error {
	var body io.Reader
	header := http.Header{}
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
		header.Set("Content-Type", "application/json")
	}
	resp, err := c.do(ctx, method, path, secret, body, header)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	ok := false
	for _, s := range wantStatus {
		ok = ok || resp.StatusCode == s
	}
	if !ok {
		return readStatusError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(out)
}

// InviteOptions is the invite body: role editor|viewer, max_uses 1-10,
// ttl_seconds up to 86400. Zero values take the relay's defaults.
type InviteOptions struct {
	Role       string `json:"role,omitempty"`
	MaxUses    int    `json:"max_uses,omitempty"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

// CreateSessionResult is the answer to POST /sessions.
type CreateSessionResult struct {
	RelaySessionID string `json:"relay_session_id"`
	OwnerSecret    string `json:"owner_secret"`
	InviteSecret   string `json:"invite_secret"`
}

// InviteResult is the answer to POST /sessions/{id}/invites.
type InviteResult struct {
	InviteID     string `json:"invite_id"`
	InviteSecret string `json:"invite_secret"`
}

// JoinResult is the answer to POST /sessions/{id}/join.
type JoinResult struct {
	MemberID     string `json:"member_id"`
	MemberSecret string `json:"member_secret"`
}

// CreateSession opens a room with the relay token and returns the owner secret
// and the first invite. invite may be nil for the relay's defaults.
func (c *RelayClient) CreateSession(ctx context.Context, relayToken string, invite *InviteOptions) (CreateSessionResult, error) {
	var in any
	if invite != nil {
		in = invite
	}
	var out CreateSessionResult
	err := c.doJSON(ctx, http.MethodPost, "/sessions", relayToken, in, &out, http.StatusCreated)
	return out, err
}

// CreateInvite mints another invite (owner secret).
func (c *RelayClient) CreateInvite(ctx context.Context, sessionID, ownerSecret string, opts InviteOptions) (InviteResult, error) {
	var out InviteResult
	err := c.doJSON(ctx, http.MethodPost, "/sessions/"+sessionID+"/invites", ownerSecret, opts, &out, http.StatusCreated)
	return out, err
}

// RevokeInvite revokes an invite (owner secret).
func (c *RelayClient) RevokeInvite(ctx context.Context, sessionID, ownerSecret, inviteID string) error {
	return c.doJSON(ctx, http.MethodDelete, "/sessions/"+sessionID+"/invites/"+url.PathEscape(inviteID),
		ownerSecret, nil, nil, http.StatusNoContent)
}

// Join exchanges an invite secret for a member credential. It sends only the
// invite secret, and only to this client's origin.
func (c *RelayClient) Join(ctx context.Context, sessionID, inviteSecret string) (JoinResult, error) {
	var out JoinResult
	err := c.doJSON(ctx, http.MethodPost, "/sessions/"+sessionID+"/join", inviteSecret, nil, &out, http.StatusCreated)
	return out, err
}

// PutArtifact uploads one approval artifact (owner secret, current lease epoch).
func (c *RelayClient) PutArtifact(ctx context.Context, sessionID, ownerSecret, approvalID, name string, epoch uint64, body []byte) error {
	header := http.Header{}
	header.Set("Content-Type", "application/octet-stream")
	header.Set("X-Plan-Forge-Epoch", fmt.Sprintf("%d", epoch))
	resp, err := c.do(ctx, http.MethodPut, "/sessions/"+sessionID+"/artifacts/"+approvalID+"/"+name,
		ownerSecret, bytes.NewReader(body), header)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return readStatusError(resp)
	}
	return nil
}

// GetArtifact downloads one artifact and returns its bytes with the relay's own
// SHA-256 header. Callers must still verify against PLAN_APPROVED.
func (c *RelayClient) GetArtifact(ctx context.Context, sessionID, secret, approvalID, name string) ([]byte, string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/sessions/"+sessionID+"/artifacts/"+approvalID+"/"+name, secret, nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", readStatusError(resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxFrameBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > MaxFrameBytes {
		return nil, "", errors.New("artifact exceeds 4 MiB")
	}
	return body, resp.Header.Get("X-Plan-Forge-SHA256"), nil
}

// Dial opens the authenticated WebSocket. The Authorization header carries the
// owner or member secret; no secret appears in the URL, and redirects are
// refused.
func (c *RelayClient) Dial(ctx context.Context, sessionID, secret string) (*websocket.Conn, error) {
	conn, resp, err := websocket.Dial(ctx, c.ep.WSURL("/ws", "session="+url.QueryEscape(sessionID)), &websocket.DialOptions{
		HTTPClient:      c.http,
		HTTPHeader:      http.Header{"Authorization": {"Bearer " + secret}},
		Subprotocols:    []string{WSSubprotocol},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if resp != nil {
			return nil, dialStatusError(resp)
		}
		return nil, err
	}
	if conn.Subprotocol() != WSSubprotocol {
		_ = conn.Close(websocket.StatusPolicyViolation, "subprotocol plan-forge.v1 required")
		return nil, ErrProtocolVersion
	}
	conn.SetReadLimit(MaxFrameBytes)
	return conn, nil
}

func dialStatusError(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrRelayAuth
	case http.StatusGone:
		return ErrRoomClosed
	case http.StatusConflict:
		return ErrRoomFull
	}
	return &StatusError{Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
}

// --- reconnecting session -----------------------------------------------------------

// ConnState is the relay connection's state, mirrored in the UI banner.
type ConnState string

// Connection states.
const (
	StateConnecting ConnState = "connecting"
	StateOpen       ConnState = "open"
	StateOffline    ConnState = "offline"
	StateClosed     ConnState = "closed"
)

// RelayConnConfig configures a RelayConn.
type RelayConnConfig struct {
	Client     *RelayClient
	SessionID  string
	Secret     string // owner or member secret
	CanExecute bool
	// Guard rejects envelopes that carry local paths. Strongly recommended.
	Guard *EgressGuard
	// DocVersion reports the local document version for HELLO.
	DocVersion func() uint64
	// OnEnvelope receives every decoded frame in authority order. It runs on
	// the reader goroutine and must not block.
	OnEnvelope func(WSEnvelope)
	OnState    func(ConnState, error)
	Logger     *slog.Logger
	Clock      func() time.Time
	// BackoffMin and BackoffMax override the 250 ms / 5 s reconnect bounds.
	BackoffMin, BackoffMax time.Duration
}

// RelayConn keeps one authenticated socket to the relay alive: it dials,
// says HELLO with the last control sequence it saw, delivers frames in order,
// asks for a replay when it detects a gap, and reconnects with jittered
// exponential backoff. Unconfirmed edits live in the caller's durable outbox;
// Send fails fast while the socket is down.
type RelayConn struct {
	cfg    RelayConnConfig
	logger *slog.Logger
	clock  func() time.Time

	mu          sync.Mutex
	out         chan []byte
	state       ConnState
	lastSeq     uint64 // highest control sequence applied
	replayFloor uint64 // frames at or below this are duplicates
	head        uint64 // snapshot head: frames in (floor, head] are replay
	epoch       uint64 // newest lease epoch seen on any envelope
	replayAsked bool
	opened      bool
}

// NewRelayConn validates cfg.
func NewRelayConn(cfg RelayConnConfig) (*RelayConn, error) {
	if cfg.Client == nil || cfg.SessionID == "" || cfg.Secret == "" || cfg.OnEnvelope == nil {
		return nil, errors.New("relay connection needs a client, session id, secret and envelope handler")
	}
	c := &RelayConn{cfg: cfg, logger: cfg.Logger, clock: cfg.Clock, state: StateConnecting}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	if c.clock == nil {
		c.clock = time.Now
	}
	if c.cfg.BackoffMin <= 0 {
		c.cfg.BackoffMin = backoffMin
	}
	if c.cfg.BackoffMax <= 0 {
		c.cfg.BackoffMax = backoffMax
	}
	return c, nil
}

// LastSeq is the highest control sequence applied so far.
func (c *RelayConn) LastSeq() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastSeq
}

// Epoch is the newest lease epoch observed. The executor stamps it on
// run events so a stale executor is fenced.
func (c *RelayConn) Epoch() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch
}

// State reports the current connection state.
func (c *RelayConn) State() ConnState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *RelayConn) setState(s ConnState, err error) {
	c.mu.Lock()
	changed := c.state != s
	c.state = s
	c.mu.Unlock()
	if changed && c.cfg.OnState != nil {
		c.cfg.OnState(s, err)
	}
}

// Backoff returns the delay before reconnect attempt n (0-based): 250 ms doubling
// to 5 s, with full jitter over the upper half so a room of clients that lost a
// relay together does not return in lockstep.
func Backoff(n int, lo, hi time.Duration) time.Duration {
	d := lo
	for range n {
		d *= 2
		if d >= hi {
			d = hi
			break
		}
	}
	half := d / 2
	jitter, err := rand.Int(rand.Reader, big.NewInt(int64(half)+1))
	if err != nil {
		return d
	}
	return half + time.Duration(jitter.Int64())
}

// Run connects and reconnects until ctx ends or an error that retrying cannot
// fix (auth failure, room closed, version mismatch, removal).
func (c *RelayConn) Run(ctx context.Context) error {
	attempt := 0
	for {
		c.setState(StateConnecting, nil)
		conn, err := c.cfg.Client.Dial(ctx, c.cfg.SessionID, c.cfg.Secret)
		if err == nil {
			err = c.session(ctx, conn, &attempt)
		}
		if ctx.Err() != nil {
			c.setState(StateClosed, ctx.Err())
			return ctx.Err()
		}
		if isFatal(err) {
			c.setState(StateClosed, err)
			return err
		}
		c.setState(StateOffline, err)
		t := time.NewTimer(Backoff(attempt, c.cfg.BackoffMin, c.cfg.BackoffMax))
		select {
		case <-ctx.Done():
			t.Stop()
			c.setState(StateClosed, ctx.Err())
			return ctx.Err()
		case <-t.C:
		}
		attempt++
	}
}

func isFatal(err error) bool {
	return errors.Is(err, ErrRelayAuth) || errors.Is(err, ErrRoomClosed) || errors.Is(err, ErrRoomFull) ||
		errors.Is(err, ErrProtocolVersion) || errors.Is(err, ErrRemoved)
}

// closeError maps the relay's close frame to a fatal or retryable error.
func closeError(err error) error {
	var ce websocket.CloseError
	if !errors.As(err, &ce) {
		return err
	}
	switch {
	case ce.Code == websocket.StatusPolicyViolation && ce.Reason == "UNSUPPORTED_VERSION":
		return ErrProtocolVersion
	case ce.Code == websocket.StatusPolicyViolation && strings.HasPrefix(ce.Reason, "removed"):
		return ErrRemoved
	case ce.Code == websocket.StatusPolicyViolation && strings.HasPrefix(ce.Reason, "session is closed"):
		return ErrRoomClosed
	case ce.Code == websocket.StatusNormalClosure && ce.Reason == "session cancelled":
		return ErrRoomClosed
	}
	return err
}

func (c *RelayConn) session(ctx context.Context, conn *websocket.Conn, attempt *int) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make(chan []byte, clientQueueDepth)
	c.mu.Lock()
	c.out = out
	c.opened = false
	c.replayAsked = false
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.out = nil
		c.mu.Unlock()
	}()

	go func() {
		for {
			select {
			case <-sctx.Done():
				return
			case raw := <-out:
				wctx, wcancel := context.WithTimeout(sctx, clientWriteLimit)
				err := conn.Write(wctx, websocket.MessageText, raw)
				wcancel()
				if err != nil {
					_ = conn.CloseNow()
					cancel()
					return
				}
			}
		}
	}()

	if err := c.queueControl(out, EventHello, HelloPayload{
		Proto: contracts.ProtocolVersion, LastSeq: c.LastSeq(), DocVersion: c.docVersion(), CanExecute: c.cfg.CanExecute,
	}); err != nil {
		_ = conn.CloseNow()
		return err
	}
	for {
		_, data, err := conn.Read(sctx)
		if err != nil {
			_ = conn.CloseNow()
			return closeError(err)
		}
		env, err := DecodeEnvelope(data)
		if err != nil {
			_ = conn.Close(websocket.StatusPolicyViolation, "invalid frame from relay")
			return fmt.Errorf("relay sent an invalid frame: %w", err)
		}
		if c.handle(env, out, attempt) {
			c.cfg.OnEnvelope(env)
		}
	}
}

func (c *RelayConn) docVersion() uint64 {
	if c.cfg.DocVersion == nil {
		return 0
	}
	return c.cfg.DocVersion()
}

// handle applies the sequencing rules and reports whether env is delivered.
//
// Control frames (seq > 0) are gapless. After a STATE_SNAPSHOT with head H, the
// replay that follows covers (floor, H] and may be sparse (sticky state after a
// long absence), so it is delivered as it comes; frames above H must arrive as
// lastSeq+1. A gap triggers one REPLAY_REQUEST; duplicates are dropped. Frames
// with seq 0 (document channel, replies, ephemeral slots) pass straight through.
func (c *RelayConn) handle(env WSEnvelope, out chan []byte, attempt *int) bool {
	c.mu.Lock()
	if env.Epoch > c.epoch {
		c.epoch = env.Epoch
	}
	if env.EventType == EventStateSnapshot {
		var snap StateSnapshotPayload
		if json.Unmarshal(env.Payload, &snap) == nil {
			c.replayFloor = c.lastSeq
			c.head = snap.HeadSeq
			if snap.HeadSeq > c.lastSeq {
				c.lastSeq = snap.HeadSeq
			}
		}
		c.replayAsked = false
		first := !c.opened
		c.opened = true
		c.mu.Unlock()
		if first {
			*attempt = 0
			c.setState(StateOpen, nil)
		}
		return true
	}
	if env.Seq == 0 {
		c.mu.Unlock()
		return true
	}
	var deliver, gap bool
	switch {
	case env.Seq <= c.replayFloor:
	case env.Seq <= c.head:
		deliver = true
	case env.Seq <= c.lastSeq:
	case env.Seq == c.lastSeq+1:
		c.lastSeq = env.Seq
		deliver = true
	default:
		gap = !c.replayAsked
		c.replayAsked = true
	}
	last := c.lastSeq
	c.mu.Unlock()
	if gap {
		_ = c.queueControl(out, EventReplayRequest, map[string]string{"last_seq": fmt.Sprintf("%d", last)})
	}
	return deliver
}

func (c *RelayConn) queueControl(out chan []byte, t WSEventType, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	frame, err := Encode(WSEnvelope{
		Version: contracts.ProtocolVersion, SessionID: c.cfg.SessionID, EventID: newClientUUID(),
		EventType: t, Timestamp: c.clock().UTC(), Payload: raw,
	})
	if err != nil {
		return err
	}
	select {
	case out <- frame:
		return nil
	default:
		return ErrSendQueueFull
	}
}

// Send submits one command. The egress guard runs first; the authority assigns
// sender, sequence and timestamp, so those are cleared here. The caller stamps
// env.Epoch for executor events (see Epoch).
func (c *RelayConn) Send(env WSEnvelope) error {
	if c.cfg.Guard != nil {
		if err := c.cfg.Guard.Check(env); err != nil {
			return err
		}
	}
	env.Version = contracts.ProtocolVersion
	env.SessionID = c.cfg.SessionID
	env.SenderID, env.Seq = "", 0
	if env.EventID == "" {
		env.EventID = newClientUUID()
	}
	if env.Timestamp.IsZero() {
		env.Timestamp = c.clock().UTC()
	}
	if len(env.Payload) == 0 {
		env.Payload = json.RawMessage(`{}`)
	}
	frame, err := Encode(env)
	if err != nil {
		return err
	}
	c.mu.Lock()
	out := c.out
	c.mu.Unlock()
	if out == nil {
		return ErrNotConnected
	}
	select {
	case out <- frame:
		return nil
	default:
		return ErrSendQueueFull
	}
}

func newClientUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UnixNano()&0xffffffffffff)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// --- egress guard ---------------------------------------------------------------------

// LocalPathPlaceholder replaces a local path inside public summaries and draft
// bodies.
const LocalPathPlaceholder = "<local-path>"

// EgressError reports which event carried a local path. It never includes the
// path itself.
type EgressError struct {
	EventType WSEventType
}

func (e *EgressError) Error() string {
	return fmt.Sprintf("refusing to send %s: it contains a local filesystem path", e.EventType)
}

func (e *EgressError) Unwrap() error { return ErrLocalPath }

// EgressGuard keeps this machine's filesystem layout off the relay. It matches
// the repository root, the home directory, the temp directory and Plan-Forge's
// home and cache, each as given and symlink-resolved, case-insensitively with
// "\" and "/" treated alike. Roots shorter than four characters are skipped (a
// bare "/" or "C:" would match everything). It inspects decoded string values,
// never raw JSON bytes, because Windows backslashes are escaped there.
//
// The document channel is exempt: it carries user-authored shared text, which
// the join screen warns about.
type EgressGuard struct {
	patterns []*regexp.Regexp
}

// DefaultEgressRoots lists the roots the plan names for a repository.
func DefaultEgressRoots(repoRoot string) []string {
	roots := []string{repoRoot, os.TempDir()}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, home)
	}
	if dir, err := config.Home(); err == nil {
		roots = append(roots, dir)
	}
	if dir, err := config.Cache(); err == nil {
		roots = append(roots, dir)
	}
	return roots
}

// NewEgressGuard compiles the roots into matchers.
func NewEgressGuard(roots ...string) *EgressGuard {
	seen := map[string]bool{}
	g := &EgressGuard{}
	add := func(p string) {
		norm := strings.ToLower(strings.ReplaceAll(filepath.Clean(p), "\\", "/"))
		norm = strings.TrimRight(norm, "/")
		if len(norm) < 4 || seen[norm] {
			return
		}
		seen[norm] = true
		parts := strings.Split(norm, "/")
		for i, part := range parts {
			parts[i] = regexp.QuoteMeta(part)
		}
		g.patterns = append(g.patterns, regexp.MustCompile(`(?i)`+strings.Join(parts, `[/\\]+`)))
	}
	for _, r := range roots {
		if r == "" {
			continue
		}
		add(r)
		if resolved, err := filepath.EvalSymlinks(r); err == nil {
			add(resolved)
		}
	}
	return g
}

// Contains reports whether s mentions a guarded root.
func (g *EgressGuard) Contains(s string) bool {
	for _, p := range g.patterns {
		if p.MatchString(s) {
			return true
		}
	}
	return false
}

// Redact replaces every guarded root in s with LocalPathPlaceholder. Use it on
// public summaries and non-synthesis draft bodies before their content hash is
// computed.
func (g *EgressGuard) Redact(s string) string {
	for _, p := range g.patterns {
		s = p.ReplaceAllLiteralString(s, LocalPathPlaceholder)
	}
	return s
}

// RedactJSON redacts every string value (and key) in a JSON document and
// reports whether anything changed. Numbers keep their exact text.
func (g *EgressGuard) RedactJSON(raw json.RawMessage) (json.RawMessage, bool, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, false, err
	}
	changed := false
	v = g.redactValue(v, &changed)
	if !changed {
		return raw, false, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, false, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), true, nil
}

func (g *EgressGuard) redactValue(v any, changed *bool) any {
	switch t := v.(type) {
	case string:
		if r := g.Redact(t); r != t {
			*changed = true
			return r
		}
	case []any:
		for i := range t {
			t[i] = g.redactValue(t[i], changed)
		}
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			nk := g.Redact(k)
			if nk != k {
				*changed = true
			}
			out[nk] = g.redactValue(val, changed)
		}
		return out
	}
	return v
}

func (g *EgressGuard) containsValue(v any) bool {
	switch t := v.(type) {
	case string:
		return g.Contains(t)
	case []any:
		for _, e := range t {
			if g.containsValue(e) {
				return true
			}
		}
	case map[string]any:
		for k, e := range t {
			if g.Contains(k) || g.containsValue(e) {
				return true
			}
		}
	}
	return false
}

// docChannelExempt lists the user-authored text events the guard lets through.
func docChannelExempt(t WSEventType) bool {
	switch t {
	case EventTextDelta, EventTextUpdates, EventTextSnapshot, EventTextCheck, EventTextAck, EventTextReject:
		return true
	}
	return false
}

// Check rejects a control event whose payload carries a local path. A guard
// with no roots, or a nil guard, allows everything.
func (g *EgressGuard) Check(env WSEnvelope) error {
	if g == nil || len(g.patterns) == 0 || docChannelExempt(env.EventType) || len(env.Payload) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(env.Payload, &v); err != nil {
		// A payload that does not decode cannot be inspected; refuse rather
		// than let an unreadable blob through.
		return fmt.Errorf("refusing to send %s: payload is not valid JSON: %w", env.EventType, err)
	}
	if g.containsValue(v) {
		return &EgressError{EventType: env.EventType}
	}
	return nil
}
