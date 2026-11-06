package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// Stop comes from a second ordinary AppWire client, never the voice device.
// The manager is held at a real model request after committing two replies in
// the same execution. Recorded input remains required after cancellation.
func TestE2E_VoiceSessionStopRetiresPlayingExecution(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprintf("providerComplete=%t", complete), func(t *testing.T) {
			voiceStopPlayingExecution(t, complete)
		})
	}
}

func voiceStopPlayingExecution(t *testing.T, outputComplete bool) {
	t.Helper()
	f := newVoiceCorrectionStack(t)
	const alpha, reply, pending = "STOP-ALPHA-INPUT-211", "STOP-ALPHA-OUTPUT-211", "STOP-PENDING-OUTPUT-223"
	alphaID := f.admit(t, alpha, false)
	request := f.next(t)
	if !request.Contains(alpha) || !request.Contains("CORRECTION-OPENING-61") {
		t.Fatal("actual manager request lacks the admitted inputs")
	}
	voiceStopContinue(request, reply)
	held := f.next(t)
	start := voiceJourneyEvent(t, f.device, "speech_start")
	peer := f.openSpeaker(t)
	old := f.speechRequest(t, peer, start, reply)
	const response, item = "stop-alpha-response", "stop-alpha-item"
	pcm := base64.StdEncoding.EncodeToString(make([]byte, 960))
	voiceCorrectionAudio(peer, old, response, item, pcm)
	audio := voiceJourneyEvent(t, f.device, "audio")
	if audio["deliveryId"] != old.ID || audio["pcm"] != pcm {
		t.Fatal("canonical speech was not actually queued for playback")
	}
	f.device.send(fmt.Sprintf(`{"type":"played","deliveryId":%q,"generation":%d,"itemId":%q,"samples":240}`, old.ID, old.PresentationGeneration, item))
	f.waitDelivery(t, old.ID, func(d hubcore.SpeechDelivery) bool {
		return len(d.Items) == 1 && d.Items[0].PlayedSamples == 240 && d.Items[0].QueuedSamples == 480
	})
	if outputComplete {
		voiceCorrectionDone(peer, old, response, item)
		voiceJourneyEvent(t, f.device, "speech_end")
		voiceJourneyEvent(t, f.device, "delivery_end")
		if !f.delivery(t, old.ID).OutputComplete {
			t.Fatal("provider-complete scenario did not reach durable completion")
		}
	}
	voiceStopContinue(held, pending)
	held = f.next(t)
	running := f.read(t)
	source, queued := voiceCorrectionItem(running, reply), voiceCorrectionItem(running, pending)
	execution := running.Thread.Evener.ActiveTurnID
	if execution == "" || source.TurnID != execution || queued.TurnID != execution || source.ID != old.CanonicalSourceItemID || !held.Contains(alpha) {
		t.Fatalf("speech and pending source do not belong to held execution: active=%s source=%+v pending=%+v", execution, source, queued)
	}
	stopped := voiceStopExecution(t, f, held, execution)
	voiceStopRequiredInput(t, stopped, alphaID, source)
	t.Logf("remote Stop applied to execution=%s, canonical source=%s, pending=%s, played=240/480, held request cancelled, Alpha remains required", execution, source.ID, queued.ID)
	minimum := voiceStopFence(t, f, old)
	voiceStopProvider(t, peer, old, response, item, outputComplete)
	voiceCorrectionDelta(peer, response, item, "//8=")

	// A fresh execution and its terminal audio are positive transport barriers:
	// any queued old packet or pending source is encountered first and fails.
	const freshInput, freshReply = "STOP-FRESH-INPUT-227", "STOP-FRESH-OUTPUT-227"
	f.admit(t, freshInput, false)
	freshRequest := f.next(t)
	if !freshRequest.Contains(freshInput) || !freshRequest.Contains(alpha) {
		t.Fatal("Stop lost the original typed conversation")
	}
	freshRequest.RespondToolCall("communicate", communicateArgs(freshReply))
	freshStart := voiceJourneyEvent(t, f.device, "speech_start")
	fresh := f.speechRequest(t, peer, freshStart, freshReply)
	if fresh.PresentationGeneration < minimum || fresh.ID == old.ID {
		t.Fatal("fresh execution reused retired presentation identity")
	}
	voiceCorrectionAudio(peer, fresh, "stop-fresh-response", "stop-fresh-item", "AQACAA==")
	voiceCorrectionDelta(peer, response, item, "//8=")
	voiceCorrectionDone(peer, fresh, "stop-fresh-response", "stop-fresh-item")
	f.expectFinishedAudio(t, fresh, "AQACAA==")
	final := f.read(t)
	if voiceCorrectionItem(final, reply).ID != source.ID || voiceCorrectionItem(final, pending).ID != queued.ID || voiceCorrectionItem(final, freshReply).TurnID == execution {
		t.Fatal("Stop removed canonical text or fresh speech did not belong to a new execution")
	}
}

