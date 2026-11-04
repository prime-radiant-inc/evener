package hubcore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func voiceJournalFixture(t *testing.T) (*VoiceJournal, string, VoiceBinding) {
	t.Helper()
	root := t.TempDir()
	j, err := NewVoiceJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	b := VoiceBinding{ID: "b", DeviceID: "d", Ref: "local:chief", InstanceID: "instance", Epoch: "epoch", ExpiresAt: time.Now().Add(time.Hour), Active: true}
	if err := j.CreateBinding(context.Background(), b, false); err != nil {
		t.Fatal(err)
	}
	return j, root, b
}

func TestVoiceJournalTurnCreatedBeforeProviderAndSequenceDeduplicated(t *testing.T) {
	t.Parallel()
	j, root, b := voiceJournalFixture(t)
	ctx := context.Background()
	turn, err := j.CreateTurn(ctx, b.ID, b.Epoch, 1)
	if err != nil {
		t.Fatalf("persist turn before provider: %v", err)
	}
	if turn.ID == "" || turn.ProviderInputItemID != "" || turn.Sequence != 1 || turn.BindingID != b.ID || turn.Epoch != b.Epoch {
		t.Fatal("invalid initial turn identity")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewVoiceJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	duplicate, err := reopened.CreateTurn(ctx, b.ID, b.Epoch, 1)
	if err != nil || !reflect.DeepEqual(duplicate, turn) {
		t.Fatalf("sequence created a second turn: %v", err)
	}
}

func voiceJournalPrepared(t *testing.T, j *VoiceJournal, b VoiceBinding, sequence int64) VoiceIntent {
	t.Helper()
	ctx := context.Background()
	turn, err := j.CreateTurn(ctx, b.ID, b.Epoch, sequence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.CommitTurnInput(ctx, turn.ID, "provider-"+turn.ID); err != nil {
		t.Fatal(err)
	}
	in, err := j.PrepareIntent(ctx, turn.ID, appwire.MethodTurnStart, " \t/opaque-"+turn.ID+"\n", "mutation-"+turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func voiceJournalReceipt(in VoiceIntent) appwire.MutationReceipt {
	ref, _ := appwire.ParseRef(in.Ref)
	return appwire.MutationReceipt{ClientMutationID: in.MutationID, ThreadID: ref.ThreadID, InstanceID: in.ExpectedInstanceID, Disposition: appwire.MutationDispositionApplied, ProjectionState: appwire.MutationProjectionPending}
}

func TestVoiceJournalIntentLostAcknowledgementSurvivesReopen(t *testing.T) {
	t.Parallel()
	j, root, b := voiceJournalFixture(t)
	ctx := context.Background()
	in := voiceJournalPrepared(t, j, b, 0)
	observer, err := NewVoiceJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	stored, err := observer.Intent(ctx, in.TurnID)
	if err != nil || !reflect.DeepEqual(stored, in) || stored.State != VoiceIntentPrepared || stored.Receipt != nil {
		t.Fatalf("intent missing before attempt: %v", err)
	}
	var payload appwire.TurnStartParams
	if err := json.Unmarshal(in.Params, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Ref != b.Ref || payload.ExpectedInstanceID != b.InstanceID || payload.ClientMutationID != in.MutationID || payload.ManagerProof != nil || len(payload.Input) != 1 || payload.Input[0].Type != "text" || payload.Input[0].Text != in.Text {
		t.Fatal("intent payload changed text or authority")
	}
	if _, err := j.RecordReceipt(ctx, in.TurnID, voiceJournalReceipt(in)); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("receipt before attempt: %v", err)
	}
	attempted, err := j.RecordAttempt(ctx, in.TurnID, b.InstanceID, false)
	if err != nil || attempted.State != VoiceIntentUncertain || attempted.Receipt != nil {
		t.Fatalf("attempt: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	lostAck, err := observer.Intent(ctx, in.TurnID)
	if err != nil || lostAck.State != VoiceIntentUncertain || lostAck.Receipt != nil || string(lostAck.Params) != string(in.Params) {
		t.Fatalf("lost acknowledgement custody: %v", err)
	}
	if _, err := observer.RecordAttempt(ctx, in.TurnID, b.InstanceID, false); !errors.Is(err, ErrVoiceReconcileRequired) {
		t.Fatalf("blind retry: %v", err)
	}
	if _, err := observer.RecordAttempt(ctx, in.TurnID, "replacement", true); !errors.Is(err, ErrVoiceStale) {
		t.Fatalf("retargeted retry: %v", err)
	}
	retry, err := observer.RecordAttempt(ctx, in.TurnID, b.InstanceID, true)
	if err != nil || !reflect.DeepEqual(retry, lostAck) {
		t.Fatalf("retry drift: %v", err)
	}
	for _, change := range []func(*appwire.MutationReceipt){
		func(r *appwire.MutationReceipt) { r.ClientMutationID += "x" },
		func(r *appwire.MutationReceipt) { r.ThreadID += "x" },
		func(r *appwire.MutationReceipt) { r.InstanceID += "x" },
		func(r *appwire.MutationReceipt) { r.Disposition = "unknown" },
	} {
		receipt := voiceJournalReceipt(in)
		change(&receipt)
		if _, err := observer.RecordReceipt(ctx, in.TurnID, receipt); !errors.Is(err, ErrVoiceConflict) {
			t.Fatalf("foreign receipt: %v", err)
		}
	}
	if err := observer.RetireBinding(ctx, b.ID, b.Epoch); err != nil {
		t.Fatal(err)
	}
	accepted, err := observer.RecordReceipt(ctx, in.TurnID, voiceJournalReceipt(in))
	if err != nil || accepted.State != VoiceIntentAccepted || accepted.Receipt == nil {
		t.Fatalf("late receipt after retirement: %v", err)
	}
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewVoiceJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	durable, err := reopened.Intent(ctx, in.TurnID)
	if err != nil || !reflect.DeepEqual(durable, accepted) {
		t.Fatalf("accepted custody lost on reopen: %v", err)
	}
	duplicate, err := reopened.PrepareIntent(ctx, in.TurnID, in.Method, in.Text, in.MutationID)
	if err != nil || !reflect.DeepEqual(duplicate, accepted) {
		t.Fatalf("duplicate completed call: %v", err)
	}
}

func TestVoiceJournalIntentIdentityIsImmutable(t *testing.T) {
	t.Parallel()
	j, _, b := voiceJournalFixture(t)
	ctx := context.Background()
	in := voiceJournalPrepared(t, j, b, 1)
	for _, args := range [][3]string{{appwire.MethodTurnSteer, in.Text, in.MutationID}, {in.Method, in.Text + "x", in.MutationID}, {in.Method, in.Text, in.MutationID + "x"}} {
		if _, err := j.PrepareIntent(ctx, in.TurnID, args[0], args[1], args[2]); !errors.Is(err, ErrVoiceConflict) {
			t.Fatalf("changed intent: %v", err)
		}
	}
	duplicate, err := j.PrepareIntent(ctx, in.TurnID, in.Method, in.Text, in.MutationID)
	if err != nil || !reflect.DeepEqual(duplicate, in) {
		t.Fatalf("identity overwritten: %v", err)
	}
	another, err := j.CreateTurn(ctx, b.ID, b.Epoch, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.CommitTurnInput(ctx, another.ID, "next-item"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.PrepareIntent(ctx, another.ID, in.Method, in.Text, in.MutationID); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("mutation reused by another turn: %v", err)
	}
	steer, err := j.PrepareIntent(ctx, another.ID, appwire.MethodTurnSteer, in.Text, in.MutationID+"-steer")
	if err != nil || steer.Method != appwire.MethodTurnSteer {
		t.Fatalf("steer admission: %v", err)
	}
	if err := j.AbortTurn(ctx, in.TurnID); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("aborted finalized text: %v", err)
	}
}

func TestVoiceJournalPartialTurnCannotBecomeIntent(t *testing.T) {
	t.Parallel()
	j, _, b := voiceJournalFixture(t)
	ctx := context.Background()
	turn, err := j.CreateTurn(ctx, b.ID, b.Epoch, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.PrepareIntent(ctx, turn.ID, appwire.MethodTurnStart, "opaque", "m"); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("partial input interpreted: %v", err)
	}
	committed, err := j.CommitTurnInput(ctx, turn.ID, "provider-item")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.CommitTurnInput(ctx, turn.ID, "different-item"); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("input remapped: %v", err)
	}
	duplicate, err := j.CommitTurnInput(ctx, turn.ID, committed.ProviderInputItemID)
	if err != nil || !reflect.DeepEqual(duplicate, committed) {
		t.Fatalf("commit duplicate: %v", err)
	}
	if err := j.AbortTurn(ctx, turn.ID); err != nil {
		t.Fatal(err)
	}
	aborted, err := j.Turn(ctx, turn.ID)
	if err != nil || aborted.State != VoiceTurnAborted || aborted.ProviderInputItemID != committed.ProviderInputItemID {
		t.Fatalf("abort lost immutable provider mapping: %v", err)
	}
	if _, err := j.PrepareIntent(ctx, turn.ID, appwire.MethodTurnStart, "opaque", "m"); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("aborted input interpreted: %v", err)
	}
	if _, err := j.Intent(ctx, turn.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("partial turn has intent: %v", err)
	}
	if _, err := j.CommitTurnInput(ctx, turn.ID, committed.ProviderInputItemID); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("aborted turn reopened: %v", err)
	}
}

func TestVoiceJournalBindingRenewalAndRetirementFences(t *testing.T) {
	t.Parallel()
	j, _, b := voiceJournalFixture(t)
	ctx := context.Background()
	in := voiceJournalPrepared(t, j, b, 1)
	if err := j.RenewBinding(ctx, b.ID, "stale", b.ExpiresAt.Add(time.Hour)); !errors.Is(err, ErrVoiceStale) {
		t.Fatalf("stale renewal: %v", err)
	}
	if err := j.RenewBinding(ctx, b.ID, b.Epoch, b.ExpiresAt.Add(-time.Minute)); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("expiry regressed: %v", err)
	}
	expiry := b.ExpiresAt.Add(time.Hour)
	if err := j.RenewBinding(ctx, b.ID, b.Epoch, expiry); err != nil {
		t.Fatal(err)
	}
	got, err := j.Binding(ctx, b.ID)
	if err != nil || !got.ExpiresAt.Equal(expiry) {
		t.Fatalf("renewal missing: %v", err)
	}
	if _, err := j.CreateTurn(ctx, b.ID, "stale", 2); !errors.Is(err, ErrVoiceStale) {
		t.Fatalf("stale turn: %v", err)
	}
	if _, err := j.RecordAttempt(ctx, in.TurnID, "replacement", false); !errors.Is(err, ErrVoiceStale) {
		t.Fatalf("changed instance dispatch: %v", err)
	}
	if err := j.RetireBinding(ctx, b.ID, "stale"); !errors.Is(err, ErrVoiceStale) {
		t.Fatalf("stale retirement: %v", err)
	}
	if err := j.RetireBinding(ctx, b.ID, b.Epoch); err != nil {
		t.Fatal(err)
	}
	if err := j.RenewBinding(ctx, b.ID, b.Epoch, expiry); !errors.Is(err, ErrVoiceStale) {
		t.Fatalf("retired renewal: %v", err)
	}
	if _, err := j.RecordAttempt(ctx, in.TurnID, b.InstanceID, false); !errors.Is(err, ErrVoiceStale) {
		t.Fatalf("retired dispatch: %v", err)
	}
	held, err := j.Intent(ctx, in.TurnID)
	if err != nil || !reflect.DeepEqual(held, in) {
		t.Fatalf("held input changed: %v", err)
	}
	expired := b
	expired.ID, expired.Epoch = "expired", "expired-epoch"
	expired.ExpiresAt = time.Now().Add(-time.Hour)
	if err := j.CreateBinding(ctx, expired, false); err != nil {
		t.Fatal(err)
	}
	if _, err := j.CreateTurn(ctx, expired.ID, expired.Epoch, 1); !errors.Is(err, ErrVoiceStale) {
		t.Fatalf("expired grant: %v", err)
	}
}

func TestVoiceJournalConcurrentConnectionsHaveOneOwnerAndTurn(t *testing.T) {
	t.Parallel()
	j, root, b := voiceJournalFixture(t)
	ctx := context.Background()
	other, err := NewVoiceJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	turns := make([]VoiceTurn, 2)
	errs := make([]error, 2)
	for n, store := range []*VoiceJournal{j, other} {
		wg.Add(1)
		go func() { defer wg.Done(); turns[n], errs[n] = store.CreateTurn(ctx, b.ID, b.Epoch, 1) }()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || turns[0].ID == "" || turns[0].ID != turns[1].ID {
		t.Fatalf("concurrent sequence: %v", errs)
	}
	if err := j.RetireBinding(ctx, b.ID, b.Epoch); err != nil {
		t.Fatal(err)
	}
	for n, store := range []*VoiceJournal{j, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			candidate := b
			candidate.ID += string(rune('a' + n))
			candidate.Epoch = candidate.ID
			errs[n] = store.CreateBinding(ctx, candidate, false)
		}()
	}
	wg.Wait()
	successes, conflicts := 0, 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrVoiceBindingBusy) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("two connections admitted competing owners")
	}
}

