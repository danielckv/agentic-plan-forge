// Package repowork inventories the planning documents a repository already
// holds — state, plans, specs, ADRs, follow-ups, epics and tasks — so the
// window can browse real work without a model call. Scanning is read-only
// and bounded: no symbolic link is followed, no path outside the repository
// root is opened, and every limit or unreadable file lands in
// Inventory.Skipped with a reason instead of failing the whole scan.
package repowork

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Scan bounds. A zero Options field falls back to the default here.
const (
	defaultMaxDocuments  = 400
	defaultMaxFileBytes  = int64(2_000_000)
	defaultMaxTotalBytes = int64(20_000_000)
	defaultBudget        = 2 * time.Second
	// maxDepth bounds directory recursion below each discovery root, the way
	// internal/gather bounds its walk.
	maxDepth = 4
	// statePath is the one active state file whose ## Next order feeds
	// Inventory.Next. No other file contributes to it.
	statePath = ".agents/STATE.md"
	// idHexLen is the length of a document ID: the first 16 hex characters of
	// the SHA-256 of the slash-separated repository-relative path.
	idHexLen = 16
)

// Options bounds and shapes one Scan.
type Options struct {
	Root          string        // repository root
	ExtraRoots    []string      // repository-relative additional roots (nonstandard layouts)
	MaxDocuments  int           // <=0 -> 400
	MaxFileBytes  int64         // <=0 -> 2_000_000
	MaxTotalBytes int64         // <=0 -> 20_000_000
	Budget        time.Duration // <=0 -> 2*time.Second
}

// Heading is one ATX markdown heading.
type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
	Line  int    `json:"line"`
}

// Item is one work item parsed from a document: a task checkbox, or a
// top-level bullet in the state file's ## Next section.
type Item struct {
	ID        string   `json:"id"`
	Text      string   `json:"text"`
	Status    string   `json:"status"`
	Line      int      `json:"line"`
	EndLine   int      `json:"end_line"`
	Source    string   `json:"source"`
	DependsOn []string `json:"depends_on"`
}

// Skipped records one candidate file the scan did not index, and why.
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Document is one indexed repository document.
type Document struct {
	ID       string    `json:"id"`
	Path     string    `json:"path"`
	Kind     string    `json:"kind"`
	SHA256   string    `json:"sha256"`
	Bytes    int       `json:"bytes"`
	Headings []Heading `json:"headings"`
	Items    []Item    `json:"items"`
}

// Inventory is the bounded result of one Scan. Partial is true whenever any
// candidate was skipped or the budget ran out; the reasons say which.
type Inventory struct {
	Revision    string     `json:"revision"`
	Partial     bool       `json:"partial"`
	Skipped     []Skipped  `json:"skipped"`
	Documents   []Document `json:"documents"`
	Next        []Item     `json:"next"`
	GeneratedAt time.Time  `json:"generated_at"`
}

// ErrChanged reports that a document's bytes no longer match the scan the
// caller is opening from. ErrNotFound reports an ID no scan produced.
var (
	ErrChanged  = errors.New("repowork: document changed since it was scanned")
	ErrNotFound = errors.New("repowork: unknown document id")
)

func (o Options) withDefaults() Options {
	if o.MaxDocuments <= 0 {
		o.MaxDocuments = defaultMaxDocuments
	}
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = defaultMaxFileBytes
	}
	if o.MaxTotalBytes <= 0 {
		o.MaxTotalBytes = defaultMaxTotalBytes
	}
	if o.Budget <= 0 {
		o.Budget = defaultBudget
	}
	return o
}

// fixedDirs are the discovery roots walked below the repository root, in
// order. Missing roots are simply absent from the inventory.
var fixedDirs = []string{
	".agents/lanes", ".agents/sessions",
	"docs/plans", "docs/specs", "docs/adrs", "specs",
}

// rootPrefixes match root-level files by lowercased basename prefix.
var rootPrefixes = []string{"plan", "follow", "epic", "task", "state"}

