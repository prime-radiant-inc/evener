package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// input_abort must retire only incomplete input, not the native socket. A
// cancelled provider setup must drain before the next Talk can use its work slot.
func TestE2E_VoiceInputAbortKeepsSocketUsable(t *testing.T) {
	for _, phase := range []string{"input_idle", "input_overflow", "input_start_timeout", "capture_interrupted"} {
		t.Run(phase, func(t *testing.T) {
			f := newVoiceCorrectionStack(t)
			f.device.send(`{"type":"input_start","sequence":1}`)
			var peer realtimePeer
			if phase == "input_start_timeout" {
				select {
				case peer = <-f.peers:
				case <-f.ctx.Done():
					t.Fatal("provider setup did not start")
				}
				if peer.read()["type"] != "session.update" {
					t.Fatal("provider setup did not reach its acknowledgement boundary")
				}
			} else {
				peer = f.openSpeaker(t)
				if peer.read()["type"] != "input_audio_buffer.clear" {
					t.Fatal("Talk did not reach provider input setup")
				}
				if phase != "capture_interrupted" {
					peer.send(`{"type":"input_audio_buffer.cleared"}`)
					voiceJourneyEvent(t, f.device, "input_started")
					if phase == "input_overflow" {
						f.device.send(`{"type":"audio","sequence":1,"frame":0,"pcm":"//8="}`)
						if peer.read()["type"] != "input_audio_buffer.append" {
							t.Fatal("partial input did not reach the provider")
						}
					}
				}
			}
			f.device.send(fmt.Sprintf(`{"type":"input_abort","sequence":1,"reason":%q}`, phase))
			f.device.send(`{"type":"input_start","sequence":2}`)
			f.device.send(`{"type":"renew"}`)
			// Renewal is an ordered positive socket barrier. An input_failed
			// packet or a disconnect fails immediately, not by waiting for silence.
			voiceJourneyEvent(t, f.device, "renewed")
			aborted, err := f.journal.CreateTurn(f.ctx, f.armed.BindingID, f.armed.Epoch, 1)
			if err != nil || aborted.State != hubcore.VoiceTurnAborted {
				t.Fatalf("input abort did not settle its journal turn: %+v %v", aborted, err)
			}
			fresh := f.openSpeaker(t)
			if fresh.read()["type"] != "input_audio_buffer.clear" {
				t.Fatal("fresh Talk did not clear provider input")
			}
			fresh.send(`{"type":"input_audio_buffer.cleared"}`)
			started := voiceJourneyEvent(t, f.device, "input_started")
			turnID, _ := started["turnId"].(string)
			if turnID == "" || turnID == aborted.ID || started["sequence"] != float64(2) {
				t.Fatal("fresh Talk reused the aborted input identity")
			}
			f.device.send(`{"type":"audio","sequence":2,"frame":0,"pcm":"AAABAA=="}`)
			f.device.send(`{"type":"input_end","sequence":2}`)
			// Once input_end owns interpretation, a delayed capture abort must
			// leave that work intact rather than discard or duplicate its intent.
			f.device.send(`{"type":"input_abort","sequence":2,"reason":"capture_interrupted"}`)
			f.device.send(`{"type":"renew"}`)
			voiceJourneyEvent(t, f.device, "renewed")
			if fresh.read()["type"] != "input_audio_buffer.append" || fresh.read()["type"] != "input_audio_buffer.commit" {
				t.Fatal("fresh utterance did not reach the provider")
			}
			fresh.send(`{"type":"input_audio_buffer.committed","item_id":"abort-fresh-input"}`)
			interpret := fresh.read()
			response, _ := interpret["response"].(map[string]any)
			metadata, _ := response["metadata"].(map[string]any)
			if interpret["type"] != "response.create" || metadata["turn_id"] != turnID {
				t.Fatal("fresh interpretation used the wrong journal turn")
			}
			meta, err := json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			const text = "abort-fresh-utterance-571"
			fresh.send(fmt.Sprintf(`{"type":"response.created","response":{"id":"abort-fresh-response","status":"in_progress","metadata":%s}}`, meta))
			fresh.send(fmt.Sprintf(`{"type":"response.done","response":{"id":"abort-fresh-response","status":"completed","metadata":%s,"output":[{"id":"abort-fresh-call-item","type":"function_call","status":"completed","name":"submit_utterance","call_id":"abort-fresh-call","arguments":"{\"text\":\"%s\"}"}]}}`, meta, text))
			accepted := voiceJourneyEvent(t, f.device, "intent")
			if accepted["turnId"] != turnID || accepted["custody"] != hubcore.VoiceIntentAccepted {
				t.Fatal("fresh utterance was not admitted on the original socket")
			}
			if request := f.next(t); !request.Contains(text) {
				t.Fatal("manager did not consume the fresh utterance")
			}
			intents, _, err := f.journal.ListIntents(f.ctx, f.armed.BindingID, 0, 10)
			if err != nil || len(intents) != 1 || intents[0].TurnID != turnID {
				t.Fatalf("aborted input produced an intent: %+v %v", intents, err)
			}
		})
	}
}

