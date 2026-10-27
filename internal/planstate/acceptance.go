//go:build client && !server

package planstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/storage"
)

// Implementation acceptance is a separate decision from plan approval. The
// approval ledger records that a manifest was approved; this ledger records
// that an implementation of it was accepted, by whom, on which revision.
const (
	acceptanceFileName = "acceptances.json"
	acceptanceVersion  = 1
	// maxAcceptances bounds the file: one entry per accepted revision.
	maxAcceptances = 2048
	// maxAcceptanceBytes bounds the file on read.
	maxAcceptanceBytes = 4 << 20
)

// AcceptanceEntry is one implementation acceptance, bound to the exact revision
// it was decided on. A newer commit does not inherit it.
type AcceptanceEntry struct {
	Repository   string `json:"repo_realpath"`
	ApprovalID   string `json:"approval_id"`
	ManifestHash string `json:"manifest_sha256"`
	PullRequest  string `json:"pr"`
	HeadSHA      string `json:"head_sha"`
	RecipeHash   string `json:"recipe_hash"`
	BundleDigest string `json:"bundle_digest"`
	EvidenceHash string `json:"evidence_hash"`
	// ProductOwner and Developer are authenticated actors. A browser-claimed
	// name is never evidence, so both come from a verified source.
	ProductOwner    string    `json:"pm_actor"`
	Developer       string    `json:"reviewer"`
	ValidatedAt     time.Time `json:"accepted_at"`
	ProductSignedAt time.Time `json:"pm_signed_at"`
	DeveloperSigned time.Time `json:"reviewed_at"`
}

// SignoffEntry records a product sign-off before the implementation is
// accepted. It is separate from the acceptance because the two are decided at
// different times and by different people, and the gate requires both.
type SignoffEntry struct {
	Repository string    `json:"repo_realpath"`
	ApprovalID string    `json:"approval_id"`
	HeadSHA    string    `json:"head_sha"`
	Actor      string    `json:"actor"`
	At         time.Time `json:"at"`
}

type acceptanceFile struct {
	Version  int               `json:"version"`
	Accepted []AcceptanceEntry `json:"entries"`
	Signoffs []SignoffEntry    `json:"signoffs"`
}

func acceptancePath() (string, error) {
	home, err := config.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, acceptanceFileName), nil
}

// RecordAcceptance writes one acceptance. The same repository, approval and head
// update one entry rather than adding another, so re-checking an unchanged
// decision is idempotent. A record that does not name both actors and the
// revision it covers is refused: nothing later could tell what it approved.
func RecordAcceptance(repo string, entry AcceptanceEntry) error {
	repoPath, err := realRepo(repo)
	if err != nil {
		return err
	}
	switch {
	case entry.ApprovalID == "":
		return errors.New("acceptance record: approval id is required")
	case !validSHA(entry.ManifestHash):
		return errors.New("acceptance record: manifest hash must be 64 lowercase hex characters")
	case entry.HeadSHA == "":
		return errors.New("acceptance record: the validated commit is required")
	case entry.PullRequest == "":
		return errors.New("acceptance record: the pull request is required")
	case entry.ProductOwner == "":
		return errors.New("acceptance record: the product sign-off is required")
	case entry.Developer == "":
		return errors.New("acceptance record: the approving developer is required")
	}
	if entry.ValidatedAt.IsZero() {
		entry.ValidatedAt = time.Now().UTC()
	}
	entry.Repository = repoPath

	path, err := acceptancePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ledgerLockTimeout)
	defer cancel()
	unlock, err := storage.LockFile(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()

	ledger, err := readAcceptances(path)
	if err != nil {
		return err
	}
	replaced := false
	for i := range ledger.Accepted {
		prior := &ledger.Accepted[i]
		if prior.Repository == repoPath && prior.ApprovalID == entry.ApprovalID && prior.HeadSHA == entry.HeadSHA {
			*prior = entry
			replaced = true
			break
		}
	}
	if !replaced {
		ledger.Accepted = append(ledger.Accepted, entry)
	}
	if n := len(ledger.Accepted); n > maxAcceptances {
		ledger.Accepted = ledger.Accepted[n-maxAcceptances:]
	}
	return writeAcceptances(path, ledger)
}

// writeAcceptances stores the ledger atomically and privately: it names people
// and the revisions they decided on.
func writeAcceptances(path string, ledger acceptanceFile) error {
	raw, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	return storage.WriteFileAtomic(path, append(raw, '\n'), 0o600)
}

