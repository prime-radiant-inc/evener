package hub

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

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

func TestVoicePlaybackFenceRejectsUnrepresentableGeneration(t *testing.T) {
	bridge := &voiceBridge{generation: 1}
	if err := bridge.interruptSpeech(voiceControl{Generation: 1 << 53}); !errors.Is(err, hubcore.ErrVoiceInvalid) {
		t.Fatalf("unsafe wire generation accepted: %v", err)
	}
	if bridge.generation != 1 {
		t.Fatal("invalid generation changed the current playback fence")
	}
}
