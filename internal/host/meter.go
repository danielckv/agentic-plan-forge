//go:build client && !server

package host

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

// Meter wraps a generator and keeps the session's provider accounting: how many
// calls each host, model and role served, how many failed, how long they took
// and how many tokens the host reported. Every stage goes through one
// generator, so one wrapper sees the whole session.
//
// The host is read from the receipt the adapter returns, which it fills before
// the call runs: a failed call is still attributed to the host that failed it.
type Meter struct {
	inner    contracts.Generator
	defaults map[contracts.ModelSlot]string

	mu    sync.Mutex
	rows  map[usageKey]*contracts.UsageRow
	clock func() time.Time
}

type usageKey struct{ host, model, role string }

// NewMeter wraps gen. mainModel and judgeModel name the models a request that
// carries no model of its own resolves to, so a row is never labelled blank.
func NewMeter(gen contracts.Generator, mainModel, judgeModel string) *Meter {
	return &Meter{
		inner: gen,
		defaults: map[contracts.ModelSlot]string{
			contracts.SlotMain: mainModel, contracts.SlotJudge: judgeModel,
		},
		rows:  map[usageKey]*contracts.UsageRow{},
		clock: time.Now,
	}
}

// GenerateStream implements contracts.Generator.
func (m *Meter) GenerateStream(ctx context.Context, req contracts.GenerateRequest, emit func(contracts.GenerationEvent) error) (contracts.GenerationResult, error) {
	start := m.clock()
	res, err := m.inner.GenerateStream(ctx, req, emit)
	m.record(req, res, err, m.clock().Sub(start))
	return res, err
}

// record adds one finished call to its row.
func (m *Meter) record(req contracts.GenerateRequest, res contracts.GenerationResult, err error, took time.Duration) {
	key := usageKey{host: res.Receipt.Host, model: req.Model, role: string(req.Role)}
	if key.host == "" {
		// No adapter served the call: the router had no host to resolve.
		key.host = "unresolved"
	}
	if key.model == "" {
		key.model = m.defaults[req.ModelSlot]
	}
	if key.model == "" {
		key.model = "host default"
	}
	if key.role == "" {
		key.role = "unknown"
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[key]
	if !ok {
		row = &contracts.UsageRow{Host: key.host, Model: key.model, Role: key.role}
		m.rows[key] = row
	}
	row.Requests++
	if err != nil {
		row.Failures++
	}
	row.InputTokens += res.Usage.InputTokens
	row.OutputTokens += res.Usage.OutputTokens
	row.Milliseconds += took.Milliseconds()
}

// Report returns the accounting so far, busiest row first.
func (m *Meter) Report() contracts.UsageReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := contracts.UsageReport{Rows: make([]contracts.UsageRow, 0, len(m.rows))}
	for _, row := range m.rows {
		out.Rows = append(out.Rows, *row)
		out.Totals.Requests += row.Requests
		out.Totals.Failures += row.Failures
		out.Totals.InputTokens += row.InputTokens
		out.Totals.OutputTokens += row.OutputTokens
		out.Totals.Milliseconds += row.Milliseconds
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		switch {
		case a.Requests != b.Requests:
			return a.Requests > b.Requests
		case a.Host != b.Host:
			return a.Host < b.Host
		case a.Model != b.Model:
			return a.Model < b.Model
		default:
			return a.Role < b.Role
		}
	})
	return out
}