// Scan walks the discovery roots under opts.Root and returns a bounded,
// read-only inventory of the repository's planning documents. Hitting a
// bound never fails the scan: the candidate is skipped with a reason and
// Inventory.Partial is set. A zero-document repository is a normal empty
// result, not an error.
func Scan(ctx context.Context, opts Options) (Inventory, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(opts.Root) == "" {
		return Inventory{}, errors.New("repowork: repository root is required")
	}
	o := opts.withDefaults()
	info, err := os.Lstat(o.Root)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return Inventory{}, errors.New("repowork: repository root is missing or not a directory")
	}
	s := &scanner{
		ctx:      ctx,
		deadline: time.Now().Add(o.Budget),
		root:     o.Root,
		opts:     o,
		inv: Inventory{
			Skipped:   []Skipped{},
			Documents: []Document{},
			Next:      []Item{},
		},
		seen: map[string]bool{},
	}
	roots, err := s.buildRoots(o.ExtraRoots)
	if err != nil {
		return Inventory{}, err
	}
	for _, wr := range roots {
		if s.over() {
			s.stopped = true
			break
		}
		if wr.dir {
			s.walkDir(wr.rel, 0)
		} else {
			s.file(wr.rel)
		}
	}
	if s.stopped {
		s.skip("", "budget exhausted")
	}
	sort.Slice(s.inv.Documents, func(i, j int) bool {
		return s.inv.Documents[i].Path < s.inv.Documents[j].Path
	})
	sort.Slice(s.inv.Skipped, func(i, j int) bool {
		return s.inv.Skipped[i].Path < s.inv.Skipped[j].Path
	})
	rev := sha256.New()
	for _, d := range s.inv.Documents {
		fmt.Fprintf(rev, "%s %s\n", d.ID, d.SHA256)
	}
	s.inv.Revision = hex.EncodeToString(rev.Sum(nil))[:idHexLen]
	s.inv.GeneratedAt = time.Now().UTC()
	return s.inv, nil
}

type walkRoot struct {
	rel string
	dir bool
}

type scanner struct {
	ctx      context.Context
	deadline time.Time
	root     string
	opts     Options
	inv      Inventory
	seen     map[string]bool
	total    int64
	stopped  bool
}

func (s *scanner) over() bool {
	return s.ctx.Err() != nil || time.Now().After(s.deadline)
}

func (s *scanner) skip(path, reason string) {
	s.inv.Partial = true
	s.inv.Skipped = append(s.inv.Skipped, Skipped{Path: path, Reason: reason})
}

