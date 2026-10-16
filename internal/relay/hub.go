//go:build server && !client

// Package relay, under the server build tag, hosts the collaboration hub: the
// HTTP routes that mint sessions, invites and member credentials, and the
// WebSocket transport around the authority that sequences events and owns the
// executor lease.
//
// The hub wraps two untagged packages — internal/collab for the document
// authority and internal/session for the reducer — so Solo mode and the relay
// run the identical authority with only transport differing. Those packages
// cannot import this one (an import cycle), so their mirrored wire shapes are
// converted here; hub_integration_test.go asserts the conversion is faithful.
//
// Concurrency model: every room has one mutex. Every state-changing step —
// reducer Apply, document push, sequencing, fan-out — runs inside it, so the
// order in which frames enter a receiver's queue is the authority's order.
// Fan-out never blocks: each connection owns a bounded outbox drained by one
// writer goroutine, and a receiver that falls 256 frames behind is closed with
// 1013 and resyncs from its last_seq.
package relay

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/danielckv/agentic-plan-forge/internal/collab"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/session"
)

// Transport limits.
const (
	subprotocol       = "plan-forge.v1"
	writeQueueDepth   = 256
	writeDeadline     = 5 * time.Second
	pingInterval      = 15 * time.Second
	pongTimeout       = 10 * time.Second
	helloDeadline     = 10 * time.Second
	replayWindow      = 2000
	ephemeralInterval = 100 * time.Millisecond
	cancelCloseDelay  = 2 * time.Second

	artifactRetention = 24 * time.Hour
	roomIdleTTL       = 24 * time.Hour
	maxArtifactBytes  = 4 << 20
	maxArtifactSets   = 8

	maxRooms          = 1024
	maxMembersPerRoom = 32
	maxConnsPerRoom   = 64

	maxTextDeltaPayload = 1 << 20
	maxTextDeltaUpdates = 256
	maxReplyCache       = 1024
	maxLabelRunes       = 80

	secretBytes      = 32 // 256-bit secrets
	sessionIDBytes   = 16 // 128-bit relay session ids
	inviteMaxUsesMax = 10
	maxUsesDefault   = 1
	inviteTTLMax     = 24 * time.Hour

	ownerMemberID = "owner"
	authorityID   = "authority"

	artifactEpochHeader = "X-Plan-Forge-Epoch"
)

// Rate limits (token buckets): requests or frames per second, then burst.
const (
	ipRate          = 20.0
	ipBurst         = 40.0
	roomHTTPRate    = 100.0
	roomHTTPBurst   = 200.0
	connMsgRate     = 200.0
	connMsgBurst    = 400.0
	roomMsgRate     = 1000.0
	roomMsgBurst    = 2000.0
	rateStrikeLimit = 100
)

var (
	sessionIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	uuidRe      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// artifactName allows only the two tracked artifact names.
func artifactName(name string) bool {
	return name == "synthesized_spec.md" || name == "plan_manifest.json"
}

// WebSocket close codes used by the hub, named for the protocol rules they
// enforce rather than their numbers.
const (
	closePolicy       = websocket.StatusPolicyViolation // 1008: protocol violation
	closeTryAgain     = websocket.StatusTryAgainLater   // 1013: queue overflow, resync
	closeGoingAway    = websocket.StatusGoingAway
	closeNormal       = websocket.StatusNormalClosure
	closeUnsupported  = websocket.StatusUnsupportedData
	closeProtoVersion = "UNSUPPORTED_VERSION"
)

// --- configuration ------------------------------------------------------------

// HubConfig configures a Hub.
type HubConfig struct {
	// Token is the bearer token POST /sessions requires. Empty disables the
	// check, which cmd/relay permits only on a loopback listener.
	Token string
	// BehindProxy makes the hub read the rightmost X-Forwarded-For entry as
	// the client IP when the TCP peer is TrustedProxy (or loopback/private
	// when TrustedProxy is nil).
	BehindProxy  bool
	TrustedProxy *net.IPNet
	Logger       *slog.Logger
	// Clock defaults to time.Now. Tests inject a fake.
	Clock func() time.Time
}

// RejectBrowserOrigin refuses any request that carries an Origin header. The
// relay's clients are local binaries, never browsers, so an Origin is either a
// mistake or an attempt to reach the relay from a page.
func RejectBrowserOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			http.Error(w, "requests carrying an Origin header are not accepted", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- rate limiting --------------------------------------------------------------

// bucket is a token bucket. It is safe for concurrent use.
type bucket struct {
	mu     sync.Mutex
	tokens float64
	rate   float64
	burst  float64
	last   time.Time
}

func newBucket(rate, burst float64, now time.Time) *bucket {
	return &bucket{tokens: burst, rate: rate, burst: burst, last: now}
}

func (b *bucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type ipEntry struct {
	b    *bucket
	seen time.Time
}

// --- credentials ----------------------------------------------------------------

type principalKind uint8

const (
	kindOwner principalKind = iota + 1
	kindMember
	kindInvite
)

// principal is what a bearer secret resolves to. Secrets are stored only as
// SHA-256 hashes; the hub never holds a plaintext after minting it.
type principal struct {
	kind principalKind
	room *room
	id   string // member id ("owner" for the owner) or invite id
	hash [32]byte
}

type invite struct {
	id      string
	role    session.Role
	maxUses int
	uses    int
	expires time.Time
	revoked bool
}

type member struct {
	id   string
	role session.Role
	hash [32]byte
}

type artifactSet struct {
	approvalID   string
	spec         []byte
	manifest     []byte
	specHash     string
	manifestHash string
	saved        time.Time
}

type ringEntry struct {
	seq uint64
	raw []byte
}

type stickyEntry struct {
	seq uint64
	raw []byte
}

type ephFrame struct {
	t       WSEventType
	sender  string
	payload json.RawMessage
}

type presenceState struct {
	MemberID string `json:"member_id"`
	Role     string `json:"role"`
	Page     string `json:"page,omitempty"`
	Online   bool   `json:"online"`
}

// room is one collaboration session. Every field below mu is guarded by it.
type room struct {
	hub *Hub
	id  string

	lastActive atomic.Int64 // unix nanos; read lock-free by the janitor
	msgLimit   *bucket      // aggregate inbound frame rate
	httpLimit  *bucket      // aggregate HTTP rate

	mu        sync.Mutex
	doc       *collab.Authority
	reducer   *session.Reducer
	invites   map[string]*invite
	members   map[string]*member
	memberSeq int
	hashes    [][32]byte // every credential hash indexed for this room
	conns     map[*hubConn]struct{}
	closed    bool

	seq       uint64      // gapless control-channel sequence
	ring      []ringEntry // last replayWindow control frames
	sticky    map[string]stickyEntry
	draftHash map[string]string // avatar id -> latest draft content_hash
	eph       map[string]ephFrame
	presence  map[string]*presenceState

	artifacts  map[string]*artifactSet
	lostEpoch  uint64
	replies    map[string][][]byte // reply cache for retried TEXT_DELTA / INGEST_APPEND
	replyOrder []string
}

// Hub owns every room and the relay credential.
type Hub struct {
	cfg    HubConfig
	logger *slog.Logger
	clock  func() time.Time

	mu     sync.RWMutex
	rooms  map[string]*room
	byHash map[[32]byte]principal

	tokenHash [32]byte
	hasToken  bool

	rlMu  sync.Mutex
	ipLim map[string]*ipEntry
}

// NewHub creates a hub.
func NewHub(cfg HubConfig) *Hub {
	h := &Hub{
		cfg:    cfg,
		logger: cfg.Logger,
		clock:  cfg.Clock,
		rooms:  map[string]*room{},
		byHash: map[[32]byte]principal{},
		ipLim:  map[string]*ipEntry{},
	}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	if h.clock == nil {
		h.clock = time.Now
	}
	if cfg.Token != "" {
		h.tokenHash = sha256.Sum256([]byte(cfg.Token))
		h.hasToken = true
	}
	return h
}

// Routes mounts every hub route on mux. It does not register /healthz or the
// Origin-rejection middleware: cmd/relay owns the process shell.
func (h *Hub) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /sessions", h.handleCreateSession)
	mux.HandleFunc("POST /sessions/{id}/invites", h.handleCreateInvite)
	mux.HandleFunc("DELETE /sessions/{id}/invites/{invite_id}", h.handleRevokeInvite)
	mux.HandleFunc("POST /sessions/{id}/join", h.handleJoin)
	mux.HandleFunc("GET /ws", h.handleWS)
	mux.HandleFunc("GET /sessions/{id}/artifacts/{approval_id}/{name}", h.handleGetArtifact)
	mux.HandleFunc("PUT /sessions/{id}/artifacts/{approval_id}/{name}", h.handlePutArtifact)
	mux.HandleFunc("GET /join/{id}", h.handleJoinLanding)
}

func newRoom(h *Hub, id string) *room {
	now := h.clock()
	rm := &room{
		hub:       h,
		id:        id,
		msgLimit:  newBucket(roomMsgRate, roomMsgBurst, now),
		httpLimit: newBucket(roomHTTPRate, roomHTTPBurst, now),
		doc:       collab.NewAuthority(""),
		reducer:   session.New(h.clock),
		invites:   map[string]*invite{},
		members:   map[string]*member{},
		conns:     map[*hubConn]struct{}{},
		sticky:    map[string]stickyEntry{},
		draftHash: map[string]string{},
		eph:       map[string]ephFrame{},
		presence:  map[string]*presenceState{},
		artifacts: map[string]*artifactSet{},
		replies:   map[string][][]byte{},
	}
	rm.lastActive.Store(now.UnixNano())
	return rm
}

func (h *Hub) index(p principal) {
	h.mu.Lock()
	h.byHash[p.hash] = p
	h.mu.Unlock()
}

func (h *Hub) unindex(hash [32]byte) {
	h.mu.Lock()
	delete(h.byHash, hash)
	h.mu.Unlock()
}

// lookup resolves a bearer secret. The map is keyed by the secret's SHA-256, so
// lookup time does not depend on how many leading bytes of a guess are right;
// the stored hash is still compared with crypto/subtle.
func (h *Hub) lookup(secret string) (principal, bool) {
	sum := sha256.Sum256([]byte(secret))
	h.mu.RLock()
	p, ok := h.byHash[sum]
	h.mu.RUnlock()
	if !ok || subtle.ConstantTimeCompare(sum[:], p.hash[:]) != 1 {
		return principal{}, false
	}
	return p, true
}

// --- HTTP routes ----------------------------------------------------------------

type createSessionResponse struct {
	RelaySessionID string `json:"relay_session_id"`
	OwnerSecret    string `json:"owner_secret"`
	InviteSecret   string `json:"invite_secret"`
}

type inviteRequest struct {
	Role       string `json:"role"`
	MaxUses    int    `json:"max_uses"`
	TTLSeconds int    `json:"ttl_seconds"`
}

type createInviteResponse struct {
	InviteID     string `json:"invite_id"`
	InviteSecret string `json:"invite_secret"`
}

type joinResponse struct {
	MemberID     string `json:"member_id"`
	MemberSecret string `json:"member_secret"`
}

// resolve applies defaults and bounds: max_uses 1–10, ttl <= 24 h, role
// editor or viewer.
func (q inviteRequest) resolve(defaultRole session.Role) (session.Role, int, time.Duration, error) {
	role := session.Role(q.Role)
	if q.Role == "" {
		role = defaultRole
	}
	if role != session.RoleEditor && role != session.RoleViewer {
		return "", 0, 0, errors.New("role must be editor or viewer")
	}
	uses := q.MaxUses
	if uses == 0 {
		uses = maxUsesDefault
	}
	if uses < 1 || uses > inviteMaxUsesMax {
		return "", 0, 0, fmt.Errorf("max_uses must be 1-%d", inviteMaxUsesMax)
	}
	ttl := inviteTTLMax
	if q.TTLSeconds != 0 {
		if q.TTLSeconds < 1 || time.Duration(q.TTLSeconds)*time.Second > inviteTTLMax {
			return "", 0, 0, errors.New("ttl_seconds must be 1-86400")
		}
		ttl = time.Duration(q.TTLSeconds) * time.Second
	}
	return role, uses, ttl, nil
}

// readInviteRequest decodes an invite body (<= 4 KiB, no unknown fields). An
// empty body is accepted when optional.
func readInviteRequest(w http.ResponseWriter, r *http.Request, optional bool) (inviteRequest, bool) {
	var req inviteRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		if errors.Is(err, io.EOF) && optional {
			return req, true
		}
		httpError(w, http.StatusBadRequest, "invalid invite body")
		return req, false
	}
	return req, true
}

func (h *Hub) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if !h.limitIP(w, r) {
		return
	}
	if h.hasToken {
		got := sha256.Sum256([]byte(bearer(r)))
		if subtle.ConstantTimeCompare(got[:], h.tokenHash[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="plan-forge-relay"`)
			httpError(w, http.StatusUnauthorized, "invalid relay token")
			return
		}
	}
	req, ok := readInviteRequest(w, r, true)
	if !ok {
		return
	}
	role, uses, ttl, err := req.resolve(session.RoleEditor)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.mu.RLock()
	count := len(h.rooms)
	h.mu.RUnlock()
	if count >= maxRooms {
		httpError(w, http.StatusServiceUnavailable, "relay is at its session limit")
		return
	}
	id, e1 := randomHex(sessionIDBytes)
	ownerSecret, e2 := randomHex(secretBytes)
	inviteSecret, e3 := randomHex(secretBytes)
	inviteID, e4 := randomHex(8)
	if err := errors.Join(e1, e2, e3, e4); err != nil {
		httpError(w, http.StatusInternalServerError, "credential generation failed")
		return
	}

	rm := newRoom(h, id)
	ownerHash := sha256.Sum256([]byte(ownerSecret))
	inviteHash := sha256.Sum256([]byte(inviteSecret))
	rm.invites[inviteID] = &invite{id: inviteID, role: role, maxUses: uses, expires: h.clock().Add(ttl)}
	rm.hashes = append(rm.hashes, ownerHash, inviteHash)
	rm.reducer.Join(ownerMemberID, session.RoleOwner)

	h.mu.Lock()
	h.rooms[id] = rm
	h.byHash[ownerHash] = principal{kind: kindOwner, room: rm, id: ownerMemberID, hash: ownerHash}
	h.byHash[inviteHash] = principal{kind: kindInvite, room: rm, id: inviteID, hash: inviteHash}
	h.mu.Unlock()

	writeJSON(w, http.StatusCreated, createSessionResponse{
		RelaySessionID: id, OwnerSecret: ownerSecret, InviteSecret: inviteSecret,
	})
}

// authenticate resolves the bearer credential for a /sessions/{id}/... route.
// Missing, unknown and wrong-room credentials all answer 401 identically so a
// caller cannot probe which session ids exist.
func (h *Hub) authenticate(w http.ResponseWriter, r *http.Request) (principal, bool) {
	id := r.PathValue("id")
	if !sessionIDRe.MatchString(id) {
		httpError(w, http.StatusBadRequest, "session id must be 128-bit hex")
		return principal{}, false
	}
	secret := bearer(r)
	if secret == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="plan-forge-relay"`)
		httpError(w, http.StatusUnauthorized, "Authorization: Bearer required")
		return principal{}, false
	}
	p, ok := h.lookup(secret)
	if !ok || p.room.id != id {
		httpError(w, http.StatusUnauthorized, "credential rejected")
		return principal{}, false
	}
	return p, true
}

func (h *Hub) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	if !h.limitIP(w, r) {
		return
	}
	p, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if p.kind != kindOwner {
		httpError(w, http.StatusUnauthorized, "owner secret required")
		return
	}
	rm := p.room
	if !h.limitRoom(w, rm) {
		return
	}
	req, ok := readInviteRequest(w, r, false)
	if !ok {
		return
	}
	role, uses, ttl, err := req.resolve("")
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	secret, e1 := randomHex(secretBytes)
	id, e2 := randomHex(8)
	if err := errors.Join(e1, e2); err != nil {
		httpError(w, http.StatusInternalServerError, "credential generation failed")
		return
	}
	hash := sha256.Sum256([]byte(secret))
	rm.mu.Lock()
	if rm.closed {
		rm.mu.Unlock()
		httpError(w, http.StatusGone, "session is closed")
		return
	}
	rm.invites[id] = &invite{id: id, role: role, maxUses: uses, expires: h.clock().Add(ttl)}
	rm.hashes = append(rm.hashes, hash)
	rm.mu.Unlock()
	h.index(principal{kind: kindInvite, room: rm, id: id, hash: hash})
	writeJSON(w, http.StatusCreated, createInviteResponse{InviteID: id, InviteSecret: secret})
}

