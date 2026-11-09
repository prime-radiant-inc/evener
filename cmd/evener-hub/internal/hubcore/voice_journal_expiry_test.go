package hubcore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func voiceJournalExpiredOwner(t *testing.T) (*VoiceJournal, string, VoiceBinding, SpeechDelivery) {
	t.Helper()
	j, root, owner := voiceJournalFixture(t)
	speech := voiceJournalDelivery(owner)
	ctx := context.Background()
	if err := j.CreateSpeechDelivery(ctx, speech); err != nil {
		t.Fatal(err)
	}
	// Persist an elapsed expiry, then reopen as a hub that missed the expiry timer.
	owner.ExpiresAt = time.Now().Add(-time.Hour).UTC()
	raw, err := json.Marshal(owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.db.ExecContext(ctx, `UPDATE voice_bindings SET record=? WHERE id=?`, raw, owner.ID); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewVoiceJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	return reopened, root, owner, speech
}

func TestVoiceJournalExpiredOwnerDoesNotRequireTakeover(t *testing.T) {
	t.Parallel()
	j, root, owner, speech := voiceJournalExpiredOwner(t)
	ctx := context.Background()
	next := owner
	next.ID, next.Epoch, next.DeviceID = "next", "next-epoch", "next-device"
	next.ExpiresAt = time.Now().Add(time.Hour).UTC()
	if err := j.CreateBinding(ctx, next, false); err != nil {
		t.Fatalf("fresh Arm blocked by expired owner: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewVoiceJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	old, err := reopened.Binding(ctx, owner.ID)
	if err != nil || old.Active {
		t.Fatalf("expired owner not durably retired: %v", err)
	}
	got, err := reopened.Binding(ctx, next.ID)
	if err != nil || !reflect.DeepEqual(got, next) {
		t.Fatalf("replacement not durable: %v", err)
	}
	interrupted, err := reopened.SpeechDelivery(ctx, speech.ID)
	if err != nil || !interrupted.Interrupted {
		t.Fatalf("expired speech not durably interrupted: %v", err)
	}
	if err := reopened.RenewBinding(ctx, owner.ID, owner.Epoch, next.ExpiresAt); !errors.Is(err, ErrVoiceStale) {
		t.Fatalf("old owner revived: %v", err)
	}
}

func TestVoiceJournalExpiredReplacementPreservesOwner(t *testing.T) {
	t.Parallel()
	j, _, owner, speech := voiceJournalExpiredOwner(t)
	ctx := context.Background()
	next := owner
	next.ID, next.Epoch = "next", "next-epoch"
	for _, takeover := range []bool{false, true} {
		if err := j.CreateBinding(ctx, next, takeover); !errors.Is(err, ErrVoiceStale) {
			t.Errorf("expired replacement admission, takeover=%v: %v", takeover, err)
		}
		got, err := j.Binding(ctx, owner.ID)
		if err != nil || !reflect.DeepEqual(got, owner) {
			t.Fatalf("invalid replacement changed owner: %v", err)
		}
		gotSpeech, err := j.SpeechDelivery(ctx, speech.ID)
		if err != nil || !reflect.DeepEqual(gotSpeech, speech) {
			t.Fatalf("invalid replacement changed speech: %v", err)
		}
	}
}

func TestVoiceJournalExpiredOwnerReplacementRollsBackOnInsertFailure(t *testing.T) {
	t.Parallel()
	j, root, owner, speech := voiceJournalExpiredOwner(t)
	ctx := context.Background()
	conflict := owner
	conflict.Epoch = "replacement-epoch"
	conflict.ExpiresAt = time.Now().Add(time.Hour).UTC()
	err := j.CreateBinding(ctx, conflict, false)
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY {
		t.Fatalf("expected duplicate-ID insertion failure after expiry retirement, got %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewVoiceJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Binding(ctx, owner.ID)
	if err != nil || !reflect.DeepEqual(got, owner) {
		t.Fatalf("failed insertion retired owner: %v", err)
	}
	gotSpeech, err := reopened.SpeechDelivery(ctx, speech.ID)
	if err != nil || !reflect.DeepEqual(gotSpeech, speech) {
		t.Fatalf("failed insertion interrupted speech: %v", err)
	}
}

func TestVoiceJournalUnexpiredOwnerStillRequiresTakeover(t *testing.T) {
	t.Parallel()
	j, _, owner := voiceJournalFixture(t)
	ctx := context.Background()
	next := owner
	next.ID, next.Epoch = "next", "next-epoch"
	if err := j.CreateBinding(ctx, next, false); !errors.Is(err, ErrVoiceBindingBusy) {
		t.Fatalf("unexpired owner replaced without consent: %v", err)
	}
	if err := j.CreateBinding(ctx, next, true); err != nil {
		t.Fatal(err)
	}
	old, err := j.Binding(ctx, owner.ID)
	if err != nil || old.Active {
		t.Fatalf("explicit takeover did not retire owner: %v", err)
	}
}
