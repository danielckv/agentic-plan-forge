//go:build client && !server

package sandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Packaging the project for a prepared sandbox.
//
// The VM is rented, so it may only receive what the build needs: the tracked
// source, the recorded scripts and the recipe. It never receives .git (the
// whole history is not the build's business, and it is the largest thing in
// most repositories), host state, credentials, or build output. A path that
// would escape the unpack directory is refused while packing, not discovered
// on the VM.

// Source bounds. They are deliberately generous for a real project and hard
// against an accident: a 20 MB archive is a build, a 2 GB one is a mistake.
const (
	// MaxSourceFiles bounds how many files one upload carries.
	MaxSourceFiles = 20000
	// MaxSourceBytes bounds the archive produced.
	MaxSourceBytes = 64 << 20
	// MaxSourceFileBytes bounds one file. A larger file is skipped and named.
	MaxSourceFileBytes = 8 << 20
	// SourceArchiveName is what the archive is called inside the sandbox.
	SourceArchiveName = "source.tar.gz"
)

// SourceOptions says what to pack and what to leave out.
type SourceOptions struct {
	// Root is the repository checkout. Everything is relative to it.
	Root string
	// Include names repository-relative paths to keep even when an exclusion
	// would drop them, for example a generated script the recipe needs.
	Include []string
}

// SourceArchive is a packed project, ready to upload.
type SourceArchive struct {
	// Data is the gzip-compressed tar.
	Data []byte
	// Files counts what landed.
	Files int
	// Bytes is the uncompressed size, which the caller can compare with the
	// provider's disk budget.
	Bytes int64
	// Skipped names every path left out and why, so the window can show it.
	Skipped []SourceSkip
}

// SourceSkip is one path the packager refused.
type SourceSkip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// excludedDirs are never packed: version control, dependency caches, build
// output and host state. A repository is mostly these, and none of them is the
// project's source.
var excludedDirs = map[string]bool{
	".git":         true,
	".plan":        true,
	".agents":      true,
	".omp":         true,
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	"target":       true,
	"out":          true,
	"coverage":     true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
	".next":        true,
	".turbo":       true,
	".cache":       true,
	".idea":        true,
	".vscode":      true,
}

// excludedFiles are secrets and host-local noise. A credential that reaches the
// VM is a leak the owner cannot see and did not choose.
var excludedFiles = map[string]bool{
	".DS_Store": true,
	".env":      true,
}

// excludedSuffixes drop credential material wherever it sits.
var excludedSuffixes = []string{
	".pem", ".key", ".p12", ".pfx", ".keystore",
	".log",
}

// PackageSource builds a gzipped tar of the project's buildable source.
//
// The walk is deterministic: entries are sorted, timestamps are zeroed and
// modes are normalised, so the same tree produces the same archive and a hash
// of it can identify the revision that was built.
func PackageSource(opts SourceOptions) (SourceArchive, error) {
	if strings.TrimSpace(opts.Root) == "" {
		return SourceArchive{}, errors.New("sandbox: packaging needs a repository root")
	}
	info, err := os.Stat(opts.Root)
	if err != nil {
		return SourceArchive{}, fmt.Errorf("sandbox: %w", err)
	}
	if !info.IsDir() {
		return SourceArchive{}, errors.New("sandbox: the repository root is not a directory")
	}

	include := map[string]bool{}
	for _, rel := range opts.Include {
		clean, ok := cleanRel(rel)
		if !ok {
			return SourceArchive{}, fmt.Errorf("sandbox: include path %q escapes the repository", rel)
		}
		include[clean] = true
	}

	var files []string
	var skipped []SourceSkip
	var total int64
	err = filepath.WalkDir(opts.Root, func(full string, d fs.DirEntry, werr error) error {
		if werr != nil {
			// An unreadable directory is skipped rather than failing the pack:
			// a permission oddity should not block a build.
			skipped = append(skipped, SourceSkip{Path: relOf(opts.Root, full), Reason: "unreadable"})
			return nil
		}
		rel := relOf(opts.Root, full)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if excludedDirs[d.Name()] && !include[rel] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			// A symlink could point outside the checkout, so it is never packed.
			skipped = append(skipped, SourceSkip{Path: rel, Reason: "not a regular file"})
			return nil
		}
		if why := excluded(rel, d.Name()); why != "" && !include[rel] {
			skipped = append(skipped, SourceSkip{Path: rel, Reason: why})
			return nil
		}
		if len(files) >= MaxSourceFiles {
			skipped = append(skipped, SourceSkip{Path: rel, Reason: "file limit reached"})
			return nil
		}
		st, serr := d.Info()
		if serr != nil {
			skipped = append(skipped, SourceSkip{Path: rel, Reason: "unreadable"})
			return nil
		}
		if st.Size() > MaxSourceFileBytes {
			skipped = append(skipped, SourceSkip{Path: rel, Reason: "larger than the per-file bound"})
			return nil
		}
		total += st.Size()
		if total > MaxSourceBytes {
			skipped = append(skipped, SourceSkip{Path: rel, Reason: "archive bound reached"})
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return SourceArchive{}, err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return SourceArchive{}, errors.New("sandbox: there is no source to package")
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	var written int64
	for _, rel := range files {
		full := filepath.Join(opts.Root, filepath.FromSlash(rel))
		raw, rerr := os.ReadFile(full)
		if rerr != nil {
			skipped = append(skipped, SourceSkip{Path: rel, Reason: "unreadable"})
			continue
		}
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     rel,
			Mode:     0o644,
			Size:     int64(len(raw)),
			// Zeroed so the archive depends on content, not on when it was read.
			ModTime: timeZero,
			Format:  tar.FormatPAX,
		}
		if herr := tw.WriteHeader(hdr); herr != nil {
			return SourceArchive{}, fmt.Errorf("sandbox: pack %s: %w", rel, herr)
		}
		if _, werr := tw.Write(raw); werr != nil {
			return SourceArchive{}, fmt.Errorf("sandbox: pack %s: %w", rel, werr)
		}
		written += int64(len(raw))
	}
	if err := tw.Close(); err != nil {
		return SourceArchive{}, err
	}
	if err := gz.Close(); err != nil {
		return SourceArchive{}, err
	}
	if buf.Len() > MaxSourceBytes {
		return SourceArchive{}, fmt.Errorf("sandbox: the archive is %d bytes, past the transfer bound", buf.Len())
	}
	return SourceArchive{Data: buf.Bytes(), Files: len(files), Bytes: written, Skipped: skipped}, nil
}

// timeZero is the fixed timestamp every entry carries.
var timeZero = time.Unix(0, 0).UTC()

// relOf is the repository-relative, slash-separated form of a path.
func relOf(root, full string) string {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return filepath.ToSlash(full)
	}
	return filepath.ToSlash(rel)
}

// excluded reports why a path is not build source, or "" when it is.
func excluded(rel, name string) string {
	if excludedFiles[name] {
		return "host-local or secret file"
	}
	lower := strings.ToLower(name)
	for _, suffix := range excludedSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return "credential or log file"
		}
	}
	for _, part := range strings.Split(rel, "/") {
		if excludedDirs[part] {
			return "not build source"
		}
	}
	return ""
}

// cleanRel keeps a caller-supplied path inside the repository.
func cleanRel(rel string) (string, bool) {
	rel = strings.TrimSpace(rel)
	if rel == "" || strings.Contains(rel, "\\") || strings.HasPrefix(rel, "/") {
		return "", false
	}
	clean := strings.Trim(strings.TrimPrefix(rel, "./"), "/")
	if clean == "" || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}
