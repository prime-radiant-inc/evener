package hub

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestE2E_VoiceSpeechReachesOlderHistoryPages(t *testing.T) {
	for _, reconnect := range []bool{false, true} {
		name := "playing"
		if reconnect {
			name = "reconnect_fence"
		}
		t.Run(name, func(t *testing.T) {
			// Each case builds its own provider, hub and voice binding, and
			// reaches no process-wide state, so the two journeys overlap
			// instead of paying two full stack startups one after another.
			t.Parallel()
			f := newVoiceCorrectionStack(t)
			var ready map[string]any
			var peer realtimePeer
			var first hubcore.SpeechDelivery
			if reconnect {
				_ = f.device.conn.CloseNow()
				ready = f.connect(t)
				// The reconnect baseline precedes all output below. The device
				// has not acknowledged its fence, so no source can speak yet.
			}
			f.admit(t, "PAGED-INPUT-163", false)
			message := communicateArgs("PAGED-FIRST-167")
			message["end_turn"] = false
			f.next(t).RespondToolCall("communicate", message)
			if !reconnect {
				start := voiceJourneyEvent(t, f.device, "speech_start")
				peer = f.openSpeaker(t)
				first = f.speechRequest(t, peer, start, "PAGED-FIRST-167")
				voiceCorrectionAudio(peer, first, "paged-first-response", "paged-first-audio", "AQACAA==")
				voiceCorrectionDone(peer, first, "paged-first-response", "paged-first-audio")
				f.expectFinishedAudio(t, first, "AQACAA==")
				// Withhold only the device's completion, not the manager's work.
			}
			message = communicateArgs("PAGED-UNSAID-173")
			message["end_turn"] = false
			f.next(t).RespondToolCall("communicate", message)
			for range 2*appwire.TranscriptItemPageLimit + 1 {
				f.next(t).RespondToolCall("read_file", map[string]any{"file_path": filepath.Join(f.stack.workDir, "NOTES.md"), "intent": "Reading the fixture to advance real history"})
			}
			f.next(t).RespondToolCall("communicate", communicateArgs("PAGED-LAST-179"))
			latest := f.waitRead(t, "paged output complete", func(read appwire.ThreadReadResponse) bool {
				return read.Thread.Evener.ActiveTurnID == "" && voiceCorrectionItem(read, "PAGED-LAST-179").ID != ""
			})
			if latest.OlderCursor == "" || voiceCorrectionItem(latest, "PAGED-UNSAID-173").ID != "" {
				t.Fatal("fixture did not push eligible unsaid output out of the latest page")
			}
			if latest.Authoritative || latest.BootGeneration == "" || latest.BootGeneration == appwire.DaemonlessBootGeneration {
				t.Fatal("fixture did not return the live daemon's non-replacing history read")
			}
			// Independently prove the source is in a later page under this same
			// daemon, epoch and snapshot, rather than testing a missing fixture.
			cursor := latest.OlderCursor
			pages := 1
			var unsaid appwire.ThreadItem
			for cursor != "" && unsaid.ID == "" {
				page, err := f.client.ThreadTurnsList(f.ctx, appwire.ThreadTurnsListParams{Ref: f.ref, Cursor: cursor, ItemLimit: appwire.TranscriptItemPageLimit})
				if err != nil {
					t.Fatal(err)
				}
				if page.BootGeneration != latest.BootGeneration || page.Epoch != latest.Epoch || page.Snapshot == nil || *page.Snapshot != *latest.Snapshot {
					t.Fatal("fixture pages did not share one settled history identity")
				}
				if page.Authoritative {
					t.Fatal("fixture did not return the live daemon's non-replacing history page")
				}
				unsaid = voiceCorrectionItem(appwire.ThreadReadResponse{Thread: appwire.Thread{Turns: page.Data}}, "PAGED-UNSAID-173")
				cursor = page.NextCursor
				pages++
			}
			if unsaid.ID == "" || unsaid.ConversationProvenance == nil {
				t.Fatal("older pages lack the real eligible unsaid source")
			}
			t.Logf("eligible source=%s is on page %d beyond the latest %d-item window, same snapshot=%+v boot=%s epoch=%d, live read and pages all Authoritative=false", unsaid.ID, pages, appwire.TranscriptItemPageLimit, *latest.Snapshot, latest.BootGeneration, latest.Epoch)
			if reconnect {
				voiceJourneyAcknowledgeReady(t, f.device, ready, 2)
			} else {
				f.device.send(fmt.Sprintf(`{"type":"played","deliveryId":%q,"generation":%d,"itemId":"paged-first-audio","samples":2,"complete":true}`, first.ID, first.PresentationGeneration))
			}
			start := voiceJourneyEvent(t, f.device, "speech_start")
			expected := "PAGED-UNSAID-173"
			if reconnect {
				expected = "PAGED-FIRST-167"
			}
			if start["canonicalText"] != expected {
				t.Fatalf("later-page source was skipped: got source=%v text=%v, want fixture=%s", start["sourceItemId"], start["canonicalText"], expected)
			}
			if reconnect {
				peer = f.openSpeaker(t)
			} else {
				truncate := peer.read()
				if truncate["type"] != "conversation.item.truncate" || truncate["item_id"] != "paged-first-audio" {
					t.Fatal("previous completed speech was not retired before the next source")
				}
				peer.send(`{"type":"conversation.item.truncated","item_id":"paged-first-audio","content_index":0,"audio_end_ms":0}`)
			}
			delivery := f.speechRequest(t, peer, start, expected)
			voiceCorrectionAudio(peer, delivery, "paged-next-response", "paged-next-audio", "AgADAA==")
			voiceCorrectionDone(peer, delivery, "paged-next-response", "paged-next-audio")
			f.expectFinishedAudio(t, delivery, "AgADAA==")
		})
	}
}

