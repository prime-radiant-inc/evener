package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// Native speech onset sends interrupt then input_start without waiting for the
// provider. The hub must preserve that input while retiring the old presentation.
func TestE2E_VoiceSpokenCorrectionKeepsManagerWork(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	const alpha = "BARGE-ALPHA-INPUT-17"
	const alphaReply = "BARGE-ALPHA-OUTPUT-17"
	const beta = "BARGE-BETA-INPUT-29"
	const betaReply = "BARGE-BETA-OUTPUT-29"
	const lateAlpha = "BARGE-ALPHA-LATE-OUTPUT-17"
	f.admit(t, alpha, false)
	request := f.next(t)
	args := communicateArgs(alphaReply)
	args["end_turn"] = false
	request.RespondToolCall("communicate", args)
	held := f.next(t)
	start := voiceJourneyEvent(t, f.device, "speech_start")
	peer := f.openSpeaker(t)
	old := f.speechRequest(t, peer, start, alphaReply)
	const oldResponse, oldItem = "barge-alpha-response", "barge-alpha-item"
	voiceCorrectionAudio(peer, old, oldResponse, oldItem, base64.StdEncoding.EncodeToString(make([]byte, 960)))
	audio := voiceJourneyEvent(t, f.device, "audio")
	if audio["deliveryId"] != old.ID {
		t.Fatal("old speech did not reach playback before correction")
	}
	running := f.read(t)
	if running.Thread.Evener.ActiveTurnID == "" {
		t.Fatal("manager work was not active before acoustic interruption")
	}
	// The watermark rides the interruption itself, independently of periodic
	// played receipts. The new utterance starts before provider cancellation ACK.
	f.device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{%q:240}}`, old.PresentationGeneration, oldItem))
	f.device.send(`{"type":"input_start","sequence":1}`)
	stop := voiceJourneyEvent(t, f.device, "stop")
	if stop["generation"].(float64) <= float64(old.PresentationGeneration) {
		t.Fatal("native interruption did not fence old audio")
	}
	f.waitDelivery(t, old.ID, func(d hubcore.SpeechDelivery) bool {
		return d.Interrupted && len(d.Items) == 1 && d.Items[0].PlayedSamples == 240
	})
	cancel := peer.read()
	if cancel["type"] != "response.cancel" || cancel["response_id"] != oldResponse {
		t.Fatalf("native interruption did not cancel its provider response: %v", cancel)
	}
	peer.send(fmt.Sprintf(`{"type":"response.done","response":{"id":%q,"status":"cancelled","metadata":{"delivery_id":%q,"generation":%q},"output":[]}}`, oldResponse, old.ID, strconv.FormatUint(old.PresentationGeneration, 10)))
	truncate := peer.read()
	if truncate["type"] != "conversation.item.truncate" || truncate["item_id"] != oldItem || truncate["audio_end_ms"] != float64(10) {
		t.Fatalf("native played watermark was not used for truncation: %v", truncate)
	}
	peer.send(fmt.Sprintf(`{"type":"conversation.item.truncated","item_id":%q,"content_index":0,"audio_end_ms":10}`, oldItem))
	voiceCorrectionDelta(peer, oldResponse, oldItem, "//8=")
	if peer.read()["type"] != "input_audio_buffer.clear" {
		t.Fatal("correction did not begin after retiring speech")
	}
	peer.send(`{"type":"input_audio_buffer.cleared"}`)
	started := voiceJourneyEvent(t, f.device, "input_started")
	turnID, _ := started["turnId"].(string)
	voiceBargeInterpret(t, peer, f.device, turnID, beta)
	accepted := voiceJourneyEvent(t, f.device, "intent")
	intent, err := f.journal.Intent(f.ctx, turnID)
	if err != nil || intent.Method != appwire.MethodTurnSteer || intent.State != hubcore.VoiceIntentAccepted || accepted["custody"] != hubcore.VoiceIntentAccepted || intent.Text != beta {
		t.Fatalf("spoken correction was not durably steered to working manager: %+v %v", intent, err)
	}
	after := f.read(t)
	if after.Thread.Evener.ActiveTurnID != running.Thread.Evener.ActiveTurnID {
		t.Fatal("acoustic interruption stopped manager work")
	}
	select {
	case <-held.Cancelled():
		t.Fatal("acoustic interruption cancelled manager model request")
	default:
	}
	args = communicateArgs(lateAlpha)
	args["end_turn"] = false
	held.RespondToolCall("communicate", args)
	freshRequest := f.next(t)
	if !freshRequest.Contains(beta) || !freshRequest.Contains(alpha) {
		t.Fatal("manager did not incorporate spoken correction in its existing context")
	}
	freshRequest.RespondToolCall("communicate", communicateArgs(betaReply))
	freshStart := voiceJourneyEvent(t, f.device, "speech_start")
	fresh := f.speechRequest(t, peer, freshStart, betaReply)
	voiceCorrectionAudio(peer, fresh, "barge-beta-response", "barge-beta-item", "AQACAA==")
	voiceCorrectionDelta(peer, oldResponse, oldItem, "//8=")
	voiceCorrectionDone(peer, fresh, "barge-beta-response", "barge-beta-item")
	f.expectFinishedAudio(t, fresh, "AQACAA==")
	final := f.waitRead(t, "corrected manager settled", func(read appwire.ThreadReadResponse) bool {
		return read.Thread.Evener.ActiveTurnID == "" && voiceCorrectionItem(read, betaReply).ID != ""
	})
	if voiceCorrectionItem(final, lateAlpha).ID == "" || voiceCorrectionItem(final, alphaReply).ID != old.CanonicalSourceItemID {
		t.Fatal("spoken correction lost prior canonical outputs")
	}
}

func TestE2E_VoiceBargeReplacesUnacknowledgedProvider(t *testing.T) {
	for _, phase := range []string{"session", "response"} {
		t.Run(phase, func(t *testing.T) {
			f := newVoiceCorrectionStack(t)
			const reply = "UNACKNOWLEDGED-REPLY-13"
			const correction = "UNACKNOWLEDGED-CORRECTION-23"
			f.admit(t, "UNACKNOWLEDGED-INPUT-13", false)
			args := communicateArgs(reply)
			args["end_turn"] = false
			f.next(t).RespondToolCall("communicate", args)
			held := f.next(t)
			start := voiceJourneyEvent(t, f.device, "speech_start")
			var peer realtimePeer
			if phase == "session" {
				select {
				case peer = <-f.peers:
				case <-f.ctx.Done():
					t.Fatal("speech provider did not connect")
				}
				if peer.read()["type"] != "session.update" {
					t.Fatal("speech provider did not begin configuration")
				}
			} else {
				peer = f.openSpeaker(t)
				f.speechRequest(t, peer, start, reply)
			}
			old := f.delivery(t, start["deliveryId"].(string))
			active := f.read(t).Thread.Evener.ActiveTurnID
			if active == "" {
				t.Fatal("manager was not working at interruption")
			}
			f.device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{}}`, old.PresentationGeneration))
			f.device.send(`{"type":"input_start","sequence":1}`)
			stop := voiceJourneyEvent(t, f.device, "stop")
			if stop["generation"].(float64) <= float64(old.PresentationGeneration) {
				t.Fatal("unacknowledged speech was not fenced")
			}
			// A stuck provider must be replaced without waiting for its missing ACK.
			// This deadline is only a failure tripwire; peer closure is the oracle.
			closed, cancel := context.WithTimeout(f.ctx, 2*time.Second)
			_, _, err := peer.conn.Read(closed)
			timedOut := closed.Err() != nil
			cancel()
			if err == nil || timedOut {
				t.Fatal("interrupted provider still waits for its old acknowledgement")
			}
			fresh := f.openSpeaker(t)
			if fresh.read()["type"] != "input_audio_buffer.clear" {
				t.Fatal("replacement did not start the preserved utterance")
			}
			fresh.send(`{"type":"input_audio_buffer.cleared"}`)
			started := voiceJourneyEvent(t, f.device, "input_started")
			turnID := started["turnId"].(string)
			voiceBargeInterpret(t, fresh, f.device, turnID, correction)
			voiceJourneyEvent(t, f.device, "intent")
			intent, err := f.journal.Intent(f.ctx, turnID)
			if err != nil || intent.State != hubcore.VoiceIntentAccepted || intent.Method != appwire.MethodTurnSteer || intent.Text != correction {
				t.Fatalf("replacement lost the spoken correction: %+v %v", intent, err)
			}
			if f.read(t).Thread.Evener.ActiveTurnID != active || !f.delivery(t, old.ID).Interrupted {
				t.Fatal("replacement changed manager work or lost presentation retirement")
			}
			select {
			case <-held.Cancelled():
				t.Fatal("replacement cancelled manager work")
			default:
			}
		})
	}
}

