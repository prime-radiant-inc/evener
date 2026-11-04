package hubcore

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func voiceJournalDelivery(b VoiceBinding) SpeechDelivery {
	return SpeechDelivery{ID: "speech", BindingID: b.ID, Epoch: b.Epoch, InstanceID: b.InstanceID, CanonicalSourceItemID: "source-item", CanonicalText: "opaque-canonical-" + b.ID, OriginatingRequestID: "request", FrontierID: "frontier", RunGeneration: 7, PresentationGeneration: 9}
}

func TestVoiceJournalSpeechSourceAndPlaybackSurviveReopen(t *testing.T) {
	t.Parallel()
	j, root, b := voiceJournalFixture(t)
	ctx := context.Background()
	source := voiceJournalDelivery(b)
	if err := j.CreateSpeechDelivery(ctx, source); err != nil {
		t.Fatalf("persist authoritative source before provider: %v", err)
	}
	generated := "opaque-generated-" + source.ID
	item := SpeechDeliveryItem{ResponseID: "response", ItemID: "item", GeneratedSamples: 24000}
	update := SpeechDeliveryUpdate{Epoch: b.Epoch, PresentationGeneration: source.PresentationGeneration, Item: &item, GeneratedTranscript: &generated}
	got, err := j.UpdateSpeechDelivery(ctx, source.ID, update)
	if err != nil || got.CanonicalText != source.CanonicalText || got.GeneratedTranscript != generated || len(got.Items) != 1 || got.Items[0].PlayedSamples != 0 {
		t.Fatalf("generation claimed playout or replaced source: %v", err)
	}
	item.QueuedSamples = 24000
	if _, err := j.UpdateSpeechDelivery(ctx, source.ID, update); err != nil {
		t.Fatal(err)
	}
	item.PlayedSamples = 12000
	got, err = j.UpdateSpeechDelivery(ctx, source.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewVoiceJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stored, err := reopened.SpeechDelivery(ctx, source.ID)
	if err != nil || !reflect.DeepEqual(stored, got) {
		t.Fatalf("lost playback evidence: %v", err)
	}
	if err := reopened.CreateSpeechDelivery(ctx, source); err != nil {
		t.Fatalf("duplicate source: %v", err)
	}
	after, err := reopened.SpeechDelivery(ctx, source.ID)
	if err != nil || !reflect.DeepEqual(after, stored) {
		t.Fatalf("duplicate creation erased playback: %v", err)
	}
	update.Interrupted = true
	interrupted, err := reopened.UpdateSpeechDelivery(ctx, source.ID, update)
	if err != nil || !interrupted.Interrupted || interrupted.Items[0].PlayedSamples != 12000 {
		t.Fatalf("interrupt misrepresented rendering: %v", err)
	}
	if err := reopened.RetireBinding(ctx, b.ID, b.Epoch); err != nil {
		t.Fatal(err)
	}
	item.PlayedSamples = 18000
	if _, err := reopened.UpdateSpeechDelivery(ctx, source.ID, update); err != nil {
		t.Fatalf("late played evidence lost: %v", err)
	}
	item.GeneratedSamples++
	if _, err := reopened.UpdateSpeechDelivery(ctx, source.ID, update); !errors.Is(err, ErrVoiceStale) {
		t.Fatalf("retired generation extended: %v", err)
	}
}

func TestVoiceJournalSpeechIdentityAndWatermarkFences(t *testing.T) {
	t.Parallel()
	j, _, b := voiceJournalFixture(t)
	ctx := context.Background()
	source := voiceJournalDelivery(b)
	if err := j.CreateSpeechDelivery(ctx, source); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SpeechDelivery){
		func(d *SpeechDelivery) { d.CanonicalSourceItemID += "x" },
		func(d *SpeechDelivery) { d.CanonicalText += "x" },
		func(d *SpeechDelivery) { d.OriginatingRequestID += "x" },
		func(d *SpeechDelivery) { d.FrontierID += "x" },
		func(d *SpeechDelivery) { d.RunGeneration++ },
		func(d *SpeechDelivery) { d.InstanceID += "x" },
	} {
		changed := source
		change(&changed)
		if err := j.CreateSpeechDelivery(ctx, changed); !errors.Is(err, ErrVoiceConflict) {
			t.Fatalf("replaced immutable source: %v", err)
		}
	}
	other := source
	other.ID += "x"
	if err := j.CreateSpeechDelivery(ctx, other); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("reused presentation generation: %v", err)
	}
	item := SpeechDeliveryItem{ResponseID: "response", ItemID: "item", GeneratedSamples: 24000, QueuedSamples: 12000, PlayedSamples: 6000}
	update := SpeechDeliveryUpdate{Epoch: b.Epoch, PresentationGeneration: source.PresentationGeneration, Item: &item}
	if _, err := j.UpdateSpeechDelivery(ctx, source.ID, update); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SpeechDeliveryUpdate){
		func(u *SpeechDeliveryUpdate) { u.Epoch += "x" },
		func(u *SpeechDeliveryUpdate) { u.PresentationGeneration++ },
	} {
		changed := update
		change(&changed)
		if _, err := j.UpdateSpeechDelivery(ctx, source.ID, changed); !errors.Is(err, ErrVoiceStale) {
			t.Fatalf("stale playback update: %v", err)
		}
	}
	for _, change := range []func(*SpeechDeliveryItem){
		func(i *SpeechDeliveryItem) { i.GeneratedSamples-- },
		func(i *SpeechDeliveryItem) { i.QueuedSamples-- },
		func(i *SpeechDeliveryItem) { i.PlayedSamples-- },
		func(i *SpeechDeliveryItem) { i.PlayedSamples = i.GeneratedSamples + 1 },
		func(i *SpeechDeliveryItem) { i.QueuedSamples = i.GeneratedSamples + 1 },
		func(i *SpeechDeliveryItem) { i.ResponseID += "x" },
	} {
		changed := item
		change(&changed)
		u := update
		u.Item = &changed
		if _, err := j.UpdateSpeechDelivery(ctx, source.ID, u); !errors.Is(err, ErrVoiceConflict) {
			t.Fatalf("invalid item mutation: %v", err)
		}
	}
	next := SpeechDeliveryItem{ResponseID: item.ResponseID, ItemID: "next", GeneratedSamples: 8000}
	update.Item = &next
	if _, err := j.UpdateSpeechDelivery(ctx, source.ID, update); err != nil {
		t.Fatal(err)
	}
	if err := j.RetireBinding(ctx, b.ID, b.Epoch); err != nil {
		t.Fatal(err)
	}
	got, err := j.SpeechDelivery(ctx, source.ID)
	if err != nil || !got.Interrupted || len(got.Items) != 2 || got.Items[1].PlayedSamples != 0 {
		t.Fatalf("retirement lost playback uncertainty: %v", err)
	}
}