func TestE2E_VoiceSessionStopClosesUnacknowledgedProvider(t *testing.T) {
	for _, phase := range []string{"session", "response"} {
		t.Run(phase, func(t *testing.T) {
			f := newVoiceCorrectionStack(t)
			const input, reply = "STOP-ACK-INPUT-229", "STOP-ACK-OUTPUT-229"
			alphaID := f.admit(t, input, false)
			request := f.next(t)
			if !request.Contains(input) {
				t.Fatal("actual model request did not incorporate the input")
			}
			voiceStopContinue(request, reply)
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
					t.Fatal("provider did not reach its unacknowledged session setup")
				}
			} else {
				peer = f.openSpeaker(t)
				f.speechRequest(t, peer, start, reply)
			}
			old := f.delivery(t, start["deliveryId"].(string))
			running := f.read(t)
			source := voiceCorrectionItem(running, reply)
			if source.TurnID == "" || source.TurnID != running.Thread.Evener.ActiveTurnID {
				t.Fatal("unacknowledged delivery is not owned by the held execution")
			}
			stopped := voiceStopExecution(t, f, held, source.TurnID)
			voiceStopRequiredInput(t, stopped, alphaID, source)
			voiceStopFence(t, f, old)
			ctx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
			_, _, err := peer.conn.Read(ctx)
			timedOut := ctx.Err() != nil
			cancel()
			if err == nil || timedOut {
				t.Fatal("remote Stop still waits for the provider acknowledgement")
			}
			const freshReply = "STOP-ACK-FRESH-OUTPUT-233"
			f.admit(t, "STOP-ACK-FRESH-INPUT-233", false)
			f.next(t).RespondToolCall("communicate", communicateArgs(freshReply))
			freshStart := voiceJourneyEvent(t, f.device, "speech_start")
			freshPeer := f.openSpeaker(t)
			fresh := f.speechRequest(t, freshPeer, freshStart, freshReply)
			voiceCorrectionAudio(freshPeer, fresh, "stop-ack-response", "stop-ack-item", "AQACAA==")
			voiceCorrectionDone(freshPeer, fresh, "stop-ack-response", "stop-ack-item")
			f.expectFinishedAudio(t, fresh, "AQACAA==")
		})
	}
}