// buildRoots lists every discovery root as a repository-relative walk target:
// the active state file, the fixed directories, matching root-level files and
// the caller's extra roots. Missing roots are dropped silently. A malformed
// extra root is a caller error and fails the scan.
func (s *scanner) buildRoots(extra []string) ([]walkRoot, error) {
	var out []walkRoot
	addRoot := func(rel string) error {
		full := filepath.Join(s.root, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return readErr(rel, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			s.skip(rel, "symbolic link refused")
			return nil
		}
		if info.IsDir() {
			out = append(out, walkRoot{rel: rel, dir: true})
			return nil
		}
		if info.Mode().IsRegular() {
			out = append(out, walkRoot{rel: rel})
			return nil
		}
		s.skip(rel, "not a regular file")
		return nil
	}
	if err := addRoot(statePath); err != nil {
		return nil, err
	}
	for _, d := range fixedDirs {
		if err := addRoot(d); err != nil {
			return nil, err
		}
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, readErr(".", err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !rootFileMatch(name) {
			continue
		}
		if e.Type()&fs.ModeSymlink != 0 {
			s.skip(name, "symbolic link refused")
			continue
		}
		if e.Type().IsRegular() {
			out = append(out, walkRoot{rel: name})
		}
	}
	for _, x := range extra {
		rel, ok := safeRel(x)
		if !ok {
			return nil, errors.New("repowork: extra root must be repository-relative and may not contain '..' or '.git'")
		}
		if err := addRoot(rel); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// walkDir descends one discovery root, skipping dot directories, symbolic
// links and anything deeper than maxDepth. Directory entries are Lstat-
// verified, so the walk never crosses a symbolic link or leaves the root.
func (s *scanner) walkDir(rel string, depth int) {
	if s.over() {
		s.stopped = true
		return
	}
	full := filepath.Join(s.root, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if err != nil {
		s.skip(rel, unreadableReason(err))
		return
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		s.skip(rel, "symbolic link refused")
		return
	}
	if !info.IsDir() {
		s.skip(rel, "not a directory")
		return
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		s.skip(rel, unreadableReason(err))
		return
	}
	for _, e := range entries {
		if s.over() {
			s.stopped = true
			return
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		child := rel + "/" + name
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			s.skip(child, "symbolic link refused")
		case e.Type().IsDir():
			if depth+1 > maxDepth {
				s.skip(child, "depth limit reached")
				continue
			}
			s.walkDir(child, depth+1)
		case e.Type().IsRegular():
			s.file(child)
		default:
			s.skip(child, "unsupported file type")
		}
	}
}

// file reads and parses one candidate document, enforcing every bound. A
// non-text extension is not an error and not a skip: it is not a candidate.
func (s *scanner) file(rel string) {
	if s.seen[rel] {
		return
	}
	s.seen[rel] = true
	if !textExt(rel) {
		return
	}
	if len(s.inv.Documents) >= s.opts.MaxDocuments {
		s.skip(rel, "document limit reached")
		return
	}
	full := filepath.Join(s.root, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if err != nil {
		s.skip(rel, unreadableReason(err))
		return
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		s.skip(rel, "symbolic link refused")
		return
	}
	if !info.Mode().IsRegular() {
		s.skip(rel, "not a regular file")
		return
	}
	if info.Size() > s.opts.MaxFileBytes {
		s.skip(rel, "file exceeds per-file size limit")
		return
	}
	if s.total+info.Size() > s.opts.MaxTotalBytes {
		s.skip(rel, "total size limit exceeded")
		return
	}
	raw, err := readFileBounded(full, s.opts.MaxFileBytes)
	if err != nil {
		s.skip(rel, unreadableReason(err))
		return
	}
	if int64(len(raw)) > s.opts.MaxFileBytes {
		s.skip(rel, "file exceeds per-file size limit")
		return
	}
	if !utf8.Valid(raw) {
		s.skip(rel, "invalid UTF-8")
		return
	}
	s.total += int64(len(raw))
	doc, next := buildDocument(rel, raw)
	s.inv.Documents = append(s.inv.Documents, doc)
	if rel == statePath {
		s.inv.Next = next
	}
}

// Open resolves an ID produced by Scan, re-reads that exact file with the
// same symlink and traversal checks the scan applies, and verifies its bytes
// against the content half of the ID. An unknown or malformed ID returns
// ErrNotFound; content that no longer matches the scan returns ErrChanged.
//
// The scan is the only way to learn which path an ID belongs to, so it runs
// against a bounded budget and the path is then re-verified against the
// content half of the ID. A document whose bytes changed gets a different ID,
// so the caller is told the page is stale instead of being handed new bytes
// under an old heading.
func Open(ctx context.Context, root, id string) (Document, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(root) == "" {
		return Document{}, nil, errors.New("repowork: repository root is required")
	}
	if err := ctx.Err(); err != nil {
		return Document{}, nil, err
	}
	if !validID(id) {
		return Document{}, nil, ErrNotFound
	}
	inv, err := Scan(ctx, Options{Root: root})
	if err != nil {
		return Document{}, nil, err
	}
	// The path digest is half the ID: it narrows the search to one document
	// even after its content changed, which is what makes the changed case
	// reportable rather than merely "not found".
	for _, d := range inv.Documents {
		if d.ID == id {
			content, err := readVerified(root, d.Path, d.SHA256)
			if err != nil {
				return Document{}, nil, err
			}
			return d, content, nil
		}
	}
	for _, d := range inv.Documents {
		if pathPrefix(d.Path) == id[:idHexLen/2] {
			return Document{}, nil, ErrChanged
		}
	}
	return Document{}, nil, ErrNotFound
}

// readVerified opens rel below root only through Lstat-verified components,
// refuses any symbolic link or non-regular file, and fails with ErrChanged
// unless the bytes still hash to wantSHA.
func readVerified(root, rel, wantSHA string) ([]byte, error) {
	if _, ok := safeRel(rel); !ok {
		return nil, ErrNotFound
	}
	parts := strings.Split(rel, "/")
	cur := root
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return nil, readErr(rel, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("repowork: %s: symbolic link refused", rel)
		}
		last := i == len(parts)-1
		switch {
		case last:
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("repowork: %s: not a regular file", rel)
			}
		case !info.IsDir():
			return nil, fmt.Errorf("repowork: %s: not a directory", rel)
		}
	}
	raw, err := readFileBounded(filepath.Join(root, filepath.FromSlash(rel)), defaultMaxFileBytes)
	if err != nil {
		return nil, readErr(rel, err)
	}
	if int64(len(raw)) > defaultMaxFileBytes {
		return nil, ErrChanged
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != wantSHA {
		return nil, ErrChanged
	}
	return raw, nil
}

// readFileBounded reads at most max bytes of one file. The error never
// carries a host path.
func readFileBounded(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, scrub(err)
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max+1))
}

// scrub strips the path a wrapped *os error embeds, so messages stay free of
// host paths.
func scrub(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err
	}
	return err
}

// readErr renders a read failure against a repository-relative path only.
func readErr(rel string, err error) error {
	err = scrub(err)
	switch {
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("repowork: %s: permission denied", rel)
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("repowork: %s: file not found", rel)
	default:
		return fmt.Errorf("repowork: %s: unreadable", rel)
	}
}

// unreadableReason classifies a read failure for Inventory.Skipped.
func unreadableReason(err error) string {
	err = scrub(err)
	switch {
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case errors.Is(err, fs.ErrNotExist):
		return "file not found"
	default:
		return "unreadable"
	}
}

// safeRel accepts a repository-relative path that stays inside the root:
// never absolute, never a ".." component, never under .git.
var absDrive = regexp.MustCompile(`^[A-Za-z]:[/\\]`)

func safeRel(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") || absDrive.MatchString(p) {
		return "", false
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." || part == ".git" {
			return "", false
		}
	}
	return clean, true
}

// validID accepts exactly the IDs Scan produces: 16 lowercase hex characters.
func validID(id string) bool {
	if len(id) != idHexLen {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