func voiceBargeInterpret(t *testing.T, peer, device realtimePeer, turnID, text string) {
	t.Helper()
	device.send(`{"type":"audio","sequence":1,"frame":0,"pcm":"AAABAA=="}`)
	device.send(`{"type":"input_end","sequence":1}`)
	if peer.read()["type"] != "input_audio_buffer.append" || peer.read()["type"] != "input_audio_buffer.commit" {
		t.Fatal("new utterance did not reach provider input commit")
	}
	peer.send(`{"type":"input_audio_buffer.committed","item_id":"barge-input"}`)
	interpret := peer.read()
	response, _ := interpret["response"].(map[string]any)
	metadata, _ := response["metadata"].(map[string]any)
	if interpret["type"] != "response.create" || metadata["turn_id"] != turnID || metadata["input_item_id"] != "barge-input" {
		t.Fatal("new interpretation lost its durable input identity")
	}
	meta, _ := json.Marshal(metadata)
	arguments, _ := json.Marshal(map[string]string{"text": text})
	peer.send(fmt.Sprintf(`{"type":"response.created","response":{"id":"barge-interpret","status":"in_progress","metadata":%s}}`, meta))
	peer.send(fmt.Sprintf(`{"type":"response.done","response":{"id":"barge-interpret","status":"completed","metadata":%s,"output":[{"id":"barge-call-item","type":"function_call","status":"completed","name":"submit_utterance","call_id":"barge-call","arguments":%q}]}}`, meta, arguments))
}