func (h *Hub) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	if !h.limitIP(w, r) {
		return
	}
	p, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if p.kind != kindOwner {
		httpError(w, http.StatusUnauthorized, "owner secret required")
		return
	}
	rm := p.room
	if !h.limitRoom(w, rm) {
		return
	}
	rm.mu.Lock()
	found := rm.revokeInviteLocked(r.PathValue("invite_id"))
	rm.mu.Unlock()
	if !found {
		httpError(w, http.StatusNotFound, "unknown invite")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleJoin atomically consumes one invite use and mints the member
// credential. Reconnects reuse the member secret and never come back here.
func (h *Hub) handleJoin(w http.ResponseWriter, r *http.Request) {
	if !h.limitIP(w, r) {
		return
	}
	p, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if p.kind != kindInvite {
		httpError(w, http.StatusUnauthorized, "invite secret required")
		return
	}
	rm := p.room
	if !h.limitRoom(w, rm) {
		return
	}
	secret, err := randomHex(secretBytes)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "credential generation failed")
		return
	}
	hash := sha256.Sum256([]byte(secret))

	rm.mu.Lock()
	if rm.closed {
		rm.mu.Unlock()
		httpError(w, http.StatusGone, "session is closed")
		return
	}
	inv := rm.invites[p.id]
	now := h.clock()
	switch {
	case inv == nil || inv.revoked:
		rm.mu.Unlock()
		httpError(w, http.StatusUnauthorized, "invite is revoked")
		return
	case now.After(inv.expires):
		rm.mu.Unlock()
		httpError(w, http.StatusUnauthorized, "invite has expired")
		return
	case inv.uses >= inv.maxUses:
		rm.mu.Unlock()
		httpError(w, http.StatusUnauthorized, "invite has no uses left")
		return
	case len(rm.members) >= maxMembersPerRoom:
		rm.mu.Unlock()
		httpError(w, http.StatusConflict, "session is full")
		return
	}
	inv.uses++
	rm.memberSeq++
	memberID := "m" + strconv.Itoa(rm.memberSeq)
	rm.members[memberID] = &member{id: memberID, role: inv.role, hash: hash}
	rm.hashes = append(rm.hashes, hash)
	rm.reducer.Join(memberID, inv.role)
	rm.lastActive.Store(now.UnixNano())
	rm.mu.Unlock()

	h.index(principal{kind: kindMember, room: rm, id: memberID, hash: hash})
	writeJSON(w, http.StatusCreated, joinResponse{MemberID: memberID, MemberSecret: secret})
}

// handleJoinLanding serves static text only. The invite secret lives in the URL
// fragment and never reaches this handler.
func (h *Hub) handleJoinLanding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintln(w, "Plan-Forge collaboration session.")
	fmt.Fprintln(w, "Install Plan-Forge, then run `plan-forge join` in your terminal and paste this link when asked.")
}

// --- artifacts ------------------------------------------------------------------

