//go:build client && !server

package relay

// The loopback bridge is the only network surface the Plan-Forge tab talks
// to. It binds 127.0.0.1 on an ephemeral port, serves the embedded UI, and
// carries the UI's WebSocket. The browser never sees a relay secret: the binary
// dials the relay itself (client_conn.go) and forwards.
//
// Bootstrap. The window is launched at http://127.0.0.1:<port>/#n=<nonce>. The
// nonce is 128 random bits, valid for 10 seconds and redeemable once; browser
// argv is visible to other local users, so the launch URL never carries a
// long-lived credential. The page POSTs the nonce to /api/session and receives
// a 256-bit session token in the JSON body, which it keeps in sessionStorage
// only (never the URL, a cookie or localStorage). Redeeming an expired or
// already-redeemed nonce aborts the session: the bridge reports a fatal error
// and the command exits 1 (fail closed).
//
// Wire contract with the UI:
//
//	POST /api/session   {"nonce":"<hex>"}            -> {"token":"<hex>"}
//	GET  /ws            first frame {"type":"auth","token":"<hex>"}
//	                    reply       {"type":"auth.ok"}; every later text frame
//	                    is an opaque application message in both directions.
//	POST /api/open-url  {"url":"https://..."}        -> 204 (Bearer token)
//	POST /api/upload    multipart/form-data files     -> handler JSON (Bearer)
//
// Every request must carry Host == 127.0.0.1:<port> (DNS-rebinding defense).
// The WebSocket upgrade and every non-GET request must also carry
// Origin == http://127.0.0.1:<port>. HTTP auth is `Authorization: Bearer`;
// WebSocket auth is the first message. Tokens are compared in constant time.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coder/websocket"
)

// Bridge limits.
const (
	bridgeNonceTTL     = 10 * time.Second
	bridgeAuthDeadline = 5 * time.Second
	bridgeWriteTimeout = 5 * time.Second
	bridgeQueueDepth   = UIQueueDepth
	bridgeTokenBytes   = 32
	bridgeNonceBytes   = 16

	// MaxUploadFileBytes and MaxUploadFiles bound one /api/upload request.
	MaxUploadFileBytes = 25 << 20
	MaxUploadFiles     = 5
	maxFormFieldBytes  = 1 << 20
	maxOpenURLBytes    = 2048
	maxUploadNameBytes = 255
)

// BridgeCSP is the Content-Security-Policy sent with every response: scripts
// and connections stay on the bridge's own origin, styles may be inline (the
// UI toolkit injects them), nothing may frame the page or submit forms.
const BridgeCSP = "default-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; " +
	"form-action 'none'; frame-ancestors 'none'"

// Bridge errors.
var (
	// ErrNonceReused and ErrNonceExpired abort the session.
	ErrNonceReused  = errors.New("launch nonce was already redeemed")
	ErrNonceExpired = errors.New("launch nonce expired")
	// ErrUINotConnected is returned by Send when no authenticated UI socket exists.
	ErrUINotConnected = errors.New("the window is not connected")
	// ErrUIBackpressure is returned by Send when the UI stopped reading.
	ErrUIBackpressure = errors.New("the window is not keeping up")
)

// UploadedFile is one file from /api/upload. Name is display text for
// extension routing only: it is never used in a filesystem path.
type UploadedFile struct {
	Name    string
	Size    int64
	Content *bytes.Reader
}

// UploadHandler parses uploaded files on this machine. The returned value is
// sent to the UI as JSON; an error becomes a 422 with its message.
type UploadHandler func(ctx context.Context, files []UploadedFile) (any, error)

// BridgeConfig configures a Bridge.
type BridgeConfig struct {
	// Assets is the embedded UI, rooted at its index.html. nil serves 404.
	Assets fs.FS
	Logger *slog.Logger
	Clock  func() time.Time
	// OnMessage receives each authenticated text frame from the UI. It runs on
	// the socket's reader goroutine and must not block.
	OnMessage func(raw []byte)
	// Upload handles /api/upload. nil answers 404.
	Upload UploadHandler
	// OpenURL opens a validated URL in the user's browser. nil uses the
	// platform opener (OpenURLArgv).
	OpenURL func(rawURL string) error
	// LaunchPath is the path the window opens at, for example "/board". It
	// must be "/" followed by lowercase letters, digits, '-' and '/'; anything
	// else (including empty) launches at "/".
	LaunchPath string
}