func TestE2E_VoiceHistorySnapshotAdvanceKeepsCursor(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	_ = f.device.conn.CloseNow()
	baseline := f.read(t)
	binding, err := f.journal.Binding(f.ctx, f.armed.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	bridge := voiceBridge{client: f.client, binding: binding}
	bridge.captureSpeechBaseline(baseline)
	before := appwire.ClonePointer(bridge.speechCursor)
	f.admit(t, "SNAPSHOT-INPUT-181", false)
	message := communicateArgs("SNAPSHOT-FIRST-191")
	message["end_turn"] = false
	f.next(t).RespondToolCall("communicate", message)
	for range appwire.TranscriptItemPageLimit + 1 {
		f.next(t).RespondToolCall("read_file", map[string]any{"file_path": filepath.Join(f.stack.workDir, "NOTES.md"), "intent": "Reading the fixture to advance real history"})
	}
	request := f.next(t)
	stale := f.read(t)
	if stale.OlderCursor == "" || voiceCorrectionItem(stale, "SNAPSHOT-FIRST-191").ID != "" {
		t.Fatal("fixture lacks an earlier unsaid page")
	}
	request.RespondToolCall("communicate", communicateArgs("SNAPSHOT-LAST-193"))
	current := f.waitRead(t, "history advanced before backfill", func(read appwire.ThreadReadResponse) bool {
		return read.Thread.Evener.ActiveTurnID == "" && voiceCorrectionItem(read, "SNAPSHOT-LAST-193").ID != ""
	})
	if *current.Snapshot == *stale.Snapshot {
		t.Fatal("real provider completion did not advance history")
	}
	if current.BootGeneration != stale.BootGeneration || current.Epoch != stale.Epoch || current.Snapshot.Incarnation != stale.Snapshot.Incarnation || current.Snapshot.Length <= stale.Snapshot.Length {
		t.Fatal("fixture did not isolate snapshot append from boot, epoch and incarnation replacement")
	}
	_, err = bridge.backfillSpeechHistory(f.ctx, stale, before)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("mixed snapshots were joined: %v", err)
	}
	data, ok := appwire.DecodedErrorData(wire.Data)
	if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptItemCursorStale || !reflect.DeepEqual(bridge.speechCursor, before) {
		t.Fatal("snapshot drift did not preserve a retryable cursor")
	}
	fresh, err := bridge.readSpeechHistory(f.ctx, bridge.history, before)
	if err != nil || voiceCorrectionItem(fresh, "SNAPSHOT-FIRST-191").ID == "" || *fresh.Snapshot != *current.Snapshot {
		t.Fatalf("fresh retry lost earlier unsaid output: %v", err)
	}
	t.Logf("same boot=%s epoch=%d incarnation, snapshot length advanced %d -> %d; mixed pages rejected without cursor movement and fresh read recovered the earlier source", current.BootGeneration, current.Epoch, stale.Snapshot.Length, current.Snapshot.Length)
}
