//go:build client && !server

package ast

import (
	"context"
	"crypto/sha1" //nolint:gosec // git blob object IDs are SHA-1 by definition; used for comparison only
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Scan limits and the cache location.
const (
	// DefaultMaxFileBytes is the largest source file the scanner parses. Larger
	// files are recorded as coverage gaps.
	DefaultMaxFileBytes int64 = 2 << 20
	// DefaultParseTimeout bounds the parse of one file.
	DefaultParseTimeout = 2 * time.Second
	// DefaultScanTimeout bounds a whole scan. When it elapses the scan returns a
	// partial graph with Coverage.TimedOut set.
	DefaultScanTimeout = 120 * time.Second
	// SnapshotBudgetBytes is the serialized size the graph handed to avatars may
	// not exceed in one stage.
	SnapshotBudgetBytes = 200 << 10
	// CachePath is the repository-relative location of the parse cache.
	CachePath = "docs/plans/.plan/cache/graph.json"

	maxScanFiles       = 200_000
	maxDocBytes        = 1 << 20
	maxDocFiles        = 200
	maxCacheBytes      = 64 << 20
	maxStackFiles      = 50
	maxStackFileBytes  = 256 << 10
	cacheSchemaVersion = 1
	// extractorVersion invalidates cached parse results. Bump it when
	// symbols.go changes what it extracts; query changes are detected from the
	// embedded query text.
	extractorVersion = 1
	// racyWindow is how close to the scan start a file's mtime may be before its
	// cached result is not trusted: a write inside the same timestamp tick would
	// leave size and mtime unchanged.
	racyWindow = 2 * time.Second

	// snapshotReserve leaves room for the truncation statement.
	snapshotReserve = 2048
	// snapshotMaxGaps caps the parse gaps copied into a snapshot.
	snapshotMaxGaps = 50
)

// Coverage gap reasons. Each Coverage.ParseGaps entry has the form
// "<path>: <REASON> <detail>".
const (
	// GapParseError marks a file whose syntax tree contains error nodes. Symbols
	// from the valid subtrees are still listed.
	GapParseError = "PARSE_ERROR"
	// GapParseTimeout marks a file whose parse exceeded the per-file budget.
	GapParseTimeout = "PARSE_TIMEOUT"
	// GapTooLarge marks a source file above the per-file size cap.
	GapTooLarge = "TOO_LARGE"
	// GapUnreadable marks a file that could not be opened or read.
	GapUnreadable = "UNREADABLE"
)

// NoteFileListTruncated prefixes the Coverage.Notes entry written when the file
// list was cut at the scanner's limit. Documentation drift treats such a list
// as incomplete.
const NoteFileListTruncated = "FILE_LIST_TRUNCATED"

// hardExcludedSegments are directory names the scanner never reads, wherever
// they appear in a path. They hold tool state, agent configuration and
// third-party code rather than the project's own source.
var hardExcludedSegments = map[string]bool{
	".plan": true, ".agents": true, ".omp": true, ".claude": true, ".gemini": true,
	".agy": true, "node_modules": true, "vendor": true, ".git": true,
}

// IsHardExcluded reports whether a repository-relative path lies under a
// directory the scanner never reads: .plan, .agents, .omp, .claude, .gemini,
// .agy, node_modules, vendor or .git.
func IsHardExcluded(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if hardExcludedSegments[seg] {
			return true
		}
	}
	return false
}

// ScanOptions configures Scan.
type ScanOptions struct {
	// Root is the repository, or any directory inside one. Inside a Git work
	// tree the scan always covers the top-level directory. Empty means ".".
	Root string
	// Workers is the number of parallel parse workers. Zero selects
	// min(4, GOMAXPROCS).
	Workers int
	// Timeout bounds the whole scan. Zero selects DefaultScanTimeout.
	Timeout time.Duration
	// ParseTimeout bounds one file's parse. Zero selects DefaultParseTimeout.
	ParseTimeout time.Duration
	// MaxFileBytes is the per-file size cap. Zero selects DefaultMaxFileBytes.
	MaxFileBytes int64
	// ReadCache lets unchanged files reuse the result recorded in CachePath.
	ReadCache bool
	// WriteCache persists the results to CachePath, creating .plan/.gitignore
	// when it is missing. The hook command leaves it false: an in-memory scan
	// writes nothing.
	WriteCache bool
	// SkipDocDrift leaves Graph.DocDriftReports empty.
	SkipDocDrift bool
	// Progress, when set, is called after each file with the running totals. It
	// may be called from several goroutines, one call at a time.
	Progress func(ScanProgress)
}

// ScanProgress reports how far a scan has come.
type ScanProgress struct {
	FilesTotal int
	FilesDone  int
}

func (o ScanOptions) withDefaults() ScanOptions {
	if o.Root == "" {
		o.Root = "."
	}
	if o.Workers <= 0 {
		o.Workers = min(4, runtime.GOMAXPROCS(0))
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultScanTimeout
	}
	if o.ParseTimeout <= 0 {
		o.ParseTimeout = DefaultParseTimeout
	}
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = DefaultMaxFileBytes
	}
	return o
}

