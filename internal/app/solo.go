//go:build client && !server

package app

import (
	"context"
	"fmt"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/relay"
)

// soloAuthority adapts the in-process room to the executor's authority
// interface.
type soloAuthority struct{ r *room }

var _ authority = soloAuthority{}

func (s soloAuthority) Emit(t relay.WSEventType, payload any) error { return s.r.Emit(t, payload) }

func (s soloAuthority) Doc(context.Context) (string, uint64, error) {
	text, version := s.r.Doc()
	return text, version, nil
}

func (s soloAuthority) Ingest(p relay.IngestAppendPayload) error { return s.r.Ingest(p) }

func (s soloAuthority) Local(t relay.WSEventType, payload any, sticky string) {
	s.r.Local(t, payload, sticky)
}

func (s soloAuthority) Events() *pump { return s.r.Events() }

// HoldLease claims the lease and renews it on the lease cadence until ctx ends.
// The reducer measures the 15 s TTL on its own clock, so a renewal that stops
// would fence the executor exactly as it would against the relay.
func (s soloAuthority) HoldLease(ctx context.Context, lost func(error)) error {
	if err := s.r.ClaimLease(); err != nil {
		return fmt.Errorf("claim executor lease: %w", err)
	}
	go func() {
		t := time.NewTicker(leaseRenewEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.r.RenewLease(); err != nil {
					lost(err)
					return
				}
			}
		}
	}()
	return nil
}