func TestVoiceJournalCorruptRecordsDoNotExposeText(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"{", `{"Text":"sensitive-canary"}`} {
		t.Run(raw[:1], func(t *testing.T) {
			j, _, b := voiceJournalFixture(t)
			ctx := context.Background()
			in := voiceJournalPrepared(t, j, b, 1)
			if _, err := j.db.ExecContext(ctx, `UPDATE voice_intents SET record=? WHERE turn_id=?`, raw, in.TurnID); err != nil {
				t.Fatal(err)
			}
			_, err := j.Intent(ctx, in.TurnID)
			if !errors.Is(err, ErrVoiceCorrupt) || strings.Contains(err.Error(), in.Text) {
				t.Fatalf("corrupt intent not safely classified: %v", err)
			}
		})
	}
}

func TestVoiceJournalFilesystemFailureCannotAccept(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// A directory at the database path is an actual filesystem failure, not a mocked journal.
	if err := os.MkdirAll(filepath.Join(root, "voice", "journal.db"), 0700); err != nil {
		t.Fatal(err)
	}
	if j, err := NewVoiceJournal(root); err == nil || j != nil {
		if j != nil {
			_ = j.Close()
		}
		t.Fatal("filesystem failure returned usable journal")
	}
	j, _, b := voiceJournalFixture(t)
	ctx := context.Background()
	in := voiceJournalPrepared(t, j, b, 1)
	if _, err := j.RecordAttempt(ctx, in.TurnID, b.InstanceID, false); err != nil {
		t.Fatal(err)
	}
	// SQLite read-only enforcement fails the real receipt write and leaves uncertainty intact.
	if _, err := j.db.ExecContext(ctx, `PRAGMA query_only=ON`); err != nil {
		t.Fatal(err)
	}
	accepted, err := j.RecordReceipt(ctx, in.TurnID, voiceJournalReceipt(in))
	if err == nil || accepted.State == VoiceIntentAccepted {
		t.Fatal("failed write reported acceptance")
	}
	stored, err := j.Intent(ctx, in.TurnID)
	if err != nil || stored.State != VoiceIntentUncertain || stored.Receipt != nil {
		t.Fatalf("failed write changed custody: %v", err)
	}
}