func (h *Hub) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	if !h.limitIP(w, r) {
		return
	}
	p, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if p.kind != kindOwner && p.kind != kindMember {
		httpError(w, http.StatusUnauthorized, "owner or member secret required")
		return
	}
	rm := p.room
	if !h.limitRoom(w, rm) {
		return
	}
	name := r.PathValue("name")
	approvalID := r.PathValue("approval_id")
	if !artifactName(name) {
		httpError(w, http.StatusBadRequest, "unknown artifact name")
		return
	}
	if !uuidRe.MatchString(approvalID) {
		httpError(w, http.StatusBadRequest, "approval_id must be a canonical UUID")
		return
	}
	rm.mu.Lock()
	art := rm.artifacts[approvalID]
	var body []byte
	var sum string
	if art != nil {
		if name == "synthesized_spec.md" {
			body, sum = art.spec, art.specHash
		} else {
			body, sum = art.manifest, art.manifestHash
		}
	}
	rm.mu.Unlock()
	if body == nil {
		httpError(w, http.StatusNotFound, "artifact not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Plan-Forge-SHA256", sum)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// handlePutArtifact accepts one approval artifact from the lease-holding owner.
// The relay hashes the bytes itself; ARTIFACT_SAVED{ok} is accepted later only
// when its reported hashes equal these.
func (h *Hub) handlePutArtifact(w http.ResponseWriter, r *http.Request) {
	if !h.limitIP(w, r) {
		return
	}
	p, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if p.kind != kindOwner {
		httpError(w, http.StatusUnauthorized, "owner secret required")
		return
	}
	rm := p.room
	if !h.limitRoom(w, rm) {
		return
	}
	name := r.PathValue("name")
	approvalID := r.PathValue("approval_id")
	if !artifactName(name) {
		httpError(w, http.StatusBadRequest, "unknown artifact name")
		return
	}
	if !uuidRe.MatchString(approvalID) {
		httpError(w, http.StatusBadRequest, "approval_id must be a canonical UUID")
		return
	}
	epoch, err := strconv.ParseUint(r.Header.Get(artifactEpochHeader), 10, 64)
	if err != nil {
		httpError(w, http.StatusBadRequest, artifactEpochHeader+" must be a decimal integer")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxArtifactBytes))
	if err != nil {
		httpError(w, http.StatusRequestEntityTooLarge, "artifact exceeds 4 MiB")
		return
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()
	snap := rm.reducer.Snapshot()
	if snap.Phase != session.PhaseApproving && snap.Phase != session.PhaseApprovingUnknown {
		httpError(w, http.StatusConflict, "artifact upload requires phase APPROVING or APPROVING_UNKNOWN")
		return
	}
	if snap.ApprovalID != approvalID {
		httpError(w, http.StatusConflict, "approval_id is not the approval in progress")
		return
	}
	holder, leaseEpoch, valid := rm.reducer.LeaseSnapshot()
	if !valid || holder != p.id || leaseEpoch != epoch {
		httpError(w, http.StatusConflict, "FENCED: the executor lease is not held at that epoch")
		return
	}
	art := rm.artifacts[approvalID]
	if art == nil {
		if len(rm.artifacts) >= maxArtifactSets {
			rm.evictOldestArtifactLocked()
		}
		art = &artifactSet{approvalID: approvalID}
		rm.artifacts[approvalID] = art
	}
	sum := sha256.Sum256(body)
	if name == "synthesized_spec.md" {
		art.spec, art.specHash = body, hex.EncodeToString(sum[:])
	} else {
		art.manifest, art.manifestHash = body, hex.EncodeToString(sum[:])
	}
	art.saved = h.clock()
	rm.lastActive.Store(art.saved.UnixNano())
	w.WriteHeader(http.StatusNoContent)
}

func (rm *room) evictOldestArtifactLocked() {
	var oldest string
	var at time.Time
	for id, a := range rm.artifacts {
		if oldest == "" || a.saved.Before(at) {
			oldest, at = id, a.saved
		}
	}
	delete(rm.artifacts, oldest)
}

// --- HTTP helpers ---------------------------------------------------------------

func bearer(r *http.Request) string {
	const prefix = "Bearer "
	v := r.Header.Get("Authorization")
	if len(v) > len(prefix) && strings.EqualFold(v[:len(prefix)], prefix) {
		return strings.TrimSpace(v[len(prefix):])
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, msg, code)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; a time-derived id
		// keeps the frame well-formed rather than panicking the relay.
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UnixNano()&0xffffffffffff)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// clientIP is the identity rate limits key on: the TCP peer, or the rightmost
// X-Forwarded-For entry when the peer is the configured trusted proxy.
func (h *Hub) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !h.cfg.BehindProxy {
		return host
	}
	peer := net.ParseIP(host)
	trusted := false
	switch {
	case peer == nil:
	case h.cfg.TrustedProxy != nil:
		trusted = h.cfg.TrustedProxy.Contains(peer)
	default:
		trusted = peer.IsLoopback() || peer.IsPrivate()
	}
	if !trusted {
		return host
	}
	fwd := r.Header.Values("X-Forwarded-For")
	if len(fwd) == 0 {
		return host
	}
	parts := strings.Split(fwd[len(fwd)-1], ",")
	last := strings.TrimSpace(parts[len(parts)-1])
	if net.ParseIP(last) == nil {
		return host
	}
	return last
}

func (h *Hub) limitIP(w http.ResponseWriter, r *http.Request) bool {
	ip := h.clientIP(r)
	now := h.clock()
	h.rlMu.Lock()
	e := h.ipLim[ip]
	if e == nil {
		e = &ipEntry{b: newBucket(ipRate, ipBurst, now)}
		h.ipLim[ip] = e
	}
	e.seen = now
	h.rlMu.Unlock()
	if !e.b.allow(now) {
		w.Header().Set("Retry-After", "1")
		httpError(w, http.StatusTooManyRequests, string(contracts.CodeRateLimited))
		return false
	}
	return true
}

func (h *Hub) limitRoom(w http.ResponseWriter, rm *room) bool {
	if !rm.httpLimit.allow(h.clock()) {
		w.Header().Set("Retry-After", "1")
		httpError(w, http.StatusTooManyRequests, string(contracts.CodeRateLimited))
		return false
	}
	return true
}

func (h *Hub) pruneLimiters() {
	cutoff := h.clock().Add(-10 * time.Minute)
	h.rlMu.Lock()
	defer h.rlMu.Unlock()
	for ip, e := range h.ipLim {
		if e.seen.Before(cutoff) {
			delete(h.ipLim, ip)
		}
	}
}

// revokeInviteLocked marks an invite revoked. Caller holds rm.mu.
func (rm *room) revokeInviteLocked(id string) bool {
	inv := rm.invites[id]
	if inv == nil {
		return false
	}
	inv.revoked = true
	return true
}

// sanitizeLabel makes a user-supplied name safe to splice into a Markdown
// heading: control characters and Markdown/HTML punctuation are dropped,
// whitespace collapses, and the result is capped.
func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == utf8.RuneError:
			b.WriteRune(' ')
		case strings.ContainsRune("#`<>[]|*_\\", r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(out); len(r) > maxLabelRunes {
		out = string(r[:maxLabelRunes])
	}
	return out
}

// sortedKeys returns map keys in order so fan-out is deterministic.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- WebSocket transport ----------------------------------------------------------

// outbox is a connection's pending frames. Replay frames (snapshot, missed
// events, document catch-up) are produced under the room lock before any live
// frame can exist for the connection and are drained first, so a reconnecting
// client sees them in authority order without the replay counting against the
// 256-frame live bound. Live frames are bounded: a full queue closes the
// connection with 1013 and the client resyncs from its last_seq.
type outbox struct {
	mu     sync.Mutex
	replay [][]byte
	live   [][]byte
	wake   chan struct{}
}

func newOutbox() *outbox { return &outbox{wake: make(chan struct{}, 1)} }

func (o *outbox) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

func (o *outbox) pushReplay(raw []byte) {
	o.mu.Lock()
	o.replay = append(o.replay, raw)
	o.mu.Unlock()
	o.signal()
}

func (o *outbox) pushLive(raw []byte) bool {
	o.mu.Lock()
	if len(o.live) >= writeQueueDepth {
		o.mu.Unlock()
		return false
	}
	o.live = append(o.live, raw)
	o.mu.Unlock()
	o.signal()
	return true
}

func (o *outbox) liveLen() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.live)
}

// next blocks until a frame is ready or ctx ends.
func (o *outbox) next(ctx context.Context) ([]byte, bool) {
	for {
		var raw []byte
		o.mu.Lock()
		switch {
		case len(o.replay) > 0:
			raw, o.replay[0], o.replay = o.replay[0], nil, o.replay[1:]
		case len(o.live) > 0:
			raw, o.live[0], o.live = o.live[0], nil, o.live[1:]
		}
		o.mu.Unlock()
		if raw != nil {
			return raw, true
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-o.wake:
		}
	}
}

// hubConn is one authenticated member socket.
type hubConn struct {
	room     *room
	memberID string
	role     session.Role
	conn     *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	box      *outbox
	limiter  *bucket
	strikes  int // reader goroutine only
	hello    atomic.Bool

	closeOnce sync.Once

	// Guarded by room.mu.
	live       bool   // set once the HELLO replay has been queued
	docVersion uint64 // newest document version this member is known to hold
}

// sendEnvelope encodes env and queues it behind the connection's pending
// frames. It reports false, and starts closing the connection with 1013, when
// the bounded queue is full or the envelope cannot be encoded.
func (hc *hubConn) sendEnvelope(env WSEnvelope) bool {
	raw, err := Encode(env)
	if err != nil {
		return false
	}
	return hc.enqueue(raw)
}

func (hc *hubConn) enqueue(raw []byte) bool {
	if raw == nil {
		return false
	}
	if hc.box.pushLive(raw) {
		return true
	}
	hc.shutdown(closeTryAgain, "write queue overflow")
	return false
}

