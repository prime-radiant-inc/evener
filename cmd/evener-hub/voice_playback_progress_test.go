package hub

import (
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestVoicePlaybackProgressRefreshesOnCompletion(t *testing.T) {
	t.Parallel()
	for _, outputComplete := range []bool{false, true} {
		t.Run(fmt.Sprintf("outputComplete=%v", outputComplete), func(t *testing.T) {
			ctx := t.Context()
			journal, err := hubcore.NewVoiceJournal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = journal.Close() })
			binding := hubcore.VoiceBinding{ID: "binding", Ref: "local:manager", InstanceID: "instance", Epoch: "epoch", Active: true, ExpiresAt: time.Now().Add(time.Hour)}
			if err := journal.CreateBinding(ctx, binding, false); err != nil {
				t.Fatal(err)
			}
			delivery := hubcore.SpeechDelivery{ID: "delivery", BindingID: binding.ID, Epoch: binding.Epoch, InstanceID: binding.InstanceID, CanonicalSourceItemID: "source", CanonicalText: "fixture-591", OriginatingRequestID: "request", FrontierID: "frontier", PresentationGeneration: 2}
			if err := journal.CreateSpeechDelivery(ctx, delivery); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"first", "second"} {
				item := hubcore.SpeechDeliveryItem{ResponseID: "response", ItemID: id, GeneratedSamples: 480, QueuedSamples: 480}
				if _, err := journal.UpdateSpeechDelivery(ctx, delivery.ID, hubcore.SpeechDeliveryUpdate{Epoch: binding.Epoch, PresentationGeneration: 2, Item: &item}); err != nil {
					t.Fatal(err)
				}
			}
			delivery, err = journal.UpdateSpeechDelivery(ctx, delivery.ID, hubcore.SpeechDeliveryUpdate{Epoch: binding.Epoch, PresentationGeneration: 2, OutputComplete: outputComplete})
			if err != nil {
				t.Fatal(err)
			}
			bridge := voiceBridge{ctx: ctx, journal: journal, binding: binding, delivery: &delivery}
			record := func(item string, samples int64, complete, refresh bool) {
				t.Helper()
				if err := bridge.recordPlayed(voiceControl{DeliveryID: delivery.ID, Generation: 2, ItemID: item, Samples: samples, Complete: complete}); err != nil {
					t.Fatal(err)
				}
				if bridge.refresh != refresh {
					t.Errorf("item=%s samples=%d complete=%v refresh=%v, want %v", item, samples, complete, bridge.refresh, refresh)
				}
				stored, err := journal.SpeechDelivery(ctx, delivery.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, got := range stored.Items {
					if got.ItemID == item && (got.PlayedSamples != samples || got.PlaybackComplete != complete) {
						t.Fatalf("progress was not persisted: %+v", got)
					}
				}
				bridge.refresh = false
			}
			record("first", 240, false, false)
			record("first", 240, false, false)
			record("first", 480, true, false)
			record("first", 480, true, false)
			// A pending history/provenance refresh must survive progress updates.
			bridge.refresh = true
			record("second", 240, false, true)
			record("second", 480, false, false)
			record("second", 480, true, outputComplete)
			record("second", 480, true, false)
			if bridge.playbackFinished() != outputComplete {
				t.Fatal("playback completion lost the provider-completion boundary")
			}
		})
	}
}
