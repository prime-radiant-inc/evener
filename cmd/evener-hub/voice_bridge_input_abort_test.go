package hub

import (
	"encoding/json"
	"fmt"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
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
