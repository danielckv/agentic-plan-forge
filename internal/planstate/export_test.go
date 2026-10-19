//go:build client && !server

package planstate

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// InstrumentScan counts the AST scans a plan check takes and runs after, when
// non-nil, once each scan has returned. restore puts the real scan back.
func InstrumentScan(after func()) (scans func() int, restore func()) {
	var n atomic.Int64
	prev := scanGraph
	scanGraph = func(ctx context.Context, real string, timeout time.Duration) (*contracts.RepoContextGraph, error) {
		g, err := prev(ctx, real, timeout)
		n.Add(1)
		if after != nil {
			after()
		}
		return g, err
	}
	return func() int { return int(n.Load()) }, func() { scanGraph = prev }
}
