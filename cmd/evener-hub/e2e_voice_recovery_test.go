package hub

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// Renewal must extend the authenticated connection's timer as well as storage.
// A second real input after the original deadline proves the old timer retired.
func TestE2E_VoiceRenewalPreservesConnection(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	binding, err := f.journal.Binding(f.ctx, f.armed.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.device.conn.CloseNow()
	// Seed a short real grant through the journal's ordinary binding contract.
	// Production Arm's twenty-minute grant would make this deadline test slow.
	binding.ID, binding.Epoch = newMutationID(t), newMutationID(t)
	binding.ExpiresAt = time.Now().UTC().Add(2 * time.Second)
	if err := f.journal.CreateBinding(f.ctx, binding, true); err != nil {
		t.Fatal(err)
	}
	f.headers.Set("X-Evener-Voice-Binding", binding.ID)
	f.headers.Set("X-Evener-Voice-Epoch", binding.Epoch)
	ready := f.connect(t)
	if ready["bindingId"] != binding.ID || ready["epoch"] != binding.Epoch {
		t.Fatal("short-grant connection did not establish its binding")
	}
	voiceJourneyAcknowledgeReady(t, f.device, ready, 0)
	voiceJourneyEvent(t, f.device, "stop")
	f.device.send(`{"type":"renew"}`)
	renewed := voiceJourneyEvent(t, f.device, "renewed")
	expires, err := time.Parse(time.RFC3339Nano, renewed["expiresAt"].(string))
	if err != nil || !expires.After(binding.ExpiresAt) {
		t.Fatal("renewal did not extend the original expiry")
	}
	stored, err := f.journal.Binding(f.ctx, binding.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := binding
	expected.ExpiresAt = expires
	if stored != expected {
		t.Fatal("renewal changed binding authority or did not persist its expiry")
	}
	deadline := time.NewTimer(time.Until(binding.ExpiresAt.Add(time.Millisecond)))
	defer deadline.Stop()
	select {
	case <-deadline.C:
	case <-f.ctx.Done():
		t.Fatal("original grant deadline was not reached")
	}
	f.device.send(`{"type":"input_start","sequence":1}`)
	peer := f.openSpeaker(t)
	if peer.read()["type"] != "input_audio_buffer.clear" {
		t.Fatal("renewed connection did not admit another utterance")
	}
	peer.send(`{"type":"input_audio_buffer.cleared"}`)
	started := voiceJourneyEvent(t, f.device, "input_started")
	turnID := started["turnId"].(string)
	voiceBargeInterpret(t, peer, f.device, turnID, "RENEWED-REQUEST-43")
	voiceJourneyEvent(t, f.device, "intent")
	intent, err := f.journal.Intent(f.ctx, turnID)
	if err != nil || intent.State != hubcore.VoiceIntentAccepted || intent.ExpectedInstanceID != binding.InstanceID {
		t.Fatalf("renewed connection lost original manager custody: %+v %v", intent, err)
	}
	if !f.next(t).Contains("RENEWED-REQUEST-43") {
		t.Fatal("manager did not receive the post-deadline input")
	}
}

func TestE2E_VoiceRenewalDoesNotReviveRetiredGrant(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	f.admit(t, "RETIRED-GRANT-WORK-47", false)
	held := f.next(t)
	active := f.read(t).Thread.Evener.ActiveTurnID
	if active == "" {
		t.Fatal("manager work did not start")
	}
	before, err := f.journal.Binding(f.ctx, f.armed.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.journal.RetireBinding(f.ctx, before.ID, before.Epoch); err != nil {
		t.Fatal(err)
	}
	f.device.send(`{"type":"renew"}`)
	voiceJourneyEvent(t, f.device, "error")
	closed, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	_, _, err = f.device.conn.Read(closed)
	if err == nil || closed.Err() != nil {
		t.Fatal("revoked grant left the voice connection authorized")
	}
	after, err := f.journal.Binding(f.ctx, before.ID)
	if err != nil || after.Active || !after.ExpiresAt.Equal(before.ExpiresAt) {
		t.Fatal("renewal revived or extended the retired binding")
	}
	if f.read(t).Thread.Evener.ActiveTurnID != active {
		t.Fatal("grant retirement stopped manager work")
	}
	select {
	case <-held.Cancelled():
		t.Fatal("grant retirement cancelled the manager's request")
	default:
	}
}

func TestE2E_VoiceReconnectSynchronizesPlaybackFence(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	f.admit(t, "BEFORE-RECONNECT-INPUT-51", false)
	f.next(t).RespondToolCall("communicate", communicateArgs("BEFORE-RECONNECT-OUTPUT-51"))
	oldStart := voiceJourneyEvent(t, f.device, "speech_start")
	oldPeer := f.openSpeaker(t)
	old := f.speechRequest(t, oldPeer, oldStart, "BEFORE-RECONNECT-OUTPUT-51")
	voiceCorrectionAudio(oldPeer, old, "before-reconnect-response", "before-reconnect-item", "AwAEAA==")
	voiceJourneyEvent(t, f.device, "audio")
	_ = f.device.conn.CloseNow()
	f.waitDelivery(t, old.ID, func(d hubcore.SpeechDelivery) bool { return d.Interrupted })
	ready := f.connect(t)
	minimum := old.PresentationGeneration + 1
	if ready["generation"].(float64) >= float64(minimum) {
		t.Fatal("reconnect fixture did not need to synchronize its local fence")
	}
	// Native retains this local watermark across connection loss and sends it
	// before accepting any output from the replacement control stream.
	f.device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{}}`, minimum-1))
	syncCtx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	syncPeer := f.device
	syncPeer.ctx = syncCtx
	stop := voiceJourneyEvent(t, syncPeer, "stop")
	if stop["generation"].(float64) < float64(minimum) {
		t.Fatal("reconnect left the bridge behind the phone's playback fence")
	}
	f.admit(t, "RECONNECT-INPUT-53", false)
	request := f.next(t)
	if !request.Contains("BEFORE-RECONNECT-INPUT-51") || !request.Contains("RECONNECT-INPUT-53") {
		t.Fatal("reconnected manager lost the earlier conversation")
	}
	request.RespondToolCall("communicate", communicateArgs("RECONNECT-OUTPUT-53"))
	start := voiceJourneyEvent(t, f.device, "speech_start")
	peer := f.openSpeaker(t)
	delivery := f.speechRequest(t, peer, start, "RECONNECT-OUTPUT-53")
	if delivery.PresentationGeneration < minimum {
		t.Fatal("fresh reply is inaudible behind the retained native fence")
	}
	voiceCorrectionAudio(peer, delivery, "reconnected-response", "reconnected-item", "AQACAA==")
	voiceCorrectionDone(peer, delivery, "reconnected-response", "reconnected-item")
	f.expectFinishedAudio(t, delivery, "AQACAA==")
}

func TestE2E_VoiceReconnectRetainsOutputBeforeDelayedFence(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	f.admit(t, "DELAYED-FENCE-INPUT-71", false)
	args := communicateArgs("DELAYED-FENCE-OLD-71")
	args["end_turn"] = false
	f.next(t).RespondToolCall("communicate", args)
	held := f.next(t)
	active := f.read(t).Thread.Evener.ActiveTurnID
	oldStart := voiceJourneyEvent(t, f.device, "speech_start")
	oldPeer := f.openSpeaker(t)
	old := f.speechRequest(t, oldPeer, oldStart, "DELAYED-FENCE-OLD-71")
	if old.PresentationGeneration != 2 || active == "" {
		t.Fatal("fixture did not reach durable generation 2 with manager work running")
	}
	voiceCorrectionAudio(oldPeer, old, "delayed-fence-old-response", "delayed-fence-old-item", "AwAEAA==")
	voiceJourneyEvent(t, f.device, "audio")
	_ = f.device.conn.CloseNow()
	f.waitDelivery(t, old.ID, func(d hubcore.SpeechDelivery) bool { return d.Interrupted })
	ready := f.connect(t)
	minimum := old.PresentationGeneration + 1
	if ready["generation"].(float64) >= float64(minimum) {
		t.Fatal("reconnect fixture did not need the retained device fence")
	}
	if f.read(t).Thread.Evener.ActiveTurnID != active {
		t.Fatal("reconnect stopped the manager's existing work")
	}
	select {
	case <-held.Cancelled():
		t.Fatal("socket loss cancelled the held manager request")
	default:
	}
	// Ready proves the reconnect baseline was captured. The real manager commits
	// this response before the device sends its normal ready-triggered interrupt.
	held.RespondToolCall("communicate", communicateArgs("DELAYED-FENCE-FRESH-73"))
	committed := f.waitRead(t, "fresh output committed before device fence", func(read appwire.ThreadReadResponse) bool {
		return read.Thread.Evener.ActiveTurnID == "" && voiceCorrectionItem(read, "DELAYED-FENCE-FRESH-73").ID != ""
	})
	source := voiceCorrectionItem(committed, "DELAYED-FENCE-FRESH-73")
	t.Logf("delayed fence reached: durable generation=%d, reconnect ready=%v, fresh source=%s committed on original instance before interrupt", old.PresentationGeneration, ready["generation"], source.ID)
	f.device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{}}`, minimum-1))
	stop := voiceJourneyEvent(t, f.device, "stop")
	if stop["generation"].(float64) < float64(minimum) {
		t.Fatal("reconnect did not reconcile the retained fence")
	}
	start := voiceJourneyEvent(t, f.device, "speech_start")
	peer := f.openSpeaker(t)
	fresh := f.speechRequest(t, peer, start, "DELAYED-FENCE-FRESH-73")
	if fresh.CanonicalSourceItemID != source.ID || fresh.PresentationGeneration <= old.PresentationGeneration || fresh.PresentationGeneration < uint64(stop["generation"].(float64)) {
		t.Fatal("fresh output was lost, replayed, or allocated behind the fence")
	}
	// A duplicated ready acknowledgement is stale once fresh speech has started.
	// Renewal is an ordered control barrier before the provider emits any audio.
	f.device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{}}`, minimum-1))
	f.device.send(`{"type":"renew"}`)
	if event := f.device.read(); event["type"] != "renewed" || f.delivery(t, fresh.ID).Interrupted {
		t.Fatalf("duplicate ready acknowledgement retired fresh speech: %v", event["type"])
	}
	voiceCorrectionAudio(peer, fresh, "delayed-fence-fresh-response", "delayed-fence-fresh-item", "AQACAA==")
	voiceCorrectionDone(peer, fresh, "delayed-fence-fresh-response", "delayed-fence-fresh-item")
	f.expectFinishedAudio(t, fresh, "AQACAA==")
}

func TestE2E_VoiceReconnectAfterRepeatedInterruptions(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	minimum, durable := uint64(1), uint64(0)
	for cycle := range 3 {
		input, output := fmt.Sprintf("RESTART-INPUT-%d", cycle), fmt.Sprintf("RESTART-OUTPUT-%d", cycle)
		f.admit(t, input, false)
		held := f.next(t)
		active := f.read(t).Thread.Evener.ActiveTurnID
		if active == "" || !held.Contains(input) {
			t.Fatal("manager did not start the new request before socket replacement")
		}
		for range 2 {
			f.device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{}}`, minimum))
			minimum++
			if stop := voiceJourneyEvent(t, f.device, "stop"); stop["generation"] != float64(minimum) {
				t.Fatal("local interruption did not advance its retained fence")
			}
		}
		minimum++ // Native also retires playback on socket loss.
		previous := f.device
		// A new authenticated socket replaces a half-open predecessor. Ready must
		// follow its shutdown even when its provider retirement is unacknowledged.
		ready := f.connect(t)
		if ready["generation"] != float64(max(uint64(1), durable)) {
			t.Fatalf("replacement lost the durable allocation floor: %v", ready["generation"])
		}
		closed, cancel := context.WithTimeout(f.ctx, 2*time.Second)
		_, _, err := previous.conn.Read(closed)
		timedOut := closed.Err() != nil
		cancel()
		if err == nil || timedOut {
			t.Fatal("replacement left its preceding device socket alive")
		}
		voiceJourneyAcknowledgeReady(t, f.device, ready, minimum)
		if stop := voiceJourneyEvent(t, f.device, "stop"); stop["generation"] != float64(minimum) {
			t.Fatal("replacement did not retain the higher local fence")
		}
		if f.read(t).Thread.Evener.ActiveTurnID != active {
			t.Fatal("socket replacement stopped or replaced manager work")
		}
		select {
		case <-held.Cancelled():
			t.Fatal("socket replacement cancelled the manager request")
		default:
		}
		held.RespondToolCall("communicate", communicateArgs(output))
		f.waitRead(t, "new output committed and manager settled", func(read appwire.ThreadReadResponse) bool {
			return read.Thread.Evener.ActiveTurnID == "" && voiceCorrectionItem(read, output).ID != ""
		})
		start := voiceJourneyEvent(t, f.device, "speech_start")
		peer := f.openSpeaker(t)
		delivery := f.speechRequest(t, peer, start, output)
		if delivery.PresentationGeneration != minimum+1 {
			t.Fatal("new delivery reused a generation or remained behind the device fence")
		}
		voiceCorrectionAudio(peer, delivery, "restart-response", "restart-item", "AQACAA==")
		voiceCorrectionDone(peer, delivery, "restart-response", "restart-item")
		f.expectFinishedAudio(t, delivery, "AQACAA==")
		f.device.send(fmt.Sprintf(`{"type":"played","deliveryId":%q,"generation":%d,"itemId":"restart-item","samples":2,"complete":true}`, delivery.ID, delivery.PresentationGeneration))
		f.waitDelivery(t, delivery.ID, func(d hubcore.SpeechDelivery) bool {
			return d.OutputComplete && !d.Interrupted && len(d.Items) == 1 && d.Items[0].PlaybackComplete
		})
		minimum, durable = delivery.PresentationGeneration, delivery.PresentationGeneration
		// With no retained client state, the ready acknowledgement is below the
		// durable floor. It must still release the gate without lowering that floor.
		ready = f.connect(t)
		if ready["generation"] != float64(durable) {
			t.Fatal("reconnect lost the completed delivery's generation")
		}
		voiceJourneyAcknowledgeReady(t, f.device, ready, 0)
		if stop := voiceJourneyEvent(t, f.device, "stop"); stop["generation"] != float64(durable) {
			t.Fatal("acknowledgement below the durable floor failed to release the gate")
		}
	}
	deliveries, _, err := f.journal.ListSpeechDeliveries(f.ctx, f.armed.BindingID, 0, 20)
	if err != nil || len(deliveries) != 3 {
		t.Fatalf("reconnect created duplicate or missing speech: count=%d err=%v", len(deliveries), err)
	}
}

