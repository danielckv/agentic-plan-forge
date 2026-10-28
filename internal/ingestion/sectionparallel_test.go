//go:build client && !server

package ingestion

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// barrierGen holds every call open until want of them are in flight. A
// sequential normalizer never reaches the barrier and the test fails on its
// own deadline rather than passing because the calls happened to overlap.
type barrierGen struct {
	want int
	body string

	mu       sync.Mutex
	inFlight int
	peak     int

	once    sync.Once
	reached chan struct{}
}

func newBarrierGen(want int, body string) *barrierGen {
	return &barrierGen{want: want, body: body, reached: make(chan struct{})}
}

func (g *barrierGen) GenerateStream(ctx context.Context, _ contracts.GenerateRequest, _ func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	g.mu.Lock()
	g.inFlight++
	if g.inFlight > g.peak {
		g.peak = g.inFlight
	}
	atBarrier := g.inFlight >= g.want
	g.mu.Unlock()

	if atBarrier {
		g.once.Do(func() { close(g.reached) })
	}
	select {
	case <-g.reached:
	case <-ctx.Done():
		g.release()
		return contracts.GenerationResult{}, ctx.Err()
	}
	g.release()
	return contracts.GenerationResult{Content: json.RawMessage(g.body)}, nil
}

func (g *barrierGen) release() {
	g.mu.Lock()
	g.inFlight--
	g.mu.Unlock()
}

func (g *barrierGen) peakInFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.peak
}

// sectionedDocument returns a document that splits into exactly parts
// sections: each part is 60% of a section, so no two of them fit together.
func sectionedDocument(parts int) string {
	var sb strings.Builder
	for i := 0; i < parts; i++ {
		sb.WriteString("# Part ")
		sb.WriteString(string(rune('A' + i)))
		sb.WriteString("\n\n")
		sb.WriteString(strings.Repeat("word ", MaxSectionChars*6/10/5))
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// A long document is the slowest part of an interpretation and its sections
// are independent, so they must be classified together rather than one after
// another.
//
// The barrier wants a fixed two calls, not maxSectionParallel: an expectation
// that tracked the constant would pass against a sequential normalizer the
// moment the constant was set to 1.
func TestNormalizeClassifiesSectionsConcurrently(t *testing.T) {
	if maxSectionParallel < 2 {
		t.Fatalf("maxSectionParallel = %d: sections no longer run together", maxSectionParallel)
	}
	text := sectionedDocument(4)
	gen := newBarrierGen(2, fullResponse)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := Normalize(ctx, gen, text, nil)
	if err != nil {
		t.Fatalf("Normalize: %v (the sections did not run together)", err)
	}
	if got := gen.peakInFlight(); got < 2 {
		t.Errorf("peak calls in flight = %d, want at least 2", got)
	}
	if out.Product.Title == "" {
		t.Error("the concurrent sections produced no product view")
	}
}

// The bound holds: a document with more sections than the limit never opens
// more than maxSectionParallel calls at once.
func TestNormalizeNeverExceedsTheParallelBound(t *testing.T) {
	const sections = maxSectionParallel * 2
	text := sectionedDocument(sections)
	gen := newBarrierGen(maxSectionParallel, fullResponse)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := Normalize(ctx, gen, text, nil); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got := gen.peakInFlight(); got > maxSectionParallel {
		t.Errorf("peak calls in flight = %d, over the %d bound", got, maxSectionParallel)
	}
}