// Scan reads a repository and returns its context graph: modules, symbols,
// routes, the detected stack, documentation drift and the coverage the scan
// achieved.
//
// Inside a Git work tree the files are the ones git lists (tracked plus
// untracked files no ignore rule hides); outside one the tree is walked with the
// root .gitignore applied. In both cases the directories named in
// IsHardExcluded and every symbolic link are skipped. All Git access goes
// through the Git type, which never runs repository-configured commands.
//
// WorktreeHash is the SHA-256 over the sorted lines "path\0sha256\n" of every
// selected file. A scan that times out returns a partial graph with
// Coverage.TimedOut set; a cancelled context returns the context error. Gaps
// are recorded, never treated as the absence of a symbol.
//
// Symbol.Imports is populated on the first symbol of each file with the file's
// import list.
func Scan(ctx context.Context, opts ScanOptions) (*contracts.RepoContextGraph, error) {
	opts = opts.withDefaults()
	started := time.Now()

	root, err := resolveRoot(opts.Root)
	if err != nil {
		return nil, err
	}
	sel, err := selectFiles(ctx, root)
	if err != nil {
		return nil, err
	}
	root = sel.root

	set, err := compileLanguages()
	if err != nil {
		return nil, err
	}
	defer set.Close()
	pool := newParserPool()
	defer pool.Close()

	digest := queriesDigest()
	var cache map[string]cacheEntry
	if opts.ReadCache {
		cache = loadCache(root, digest)
	}
	oidLen := 40
	for _, e := range sel.index {
		oidLen = len(e.OID)
		break
	}

	sctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	proc := &scanProc{
		root:         root,
		set:          set,
		pool:         pool,
		index:        sel.index,
		oidLen:       oidLen,
		cache:        cache,
		maxBytes:     opts.MaxFileBytes,
		parseTimeout: opts.ParseTimeout,
	}
	results := make([]fileResult, len(sel.files))
	var done atomic.Int64
	var progressMu sync.Mutex
	report := func() {
		n := int(done.Add(1))
		if opts.Progress != nil {
			progressMu.Lock()
			opts.Progress(ScanProgress{FilesTotal: len(sel.files), FilesDone: n})
			progressMu.Unlock()
		}
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	for range opts.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			qc := ts.NewQueryCursor()
			defer qc.Close()
			for i := range jobs {
				if sctx.Err() != nil {
					continue
				}
				results[i] = proc.process(sctx, qc, sel.files[i])
				if results[i].processed {
					report()
				}
			}
		}()
	}
feed:
	for i := range sel.files {
		select {
		case jobs <- i:
		case <-sctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timedOut := false
	for i := range results {
		if !results[i].processed {
			timedOut = true
			break
		}
	}

	g := assembleGraph(root, sel, results, timedOut)
	if opts.WriteCache && !timedOut {
		if werr := writeCache(root, digest, started, sel.files, results); werr != nil {
			g.Coverage.Notes = append(g.Coverage.Notes, "the parse cache was not written: "+werr.Error())
		}
	}
	if !opts.SkipDocDrift {
		docs, notes := readDocs(root, sel.files, results)
		g.Coverage.Notes = append(g.Coverage.Notes, notes...)
		var files []string
		for i := range results {
			if results[i].processed && !results[i].skipped {
				files = append(files, sel.files[i])
			}
		}
		g.DocDriftReports = DetectDocDrift(DriftInput{Docs: docs, Graph: g, Files: files})
	}
	return g, nil
}

// ------------------------------------------------------------ file selection

type selection struct {
	root      string
	git       *Git
	files     []string
	excluded  int
	truncated bool
	notes     []string
	index     map[string]IndexEntry
	head      string
	branch    string
}

func resolveRoot(root string) (string, error) {
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("ast: resolve scan root: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("ast: resolve scan root: %w", err)
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("ast: scan root: %w", err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("ast: scan root is not a directory")
	}
	return real, nil
}

// selectFiles lists the repository files through git when possible and by
// walking the tree otherwise, then applies the hard excludes.
func selectFiles(ctx context.Context, root string) (selection, error) {
	sel := selection{root: root}
	var listed []string
	inGit := false
	if g, err := NewGit(root); err != nil {
		sel.notes = append(sel.notes, "git is not installed; files were selected by walking the directory tree")
	} else if g.InsideWorkTree(ctx) {
		if top, terr := g.TopLevel(ctx); terr == nil {
			if real, rerr := filepath.EvalSymlinks(top); rerr == nil {
				top = real
			}
			if tg, nerr := NewGit(top); nerr == nil {
				files, lerr := tg.ListFiles(ctx)
				if lerr == nil {
					sel.git, sel.root, listed, inGit = tg, top, files, true
				} else {
					sel.notes = append(sel.notes, "git file listing failed; files were selected by walking the directory tree")
				}
			}
		}
	} else {
		sel.notes = append(sel.notes, "not a Git repository; files were selected by walking the tree with the root .gitignore")
	}
	if err := ctx.Err(); err != nil {
		return sel, err
	}
	if !inGit {
		files, truncated, err := walkTree(ctx, root)
		if err != nil {
			return sel, err
		}
		listed, sel.truncated = files, truncated
	} else {
		sel.head, _ = sel.git.Head(ctx)
		sel.branch, _ = sel.git.Branch(ctx)
		if idx, err := sel.git.IndexEntries(ctx); err == nil {
			sel.index = idx
		}
	}

	kept := make([]string, 0, len(listed))
	for _, f := range listed {
		if IsHardExcluded(f) {
			sel.excluded++
			continue
		}
		kept = append(kept, f)
	}
	sort.Strings(kept)
	if len(kept) > maxScanFiles {
		kept = kept[:maxScanFiles]
		sel.truncated = true
	}
	if sel.truncated {
		sel.notes = append(sel.notes, fmt.Sprintf("%s: only the first %d files were scanned", NoteFileListTruncated, maxScanFiles))
	}
	sel.files = kept
	return sel, nil
}

// walkTree lists the files of a directory tree, applying the root .gitignore.
// Symbolic links and other irregular entries are listed so the caller counts
// and skips them; directories are never followed through links.
func walkTree(ctx context.Context, root string) (files []string, truncated bool, err error) {
	ig := &ignoreSet{}
	if data, rerr := readSmall(root, ".gitignore", 1<<20); rerr == nil {
		ig = parseIgnore(data)
	}
	werr := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if p == root {
				return walkErr
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if hardExcludedSegments[d.Name()] || ig.match(rel, true) {
				return fs.SkipDir
			}
			return nil
		}
		if ig.match(rel, false) {
			return nil
		}
		if len(files) >= maxScanFiles+1 {
			truncated = true
			return fs.SkipAll
		}
		files = append(files, rel)
		return nil
	})
	if werr != nil {
		return nil, false, werr
	}
	return files, truncated, nil
}

