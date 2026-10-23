//go:build client && !server

package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// timeoutGen is the host killing a call that outran contracts.DefaultGenerateTimeout:
// any section longer than limit fails with context.DeadlineExceeded.
type timeoutGen struct {
	limit int
	sizes []int
	body  string
}

func (g *timeoutGen) GenerateStream(_ context.Context, req contracts.GenerateRequest, _ func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	var p normalizePayload
	if err := json.Unmarshal(req.UserPayload, &p); err != nil {
		return contracts.GenerationResult{}, err
	}
	n := utf8.RuneCountInString(p.DocumentMarkdown)
	g.sizes = append(g.sizes, n)
	if n > g.limit {
		return contracts.GenerationResult{}, context.DeadlineExceeded
	}
	return contracts.GenerationResult{Content: json.RawMessage(g.body)}, nil
}

func TestNormalizeSplitsAgainWhenASectionOutrunsTheCallTimeout(t *testing.T) {
	text := strings.Repeat("word ", MaxSectionChars/5)
	// The host accepts only a third of a section, so the first call times out
	// and the retry must divide the section rather than fail the document.
	gen := &timeoutGen{limit: MaxSectionChars / 3, body: fullResponse}
	out, err := Normalize(context.Background(), gen, text, nil)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(gen.sizes) < 2 {
		t.Fatalf("model called %d times, want a timeout and at least one retry", len(gen.sizes))
	}
	if gen.sizes[0] <= gen.limit {
		t.Fatalf("first call carried %d characters, want one over the %d limit", gen.sizes[0], gen.limit)
	}
	accepted := 0
	for _, n := range gen.sizes {
		if n <= gen.limit {
			accepted += n
		}
	}
	if accepted != utf8.RuneCountInString(text) {
		t.Errorf("the accepted sections carry %d of %d characters", accepted, utf8.RuneCountInString(text))
	}
	if out.Product.Title == "" {
		t.Error("the retried document produced no product view")
	}
}

func TestNormalizeStopsSplittingAtMinSectionChars(t *testing.T) {
	// No section is ever small enough, so the split must bottom out and report
	// the timeout instead of dividing for ever.
	gen := &timeoutGen{limit: 0, body: fullResponse}
	_, err := Normalize(context.Background(), gen, strings.Repeat("word ", MaxSectionChars/5), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	for _, n := range gen.sizes {
		if n < MinSectionChars {
			t.Fatalf("a call carried %d characters, under the %d floor", n, MinSectionChars)
		}
	}
}

func TestNormalizeReportsCallerCancellationWithoutSplitting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gen := &timeoutGen{limit: 0, body: fullResponse}
	_, err := Normalize(ctx, gen, strings.Repeat("word ", MaxSectionChars/5), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(gen.sizes) != 0 {
		t.Fatalf("model called %d times after the caller cancelled", len(gen.sizes))
	}
}