// shutdown closes the socket exactly once. The close handshake runs off the
// caller's goroutine so a slow peer never stalls the room lock.
func (hc *hubConn) shutdown(code websocket.StatusCode, reason string) {
	hc.closeOnce.Do(func() {
		go func() {
			_ = hc.conn.Close(code, reason)
			hc.cancel()
		}()
	})
}

func (h *Hub) handleWS(w http.ResponseWriter, r *http.Request) {
	if !h.limitIP(w, r) {
		return
	}
	secret := bearer(r)
	if secret == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="plan-forge-relay"`)
		httpError(w, http.StatusUnauthorized, "Authorization: Bearer required")
		return
	}
	p, ok := h.lookup(secret)
	if !ok {
		httpError(w, http.StatusUnauthorized, "credential rejected")
		return
	}
	if p.kind == kindInvite {
		httpError(w, http.StatusUnauthorized, "invite secrets cannot open /ws; join first")
		return
	}
	// The session query parameter is advisory: identity comes from the secret.
	// A mismatch still fails closed rather than silently joining another room.
	if q := r.URL.Query().Get("session"); q != "" && q != p.room.id {
		httpError(w, http.StatusUnauthorized, "credential rejected")
		return
	}
	rm := p.room
	role := session.RoleOwner
	if p.kind == kindMember {
		rm.mu.Lock()
		m := rm.members[p.id]
		if m != nil {
			role = m.role
		}
		rm.mu.Unlock()
		if m == nil {
			httpError(w, http.StatusUnauthorized, "credential rejected")
			return
		}
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    []string{subprotocol},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return // Accept already answered the handshake failure
	}
	if c.Subprotocol() != subprotocol {
		_ = c.Close(closePolicy, "subprotocol plan-forge.v1 required")
		return
	}
	c.SetReadLimit(MaxFrameBytes)

	ctx, cancel := context.WithCancel(context.Background())
	hc := &hubConn{
		room: rm, memberID: p.id, role: role, conn: c, ctx: ctx, cancel: cancel,
		box: newOutbox(), limiter: newBucket(connMsgRate, connMsgBurst, h.clock()),
	}
	if !rm.attach(hc) {
		_ = c.Close(closePolicy, "session is closed or full")
		cancel()
		return
	}
	go h.writePump(hc)
	go h.heartbeat(hc)
	h.readLoop(hc)
	rm.detach(hc)
	hc.shutdown(closeNormal, "bye")
}

func (rm *room) attach(hc *hubConn) bool {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.closed || len(rm.conns) >= maxConnsPerRoom {
		return false
	}
	rm.conns[hc] = struct{}{}
	return true
}

func (rm *room) detach(hc *hubConn) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	delete(rm.conns, hc)
	rm.refreshPresenceLocked(hc.memberID)
}

// writePump is the connection's single writer: one frame at a time, each with
// a 5 s deadline.
func (h *Hub) writePump(hc *hubConn) {
	for {
		raw, ok := hc.box.next(hc.ctx)
		if !ok {
			return
		}
		wctx, cancel := context.WithTimeout(hc.ctx, writeDeadline)
		err := hc.conn.Write(wctx, websocket.MessageText, raw)
		cancel()
		if err != nil {
			_ = hc.conn.CloseNow()
			hc.cancel()
			return
		}
	}
}

// heartbeat pings every 15 s and drops the connection when no pong arrives
// within 10 s. The reader goroutine keeps reading, which is what lets the
// library observe the pong.
func (h *Hub) heartbeat(hc *hubConn) {
	tick := time.NewTicker(pingInterval)
	defer tick.Stop()
	for {
		select {
		case <-hc.ctx.Done():
			return
		case <-tick.C:
			pctx, cancel := context.WithTimeout(hc.ctx, pongTimeout)
			err := hc.conn.Ping(pctx)
			cancel()
			if err != nil {
				_ = hc.conn.CloseNow()
				hc.cancel()
				return
			}
		}
	}
}

// readLoop is the connection's single reader. It always reads until the
// socket closes; frames are handled inline because handling never blocks on
// the network (writes go to the outbox).
func (h *Hub) readLoop(hc *hubConn) {
	rm := hc.room
	deadline := time.AfterFunc(helloDeadline, func() {
		if !hc.hello.Load() {
			hc.shutdown(closePolicy, "HELLO required within 10s")
		}
	})
	defer deadline.Stop()
	for {
		typ, data, err := hc.conn.Read(hc.ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			hc.shutdown(closeUnsupported, "text frames only")
			return
		}
		now := h.clock()
		if !hc.limiter.allow(now) || !rm.msgLimit.allow(now) {
			hc.strikes++
			if hc.strikes >= rateStrikeLimit {
				hc.shutdown(closePolicy, string(contracts.CodeRateLimited))
				return
			}
			rm.mu.Lock()
			rm.replyError(hc, "", contracts.CodeRateLimited, "too many frames; slow down")
			rm.mu.Unlock()
			continue
		}
		env, err := DecodeEnvelope(data)
		if err != nil {
			reason := string(contracts.CodeOf(err))
			if contracts.CodeOf(err) == contracts.CodeUnsupportedVer {
				reason = closeProtoVersion
			}
			hc.shutdown(closePolicy, reason)
			return
		}
		if env.SessionID != rm.id {
			hc.shutdown(closePolicy, "session_id does not match this connection")
			return
		}
		if !hc.hello.Load() && env.EventType != EventHello {
			hc.shutdown(closePolicy, "HELLO must be the first frame")
			return
		}
		h.handleEnvelope(hc, env)
	}
}

// Commands the reducer owns. Everything else a client may send is handled by
// the hub directly (doc channel, presence, sync reports, owner membership
// controls).
var reducerCommands = map[WSEventType]bool{
	EventFreezeRequest: true, EventLeaseClaim: true, EventLeaseRenew: true, EventLeaseRelease: true,
	EventCycleStart: true, EventAvatarThought: true, EventAvatarDraft: true, EventAvatarCritique: true,
	EventSynthesizeRequest: true, EventRetryStage: true, EventDecisionPrompt: true,
	EventDecisionResolve: true, EventApprove: true, EventCancel: true, EventEditBaseline: true,
	EventArtifactSaved: true, EventAmbiguityResolve: true,
}

// Executor-emitted events carry the epoch the executor believes is current;
// the authority drops a mismatch with FENCED before the reducer sees it.
var epochChecked = map[WSEventType]bool{
	EventCycleStart: true, EventAvatarThought: true, EventAvatarDraft: true, EventAvatarCritique: true,
	EventSynthesizeRequest: true, EventRetryStage: true, EventDecisionPrompt: true,
	EventApprove: true, EventArtifactSaved: true, EventLeaseRenew: true,
}

// Events a client may forward unchanged when the reducer accepts them but has
// no broadcast of its own.
var forwardAsIs = map[WSEventType]bool{
	EventCycleStart: true, EventRetryStage: true, EventAmbiguityResolve: true,
}

func (h *Hub) handleEnvelope(hc *hubConn, env WSEnvelope) {
	rm := hc.room
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.lastActive.Store(h.clock().UnixNano())
	switch env.EventType {
	case EventHello:
		rm.onHello(hc, env)
	case EventReplayRequest:
		rm.onReplayRequest(hc, env)
	case EventTextDelta:
		rm.onTextDelta(hc, env)
	case EventTextCheck:
		rm.onTextCheck(hc, env)
	case EventIngestAppend:
		rm.onIngestAppend(hc, env)
	case EventPresence:
		rm.onPresence(hc, env)
	case EventArtifactSynced:
		rm.onArtifactSynced(hc, env)
	case EventInviteRevoke:
		rm.onInviteRevoke(hc, env)
	case EventMemberRemove:
		rm.onMemberRemove(hc, env)
	case EventExecutorLeft:
		rm.onExecutorLeft(hc, env)
	default:
		if !reducerCommands[env.EventType] {
			rm.replyError(hc, env.EventID, contracts.CodeInvalidPayload,
				"event type "+string(env.EventType)+" is not accepted from clients")
			return
		}
		rm.onReducerCommand(hc, env)
	}
}

// --- framing and fan-out (caller holds rm.mu) -----------------------------------------

// payloadFor turns an arbitrary payload into wire JSON. Reducer events carry
// numeric counters in maps; the protocol carries counters as decimal strings,
// so known counter keys are quoted here. Raw client payloads pass through.
func payloadFor(p any) json.RawMessage {
	switch v := p.(type) {
	case nil:
		return json.RawMessage(`{}`)
	case json.RawMessage:
		if len(v) == 0 {
			return json.RawMessage(`{}`)
		}
		return v
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return stringifyCounters(raw)
}

// counterKeys are the payload fields the protocol carries as decimal strings.
var counterKeys = map[string]bool{
	"expected_revision": true, "approved_revision": true, "baseline_rev": true,
	"lease_epoch": true, "epoch": true, "current_epoch": true, "input_revision": true,
	"synthesis_rev": true, "draft_revision": true, "head_version": true, "from_version": true,
	"version": true, "last_seq": true, "doc_version": true, "head_seq": true,
	"head_doc_version": true, "revision": true,
}

func stringifyCounters(raw json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return raw
	}
	changed := false
	for k, v := range obj {
		if counterKeys[k] && len(v) > 0 && v[0] >= '0' && v[0] <= '9' {
			obj[k] = json.RawMessage(strconv.Quote(string(v)))
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}

// frame builds one encoded envelope. The authority alone assigns sender, seq
// and timestamp; revision and epoch come from the reducer's current state.
func (rm *room) frame(t WSEventType, sender, eventID string, seq uint64, payload json.RawMessage) []byte {
	snap := rm.reducer.Snapshot()
	if eventID == "" {
		eventID = newUUID()
	}
	raw, err := Encode(WSEnvelope{
		Version: contracts.ProtocolVersion, SessionID: rm.id, SenderID: sender, EventID: eventID,
		EventType: t, Seq: seq, Revision: snap.Revision, Epoch: snap.LeaseEpoch,
		Timestamp: rm.hub.clock().UTC(), Payload: payload,
	})
	if err != nil {
		rm.hub.logger.Warn("dropping unencodable frame", "event", string(t), "err", err)
		return nil
	}
	return raw
}

// broadcast sequences one control event into the ring and fans it out to every
// live connection. A receiver whose queue is full is dropped (1013) and
// resyncs; the others are unaffected.
func (rm *room) broadcast(t WSEventType, sender string, payload json.RawMessage) (uint64, []byte) {
	seq := rm.seq + 1
	raw := rm.frame(t, sender, "", seq, payload)
	if raw == nil {
		return 0, nil
	}
	rm.seq = seq
	rm.ring = append(rm.ring, ringEntry{seq: seq, raw: raw})
	if len(rm.ring) > replayWindow {
		rm.ring[0] = ringEntry{}
		rm.ring = rm.ring[len(rm.ring)-replayWindow:]
	}
	for hc := range rm.conns {
		if hc.live && !hc.enqueue(raw) {
			delete(rm.conns, hc)
		}
	}
	return seq, raw
}

// unicast sends one out-of-sequence reply (seq 0) to a single connection. The
// frame echoes the command's event_id so the sender can correlate it. It
// returns the encoded frame for the reply cache.
func (rm *room) unicast(hc *hubConn, replyTo string, t WSEventType, payload any) []byte {
	raw := rm.frame(t, authorityID, replyTo, 0, payloadFor(payload))
	if raw != nil {
		hc.enqueue(raw)
	}
	return raw
}

func (rm *room) replyError(hc *hubConn, replyTo string, code contracts.ErrorCode, msg string) []byte {
	return rm.unicast(hc, replyTo, EventError, ErrorPayload{Code: code, Message: msg})
}

func (rm *room) sendFenced(hc *hubConn, replyTo string, epoch uint64) []byte {
	return rm.unicast(hc, replyTo, EventFenced, FencedPayload{CurrentEpoch: epoch, RunID: rm.reducer.Snapshot().RunID})
}

func (rm *room) cacheReply(key string, frames ...[]byte) {
	kept := frames[:0:0]
	for _, f := range frames {
		if f != nil {
			kept = append(kept, f)
		}
	}
	if _, exists := rm.replies[key]; !exists {
		rm.replyOrder = append(rm.replyOrder, key)
		if len(rm.replyOrder) > maxReplyCache {
			delete(rm.replies, rm.replyOrder[0])
			rm.replyOrder = rm.replyOrder[1:]
		}
	}
	rm.replies[key] = kept
}

// resendReply replays the original answer to a retried command.
func (rm *room) resendReply(hc *hubConn, key string) {
	for _, f := range rm.replies[key] {
		hc.enqueue(f)
	}
}

func replyKey(hc *hubConn, env WSEnvelope) string { return hc.memberID + "|" + env.EventID }

func (rm *room) setEphemeral(key string, t WSEventType, sender string, payload json.RawMessage) {
	rm.eph[key] = ephFrame{t: t, sender: sender, payload: payload}
}

// flushEphemeral delivers the latest value of each ephemeral slot (presence,
// avatar progress). Slots are coalesced between ticks, carry seq 0 and are
// never stored in the ring, so a lagging client simply misses stale values.
func (rm *room) flushEphemeral() {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if len(rm.eph) == 0 {
		return
	}
	for _, key := range sortedKeys(rm.eph) {
		ef := rm.eph[key]
		raw := rm.frame(ef.t, ef.sender, "", 0, ef.payload)
		if raw == nil {
			continue
		}
		for hc := range rm.conns {
			if hc.live && !hc.enqueue(raw) {
				delete(rm.conns, hc)
			}
		}
	}
	rm.eph = map[string]ephFrame{}
}

func (rm *room) refreshPresenceLocked(id string) {
	st := rm.presence[id]
	if st == nil {
		return
	}
	online := false
	for c := range rm.conns {
		if c.memberID == id && c.hello.Load() {
			online = true
			break
		}
	}
	if st.Online == online {
		return
	}
	st.Online = online
	rm.setEphemeral("presence:"+id, EventPresence, authorityID, payloadFor(st))
}

// --- session start, resync ------------------------------------------------------------------

func (rm *room) snapshotPayload(hc *hubConn) StateSnapshotPayload {
	snap := rm.reducer.Snapshot()
	pending := append([]string(nil), snap.PendingDecisions...)
	sort.Strings(pending)
	hashes := make(map[string]string, len(rm.draftHash))
	for k, v := range rm.draftHash {
		hashes[k] = v
	}
	return StateSnapshotPayload{
		MemberID: hc.memberID, Role: Role(hc.role), HeadSeq: rm.seq,
		HeadDocVersion: rm.doc.Snapshot().Version, Phase: Phase(snap.Phase),
		BaselineRev: snap.BaselineRev, BaselineHash: snap.BaselineHash, RunID: snap.RunID,
		InputRevision: snap.InputRevision, SynthesisRev: snap.SynthesisRev,
		LeaseEpoch: snap.LeaseEpoch, LeaseHolder: snap.LeaseHolder,
		PendingDecisions: pending, DraftHashes: hashes,
	}
}

// missedFrames returns the control frames a client with lastSeq has not seen.
// A client that is inside the ring gets exactly the missing events. A fresh
// client (lastSeq 0), one beyond the ring, or one whose gap exceeds budget gets
// the sticky state instead: the latest draft per avatar, pending and resolved
// decisions, the run and approval markers. budget < 0 means unbounded.
func (rm *room) missedFrames(lastSeq uint64, budget int) [][]byte {
	if lastSeq >= rm.seq {
		return nil
	}
	if lastSeq > 0 && len(rm.ring) > 0 && rm.ring[0].seq <= lastSeq+1 {
		var out [][]byte
		for _, e := range rm.ring {
			if e.seq > lastSeq {
				out = append(out, e.raw)
			}
		}
		if budget < 0 || len(out) <= budget {
			return out
		}
	}
	entries := make([]stickyEntry, 0, len(rm.sticky))
	for _, e := range rm.sticky {
		if e.seq > lastSeq {
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].seq < entries[j].seq })
	out := make([][]byte, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.raw)
	}
	return out
}

// docCatchup returns the frame that brings a client at version have up to the
// document head: nothing when current, TEXT_UPDATES when the log still covers
// it, TEXT_SNAPSHOT otherwise.
func (rm *room) docCatchup(have uint64) ([]byte, uint64) {
	snap := rm.doc.Snapshot()
	if have == snap.Version {
		return nil, snap.Version
	}
	if have > 0 && have < snap.Version {
		if ups := rm.doc.UpdatesSince(have); len(ups) > 0 {
			return rm.frame(EventTextUpdates, authorityID, "", 0, payloadFor(TextUpdatesPayload{
				DocumentID: contracts.DocumentIDPRD, FromVersion: have, Updates: fromCollab(ups),
			})), snap.Version
		}
	}
	return rm.frame(EventTextSnapshot, authorityID, "", 0, payloadFor(TextSnapshotPayload{
		DocumentID: contracts.DocumentIDPRD, Version: snap.Version, Text: snap.Text, SHA256: snap.SHA256,
	})), snap.Version
}

func (rm *room) onHello(hc *hubConn, env WSEnvelope) {
	if hc.hello.Load() {
		rm.replyError(hc, env.EventID, contracts.CodeInvalidPayload, "HELLO was already received; send REPLAY_REQUEST to resync")
		return
	}
	var hp HelloPayload
	if err := json.Unmarshal(env.Payload, &hp); err != nil {
		hc.shutdown(closePolicy, "invalid HELLO payload")
		return
	}
	if hp.Proto != contracts.ProtocolVersion {
		hc.shutdown(closePolicy, closeProtoVersion)
		return
	}
	hc.hello.Store(true)

	if raw := rm.frame(EventStateSnapshot, authorityID, env.EventID, 0, payloadFor(rm.snapshotPayload(hc))); raw != nil {
		hc.box.pushReplay(raw)
	}
	for _, f := range rm.missedFrames(hp.LastSeq, -1) {
		hc.box.pushReplay(f)
	}
	catchup, head := rm.docCatchup(hp.DocVersion)
	if catchup != nil {
		hc.box.pushReplay(catchup)
	}
	hc.docVersion = head
	for _, id := range sortedKeys(rm.presence) {
		if raw := rm.frame(EventPresence, authorityID, "", 0, payloadFor(rm.presence[id])); raw != nil {
			hc.box.pushReplay(raw)
		}
	}
	hc.live = true

	st := rm.presence[hc.memberID]
	if st == nil {
		st = &presenceState{MemberID: hc.memberID, Role: string(hc.role)}
		rm.presence[hc.memberID] = st
	}
	st.Online = true
	rm.setEphemeral("presence:"+hc.memberID, EventPresence, authorityID, payloadFor(st))
}

type replayRequestPayload struct {
	LastSeq uint64 `json:"last_seq,string"`
}

// onReplayRequest resyncs a live connection: a fresh STATE_SNAPSHOT plus the
// missing events when they fit the queue headroom, the sticky state otherwise.
func (rm *room) onReplayRequest(hc *hubConn, env WSEnvelope) {
	var rp replayRequestPayload
	if err := json.Unmarshal(env.Payload, &rp); err != nil {
		rm.replyError(hc, env.EventID, contracts.CodeInvalidPayload, "REPLAY_REQUEST needs last_seq")
		return
	}
	if raw := rm.frame(EventStateSnapshot, authorityID, env.EventID, 0, payloadFor(rm.snapshotPayload(hc))); raw != nil {
		hc.enqueue(raw)
	}
	for _, f := range rm.missedFrames(rp.LastSeq, writeQueueDepth/2) {
		hc.enqueue(f)
	}
}

// --- document channel -------------------------------------------------------------------------

func toCollab(us []Update) []collab.Update {
	out := make([]collab.Update, len(us))
	for i, u := range us {
		out[i] = collab.Update{ClientID: u.ClientID, Changes: u.Changes}
	}
	return out
}

func fromCollab(us []collab.Update) []Update {
	out := make([]Update, len(us))
	for i, u := range us {
		out[i] = Update{ClientID: u.ClientID, Changes: u.Changes}
	}
	return out
}

// onTextDelta pushes a batch of uncomposed updates through the reducer's
// role/phase gate and then the OT authority. A push after the baseline froze
// answers TEXT_REJECT{reason:"locked"}.
func (rm *room) onTextDelta(hc *hubConn, env WSEnvelope) {
	key := replyKey(hc, env)
	if len(env.Payload) > maxTextDeltaPayload {
		rm.replyError(hc, env.EventID, contracts.CodeInvalidPayload, "TEXT_DELTA exceeds 1 MiB")
		return
	}
	res := rm.reducer.Apply(session.Command{
		EventID: env.EventID, ActorID: hc.memberID, Type: string(env.EventType), Payload: env.Payload,
	})
	if res.Rejected != nil {
		if res.Rejected.Code == contracts.CodeIllegalPhase {
			head := rm.doc.Snapshot().Version
			rm.cacheReply(key, rm.unicast(hc, env.EventID, EventTextReject, TextRejectPayload{
				DocumentID: contracts.DocumentIDPRD, HeadVersion: head, Reason: "locked",
			}))
			return
		}
		rm.reject(hc, env.EventID, res)
		return
	}
	if res.Deduplicated {
		rm.resendReply(hc, key)
		return
	}
	var p TextDeltaPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.DocumentID != contracts.DocumentIDPRD ||
		len(p.Updates) == 0 || len(p.Updates) > maxTextDeltaUpdates {
		head := rm.doc.Snapshot().Version
		rm.cacheReply(key, rm.unicast(hc, env.EventID, EventTextReject, TextRejectPayload{
			DocumentID: p.DocumentID, HeadVersion: head, Reason: "invalid",
		}))
		return
	}
	pushed, rej := rm.doc.Push(hc.memberID, p.BaseVersion, toCollab(p.Updates))
	if rej != nil {
		rm.sendTextReject(hc, env.EventID, key, p.DocumentID, rej)
		return
	}
	ack := rm.unicast(hc, env.EventID, EventTextAck, TextAckPayload{
		DocumentID: p.DocumentID, Version: pushed.NewVersion,
	})
	rm.cacheReply(key, ack)
	hc.docVersion = pushed.NewVersion
	rm.fanOutDocUpdates(hc, p.BaseVersion, pushed.NewVersion, p.Updates)
}

// fanOutDocUpdates delivers accepted updates to every live connection except
// the pusher (which got TEXT_ACK). skip may be nil for system appends.
func (rm *room) fanOutDocUpdates(skip *hubConn, from, head uint64, updates []Update) {
	raw := rm.frame(EventTextUpdates, authorityID, "", 0, payloadFor(TextUpdatesPayload{
		DocumentID: contracts.DocumentIDPRD, FromVersion: from, Updates: updates,
	}))
	if raw == nil {
		return
	}
	for other := range rm.conns {
		if other == skip || !other.live {
			continue
		}
		other.docVersion = head
		if !other.enqueue(raw) {
			delete(rm.conns, other)
		}
	}
}

func (rm *room) sendTextReject(hc *hubConn, replyTo, key, docID string, rej *collab.Reject) {
	resync := rej.Resync || (rej.Reason == "stale_base" && len(rej.Updates) == 0)
	frames := [][]byte{rm.unicast(hc, replyTo, EventTextReject, TextRejectPayload{
		DocumentID: docID, HeadVersion: rej.HeadVersion, Updates: fromCollab(rej.Updates),
		Resync: resync, Reason: rej.Reason,
	})}
	if resync {
		snap := rm.doc.Snapshot()
		frames = append(frames, rm.unicast(hc, replyTo, EventTextSnapshot, TextSnapshotPayload{
			DocumentID: contracts.DocumentIDPRD, Version: snap.Version, Text: snap.Text, SHA256: snap.SHA256,
		}))
		hc.docVersion = snap.Version
	}
	rm.cacheReply(key, frames...)
}

// onTextCheck compares an idle client's (version, sha256) with the head. A
// match is silent; a mismatch forces TEXT_SNAPSHOT for that client only. Any
// role may check: viewers receive updates and can diverge too.
func (rm *room) onTextCheck(hc *hubConn, env WSEnvelope) {
	var p TextCheckPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil || p.DocumentID != contracts.DocumentIDPRD {
		rm.replyError(hc, env.EventID, contracts.CodeInvalidPayload, "TEXT_CHECK needs document_id prd, version and sha256")
		return
	}
	if rm.doc.Check(p.Version, p.SHA256) {
		hc.docVersion = p.Version
		return
	}
	snap := rm.doc.Snapshot()
	hc.docVersion = snap.Version
	rm.unicast(hc, env.EventID, EventTextSnapshot, TextSnapshotPayload{
		DocumentID: contracts.DocumentIDPRD, Version: snap.Version, Text: snap.Text, SHA256: snap.SHA256,
	})
}

// composeIngest renders an INGEST_APPEND as a separator heading plus body, with
// CRLF normalized because the OT authority refuses "\r".
func composeIngest(docEmpty bool, p IngestAppendPayload) string {
	body := strings.ReplaceAll(p.Markdown, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	body = strings.TrimRight(body, "\n")
	label := sanitizeLabel(p.SourceName)
	if label == "" {
		label = p.Source
	}
	var b strings.Builder
	if !docEmpty {
		b.WriteString("\n\n")
	}
	b.WriteString("## Source: ")
	b.WriteString(label)
	b.WriteString("\n\n")
	b.WriteString(body)
	b.WriteString("\n")
	return b.String()
}

// onIngestAppend appends parsed or composed text to the shared document. The
// reducer owns the role and phase rules; this adds the size cap, the
// per-session ingest_seq and the system client id. It never replaces text.
func (rm *room) onIngestAppend(hc *hubConn, env WSEnvelope) {
	key := replyKey(hc, env)
	res := rm.reducer.Apply(session.Command{
		EventID: env.EventID, ActorID: hc.memberID, Type: string(env.EventType), Payload: env.Payload,
	})
	if res.Rejected != nil {
		rm.reject(hc, env.EventID, res)
		return
	}
	if res.Deduplicated {
		rm.resendReply(hc, key)
		return
	}
	var p IngestAppendPayload
	err := json.Unmarshal(env.Payload, &p)
	switch {
	case err != nil:
	case p.Source != IngestSourceUpload && p.Source != IngestSourceComposer:
		err = errors.New("source must be upload or composer")
	case strings.TrimSpace(p.Markdown) == "":
		err = errors.New("markdown is empty")
	case len(p.Markdown) > MaxIngestAppendBytes:
		err = errors.New("markdown exceeds 256 KiB")
	case !utf8.ValidString(p.Markdown):
		err = errors.New("markdown is not valid UTF-8")
	}
	if err != nil {
		rm.cacheReply(key, rm.replyError(hc, env.EventID, contracts.CodeInvalidPayload, err.Error()))
		return
	}
	text := composeIngest(rm.doc.Snapshot().Text == "", p)
	// The reducer assigned the per-session ingest_seq and its client id.
	pushed, rej := rm.doc.Append(res.IngestClientID, text)
	if rej != nil {
		rm.cacheReply(key, rm.unicast(hc, env.EventID, EventTextReject, TextRejectPayload{
			DocumentID: contracts.DocumentIDPRD, HeadVersion: rej.HeadVersion, Reason: rej.Reason,
		}))
		return
	}
	rm.cacheReply(key)
	hc.docVersion = pushed.NewVersion
	// Every member, the appender included, applies a system append as a remote
	// update: nobody has a pending local update to confirm.
	rm.fanOutDocUpdates(nil, pushed.NewVersion-uint64(len(pushed.Applied)), pushed.NewVersion, fromCollab(pushed.Applied))
}

// --- presence, sync reports, owner controls ------------------------------------------------

func (rm *room) onPresence(hc *hubConn, env WSEnvelope) {
	var p struct {
		Page string `json:"page"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		rm.replyError(hc, env.EventID, contracts.CodeInvalidPayload, "PRESENCE needs a JSON object")
		return
	}
	st := rm.presence[hc.memberID]
	if st == nil {
		return
	}
	page := sanitizeLabel(p.Page)
	if r := []rune(page); len(r) > 32 {
		page = string(r[:32])
	}
	st.Page = page
	st.Online = true
	rm.setEphemeral("presence:"+hc.memberID, EventPresence, authorityID, payloadFor(st))
}

