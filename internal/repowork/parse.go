package repowork

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Document kinds. Kind is classification by location and name only; it never
// implies completion or approval.
const (
	kindState    = "state"
	kindPlan     = "plan"
	kindSpec     = "spec"
	kindADR      = "adr"
	kindFollowUp = "follow-up"
	kindEpic     = "epic"
	kindTask     = "task"
	kindDocument = "document"
)

// Item statuses. Only explicit checkbox state or the state file's ## Next
// section yields open/done; anything else is unknown. Completion is never
// inferred from prose or history.
const (
	statusOpen    = "open"
	statusDone    = "done"
	statusUnknown = "unknown"
)

var (
	headingRe   = regexp.MustCompile(`^#{1,6} +(\S.*)$`)
	checkboxRe  = regexp.MustCompile(`^[-*] +\[([ xX])\] +(\S.*)$`)
	bulletRe    = regexp.MustCompile(`^[-*] +(\S.*)$`)
	dependsRe   = regexp.MustCompile(`(?i)\b(?:depends on|blocked by):\s*(.+)$`)
	identRe     = regexp.MustCompile("`([^`]+)`|[A-Za-z0-9][A-Za-z0-9._/-]*")
	continuesRe = regexp.MustCompile(`^\s{2,}\S`)
)

// textExt reports whether a path names a text document the scanner indexes.
func textExt(rel string) bool {
	rel = strings.ToLower(rel)
	switch {
	case strings.HasSuffix(rel, ".md"), strings.HasSuffix(rel, ".markdown"), strings.HasSuffix(rel, ".txt"):
		return true
	default:
		return false
	}
}

