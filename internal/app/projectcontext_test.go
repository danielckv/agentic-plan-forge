//go:build client && !server

package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
	"github.com/danielckv/agentic-plan-forge/internal/gather"
)

// A gather started for an older draft can finish after one started for the
// current draft. The window must keep showing the current one.
func TestRefreshContextNeverLetsAnOlderRunWin(t *testing.T) {
	release := make(chan struct{})
	exec := newExecutor(executorConfig{Repo: t.TempDir(), Logger: quietLogger(),
		Gather: func(ctx context.Context, opts gather.Options) (contracts.ProjectContext, error) {
			if opts.Text == "old draft" {
				<-release
			}
			return contracts.ProjectContext{Source: contracts.ContextSourceFiles, Hits: []contracts.ContextHit{
				{Path: "docs/" + strings.ReplaceAll(opts.Text, " ", "-") + ".md", Text: opts.Text, Score: 1},
			}}, nil
		},
	})
	ctx := context.Background()
	done := make(chan struct{})
	go func() { exec.refreshContext(ctx, "old draft"); close(done) }()
	// Wait until the old run holds its sequence number, so the order is fixed.
	for deadline := time.Now().Add(5 * time.Second); ; {
		exec.mu.Lock()
		started := exec.contextSeq == 1
		exec.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the old run never started")
		}
		time.Sleep(time.Millisecond)
	}
	exec.refreshContext(ctx, "new draft")
	close(release)
	<-done

	got := exec.ProjectContext()
	if len(got.Hits) != 1 || got.Hits[0].Path != "docs/new-draft.md" {
		t.Fatalf("context = %+v, want the newer draft's", got)
	}
	if st := exec.Status(); st.Context != contextOK || !strings.Contains(st.ContextDetail, "1 related note(s) from project notes and docs") {
		t.Errorf("pill = %q %q", st.Context, st.ContextDetail)
	}
}

// A gatherer error never fails the session; it shows as a failed pill.
func TestCollectContextTurnsAnErrorIntoANote(t *testing.T) {
	exec := newExecutor(executorConfig{Repo: t.TempDir(), Logger: quietLogger(),
		Gather: func(context.Context, gather.Options) (contracts.ProjectContext, error) {
			return contracts.ProjectContext{}, errors.New("disk on fire")
		},
	})
	exec.refreshContext(context.Background(), "anything")
	st := exec.Status()
	if st.Context != contextFailed || !strings.Contains(st.ContextDetail, "disk on fire") {
		t.Errorf("pill = %q %q, want failed with the reason", st.Context, st.ContextDetail)
	}
	if got := exec.ProjectContext(); got.Hits == nil || len(got.Hits) != 0 {
		t.Errorf("context = %+v, want an empty, non-nil hit list", got)
	}
}

func TestContextPillWording(t *testing.T) {
	for _, tc := range []struct {
		pc          contracts.ProjectContext
		state, want string
	}{
		{contracts.ProjectContext{Source: contracts.ContextSourceRecall, Hits: make([]contracts.ContextHit, 3)}, contextOK, "3 related note(s) from the recall index"},
		{contracts.ProjectContext{Source: contracts.ContextSourceFiles}, contextNone, "nothing related in project notes and docs"},
		{contracts.ProjectContext{Source: contracts.ContextSourceNone, Notes: []string{"no requirements text to match yet"}}, contextNone, "write requirements to find related notes"},
		{contracts.ProjectContext{Source: contracts.ContextSourceNone}, contextNone, "no project notes, specs or decision records found"},
	} {
		state, detail := contextPill(tc.pc)
		if state != tc.state || detail != tc.want {
			t.Errorf("contextPill(%+v) = %q %q, want %q %q", tc.pc, state, detail, tc.state, tc.want)
		}
	}
}
