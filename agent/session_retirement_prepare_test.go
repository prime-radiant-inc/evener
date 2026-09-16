package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

// retirementPrepareFixture builds a root session with an attached controller and
// returns both, plus the claim a manual TryClaim produced.
func retirementPrepareFixture(t *testing.T) (*Session, *RetirementController, *RetirementClaim) {
	t.Helper()
	root := newQueuePersistTestSession(t, t.TempDir())
	t.Cleanup(func() { root.Close() })
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	claim, state, err := c.TryClaim(true)
	if err != nil {
		t.Fatalf("TryClaim: %v", err)
	}
	if claim == nil {
		t.Fatalf("fixture was not claimable: %+v", state)
	}
	return root, c, claim
}

// TestRetirementPreparationCorruptTranscriptStaysResident is the plan's Step 1
// primary-file failure case: the real transcript file is replaced with garbage,
// preparation must refuse it, the session stays resident, and admission reopens.
func TestRetirementPreparationCorruptTranscriptStaysResident(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	path := root.TranscriptPath()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Error(err)
		}
	})
	claim, state, err := c.TryClaim(true)
	if err != nil {
		t.Fatal(err)
	}
	// Either the evidence read or preparation must refuse the corrupt original.
	if claim != nil {
		if _, err := c.Prepare(context.Background(), claim); err == nil {
			t.Fatal("preparation accepted corrupt original transcript")
		}
		if err := c.Abort(claim, "prepare_failed"); err != nil {
			t.Fatal(err)
		}
	} else if !slices.ContainsFunc(state.Blockers, func(b RetirementBlocker) bool {
		return b.Category == "persistence"
	}) {
		t.Fatalf("corruption lacked persistence diagnostic: %+v", state)
	}
	if got := c.Snapshot().Phase; got != "resident" {
		t.Fatalf("phase = %s", got)
	}
	release, err := c.BeginMutation(root.ID(), "input")
	if err != nil {
		t.Fatalf("preparation failure closed admission: %v", err)
	}
	release()
}

// TestRetirementPreparationMetadataWriteFailureStaysResident forces the
// metadata write through SessionConfig.testOnly.metaFS to fail; preparation
// must surface the persistence error instead of succeeding.
func TestRetirementPreparationMetadataWriteFailureStaysResident(t *testing.T) {
	root, c, claim := retirementPrepareFixture(t)
	root.cfg.testOnly.metaFS = afero.NewReadOnlyFs(afero.NewMemMapFs())
	if _, err := c.Prepare(context.Background(), claim); err == nil {
		t.Fatal("preparation accepted a failed metadata write")
	}
	if err := c.Abort(claim, "prepare_failed"); err != nil {
		t.Fatal(err)
	}
	if got := c.Snapshot().Phase; got != "resident" {
		t.Fatalf("phase = %s", got)
	}
}

// TestRetirementPreparationCorruptMutationStaysResident replaces the committed
// client-mutation snapshot with malformed JSON; readiness reads the primary
// file, so preparation must refuse it.
func TestRetirementPreparationCorruptMutationStaysResident(t *testing.T) {
	root, c, claim := retirementPrepareFixture(t)
	path := clientMutationFilePath(root.stateDir, root.id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if original == nil {
			_ = os.Remove(path)
			return
		}
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Error(err)
		}
	})
	if _, err := c.Prepare(context.Background(), claim); err == nil {
		t.Fatal("preparation accepted a corrupt client mutation snapshot")
	}
	if err := c.Abort(claim, "prepare_failed"); err != nil {
		t.Fatal(err)
	}
}

// TestRetirementPreparationMissingStateDirStaysResident proves a session with
// no durable state cannot be validated as reconstructible.
func TestRetirementPreparationMissingStateDirStaysResident(t *testing.T) {
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	root, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer root.Close()
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	claim, _, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("TryClaim: claim=%v err=%v", claim, err)
	}
	if _, err := c.Prepare(context.Background(), claim); err == nil {
		t.Fatal("preparation accepted a session without a state directory")
	}
	if err := c.Abort(claim, "prepare_failed"); err != nil {
		t.Fatal(err)
	}
}