var artifactErrorCodes = map[string]bool{
	ArtifactEACCES: true, ArtifactENOSPC: true, ArtifactEEXIST: true, ArtifactEJOURNAL: true,
	ArtifactEHASH: true, ArtifactESYMLINK: true, ArtifactEOTHER: true,
}

var artifactRelPaths = map[string]bool{
	".plan/synthesized_spec.md": true, ".plan/plan_manifest.json": true,
}

// cleanArtifactError keeps only allowlisted codes and relative paths, so free
// text and absolute local paths can never ride a relayed event.
func cleanArtifactError(e *ArtifactError) *ArtifactError {
	if e == nil {
		return nil
	}
	out := &ArtifactError{Code: ArtifactEOTHER}
	if artifactErrorCodes[e.Code] {
		out.Code = e.Code
	}
	if artifactRelPaths[e.RelPath] {
		out.RelPath = e.RelPath
	}
	return out
}

// onArtifactSynced relays a follower's persistence report. Followers are not
// the executor, so it bypasses the reducer's lease gate; it is meaningful only
// after the plan was approved, and a failure never reverts the approval.
func (rm *room) onArtifactSynced(hc *hubConn, env WSEnvelope) {
	if phase := rm.reducer.Phase(); phase != session.PhaseApproved {
		rm.replyError(hc, env.EventID, contracts.CodeIllegalPhase, "ARTIFACT_SYNCED requires phase APPROVED, have "+string(phase))
		return
	}
	var p struct {
		OK    bool           `json:"ok"`
		Error *ArtifactError `json:"error,omitempty"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		rm.replyError(hc, env.EventID, contracts.CodeInvalidPayload, "ARTIFACT_SYNCED needs ok")
		return
	}
	out := map[string]any{"ok": p.OK}
	if !p.OK {
		out["error"] = cleanArtifactError(p.Error)
	}
	payload := payloadFor(out)
	seq, raw := rm.broadcast(EventArtifactSynced, hc.memberID, payload)
	if raw != nil {
		rm.sticky["synced:"+hc.memberID] = stickyEntry{seq: seq, raw: raw}
	}
}

func (rm *room) onInviteRevoke(hc *hubConn, env WSEnvelope) {
	if hc.role != session.RoleOwner {
		rm.replyError(hc, env.EventID, contracts.CodeRoleForbidden, "only the owner revokes invites")
		return
	}
	var p struct {
		InviteID string `json:"invite_id"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil || !rm.revokeInviteLocked(p.InviteID) {
		rm.replyError(hc, env.EventID, contracts.CodeInvalidPayload, "unknown invite_id")
		return
	}
	rm.unicast(hc, env.EventID, EventInviteRevoke, map[string]any{"invite_id": p.InviteID})
}

// onMemberRemove drops a member: their credential stops working, their sockets
// close with 1008, and the reducer forgets them.
func (rm *room) onMemberRemove(hc *hubConn, env WSEnvelope) {
	if hc.role != session.RoleOwner {
		rm.replyError(hc, env.EventID, contracts.CodeRoleForbidden, "only the owner removes members")
		return
	}
	var p struct {
		MemberID string `json:"member_id"`
	}
	m := (*member)(nil)
	if err := json.Unmarshal(env.Payload, &p); err == nil {
		m = rm.members[p.MemberID]
	}
	if m == nil {
		rm.replyError(hc, env.EventID, contracts.CodeInvalidPayload, "unknown member_id")
		return
	}
	delete(rm.members, m.id)
	rm.reducer.Leave(m.id)
	rm.hub.unindex(m.hash)
	for c := range rm.conns {
		if c.memberID == m.id {
			delete(rm.conns, c)
			c.shutdown(closePolicy, "removed by the owner")
		}
	}
	rm.refreshPresenceLocked(m.id)
	rm.unicast(hc, env.EventID, EventMemberRemove, map[string]any{"member_id": m.id})
}

type leaseLostPayload struct {
	Epoch uint64 `json:"epoch,string"`
}

type executorLostPayload struct {
	ExecutorID string `json:"executor_id"`
	Epoch      uint64 `json:"epoch,string"`
}

// loseExecutor records that the executor is gone: mid-approval it moves the
// session to APPROVING_UNKNOWN (resolved idempotently when an executor returns
// and reports ARTIFACT_SAVED), and every member sees LEASE_LOST/EXECUTOR_LOST.
func (rm *room) loseExecutor(holder string, epoch uint64, announceLeaseLost bool) {
	before := rm.reducer.Phase()
	rm.reducer.ExecutorLost(holder)
	if after := rm.reducer.Phase(); after != before {
		rm.broadcast(EventPageTransition, authorityID, payloadFor(map[string]any{
			"from": string(before), "to": string(after),
			"expected_revision": rm.reducer.Snapshot().Revision,
		}))
	}
	if announceLeaseLost {
		rm.broadcast(EventLeaseLost, authorityID, payloadFor(leaseLostPayload{Epoch: epoch}))
	}
	rm.broadcast(EventExecutorLost, authorityID, payloadFor(executorLostPayload{ExecutorID: holder, Epoch: epoch}))
}

// onExecutorLeft handles the executor's own goodbye after its window stayed
// gone past the grace period: the lease is released and members are told.
func (rm *room) onExecutorLeft(hc *hubConn, env WSEnvelope) {
	holder, epoch, valid := rm.reducer.LeaseSnapshot()
	if hc.role != session.RoleOwner || holder != hc.memberID || !valid {
		rm.sendFenced(hc, env.EventID, rm.reducer.Snapshot().LeaseEpoch)
		return
	}
	rm.lostEpoch = epoch
	rm.loseExecutor(holder, epoch, true)
	rm.reducer.Apply(session.Command{EventID: newUUID(), ActorID: hc.memberID, Type: string(EventLeaseRelease)})
}

// expireLease announces a lease that ran out without renewal. The authority
// expires leases but never elects a new holder.
func (rm *room) expireLease() {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	holder, epoch, valid := rm.reducer.LeaseSnapshot()
	if holder == "" || valid || rm.lostEpoch == epoch {
		return
	}
	rm.lostEpoch = epoch
	rm.loseExecutor(holder, epoch, true)
}

// --- reducer commands ------------------------------------------------------------------------------

func (rm *room) onReducerCommand(hc *hubConn, env WSEnvelope) {
	snap := rm.reducer.Snapshot()
	if epochChecked[env.EventType] {
		epoch := env.Epoch
		if env.EventType == EventLeaseRenew {
			var lp leaseLostPayload
			if json.Unmarshal(env.Payload, &lp) == nil {
				epoch = lp.Epoch
			}
		}
		if epoch != snap.LeaseEpoch {
			rm.sendFenced(hc, env.EventID, snap.LeaseEpoch)
			return
		}
	}
	if env.EventType == EventArtifactSaved {
		payload, ok := rm.checkArtifactSaved(hc, env, snap)
		if !ok {
			return
		}
		env.Payload = payload
	}
	res := rm.reducer.Apply(session.Command{
		EventID: env.EventID, ActorID: hc.memberID, Type: string(env.EventType), Payload: env.Payload,
	})
	rm.finish(hc, env, snap.ApprovalID, res)
}

// checkArtifactSaved enforces that ARTIFACT_SAVED{ok} reports exactly the bytes
// the relay received over PUT. The reducer then sees canonical lowercase hashes.
func (rm *room) checkArtifactSaved(hc *hubConn, env WSEnvelope, snap session.Snapshot) (json.RawMessage, bool) {
	var p ArtifactSavedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		rm.replyError(hc, env.EventID, contracts.CodeInvalidPayload, "ARTIFACT_SAVED payload is malformed")
		return nil, false
	}
	if !p.OK || snap.Phase == session.PhaseApproved {
		return env.Payload, true
	}
	art := rm.artifacts[snap.ApprovalID]
	if art == nil || art.spec == nil || art.manifest == nil {
		rm.replyError(hc, env.EventID, contracts.CodeApprovalFailed, "both artifacts must be uploaded before ARTIFACT_SAVED ok")
		return nil, false
	}
	if !strings.EqualFold(p.SpecSHA256, art.specHash) || !strings.EqualFold(p.ManifestSHA256, art.manifestHash) {
		rm.replyError(hc, env.EventID, contracts.CodeApprovalFailed, "reported hashes differ from the uploaded artifacts")
		return nil, false
	}
	canon, err := json.Marshal(ArtifactSavedPayload{OK: true, SpecSHA256: art.specHash, ManifestSHA256: art.manifestHash})
	if err != nil {
		rm.replyError(hc, env.EventID, contracts.CodeInternal, "could not canonicalize ARTIFACT_SAVED")
		return nil, false
	}
	return canon, true
}