// Run the real selector synchronously over real daemon cuts. Its return is a
// positive processing barrier before ANY fresh input can fence the old outputs.
// Both the stopped read and a captured terminal notification must prevent an
// older in-flight read from selecting pending sources of the stopped execution.
func TestE2E_VoiceSessionStopSuppressesPendingAndStaleHistory(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	_ = f.device.conn.CloseNow()
	baseline := f.read(t)
	observer := f.stack.dialRPC(f.ctx, t)
	if _, err := observer.ThreadRead(f.ctx, appwire.ThreadReadParams{Ref: f.ref, Subscribe: true}); err != nil {
		t.Fatal(err)
	}
	const input, first, second = "STOP-PENDING-INPUT-239", "STOP-PENDING-FIRST-239", "STOP-PENDING-SECOND-241"
	alphaID := f.admit(t, input, false)
	request := f.next(t)
	if !request.Contains(input) || !request.Contains("CORRECTION-OPENING-61") {
		t.Fatal("pending sources lack actual incorporated input")
	}
	voiceStopContinue(request, first)
	voiceStopContinue(f.next(t), second)
	held := f.next(t)
	running := f.read(t)
	source, pending := voiceCorrectionItem(running, first), voiceCorrectionItem(running, second)
	execution := running.Thread.Evener.ActiveTurnID
	if execution == "" || source.TurnID != execution || pending.TurnID != execution {
		t.Fatal("both pending sources must belong to the actual held execution")
	}
	stopped := voiceStopExecution(t, f, held, execution)
	voiceStopRequiredInput(t, stopped, alphaID, source)
	update := voiceStopNotification(t, f, observer, execution)
	for _, evidence := range []string{"read", "notification"} {
		t.Run(evidence, func(t *testing.T) {
			bridge, _, _ := voiceQuestionSelector(t, f, baseline)
			cursor := appwire.ClonePointer(bridge.speechCursor)
			generation, err := f.journal.MaxSpeechGeneration(f.ctx, f.armed.BindingID, f.armed.Epoch)
			if err != nil {
				t.Fatal(err)
			}
			if evidence == "notification" {
				// Reject messages that do not belong to the established read identity.
				for _, change := range []func(*appwire.HistoryUpdatedParams){
					func(p *appwire.HistoryUpdatedParams) { p.Ref += "-other" },
					func(p *appwire.HistoryUpdatedParams) { p.ThreadID += "-other" },
					func(p *appwire.HistoryUpdatedParams) { p.BootGeneration += "-other" },
					func(p *appwire.HistoryUpdatedParams) { p.Epoch++ },
					func(p *appwire.HistoryUpdatedParams) { p.Snapshot.Incarnation += "-other" },
				} {
					other := update
					change(&other)
					if err := bridge.applySpeechHistoryUpdate(other); err != nil || len(bridge.interruptedTurns) != 0 {
						t.Fatalf("unrelated history identity changed speech: %v", err)
					}
				}
				if err := bridge.applySpeechHistoryUpdate(update); err != nil {
					t.Fatal(err)
				}
			} else if err := bridge.selectSpeech(stopped); err != nil {
				t.Fatal(err)
			}
			for _, read := range []appwire.ThreadReadResponse{running, stopped} {
				if err := bridge.selectSpeech(read); err != nil {
					t.Fatal(err)
				}
				if bridge.delivery != nil || bridge.generation != 100 || !reflect.DeepEqual(cursor, bridge.speechCursor) {
					t.Fatalf("stopped execution selected before any fresh input: delivery=%+v cursor=%+v", bridge.delivery, bridge.speechCursor)
				}
			}
			after, err := f.journal.MaxSpeechGeneration(f.ctx, f.armed.BindingID, f.armed.Epoch)
			if err != nil || after != generation {
				t.Fatal("pending-source suppression created a durable delivery")
			}
		})
	}
	t.Logf("real interrupted execution=%s, pending sources=%s,%s, stopped and stale cuts processed without new input or delivery", execution, source.ID, pending.ID)
}