// A replaced device socket retires its subscription while the successor's
// baseline read is on the relay, and the relay reports that cut as a
// retryable outage. The successor must read again, become ready and speak.
func TestE2E_VoiceReconnectRetriesRelayOutageBeforeReady(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	var cut atomic.Bool
	voiceStopRecoveryHub(t, f, nil, false, func(ctx context.Context, params appwire.ThreadReadParams) error {
		if bridge, _ := ctx.Value(voicePresentationContextKey{}).(bool); bridge && params.Subscribe && cut.CompareAndSwap(false, true) {
			return appwire.RetryableSessionUnavailable("relay connection ended before route publication")
		}
		return nil
	})
	if !cut.Load() {
		t.Fatal("the bridge's baseline read never met the relay outage")
	}
	f.admit(t, "RELAY-CUT-INPUT-91", false)
	f.next(t).RespondToolCall("communicate", communicateArgs("RELAY-CUT-OUTPUT-91"))
	start := voiceJourneyEvent(t, f.device, "speech_start")
	f.speechRequest(t, f.openSpeaker(t), start, "RELAY-CUT-OUTPUT-91")
}

func TestVoicePlaybackFenceRejectsUnrepresentableGeneration(t *testing.T) {
	bridge := &voiceBridge{generation: 1}
	if err := bridge.interruptSpeech(voiceControl{Generation: 1 << 53}); !errors.Is(err, hubcore.ErrVoiceInvalid) {
		t.Fatalf("unsafe wire generation accepted: %v", err)
	}
	if bridge.generation != 1 {
		t.Fatal("invalid generation changed the current playback fence")
	}
}

func TestVoicePlaybackFenceRejectsUnrepresentableReconnectGeneration(t *testing.T) {
	for _, generation := range []uint64{voiceMaxGeneration, 1 << 53, ^uint64(0)} {
		bridge := &voiceBridge{generation: 9, awaitingFence: true}
		if err := bridge.interruptSpeech(voiceControl{Generation: generation}); !errors.Is(err, hubcore.ErrVoiceInvalid) {
			t.Fatalf("unsafe reconnect generation %d accepted: %v", generation, err)
		}
		if bridge.generation != 9 || !bridge.awaitingFence {
			t.Fatal("invalid device generation changed the durable floor or released selection")
		}
	}
}
