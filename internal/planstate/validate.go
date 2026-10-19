//go:build client && !server

package planstate

import (
	"context"
	"errors"

	"github.com/danielckv/agentic-plan-forge/internal/sanitize"
)

const (
	noteNoManifest   = "no plan manifest present"
	noteScanTimedOut = "validation timed out; run plan-forge validate"
	noteNotApproved  = "manifest is not approved on this machine"
)

// ValidationDrift is one drift reason in a Validation.
type ValidationDrift struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// Validation is the C1 result of `plan-forge validate`. Every string is
// sanitized and every slice is non-nil, so it encodes as `[]`, never `null`.
type Validation struct {
	Valid          bool              `json:"valid"`
	Approved       bool              `json:"approved"`
	Status         string            `json:"status"`
	Title          string            `json:"title"`
	ManifestSHA256 string            `json:"manifest_sha256"`
	Verified       int               `json:"verified"`
	Total          int               `json:"total"`
	Tasks          int               `json:"tasks"`
	Pending        []string          `json:"pending"`
	Drift          []ValidationDrift `json:"drift"`
	Notes          []string          `json:"notes"`
	SpecPath       string            `json:"spec_path"`
	ManifestPath   string            `json:"manifest_path"`
}

// Validate evaluates the committed plan with a bounded CheckStateWith scan. It
// is valid only when the manifest is approved on this machine and the plan is
// neither DRIFTED nor UNKNOWN. A missing manifest is invalid, not an error; a
// scan that does not finish yields status UNKNOWN. Nothing is written.
func Validate(ctx context.Context, repo string, opts CheckOptions) (Validation, error) {
	v := Validation{
		Status: string(StatusUnknown), Pending: []string{}, Drift: []ValidationDrift{}, Notes: []string{},
		SpecPath: SpecRel, ManifestPath: ManifestRel,
	}
	st, err := CheckStateWith(ctx, repo, opts)
	if errors.Is(err, ErrStatusUnknown) {
		v.Notes = append(v.Notes, noteScanTimedOut)
		return v, nil
	}
	if err != nil {
		return Validation{}, err
	}
	if !st.Present {
		v.Notes = append(v.Notes, noteNoManifest)
		return v, nil
	}
	v.Approved = st.Approved
	v.Status = sanitize.Terminal(string(st.Status))
	v.Title = sanitize.Title(st.Title)
	v.ManifestSHA256 = sanitize.Terminal(st.ManifestSHA256)
	v.Verified, v.Total = st.Verified, st.Total
	for _, p := range st.Pending {
		v.Pending = append(v.Pending, sanitize.Terminal(p))
	}
	for _, d := range st.Drift {
		v.Drift = append(v.Drift, ValidationDrift{Code: sanitize.Terminal(d.Code), Detail: sanitize.Terminal(d.Detail)})
	}
	for _, n := range st.Notes {
		v.Notes = append(v.Notes, sanitize.Terminal(n))
	}
	if !st.Approved {
		v.Notes = append(v.Notes, noteNotApproved)
	}
	if art, aerr := readArtifacts(st.Repo); aerr == nil && art.present {
		v.Tasks = len(art.manifest.TaskDAG)
	}
	v.Valid = st.Approved && st.Status != StatusDrifted && st.Status != StatusUnknown
	return v, nil
}
