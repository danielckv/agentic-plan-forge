//go:build client && !server

package planstate

import (
	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/storage"
)

// checkEvidence is what one checkStateWith run read: the artifacts, the AST
// snapshot and the receipts its verdict rests on. TaskBoard derives the
// per-task view from it, so the plan status and every task column come from the
// same graph and the repository is scanned once. g stays nil when the manifest
// was not trusted enough to scan (no manifest, not approved, invalid paths or a
// hash mismatch).
type checkEvidence struct {
	art      artifactSet
	g        *contracts.RepoContextGraph
	receipts []storage.Receipt
}

// scanGraph takes the AST snapshot of a plan check. It is a variable only so a
// test can count the scans one evaluation performs.
var scanGraph = scanBounded
