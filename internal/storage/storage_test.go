package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danielckv/agentic-plan-forge/internal/contracts"
)

func testRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func pairBytes(t *testing.T, approvalID, specText string) (contracts.ApprovalMeta, []byte, []byte) {
	t.Helper()
	spec := []byte(specText)
	meta := contracts.ApprovalMeta{ApprovalID: approvalID, CreatedAt: time.Date(2026, 5, 1, 12, 0, len(approvalID), 0, time.UTC)}
	m := contracts.PlanManifest{
		SchemaVersion: 1, ApprovalID: approvalID, Title: "t", CreatedAt: meta.CreatedAt,
		SpecSHA256: sum(spec), TaskDAG: []contracts.TechnicalTask{}, Targets: []contracts.Target{},
		VerificationCommands: []contracts.VerificationCommand{}, EstimateAssumptions: []string{},
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return meta, spec, append(raw, '\n')
}

func newStore(t *testing.T, repo string) *ArtifactStore {
	t.Helper()
	s, err := NewArtifactStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPlanGitignoreIsCreatedWithTheFourLines(t *testing.T) {
	repo := testRepo(t)
	st := NewStateStore()
	if _, err := st.Load(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, PlanDirName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("Load must not create .plan/")
	}
	if err := st.Save(context.Background(), State{SchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(repo, ".plan", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "*\n!.gitignore\n!synthesized_spec.md\n!plan_manifest.json\n" {
		t.Fatalf(".gitignore = %q", got)
	}
	// A user's edit survives a later write.
	if err := os.WriteFile(filepath.Join(repo, ".plan", ".gitignore"), []byte("custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(context.Background(), State{SchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(repo, ".plan", ".gitignore"))
	if string(got) != "custom\n" {
		t.Fatal("an existing .gitignore must not be rewritten")
	}
}

func TestGitCheckIgnoreSplitsArtifactsFromState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := testRepo(t)
	run := func(args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		return cmd.Run()
	}
	if err := run("init", "-q"); err != nil {
		t.Skipf("git init: %v", err)
	}
	ctx := context.Background()
	st := NewStateStore()
	if _, err := st.Load(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(ctx, State{SchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	meta, spec, manifest := pairBytes(t, "a1", "# spec\n")
	if _, err := newStore(t, repo).Commit(ctx, meta, spec, manifest); err != nil {
		t.Fatal(err)
	}
	exit := func(path string) int {
		err := run("check-ignore", "-q", path)
		if err == nil {
			return 0
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		t.Fatal(err)
		return -1
	}
	if got := exit(".plan/synthesized_spec.md"); got != 1 {
		t.Errorf("git check-ignore .plan/synthesized_spec.md exit = %d, want 1 (trackable)", got)
	}
	if got := exit(".plan/plan_manifest.json"); got != 1 {
		t.Errorf("git check-ignore .plan/plan_manifest.json exit = %d, want 1 (trackable)", got)
	}
	if got := exit(".plan/state.json"); got != 0 {
		t.Errorf("git check-ignore .plan/state.json exit = %d, want 0 (ignored)", got)
	}
}

func TestStateStoreRoundTripAndMode(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	st := NewStateStore()
	got, err := st.Load(ctx, repo)
	if err != nil || got.SchemaVersion != 1 || len(got.RunLog) != 0 {
		t.Fatalf("fresh Load = %+v, %v", got, err)
	}
	got.PRDRevision, got.InputRevision = 4, 9
	got.RunLog = append(got.RunLog, RunLogEntry{At: time.Unix(1_800_000_000, 0).UTC(), RunID: "r1", Cycle: 2, Event: "CYCLE_START"})
	got.LastApproval = &ApprovalRecord{ApprovalID: "a1", SpecSHA256: strings.Repeat("a", 64), ManifestSHA256: strings.Repeat("b", 64)}
	if err := st.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(repo, ".plan", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("state.json mode = %v, want 0600", info.Mode().Perm())
	}
	again, err := NewStateStore().Load(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if again.PRDRevision != 4 || again.InputRevision != 9 || len(again.RunLog) != 1 || again.LastApproval.ApprovalID != "a1" {
		t.Fatalf("round trip lost data: %+v", again)
	}
}

func TestStateStoreRefusesSecretsPathsAndURLs(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	st := NewStateStore()
	if _, err := st.Load(ctx, repo); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		"absolute unix path": "/Users/someone/project/src",
		"absolute windows":   `C:\Users\someone`,
		"embedded path":      "failed reading /home/someone/.ssh/id",
		"invite url":         "https://relay.example/join/abc",
		"websocket url":      "wss://relay.example/ws?session=1",
		"unc path":           `\\server\share`,
	}
	for name, detail := range bad {
		s := State{SchemaVersion: 1, RunLog: []RunLogEntry{{Event: "x", Detail: detail}}}
		if err := st.Save(ctx, s); err == nil {
			t.Errorf("%s: Save accepted %q", name, detail)
		}
	}
	// Secret-looking keys cannot be smuggled in through the schema either: the
	// walk rejects any such key in the serialized document.
	if err := walkState("", map[string]any{"relay_token": "x"}); err == nil {
		t.Error("a relay_token key must be forbidden")
	}
	for _, key := range []string{"invite_url", "room_id", "member_id", "prompt", "password", "api_key", "Authorization", "client_secret"} {
		if err := walkState("", map[string]any{key: "v"}); err == nil {
			t.Errorf("key %q must be forbidden", key)
		}
	}
	// The real schema's own keys are all allowed.
	if err := (State{SchemaVersion: 1, RunLog: []RunLogEntry{{Event: "CYCLE_START", Detail: "cycle 2 of 3"}}, LastApproval: &ApprovalRecord{ApprovalID: "a"}}).Validate(); err != nil {
		t.Errorf("a legitimate state was rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".plan", "state.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a rejected Save must not create state.json")
	}
}

func TestStateStoreMalformedAndUnsupported(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(repo, ".plan"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".plan", "state.json")
	if err := os.WriteFile(path, []byte(`{"schema_version": 1, "run_log": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewStateStore().Load(ctx, repo)
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("malformed state: %v", err)
	}
	if got, _ := os.ReadFile(path); !strings.Contains(string(got), `"run_log": [`) {
		t.Fatal("a malformed file must not be overwritten")
	}
	if err := os.WriteFile(path, []byte(`{"schema_version": 7}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStateStore().Load(ctx, repo); contracts.CodeOf(err) != contracts.CodeUnsupportedVer {
		t.Fatalf("unsupported version: %v", err)
	}
}

func TestStateStoreNeedsLoadBeforeSave(t *testing.T) {
	if err := NewStateStore().Save(context.Background(), State{}); err == nil {
		t.Fatal("Save before Load must fail")
	}
}

func readPair(t *testing.T, repo string) (Pair, error) {
	t.Helper()
	return ReadPair(repo)
}

func TestCommitAndReadBack(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	meta, spec, manifest := pairBytes(t, "approval-1", "# one\n")
	res, err := newStore(t, repo).Commit(ctx, meta, spec, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if res.SpecSHA256 != sum(spec) || res.ManifestSHA256 != sum(manifest) {
		t.Fatalf("CommitResult hashes wrong: %+v", res)
	}
	p, err := newStore(t, repo).Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.Spec, spec) || !bytes.Equal(p.Manifest, manifest) || p.Meta.ApprovalID != "approval-1" {
		t.Fatalf("read back %+v", p.Meta)
	}
	for _, leftover := range []string{"transactions"} {
		if _, err := os.Stat(filepath.Join(repo, ".plan", leftover)); err == nil {
			entries, _ := os.ReadDir(filepath.Join(repo, ".plan", leftover))
			if len(entries) != 0 {
				t.Fatalf("%s still holds %d entries after a clean commit", leftover, len(entries))
			}
		}
	}
	if _, err := ReadPair(testRepo(t)); !errors.Is(err, ErrNoPlan) {
		t.Fatalf("empty repo: %v, want ErrNoPlan", err)
	}
}

func TestCommitRejectsInconsistentInput(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	meta, spec, manifest := pairBytes(t, "a1", "# one\n")
	s := newStore(t, repo)
	if _, err := s.Commit(ctx, meta, []byte("# tampered\n"), manifest); ArtifactCode(err) != CodeEHASH {
		t.Fatalf("spec/manifest hash mismatch: %v", err)
	}
	if _, err := s.Commit(ctx, contracts.ApprovalMeta{ApprovalID: "other"}, spec, manifest); ArtifactCode(err) != CodeEHASH {
		t.Fatalf("approval id mismatch: %v", err)
	}
	if _, err := s.Commit(ctx, meta, spec, []byte("not json")); ArtifactCode(err) != CodeEHASH {
		t.Fatalf("invalid manifest: %v", err)
	}
	if _, err := s.Commit(ctx, meta, nil, manifest); ArtifactCode(err) != CodeEHASH {
		t.Fatalf("empty spec: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".plan")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a rejected commit must not touch the repository")
	}
}

func TestHistoryKeepsFivePriorPairs(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	for i := 1; i <= 8; i++ {
		meta, spec, manifest := pairBytes(t, fmt.Sprintf("approval-%02d", i), fmt.Sprintf("# generation %d\n", i))
		if _, err := newStore(t, repo).Commit(ctx, meta, spec, manifest); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(repo, ".plan", "history"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != HistoryKeep {
		t.Fatalf("history holds %d pairs, want %d", len(entries), HistoryKeep)
	}
	// Generation 8 is current; history keeps 3..7 and pruned 1 and 2.
	first := entries[0].Name()
	if !strings.Contains(first, "approval-03") {
		t.Fatalf("oldest retained = %s, want approval-03", first)
	}
	spec, err := os.ReadFile(filepath.Join(repo, ".plan", "history", first, SpecFileName))
	if err != nil || string(spec) != "# generation 3\n" {
		t.Fatalf("history spec = %q, %v", spec, err)
	}
	if p, err := readPair(t, repo); err != nil || p.Meta.ApprovalID != "approval-08" {
		t.Fatalf("current pair = %+v, %v", p.Meta.ApprovalID, err)
	}
}

// crashPoints lists every hook point a commit reaches, in order, by running one
// commit with a recording hook.
func crashPoints(t *testing.T, withPrior bool) []string {
	t.Helper()
	repo := testRepo(t)
	ctx := context.Background()
	if withPrior {
		meta, spec, manifest := pairBytes(t, "old", "# old\n")
		if _, err := newStore(t, repo).Commit(ctx, meta, spec, manifest); err != nil {
			t.Fatal(err)
		}
	}
	s := newStore(t, repo)
	var points []string
	s.hook = func(p string) error { points = append(points, p); return nil }
	meta, spec, manifest := pairBytes(t, "new", "# new\n")
	if _, err := s.Commit(ctx, meta, spec, manifest); err != nil {
		t.Fatal(err)
	}
	return points
}

func assertNoLeftovers(t *testing.T, repo, label string) {
	t.Helper()
	dir := filepath.Join(repo, ".plan")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".new-") || strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("%s: staging leftover %s", label, e.Name())
		}
	}
	if tx, err := os.ReadDir(filepath.Join(dir, "transactions")); err == nil && len(tx) != 0 {
		t.Errorf("%s: %d transactions left after recovery", label, len(tx))
	}
}

func TestCommitCrashInjectionLeavesOldOrNewPair(t *testing.T) {
	for _, withPrior := range []bool{true, false} {
		points := crashPoints(t, withPrior)
		if len(points) < 14 {
			t.Fatalf("expected at least 14 injection points, got %d: %v", len(points), points)
		}
		seen := map[string]bool{}
		for _, point := range points {
			if seen[point] {
				continue
			}
			seen[point] = true
			label := fmt.Sprintf("prior=%v crash@%s", withPrior, point)
			repo := testRepo(t)
			ctx := context.Background()
			oldMeta, oldSpec, oldManifest := pairBytes(t, "old", "# old\n")
			if withPrior {
				if _, err := newStore(t, repo).Commit(ctx, oldMeta, oldSpec, oldManifest); err != nil {
					t.Fatal(err)
				}
			}
			crashing := newStore(t, repo)
			crashing.hook = func(p string) error {
				if p == point {
					return errCrash
				}
				return nil
			}
			meta, spec, manifest := pairBytes(t, "new", "# new\n")
			_, err := crashing.Commit(ctx, meta, spec, manifest)
			if !errors.Is(err, errCrash) {
				t.Fatalf("%s: Commit returned %v, want the simulated crash", label, err)
			}

			// A fresh process reads: it recovers, then sees one whole pair.
			got, err := newStore(t, repo).Read(ctx)
			switch {
			case errors.Is(err, ErrNoPlan):
				if withPrior {
					t.Fatalf("%s: the old pair vanished", label)
				}
			case err != nil:
				t.Fatalf("%s: Read: %v", label, err)
			default:
				isOld := withPrior && bytes.Equal(got.Spec, oldSpec) && bytes.Equal(got.Manifest, oldManifest)
				isNew := bytes.Equal(got.Spec, spec) && bytes.Equal(got.Manifest, manifest)
				if !isOld && !isNew {
					t.Fatalf("%s: mixed pair: spec %q approval %q", label, got.Spec, got.Meta.ApprovalID)
				}
			}
			assertNoLeftovers(t, repo, label)
			// Recovery is idempotent.
			if err := newStore(t, repo).Recover(ctx); err != nil {
				t.Fatalf("%s: second Recover: %v", label, err)
			}
			// And the store is usable afterwards.
			if _, err := newStore(t, repo).Commit(ctx, meta, spec, manifest); err != nil {
				t.Fatalf("%s: commit after recovery: %v", label, err)
			}
			if p, err := ReadPair(repo); err != nil || p.Meta.ApprovalID != "new" {
				t.Fatalf("%s: after recovery commit: %v", label, err)
			}
		}
	}
}

func TestCommitFailureKeepsTheOldPair(t *testing.T) {
	points := crashPoints(t, true)
	seen := map[string]bool{}
	boom := errors.New("injected disk failure")
	for _, point := range points {
		if seen[point] || strings.HasPrefix(point, "lock") || strings.HasPrefix(point, "recovered") {
			continue
		}
		seen[point] = true
		repo := testRepo(t)
		ctx := context.Background()
		oldMeta, oldSpec, oldManifest := pairBytes(t, "old", "# old\n")
		if _, err := newStore(t, repo).Commit(ctx, oldMeta, oldSpec, oldManifest); err != nil {
			t.Fatal(err)
		}
		s := newStore(t, repo)
		s.hook = func(p string) error {
			if p == point {
				return boom
			}
			return nil
		}
		meta, spec, manifest := pairBytes(t, "new", "# new\n")
		_, err := s.Commit(ctx, meta, spec, manifest)
		got, rerr := newStore(t, repo).Read(ctx)
		if rerr != nil {
			t.Fatalf("fail@%s: Read: %v", point, rerr)
		}
		committedAnyway := err == nil
		isOld := bytes.Equal(got.Spec, oldSpec) && bytes.Equal(got.Manifest, oldManifest)
		isNew := bytes.Equal(got.Spec, spec) && bytes.Equal(got.Manifest, manifest)
		switch {
		case err != nil && !isOld:
			t.Errorf("fail@%s: Commit returned %v but the old pair was not preserved", point, err)
		case err == nil && !isNew:
			t.Errorf("fail@%s: Commit reported success but the new pair is not on disk", point)
		case !isOld && !isNew:
			t.Errorf("fail@%s: mixed pair", point)
		}
		_ = committedAnyway
		assertNoLeftovers(t, repo, "fail@"+point)
	}
}

func TestReadersNeverSeeAMixedPair(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	oldMeta, oldSpec, oldManifest := pairBytes(t, "old", "# old\n")
	if _, err := newStore(t, repo).Commit(ctx, oldMeta, oldSpec, oldManifest); err != nil {
		t.Fatal(err)
	}
	s := newStore(t, repo)
	s.hook = func(p string) error {
		if p == "replace-spec:after" {
			return errCrash
		}
		return nil
	}
	meta, spec, manifest := pairBytes(t, "new", "# new\n")
	if _, err := s.Commit(ctx, meta, spec, manifest); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	// New spec, old manifest: the lock-free reader refuses to expose it.
	if _, err := ReadPair(repo); !errors.Is(err, ErrPairMismatch) {
		t.Fatalf("ReadPair on a torn pair = %v, want ErrPairMismatch", err)
	}
	// ReadPair must not have repaired anything: it writes nothing.
	if _, err := ReadPair(repo); !errors.Is(err, ErrPairMismatch) {
		t.Fatal("ReadPair must not recover")
	}
	got, err := newStore(t, repo).Read(ctx)
	if err != nil || got.Meta.ApprovalID != "old" {
		t.Fatalf("Read after recovery = %v %v, want the old pair restored", got.Meta.ApprovalID, err)
	}
}

func TestSymlinkComponentsAreRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	ctx := context.Background()
	meta, spec, manifest := pairBytes(t, "a1", "# one\n")

	repo := testRepo(t)
	outside := testRepo(t)
	if err := os.Symlink(outside, filepath.Join(repo, ".plan")); err != nil {
		t.Fatal(err)
	}
	_, err := newStore(t, repo).Commit(ctx, meta, spec, manifest)
	if ArtifactCode(err) != CodeESYMLINK {
		t.Fatalf(".plan symlink: %v", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("a write escaped through the symlink: %d entries", len(entries))
	}
	if err := NewStateStore().Save(ctx, State{}); err == nil {
		t.Fatal("unbound Save must fail")
	}
	st := NewStateStore()
	if _, err := st.Load(ctx, repo); ArtifactCode(err) != CodeESYMLINK {
		t.Fatalf("Load through a .plan symlink: %v", err)
	}

	repo2 := testRepo(t)
	if err := os.MkdirAll(filepath.Join(repo2, ".plan"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo2, ".plan", "history")); err != nil {
		t.Fatal(err)
	}
	old, oldSpec, oldManifest := pairBytes(t, "old", "# old\n")
	if _, err := newStore(t, repo2).Commit(ctx, old, oldSpec, oldManifest); err != nil {
		t.Fatalf("first commit needs no history: %v", err)
	}
	if _, err := newStore(t, repo2).Commit(ctx, meta, spec, manifest); ArtifactCode(err) != CodeESYMLINK {
		// The second commit rotates into history; a symlinked history is refused
		// without failing the already-durable commit.
		got, rerr := ReadPair(repo2)
		if rerr != nil || got.Meta.ApprovalID != "a1" {
			t.Fatalf("history symlink: err=%v pair=%v", err, rerr)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("history rotation wrote through a symlink: %d entries", len(entries))
	}

	repo3 := testRepo(t)
	if err := os.MkdirAll(filepath.Join(repo3, ".plan"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "elsewhere.md")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(repo3, ".plan", SpecFileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := newStore(t, repo3).Commit(ctx, meta, spec, manifest); ArtifactCode(err) != CodeESYMLINK {
		t.Fatalf("spec symlink: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "keep" {
		t.Fatal("a write went through the spec symlink")
	}
}

func TestPathRejectsTraversal(t *testing.T) {
	p, err := openPlan(testRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../escape", ".plan/../../escape", "/etc/passwd", ".."} {
		if _, err := p.path(rel); err == nil {
			t.Errorf("path(%q) accepted a traversal", rel)
		}
	}
	if _, err := p.path(".plan/state.json"); err != nil {
		t.Errorf("a plain path was rejected: %v", err)
	}
}

func TestCommitTakesThePlanLock(t *testing.T) {
	repo := testRepo(t)
	p, err := openPlan(repo)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := p.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	meta, spec, manifest := pairBytes(t, "a1", "# one\n")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err = newStore(t, repo).Commit(ctx, meta, spec, manifest)
	if contracts.CodeOf(err) != contracts.CodeLocked {
		t.Fatalf("Commit under a held lock = %v, want LOCKED", err)
	}
	if err := NewStateStore().Save(ctx, State{}); err == nil {
		t.Fatal("unbound Save must fail")
	}
	unlock()
	if _, err := newStore(t, repo).Commit(context.Background(), meta, spec, manifest); err != nil {
		t.Fatalf("Commit after unlock: %v", err)
	}
}

func TestReceiptsRoundTripAndAreKeyedByHashes(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	r := Receipt{
		TaskID: "../../etc/passwd", VerificationID: "V1", CommandSHA256: strings.Repeat("c", 64),
		SourceFingerprint: strings.Repeat("f", 64), ExitCode: 0, ExpectedExitCode: 0, OutputSHA256: strings.Repeat("d", 64), At: time.Now().UTC(),
	}
	if err := WriteReceipt(ctx, repo, r); err != nil {
		t.Fatal(err)
	}
	failed := r
	failed.TaskID, failed.ExitCode = "T2", 1
	if err := WriteReceipt(ctx, repo, failed); err != nil {
		t.Fatal(err)
	}
	got, err := ReadReceipts(repo)
	if err != nil || len(got) != 2 {
		t.Fatalf("ReadReceipts = %d, %v", len(got), err)
	}
	var ok, bad int
	for _, g := range got {
		if g.Succeeded() {
			ok++
		} else {
			bad++
		}
	}
	if ok != 1 || bad != 1 {
		t.Fatalf("succeeded/failed = %d/%d", ok, bad)
	}
	entries, _ := os.ReadDir(filepath.Join(repo, ".plan", "verification"))
	for _, e := range entries {
		if strings.Contains(e.Name(), "passwd") || strings.Contains(e.Name(), "..") {
			t.Fatalf("an untrusted task ID reached a file name: %s", e.Name())
		}
	}
	if none, err := ReadReceipts(testRepo(t)); err != nil || len(none) != 0 {
		t.Fatalf("no receipts: %v %v", none, err)
	}
}

func TestArtifactErrorsNeverCarryAbsolutePaths(t *testing.T) {
	repo := testRepo(t)
	outside := testRepo(t)
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	if err := os.Symlink(outside, filepath.Join(repo, ".plan")); err != nil {
		t.Fatal(err)
	}
	meta, spec, manifest := pairBytes(t, "a1", "# one\n")
	_, err := newStore(t, repo).Commit(context.Background(), meta, spec, manifest)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), repo) || strings.Contains(err.Error(), outside) {
		t.Fatalf("error leaks an absolute path: %v", err)
	}
}
