package hubcore

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

func TestVoiceJournalMaxSpeechGenerationSurvivesReopen(t *testing.T) {
	t.Parallel()
	j, root, binding := voiceJournalFixture(t)
	ctx := context.Background()
	if got, err := j.MaxSpeechGeneration(ctx, binding.ID, binding.Epoch); err != nil || got != 0 {
		t.Fatalf("fresh binding generation = %d, %v", got, err)
	}
	// Insert out of order and across decimal widths. Neither rowid nor lexical
	// order identifies the greatest reserved generation.
	for _, generation := range []uint64{2, 100, 10, 9} {
		delivery := voiceJournalDelivery(binding)
		delivery.ID = "generation-" + strconv.FormatUint(generation, 10)
		delivery.PresentationGeneration = generation
		if err := j.CreateSpeechDelivery(ctx, delivery); err != nil {
			t.Fatal(err)
		}
		if _, err := j.UpdateSpeechDelivery(ctx, delivery.ID, SpeechDeliveryUpdate{Epoch: binding.Epoch, PresentationGeneration: generation, Interrupted: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewVoiceJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got, err := reopened.MaxSpeechGeneration(ctx, binding.ID, binding.Epoch); err != nil || got != 100 {
		t.Fatalf("reopened interrupted generation floor = %d, %v", got, err)
	}
	next := voiceJournalDelivery(binding)
	next.ID, next.PresentationGeneration = "next-generation", 101
	if err := reopened.CreateSpeechDelivery(ctx, next); err != nil {
		t.Fatal(err)
	}
	next.ID = "conflicting-generation"
	if err := reopened.CreateSpeechDelivery(ctx, next); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("duplicate durable generation accepted: %v", err)
	}
	if got, err := reopened.MaxSpeechGeneration(ctx, binding.ID, binding.Epoch); err != nil || got != 101 {
		t.Fatalf("new durable generation floor = %d, %v", got, err)
	}
}

func TestVoiceJournalMaxSpeechGenerationScopesBindingAndEpoch(t *testing.T) {
	t.Parallel()
	j, _, binding := voiceJournalFixture(t)
	ctx := context.Background()
	delivery := voiceJournalDelivery(binding)
	delivery.PresentationGeneration = ^uint64(0)
	if err := j.CreateSpeechDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	if got, err := j.MaxSpeechGeneration(ctx, binding.ID, binding.Epoch); err != nil || got != delivery.PresentationGeneration {
		t.Fatalf("unsigned journal generation was narrowed: %d, %v", got, err)
	}
	if _, err := j.MaxSpeechGeneration(ctx, binding.ID, binding.Epoch+"-stale"); !errors.Is(err, ErrVoiceStale) {
		t.Fatalf("stale epoch read a generation floor: %v", err)
	}
	replacement := binding
	replacement.ID, replacement.Epoch = "replacement", "replacement-epoch"
	if err := j.CreateBinding(ctx, replacement, true); err != nil {
		t.Fatal(err)
	}
	if got, err := j.MaxSpeechGeneration(ctx, replacement.ID, replacement.Epoch); err != nil || got != 0 {
		t.Fatalf("takeover inherited another binding's generation: %d, %v", got, err)
	}
	if _, err := j.MaxSpeechGeneration(ctx, binding.ID, binding.Epoch); !errors.Is(err, ErrVoiceStale) {
		t.Fatalf("retired binding supplied a generation floor: %v", err)
	}
}