func TestE2E_VoiceSessionStopReadPreservesRenderedEvidence(t *testing.T) {
	for _, rendered := range []bool{false, true} {
		t.Run(fmt.Sprintf("rendered=%t", rendered), func(t *testing.T) {
			f := newVoiceCorrectionStack(t)
			_ = f.device.conn.CloseNow()
			bridge, device, peer := voiceQuestionSelector(t, f, f.read(t))
			const input, reply = "STOP-RENDER-INPUT-251", "STOP-RENDER-OUTPUT-251"
			alphaID := f.admit(t, input, false)
			request := f.next(t)
			if !request.Contains(input) {
				t.Fatal("actual request did not consume the recorded input")
			}
			voiceStopContinue(request, reply)
			held := f.next(t)
			running := f.read(t)
			source := voiceCorrectionItem(running, reply)
			if source.TurnID == "" || source.TurnID != running.Thread.Evener.ActiveTurnID {
				t.Fatal("source execution is not held at the model boundary")
			}
			if err := bridge.selectSpeech(running); err != nil {
				t.Fatal(err)
			}
			start := voiceJourneyEvent(t, device, "speech_start")
			old := f.speechRequest(t, peer, start, reply)
			voiceCorrectionAudio(peer, old, "stop-render-response", "stop-render-item", base64.StdEncoding.EncodeToString(make([]byte, 960)))
			voiceStopSelectorSetup(t, f, bridge)
			voiceCorrectionDone(peer, old, "stop-render-response", "stop-render-item")
			for !bridge.delivery.OutputComplete {
				select {
				case event := <-bridge.provider.Events():
					if err := bridge.providerEvent(event); err != nil {
						t.Fatal(err)
					}
				case <-f.ctx.Done():
					t.Fatal("provider never completed actual speech")
				}
			}
			for _, kind := range []string{"audio", "speech_end", "delivery_end"} {
				voiceJourneyEvent(t, device, kind)
			}
			played := int64(240)
			if rendered {
				played = 480
			}
			if err := bridge.recordPlayed(voiceControl{DeliveryID: old.ID, Generation: old.PresentationGeneration, ItemID: "stop-render-item", Samples: played, Complete: rendered}); err != nil {
				t.Fatal(err)
			}
			stopped := voiceStopExecution(t, f, held, source.TurnID)
			voiceStopRequiredInput(t, stopped, alphaID, source)
			if err := bridge.selectSpeech(stopped); err != nil {
				t.Fatal(err)
			}
			if err := bridge.selectSpeech(running); err != nil {
				t.Fatal(err)
			}
			stored := f.delivery(t, old.ID)
			if stored.Interrupted == rendered || !stored.OutputComplete || len(stored.Items) != 1 || stored.Items[0].PlayedSamples != played || stored.Items[0].PlaybackComplete != rendered {
				t.Fatalf("Stop changed completed/unfinished playback evidence: %+v", stored)
			}
			if !rendered {
				stop := voiceJourneyEvent(t, device, "stop")
				if stop["generation"].(float64) <= float64(old.PresentationGeneration) || bridge.delivery != nil {
					t.Fatal("refreshed interrupted read did not retire unfinished delivery")
				}
				return
			}
			if bridge.generation != old.PresentationGeneration || bridge.delivery == nil || bridge.delivery.ID != old.ID {
				t.Fatal("Stop retired or fenced already rendered completion")
			}
			// Fresh canonical speech is the ordered socket barrier proving there
			// was no Stop fence. Normal next-delivery retirement still truncates
			// the previous provider item at its fully played watermark.
			const freshReply = "STOP-RENDER-FRESH-OUTPUT-257"
			f.admit(t, "STOP-RENDER-FRESH-INPUT-257", false)
			f.next(t).RespondToolCall("communicate", communicateArgs(freshReply))
			freshRead := f.waitRead(t, "fresh canonical reply", func(read appwire.ThreadReadResponse) bool {
				return voiceCorrectionItem(read, freshReply).ID != "" && read.Thread.Evener.ActiveTurnID == ""
			})
			if err := bridge.selectSpeech(freshRead); err != nil {
				t.Fatal(err)
			}
			freshStart := voiceJourneyEvent(t, device, "speech_start")
			truncate := peer.read()
			if truncate["type"] != "conversation.item.truncate" || truncate["item_id"] != "stop-render-item" || truncate["audio_end_ms"] != float64(20) {
				t.Fatalf("normal next-delivery retirement lost rendered watermark: %v", truncate)
			}
			peer.send(`{"type":"conversation.item.truncated","item_id":"stop-render-item","content_index":0,"audio_end_ms":20}`)
			f.speechRequest(t, peer, freshStart, freshReply)
		})
	}
}