// TestRetirementPreparationStaleClaimRefused proves Prepare requires the exact
// live preparation and never manufactures one from an aborted claim.
func TestRetirementPreparationStaleClaimRefused(t *testing.T) {
	_, c, claim := retirementPrepareFixture(t)
	if err := c.Abort(claim, "prepare_failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Prepare(context.Background(), claim); err == nil {
		t.Fatal("preparation accepted an aborted claim")
	}
	if _, err := c.Prepare(context.Background(), nil); err == nil {
		t.Fatal("preparation accepted a nil claim")
	}
}

// TestRetirementPreparationMissingScratchArtifactStaysResident proves a pinned
// required scratch directory that has vanished blocks preparation instead of
// being silently minted or ignored.
func TestRetirementPreparationMissingScratchArtifactStaysResident(t *testing.T) {
	root, c, claim := retirementPrepareFixture(t)
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}
	scratch, err := sandbox.NewSessionScratch(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Retain() })
	ref := sandbox.ScratchReference{Dir: scratch.Dir, Kind: sandbox.ScratchKindUnsandboxed}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(scratch.Dir); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Prepare(context.Background(), claim); err == nil {
		t.Fatal("preparation accepted a missing required scratch artifact")
	}
	if err := c.Abort(claim, "prepare_failed"); err != nil {
		t.Fatal(err)
	}
}

// TestRetirementPreparationCorruptTaskStoreStaysResident proves preparation
// finishes/flushes the session's durable task store and surfaces a corrupt
// primary task file as a persistence failure rather than a warning.
func TestRetirementPreparationCorruptTaskStoreStaysResident(t *testing.T) {
	root, c, claim := retirementPrepareFixture(t)
	path := filepath.Join(root.stateDir, "tasks", root.id+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if _, err := c.Prepare(context.Background(), claim); err == nil {
		t.Fatal("preparation accepted a corrupt task store")
	}
	if err := c.Abort(claim, "prepare_failed"); err != nil {
		t.Fatal(err)
	}
	if got := c.Snapshot().Phase; got != "resident" {
		t.Fatalf("phase = %s", got)
	}
}

// TestRetirementPreparationCapturesOccupiedLane proves preparation carries the
// occupied managed lane's identity and lock ownership, verified against the
// live lock, rather than only refusing a bad lane in tree evidence.
func TestRetirementPreparationCapturesOccupiedLane(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	res, err := r.create(t, map[string]any{"name": "lane"})
	if err != nil {
		t.Fatalf("create lane: %v", err)
	}
	lanePath := res["path"].(string)
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(r.s); err != nil {
		t.Fatal(err)
	}
	claim, state, err := c.TryClaim(true)
	if err != nil {
		t.Fatalf("TryClaim: %v", err)
	}
	if claim == nil {
		t.Fatalf("lane fixture was not claimable: %+v", state)
	}
	prep, err := c.Prepare(context.Background(), claim)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(prep.lanes) != 1 {
		t.Fatalf("preparation lanes = %+v, want the occupied lane", prep.lanes)
	}
	lane := prep.lanes[0]
	if lane.sessionID != r.s.id || lane.path != lanePath || lane.branch == "" || lane.owner == "" {
		t.Fatalf("lane evidence = %+v (want session %q path %q with branch and owner)", lane, r.s.id, lanePath)
	}
}

// TestRetirementPreparationFailureEmitsNoTerminalEventOrClose proves plan 572's
// requirement that a preparation failure leaves the session resident: no
// session-end event is emitted and the job and delegate stores stay open.
func TestRetirementPreparationFailureEmitsNoTerminalEventOrClose(t *testing.T) {
	root, c, claim := retirementPrepareFixture(t)
	evs, mu, _ := collectEvents(root)
	path := root.TranscriptPath()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(path, original, 0o600) })
	if _, err := c.Prepare(context.Background(), claim); err == nil {
		t.Fatal("preparation accepted a corrupt transcript")
	}
	if err := c.Abort(claim, "prepare_failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := root.jobManager.store.Load(); err != nil {
		t.Fatalf("preparation failure closed the job store: %v", err)
	}
	if _, err := root.delegateController.store.Load(); err != nil {
		t.Fatalf("preparation failure closed the delegate store: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range *evs {
		if ev.Kind == events.EventSessionEnd {
			t.Fatalf("preparation failure emitted a session-end event: %+v", ev)
		}
	}
	if root.state == SessionClosed {
		t.Fatal("preparation failure closed the session")
	}
}