// Bridge is the loopback HTTP and WebSocket server for the Plan-Forge tab.
type Bridge struct {
	cfg    BridgeConfig
	logger *slog.Logger
	clock  func() time.Time

	ln     net.Listener
	srv    *http.Server
	mux    *http.ServeMux
	host   string // 127.0.0.1:<port>
	origin string // http://127.0.0.1:<port>

	events chan bool
	fatal  chan error

	mu           sync.Mutex
	nonceHash    [32]byte
	nonceSet     bool
	nonceExpires time.Time
	nonceUsed    bool
	tokenHash    [32]byte
	tokenSet     bool
	ui           *uiConn
}

// uiConn is the single authenticated UI socket.
type uiConn struct {
	conn    *websocket.Conn
	out     chan []byte
	ctx     context.Context
	cancel  context.CancelFunc
	once    sync.Once
	closing atomic.Bool
}

// close starts the teardown. It marks the socket closing first, because
// websocket.Close runs the closing handshake and only returns once the peer
// answers; a page that reconnects during that window must displace this socket
// rather than be refused with 409 for a connection that is already gone.
func (u *uiConn) close(code websocket.StatusCode, reason string) {
	u.once.Do(func() {
		u.closing.Store(true)
		go func() {
			_ = u.conn.Close(code, reason)
			u.cancel()
		}()
	})
}

// claimLocked takes the UI slot for conn, displacing a socket that is already
// tearing down. It reports whether the slot was free. b.mu must be held.
func (b *Bridge) claimLocked(conn *uiConn) bool {
	if b.ui != nil && b.ui.closing.Load() {
		b.ui = nil
	}
	if b.ui != nil {
		return false
	}
	b.ui = conn
	return true
}

// NewBridge binds 127.0.0.1:0 and prepares the routes. Call LaunchURL to mint
// the window's nonce and Serve to start answering.
func NewBridge(cfg BridgeConfig) (*Bridge, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("bind loopback bridge: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	b := &Bridge{
		cfg:    cfg,
		logger: cfg.Logger,
		clock:  cfg.Clock,
		ln:     ln,
		mux:    http.NewServeMux(),
		host:   fmt.Sprintf("127.0.0.1:%d", port),
		origin: fmt.Sprintf("http://127.0.0.1:%d", port),
		events: make(chan bool, 16),
		fatal:  make(chan error, 1),
	}
	if b.logger == nil {
		b.logger = slog.Default()
	}
	if b.clock == nil {
		b.clock = time.Now
	}
	b.mux.HandleFunc("POST /api/session", b.handleSession)
	b.mux.HandleFunc("GET /ws", b.handleWS)
	b.mux.HandleFunc("POST /api/open-url", b.authed(b.handleOpenURL))
	if cfg.Upload != nil {
		b.mux.HandleFunc("POST /api/upload", b.authed(b.handleUpload))
	}
	b.mux.HandleFunc("GET /", b.handleStatic)
	b.srv = &http.Server{
		Handler:           b.guard(b.mux),
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	return b, nil
}

// Addr is the loopback address the bridge listens on.
func (b *Bridge) Addr() string { return b.host }

// Origin is the exact Origin value the UI must send.
func (b *Bridge) Origin() string { return b.origin }

// Handle registers an application API route behind the bridge's Host, Origin
// and Bearer checks. Patterns follow net/http (for example "POST /api/state").
func (b *Bridge) Handle(pattern string, h http.HandlerFunc) {
	b.mux.HandleFunc(pattern, b.authed(h))
}

// UIEvents reports the UI socket's state: true when an authenticated socket
// opens, false when it closes. It is the primary input to window close
// detection (internal/window).
func (b *Bridge) UIEvents() <-chan bool { return b.events }

// Fatal receives the error that aborted the session (a reused or expired
// nonce). The command must exit 1 when it fires.
func (b *Bridge) Fatal() <-chan error { return b.fatal }

func (b *Bridge) emit(up bool) {
	select {
	case b.events <- up:
	default:
		// A full buffer means nobody supervises the window; dropping beats
		// blocking the socket goroutine.
	}
}

func (b *Bridge) abort(err error) {
	select {
	case b.fatal <- err:
	default:
	}
}

// LaunchURL mints a fresh single-use nonce and returns the URL to hand to the
// window. Minting again invalidates the previous nonce.
func (b *Bridge) LaunchURL() (string, error) {
	nonce, err := cryptoHex(bridgeNonceBytes)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	b.nonceHash = sha256.Sum256([]byte(nonce))
	b.nonceSet, b.nonceUsed = true, false
	b.nonceExpires = b.clock().Add(bridgeNonceTTL)
	b.mu.Unlock()
	return b.origin + launchPath(b.cfg.LaunchPath) + "#n=" + nonce, nil
}

// launchPath returns p when it is "/" plus [a-z0-9-/] and carries no "//";
// otherwise "/". The fragment that follows is the only credential-bearing part
// of the launch URL, so the path never needs escaping.
func launchPath(p string) string {
	if p == "" || p[0] != '/' || strings.Contains(p, "//") {
		return "/"
	}
	for i := 1; i < len(p); i++ {
		c := p[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '/' {
			return "/"
		}
	}
	return p
}

// Serve answers requests until ctx ends or the listener fails.
func (b *Bridge) Serve(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			b.Close()
		case <-done:
		}
	}()
	err := b.srv.Serve(b.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close stops the server and closes the UI socket.
func (b *Bridge) Close() {
	b.mu.Lock()
	ui := b.ui
	b.mu.Unlock()
	if ui != nil {
		ui.close(websocket.StatusGoingAway, "session ended")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = b.srv.Shutdown(ctx)
	_ = b.srv.Close()
}

// Send queues one text frame for the UI. It never blocks: a UI that stops
// reading is closed with 1013 and must reconnect.
func (b *Bridge) Send(raw []byte) error {
	b.mu.Lock()
	ui := b.ui
	b.mu.Unlock()
	if ui == nil {
		return ErrUINotConnected
	}
	select {
	case ui.out <- raw:
		return nil
	default:
		ui.close(websocket.StatusTryAgainLater, "write queue overflow")
		return ErrUIBackpressure
	}
}

// --- request guards --------------------------------------------------------------------------

// guard enforces the Host rule on every request and stamps the security
// headers on every response, including errors.
func (b *Bridge) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", BridgeCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		if r.Host != b.host {
			bridgeError(w, http.StatusForbidden, "unexpected Host")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originOK applies the Origin rule: exact match on every non-GET request and on
// the WebSocket upgrade; a GET without an Origin (top-level navigation, asset
// fetch) is fine, but a GET that carries one must carry ours.
func (b *Bridge) originOK(r *http.Request, strict bool) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return !strict
	}
	return origin == b.origin
}

func (b *Bridge) tokenMatches(candidate string) bool {
	sum := sha256.Sum256([]byte(candidate))
	b.mu.Lock()
	set, want := b.tokenSet, b.tokenHash
	b.mu.Unlock()
	return set && subtle.ConstantTimeCompare(sum[:], want[:]) == 1
}

// authed wraps an API handler with the Origin and Bearer checks.
func (b *Bridge) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !b.originOK(r, r.Method != http.MethodGet && r.Method != http.MethodHead) {
			bridgeError(w, http.StatusForbidden, "unexpected Origin")
			return
		}
		token := bearerToken(r)
		if token == "" || !b.tokenMatches(token) {
			bridgeError(w, http.StatusUnauthorized, "session token required")
			return
		}
		h(w, r)
	}
}