// readSmall reads a regular, non-symlink file of at most max bytes below root.
func readSmall(root, rel string, max int64) ([]byte, error) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	if info.Size() > max {
		return nil, fmt.Errorf("file exceeds %d bytes", max)
	}
	return os.ReadFile(abs)
}

// ----------------------------------------------------------- .gitignore subset

// ignoreRule is one parsed .gitignore pattern.
type ignoreRule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
	base    bool // no slash in the pattern: match the base name at any depth
}

// ignoreSet is the root .gitignore of a tree that is not a Git work tree. It
// implements the documented pattern syntax (comments, negation, anchoring,
// trailing slash, *, ?, [...] and **), evaluated last match wins. Nested
// .gitignore files and global excludes apply only when git does the listing.
type ignoreSet struct {
	rules []ignoreRule
}

func parseIgnore(data []byte) *ignoreSet {
	set := &ignoreSet{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, `\ `) {
			line = strings.TrimSuffix(line, " ")
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule := ignoreRule{}
		switch {
		case strings.HasPrefix(line, "!"):
			rule.negate = true
			line = line[1:]
		case strings.HasPrefix(line, `\#`), strings.HasPrefix(line, `\!`):
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			rule.dirOnly = true
			line = strings.TrimRight(line, "/")
		}
		if line == "" {
			continue
		}
		rule.base = !strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")
		re, err := globToRegexp(line)
		if err != nil {
			continue
		}
		rule.re = re
		set.rules = append(set.rules, rule)
	}
	return set
}

func (s *ignoreSet) match(rel string, isDir bool) bool {
	ignored := false
	for _, r := range s.rules {
		if r.dirOnly && !isDir {
			continue
		}
		target := rel
		if r.base {
			target = path.Base(rel)
		}
		if r.re.MatchString(target) {
			ignored = !r.negate
		}
	}
	return ignored
}