// TestRetirementPreparationWrongOwnerLaneStaysResident exercises the lane
// verification error branches: a foreign-locked and an unlocked occupied lane
// both refuse preparation.
func TestRetirementPreparationWrongOwnerLaneStaysResident(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, r *scriptedLaneRepo, lanePath string)
		want   string
	}{
		{name: "foreign owner", mutate: func(t *testing.T, r *scriptedLaneRepo, lanePath string) {
			r.setLaneLock(t, lanePath, "foreign-owner-marker")
		}, want: "another owner"},
		{name: "unlocked", mutate: func(t *testing.T, r *scriptedLaneRepo, lanePath string) {
			r.unlockLane(t, lanePath)
		}, want: "not locked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sr := newScriptedLaneRepo(t)
			r := sr.wt()
			res, err := r.create(t, map[string]any{"name": "lane"})
			if err != nil {
				t.Fatalf("create lane: %v", err)
			}
			lanePath := res["path"].(string)
			tc.mutate(t, sr, lanePath)
			c, err := NewRetirementController(0, clock.Real())
			if err != nil {
				t.Fatal(err)
			}
			if err := c.AttachRoot(r.s); err != nil {
				t.Fatal(err)
			}
			claim, state, err := c.TryClaim(true)
			if err != nil || claim == nil {
				t.Fatalf("lane fixture not claimable: %+v %v", state, err)
			}
			if _, err := c.Prepare(context.Background(), claim); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Prepare error = %v, want %q", err, tc.want)
			}
			if err := c.Abort(claim, "prepare_failed"); err != nil {
				t.Fatal(err)
			}
			if got := c.Snapshot().Phase; got != "resident" {
				t.Fatalf("phase = %s", got)
			}
		})
	}
}

// TestRetirementPreparationMissingChildTranscriptStaysResident exercises a
// missing child transcript through Prepare, not only the TryClaim predicate.
func TestRetirementPreparationMissingChildTranscriptStaysResident(t *testing.T) {
	root, _, c := newRetirementDelegateController(t)
	defer root.Close()
	d := retirementIdleDelegate(t, root)
	claim, state, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("idle delegate not claimable: %+v %v", state, err)
	}
	child := root.delegateController.residentDelegateRuntime(d.DelegateID)
	if child == nil {
		t.Fatal("resident delegate missing")
	}
	if err := os.Remove(child.TranscriptPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Prepare(context.Background(), claim); err == nil {
		t.Fatal("preparation accepted a missing child transcript")
	}
	if err := c.Abort(claim, "prepare_failed"); err != nil {
		t.Fatal(err)
	}
}

// TestRetirementPreparationMalformedDescriptorStaysResident exercises a
// corrupt durable delegate descriptor through Prepare's strict store readiness.
func TestRetirementPreparationMalformedDescriptorStaysResident(t *testing.T) {
	root, _, c := newRetirementDelegateController(t)
	defer root.Close()
	_ = retirementIdleDelegate(t, root)
	claim, state, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("idle delegate not claimable: %+v %v", state, err)
	}
	journal := filepath.Join(jobsDir(root.stateDir, root.id), "delegates.jsonl")
	original, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, []byte("{not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(journal, original, 0o600) })
	if _, err := c.Prepare(context.Background(), claim); err == nil {
		t.Fatal("preparation accepted a malformed delegate descriptor log")
	}
	if err := c.Abort(claim, "prepare_failed"); err != nil {
		t.Fatal(err)
	}
}

// TestRetirementPreparationUnreadableColdEvidenceStaysResident exercises a cold
// delegate whose reconstruction evidence cannot be read, through Prepare.
func TestRetirementPreparationUnreadableColdEvidenceStaysResident(t *testing.T) {
	root, tree, c := newRetirementDelegateController(t)
	defer root.Close()
	d := retirementIdleDelegate(t, root)
	claim, state, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("idle delegate not claimable: %+v %v", state, err)
	}
	tree.mu.Lock()
	if live := tree.live[d.DelegateID]; live != nil {
		live.runtime = nil
	}
	tree.mu.Unlock()
	jobs := filepath.Join(jobsDir(root.stateDir, d.ChildSessionID), "jobs.jsonl")
	if err := os.Remove(jobs); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Prepare(context.Background(), claim); err == nil {
		t.Fatal("preparation accepted unreadable cold reconstruction evidence")
	}
	if err := c.Abort(claim, "prepare_failed"); err != nil {
		t.Fatal(err)
	}
}
