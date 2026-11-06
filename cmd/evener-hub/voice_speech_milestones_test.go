package hub

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// providerEvent must accumulate chunks in memory and flush the complete pending
// snapshot at milestones, including played receipts and disconnect retirement.
func TestE2E_VoiceSpeechCountersPersistOnlyAtMilestones(t *testing.T) {
	for _, terminal := range []string{"output_done", "interrupt", "disconnect"} {
		t.Run(terminal, func(t *testing.T) {
			f := newVoiceCorrectionStack(t)
			// Count actual INSERT/UPDATE writes in SQLite, not calls to a mocked
			// journal or the bridge's own bookkeeping.
			db, err := sql.Open("sqlite", filepath.Join(f.stack.home, "state", "evener", "voice", "journal.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			_, err = db.ExecContext(f.ctx, `CREATE TABLE speech_writes (n INTEGER NOT NULL);
INSERT INTO speech_writes VALUES (0);
CREATE TRIGGER count_speech_insert AFTER INSERT ON voice_speech BEGIN UPDATE speech_writes SET n=n+1; END;
CREATE TRIGGER count_speech_update AFTER UPDATE ON voice_speech BEGIN UPDATE speech_writes SET n=n+1; END;`)
			if err != nil {
				t.Fatal(err)
			}
			writes := 0
			checkWrites := func(boundary string, changed bool) {
				t.Helper()
				var count int
				if err := db.QueryRowContext(f.ctx, `SELECT n FROM speech_writes`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if (changed && count <= writes) || (!changed && count != writes) {
					t.Fatalf("%s: speech journal writes=%d, previous=%d, want changed=%v; persist counters only at durable milestones", boundary, count, writes, changed)
				}
				writes = count
			}
			const reply = "milestone-reply-631"
			f.admit(t, "milestone-input-631", false)
			f.next(t).RespondToolCall("communicate", communicateArgs(reply))
			start := voiceJourneyEvent(t, f.device, "speech_start")
			peer := f.openSpeaker(t)
			delivery := f.speechRequest(t, peer, start, reply)
			checkWrites("delivery start", true)
			const response, first, second = "milestone-response", "milestone-first", "milestone-second"
			const samples = 240
			pcm := base64.StdEncoding.EncodeToString(make([]byte, samples*2))
			voiceCorrectionAudio(peer, delivery, response, first, pcm)
			voiceJourneyEvent(t, f.device, "audio")
			checkWrites("first item", true)
			assertStored := func(text string, items []hubcore.SpeechDeliveryItem, complete, interrupted bool) {
				t.Helper()
				d := f.delivery(t, delivery.ID)
				if d.GeneratedTranscript != text || !reflect.DeepEqual(d.Items, items) || d.OutputComplete != complete || d.Interrupted != interrupted {
					t.Fatalf("durable delivery lost milestone evidence: %+v, want transcript=%q items=%+v complete=%v interrupted=%v", d, text, items, complete, interrupted)
				}
			}
			firstItem := hubcore.SpeechDeliveryItem{ResponseID: response, ItemID: first, GeneratedSamples: samples, QueuedSamples: samples}
			assertStored("", []hubcore.SpeechDeliveryItem{firstItem}, false, false)
			audio := func(item string) {
				t.Helper()
				voiceCorrectionDelta(peer, response, item, pcm)
				got := voiceJourneyEvent(t, f.device, "audio")
				if got["itemId"] != item || got["pcm"] != pcm {
					t.Fatal("chunk did not reach the device")
				}
			}
			transcript := func(item, text string) {
				t.Helper()
				peer.send(fmt.Sprintf(`{"type":"response.output_audio_transcript.delta","response_id":%q,"item_id":%q,"content_index":0,"delta":%q}`, response, item, text))
				got := voiceJourneyEvent(t, f.device, "speech_text")
				if got["itemId"] != item || got["text"] != text {
					t.Fatal("transcript delta did not reach the device")
				}
			}
			var transcriptText strings.Builder
			for i := range 3 {
				audio(first)
				checkWrites("intermediate audio chunk", false)
				delta := fmt.Sprintf("segment-%d|", i)
				transcript(first, delta)
				transcriptText.WriteString(delta)
				checkWrites("intermediate transcript chunk", false)
			}
			text := transcriptText.String()
			assertStored("", []hubcore.SpeechDeliveryItem{firstItem}, false, false)
			peer.send(fmt.Sprintf(`{"type":"response.output_item.added","response_id":%q,"item":{"id":%q,"type":"message"}}`, response, second))
			audio(second)
			checkWrites("new item flushes earlier chunks", true)
			firstItem.GeneratedSamples, firstItem.QueuedSamples = samples*4, samples*4
			secondItem := hubcore.SpeechDeliveryItem{ResponseID: response, ItemID: second, GeneratedSamples: samples, QueuedSamples: samples}
			assertStored(text, []hubcore.SpeechDeliveryItem{firstItem, secondItem}, false, false)
			audio(first)
			audio(second)
			peer.send(fmt.Sprintf(`{"type":"response.output_audio_transcript.done","response_id":%q,"item_id":%q,"content_index":0,"transcript":%q}`, response, first, text+"tail|"))
			if got := voiceJourneyEvent(t, f.device, "speech_text"); got["text"] != "tail|" {
				t.Fatal("transcript completion lost its unsent suffix")
			}
			text += "tail|"
			checkWrites("audio and transcript completion chunks", false)
			// This played watermark exceeds the last persisted generated count.
			// Receipt validation must use the buffered counters, not stale SQLite.
			firstItem.GeneratedSamples, firstItem.QueuedSamples, firstItem.PlayedSamples = samples*5, samples*5, samples*5
			secondItem.GeneratedSamples, secondItem.QueuedSamples = samples*2, samples*2
			f.device.send(fmt.Sprintf(`{"type":"played","deliveryId":%q,"generation":%d,"itemId":%q,"samples":%d}`, delivery.ID, delivery.PresentationGeneration, first, firstItem.PlayedSamples))
			f.device.send(`{"type":"renew"}`)
			voiceJourneyEvent(t, f.device, "renewed")
			checkWrites("played receipt", true)
			assertStored(text, []hubcore.SpeechDeliveryItem{firstItem, secondItem}, false, false)
			audio(second)
			transcript(second, "second-tail|")
			text += "second-tail|"
			checkWrites("chunks after played receipt", false)
			secondItem.GeneratedSamples, secondItem.QueuedSamples = samples*3, samples*3
			switch terminal {
			case "output_done":
				voiceCorrectionDone(peer, delivery, response, first)
				for _, item := range []string{first, second} {
					if got := voiceJourneyEvent(t, f.device, "speech_end"); got["itemId"] != item {
						t.Fatal("output completion lost an audio item")
					}
				}
				voiceJourneyEvent(t, f.device, "delivery_end")
				checkWrites("provider output_done", true)
				assertStored(text, []hubcore.SpeechDeliveryItem{firstItem, secondItem}, true, false)
				for _, item := range []hubcore.SpeechDeliveryItem{firstItem, secondItem} {
					f.device.send(fmt.Sprintf(`{"type":"played","deliveryId":%q,"generation":%d,"itemId":%q,"samples":%d,"complete":true}`, delivery.ID, delivery.PresentationGeneration, item.ItemID, item.QueuedSamples))
				}
				f.device.send(`{"type":"renew"}`)
				voiceJourneyEvent(t, f.device, "renewed")
				checkWrites("playback completion receipts", true)
				firstItem.PlaybackComplete, secondItem.PlaybackComplete = true, true
				secondItem.PlayedSamples = secondItem.QueuedSamples
				assertStored(text, []hubcore.SpeechDeliveryItem{firstItem, secondItem}, true, false)
			case "interrupt":
				f.device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{}}`, delivery.PresentationGeneration))
				voiceJourneyEvent(t, f.device, "stop")
				checkWrites("interrupt", true)
				assertStored(text, []hubcore.SpeechDeliveryItem{firstItem, secondItem}, false, true)
				// An acknowledgement arriving after retirement must not overwrite
				// buffered counts or remove the interruption evidence.
				secondItem.PlayedSamples = samples * 2
				f.device.send(fmt.Sprintf(`{"type":"played","deliveryId":%q,"generation":%d,"itemId":%q,"samples":%d}`, delivery.ID, delivery.PresentationGeneration, second, secondItem.PlayedSamples))
				f.device.send(`{"type":"renew"}`)
				voiceJourneyEvent(t, f.device, "renewed")
				checkWrites("late played receipt", true)
				assertStored(text, []hubcore.SpeechDeliveryItem{firstItem, secondItem}, false, true)
			case "disconnect":
				if err := f.device.conn.CloseNow(); err != nil {
					t.Fatal(err)
				}
				f.waitDelivery(t, delivery.ID, func(d hubcore.SpeechDelivery) bool { return d.Interrupted })
				checkWrites("disconnect retirement", true)
				assertStored(text, []hubcore.SpeechDeliveryItem{firstItem, secondItem}, false, true)
			}
		})
	}
}
