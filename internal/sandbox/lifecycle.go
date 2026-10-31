package sandbox

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ReceiptKind names one recorded command or probe on the lease.
type ReceiptKind string

// Receipt kinds. Build and test receipts come from the recorded build/test
// commands; the readiness receipt comes from the declared readiness probe,
// never from a merely opened port.
const (
	ReceiptBuild     ReceiptKind = "build"
	ReceiptTest      ReceiptKind = "test"
	ReceiptReadiness ReceiptKind = "readiness"
	ReceiptRun       ReceiptKind = "run"
	ReceiptStop      ReceiptKind = "stop"
	ReceiptCleanup   ReceiptKind = "cleanup"
)

// Receipt is one bounded, exact exit-code record from the prepared VM.
type Receipt struct {
	Kind      ReceiptKind
	Command   string
	ExitCode  int
	StartedAt time.Time
	EndedAt   time.Time
}

// OK reports whether the receipt records a successful step.
func (r Receipt) OK() bool { return r.ExitCode == 0 }

// Lifecycle is the persisted lease record. It lives outside the guest: the
// untrusted application never sees or influences these fields.
type Lifecycle struct {
	SandboxID    string
	OperationID  string
	Repository   string
	PR           string
	HeadSHA      string
	ApprovalID   string
	RecipeHash   string
	BundleDigest string
	ExpiresAt    time.Time
	State        State
	Receipts     []Receipt
	LastError    string
}

// ErrNotReady reports that a lease was driven to StateReady without the
// receipts that justify it. A green dashboard without evidence is the exact
// false-success the flow exists to prevent.
var ErrNotReady = errors.New("sandbox lifecycle: ready state requires successful build, test and readiness receipts")

// Validate checks the record's internal consistency. It rejects a ready
// state that lacks a successful build receipt, a successful test receipt,
// and a readiness receipt — in that it refuses to present readiness that
// the recorded evidence does not show.
func (l Lifecycle) Validate() error {
	if l.State != StateReady && !l.State.Valid() {
		return fmt.Errorf("sandbox lifecycle: unknown state %q", l.State)
	}
	if l.State != StateReady {
		return nil
	}
	if !hasOKReceipt(l.Receipts, ReceiptBuild) {
		return fmt.Errorf("%w: no successful build receipt", ErrNotReady)
	}
	if !hasOKReceipt(l.Receipts, ReceiptTest) {
		return fmt.Errorf("%w: no successful test receipt", ErrNotReady)
	}
	if !hasOKReceipt(l.Receipts, ReceiptReadiness) {
		return fmt.Errorf("%w: no readiness receipt", ErrNotReady)
	}
	return nil
}

// hasOKReceipt reports whether at least one receipt of the kind recorded
// exit code 0.
func hasOKReceipt(receipts []Receipt, kind ReceiptKind) bool {
	for _, r := range receipts {
		if r.Kind == kind && r.OK() {
			return true
		}
	}
	return false
}

// ReconcileUnknown marks a transport failure at the caller: the provider's
// true state cannot be seen, so the record must say unknown rather than
// repeating a stale progress state.
func (l Lifecycle) ReconcileUnknown() Lifecycle {
	next := l
	next.State = StateUnknown
	return next
}

// MarkFailed records a bounded failure with its reason and moves the lease
// to failed unless it already reached a terminal state.
func (l Lifecycle) MarkFailed(reason string) Lifecycle {
	if l.State.Terminal() {
		return l
	}
	next := l
	next.State = StateFailed
	next.LastError = truncateReason(reason)
	return next
}

// AddReceipt appends one command receipt and drops the ready claim when the
// new evidence contradicts it: a lease that looked ready keeps that state
// only while its receipts still back it.
func (l Lifecycle) AddReceipt(r Receipt) Lifecycle {
	next := l
	next.Receipts = append(next.Receipts, Receipt{Kind: r.Kind, Command: r.Command, ExitCode: r.ExitCode, StartedAt: r.StartedAt, EndedAt: r.EndedAt})
	if next.State == StateReady && !next.readyEvidenceOK() {
		next.State = StateUnknown
		next.LastError = "receipt " + string(r.Kind) + " failed after ready state"
	}
	return next
}

// readyEvidenceOK reports whether the receipts still justify StateReady.
func (l Lifecycle) readyEvidenceOK() bool {
	return hasOKReceipt(l.Receipts, ReceiptBuild) &&
		hasOKReceipt(l.Receipts, ReceiptTest) &&
		hasOKReceipt(l.Receipts, ReceiptReadiness)
}

// truncateReason bounds a failure reason so a hostile provider message
// cannot bloat the persisted record.
func truncateReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > 512 {
		reason = reason[:512]
	}
	return reason
}