// rootFileMatch reports whether a root-level file name identifies plans,
// epics, tasks, follow-ups or state, per the plan's discovery rule.
func rootFileMatch(name string) bool {
	lower := strings.ToLower(name)
	if !textExt(name) {
		return false
	}
	for _, p := range rootPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// docID derives the opaque document ID from its path *and* its content: the
// first 8 hex characters of the path digest, then the first 8 of the content
// digest. Binding the bytes into the identity is what lets Open report a
// document that changed after the inventory was taken — with the path alone the
// ID would still match and the stale content would be served as current.
func docID(rel, contentSHA string) string {
	return pathPrefix(rel) + contentSHA[:idHexLen/2]
}

// pathPrefix is the part of an ID that identifies the document's path.
func pathPrefix(rel string) string {
	sum := sha256.Sum256([]byte(rel))
	return hex.EncodeToString(sum[:])[:idHexLen/2]
}

// buildDocument parses one document's raw bytes into its index entry. The
// second return carries the work items for the state file's ## Next section
// only; for any other path it is nil. Parsing is deterministic: heading and
// item order follow source order, and no clock, environment or randomness
// takes part.
func buildDocument(rel string, raw []byte) (Document, []Item) {
	sum := hashBytes(raw)
	doc := Document{
		ID:       docID(rel, sum),
		Path:     rel,
		Kind:     classify(rel),
		SHA256:   sum,
		Bytes:    len(raw),
		Headings: []Heading{},
		Items:    []Item{},
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	doc.Headings = parseHeadings(lines)
	doc.Items = parseItems(doc.ID, lines)
	var next []Item
	if rel == statePath {
		next = nextItems(doc.Items)
	}
	return doc, next
}

// classify maps a repository-relative path to its kind by location and file
// name. Root files matching the plan/epic/task/follow-up/state prefixes take
// their specific kind; everything else is a plain document.
func classify(rel string) string {
	parts := strings.Split(rel, "/")
	base := strings.ToLower(parts[len(parts)-1])
	if rel == statePath {
		return kindState
	}
	if len(parts) == 1 {
		switch {
		case strings.HasPrefix(base, "plan"):
			return kindPlan
		case strings.HasPrefix(base, "follow"):
			return kindFollowUp
		case strings.HasPrefix(base, "epic"):
			return kindEpic
		case strings.HasPrefix(base, "task"):
			return kindTask
		case strings.HasPrefix(base, "state"):
			return kindState
		}
		return kindDocument
	}
	// A file name that names its kind wins over the directory it sits in:
	// docs/plans/epic-launch.md is an epic, not a plan, and a reader filtering
	// by kind would otherwise never find it.
	switch {
	case strings.HasPrefix(base, "epic"):
		return kindEpic
	case strings.HasPrefix(base, "follow"):
		return kindFollowUp
	case strings.HasPrefix(base, "task"):
		return kindTask
	}
	dir := strings.ToLower(parts[len(parts)-2])
	switch dir {
	case "plans":
		return kindPlan
	case "specs":
		return kindSpec
	case "adrs":
		return kindADR
	case "lanes", "sessions":
		return kindDocument
	}
	if strings.HasPrefix(base, "plan") {
		return kindPlan
	}
	return kindDocument
}

// parseHeadings extracts ATX headings (1-6 leading #) with 1-based lines.
func parseHeadings(lines []string) []Heading {
	out := []Heading{}
	for i, line := range lines {
		if m := headingRe.FindStringSubmatch(line); m != nil {
			out = append(out, Heading{
				Level: strings.Count(line[:len(line)-len(m[1])], "#"),
				Text:  strings.TrimSpace(m[1]),
				Line:  i + 1,
			})
		}
	}
	return out
}

// parseItems extracts work items with explicit status: task checkboxes
// anywhere in the document, and top-level bullets under a "## Next" heading
// (the state file's active order). Other bullets carry no explicit status,
// so none is emitted. Nested continuation lines extend the owning item's
// EndLine and source text but never become separate items.
func parseItems(docID string, lines []string) []Item {
	out := []Item{}
	inNext := false
	for i, line := range lines {
		if m := headingRe.FindStringSubmatch(line); m != nil {
			level := strings.Count(line[:len(line)-len(m[1])], "#")
			inNext = level == 2 && strings.TrimSpace(m[1]) == "Next"
			continue
		}
		if m := checkboxRe.FindStringSubmatch(line); m != nil {
			text := strings.TrimSpace(m[2])
			item := Item{
				ID:     itemID(docID, i+1),
				Text:   text,
				Status: statusOpen,
				Line:   i + 1,
			}
			if m[1] == "x" || m[1] == "X" {
				item.Status = statusDone
			}
			end, source := blockEnd(lines, i)
			item.EndLine = end
			item.Source = source
			item.DependsOn = extractDeps(text)
			out = append(out, item)
			continue
		}
		if !inNext {
			continue
		}
		if m := bulletRe.FindStringSubmatch(line); m != nil {
			text := strings.TrimSpace(m[1])
			end, source := blockEnd(lines, i)
			out = append(out, Item{
				ID:        itemID(docID, i+1),
				Text:      text,
				Status:    statusOpen,
				Line:      i + 1,
				EndLine:   end,
				Source:    source,
				DependsOn: extractDeps(text),
			})
		}
	}
	return out
}

// nextItems selects the state file's active next order: top-level ## Next
// bullets in source order. They are already in parseItems output with status
// open; this returns them unchanged for Inventory.Next.
func nextItems(items []Item) []Item {
	return items
}

// blockEnd returns the 1-based last line and raw text of the list item block
// starting at line idx: the item line plus its indented continuation lines.
func blockEnd(lines []string, idx int) (int, string) {
	last := idx
	for j := idx + 1; j < len(lines); j++ {
		if lines[j] == "" || !continuesRe.MatchString(lines[j]) {
			break
		}
		last = j
	}
	return last + 1, strings.Join(lines[idx:last+1], "\n")
}

// extractDeps returns the identifiers a "Depends on:" or "Blocked by:"
// clause names. Dependencies are captured only when explicitly stated; the
// clause must sit in the item's own text.
func extractDeps(text string) []string {
	m := dependsRe.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	seen := map[string]bool{}
	deps := []string{}
	for _, id := range identRe.FindAllStringSubmatch(m[1], -1) {
		raw := id[1]
		if raw == "" {
			raw = id[0]
		}
		raw = strings.Trim(raw, "`")
		if raw == "" || isStopWord(raw) {
			continue
		}
		if !seen[raw] {
			seen[raw] = true
			deps = append(deps, raw)
		}
	}
	if len(deps) == 0 {
		return nil
	}
	return deps
}

// isStopWord filters clause filler words that look like identifiers.
func isStopWord(s string) bool {
	if len(s) > 16 {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	switch strings.ToLower(s) {
	case "and", "or", "the", "a", "an", "on", "for", "this", "that", "which":
		return true
	default:
		return false
	}
}

// itemID builds a deterministic item ID from the document ID and line.
func itemID(docID string, line int) string {
	return docID + ":" + strconv.Itoa(line)
}

// hashBytes returns the lowercase hex SHA-256 of raw bytes.
func hashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
