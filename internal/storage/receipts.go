package storage

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// maxReceiptBytes bounds one receipt file on read.
const maxReceiptBytes = 16 << 10

// maxReceipts bounds how many receipt files a reader considers.
const maxReceipts = 2000

// Receipt records one explicit, confirmed run of a manifest verification
// command. It proves behavior only for the exact task, command and source
// fingerprint it names: a changed fingerprint invalidates it.
type Receipt struct {
	TaskID            string    `json:"task_id"`
	VerificationID    string    `json:"verification_id"`
	CommandSHA256     string    `json:"command_sha256"`
	SourceFingerprint string    `json:"source_fingerprint"`
	ExitCode          int       `json:"exit_code"`
	ExpectedExitCode  int       `json:"expected_exit_code"`
	OutputSHA256      string    `json:"output_sha256"`
	DurationMS        int64     `json:"duration_ms"`
	At                time.Time `json:"at"`
}

// Succeeded reports whether the run matched the expected exit status. A failed
// verification never yields a successful receipt.
func (r Receipt) Succeeded() bool { return r.ExitCode == r.ExpectedExitCode }

// receiptName derives the file name from hashes only, so untrusted task IDs
// from a manifest never reach the filesystem.
func receiptName(r Receipt) string {
	return sum([]byte(r.TaskID + "\x00" + r.VerificationID + "\x00" + r.CommandSHA256))[:32] + ".json"
}

// WriteReceipt stores a receipt in .plan/verification/ (ignored by git).
func WriteReceipt(ctx context.Context, repoRoot string, r Receipt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := openPlan(repoRoot)
	if err != nil {
		return err
	}
	unlock, err := p.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	dir, err := p.ensureDir(PlanDirName + "/" + VerificationDir)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, receiptName(r))
	if info, err := os.Lstat(path); err == nil && hostile(info.Mode()) {
		return &ArtifactError{Code: CodeESYMLINK, RelPath: PlanDirName + "/" + VerificationDir, Err: errors.New("receipt path is a symbolic link")}
	}
	if err := writeFileAtomic(path, append(raw, '\n'), 0o600); err != nil {
		return artifactErr(PlanDirName+"/"+VerificationDir, err)
	}
	return nil
}

// ReadReceipts returns the readable receipts. It writes nothing. A missing
// directory yields none; unreadable or malformed files are skipped, because a
// receipt that cannot be read proves nothing.
func ReadReceipts(repoRoot string) ([]Receipt, error) {
	p, err := openPlan(repoRoot)
	if err != nil {
		return nil, err
	}
	dir, err := p.path(PlanDirName + "/" + VerificationDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, artifactErr(PlanDirName+"/"+VerificationDir, err)
	}
	var out []Receipt
	for _, e := range entries {
		if len(out) >= maxReceipts {
			break
		}
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := readBounded(filepath.Join(dir, e.Name()), maxReceiptBytes)
		if err != nil {
			continue
		}
		var r Receipt
		if json.Unmarshal(raw, &r) != nil || r.TaskID == "" || r.CommandSHA256 == "" {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}
