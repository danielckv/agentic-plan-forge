//go:build client && !server

package host

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// meterGen answers with a fixed receipt and usage, so the meter can be read
// without a provider.
type meterGen struct {
	host string
	in   int
	out  int
	err  error
}

func (g meterGen) GenerateStream(context.Context, contracts.GenerateRequest, func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	return contracts.GenerationResult{
		Receipt: contracts.ToolFreeReceipt{Host: g.host},
		Usage:   contracts.Usage{InputTokens: g.in, OutputTokens: g.out},
	}, g.err
}

// Every provider call is counted against the host that served it, the model it
// asked for and the role that asked, so the window can show where the tokens
// went.
func TestMeterCountsEveryCallPerHostModelAndRole(t *testing.T) {
	m := NewMeter(meterGen{host: "claude", in: 1200, out: 300}, "opus", "sonnet")
	ticks := 0
	m.clock = func() time.Time {
		ticks++
		return time.Unix(0, 0).Add(time.Duration(ticks) * 250 * time.Millisecond)
	}

	ctx := context.Background()
	for range 2 {
		if _, err := m.GenerateStream(ctx, contracts.GenerateRequest{
			Role: contracts.RoleArchitect, ModelSlot: contracts.SlotMain,
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.GenerateStream(ctx, contracts.GenerateRequest{
		Role: contracts.RoleCritic, ModelSlot: contracts.SlotJudge, Model: "haiku",
	}, nil); err != nil {
		t.Fatal(err)
	}

	report := m.Report()
	if len(report.Rows) != 2 {
		t.Fatalf("rows = %d, want one per role", len(report.Rows))
	}
	arch := report.Rows[0]
	if arch.Host != "claude" || arch.Model != "opus" || arch.Role != string(contracts.RoleArchitect) {
		t.Errorf("busiest row = %+v, want the architect on claude/opus", arch)
	}
	if arch.Requests != 2 || arch.InputTokens != 2400 || arch.OutputTokens != 600 {
		t.Errorf("architect row = %+v, want 2 requests and the summed tokens", arch)
	}
	if arch.Milliseconds != 500 {
		t.Errorf("architect milliseconds = %d, want 500", arch.Milliseconds)
	}
	critic := report.Rows[1]
	if critic.Model != "haiku" {
		t.Errorf("critic model = %q, want the model the request named", critic.Model)
	}
	if report.Totals.Requests != 3 || report.Totals.InputTokens != 3600 {
		t.Errorf("totals = %+v, want 3 requests and 3600 input tokens", report.Totals)
	}
}

// A failed call still cost something and still names a host: it is counted, and
// counted as a failure.
func TestMeterCountsAFailedCall(t *testing.T) {
	m := NewMeter(meterGen{host: "omp", err: errors.New("the provider refused")}, "opus", "")
	if _, err := m.GenerateStream(context.Background(), contracts.GenerateRequest{
		Role: contracts.RoleProductManager, ModelSlot: contracts.SlotMain,
	}, nil); err == nil {
		t.Fatal("the stub generator should have failed")
	}
	report := m.Report()
	if len(report.Rows) != 1 || report.Rows[0].Host != "omp" {
		t.Fatalf("rows = %+v, want one omp row", report.Rows)
	}
	if report.Rows[0].Requests != 1 || report.Rows[0].Failures != 1 {
		t.Errorf("row = %+v, want one request and one failure", report.Rows[0])
	}
}

// A call no host served is still visible: a row that says so beats a silent
// gap in the accounting.
func TestMeterNamesACallNoHostServed(t *testing.T) {
	m := NewMeter(meterGen{err: errors.New("no host")}, "", "")
	_, _ = m.GenerateStream(context.Background(), contracts.GenerateRequest{ModelSlot: contracts.SlotMain}, nil)
	report := m.Report()
	if len(report.Rows) != 1 {
		t.Fatalf("rows = %+v, want one", report.Rows)
	}
	if report.Rows[0].Host != "unresolved" || report.Rows[0].Model != "host default" || report.Rows[0].Role != "unknown" {
		t.Errorf("row = %+v, want every label filled", report.Rows[0])
	}
}
