package synthesizer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// A bundle is the plan, the specification and the decision records as real
// repository files, so an approved plan can land in the project's own docs
// instead of only in the plan directory. It is built without a filesystem
// effect: the caller decides where and when to write.

// DocumentKind names what a bundle entry is.
type DocumentKind string

// Bundle kinds.
const (
	KindPlan DocumentKind = "plan"
	KindSpec DocumentKind = "spec"
	KindADR  DocumentKind = "adr"
)

// DocumentKindOrder is the order entries are emitted in: a reader opens the plan
// first, then the specification, then the decisions behind them.
var DocumentKindOrder = []DocumentKind{KindPlan, KindSpec, KindADR}

// BundleEntry is one generated document.
type BundleEntry struct {
	Path   string       `json:"path"`
	Kind   DocumentKind `json:"kind"`
	Title  string       `json:"title"`
	Body   []byte       `json:"-"`
	SHA256 string       `json:"sha256"`
}

// Bundle is the generated set.
type Bundle struct {
	Entries []BundleEntry `json:"entries"`
}

// BundleOptions shapes path selection without touching disk.
type BundleOptions struct {
	// ExistingDirs names, per kind, the directories this project already uses,
	// most preferred first.
	ExistingDirs map[DocumentKind][]string
	// ExistingFiles maps a repository-relative path to its current hash, so a
	// collision is detected from data the caller already has.
	ExistingFiles map[string]string
	// NextADRNumber is the next free decision-record number.
	NextADRNumber int
}

// BundleError reports a bundle that cannot be built.
type BundleError struct {
	Reason string
}

func (e *BundleError) Error() string { return "bundle: " + e.Reason }

// BundleCollision reports a destination that already exists. A generated file
// never overwrites a document a person wrote, and a decision record is never
// renumbered.
type BundleCollision struct {
	Path string
}

func (e *BundleCollision) Error() string {
	return "bundle: " + e.Path + " already exists"
}

// defaultDirs are the fallbacks when the project has no convention of its own.
var defaultDirs = map[DocumentKind]string{
	KindPlan: "docs/plans",
	KindSpec: "docs/specs",
	KindADR:  "docs/adrs",
}

// BuildBundle renders the documents for one approved plan. It is deterministic:
// the same input and options yield byte-identical bodies and the same paths.
func BuildBundle(input contracts.SynthesisInput, meta contracts.ApprovalMeta, opts BundleOptions) (Bundle, error) {
	plan, err := Compile(input, meta)
	if err != nil {
		return Bundle{}, err
	}
	docs, err := DecodeInput(input)
	if err != nil {
		return Bundle{}, err
	}
	title := plan.Title
	if strings.TrimSpace(title) == "" {
		title = "Approved plan"
	}

	var entries []BundleEntry
	planPath, err := bundlePath(KindPlan, title, opts)
	if err != nil {
		return Bundle{}, err
	}
	planBody := []byte(renderPlanFile(title, plan, docs))
	if err := checkBody(planBody); err != nil {
		return Bundle{}, err
	}
	entries = append(entries, newEntry(planPath, KindPlan, title, planBody))

	specPath, err := bundlePath(KindSpec, title, opts)
	if err != nil {
		return Bundle{}, err
	}
	if err := checkBody(plan.SpecMarkdown); err != nil {
		return Bundle{}, err
	}
	entries = append(entries, newEntry(specPath, KindSpec, title, plan.SpecMarkdown))

	// One record per decision that was actually made. A plan with none gets no
	// record: inventing an architecture decision to have a file is worse than
	// having no file.
	number := opts.NextADRNumber
	if number <= 0 {
		number = 1
	}
	for _, decision := range sortedDecisions(input) {
		// A record exists only for a decision that was actually made. An open
		// question is not a decision, and inventing an architecture record to have
		// a file would be worse than having none.
		if strings.TrimSpace(decision.ResolvedOptionID) == "" {
			continue
		}
		adrTitle := strings.TrimSpace(decision.Question)
		if adrTitle == "" {
			adrTitle = "Decision " + decision.ID
		}
		adrPath, perr := adrBundlePath(number, adrTitle, opts)
		if perr != nil {
			return Bundle{}, perr
		}
		body := []byte(renderADR(decision))
		if err := checkBody(body); err != nil {
			return Bundle{}, err
		}
		entries = append(entries, newEntry(adrPath, KindADR, adrTitle, body))
		number++
	}

	if err := checkDuplicates(entries); err != nil {
		return Bundle{}, err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return kindRank(entries[i].Kind) < kindRank(entries[j].Kind)
		}
		return entries[i].Path < entries[j].Path
	})
	return Bundle{Entries: entries}, nil
}

func kindRank(kind DocumentKind) int {
	for i, k := range DocumentKindOrder {
		if k == kind {
			return i
		}
	}
	return len(DocumentKindOrder)
}

// bundlePath picks one destination and refuses an existing file.
func bundlePath(kind DocumentKind, title string, opts BundleOptions) (string, error) {
	dir := dirFor(kind, opts)
	rel := joinSlash(dir, slug(title)+".md")
	return refuseCollision(rel, opts)
}

