//go:build client && !server

package gather

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// The files the recall index covers, read directly when no index answers.
var (
	sourceFiles = []string{".agents/STATE.md"}
	sourceDirs  = []string{".agents/lanes", ".agents/sessions", "docs/specs", "docs/plans", "docs/adrs", "specs"}
)

const (
	// maxFileBytes skips larger files, as the index does.
	maxFileBytes = 2_000_000
	// maxDepth bounds directory recursion below each source directory.
	maxDepth = 4
	// maxFiles bounds how many files one Collect reads.
	maxFiles = 400
)

var paragraphBreak = regexp.MustCompile(`\n\s*\n`)

// scanFiles reads the source files and scores each excerpt against terms.
// found is false when none of the source files exists.
func scanFiles(ctx context.Context, repo string, terms []string, limit int) (hits []contracts.ContextHit, found bool) {
	paths := sourcePaths(repo)
	if len(paths) == 0 {
		return []contracts.ContextHit{}, false
	}
	for _, rel := range paths {
		if ctx.Err() != nil {
			break
		}
		raw, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
		if err != nil || !utf8.Valid(raw) {
			continue
		}
		for _, c := range chunkMarkdown(string(raw)) {
			score := scoreText(c.text, terms)
			if score == 0 {
				continue
			}
			heading := c.heading
			if h := headingOf(c.text); h != "" {
				heading = h
			}
			hits = append(hits, contracts.ContextHit{Path: rel, Heading: heading, Text: clip(c.text), Score: score})
		}
	}
	if hits == nil {
		hits = []contracts.ContextHit{}
	}
	return rankHits(hits, limit), true
}

// sourcePaths lists the readable source files, repository-relative, sorted.
// Symbolic links are skipped so nothing outside the repository is read.
func sourcePaths(repo string) []string {
	var out []string
	add := func(rel string, info fs.FileInfo) {
		if len(out) >= maxFiles || !info.Mode().IsRegular() || info.Size() > maxFileBytes {
			return
		}
		out = append(out, rel)
	}
	for _, rel := range sourceFiles {
		if info, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(rel))); err == nil {
			add(rel, info)
		}
	}
	for _, dir := range sourceDirs {
		root := filepath.Join(repo, filepath.FromSlash(dir))
		if info, err := os.Lstat(root); err != nil || !info.IsDir() {
			continue
		}
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || len(out) >= maxFiles {
				return filepath.SkipDir
			}
			name := d.Name()
			if d.IsDir() {
				if p == root {
					return nil
				}
				rel, _ := filepath.Rel(root, p)
				if strings.HasPrefix(name, ".") || strings.Count(rel, string(filepath.Separator)) >= maxDepth {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(name, ".") || d.Type()&fs.ModeSymlink != 0 || !textSource(name) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rel, err := filepath.Rel(repo, p)
			if err != nil {
				return nil
			}
			add(filepath.ToSlash(rel), info)
			return nil
		})
	}
	return out
}

func textSource(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown", ".txt":
		return true
	}
	return false
}

type chunk struct {
	heading string
	text    string
}

// chunkMarkdown packs paragraphs into excerpts of at most chunkChars, the way
// the recall index does, and remembers the heading each excerpt sits under.
func chunkMarkdown(text string) []chunk {
	var out []chunk
	var buf strings.Builder
	heading, bufHeading := "", ""
	flush := func() {
		if s := strings.TrimSpace(buf.String()); s != "" {
			out = append(out, chunk{heading: bufHeading, text: s})
		}
		buf.Reset()
	}
	for _, para := range paragraphBreak.Split(strings.ReplaceAll(text, "\r\n", "\n"), -1) {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if strings.HasPrefix(para, "#") {
			heading = strings.TrimSpace(strings.TrimLeft(strings.SplitN(para, "\n", 2)[0], "#"))
		}
		for len(para) > chunkChars {
			flush()
			cut := chunkChars
			for cut > 0 && !utf8.RuneStart(para[cut]) {
				cut--
			}
			out = append(out, chunk{heading: heading, text: para[:cut]})
			para = para[cut:]
		}
		if buf.Len() > 0 && buf.Len()+len(para)+2 > chunkChars {
			flush()
		}
		if buf.Len() == 0 {
			bufHeading = heading
		} else {
			buf.WriteString("\n\n")
		}
		buf.WriteString(para)
	}
	flush()
	return out
}

// scoreText rewards an excerpt for each distinct term it holds, then for
// repeats, so breadth outranks one word said many times.
func scoreText(text string, terms []string) float64 {
	lower := strings.ToLower(text)
	distinct, total := 0, 0
	for _, t := range terms {
		if n := strings.Count(lower, t); n > 0 {
			distinct++
			total += min(n, 5)
		}
	}
	if distinct == 0 {
		return 0
	}
	return float64(distinct*10 + total)
}
