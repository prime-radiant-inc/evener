package hubcore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestVoiceJournalRecoveryPagesAndExplicitSessionDeletion(t *testing.T) {
	t.Parallel()
	j, root, b := voiceJournalFixture(t)
	ctx := context.Background()
	first := voiceJournalPrepared(t, j, b, 1)
	second := voiceJournalPrepared(t, j, b, 2)
	d := voiceJournalDelivery(b)
	if err := j.CreateSpeechDelivery(ctx, d); err != nil {
		t.Fatal(err)
	}
	next := d
	next.ID += "-next"
	next.PresentationGeneration++
	if err := j.CreateSpeechDelivery(ctx, next); err != nil {
		t.Fatal(err)
	}
	intents, cursor, err := j.ListIntents(ctx, b.ID, 0, 1)
	if err != nil || len(intents) != 1 || intents[0].TurnID != first.TurnID || cursor <= 0 {
		t.Fatalf("first recovery page: count=%d cursor=%d err=%v", len(intents), cursor, err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewVoiceJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	third := voiceJournalPrepared(t, reopened, b, 3)
	intents, nextCursor, err := reopened.ListIntents(ctx, b.ID, cursor, 1)
	if err != nil || len(intents) != 1 || intents[0].TurnID != second.TurnID || nextCursor <= cursor {
		t.Fatalf("second recovery page after reopen: %v", err)
	}
	intents, lastCursor, err := reopened.ListIntents(ctx, b.ID, nextCursor, 1)
	if err != nil || len(intents) != 1 || intents[0].TurnID != third.TurnID {
		t.Fatalf("new recovery entry: %v", err)
	}
	intents, endCursor, err := reopened.ListIntents(ctx, b.ID, lastCursor, 1)
	if err != nil || len(intents) != 0 || endCursor != lastCursor {
		t.Fatalf("end recovery cursor: %v", err)
	}
	speech, speechCursor, err := reopened.ListSpeechDeliveries(ctx, b.ID, 0, 1)
	if err != nil || len(speech) != 1 || speech[0].ID != d.ID {
		t.Fatalf("first speech page: %v", err)
	}
	speech, _, err = reopened.ListSpeechDeliveries(ctx, b.ID, speechCursor, 1)
	if err != nil || len(speech) != 1 || speech[0].ID != next.ID {
		t.Fatalf("second speech page: %v", err)
	}
	for _, limit := range []int{-1, 0, 201} {
		if _, _, err := reopened.ListIntents(ctx, b.ID, 0, limit); !errors.Is(err, ErrVoiceInvalid) {
			t.Fatalf("unbounded intent read: %v", err)
		}
		if _, _, err := reopened.ListSpeechDeliveries(ctx, b.ID, 0, limit); !errors.Is(err, ErrVoiceInvalid) {
			t.Fatalf("unbounded speech read: %v", err)
		}
	}
	other := b
	other.ID, other.Ref, other.Epoch = "other", "remote:other", "other-epoch"
	if err := reopened.CreateBinding(ctx, other, false); err != nil {
		t.Fatal(err)
	}
	preserved := voiceJournalPrepared(t, reopened, other, 1)
	if err := reopened.RetireBinding(ctx, b.ID, b.Epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Intent(ctx, first.TurnID); err != nil {
		t.Fatalf("retirement deleted finalized text: %v", err)
	}
	if err := reopened.DeleteSession(ctx, b.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Binding(ctx, b.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted session binding remains: %v", err)
	}
	if _, err := reopened.Turn(ctx, first.TurnID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted session turn remains: %v", err)
	}
	if _, err := reopened.Intent(ctx, first.TurnID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted session text remains: %v", err)
	}
	if _, err := reopened.SpeechDelivery(ctx, d.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted session presentation remains: %v", err)
	}
	if _, err := reopened.Intent(ctx, preserved.TurnID); err != nil {
		t.Fatalf("another session was deleted: %v", err)
	}
}
