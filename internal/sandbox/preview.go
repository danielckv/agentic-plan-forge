package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TrafficTokenHeader is the header the provider requires on preview traffic.
// The proxy injects it server-side; the browser never sees the token.
const TrafficTokenHeader = "E2b-Traffic-Access-Token"

// AccessCookieName is the cookie carrying the exchanged preview credential.
const AccessCookieName = "pf_preview_access"

// maxHeaderBytes bounds how much of an upstream response header block is
// read back before it is forwarded.
const maxHeaderBytes = 1 << 20

// hopHeaders are hop-by-hop per RFC 7230; a proxy must not forward them.
var hopHeaders = map[string]bool{
	"connection":          true,
	"proxy-connection":    true,
	"keep-alive":          true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
}

// strippedRequestHeaders never reach the untrusted application. Client
// credentials, provider headers and forwarding chains stay on this side.
var strippedRequestHeaders = []string{
	"Cookie",
	"Authorization",
	"Proxy-Authorization",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Real-Ip",
	"E2b-Trace-Id",
	"E2b-Client-Id",
	"E2b-Traffic-Access-Token", // re-injected below with the server-side token
}

// PreviewHandler builds the dedicated, authenticated preview proxy. Its
// upstream is fixed at construction: a request can change the path, never
// the destination. It injects the provider traffic token, strips client
// credentials, forwards WebSocket upgrades over the same fixed upstream and
// refuses every request that fails the origin check.
//
// upstream is the sandbox host (a bare hostname, or an https URL whose host
// is used and whose path and credentials are rejected). The upstream is
// always contacted over https. allowOriginCheck decides per request; pass
// AccessGate.Authorize composed with an origin test to close the gate.
func PreviewHandler(upstream string, port int, token string, allowOriginCheck func(*http.Request) bool) (http.Handler, error) {
	host, err := previewUpstreamHost(upstream)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(host, PreviewDomainSuffix) {
		// A preview is a provider host. Anything else would let this proxy carry
		// the provider token to a destination the caller chose.
		return nil, fmt.Errorf("sandbox preview proxy: upstream %q is not under %s", host, PreviewDomainSuffix)
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("sandbox preview proxy: port %d outside [1, 65535]", port)
	}
	if token == "" {
		return nil, errors.New("sandbox preview proxy: traffic token is required")
	}
	if allowOriginCheck == nil {
		return nil, errors.New("sandbox preview proxy: allowOriginCheck is required; an open proxy is not an option")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowOriginCheck(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if isWebSocketUpgrade(r) {
			proxyWebSocket(w, r, host, port, token)
			return
		}
		proxyHTTP(w, r, host, port, token)
	}), nil
}