// adrBundlePath numbers a decision record. A number already used by a file the
// caller knows about is skipped rather than overwritten, so history is never
// rewritten.
func adrBundlePath(number int, title string, opts BundleOptions) (string, error) {
	dir := dirFor(KindADR, opts)
	for range 1000 {
		rel := joinSlash(dir, fmt.Sprintf("%04d-%s.md", number, slug(title)))
		if _, taken := opts.ExistingFiles[rel]; taken {
			number++
			continue
		}
		return refuseCollision(rel, opts)
	}
	return "", &BundleError{Reason: "no free decision-record number"}
}

func refuseCollision(rel string, opts BundleOptions) (string, error) {
	if _, ok := opts.ExistingFiles[rel]; ok {
		return "", &BundleCollision{Path: rel}
	}
	return rel, nil
}

// dirFor returns the project's own directory for a kind, else the documented
// default.
func dirFor(kind DocumentKind, opts BundleOptions) string {
	for _, dir := range opts.ExistingDirs[kind] {
		if clean, ok := safeRelPath(dir); ok {
			return clean
		}
	}
	return defaultDirs[kind]
}

// safeRelPath keeps a caller-supplied directory inside the repository.
func safeRelPath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" || strings.Contains(p, "\\") || strings.HasPrefix(p, "/") {
		return "", false
	}
	clean := strings.Trim(strings.TrimPrefix(p, "./"), "/")
	if clean == "" || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

func joinSlash(dir, name string) string {
	if dir == "" {
		return name
	}
	return strings.TrimSuffix(dir, "/") + "/" + name
}

func newEntry(path string, kind DocumentKind, title string, body []byte) BundleEntry {
	sum := sha256.Sum256(body)
	return BundleEntry{Path: path, Kind: kind, Title: title, Body: body, SHA256: hex.EncodeToString(sum[:])}
}

func checkBody(body []byte) error {
	if len(body) == 0 {
		return &BundleError{Reason: "a document has no body"}
	}
	return nil
}

func checkDuplicates(entries []BundleEntry) error {
	seen := map[string]bool{}
	for _, entry := range entries {
		if seen[entry.Path] {
			return &BundleError{Reason: "two documents claim " + entry.Path}
		}
		seen[entry.Path] = true
	}
	return nil
}

// sortedDecisions orders the input's decisions by id so two runs agree.
func sortedDecisions(input contracts.SynthesisInput) []contracts.Decision {
	out := append([]contracts.Decision(nil), input.Decisions...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// slug makes a filename from a title: lower case, single dashes, bounded.
func slug(title string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case !lastDash && b.Len() > 0:
			b.WriteByte('-')
			lastDash = true
		}
		if b.Len() >= 60 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "approved-plan"
	}
	return out
}

// renderPlanFile is the plan as a repository document: the tasks, in the order
// the manifest carries them, with their criteria and their checks.
func renderPlanFile(title string, plan CompiledPlan, docs Docs) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	b.WriteString("Approved plan. Generated from an accepted deliberation; the specification is beside it.\n\n")
	tasks := AllTasks(docs.Architect, docs.Specialists)
	fmt.Fprintf(&b, "## Tasks (%d)\n\n", len(tasks))
	for _, task := range tasks {
		fmt.Fprintf(&b, "### %s\n\n", strings.TrimSpace(task.ID))
		if task.Title != "" {
			fmt.Fprintf(&b, "%s\n\n", task.Title)
		}
		if len(task.AcceptanceCriteria) > 0 {
			b.WriteString("Acceptance criteria:\n\n")
			for _, criterion := range task.AcceptanceCriteria {
				fmt.Fprintf(&b, "- [ ] %s\n", criterion)
			}
			b.WriteString("\n")
		}
		if len(task.Verification) > 0 {
			b.WriteString("Checks:\n\n")
			for _, command := range task.Verification {
				fmt.Fprintf(&b, "- `%s`\n", strings.Join(command.Argv, " "))
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// renderADR is one decision record: what was asked, what was chosen, and why.
func renderADR(d contracts.Decision) string {
	var b strings.Builder
	title := strings.TrimSpace(d.Question)
	if title == "" {
		title = "Decision " + d.ID
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	b.WriteString("- Status: accepted\n")
	fmt.Fprintf(&b, "- Decision: %s\n\n", d.ID)
	b.WriteString("## Context\n\n")
	b.WriteString(strings.TrimSpace(d.Question) + "\n\n")
	b.WriteString("## Decision\n\n")
	b.WriteString(chosenLabel(d) + "\n\n")
	b.WriteString("## Options considered\n\n")
	for _, option := range d.Options {
		mark := " "
		if option.ID == d.ResolvedOptionID {
			mark = "x"
		}
		fmt.Fprintf(&b, "- [%s] %s", mark, strings.TrimSpace(option.Label))
		if strings.TrimSpace(option.Tradeoff) != "" {
			fmt.Fprintf(&b, ": %s", strings.TrimSpace(option.Tradeoff))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n## Consequences\n\n")
	chosen := chosenLabel(d)
	if chosen == "" {
		b.WriteString("Recorded in the approved plan.\n")
	} else {
		fmt.Fprintf(&b, "The plan follows %s.\n", chosen)
	}
	return b.String()
}

// chosenLabel is the selected option's label, or its id when it carries none.
func chosenLabel(d contracts.Decision) string {
	for _, option := range d.Options {
		if option.ID != d.ResolvedOptionID {
			continue
		}
		if label := strings.TrimSpace(option.Label); label != "" {
			return label
		}
		return strings.TrimSpace(d.ResolvedOptionID)
	}
	return strings.TrimSpace(d.ResolvedOptionID)
}
