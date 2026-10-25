//go:build client && !server

package ast

import (
	"fmt"
	"sort"
	"strings"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// DriftBudgetBytes is the serialized size the documentation drift list handed
// to the avatars may use. Detection caps items per document (50 MISSING_DOC,
// 20 UNVERIFIABLE) but not across documents, so a repository with hundreds of
// documents produces megabytes. The budget sits beside SnapshotBudgetBytes:
// both are sized by the 180 s call timeout, not by the context window.
const DriftBudgetBytes = 128 << 10

// DriftItemBudget is how many drift items may reach the avatars.
//
// A byte budget alone does not bound the work the list causes. The architect
// writes one documentation task per confirmed item and answers 20 items per
// call, so the item count decides how many provider calls the stage makes.
// Measured against this repository on 2026-10-25: 249 items survived the
// 128 KiB byte budget and became 13 of the architect's 15 calls, 43m46s of a
// 1h04m run. The resulting plan would have carried 249 documentation tasks,
// which is not a plan anyone reads to the end.
//
// 60 is three batches at the architect's batch size. The ranking already puts
// confirmed drift first, so a cap keeps the items a plan can act on and drops
// the long tail of undocumented symbols.
const DriftItemBudget = 60

// driftReserve leaves room for the JSON array brackets and separators.
const driftReserve = 256

// DriftDigestOptions tunes one digest.
type DriftDigestOptions struct {
	// Keywords rank items whose document, code file or symbol names one of
	// them. They are the story keywords, as used for the graph snapshot.
	Keywords []string
	// MaxBytes is the serialized size limit. Zero selects DriftBudgetBytes.
	MaxBytes int
	// MaxItems is the item limit. Zero selects DriftItemBudget. A negative
	// value removes the item bound and leaves only the byte bound.
	MaxItems int
}

// DriftDigestInfo reports what a digest kept.
type DriftDigestInfo struct {
	TotalItems      int
	IncludedItems   int
	Truncated       bool
	BudgetBytes     int
	ItemBudget      int
	SerializedBytes int
	// ItemBound reports that the item budget decided the cut rather than the
	// byte budget, which is what the avatars are told.
	ItemBound bool
	// Statement is the sentence handed to the avatars when items were cut, and
	// empty when the whole list fits.
	Statement string
}

// driftTier orders the drift types by how much a plan can act on them:
// confirmed drift first, then a missing document, then a claim the scan could
// not check.
func driftTier(t string) int {
	switch t {
	case contracts.DriftStaleSignature:
		return 0
	case contracts.DriftDeprecatedFlow:
		return 0
	case contracts.DriftMissingDoc:
		return 1
	default:
		return 2
	}
}

// driftConfidence orders the confidence labels, highest first.
func driftConfidence(c string) int {
	switch strings.ToUpper(c) {
	case "HIGH":
		return 0
	case "MEDIUM":
		return 1
	default:
		return 2
	}
}

// DriftDigest returns the bounded drift list handed to the avatars, most
// useful first: confirmed drift before missing documentation before
// unverifiable claims, keyword matches before the rest of their tier, and the
// detection order within that. A list that fits the budget is returned
// unchanged, in detection order. The cut, when there is one, is stated in the
// returned info; the caller shows it to the model.
func DriftDigest(items []contracts.DocDriftItem, opts DriftDigestOptions) ([]contracts.DocDriftItem, DriftDigestInfo) {
	budget := opts.MaxBytes
	if budget <= 0 {
		budget = DriftBudgetBytes
	}
	maxItems := opts.MaxItems
	switch {
	case maxItems == 0:
		maxItems = DriftItemBudget
	case maxItems < 0:
		maxItems = len(items)
	}
	info := DriftDigestInfo{TotalItems: len(items), BudgetBytes: budget, ItemBudget: maxItems}
	out := make([]contracts.DocDriftItem, 0, len(items))
	out = append(out, items...)
	if size := jsonSize(out); size <= budget && len(out) <= maxItems {
		info.IncludedItems, info.SerializedBytes = len(out), size
		return out, info
	}

	kws := make([]string, 0, len(opts.Keywords))
	for _, k := range opts.Keywords {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			kws = append(kws, k)
		}
	}
	type ranked struct {
		item  contracts.DocDriftItem
		order int
		score int
		cost  int
	}
	all := make([]ranked, 0, len(items))
	for i, it := range items {
		r := ranked{item: it, order: i, cost: jsonSize(it) + 1}
		hay := strings.ToLower(it.DocFilePath + " " + it.CodeFilePath + " " + it.SymbolName)
		for _, k := range kws {
			if strings.Contains(hay, k) {
				r.score++
			}
		}
		all = append(all, r)
	}
	sort.SliceStable(all, func(a, b int) bool {
		x, y := all[a], all[b]
		if ta, tb := driftTier(x.item.DriftType), driftTier(y.item.DriftType); ta != tb {
			return ta < tb
		}
		if x.score != y.score {
			return x.score > y.score
		}
		if ca, cb := driftConfidence(x.item.Confidence), driftConfidence(y.item.Confidence); ca != cb {
			return ca < cb
		}
		return x.order < y.order
	})

	used := driftReserve
	kept := make([]contracts.DocDriftItem, 0, min(len(all), maxItems))
	for _, r := range all {
		if len(kept) >= maxItems {
			info.ItemBound = true
			break
		}
		if used+r.cost > budget {
			continue
		}
		kept = append(kept, r.item)
		used += r.cost
	}
	for jsonSize(kept) > budget && len(kept) > 0 {
		kept = kept[:len(kept)-1]
		info.ItemBound = false
	}
	info.Truncated = true
	info.IncludedItems, info.SerializedBytes = len(kept), jsonSize(kept)
	info.Statement = driftStatement(info)
	return kept, info
}

// driftStatement names the bound that actually cut, so the model is not told
// about a size limit when the list was trimmed for being long.
func driftStatement(info DriftDigestInfo) string {
	bound := fmt.Sprintf("fit the %d KiB stage budget", info.BudgetBytes>>10)
	if info.ItemBound {
		bound = fmt.Sprintf("the %d most useful items", info.ItemBudget)
	}
	return fmt.Sprintf("The documentation drift list was cut to %s: %d of %d items are included; confirmed drift comes first, then items matching the story keywords. Treat an absent document as unchecked, not as correct.",
		bound, info.IncludedItems, info.TotalItems)
}
