package contracts

import (
	"errors"
	"fmt"
)

// ErrorCode is the single vocabulary of machine-readable failures shared by Go,
// the JSON schemas and the TypeScript client. Values are part of the wire
// contract: never rename one without versioning the protocol.
type ErrorCode string

// Codes returned on the wire and by CLI exit paths.
const (
	CodeEventIDConflict  ErrorCode = "EVENT_ID_CONFLICT"
	CodeStaleBaseline    ErrorCode = "STALE_BASELINE"
	CodeStaleRun         ErrorCode = "STALE_RUN"
	CodeStaleSynthesis   ErrorCode = "STALE_SYNTHESIS"
	CodeDecisionsPending ErrorCode = "DECISIONS_PENDING"
	CodeBlockersOpen     ErrorCode = "BLOCKERS_OPEN"
	CodeNotApprover      ErrorCode = "NOT_APPROVER"
	CodeFenced           ErrorCode = "FENCED"
	CodeRunSuperseded    ErrorCode = "RUN_SUPERSEDED"
	CodeAlreadyResolved  ErrorCode = "ALREADY_RESOLVED"
	CodeApprovalFailed   ErrorCode = "APPROVAL_FAILED"
	CodeLocked           ErrorCode = "LOCKED"
	CodeUnsupportedVer   ErrorCode = "UNSUPPORTED_VERSION"
	CodeRateLimited      ErrorCode = "RATE_LIMITED"
	CodeRoleForbidden    ErrorCode = "ROLE_FORBIDDEN"
	CodeIllegalPhase     ErrorCode = "ILLEGAL_PHASE"
	CodeInvalidPayload   ErrorCode = "INVALID_PAYLOAD"
	CodeInternal         ErrorCode = "INTERNAL"
)

// ProtocolVersion is the wire envelope version this build speaks.
const ProtocolVersion uint16 = 1

// CodedError carries an ErrorCode alongside a human-readable message so callers
// can branch on the code without matching strings.
type CodedError struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *CodedError) Error() string {
	switch {
	case e == nil:
		return ""
	case e.Message != "" && e.Err != nil:
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	case e.Message != "":
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	case e.Err != nil:
		return fmt.Sprintf("%s: %v", e.Code, e.Err)
	default:
		return string(e.Code)
	}
}

func (e *CodedError) Unwrap() error { return e.Err }

// Coded builds a CodedError.
func Coded(code ErrorCode, format string, args ...any) *CodedError {
	return &CodedError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches a code to an existing error.
func Wrap(code ErrorCode, err error, format string, args ...any) *CodedError {
	return &CodedError{Code: code, Message: fmt.Sprintf(format, args...), Err: err}
}

// CodeOf reports the ErrorCode carried by err, or CodeInternal when none is set.
func CodeOf(err error) ErrorCode {
	var coded *CodedError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return CodeInternal
}

// ErrBound reports that an input exceeded a declared limit. Callers surface the
// limit and the observed value so users can fix the input instead of guessing.
type ErrBound struct {
	Name     string
	Limit    int64
	Observed int64
}

func (e *ErrBound) Error() string {
	return fmt.Sprintf("bound exceeded: %s limit %d, observed %d", e.Name, e.Limit, e.Observed)
}

// ErrEncrypted reports an encrypted or password-protected input document.
var ErrEncrypted = errors.New("document is encrypted")

// ErrUnsupportedFormat reports an input whose type is not supported or whose
// magic bytes disagree with its extension.
var ErrUnsupportedFormat = errors.New("unsupported document format")

// ErrCapability reports that a host CLI cannot satisfy the tool-free contract
// this build requires. The message always names the missing flag or version.
type ErrCapability struct {
	Host   string
	Reason string
	Remedy string
}

func (e *ErrCapability) Error() string {
	if e.Remedy == "" {
		return fmt.Sprintf("capability missing for %s: %s", e.Host, e.Reason)
	}
	return fmt.Sprintf("capability missing for %s: %s (%s)", e.Host, e.Reason, e.Remedy)
}

// ErrToolUseObserved reports that a generation emitted a tool call even though
// every adapter runs tool-free. It is deliberately non-retryable: the same
// request would produce the same violation.
type ErrToolUseObserved struct {
	Host  string
	Event string
}

func (e *ErrToolUseObserved) Error() string {
	return fmt.Sprintf("tool use observed from %s (%s); generation discarded", e.Host, e.Event)
}