// ImplementationAcceptance returns the acceptance for one approval at one
// revision. found is false when nothing covers it; stale is true when a record
// exists for a different revision, which callers must show as not accepted.
func ImplementationAcceptance(repo, approvalID, headSHA string) (entry AcceptanceEntry, found, stale bool, err error) {
	if approvalID == "" {
		return AcceptanceEntry{}, false, false, nil
	}
	repoPath, err := realRepo(repo)
	if err != nil {
		return AcceptanceEntry{}, false, false, err
	}
	path, err := acceptancePath()
	if err != nil {
		return AcceptanceEntry{}, false, false, err
	}
	ledger, err := readAcceptances(path)
	if err != nil {
		return AcceptanceEntry{}, false, false, err
	}
	// Scan every record for this approval: two revisions of one approval are
	// both genuine decisions, so a different revision seen first must not shadow
	// the one that was asked for. Stale is reported only after the scan.
	staleFound := false
	for _, record := range ledger.Accepted {
		if record.Repository != repoPath || record.ApprovalID != approvalID {
			continue
		}
		if headSHA == "" || record.HeadSHA == headSHA {
			return record, true, false, nil
		}
		staleFound = true
	}
	return AcceptanceEntry{}, false, staleFound, nil
}

// RecordProductSignoff records that the product owner signed off one revision of
// one approval. Repeating it for the same revision updates the record rather
// than adding another.
func RecordProductSignoff(repo string, entry SignoffEntry) error {
	repoPath, err := realRepo(repo)
	if err != nil {
		return err
	}
	switch {
	case entry.ApprovalID == "":
		return errors.New("product sign-off: approval id is required")
	case entry.HeadSHA == "":
		return errors.New("product sign-off: the reviewed commit is required")
	case entry.Actor == "":
		return errors.New("product sign-off: the actor is required")
	}
	if entry.At.IsZero() {
		entry.At = time.Now().UTC()
	}
	entry.Repository = repoPath

	path, err := acceptancePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ledgerLockTimeout)
	defer cancel()
	unlock, err := storage.LockFile(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()

	ledger, err := readAcceptances(path)
	if err != nil {
		return err
	}
	replaced := false
	for i := range ledger.Signoffs {
		prior := &ledger.Signoffs[i]
		if prior.Repository == repoPath && prior.ApprovalID == entry.ApprovalID && prior.HeadSHA == entry.HeadSHA {
			*prior = entry
			replaced = true
			break
		}
	}
	if !replaced {
		ledger.Signoffs = append(ledger.Signoffs, entry)
	}
	if n := len(ledger.Signoffs); n > maxAcceptances {
		ledger.Signoffs = ledger.Signoffs[n-maxAcceptances:]
	}
	return writeAcceptances(path, ledger)
}

// ProductSignoff returns the sign-off for one approval at one revision.
func ProductSignoff(repo, approvalID, headSHA string) (SignoffEntry, bool, error) {
	if approvalID == "" {
		return SignoffEntry{}, false, nil
	}
	repoPath, err := realRepo(repo)
	if err != nil {
		return SignoffEntry{}, false, err
	}
	path, err := acceptancePath()
	if err != nil {
		return SignoffEntry{}, false, err
	}
	ledger, err := readAcceptances(path)
	if err != nil {
		return SignoffEntry{}, false, err
	}
	for _, entry := range ledger.Signoffs {
		if entry.Repository == repoPath && entry.ApprovalID == approvalID && entry.HeadSHA == headSHA {
			return entry, true, nil
		}
	}
	return SignoffEntry{}, false, nil
}

// Acceptances returns every record for a repository, newest first.
func Acceptances(repo string) ([]AcceptanceEntry, error) {
	repoPath, err := realRepo(repo)
	if err != nil {
		return nil, err
	}
	path, err := acceptancePath()
	if err != nil {
		return nil, err
	}
	ledger, err := readAcceptances(path)
	if err != nil {
		return nil, err
	}
	out := make([]AcceptanceEntry, 0, len(ledger.Accepted))
	for i := len(ledger.Accepted) - 1; i >= 0; i-- {
		if ledger.Accepted[i].Repository == repoPath {
			out = append(out, ledger.Accepted[i])
		}
	}
	return out, nil
}

func readAcceptances(path string) (acceptanceFile, error) {
	raw, err := storage.ReadFileBounded(path, maxAcceptanceBytes)
	if errors.Is(err, os.ErrNotExist) {
		return acceptanceFile{Version: acceptanceVersion}, nil
	}
	if err != nil {
		return acceptanceFile{}, err
	}
	var ledger acceptanceFile
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return acceptanceFile{}, fmt.Errorf("acceptance ledger is malformed: %w", err)
	}
	if ledger.Version != acceptanceVersion {
		return acceptanceFile{}, fmt.Errorf("acceptance ledger has unsupported version %d", ledger.Version)
	}
	return ledger, nil
}
