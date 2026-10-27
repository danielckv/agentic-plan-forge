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

type acceptanceFile struct {
	Version  int               `json:"version"`
	Accepted []AcceptanceEntry `json:"entries"`
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
	for _, record := range ledger.Accepted {
		if record.Repository != repoPath || record.ApprovalID != approvalID {
			continue
		}
		if headSHA != "" && record.HeadSHA != headSHA {
			return AcceptanceEntry{}, false, true, nil
		}
		return record, true, false, nil
	}
	return AcceptanceEntry{}, false, false, nil
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