// --- bootstrap -----------------------------------------------------------------------------------

type sessionRequest struct {
	Nonce string `json:"nonce"`
}

type sessionResponse struct {
	Token string `json:"token"`
}

// handleSession redeems the launch nonce for the session token.
func (b *Bridge) handleSession(w http.ResponseWriter, r *http.Request) {
	if !b.originOK(r, true) {
		bridgeError(w, http.StatusForbidden, "unexpected Origin")
		return
	}
	var req sessionRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Nonce == "" {
		bridgeError(w, http.StatusBadRequest, "nonce required")
		return
	}
	sum := sha256.Sum256([]byte(req.Nonce))

	b.mu.Lock()
	known := b.nonceSet && subtle.ConstantTimeCompare(sum[:], b.nonceHash[:]) == 1
	switch {
	case !known:
		b.mu.Unlock()
		// A value that was never minted cannot come from the window we launched.
		bridgeError(w, http.StatusForbidden, "unknown nonce")
		return
	case b.nonceUsed:
		b.mu.Unlock()
		b.abort(ErrNonceReused)
		bridgeError(w, http.StatusForbidden, ErrNonceReused.Error())
		return
	case b.clock().After(b.nonceExpires):
		b.nonceUsed = true
		b.mu.Unlock()
		b.abort(ErrNonceExpired)
		bridgeError(w, http.StatusForbidden, ErrNonceExpired.Error())
		return
	}
	token, err := cryptoHex(bridgeTokenBytes)
	if err != nil {
		b.mu.Unlock()
		bridgeError(w, http.StatusInternalServerError, "token generation failed")
		return
	}
	b.nonceUsed = true
	b.tokenHash, b.tokenSet = sha256.Sum256([]byte(token)), true
	b.mu.Unlock()
	bridgeJSON(w, http.StatusOK, sessionResponse{Token: token})
}