// This isolates the Stop history event from a newer input's separate provenance
// fence. Stop B must not itself target A's presentation, even when A is older.
func TestE2E_VoiceSessionStopHistoryTargetsOnlyOwningExecution(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	_ = f.device.conn.CloseNow()
	bridge, device, peer := voiceQuestionSelector(t, f, f.read(t))
	observer := f.stack.dialRPC(f.ctx, t)
	if _, err := observer.ThreadRead(f.ctx, appwire.ThreadReadParams{Ref: f.ref, Subscribe: true}); err != nil {
		t.Fatal(err)
	}
	const reply = "STOP-OTHER-OUTPUT-263"
	f.admit(t, "STOP-OTHER-INPUT-263", false)
	f.next(t).RespondToolCall("communicate", communicateArgs(reply))
	completed := f.waitRead(t, "older execution completed", func(read appwire.ThreadReadResponse) bool {
		return read.Thread.Evener.ActiveTurnID == "" && voiceCorrectionItem(read, reply).ID != ""
	})
	source := voiceCorrectionItem(completed, reply)
	if err := bridge.selectSpeech(completed); err != nil {
		t.Fatal(err)
	}
	start := voiceJourneyEvent(t, device, "speech_start")
	old := f.speechRequest(t, peer, start, reply)
	voiceCorrectionAudio(peer, old, "stop-other-response", "stop-other-item", "AQACAA==")
	voiceStopSelectorSetup(t, f, bridge)
	select {
	case event := <-bridge.provider.Events():
		if err := bridge.providerEvent(event); err != nil {
			t.Fatal(err)
		}
	case <-f.ctx.Done():
		t.Fatal("older execution audio never arrived")
	}
	voiceJourneyEvent(t, device, "audio")
	f.admit(t, "STOP-OTHER-ACTIVE-269", false)
	held := f.next(t)
	execution := f.read(t).Thread.Evener.ActiveTurnID
	if execution == "" || execution == source.TurnID {
		t.Fatal("Stop target is not a distinct execution")
	}
	voiceStopExecution(t, f, held, execution)
	update := voiceStopNotification(t, f, observer, execution)
	if err := bridge.applySpeechHistoryUpdate(update); err != nil {
		t.Fatal(err)
	}
	if bridge.delivery == nil || bridge.delivery.ID != old.ID || bridge.generation != old.PresentationGeneration || f.delivery(t, old.ID).Interrupted {
		t.Fatal("Stop of another execution retired older speech")
	}
	// Ordered audio after the processed notification proves the unrelated
	// delivery was not fenced at either the device or provider boundary.
	voiceCorrectionDelta(peer, "stop-other-response", "stop-other-item", "AwAEAA==")
	select {
	case event := <-bridge.provider.Events():
		if err := bridge.providerEvent(event); err != nil {
			t.Fatal(err)
		}
	case <-f.ctx.Done():
		t.Fatal("other execution's speech was fenced")
	}
	audio := voiceJourneyEvent(t, device, "audio")
	if audio["deliveryId"] != old.ID || audio["pcm"] != "AwAEAA==" {
		t.Fatal("unrelated Stop changed the older delivery")
	}
}

func TestVoiceSpeechInterruptionRequiresInterruptedStatus(t *testing.T) {
	bridge := &voiceBridge{}
	if err := bridge.applyInterruptedTurns([]appwire.Turn{
		{ID: "completed", Status: appwire.TurnStatusCompleted},
		{ID: "failed", Status: appwire.TurnStatusFailed},
		{ID: "working", Status: appwire.TurnStatusInProgress},
		{ID: "stopped", Status: appwire.TurnStatusInterrupted},
	}); err != nil {
		t.Fatal(err)
	}
	if len(bridge.interruptedTurns) != 1 || !bridge.interruptedTurns["stopped"] {
		t.Fatalf("Stop suppression broadened beyond interrupted executions: %v", bridge.interruptedTurns)
	}
}

func voiceStopSelectorSetup(t *testing.T, f *voiceCorrectionStack, bridge *voiceBridge) {
	t.Helper()
	select {
	case result := <-bridge.work:
		if result.err != nil {
			t.Fatal(result.err)
		}
		bridge.workCancel()
		bridge.workCancel, bridge.workKind = nil, ""
	case <-f.ctx.Done():
		t.Fatal("selected speech did not complete provider setup")
	}
}

func voiceStopNotification(t *testing.T, f *voiceCorrectionStack, client *appwire.Client, execution string) appwire.HistoryUpdatedParams {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	for {
		select {
		case notification, ok := <-client.Notifications():
			if !ok {
				t.Fatal("history subscription closed before terminal notification")
			}
			if notification.Method != appwire.NotifyHistoryUpdated {
				continue
			}
			var update appwire.HistoryUpdatedParams
			if err := json.Unmarshal(notification.Params, &update); err != nil {
				t.Fatal(err)
			}
			for _, turn := range update.Turns {
				if update.Ref == f.ref && turn.ID == execution && turn.Status == appwire.TurnStatusInterrupted {
					return update
				}
			}
		case <-ctx.Done():
			t.Fatal("accepted Stop did not publish interrupted execution metadata")
		}
	}
}