// previewUpstreamHost accepts a bare hostname or an https URL and returns
// the host to dial. Any scheme other than https, any path, query, user
// info or port inside the URL is refused: the caller supplies the port
// separately and the destination must not be negotiable per request.
func previewUpstreamHost(upstream string) (string, error) {
	if upstream == "" {
		return "", errors.New("sandbox preview proxy: upstream is required")
	}
	if strings.ContainsAny(upstream, " \t\r\n") {
		return "", fmt.Errorf("sandbox preview proxy: upstream %q contains whitespace", upstream)
	}
	if !strings.Contains(upstream, "://") {
		// Bare hostname form.
		if u, err := url.Parse("https://" + upstream); err == nil && u.Hostname() != "" {
			return u.Hostname(), nil
		}
		return "", fmt.Errorf("sandbox preview proxy: upstream %q is not a hostname", upstream)
	}
	u, err := url.Parse(upstream)
	if err != nil {
		return "", fmt.Errorf("sandbox preview proxy: parse upstream: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("sandbox preview proxy: upstream scheme must be https, got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("sandbox preview proxy: upstream %q has no host", upstream)
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("sandbox preview proxy: upstream %q carries path, query, credentials or fragment", upstream)
	}
	return u.Hostname(), nil
}

// proxiedURL builds the fixed upstream URL for one request path.
func proxiedURL(host string, port int, reqPath string) (*url.URL, error) {
	clean := path.Clean("/" + reqPath)
	if clean != "/" && strings.HasPrefix(reqPath, "/") && clean != path.Clean(reqPath) {
		// Cleaning changed the shape: the request tried to climb.
		return nil, fmt.Errorf("sandbox preview proxy: path %q does not stay inside the preview", reqPath)
	}
	if strings.ContainsAny(clean, "\x00\\") {
		return nil, fmt.Errorf("sandbox preview proxy: path %q contains control or separator characters", clean)
	}
	u := &url.URL{
		Scheme: "https",
		Host:   host + ":" + itoa(port),
		Path:   clean,
	}
	if clean != reqPath && reqPath != "/"+"" {
		// Tolerate an empty path and a trailing-slash difference; anything
		// else that cleaning altered was rejected above.
		if reqPath != "" && reqPath != clean && !strings.HasSuffix(reqPath, "/") {
			return nil, fmt.Errorf("sandbox preview proxy: path %q was rewritten to %q; refusing", reqPath, clean)
		}
	}
	return u, nil
}

// proxyHTTP forwards one plain request to the fixed upstream.
func proxyHTTP(w http.ResponseWriter, r *http.Request, host string, port int, token string) {
	target, err := proxiedURL(host, port, r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	target.RawQuery = r.URL.RawQuery

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		http.Error(w, "bad preview request", http.StatusBadRequest)
		return
	}
	copyForwardHeaders(outReq.Header, r.Header)
	outReq.Host = target.Host
	outReq.Header.Set(TrafficTokenHeader, token)

	resp, err := http.DefaultTransport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, "preview upstream unreachable", http.StatusBadGateway)
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
		_ = resp.Body.Close()
	}()

	for k, vs := range resp.Header {
		lk := strings.ToLower(k)
		if hopHeaders[lk] || lk == "set-cookie" {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// isWebSocketUpgrade reports whether the request asks for a WebSocket
// upgrade, per RFC 6455.
func isWebSocketUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, v := range r.Header.Values("Connection") {
		if strings.Contains(strings.ToLower(v), "upgrade") {
			return true
		}
	}
	return false
}

// proxyWebSocket dials the fixed upstream, relays the client's upgrade
// request with the injected token, and copies bidirectionally until either
// side closes. No client credential headers survive the relay.
func proxyWebSocket(w http.ResponseWriter, r *http.Request, host string, port int, token string) {
	target, err := proxiedURL(host, port, r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	upstreamConn, err := dialPreview(r.Context(), host, port)
	if err != nil {
		http.Error(w, "preview upstream unreachable", http.StatusBadGateway)
		return
	}

	// Rebuild the upgrade request line by line: keep the client's upgrade
	// headers, drop everything identified as client credentials, add the
	// token.
	var buf strings.Builder
	buf.WriteString(r.Method + " " + target.RequestURI() + " HTTP/1.1\r\n")
	buf.WriteString("Host: " + target.Host + "\r\n")
	for k, vs := range r.Header {
		lk := strings.ToLower(k)
		if lk == "cookie" || lk == "authorization" || lk == "host" ||
			strings.HasPrefix(lk, "e2b-") || lk == "x-forwarded-for" ||
			lk == "x-forwarded-host" || lk == "x-forwarded-proto" || lk == "x-real-ip" {
			continue
		}
		for _, v := range vs {
			buf.WriteString(k + ": " + v + "\r\n")
		}
	}
	buf.WriteString(TrafficTokenHeader + ": " + token + "\r\n")
	buf.WriteString("\r\n")
	if _, err := io.WriteString(upstreamConn, buf.String()); err != nil {
		_ = upstreamConn.Close()
		http.Error(w, "preview upstream write failed", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstreamConn.Close()
		http.Error(w, "preview proxy cannot hijack", http.StatusInternalServerError)
		return
	}
	clientConn, clientRW, err := hijacker.Hijack()
	if err != nil {
		_ = upstreamConn.Close()
		http.Error(w, "preview proxy hijack failed", http.StatusInternalServerError)
		return
	}

	// The client's handshake response is relayed verbatim from the upstream.
	if err := clientConn.SetDeadline(time.Time{}); err != nil {
		_ = clientConn.Close()
		_ = upstreamConn.Close()
		return
	}
	go func() {
		defer func() { _ = clientConn.Close() }()
		defer func() { _ = upstreamConn.Close() }()
		_, _ = io.Copy(upstreamConn, clientRW.Reader)
	}()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = upstreamConn.Close() }()
	_, _ = io.Copy(clientConn, upstreamConn)
}

// dialPreview opens a TLS connection to the fixed upstream host:port. The
// destination never depends on request input. Verification stays on: an
// unverified upstream would let traffic leave the trust boundary.
func dialPreview(ctx context.Context, host string, port int) (net.Conn, error) {
	d := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second},
		Config:    &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
	}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("dial preview upstream: %w", err)
	}
	return conn, nil
}

