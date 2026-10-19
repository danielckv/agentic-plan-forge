//go:build client && !server

package center

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Credential bounds. A ticket is the generalisation of the bridge's launch
// nonce: same length, same lifetime, same single use, but a small pool because
// several terminals may ask to open a tab at once.
const (
	ticketTTL    = 10 * time.Second
	ticketBytes  = 16
	maxTickets   = 8
	sessionTTL   = 12 * time.Hour
	sessionBytes = 32
	maxSessions  = 16
	daemonBytes  = 32
)

var (
	// ErrTicketUnknown covers an unknown, spent or expired ticket. The three
	// are one error on purpose: telling them apart tells a guesser which half
	// of a ticket it got right.
	ErrTicketUnknown = errors.New("center: unknown or expired ticket")
	// ErrTicketsBusy means the pool is full of unspent tickets.
	ErrTicketsBusy = errors.New("center: too many unredeemed tickets")
)

func cryptoHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// ticketPool holds the short-lived, single-use tickets that authorise one tab
// to redeem a session token.
type ticketPool struct {
	mu    sync.Mutex
	items map[string]time.Time
	now   func() time.Time
}

func newTicketPool() *ticketPool {
	return &ticketPool{items: map[string]time.Time{}, now: time.Now}
}

// mint issues a ticket, first dropping any that expired.
func (p *ticketPool) mint() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepLocked()
	if len(p.items) >= maxTickets {
		return "", ErrTicketsBusy
	}
	t, err := cryptoHex(ticketBytes)
	if err != nil {
		return "", err
	}
	p.items[t] = p.now().Add(ticketTTL)
	return t, nil
}

// redeem spends a ticket. A ticket is removed whether or not it had expired,
// so a leaked one cannot be retried.
func (p *ticketPool) redeem(t string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepLocked()
	exp, ok := p.items[t]
	if !ok {
		return ErrTicketUnknown
	}
	delete(p.items, t)
	if p.now().After(exp) {
		return ErrTicketUnknown
	}
	return nil
}

func (p *ticketPool) sweepLocked() {
	now := p.now()
	for t, exp := range p.items {
		if now.After(exp) {
			delete(p.items, t)
		}
	}
}

// sessionTokens holds the tokens live tabs authenticate with. The set is
// small and bounded, so a candidate is compared against every entry in
// constant time rather than looked up, and no comparison short-circuits on a
// prefix.
type sessionTokens struct {
	mu      sync.Mutex
	entries []tokenEntry
	now     func() time.Time
}

type tokenEntry struct {
	hash [32]byte
	exp  time.Time
}

func newSessionTokens() *sessionTokens {
	return &sessionTokens{now: time.Now}
}

// issue mints a session token and returns it. The token itself is never
// stored: only its digest, so a dump of this process's heap does not hand over
// a usable credential.
func (s *sessionTokens) issue() (string, error) {
	tok, err := cryptoHex(sessionBytes)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	if len(s.entries) >= maxSessions {
		// Drop the entry closest to expiry: a tab that has been open longest
		// is the one most likely already gone.
		oldest := 0
		for i, e := range s.entries {
			if e.exp.Before(s.entries[oldest].exp) {
				oldest = i
			}
		}
		s.entries = append(s.entries[:oldest], s.entries[oldest+1:]...)
	}
	s.entries = append(s.entries, tokenEntry{hash: sha256.Sum256([]byte(tok)), exp: s.now().Add(sessionTTL)})
	return tok, nil
}

// valid reports whether candidate is a live session token.
func (s *sessionTokens) valid(candidate string) bool {
	if candidate == "" {
		return false
	}
	sum := sha256.Sum256([]byte(candidate))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	ok := 0
	for _, e := range s.entries {
		ok |= subtle.ConstantTimeCompare(sum[:], e.hash[:])
	}
	return ok == 1
}

func (s *sessionTokens) sweepLocked() {
	now := s.now()
	kept := s.entries[:0]
	for _, e := range s.entries {
		if now.Before(e.exp) {
			kept = append(kept, e)
		}
	}
	s.entries = kept
}

// bearerToken extracts the token from an Authorization header.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	v := r.Header.Get("Authorization")
	if len(v) <= len(prefix) || !strings.EqualFold(v[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(v[len(prefix):])
}

// --- request guards ----------------------------------------------------------------------------

// guard enforces the Host rule on every request and stamps the security
// headers on every response, including errors. It is the bridge's guard with
// the same headers, because the command center is the same kind of surface
// reached over the same loopback.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", CSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		// The Host check is the DNS-rebinding defence: a name that resolves to
		// 127.0.0.1 still arrives with that name in Host, not ours.
		if r.Host != s.host {
			writeError(w, http.StatusForbidden, "unexpected Host")
			return
		}
		// Defence in depth, never a requirement: older browsers omit the
		// header, so only an explicit cross-site value is refused.
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, http.StatusForbidden, "cross-site request")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originOK applies the Origin rule: an exact match is required on every
// non-GET request and on the event stream; a GET without an Origin (a
// top-level navigation or an asset fetch) is fine, but one that carries an
// Origin must carry ours.
func (s *Server) originOK(r *http.Request, strict bool) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return !strict
	}
	return origin == s.origin
}

// authed wraps a handler the browser calls with the Origin and Bearer checks.
//
// Every data-returning GET goes through here, and that is deliberate: needing
// an Authorization header is what makes the request non-simple, so a page on
// another origin cannot reach it without a preflight this server never
// answers. A bare GET that returns data would undo that, whatever else guards
// it, so none may be added.
func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.originOK(r, r.Method != http.MethodGet && r.Method != http.MethodHead) {
			writeError(w, http.StatusForbidden, "unexpected Origin")
			return
		}
		if !s.tokens.valid(bearerToken(r)) {
			writeError(w, http.StatusUnauthorized, "session token required")
			return
		}
		h(w, r)
	}
}

// daemonOnly wraps a handler only the local CLI may call: it carries the
// daemon secret and must arrive without an Origin.
//
// A browser always sends Origin on a POST, so its absence is what proves the
// caller is not a page. Together with the secret, which lives in a 0600 file,
// that keeps the ingest and ticket endpoints off limits to anything running in
// a tab.
func (s *Server) daemonOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			writeError(w, http.StatusForbidden, "this endpoint is not reachable from a browser")
			return
		}
		got := bearerToken(r)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.secret)) != 1 {
			writeError(w, http.StatusUnauthorized, "daemon token required")
			return
		}
		h(w, r)
	}
}