// --- WebSocket ---------------------------------------------------------------------------------------

type authFrame struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

func (b *Bridge) handleWS(w http.ResponseWriter, r *http.Request) {
	if !b.originOK(r, true) {
		bridgeError(w, http.StatusForbidden, "unexpected Origin")
		return
	}
	b.mu.Lock()
	busy := b.ui != nil && !b.ui.closing.Load()
	b.mu.Unlock()
	if busy {
		// One active UI socket: a second window or tab is refused, not
		// allowed to steal the session. A socket that is already closing is
		// not active and does not refuse the page reconnecting onto it.
		bridgeError(w, http.StatusConflict, "another window is already connected")
		return
	}
	// The window asks for plan-forge.v1; without it in AcceptOptions the
	// handshake answers with no Sec-WebSocket-Protocol and the browser fails
	// the connection before a single frame is written.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:  []string{strings.TrimPrefix(b.origin, "http://")},
		Subprotocols:    []string{WSSubprotocol},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	if c.Subprotocol() != WSSubprotocol {
		_ = c.Close(websocket.StatusPolicyViolation, "subprotocol "+WSSubprotocol+" required")
		return
	}
	c.SetReadLimit(MaxFrameBytes)

	// First message authenticates the socket. Nothing else is read before it.
	actx, acancel := context.WithTimeout(context.Background(), bridgeAuthDeadline)
	_, data, err := c.Read(actx)
	acancel()
	var af authFrame
	if err != nil || json.Unmarshal(data, &af) != nil || af.Type != "auth" || !b.tokenMatches(af.Token) {
		_ = c.Close(websocket.StatusPolicyViolation, "authentication failed")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	ui := &uiConn{conn: c, out: make(chan []byte, bridgeQueueDepth), ctx: ctx, cancel: cancel}
	b.mu.Lock()
	claimed := b.claimLocked(ui)
	b.mu.Unlock()
	if !claimed {
		cancel()
		_ = c.Close(websocket.StatusPolicyViolation, "another window is already connected")
		return
	}

	go b.uiWriter(ui)
	ui.out <- []byte(`{"type":"auth.ok"}`)
	b.emit(true)

	b.uiReader(ui)

	b.mu.Lock()
	current := b.ui == ui
	if current {
		b.ui = nil
	}
	b.mu.Unlock()
	ui.close(websocket.StatusNormalClosure, "bye")
	// A displaced socket must stay silent: its replacement already reported the
	// window as up, and a late disconnect here would start the supervisor's
	// grace timer against a live window and end the session at exit 130.
	if current {
		b.emit(false)
	}
}

func (b *Bridge) uiReader(ui *uiConn) {
	for {
		typ, data, err := ui.conn.Read(ui.ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			ui.close(websocket.StatusUnsupportedData, "text frames only")
			return
		}
		if b.cfg.OnMessage != nil {
			b.cfg.OnMessage(data)
		}
	}
}

func (b *Bridge) uiWriter(ui *uiConn) {
	for {
		select {
		case <-ui.ctx.Done():
			return
		case raw := <-ui.out:
			wctx, cancel := context.WithTimeout(ui.ctx, bridgeWriteTimeout)
			err := ui.conn.Write(wctx, websocket.MessageText, raw)
			cancel()
			if err != nil {
				_ = ui.conn.CloseNow()
				ui.cancel()
				return
			}
		}
	}
}

// --- static assets ---------------------------------------------------------------------------------

// handleStatic serves the embedded UI. Paths without a file extension fall back
// to index.html so client-side routes (/ingest, /deliberate, /synthesis) load.
func (b *Bridge) handleStatic(w http.ResponseWriter, r *http.Request) {
	if !b.originOK(r, false) {
		bridgeError(w, http.StatusForbidden, "unexpected Origin")
		return
	}
	if b.cfg.Assets == nil {
		bridgeError(w, http.StatusNotFound, "no UI is embedded in this build")
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	data, err := fs.ReadFile(b.cfg.Assets, name)
	if err != nil {
		if path.Ext(name) != "" {
			bridgeError(w, http.StatusNotFound, "not found")
			return
		}
		name = "index.html"
		data, err = fs.ReadFile(b.cfg.Assets, name)
		if err != nil {
			bridgeError(w, http.StatusNotFound, "not found")
			return
		}
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

// --- open-url ----------------------------------------------------------------------------------------

type openURLRequest struct {
	URL string `json:"url"`
}

// ValidateOpenURL accepts only http and https URLs up to 2048 bytes without
// userinfo or control characters, and returns them re-serialized after parsing
// so what is launched is exactly what was validated.
func ValidateOpenURL(raw string) (string, error) {
	if raw == "" || len(raw) > maxOpenURLBytes {
		return "", errors.New("url must be 1-2048 bytes")
	}
	if !utf8.ValidString(raw) {
		return "", errors.New("url is not valid UTF-8")
	}
	for _, r := range raw {
		if unicode.IsControl(r) || r == ' ' {
			return "", errors.New("url contains control characters or spaces")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("url is not valid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("only http and https URLs can be opened")
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", errors.New("url has no host")
	}
	if u.User != nil {
		return "", errors.New("url must not carry credentials")
	}
	out := u.String()
	if len(out) > maxOpenURLBytes {
		return "", errors.New("url is too long")
	}
	return out, nil
}

// OpenURLArgv opens a URL with the platform opener through an argv, never a
// shell and never `cmd /c start`.
func OpenURLArgv(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", "--", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open URL: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func (b *Bridge) handleOpenURL(w http.ResponseWriter, r *http.Request) {
	var req openURLRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		bridgeError(w, http.StatusBadRequest, "url required")
		return
	}
	safe, err := ValidateOpenURL(req.URL)
	if err != nil {
		bridgeError(w, http.StatusBadRequest, err.Error())
		return
	}
	open := b.cfg.OpenURL
	if open == nil {
		open = OpenURLArgv
	}
	if err := open(safe); err != nil {
		bridgeError(w, http.StatusBadGateway, "could not open the URL")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- upload -----------------------------------------------------------------------------------------------

// cleanUploadName reduces a client filename to printable display text: its
// directory part is dropped and control characters replaced. The result routes
// parsers by extension and is never joined into a path.
func cleanUploadName(raw string) string {
	raw = strings.ReplaceAll(raw, "\\", "/")
	if i := strings.LastIndex(raw, "/"); i >= 0 {
		raw = raw[i+1:]
	}
	var sb strings.Builder
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			sb.WriteRune(' ')
			continue
		}
		sb.WriteRune(r)
	}
	out := strings.TrimSpace(sb.String())
	if len(out) > maxUploadNameBytes {
		out = strings.ToValidUTF8(out[:maxUploadNameBytes], "")
	}
	return out
}

func (b *Bridge) handleUpload(w http.ResponseWriter, r *http.Request) {
	// Whole-request cap: five full files plus form overhead.
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadFiles*MaxUploadFileBytes+maxFormFieldBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		bridgeError(w, http.StatusBadRequest, "multipart/form-data required")
		return
	}
	var files []UploadedFile
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			bridgeError(w, http.StatusRequestEntityTooLarge, "upload is malformed or too large")
			return
		}
		if part.FileName() == "" {
			_, _ = io.Copy(io.Discard, io.LimitReader(part, maxFormFieldBytes))
			continue
		}
		if len(files) >= MaxUploadFiles {
			bridgeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("at most %d files per upload", MaxUploadFiles))
			return
		}
		file, ok := readUploadPart(part)
		if !ok {
			bridgeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("a file exceeds %d MiB", MaxUploadFileBytes>>20))
			return
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		bridgeError(w, http.StatusBadRequest, "no files in upload")
		return
	}
	out, err := b.cfg.Upload(r.Context(), files)
	if err != nil {
		bridgeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	bridgeJSON(w, http.StatusOK, out)
}

func readUploadPart(part *multipart.Part) (UploadedFile, bool) {
	data, err := io.ReadAll(io.LimitReader(part, MaxUploadFileBytes+1))
	if err != nil || len(data) > MaxUploadFileBytes {
		return UploadedFile{}, false
	}
	return UploadedFile{
		Name: cleanUploadName(part.FileName()), Size: int64(len(data)), Content: bytes.NewReader(data),
	}, true
}

// --- helpers ------------------------------------------------------------------------------------------------

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	v := r.Header.Get("Authorization")
	if len(v) > len(prefix) && strings.EqualFold(v[:len(prefix)], prefix) {
		return strings.TrimSpace(v[len(prefix):])
	}
	return ""
}

func bridgeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func bridgeError(w http.ResponseWriter, code int, msg string) {
	bridgeJSON(w, code, map[string]string{"error": msg})
}

func cryptoHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