// finish turns a reducer Result into wire effects: a coded rejection to the
// submitter, or document-state side effects, sequenced broadcasts and the
// submitter's acknowledgement.
func (rm *room) finish(hc *hubConn, env WSEnvelope, approvalID string, res session.Result) {
	if res.Rejected != nil {
		rm.reject(hc, env.EventID, res)
		return
	}
	if res.Deduplicated {
		// A retried command gets its original acknowledgement and nothing is
		// broadcast twice.
		if res.Ack.Type != "" {
			rm.unicast(hc, env.EventID, WSEventType(res.Ack.Type), res.Ack.Payload)
		}
		return
	}

	switch env.EventType {
	case EventFreezeRequest:
		rm.doc.Freeze()
	case EventEditBaseline:
		rm.doc.Unfreeze()
		rm.clearRunSticky()
	case EventCancel:
		time.AfterFunc(cancelCloseDelay, func() { rm.hub.closeRoom(rm, closeNormal, "session cancelled") })
	case EventLeaseRelease:
		if res.Epoch != 0 && rm.lostEpoch != res.Epoch {
			rm.lostEpoch = res.Epoch
			rm.broadcast(EventLeaseLost, authorityID, payloadFor(leaseLostPayload{Epoch: res.Epoch}))
		}
	case EventArtifactSaved:
		var p ArtifactSavedPayload
		if json.Unmarshal(env.Payload, &p) == nil && !p.OK {
			delete(rm.artifacts, approvalID)
		}
	}

	emitted := map[string]bool{}
	for _, ev := range res.Broadcast {
		rm.emit(hc, env, ev)
		emitted[ev.Type] = true
	}
	if len(res.Broadcast) == 0 {
		switch {
		case env.EventType == EventAvatarThought:
			rm.noteThought(hc, env)
		case forwardAsIs[env.EventType]:
			seq, raw := rm.broadcast(env.EventType, hc.memberID, env.Payload)
			rm.noteSticky(env.EventType, hc.memberID, seq, raw, env.Payload)
		}
	}
	switch {
	case res.Ack.Type != "" && !emitted[res.Ack.Type]:
		rm.unicast(hc, env.EventID, WSEventType(res.Ack.Type), res.Ack.Payload)
	case env.EventType == EventLeaseRenew && res.Ack.Type == "":
		// The executor self-fences when no renewal ack arrives within TTL-3 s,
		// so a successful renewal is always answered.
		holder, epoch, _ := rm.reducer.LeaseSnapshot()
		rm.unicast(hc, env.EventID, EventLeaseRenew, LeaseGrantedPayload{
			Epoch: epoch, TTLMS: session.LeaseTTL.Milliseconds(), Holder: holder,
		})
	}
}

