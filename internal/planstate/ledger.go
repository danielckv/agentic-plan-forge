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

const (
	ledgerFileName    = "approvals.json"
	ledgerVersion     = 1
	maxLedgerEntries  = 4096
	maxLedgerBytes    = 4 << 20
	ledgerLockTimeout = 10 * time.Second
)

// LedgerEntry records that a manifest was approved on this machine.
type LedgerEntry struct {
	RepoRealpath   string `json:"repo_realpath"`
	ManifestSHA256 string `json:"manifest_sha256"`
	ApprovalID     string `json:"approval_id"`
}

type ledgerFile struct {
	Version int           `json:"version"`
	Entries []LedgerEntry `json:"entries"`
}

func ledgerPath() (string, error) {
	home, err := config.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ledgerFileName), nil
}

func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func readLedger(path string) (ledgerFile, error) {
	raw, err := storage.ReadFileBounded(path, maxLedgerBytes)
	if isNotExist(err) {
		return ledgerFile{Version: ledgerVersion}, nil
	}
	if err != nil {
		return ledgerFile{}, err
	}
	var l ledgerFile
	if err := json.Unmarshal(raw, &l); err != nil {
		return ledgerFile{}, fmt.Errorf("approval ledger is malformed: %w", err)
	}
	return l, nil
}

// IsApproved reports whether this exact manifest hash was approved for this
// exact repository on this machine. Any problem reading the ledger means "not
// approved": the answer fails closed.
func IsApproved(repo, manifestSHA256 string) (bool, error) {
	if !validSHA(manifestSHA256) {
		return false, nil
	}
	real, err := realRepo(repo)
	if err != nil {
		return false, err
	}
	path, err := ledgerPath()
	if err != nil {
		return false, err
	}
	l, err := readLedger(path)
	if err != nil {
		return false, err
	}
	for _, e := range l.Entries {
		if e.RepoRealpath == real && e.ManifestSHA256 == manifestSHA256 {
			return true, nil
		}
	}
	return false, nil
}

// RecordApproval writes {repo_realpath, manifest_sha256, approval_id} to the
// local approval ledger, PlanForgeHome()/approvals.json (mode 0600), on local
// approval or a successful artifact sync. The write is locked and atomic; the
// same repository and manifest hash update one entry instead of adding another.
// A ledger that cannot be parsed is moved aside, never overwritten.
func RecordApproval(repo, manifestSHA256, approvalID string) error {
	if !validSHA(manifestSHA256) {
		return errors.New("approval ledger: manifest hash must be 64 lowercase hex digits")
	}
	real, err := realRepo(repo)
	if err != nil {
		return err
	}
	home, err := config.EnsureHome()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ledgerFileName)
	ctx, cancel := context.WithTimeout(context.Background(), ledgerLockTimeout)
	defer cancel()
	unlock, err := storage.LockFile(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()

	l, err := readLedger(path)
	if err != nil {
		if _, serr := os.Lstat(path); serr == nil {
			if rerr := os.Rename(path, path+".corrupt"); rerr != nil {
				return fmt.Errorf("approval ledger is unreadable and could not be moved aside: %w", rerr)
			}
			l = ledgerFile{Version: ledgerVersion}
		} else {
			return err
		}
	}
	l.Version = ledgerVersion
	updated := false
	for i := range l.Entries {
		if l.Entries[i].RepoRealpath == real && l.Entries[i].ManifestSHA256 == manifestSHA256 {
			l.Entries[i].ApprovalID = approvalID
			updated = true
		}
	}
	if !updated {
		l.Entries = append(l.Entries, LedgerEntry{RepoRealpath: real, ManifestSHA256: manifestSHA256, ApprovalID: approvalID})
	}
	if n := len(l.Entries); n > maxLedgerEntries {
		l.Entries = l.Entries[n-maxLedgerEntries:]
	}
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return storage.WriteFileAtomic(path, append(raw, '\n'), 0o600)
}
