//go:build client && !server

package ingestion

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/danielckv/agentic-plan-forge/internal/config"
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// OCRInstallGuidance tells users how to enable scanned-PDF OCR. It is shown
// wherever a scanned page could not be parsed.
const OCRInstallGuidance = "install poppler (pdftoppm) and tesseract: macOS `brew install poppler tesseract`; " +
	"Debian/Ubuntu `sudo apt install poppler-utils tesseract-ocr`; " +
	"Windows `winget install UB-Mannheim.TesseractOCR` plus a Poppler build with its bin folder on PATH"

// ocrLanguagePattern accepts tesseract language codes such as "eng" or
// "eng+deu". The first character is a letter so a value can never be parsed as
// a command-line flag.
var ocrLanguagePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\+[A-Za-z][A-Za-z0-9_]*)*$`)

// maxOCROutputBytes bounds what one OCR child process may write to stdout.
const maxOCROutputBytes = 16 << 20

// OCRProvenance records which local tools and settings recovered scanned
// pages, so reviewers can judge the reliability of OCR-derived text.
type OCRProvenance struct {
	// PdftoppmVersion is the first line of `pdftoppm -v`.
	PdftoppmVersion string `json:"pdftoppm_version"`
	// TesseractVersion is the first line of `tesseract --version`.
	TesseractVersion string `json:"tesseract_version"`
	// DPI is the rasterisation resolution.
	DPI int `json:"dpi"`
	// Language is the tesseract language code used.
	Language string `json:"language"`
	// Pages lists the PDF pages that were recognised.
	Pages []int `json:"pages"`
	// MeanConfidence is the mean per-word confidence (0-100) over all pages.
	MeanConfidence float64 `json:"mean_confidence"`
}

// ocrEngine drives pdftoppm and tesseract. The command runner and the PATH
// lookup are fields so tests can replace the real tools.
type ocrEngine struct {
	lookPath func(file string) (string, error)
	run      func(ctx context.Context, dir, name string, args ...string) (stdout, stderr []byte, err error)
	language string
}

// newOCREngine returns an engine that runs the real tools.
func newOCREngine(language string) *ocrEngine {
	return &ocrEngine{lookPath: exec.LookPath, run: runTool, language: language}
}

// tools resolves both executables. Neither is optional: OCR needs the
// rasteriser and the recogniser.
func (e *ocrEngine) tools() (pdftoppm, tesseract string, ok bool) {
	p, err := e.lookPath("pdftoppm")
	if err != nil {
		return "", "", false
	}
	t, err := e.lookPath("tesseract")
	if err != nil {
		return "", "", false
	}
	return p, t, true
}

// ocrSession holds the per-document state: a private temporary directory
// containing a copy of the PDF, removed when the session closes.
type ocrSession struct {
	e        *ocrEngine
	dir      string
	pdfPath  string
	pdftoppm string
	tess     string
	prov     OCRProvenance
	words    int
	confSum  float64
}

// newSession copies the PDF into a private 0700 directory under the Plan Forge
// cache and records tool versions. The caller must Close the session.
func (e *ocrEngine) newSession(ctx context.Context, r io.ReaderAt, size int64) (*ocrSession, error) {
	if !ocrLanguagePattern.MatchString(e.language) || len(e.language) > 64 {
		return nil, fmt.Errorf("invalid OCR language %q", e.language)
	}
	pdftoppm, tess, ok := e.tools()
	if !ok {
		return nil, fmt.Errorf("pdftoppm and tesseract are required for OCR")
	}
	dir, err := ocrTempDir()
	if err != nil {
		return nil, err
	}
	s := &ocrSession{e: e, dir: dir, pdftoppm: pdftoppm, tess: tess}
	s.pdfPath = filepath.Join(dir, "input.pdf")
	if err := writeSectionToFile(ctx, s.pdfPath, r, size); err != nil {
		s.Close()
		return nil, err
	}
	s.prov = OCRProvenance{
		PdftoppmVersion:  s.version(ctx, pdftoppm, "-v"),
		TesseractVersion: s.version(ctx, tess, "--version"),
		DPI:              OCRDPI,
		Language:         e.language,
	}
	return s, nil
}

// Close removes the session's temporary directory.
func (s *ocrSession) Close() {
	if s.dir != "" {
		os.RemoveAll(s.dir)
		s.dir = ""
	}
}

// provenance returns the aggregate record for the pages recognised so far.
func (s *ocrSession) provenance() OCRProvenance {
	p := s.prov
	if s.words > 0 {
		p.MeanConfidence = s.confSum / float64(s.words)
	}
	return p
}

func (s *ocrSession) version(ctx context.Context, tool string, flag string) string {
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stdout, stderr, _ := s.e.run(vctx, s.dir, tool, flag)
	out := stdout
	if len(bytes.TrimSpace(out)) == 0 {
		out = stderr
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, line)
	if len(line) > 120 {
		line = line[:120]
	}
	return line
}

// ocrParagraph is one recognised paragraph.
type ocrParagraph struct {
	text string
	conf float64
}

// page rasterises one page at 200 dpi and recognises it. The whole call is
// bounded by OCRPageTimeout. Tools are invoked with argument vectors only.
func (s *ocrSession) page(ctx context.Context, n int) ([]ocrParagraph, error) {
	pctx, cancel := context.WithTimeout(ctx, OCRPageTimeout)
	defer cancel()

	prefix := filepath.Join(s.dir, "p")
	num := strconv.Itoa(n)
	if _, stderr, err := s.e.run(pctx, s.dir, s.pdftoppm, "-r", strconv.Itoa(OCRDPI), "-png", "-f", num, "-l", num, s.pdfPath, prefix); err != nil {
		return nil, fmt.Errorf("pdftoppm page %d: %v: %s", n, err, truncate(string(stderr), 200))
	}
	pngs, err := filepath.Glob(filepath.Join(s.dir, "p-*.png"))
	if err != nil || len(pngs) == 0 {
		return nil, fmt.Errorf("pdftoppm page %d produced no image", n)
	}
	sort.Strings(pngs)
	png := pngs[0]
	defer func() {
		for _, p := range pngs {
			os.Remove(p)
		}
	}()

	stdout, stderr, err := s.e.run(pctx, s.dir, s.tess, png, "stdout", "tsv", "-l", s.e.language)
	if err != nil {
		return nil, fmt.Errorf("tesseract page %d: %v: %s", n, err, truncate(string(stderr), 200))
	}
	words, err := parseTSV(stdout)
	if err != nil {
		return nil, fmt.Errorf("tesseract page %d: %v", n, err)
	}
	for _, w := range words {
		s.words++
		s.confSum += w.conf
	}
	s.prov.Pages = append(s.prov.Pages, n)
	return groupParagraphs(words), nil
}

type ocrWord struct {
	block, par, line int
	conf             float64
	text             string
}

// parseTSV reads tesseract's TSV output and returns the recognised words in
// reading order. Rows that are not words (level != 5), have no text or report a
// negative confidence are dropped.
func parseTSV(data []byte) ([]ocrWord, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "level\tpage_num") {
		return nil, fmt.Errorf("unexpected tesseract TSV header")
	}
	var words []ocrWord
	for _, line := range lines[1:] {
		f := strings.Split(line, "\t")
		if len(f) < 12 || f[0] != "5" {
			continue
		}
		text := strings.TrimSpace(strings.Join(f[11:], "\t"))
		if text == "" {
			continue
		}
		conf, err := strconv.ParseFloat(f[10], 64)
		if err != nil || conf < 0 {
			continue
		}
		block, err1 := strconv.Atoi(f[2])
		par, err2 := strconv.Atoi(f[3])
		ln, err3 := strconv.Atoi(f[4])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		words = append(words, ocrWord{block: block, par: par, line: ln, conf: conf, text: text})
	}
	return words, nil
}

// groupParagraphs joins words into paragraphs keyed by tesseract's block and
// paragraph numbers, preserving the order in which they first appear.
func groupParagraphs(words []ocrWord) []ocrParagraph {
	type key struct{ block, par int }
	var order []key
	texts := map[key][]string{}
	sums := map[key]float64{}
	counts := map[key]int{}
	for _, w := range words {
		k := key{w.block, w.par}
		if _, ok := texts[k]; !ok {
			order = append(order, k)
		}
		texts[k] = append(texts[k], w.text)
		sums[k] += w.conf
		counts[k]++
	}
	out := make([]ocrParagraph, 0, len(order))
	for _, k := range order {
		out = append(out, ocrParagraph{text: strings.Join(texts[k], " "), conf: sums[k] / float64(counts[k])})
	}
	return out
}

// ocrConfidenceTier maps a mean OCR confidence to a block confidence tier.
func ocrConfidenceTier(mean float64) string {
	if mean < OCRUnverifiableConfidence {
		return contracts.ConfidenceUnverifiable
	}
	return contracts.ConfidenceLow
}

// ocrTempDir creates a private working directory beneath the Plan Forge cache
// run directory, falling back to the system temporary directory when the cache
// cannot be created.
func ocrTempDir() (string, error) {
	root, err := config.EnsureCache()
	if err == nil {
		run := filepath.Join(root, "run")
		if err = os.MkdirAll(run, 0o700); err == nil {
			return os.MkdirTemp(run, "ocr-*")
		}
	}
	return os.MkdirTemp("", "plan-forge-ocr-*")
}

func writeSectionToFile(ctx context.Context, path string, r io.ReaderAt, size int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, &ctxReader{ctx: ctx, r: io.NewSectionReader(r, 0, size)})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// runTool executes one OCR tool through an argument vector, never a shell,
// with a scrubbed environment and bounded output.
func runTool(ctx context.Context, dir, name string, args ...string) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = ocrEnv(os.Environ())
	cmd.WaitDelay = 2 * time.Second
	var out, errBuf limitedBuffer
	out.limit, errBuf.limit = maxOCROutputBytes, 64<<10
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err = cmd.Run()
	if out.overflow {
		err = fmt.Errorf("output exceeds %d bytes", maxOCROutputBytes)
	}
	return out.buf.Bytes(), errBuf.buf.Bytes(), err
}

// ocrEnv keeps only the variables the OCR tools need to find their data and
// run, so no credentials from the parent environment reach them.
func ocrEnv(parent []string) []string {
	allow := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "LANG": true,
		"TMPDIR": true, "TESSDATA_PREFIX": true, "OMP_THREAD_LIMIT": true, "TZ": true,
	}
	if runtime.GOOS == "windows" {
		for _, k := range []string{"SystemRoot", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "PATHEXT", "ComSpec", "TEMP", "TMP"} {
			allow[strings.ToUpper(k)] = true
		}
	}
	var env []string
	for _, kv := range parent {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if allow[k] || (runtime.GOOS == "windows" && allow[strings.ToUpper(k)]) || strings.HasPrefix(k, "LC_") {
			env = append(env, kv)
		}
	}
	return env
}

// limitedBuffer collects output up to a limit and records when it overflowed.
type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); len(p) > room {
		b.overflow = true
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