// reject answers a refused command. FENCED and ALREADY_RESOLVED are their own
// event types; everything else is an ERROR carrying the reducer's code.
func (rm *room) reject(hc *hubConn, replyTo string, res session.Result) {
	sent := map[string]bool{}
	for _, ev := range append([]session.Event{res.Ack}, res.Broadcast...) {
		if (ev.Type == string(EventFenced) || ev.Type == string(EventAlreadyResolved)) && !sent[ev.Type] {
			rm.unicast(hc, replyTo, WSEventType(ev.Type), ev.Payload)
			sent[ev.Type] = true
		}
	}
	if len(sent) > 0 {
		return
	}
	if res.Rejected.Code == contracts.CodeFenced {
		rm.sendFenced(hc, replyTo, res.Epoch)
		return
	}
	rm.replyError(hc, replyTo, res.Rejected.Code, res.Rejected.Msg)
}

// memberSent lists the event types whose sender is the acting member; every
// other reducer effect is attributed to the authority.
var memberSent = map[WSEventType]bool{
	EventAvatarDraft: true, EventAvatarCritique: true, EventDecisionPrompt: true,
	EventDecisionResolve: true, EventLeaseGranted: true, EventCycleStart: true,
}

// emit sequences one reducer effect.
func (rm *room) emit(hc *hubConn, env WSEnvelope, ev session.Event) {
	t := WSEventType(ev.Type)
	if ev.UnicastTo != "" || t == EventFenced || t == EventAlreadyResolved {
		rm.unicast(hc, env.EventID, t, ev.Payload)
		return
	}
	payload := ev.Payload
	switch t {
	case EventPlanApproved:
		if m, ok := payload.(map[string]any); ok {
			spec, _ := m["spec_sha256"].(string)
			manifest, _ := m["manifest_sha256"].(string)
			if id, ok := bundleID(spec, manifest); ok {
				m["bundle_id"] = id
			}
		}
	case EventError:
		if env.EventType == EventArtifactSaved {
			payload = rm.artifactFailure(env)
		} else {
			payload = errorPayloadFrom(payload)
		}
	}
	sender := authorityID
	if memberSent[t] {
		sender = hc.memberID
	}
	wire := payloadFor(payload)
	seq, raw := rm.broadcast(t, sender, wire)
	rm.noteSticky(t, sender, seq, raw, wire)
}

