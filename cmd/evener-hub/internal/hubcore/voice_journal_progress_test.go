package hubcore

import (
	"errors"
	"reflect"
	"testing"
)

func voiceJournalChangeCount(t *testing.T, journal *VoiceJournal) int64 {
	t.Helper()
	var count int64
	// The journal owns a single SQLite connection, so this counts real writes
	// across each completed transaction without instrumenting production code.
	if err := journal.db.QueryRowContext(t.Context(), `SELECT total_changes()`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestVoiceJournalUnchangedSpeechProgressDoesNotWrite(t *testing.T) {
	t.Parallel()
	journal, _, binding := voiceJournalFixture(t)
	source := voiceJournalDelivery(binding)
	if err := journal.CreateSpeechDelivery(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	item := SpeechDeliveryItem{ResponseID: "response", ItemID: "item", GeneratedSamples: 480, QueuedSamples: 480, PlayedSamples: 240}
	transcript := "fixture-862"
	update := SpeechDeliveryUpdate{Epoch: binding.Epoch, PresentationGeneration: source.PresentationGeneration, Item: &item, GeneratedTranscript: &transcript}
	apply := func(writes int64) SpeechDelivery {
		t.Helper()
		before := voiceJournalChangeCount(t, journal)
		got, err := journal.UpdateSpeechDelivery(t.Context(), source.ID, update)
		if err != nil {
			t.Fatal(err)
		}
		if changed := voiceJournalChangeCount(t, journal) - before; changed != writes {
			t.Errorf("speech update wrote %d rows, want %d", changed, writes)
		}
		stored, err := journal.SpeechDelivery(t.Context(), source.ID)
		if err != nil || !reflect.DeepEqual(got, stored) {
			t.Fatalf("returned progress differs from durable progress: %+v %+v %v", got, stored, err)
		}
		return got
	}
	apply(1)
	apply(0)
	item.PlayedSamples = 480
	apply(1)
	apply(0)
	item.PlaybackComplete = true
	apply(1)
	apply(0)
	item.PlaybackComplete = false
	if got := apply(0); !got.Items[0].PlaybackComplete {
		t.Fatal("duplicate acknowledgement erased rendering completion")
	}
	update.OutputComplete = true
	apply(1)
	apply(0)
	transcript += "-tail"
	apply(1)
	apply(0)
	update.Interrupted = true
	apply(1)
	apply(0)
	update.Interrupted, update.OutputComplete = false, false
	if got := apply(0); !got.Interrupted || !got.OutputComplete {
		t.Fatal("duplicate acknowledgement erased completion or interruption")
	}
	before := voiceJournalChangeCount(t, journal)
	update.Epoch += "-stale"
	if _, err := journal.UpdateSpeechDelivery(t.Context(), source.ID, update); !errors.Is(err, ErrVoiceStale) {
		t.Fatalf("unchanged values bypassed identity validation: %v", err)
	}
	update.Epoch = binding.Epoch
	item.PlayedSamples--
	if _, err := journal.UpdateSpeechDelivery(t.Context(), source.ID, update); !errors.Is(err, ErrVoiceConflict) {
		t.Fatalf("decreasing watermark bypassed validation: %v", err)
	}
	if voiceJournalChangeCount(t, journal) != before {
		t.Fatal("rejected receipt wrote progress")
	}
}

func TestVoiceJournalUnchangedSpeechProgressPersistsExpiry(t *testing.T) {
	t.Parallel()
	journal, _, binding, source := voiceJournalExpiredOwner(t)
	update := SpeechDeliveryUpdate{Epoch: binding.Epoch, PresentationGeneration: source.PresentationGeneration}
	before := voiceJournalChangeCount(t, journal)
	got, err := journal.UpdateSpeechDelivery(t.Context(), source.ID, update)
	if err != nil || !got.Interrupted {
		t.Fatalf("elapsed binding failed to interrupt delivery: %+v %v", got, err)
	}
	if changed := voiceJournalChangeCount(t, journal) - before; changed != 1 {
		t.Fatalf("expiry wrote %d rows, want 1", changed)
	}
	before = voiceJournalChangeCount(t, journal)
	if _, err := journal.UpdateSpeechDelivery(t.Context(), source.ID, update); err != nil {
		t.Fatal(err)
	}
	if voiceJournalChangeCount(t, journal) != before {
		t.Error("unchanged expired receipt rewrote delivery")
	}
	stored, err := journal.SpeechDelivery(t.Context(), source.ID)
	if err != nil || !stored.Interrupted {
		t.Fatalf("expiry interruption was not durable: %+v %v", stored, err)
	}
}