// copyForwardHeaders copies end-to-end headers from the client request,
// dropping hop-by-hop headers and the stripped credential set.
func copyForwardHeaders(dst, src http.Header) {
	stripped := map[string]bool{}
	for _, h := range strippedRequestHeaders {
		stripped[strings.ToLower(h)] = true
	}
	for k, vs := range src {
		lk := strings.ToLower(k)
		if hopHeaders[lk] || stripped[lk] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// AccessGate issues single-use bootstrap nonces, exchanges them once for a
// private cookie value, and authorizes requests by that cookie. Bootstrap
// lives in the URL for one request only; everything after carries the cookie.
type AccessGate struct {
	mu      sync.Mutex
	ttl     time.Duration
	nonces  map[string]time.Time // nonce -> issued at
	granted map[string]time.Time // cookie value -> issued at
}

// gateCapacity bounds the outstanding nonce and grant sets, so a flood of
// Issue calls cannot grow either map without limit.
const gateCapacity = 1024

// NewAccessGate builds a gate whose bootstrap nonces and exchanged
// credentials both live for ttl.
func NewAccessGate(ttl time.Duration) *AccessGate {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &AccessGate{
		ttl:     ttl,
		nonces:  map[string]time.Time{},
		granted: map[string]time.Time{},
	}
}

// Issue mints one bootstrap nonce. The nonce is single use: the first
// Exchange consumes it, and it dies at ttl regardless.
func (g *AccessGate) Issue() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gcLocked(time.Now())
	if len(g.nonces) >= gateCapacity {
		return "", errors.New("sandbox access gate: too many outstanding nonces")
	}
	nonce, err := randomToken()
	if err != nil {
		return "", fmt.Errorf("sandbox access gate: issue nonce: %w", err)
	}
	g.nonces[nonce] = time.Now()
	return nonce, nil
}

// Exchange consumes one nonce and returns the cookie value to hand the
// browser. A nonce is spent by this call: replaying it fails, as does an
// unknown, expired or already-used nonce.
func (g *AccessGate) Exchange(nonce string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	g.gcLocked(now)
	if nonce == "" {
		return "", errors.New("sandbox access gate: empty nonce")
	}
	issuedAt, ok := g.nonces[nonce]
	if !ok {
		return "", errors.New("sandbox access gate: unknown or already used nonce")
	}
	delete(g.nonces, nonce)
	if now.Sub(issuedAt) > g.ttl {
		return "", errors.New("sandbox access gate: nonce expired")
	}
	if len(g.granted) >= gateCapacity {
		return "", errors.New("sandbox access gate: too many outstanding grants")
	}
	cookieValue, err := randomToken()
	if err != nil {
		return "", fmt.Errorf("sandbox access gate: mint grant: %w", err)
	}
	g.granted[cookieValue] = now
	return cookieValue, nil
}

// Authorize reports whether the request carries a live access cookie. It is
// the request-level check a preview proxy composes into allowOriginCheck.
func (g *AccessGate) Authorize(r *http.Request) bool {
	if r == nil {
		return false
	}
	c, err := r.Cookie(AccessCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	g.gcLocked(now)
	issuedAt, ok := g.granted[c.Value]
	return ok && now.Sub(issuedAt) <= g.ttl
}

// RevokeAll forgets every outstanding nonce and grant: stopping the sandbox
// or its expiry closes the preview immediately.
func (g *AccessGate) RevokeAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nonces = map[string]time.Time{}
	g.granted = map[string]time.Time{}
}

// gcLocked drops expired entries. Callers hold the mutex.
func (g *AccessGate) gcLocked(now time.Time) {
	for n, at := range g.nonces {
		if now.Sub(at) > g.ttl {
			delete(g.nonces, n)
		}
	}
	for c, at := range g.granted {
		if now.Sub(at) > g.ttl {
			delete(g.granted, c)
		}
	}
}

// itoa formats an integer port without importing strconv at each call site.
func itoa(n int) string { return strconv.Itoa(n) }

// randomToken mints 256 bits of hex entropy for nonces and grants.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