func errorPayloadFrom(p any) ErrorPayload {
	raw, _ := json.Marshal(p)
	var out ErrorPayload
	_ = json.Unmarshal(raw, &out)
	if out.Code == "" {
		out.Code = contracts.CodeInternal
	}
	if out.Message == "" {
		out.Message = string(out.Code)
	}
	return out
}

// artifactFailure builds the public face of a failed artifact write: a code and
// a repository-relative path from fixed allowlists, never free text.
func (rm *room) artifactFailure(env WSEnvelope) ErrorPayload {
	var p ArtifactSavedPayload
	_ = json.Unmarshal(env.Payload, &p)
	clean := cleanArtifactError(p.Error)
	if clean == nil {
		clean = &ArtifactError{Code: ArtifactEOTHER}
	}
	return ErrorPayload{
		Code: contracts.CodeApprovalFailed, Message: "artifact write failed: " + clean.Code, RelPath: clean.RelPath,
	}
}

// bundleID is the hex SHA-256 over the 32 raw spec-digest bytes followed by the
// 32 raw manifest-digest bytes.
func bundleID(specHex, manifestHex string) (string, bool) {
	s, e1 := hex.DecodeString(specHex)
	m, e2 := hex.DecodeString(manifestHex)
	if e1 != nil || e2 != nil || len(s) != sha256.Size || len(m) != sha256.Size {
		return "", false
	}
	sum := sha256.Sum256(append(append(make([]byte, 0, 2*sha256.Size), s...), m...))
	return hex.EncodeToString(sum[:]), true
}

// noteThought stores the latest AVATAR_THOUGHT per avatar in an ephemeral slot.
func (rm *room) noteThought(hc *hubConn, env WSEnvelope) {
	var p struct {
		AvatarID string `json:"avatar_id"`
	}
	_ = json.Unmarshal(env.Payload, &p)
	rm.setEphemeral("thought:"+sanitizeLabel(p.AvatarID), EventAvatarThought, hc.memberID, env.Payload)
}

// noteSticky keeps the state a resyncing client needs beyond the 2k ring.
func (rm *room) noteSticky(t WSEventType, sender string, seq uint64, raw []byte, payload json.RawMessage) {
	if raw == nil {
		return
	}
	entry := stickyEntry{seq: seq, raw: raw}
	var p struct {
		AvatarID    string `json:"avatar_id"`
		SchemaID    string `json:"schema_id"`
		ContentHash string `json:"content_hash"`
		DecisionID  string `json:"decision_id"`
	}
	_ = json.Unmarshal(payload, &p)
	switch t {
	case EventAvatarDraft:
		rm.sticky["draft:"+p.AvatarID+":"+p.SchemaID] = entry
		rm.draftHash[p.AvatarID] = p.ContentHash
	case EventAvatarCritique:
		rm.sticky["critique"] = entry
	case EventDecisionPrompt:
		rm.sticky["decision:"+p.DecisionID] = entry
	case EventDecisionResolve:
		rm.sticky["resolve:"+p.DecisionID] = entry
	case EventRunAssigned:
		rm.clearRunSticky()
		rm.sticky["type:"+string(t)] = entry
	case EventCycleStart, EventApprovalPrepared, EventPlanApproved, EventPageTransition:
		rm.sticky["type:"+string(t)] = entry
	}
}

// clearRunSticky forgets the previous run's drafts, critiques and decisions.
func (rm *room) clearRunSticky() {
	for k := range rm.sticky {
		if strings.HasPrefix(k, "draft:") || strings.HasPrefix(k, "decision:") ||
			strings.HasPrefix(k, "resolve:") || k == "critique" || k == "type:"+string(EventCycleStart) {
			delete(rm.sticky, k)
		}
	}
	rm.draftHash = map[string]string{}
}

// --- hub lifecycle ----------------------------------------------------------------------------------

func (h *Hub) snapshotRooms() []*room {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*room, 0, len(h.rooms))
	for _, rm := range h.rooms {
		out = append(out, rm)
	}
	return out
}

// closeRoom closes every socket, forgets the room's credentials and removes it.
func (h *Hub) closeRoom(rm *room, code websocket.StatusCode, reason string) {
	rm.mu.Lock()
	rm.closed = true
	conns := make([]*hubConn, 0, len(rm.conns))
	for hc := range rm.conns {
		conns = append(conns, hc)
	}
	hashes := append([][32]byte(nil), rm.hashes...)
	rm.mu.Unlock()
	for _, hc := range conns {
		hc.shutdown(code, reason)
	}
	h.mu.Lock()
	delete(h.rooms, rm.id)
	for _, hash := range hashes {
		delete(h.byHash, hash)
	}
	h.mu.Unlock()
}

// CloseExpired purges rooms idle past the retention window.
func (h *Hub) CloseExpired() {
	now := h.clock()
	for _, rm := range h.snapshotRooms() {
		if now.Sub(time.Unix(0, rm.lastActive.Load())) > roomIdleTTL {
			h.closeRoom(rm, closeGoingAway, "session expired")
		}
	}
}

// Shutdown closes every connection so the process can exit promptly.
func (h *Hub) Shutdown() {
	for _, rm := range h.snapshotRooms() {
		rm.mu.Lock()
		conns := make([]*hubConn, 0, len(rm.conns))
		for hc := range rm.conns {
			conns = append(conns, hc)
		}
		rm.mu.Unlock()
		for _, hc := range conns {
			hc.shutdown(closeGoingAway, "relay shutting down")
		}
	}
}

// gc runs the slow maintenance: document-log compaction against the slowest
// connected member and artifact retention.
func (rm *room) gc(now time.Time) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	first, minAck := true, uint64(0)
	for hc := range rm.conns {
		if !hc.live {
			continue
		}
		if first || hc.docVersion < minAck {
			minAck, first = hc.docVersion, false
		}
	}
	if !first {
		rm.doc.CompactBelow(minAck)
	}
	for id, a := range rm.artifacts {
		if now.Sub(a.saved) > artifactRetention {
			delete(rm.artifacts, id)
		}
	}
}

// Run drives the hub's background work until ctx ends: the 100 ms ephemeral
// flush, per-second lease expiry, and slow maintenance (log compaction,
// artifact retention, idle-room purge, limiter pruning). It calls Shutdown on
// the way out.
func (h *Hub) Run(ctx context.Context) {
	eph := time.NewTicker(ephemeralInterval)
	secs := time.NewTicker(time.Second)
	slow := time.NewTicker(30 * time.Second)
	defer eph.Stop()
	defer secs.Stop()
	defer slow.Stop()
	for {
		select {
		case <-ctx.Done():
			h.Shutdown()
			return
		case <-eph.C:
			for _, rm := range h.snapshotRooms() {
				rm.flushEphemeral()
			}
		case <-secs.C:
			for _, rm := range h.snapshotRooms() {
				rm.expireLease()
			}
		case <-slow.C:
			now := h.clock()
			for _, rm := range h.snapshotRooms() {
				rm.gc(now)
			}
			h.CloseExpired()
			h.pruneLimiters()
		}
	}
}