func voiceStopContinue(call *fakellm.Call, text string) {
	args := communicateArgs(text)
	args["end_turn"] = false
	call.RespondToolCall("communicate", args)
}

func voiceStopExecution(t *testing.T, f *voiceCorrectionStack, held *fakellm.Call, execution string) appwire.ThreadReadResponse {
	t.Helper()
	other := f.stack.dialRPC(f.ctx, t)
	id := newMutationID(t)
	result, err := clientRequest[appwire.TurnInterruptResponse](f.ctx, other, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{Ref: f.ref, ExpectedInstanceID: f.instance, ClientMutationID: id})
	if err != nil || result.Receipt.Disposition != appwire.MutationDispositionApplied || result.Receipt.TurnID != execution || result.Receipt.InstanceID != f.instance || result.Receipt.ClientMutationID != id {
		t.Fatalf("second-client Stop did not cancel the source execution: %+v %v", result, err)
	}
	select {
	case <-held.Cancelled():
	case <-f.ctx.Done():
		t.Fatal("accepted Stop did not cancel the actual held model request")
	}
	return f.waitRead(t, "source execution interrupted", func(read appwire.ThreadReadResponse) bool {
		for _, turn := range read.Thread.Turns {
			if turn.ID == execution {
				return turn.Status == appwire.TurnStatusInterrupted && read.Thread.Evener.ActiveTurnID == ""
			}
		}
		return false
	})
}

func voiceStopRequiredInput(t *testing.T, read appwire.ThreadReadResponse, input string, source appwire.ThreadItem) {
	t.Helper()
	state := read.Thread.Evener.ConversationProvenance
	if !slices.Contains(state.AdmittedInputs, "client:"+input) || slices.Contains(state.RetiredInputs, "client:"+input) || source.ConversationProvenance == nil || !voiceCorrectionFrontier(t, state, source.ConversationProvenance.FrontierID)["client:"+input] {
		t.Fatal("Stop retired recorded input or lost the source's actual request frontier")
	}
}

func voiceStopFence(t *testing.T, f *voiceCorrectionStack, old hubcore.SpeechDelivery) uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	_, raw, err := f.device.conn.Read(ctx)
	if err != nil {
		t.Fatalf("accepted remote Stop produced no device fence before new input or completion, durable interrupted=%t: %v", f.delivery(t, old.ID).Interrupted, err)
	}
	var stop map[string]any
	if err := json.Unmarshal(raw, &stop); err != nil {
		t.Fatal(err)
	}
	minimum, _ := stop["generation"].(float64)
	if stop["type"] != "stop" || minimum <= float64(old.PresentationGeneration) {
		t.Fatalf("remote Stop did not advance presentation generation: %v", stop)
	}
	f.waitDelivery(t, old.ID, func(d hubcore.SpeechDelivery) bool { return d.Interrupted })
	return uint64(minimum)
}

func voiceStopProvider(t *testing.T, peer realtimePeer, old hubcore.SpeechDelivery, response, item string, outputComplete bool) {
	t.Helper()
	if !outputComplete {
		cancel := peer.read()
		if cancel["type"] != "response.cancel" || cancel["response_id"] != response {
			t.Fatalf("Stop did not cancel the active provider response: %v", cancel)
		}
		peer.send(fmt.Sprintf(`{"type":"response.done","response":{"id":%q,"status":"cancelled","metadata":{"delivery_id":%q,"generation":%q},"output":[]}}`, response, old.ID, strconv.FormatUint(old.PresentationGeneration, 10)))
	}
	truncate := peer.read()
	if truncate["type"] != "conversation.item.truncate" || truncate["item_id"] != item || truncate["audio_end_ms"] != float64(10) || truncate["content_index"] != float64(0) {
		t.Fatalf("Stop did not truncate at the actual played watermark: %v", truncate)
	}
	peer.send(fmt.Sprintf(`{"type":"conversation.item.truncated","item_id":%q,"content_index":0,"audio_end_ms":10}`, item))
}