// input_abort must defer closing a provider captured by retired Interrupt until
// its result arrives, even when an earlier delivery has already fully played.
func TestE2E_VoiceInputAbortDefersProviderCloseUntilRetiredWorkSettles(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	const reply = "abort-played-reply-613"
	f.admit(t, "abort-played-input-613", false)
	f.next(t).RespondToolCall("communicate", communicateArgs(reply))
	f.waitRead(t, "played reply committed and manager idle", func(read appwire.ThreadReadResponse) bool {
		return read.Thread.Evener.ActiveTurnID == "" && voiceCorrectionItem(read, reply).ID != ""
	})
	start := voiceJourneyEvent(t, f.device, "speech_start")
	peer := f.openSpeaker(t)
	delivery := f.speechRequest(t, peer, start, reply)
	const responseID, itemID = "abort-played-response", "abort-played-item"
	pcm := base64.StdEncoding.EncodeToString(make([]byte, 960))
	voiceCorrectionAudio(peer, delivery, responseID, itemID, pcm)
	voiceCorrectionDone(peer, delivery, responseID, itemID)
	f.expectFinishedAudio(t, delivery, pcm)
	f.device.send(fmt.Sprintf(`{"type":"played","deliveryId":%q,"generation":%d,"itemId":%q,"samples":480,"complete":true}`, delivery.ID, delivery.PresentationGeneration, itemID))
	f.waitDelivery(t, delivery.ID, func(d hubcore.SpeechDelivery) bool {
		return d.OutputComplete && len(d.Items) == 1 && d.Items[0].PlaybackComplete && d.Items[0].PlayedSamples == 480
	})
	f.device.send(`{"type":"input_start","sequence":1}`)
	if peer.read()["type"] != "input_audio_buffer.clear" {
		t.Fatal("Talk did not reuse the played delivery's provider")
	}
	peer.send(`{"type":"input_audio_buffer.cleared"}`)
	voiceJourneyEvent(t, f.device, "input_started")
	f.device.send(`{"type":"audio","sequence":1,"frame":0,"pcm":"//8="}`)
	if peer.read()["type"] != "input_audio_buffer.append" {
		t.Fatal("capture was not mid-utterance before pause")
	}
	t.Log("pause precondition reached: prior delivery fully played, same provider accepted microphone PCM")
	// Native pause sends these controls back to back. Withhold the provider's
	// truncation acknowledgement so the retired job still owns the connection.
	f.device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{%q:480}}`, delivery.PresentationGeneration, itemID))
	f.device.send(`{"type":"input_abort","sequence":1,"reason":"capture_interrupted"}`)
	f.device.send(`{"type":"renew"}`)
	voiceJourneyEvent(t, f.device, "stop")
	_, raw, err := peer.conn.Read(f.ctx)
	if err != nil {
		t.Fatalf("input_abort closed the provider before retired Interrupt could settle: %v", err)
	}
	var truncate map[string]any
	if err := json.Unmarshal(raw, &truncate); err != nil {
		t.Fatal(err)
	}
	if truncate["type"] != "conversation.item.truncate" || truncate["item_id"] != itemID || truncate["audio_end_ms"] != float64(20) {
		t.Fatalf("retired job did not reach the played-sample acknowledgement boundary: %v", truncate)
	}
	// Renewal proves the loop consumed input_abort without input_failed or
	// disconnect. Ping proves its provider remains live before Interrupt settles.
	voiceJourneyEvent(t, f.device, "renewed")
	aborted, err := f.journal.CreateTurn(f.ctx, f.armed.BindingID, f.armed.Epoch, 1)
	if err != nil || aborted.State != hubcore.VoiceTurnAborted {
		t.Fatalf("pause did not abort its partial utterance: %+v %v", aborted, err)
	}
	closed := make(chan error, 1)
	go func() {
		_, _, err := peer.conn.Read(f.ctx)
		closed <- err
	}()
	pingCtx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	if err := peer.conn.Ping(pingCtx); err != nil {
		t.Fatalf("input_abort closed the provider while retired Interrupt still awaited truncation: %v", err)
	}
	peer.send(fmt.Sprintf(`{"type":"conversation.item.truncated","item_id":%q,"content_index":0,"audio_end_ms":20}`, itemID))
	select {
	case err := <-closed:
		if err == nil || f.ctx.Err() != nil {
			t.Fatalf("retired provider was not closed after acknowledgement: %v", err)
		}
	case <-f.ctx.Done():
		t.Fatal("provider close did not follow the retired result")
	}
	stored := f.delivery(t, delivery.ID)
	if stored.Interrupted || !stored.OutputComplete || !stored.Items[0].PlaybackComplete || stored.Items[0].PlayedSamples != 480 {
		t.Fatalf("pause lost fully played delivery evidence: %+v", stored)
	}
	voiceAbortFreshUtterance(t, f, aborted.ID)
}

// Closing an aborted input's provider must discard its bound speech state,
// preserving rendered evidence and stopping unfinished playback without another
// provider Interrupt. Neither a later pause nor fresh speech can use that owner.
func TestE2E_VoiceInputAbortDropsClosedProviderSpeechState(t *testing.T) {
	for _, scenario := range []string{"rendered_interrupt", "rendered_fresh_reply", "unfinished_interrupt"} {
		t.Run(scenario, func(t *testing.T) {
			f := newVoiceCorrectionStack(t)
			const reply, freshReply = "idle-abort-reply-641", "idle-abort-fresh-reply-643"
			f.admit(t, "idle-abort-input-641", false)
			f.next(t).RespondToolCall("communicate", communicateArgs(reply))
			f.waitRead(t, "reply committed and manager idle", func(read appwire.ThreadReadResponse) bool {
				return read.Thread.Evener.ActiveTurnID == "" && voiceCorrectionItem(read, reply).ID != ""
			})
			start := voiceJourneyEvent(t, f.device, "speech_start")
			peer := f.openSpeaker(t)
			delivery := f.speechRequest(t, peer, start, reply)
			const response, item = "idle-abort-response", "idle-abort-item"
			pcm := base64.StdEncoding.EncodeToString(make([]byte, 960))
			voiceCorrectionAudio(peer, delivery, response, item, pcm)
			voiceCorrectionDone(peer, delivery, response, item)
			f.expectFinishedAudio(t, delivery, pcm)
			rendered := scenario != "unfinished_interrupt"
			played := 240
			if rendered {
				played = 480
			}
			f.device.send(fmt.Sprintf(`{"type":"played","deliveryId":%q,"generation":%d,"itemId":%q,"samples":%d,"complete":%t}`, delivery.ID, delivery.PresentationGeneration, item, played, rendered))
			f.waitDelivery(t, delivery.ID, func(d hubcore.SpeechDelivery) bool {
				return d.OutputComplete && len(d.Items) == 1 && d.Items[0].PlayedSamples == int64(played) && d.Items[0].PlaybackComplete == rendered
			})
			f.device.send(`{"type":"input_start","sequence":1}`)
			if peer.read()["type"] != "input_audio_buffer.clear" {
				t.Fatal("idle Talk did not reuse the original speech provider")
			}
			peer.send(`{"type":"input_audio_buffer.cleared"}`)
			voiceJourneyEvent(t, f.device, "input_started")
			f.device.send(`{"type":"input_abort","sequence":1,"reason":"input_idle"}`)
			f.device.send(`{"type":"renew"}`)
			generation := delivery.PresentationGeneration
			if !rendered {
				stop := f.device.read()
				if stop["type"] != "stop" {
					t.Fatalf("idle abort did not stop unfinished playback before renewal: %v", stop)
				}
				generation = uint64(stop["generation"].(float64))
				if generation <= delivery.PresentationGeneration {
					t.Fatal("idle abort did not fence unfinished speech")
				}
			}
			if event := f.device.read(); event["type"] != "renewed" {
				t.Fatalf("idle abort did not preserve the socket and completed-playback fence: %v", event)
			}
			closed, cancel := context.WithTimeout(f.ctx, 5*time.Second)
			_, _, err := peer.conn.Read(closed)
			timedOut := closed.Err() != nil
			cancel()
			if err == nil || timedOut {
				t.Fatalf("idle abort did not close its provider: %v", err)
			}
			aborted, err := f.journal.CreateTurn(f.ctx, f.armed.BindingID, f.armed.Epoch, 1)
			if err != nil || aborted.State != hubcore.VoiceTurnAborted {
				t.Fatalf("idle abort did not settle its journal turn: %+v %v", aborted, err)
			}
			stored := f.delivery(t, delivery.ID)
			if stored.Interrupted == rendered || !stored.OutputComplete || stored.Items[0].PlaybackComplete != rendered || stored.Items[0].PlayedSamples != int64(played) {
				t.Fatalf("provider retirement changed playback evidence: %+v", stored)
			}
			t.Logf("idle-abort boundary reached: prior reply rendered=%v, input aborted, provider closed", rendered)
			if scenario != "rendered_fresh_reply" {
				f.device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{}}`, generation))
				voiceJourneyEvent(t, f.device, "stop")
				f.device.send(`{"type":"renew"}`)
				voiceJourneyEvent(t, f.device, "renewed")
			}
			fresh, request := voiceAbortFreshUtterance(t, f, aborted.ID)
			request.RespondToolCall("communicate", communicateArgs(freshReply))
			freshStart := voiceJourneyEvent(t, f.device, "speech_start")
			d := f.speechRequest(t, fresh, freshStart, freshReply)
			if d.ID == delivery.ID || d.PresentationGeneration <= generation {
				t.Fatal("fresh reply reused closed-provider speech state")
			}
			voiceCorrectionAudio(fresh, d, "idle-abort-fresh-response", "idle-abort-fresh-item", "AQACAA==")
			voiceCorrectionDone(fresh, d, "idle-abort-fresh-response", "idle-abort-fresh-item")
			f.expectFinishedAudio(t, d, "AQACAA==")
		})
	}
}

