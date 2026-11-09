package hub

import (
	"fmt"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// Native admission tests prove that recovery rejects speech with an interrupt
// for the incoming generation. This real hub/daemon test proves the other side:
// that control releases later output without replay or another user input.
func TestE2E_VoiceRecoveryRetiresUnplayedReplyBeforeFreshUpdate(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	const input = "AUDIO-RECOVERY-INPUT-81"
	const unavailable = "AUDIO-RECOVERY-UNAVAILABLE-83"
	const update = "AUDIO-RECOVERY-FRESH-89"
	f.admit(t, input, false)
	request := f.next(t)
	active := f.read(t).Thread.Evener.ActiveTurnID
	if active == "" {
		t.Fatal("manager work did not start before audio recovery")
	}
	// Pause retires the previous local generation before the manager replies.
	f.device.send(`{"type":"interrupt","generation":1,"playedSamples":{}}`)
	paused := voiceJourneyEvent(t, f.device, "stop")
	args := communicateArgs(unavailable)
	args["end_turn"] = false
	request.RespondToolCall("communicate", args)
	held := f.next(t)
	start := voiceJourneyEvent(t, f.device, "speech_start")
	peer := f.openSpeaker(t)
	old := f.speechRequest(t, peer, start, unavailable)
	if float64(old.PresentationGeneration) <= paused["generation"].(float64) {
		t.Fatal("reply did not arrive beyond the paused device fence")
	}
	voiceCorrectionAudio(peer, old, "recovery-old-response", "recovery-old-item", "AwAEAA==")
	voiceCorrectionDone(peer, old, "recovery-old-response", "recovery-old-item")
	f.expectFinishedAudio(t, old, "AwAEAA==")
	// Audio arrived on the socket but the recovering device never rendered it.
	// Repeating the prior pause cannot retire this newer incoming delivery.
	f.device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{}}`, old.PresentationGeneration-1))
	f.device.send(`{"type":"renew"}`)
	if event := f.device.read(); event["type"] != "renewed" {
		t.Fatalf("stale pause changed the active delivery: %v", event["type"])
	}
	before := f.delivery(t, old.ID)
	if before.Interrupted || !before.OutputComplete || len(before.Items) != 1 || before.Items[0].PlayedSamples != 0 || before.Items[0].PlaybackComplete {
		t.Fatalf("fixture did not reach completed but unplayed speech: %+v", before)
	}
	if f.read(t).Thread.Evener.ActiveTurnID != active {
		t.Fatal("audio recovery stopped manager work")
	}
	select {
	case <-held.Cancelled():
		t.Fatal("audio recovery cancelled the manager request")
	default:
	}
	held.RespondToolCall("communicate", communicateArgs(update))
	committed := f.waitRead(t, "fresh update committed without new input", func(read appwire.ThreadReadResponse) bool {
		return read.Thread.Evener.ActiveTurnID == "" && voiceCorrectionItem(read, update).ID != ""
	})
	source := voiceCorrectionItem(committed, update)
	// Recovery rejection must name the incoming delivery, with no played samples.
	f.device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{}}`, old.PresentationGeneration))
	stop := voiceJourneyEvent(t, f.device, "stop")
	if stop["generation"].(float64) <= float64(old.PresentationGeneration) {
		t.Fatal("recovery rejection did not fence the unavailable reply")
	}
	f.waitDelivery(t, old.ID, func(d hubcore.SpeechDelivery) bool { return d.Interrupted })
	truncate := peer.read()
	if truncate["type"] != "conversation.item.truncate" || truncate["item_id"] != "recovery-old-item" || truncate["audio_end_ms"] != float64(0) {
		t.Fatalf("unplayed recovery reply was not truncated to zero: %v", truncate)
	}
	peer.send(`{"type":"conversation.item.truncated","item_id":"recovery-old-item","content_index":0,"audio_end_ms":0}`)
	freshStart := voiceJourneyEvent(t, f.device, "speech_start")
	fresh := f.speechRequest(t, peer, freshStart, update)
	if fresh.CanonicalSourceItemID != source.ID || fresh.PresentationGeneration <= old.PresentationGeneration {
		t.Fatal("recovery replayed the unavailable reply or lost the fresh update")
	}
	voiceCorrectionAudio(peer, fresh, "recovery-fresh-response", "recovery-fresh-item", "AQACAA==")
	voiceCorrectionDelta(peer, "recovery-old-response", "recovery-old-item", "//8=")
	voiceCorrectionDone(peer, fresh, "recovery-fresh-response", "recovery-fresh-item")
	f.expectFinishedAudio(t, fresh, "AQACAA==")
	f.device.send(fmt.Sprintf(`{"type":"played","deliveryId":%q,"generation":%d,"itemId":"recovery-fresh-item","samples":2,"complete":true}`, fresh.ID, fresh.PresentationGeneration))
	f.waitDelivery(t, fresh.ID, func(d hubcore.SpeechDelivery) bool {
		return d.OutputComplete && !d.Interrupted && len(d.Items) == 1 && d.Items[0].PlaybackComplete && d.Items[0].PlayedSamples == 2
	})
	if voiceCorrectionItem(committed, unavailable).ID != old.CanonicalSourceItemID {
		t.Fatal("recovery lost the unavailable reply's canonical transcript")
	}
	deliveries, _, err := f.journal.ListSpeechDeliveries(f.ctx, f.armed.BindingID, 0, 20)
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("recovery created missing or duplicate deliveries: count=%d err=%v", len(deliveries), err)
	}
}