// globToRegexp translates a .gitignore glob to an anchored regular expression.
func globToRegexp(pattern string) (*regexp.Regexp, error) {
	p := []rune(pattern)
	var sb strings.Builder
	sb.WriteString("^")
	for i := 0; i < len(p); {
		c := p[i]
		switch c {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				j := i + 2
				switch {
				case j < len(p) && p[j] == '/' && (i == 0 || p[i-1] == '/'):
					sb.WriteString("(?:.*/)?")
					i = j + 1
				case j == len(p) && i > 0 && p[i-1] == '/':
					sb.WriteString(".*")
					i = j
				default:
					sb.WriteString("[^/]*")
					i = j
				}
				continue
			}
			sb.WriteString("[^/]*")
			i++
		case '?':
			sb.WriteString("[^/]")
			i++
		case '[':
			j := i + 1
			if j < len(p) && (p[j] == '!' || p[j] == '^') {
				j++
			}
			if j < len(p) && p[j] == ']' {
				j++
			}
			for j < len(p) && p[j] != ']' {
				j++
			}
			if j >= len(p) {
				sb.WriteString(`\[`)
				i++
				continue
			}
			class := string(p[i+1 : j])
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			sb.WriteString("[" + class + "]")
			i = j + 1
		case '\\':
			if i+1 < len(p) {
				sb.WriteString(regexp.QuoteMeta(string(p[i+1])))
				i += 2
			} else {
				sb.WriteString(`\\`)
				i++
			}
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}

// ----------------------------------------------------------------- per file

// fileResult is what one worker learned about one file.
type fileResult struct {
	processed   bool
	skipped     bool
	size        int64
	mtimeNS     int64
	sha         string
	oid         string
	tracked     bool
	lang        string
	routed      bool
	unsupported bool
	parsed      bool
	cached      bool
	errorNodes  int
	extract     fileExtract
	gaps        []string
}

// scanProc holds the read-only state the workers share.
type scanProc struct {
	root         string
	set          *languageSet
	pool         *parserPool
	index        map[string]IndexEntry
	oidLen       int
	cache        map[string]cacheEntry
	maxBytes     int64
	parseTimeout time.Duration
}

func gapString(rel, reason, detail string) string {
	if detail == "" {
		return rel + ": " + reason
	}
	return rel + ": " + reason + " " + detail
}

var copyBufPool = sync.Pool{New: func() any {
	b := make([]byte, 64<<10)
	return &b
}}

// copyCtx copies src to dst, checking the context between chunks.
func copyCtx(ctx context.Context, dst io.Writer, src io.Reader) error {
	bp := copyBufPool.Get().(*[]byte)
	defer copyBufPool.Put(bp)
	buf := *bp
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// newBlobHasher returns a hasher primed with the git blob header. Writing a file's
// bytes to it yields the object ID git computes for the same bytes with no
// attribute filters, which is what `git hash-object --stdin` reports.
func newBlobHasher(oidLen int, size int64) hash.Hash {
	var h hash.Hash
	if oidLen == 64 {
		h = sha256.New()
	} else {
		h = sha1.New() //nolint:gosec // object IDs of SHA-1 repositories
	}
	fmt.Fprintf(h, "blob %d\x00", size)
	return h
}

// process reads, hashes and parses one file. A result that is not marked
// processed was abandoned because the scan ended.
func (p *scanProc) process(ctx context.Context, qc *ts.QueryCursor, rel string) fileResult {
	res := fileResult{processed: true}
	abs := filepath.Join(p.root, filepath.FromSlash(rel))
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() {
		res.skipped = true
		return res
	}
	res.size, res.mtimeNS = info.Size(), info.ModTime().UnixNano()
	cl := p.set.forPath(rel)
	if cl != nil {
		res.routed = true
		res.lang = cl.spec.name
	}
	res.unsupported = cl == nil && otherSourceExts[strings.ToLower(path.Ext(rel))]
	entry, tracked := p.index[rel]
	res.tracked = tracked
	tooLarge := cl != nil && res.size > p.maxBytes
	if tooLarge {
		res.gaps = append(res.gaps, gapString(rel, GapTooLarge, fmt.Sprintf("%d bytes exceeds the %d byte limit", res.size, p.maxBytes)))
	}

	if ce, ok := p.cache[rel]; ok && ce.fresh(res.size, res.mtimeNS) && (!tracked || ce.OID != "") && (cl == nil || tooLarge || ce.Parsed) {
		res.sha, res.oid, res.cached = ce.SHA256, ce.OID, true
		if ce.Parsed && !tooLarge && ce.Extract != nil {
			res.parsed, res.extract, res.errorNodes = true, *ce.Extract, ce.ErrorNodes
			if ce.ErrorNodes > 0 {
				res.gaps = append(res.gaps, gapString(rel, GapParseError, fmt.Sprintf("%d error nodes", ce.ErrorNodes)))
			}
		}
		return res
	}

	f, err := os.Open(abs)
	if err != nil {
		detail := "cannot open file"
		if errors.Is(err, fs.ErrPermission) {
			detail = "permission denied"
		}
		res.gaps = append(res.gaps, gapString(rel, GapUnreadable, detail))
		return res
	}
	defer f.Close()

	sum := sha256.New()
	writers := []io.Writer{sum}
	var blob hash.Hash
	var counted countingWriter
	if tracked && (len(entry.OID) == 40 || len(entry.OID) == 64) {
		blob = newBlobHasher(len(entry.OID), res.size)
		writers = append(writers, blob, &counted)
	}
	w := io.MultiWriter(writers...)

	var src []byte
	if cl != nil && !tooLarge {
		src, err = io.ReadAll(io.LimitReader(f, p.maxBytes+1))
		if err != nil {
			res.gaps = append(res.gaps, gapString(rel, GapUnreadable, "read failed"))
			return res
		}
		_, _ = w.Write(src)
		if int64(len(src)) > p.maxBytes {
			src = nil
			tooLarge = true
			res.gaps = append(res.gaps, gapString(rel, GapTooLarge, fmt.Sprintf("file grew beyond the %d byte limit", p.maxBytes)))
		}
	}
	if src == nil {
		if err := copyCtx(ctx, w, f); err != nil {
			if ctx.Err() != nil {
				res.processed = false
				return res
			}
			res.gaps = append(res.gaps, gapString(rel, GapUnreadable, "read failed"))
			return res
		}
	}
	res.sha = hex.EncodeToString(sum.Sum(nil))
	if blob != nil && counted.n == res.size {
		res.oid = hex.EncodeToString(blob.Sum(nil))
	}
	if src == nil {
		return res
	}

	ex, errNodes, status, perr := parseAndExtract(ctx, p.pool, cl, qc, p.parseTimeout, rel, src)
	switch {
	case perr != nil:
		res.gaps = append(res.gaps, gapString(rel, GapUnreadable, "no parser available"))
	case status == parseCancelled:
		res.processed = false
	case status == parseTimeout:
		res.gaps = append(res.gaps, gapString(rel, GapParseTimeout, fmt.Sprintf("parse exceeded %s", p.parseTimeout)))
	default:
		res.parsed, res.extract, res.errorNodes = true, ex, errNodes
		if errNodes > 0 {
			res.gaps = append(res.gaps, gapString(rel, GapParseError, fmt.Sprintf("%d error nodes", errNodes)))
		}
	}
	return res
}

// parseAndExtract parses one source file and runs the language query over it.
// The syntax tree is closed before returning. errorNodes counts the ERROR and
// MISSING nodes; a tree with errors still yields the symbols of its valid
// subtrees.
func parseAndExtract(ctx context.Context, pool *parserPool, cl *compiledLang, qc *ts.QueryCursor, timeout time.Duration, rel string, src []byte) (ex fileExtract, errorNodes int, status parseStatus, err error) {
	pp, err := pool.acquire(cl)
	if err != nil {
		return fileExtract{}, 0, parseOK, err
	}
	tree, status := pp.parse(ctx, timeout, src)
	pool.release(pp)
	if status != parseOK {
		return fileExtract{}, 0, status, nil
	}
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() {
		errorNodes = countErrorNodes(root)
	}
	return extractFile(cl, qc, root, src, rel), errorNodes, parseOK, nil
}

// ------------------------------------------------------------- graph assembly

type moduleKey struct {
	dir  string
	lang string
}

func moduleID(lang, dir string) string { return "mod:" + lang + ":" + dir }

func assembleGraph(root string, sel selection, results []fileResult, timedOut bool) *contracts.RepoContextGraph {
	cov := contracts.Coverage{TimedOut: timedOut}
	symbols := map[string][]contracts.Symbol{}
	var routes []contracts.RouteEvidence
	langCounts := map[string]int{}
	mods := map[moduleKey]*contracts.ModuleBoundary{}
	var hashed []string
	dirty, unprocessed := 0, 0

	for i := range results {
		rel := sel.files[i]
		r := results[i]
		if !r.processed {
			unprocessed++
			continue
		}
		if r.skipped {
			cov.FilesSkipped++
			continue
		}
		if r.sha != "" {
			hashed = append(hashed, rel+"\x00"+r.sha+"\n")
		}
		if r.tracked && r.oid != "" {
			if e, ok := sel.index[rel]; ok && e.Stage == 0 && (e.Mode == "100644" || e.Mode == "100755") && e.OID != r.oid {
				dirty++
			}
		}
		if r.unsupported {
			cov.UnsupportedFiles++
		}
		cov.ParseGaps = append(cov.ParseGaps, r.gaps...)
		if !r.parsed {
			if r.routed {
				cov.FilesSkipped++
			}
			continue
		}
		cov.FilesScanned++
		langCounts[r.lang]++
		key := moduleKey{dir: path.Dir(rel), lang: r.lang}
		m := mods[key]
		if m == nil {
			m = &contracts.ModuleBoundary{ID: moduleID(r.lang, key.dir), Path: key.dir, Language: r.lang}
			mods[key] = m
		}
		if len(r.extract.Symbols) > 0 {
			syms := append([]contracts.Symbol(nil), r.extract.Symbols...)
			if len(r.extract.Imports) > 0 {
				syms[0].Imports = append([]string(nil), r.extract.Imports...)
			}
			symbols[rel] = syms
			for _, s := range syms {
				m.SymbolIDs = append(m.SymbolIDs, s.ID)
			}
		}
		routes = append(routes, r.extract.Routes...)
	}
	sort.Strings(cov.ParseGaps)

	sort.SliceStable(routes, func(i, j int) bool {
		a, b := routes[i], routes[j]
		if a.FilePath != b.FilePath {
			return a.FilePath < b.FilePath
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Method < b.Method
	})
	modules := make([]contracts.ModuleBoundary, 0, len(mods))
	for _, m := range mods {
		sort.Strings(m.SymbolIDs)
		modules = append(modules, *m)
	}
	sort.Slice(modules, func(i, j int) bool {
		if modules[i].Path != modules[j].Path {
			return modules[i].Path < modules[j].Path
		}
		return modules[i].Language < modules[j].Language
	})

	sort.Strings(hashed)
	h := sha256.New()
	for _, line := range hashed {
		_, _ = h.Write([]byte(line))
	}

	notes := append([]string(nil), sel.notes...)
	if sel.excluded > 0 {
		notes = append(notes, fmt.Sprintf("%d files under excluded directories (.plan, .agents, .omp, .claude, .gemini, .agy, node_modules, vendor) were not scanned", sel.excluded))
	}
	if dirty > 0 {
		notes = append(notes, fmt.Sprintf("%d tracked files differ from the git index (advisory: clean filters can report false differences)", dirty))
	}
	if timedOut {
		notes = append(notes, fmt.Sprintf("the scan timed out; %d of %d files were not processed, so the graph is partial", unprocessed, len(results)))
	}
	cov.Notes = notes

	return &contracts.RepoContextGraph{
		SchemaVersion:   contracts.SchemaVersion,
		RootPath:        root,
		GitCommitHash:   sel.head,
		GitBranch:       sel.branch,
		WorktreeHash:    hex.EncodeToString(h.Sum(nil)),
		DetectedStack:   detectStack(root, sel.files, langCounts),
		Modules:         modules,
		Symbols:         symbols,
		Routes:          routes,
		DocDriftReports: []contracts.DocDriftItem{},
		Coverage:        cov,
	}
}

// stackMarkers map root and nested build files to stack labels. A marker with
// no needle fires on the file's presence.
var stackMarkers = []struct{ file, needle, label string }{
	{"go.mod", "", "go-modules"},
	{"go.mod", "github.com/gin-gonic/gin", "gin"},
	{"go.mod", "github.com/labstack/echo", "echo"},
	{"go.mod", "github.com/go-chi/chi", "chi"},
	{"package.json", "", "node"},
	{"package.json", `"react"`, "react"},
	{"package.json", `"vite"`, "vite"},
	{"package.json", `"next"`, "next"},
	{"package.json", `"express"`, "express"},
	{"package.json", `"fastify"`, "fastify"},
	{"package.json", `"vue"`, "vue"},
	{"package.json", `"svelte"`, "svelte"},
	{"tsconfig.json", "", "typescript-config"},
	{"Cargo.toml", "", "cargo"},
	{"Cargo.toml", "axum", "axum"},
	{"Cargo.toml", "actix-web", "actix-web"},
	{"pyproject.toml", "", "python-packaging"},
	{"pyproject.toml", "fastapi", "fastapi"},
	{"pyproject.toml", "flask", "flask"},
	{"pyproject.toml", "django", "django"},
	{"requirements.txt", "fastapi", "fastapi"},
	{"requirements.txt", "flask", "flask"},
	{"requirements.txt", "django", "django"},
	{"Dockerfile", "", "docker"},
}

// detectStack names the languages with parsed files and the frameworks and
// build tools the repository's manifest files point at. The result is sorted
// and free of duplicates.
func detectStack(root string, files []string, langCounts map[string]int) []string {
	set := map[string]bool{}
	for lang, n := range langCounts {
		if n > 0 {
			set[lang] = true
		}
	}
	reads := 0
	for _, rel := range files {
		base := path.Base(rel)
		var content string
		loaded := false
		for _, m := range stackMarkers {
			if m.file != base {
				continue
			}
			if m.needle == "" {
				set[m.label] = true
				continue
			}
			if !loaded {
				if reads >= maxStackFiles {
					break
				}
				reads++
				loaded = true
				if data, err := readSmall(root, rel, maxStackFileBytes); err == nil {
					content = strings.ToLower(string(data))
				}
			}
			if content != "" && strings.Contains(content, strings.ToLower(m.needle)) {
				set[m.label] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// isDocPath selects the documentation files drift detection reads: the root
// README.md and ARCHITECTURE.md and every Markdown file under docs/.
func isDocPath(rel string) bool {
	lower := strings.ToLower(rel)
	if lower == "readme.md" || lower == "architecture.md" {
		return true
	}
	return strings.HasPrefix(lower, "docs/") && strings.HasSuffix(lower, ".md")
}

// readDocs loads the documentation files among the scanned paths. Oversized or
// unreadable documents are skipped with a note.
func readDocs(root string, files []string, results []fileResult) ([]DocFile, []string) {
	var docs []DocFile
	var notes []string
	for i, rel := range files {
		if !isDocPath(rel) || !results[i].processed || results[i].skipped {
			continue
		}
		if len(docs) >= maxDocFiles {
			notes = append(notes, fmt.Sprintf("only the first %d documentation files were checked for drift", maxDocFiles))
			break
		}
		data, err := readSmall(root, rel, maxDocBytes)
		if err != nil {
			notes = append(notes, fmt.Sprintf("documentation file %s was not checked for drift: it is unreadable or larger than %d bytes", rel, maxDocBytes))
			continue
		}
		docs = append(docs, DocFile{Path: rel, Content: data})
	}
	return docs, notes
}

// ----------------------------------------------------------------- the cache

// cacheEntry is one file's recorded result.
type cacheEntry struct {
	Size       int64        `json:"size"`
	MtimeNS    int64        `json:"mtime_ns"`
	Racy       bool         `json:"racy,omitempty"`
	SHA256     string       `json:"sha256"`
	OID        string       `json:"oid,omitempty"`
	Parsed     bool         `json:"parsed,omitempty"`
	ErrorNodes int          `json:"error_nodes,omitempty"`
	Extract    *fileExtract `json:"extract,omitempty"`
}

func (c cacheEntry) fresh(size, mtimeNS int64) bool {
	return !c.Racy && c.SHA256 != "" && c.Size == size && c.MtimeNS == mtimeNS
}

// graphCache is the content of CachePath.
type graphCache struct {
	Version int                   `json:"version"`
	Digest  string                `json:"digest"`
	Entries map[string]cacheEntry `json:"entries"`
}

// queriesDigest identifies the extraction logic a cached result came from.
func queriesDigest() string {
	h := sha256.New()
	fmt.Fprintf(h, "extractor=%d\x00", extractorVersion)
	entries, _ := queryFS.ReadDir("queries")
	for _, e := range entries {
		b, _ := queryFS.ReadFile("queries/" + e.Name())
		_, _ = h.Write([]byte(e.Name()))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(b)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// cacheDir returns <root>/docs/plans/.plan/cache. Each component must be a plain
// directory, never a symbolic link; with create the missing ones are made.
func cacheDir(root string, create bool) (string, error) {
	cur := root
	for _, seg := range []string{"docs", "plans", ".plan", "cache"} {
		cur = filepath.Join(cur, seg)
		info, err := os.Lstat(cur)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return "", fmt.Errorf("%s is not a plain directory", seg)
			}
		case errors.Is(err, fs.ErrNotExist):
			if !create {
				return "", err
			}
			perm := fs.FileMode(0o755)
			if seg == "cache" {
				perm = 0o700
			}
			if merr := os.Mkdir(cur, perm); merr != nil {
				return "", fmt.Errorf("cannot create %s", seg)
			}
		default:
			return "", fmt.Errorf("cannot inspect %s", seg)
		}
	}
	return cur, nil
}

// loadCache reads the cache. Any problem — a missing file, a symlinked path, an
// unknown version or a digest from different extraction logic — yields no cache
// rather than an error: the scan simply parses everything.
func loadCache(root, digest string) map[string]cacheEntry {
	dir, err := cacheDir(root, false)
	if err != nil {
		return nil
	}
	file := filepath.Join(dir, "graph.json")
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxCacheBytes {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	var gc graphCache
	if err := json.NewDecoder(io.LimitReader(f, maxCacheBytes)).Decode(&gc); err != nil {
		return nil
	}
	if gc.Version != cacheSchemaVersion || gc.Digest != digest {
		return nil
	}
	return gc.Entries
}

const planGitignore = "*\n!.gitignore\n!synthesized_spec.md\n!plan_manifest.json\n"

// writeCache records the results of a completed scan. It creates
// .plan/.gitignore when absent so the cache never reaches a commit, and
// replaces the cache file atomically.
func writeCache(root, digest string, started time.Time, files []string, results []fileResult) error {
	dir, err := cacheDir(root, true)
	if err != nil {
		return err
	}
	ignorePath := filepath.Join(filepath.Dir(dir), ".gitignore")
	if f, oerr := os.OpenFile(ignorePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); oerr == nil {
		_, werr := f.WriteString(planGitignore)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return fmt.Errorf("cannot write .plan/.gitignore")
		}
	} else if !errors.Is(oerr, fs.ErrExist) {
		return fmt.Errorf("cannot create .plan/.gitignore")
	}

	racyAfter := started.Add(-racyWindow).UnixNano()
	gc := graphCache{Version: cacheSchemaVersion, Digest: digest, Entries: map[string]cacheEntry{}}
	for i, rel := range files {
		r := results[i]
		if !r.processed || r.skipped || r.sha == "" {
			continue
		}
		if r.routed && !r.parsed && !hasGap(r.gaps, GapTooLarge) {
			continue // a timeout or read failure is not worth remembering
		}
		e := cacheEntry{
			Size: r.size, MtimeNS: r.mtimeNS, Racy: r.mtimeNS >= racyAfter,
			SHA256: r.sha, OID: r.oid, Parsed: r.parsed, ErrorNodes: r.errorNodes,
		}
		if r.parsed {
			ex := r.extract
			e.Extract = &ex
		}
		gc.Entries[rel] = e
	}

	tmp, err := os.CreateTemp(dir, "graph-*.tmp")
	if err != nil {
		return fmt.Errorf("cannot create a temporary cache file")
	}
	tmpName := tmp.Name()
	enc := json.NewEncoder(tmp)
	werr := enc.Encode(&gc)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cannot write the cache file")
	}
	if err := os.Rename(tmpName, filepath.Join(dir, "graph.json")); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cannot replace the cache file")
	}
	return nil
}

func hasGap(gaps []string, reason string) bool {
	for _, g := range gaps {
		if strings.Contains(g, ": "+reason) {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ snapshot

// SnapshotOptions tunes Snapshot.
type SnapshotOptions struct {
	// Keywords rank files when the graph exceeds the budget. Files whose path
	// or symbol names contain a keyword are kept first, then the other files of
	// their modules. KeywordsFromText derives them from the PM stories.
	Keywords []string
	// MaxBytes is the serialized size limit. Zero selects SnapshotBudgetBytes.
	MaxBytes int
}

// SnapshotInfo describes how a snapshot was cut.
type SnapshotInfo struct {
	// Truncated is true when files or symbols were left out to fit the budget.
	Truncated bool `json:"truncated"`
	// SerializedBytes is the JSON size of the snapshot.
	SerializedBytes int `json:"serialized_bytes"`
	// BudgetBytes is the limit that applied.
	BudgetBytes int `json:"budget_bytes"`
	// TotalFiles and IncludedFiles count files that carry exported symbols or
	// routes.
	TotalFiles    int `json:"total_files"`
	IncludedFiles int `json:"included_files"`
	// TotalSymbols and IncludedSymbols count exported symbols.
	TotalSymbols    int `json:"total_symbols"`
	IncludedSymbols int `json:"included_symbols"`
	// OmittedModules lists up to 50 module paths with no included file.
	OmittedModules []string `json:"omitted_modules,omitempty"`
	// Statement is the sentence stating the truncation. It is also appended to
	// the snapshot's Coverage.Notes, so it reaches the prompt and Page 2.
	Statement string `json:"statement,omitempty"`
}

// snapUnit is the smallest piece a snapshot keeps or drops: one file's exported
// symbols and route registrations.
type snapUnit struct {
	path   string
	dir    string
	lang   string
	syms   []contracts.Symbol
	routes []contracts.RouteEvidence
	cost   int
	score  int
	tier   int
}

// Snapshot returns the bounded view of a graph handed to the avatars: modules,
// exported symbols and routes, with the local root path removed and no drift
// reports. When the serialized graph exceeds the budget, files that match the
// keywords come first, then the remaining files of the same modules, then the
// rest by path; the cut is stated in the returned info and in the snapshot's
// Coverage.Notes. A nil graph yields an empty snapshot.
func Snapshot(g *contracts.RepoContextGraph, opts SnapshotOptions) (contracts.RepoContextGraph, SnapshotInfo) {
	budget := opts.MaxBytes
	if budget <= 0 {
		budget = SnapshotBudgetBytes
	}
	info := SnapshotInfo{BudgetBytes: budget}
	if g == nil {
		return contracts.RepoContextGraph{SchemaVersion: contracts.SchemaVersion}, info
	}
	shell := snapshotShell(g)
	units := snapshotUnits(g)
	info.TotalFiles = len(units)
	for _, u := range units {
		info.TotalSymbols += len(u.syms)
	}

	full := assembleSnapshot(shell, units)
	if size := jsonSize(full); size <= budget {
		info.IncludedFiles, info.IncludedSymbols, info.SerializedBytes = info.TotalFiles, info.TotalSymbols, size
		return full, info
	}

	rankUnits(units, opts.Keywords)
	order := make([]int, len(units))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ua, ub := units[order[a]], units[order[b]]
		if ua.tier != ub.tier {
			return ua.tier < ub.tier
		}
		if ua.score != ub.score {
			return ua.score > ub.score
		}
		return ua.path < ub.path
	})

	used := jsonSize(assembleSnapshot(shell, nil)) + snapshotReserve
	var chosen []snapUnit
	for _, idx := range order {
		u := units[idx]
		if used+u.cost <= budget {
			chosen = append(chosen, u)
			used += u.cost
			continue
		}
		if part, ok := fitPrefix(u, budget-used); ok {
			chosen = append(chosen, part)
			used += part.cost
		}
	}

	build := func() contracts.RepoContextGraph {
		keep := append([]snapUnit(nil), chosen...)
		snap := assembleSnapshot(shell, keep)
		info.IncludedFiles, info.IncludedSymbols = len(keep), 0
		for _, u := range keep {
			info.IncludedSymbols += len(u.syms)
		}
		info.OmittedModules = omittedModules(units, keep)
		info.Statement = snapshotStatement(info, budget)
		snap.Coverage.Notes = append(append([]string(nil), snap.Coverage.Notes...), info.Statement)
		return snap
	}
	snap := build()
	for jsonSize(snap) > budget && len(chosen) > 0 {
		chosen = chosen[:len(chosen)-1]
		snap = build()
	}
	info.Truncated = true
	info.SerializedBytes = jsonSize(snap)
	return snap, info
}

func jsonSize(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// snapshotShell copies the fixed parts of the graph into a snapshot.
func snapshotShell(g *contracts.RepoContextGraph) contracts.RepoContextGraph {
	shell := contracts.RepoContextGraph{
		SchemaVersion:   g.SchemaVersion,
		GitCommitHash:   g.GitCommitHash,
		GitBranch:       g.GitBranch,
		WorktreeHash:    g.WorktreeHash,
		DetectedStack:   append([]string{}, g.DetectedStack...),
		Modules:         []contracts.ModuleBoundary{},
		Symbols:         map[string][]contracts.Symbol{},
		DocDriftReports: []contracts.DocDriftItem{},
		Coverage:        g.Coverage,
	}
	shell.Coverage.Notes = append([]string(nil), g.Coverage.Notes...)
	shell.Coverage.ParseGaps = append([]string(nil), g.Coverage.ParseGaps...)
	if len(shell.Coverage.ParseGaps) > snapshotMaxGaps {
		extra := len(shell.Coverage.ParseGaps) - snapshotMaxGaps
		shell.Coverage.ParseGaps = shell.Coverage.ParseGaps[:snapshotMaxGaps]
		shell.Coverage.Notes = append(shell.Coverage.Notes, fmt.Sprintf("%d more parse gaps are not listed in this snapshot", extra))
	}
	return shell
}

// snapshotUnits groups the graph's exported symbols and routes by file.
func snapshotUnits(g *contracts.RepoContextGraph) []snapUnit {
	byPath := map[string]*snapUnit{}
	unit := func(p string) *snapUnit {
		u := byPath[p]
		if u == nil {
			lang, _ := LanguageForPath(p)
			u = &snapUnit{path: p, dir: path.Dir(p), lang: lang}
			byPath[p] = u
		}
		return u
	}
	for p, syms := range g.Symbols {
		for _, s := range syms {
			if s.Visibility == VisibilityPublic {
				u := unit(p)
				u.syms = append(u.syms, s)
			}
		}
	}
	for _, r := range g.Routes {
		u := unit(r.FilePath)
		u.routes = append(u.routes, r)
	}
	units := make([]snapUnit, 0, len(byPath))
	for _, u := range byPath {
		sort.SliceStable(u.syms, func(i, j int) bool { return u.syms[i].StartLine < u.syms[j].StartLine })
		u.cost = unitCost(*u)
		units = append(units, *u)
	}
	sort.Slice(units, func(i, j int) bool { return units[i].path < units[j].path })
	return units
}

func unitCost(u snapUnit) int {
	n := jsonSize(u.syms) + jsonSize(u.routes) + 2*len(u.path) + 48
	for _, s := range u.syms {
		n += len(s.ID) + 4
	}
	return n
}

// rankUnits scores every file against the keywords and assigns tiers: 0 for
// files that match, 1 for other files in a module with a match, 2 for the rest.
func rankUnits(units []snapUnit, keywords []string) {
	kws := make([]string, 0, len(keywords))
	for _, k := range keywords {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			kws = append(kws, k)
		}
	}
	hitDirs := map[string]bool{}
	for i := range units {
		u := &units[i]
		lower := strings.ToLower(u.path)
		for _, k := range kws {
			hit := strings.Contains(lower, k)
			for _, s := range u.syms {
				if hit {
					break
				}
				hit = strings.Contains(strings.ToLower(s.Name), k) || strings.Contains(strings.ToLower(s.QualifiedName), k)
			}
			if hit {
				u.score++
			}
		}
		if u.score > 0 {
			hitDirs[u.dir] = true
		}
	}
	for i := range units {
		u := &units[i]
		switch {
		case u.score > 0:
			u.tier = 0
		case hitDirs[u.dir]:
			u.tier = 1
		default:
			u.tier = 2
		}
	}
}

// fitPrefix keeps as many leading symbols of a file as fit in room bytes.
func fitPrefix(u snapUnit, room int) (snapUnit, bool) {
	if room <= 0 || len(u.syms) == 0 {
		return snapUnit{}, false
	}
	part := u
	part.routes = nil
	part.syms = nil
	for _, s := range u.syms {
		next := part
		next.syms = append(append([]contracts.Symbol(nil), part.syms...), s)
		next.cost = unitCost(next)
		if next.cost > room {
			break
		}
		part = next
	}
	if len(part.syms) == 0 {
		return snapUnit{}, false
	}
	return part, true
}

// assembleSnapshot builds a snapshot graph from the chosen units.
func assembleSnapshot(shell contracts.RepoContextGraph, units []snapUnit) contracts.RepoContextGraph {
	out := shell
	out.Symbols = map[string][]contracts.Symbol{}
	out.Routes = nil
	mods := map[moduleKey]*contracts.ModuleBoundary{}
	sorted := append([]snapUnit(nil), units...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].path < sorted[j].path })
	for _, u := range sorted {
		if len(u.syms) > 0 {
			out.Symbols[u.path] = u.syms
		}
		out.Routes = append(out.Routes, u.routes...)
		key := moduleKey{dir: u.dir, lang: u.lang}
		m := mods[key]
		if m == nil {
			m = &contracts.ModuleBoundary{ID: moduleID(u.lang, u.dir), Path: u.dir, Language: u.lang}
			mods[key] = m
		}
		for _, s := range u.syms {
			m.SymbolIDs = append(m.SymbolIDs, s.ID)
		}
	}
	out.Modules = make([]contracts.ModuleBoundary, 0, len(mods))
	for _, m := range mods {
		sort.Strings(m.SymbolIDs)
		out.Modules = append(out.Modules, *m)
	}
	sort.Slice(out.Modules, func(i, j int) bool {
		if out.Modules[i].Path != out.Modules[j].Path {
			return out.Modules[i].Path < out.Modules[j].Path
		}
		return out.Modules[i].Language < out.Modules[j].Language
	})
	return out
}

func omittedModules(all, kept []snapUnit) []string {
	have := map[string]bool{}
	for _, u := range kept {
		have[u.dir] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, u := range all {
		if have[u.dir] || seen[u.dir] {
			continue
		}
		seen[u.dir] = true
		out = append(out, u.dir)
	}
	sort.Strings(out)
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

func snapshotStatement(info SnapshotInfo, budget int) string {
	s := fmt.Sprintf("Graph snapshot truncated to fit the %d KiB stage budget: %d of %d exported symbols from %d of %d files are included; files matching the story keywords come first, then the rest of their modules.",
		budget>>10, info.IncludedSymbols, info.TotalSymbols, info.IncludedFiles, info.TotalFiles)
	if len(info.OmittedModules) > 0 {
		s += " Omitted modules: " + strings.Join(info.OmittedModules, ", ") + "."
	}
	return s
}

var keywordStopwords = map[string]bool{
	"that": true, "this": true, "with": true, "from": true, "have": true, "will": true,
	"shall": true, "should": true, "would": true, "could": true, "when": true, "then": true,
	"them": true, "they": true, "their": true, "there": true, "which": true, "what": true,
	"where": true, "into": true, "only": true, "also": true, "each": true, "able": true,
	"want": true, "need": true, "must": true, "been": true, "being": true, "does": true,
	"than": true, "your": true, "such": true, "some": true, "more": true, "most": true,
}

// KeywordsFromText derives snapshot keywords from free text such as the PM
// stories: lower-case words of four or more letters or digits, without common
// stop words, sorted and de-duplicated, at most 500.
func KeywordsFromText(texts ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range texts {
		words := strings.FieldsFunc(strings.ToLower(t), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		for _, w := range words {
			if len(w) < 4 || keywordStopwords[w] || seen[w] {
				continue
			}
			seen[w] = true
			out = append(out, w)
		}
	}
	sort.Strings(out)
	if len(out) > 500 {
		out = out[:500]
	}
	return out
}