func voiceAbortFreshUtterance(t *testing.T, f *voiceCorrectionStack, abortedID string) (realtimePeer, *fakellm.Call) {
	t.Helper()
	f.device.send(`{"type":"input_start","sequence":2}`)
	fresh := f.openSpeaker(t)
	if fresh.read()["type"] != "input_audio_buffer.clear" {
		t.Fatal("fresh Talk did not clear replacement provider input")
	}
	fresh.send(`{"type":"input_audio_buffer.cleared"}`)
	started := voiceJourneyEvent(t, f.device, "input_started")
	turnID, _ := started["turnId"].(string)
	if turnID == "" || turnID == abortedID || started["sequence"] != float64(2) {
		t.Fatal("fresh Talk reused the aborted input identity")
	}
	f.device.send(`{"type":"audio","sequence":2,"frame":0,"pcm":"AAABAA=="}`)
	f.device.send(`{"type":"input_end","sequence":2}`)
	if fresh.read()["type"] != "input_audio_buffer.append" || fresh.read()["type"] != "input_audio_buffer.commit" {
		t.Fatal("fresh utterance did not reach the replacement provider")
	}
	fresh.send(`{"type":"input_audio_buffer.committed","item_id":"abort-retired-fresh-input"}`)
	interpret := fresh.read()
	response, _ := interpret["response"].(map[string]any)
	metadata, _ := response["metadata"].(map[string]any)
	if interpret["type"] != "response.create" || metadata["turn_id"] != turnID {
		t.Fatal("fresh interpretation used the wrong journal turn")
	}
	meta, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	const text = "abort-retired-fresh-utterance-617"
	fresh.send(fmt.Sprintf(`{"type":"response.created","response":{"id":"abort-retired-fresh-response","status":"in_progress","metadata":%s}}`, meta))
	fresh.send(fmt.Sprintf(`{"type":"response.done","response":{"id":"abort-retired-fresh-response","status":"completed","metadata":%s,"output":[{"id":"abort-retired-fresh-call-item","type":"function_call","status":"completed","name":"submit_utterance","call_id":"abort-retired-fresh-call","arguments":"{\"text\":\"%s\"}"}]}}`, meta, text))
	accepted := voiceJourneyEvent(t, f.device, "intent")
	if accepted["turnId"] != turnID || accepted["custody"] != hubcore.VoiceIntentAccepted {
		t.Fatal("fresh utterance was not admitted on the original socket")
	}
	request := f.next(t)
	if !request.Contains(text) {
		t.Fatal("manager did not consume the fresh utterance")
	}
	intents, _, err := f.journal.ListIntents(f.ctx, f.armed.BindingID, 0, 10)
	if err != nil || len(intents) != 1 || intents[0].TurnID != turnID {
		t.Fatalf("pause produced an intent for aborted audio: %+v %v", intents, err)
	}
	return fresh, request
}
