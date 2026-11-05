package hub

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// This catches selecting only communicate output, speaking the bounded preview,
// losing question order, or creating a second answer pipeline for voice.
func TestE2E_VoicePendingQuestionsAndFreeTextAnswer(t *testing.T) {
	f := newVoiceCorrectionStack(t)
	inputID := f.admit(t, "QUESTION-INPUT-71", false)
	const question = "QUESTION-FIRST-73"
	const longDetail = "DETAIL-FIRST-79-" + "012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789"
	const answer = "ANSWER-FREE-TEXT-83"
	const reply = "ANSWER-COMMITTED-89"
	f.next(t).RespondToolCall("ask_user", map[string]any{
		"intent": "PRIVATE-QUESTION-INTENT-97",
		"questions": []any{
			map[string]any{"header": "HEADER-FIRST-72", "question": question, "why": "WHY-FIRST-74", "if_unanswered": "FALLBACK-FIRST-75", "options": []any{
				map[string]any{"label": "OPTION-FIRST-76", "detail": longDetail, "recommended": true},
				map[string]any{"label": "OPTION-SECOND-77", "detail": "DETAIL-SECOND-78"},
			}},
			map[string]any{"question": "QUESTION-SECOND-80", "multi_select": true, "options": []any{
				map[string]any{"label": "OPTION-THIRD-81", "detail": "DETAIL-THIRD-82"},
				map[string]any{"label": "OPTION-FOURTH-84", "detail": "DETAIL-FOURTH-85"},
			}},
		},
	})
	var source appwire.ThreadItem
	read := f.waitRead(t, "durable pending question", func(read appwire.ThreadReadResponse) bool {
		for _, turn := range read.Thread.Turns {
			for _, item := range turn.Items {
				if item.ToolName == "ask_user" && item.Status == appwire.TurnStatusCompleted {
					source = item
				}
			}
		}
		return source.ID != "" && read.Thread.Evener.AskPending && read.Thread.Evener.ActiveTurnID == ""
	})
	if source.ConversationProvenance == nil || source.ConversationProvenance.SourceItemID != source.ID || !voiceCorrectionFrontier(t, read.Thread.Evener.ConversationProvenance, source.ConversationProvenance.FrontierID)["client:"+inputID] {
		t.Fatal("actual successful ask lost its original input provenance")
	}
	pending := read.Thread.Evener.PendingAsk
	if pending == nil || pending.InstanceID != f.instance || len(pending.Calls) != 1 || pending.Calls[0].TurnID != source.TurnID || pending.Calls[0].CallID != source.CallID {
		t.Fatal("live pending owner does not identify the durable ask")
	}
	deadline, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	device := f.device
	device.ctx = deadline
	start := voiceJourneyEvent(t, device, "speech_start")
	canonical, _ := start["canonicalText"].(string)
	// Sentinels are supplied question data, not pinned production copy. The
	// long detail and private intent distinguish canonical content from preview
	// and arbitrary arguments without using the production formatter as oracle.
	last := -1
	for _, token := range []string{"HEADER-FIRST-72", question, "OPTION-FIRST-76", longDetail, "OPTION-SECOND-77", "DETAIL-SECOND-78", "QUESTION-SECOND-80", "OPTION-THIRD-81", "DETAIL-THIRD-82", "OPTION-FOURTH-84", "DETAIL-FOURTH-85"} {
		index := strings.Index(canonical, token)
		if index <= last {
			t.Fatalf("question data lost or reordered at %q", token)
		}
		last = index
	}
	if !strings.Contains(canonical, "WHY-FIRST-74") || !strings.Contains(canonical, "FALLBACK-FIRST-75") || strings.Contains(canonical, "PRIVATE-QUESTION-INTENT-97") {
		t.Fatal("speech did not use only the full public question content")
	}
	peer := f.openSpeaker(t)
	delivery := f.speechRequest(t, peer, start, canonical)
	if delivery.CanonicalSourceItemID != source.ID || delivery.OriginatingRequestID != source.ConversationProvenance.RequestID || delivery.FrontierID != source.ConversationProvenance.FrontierID {
		t.Fatal("question speech was relabeled from current state")
	}
	voiceCorrectionAudio(peer, delivery, "question-response", "question-audio", "AQACAA==")
	voiceCorrectionDone(peer, delivery, "question-response", "question-audio")
	f.expectFinishedAudio(t, delivery, "AQACAA==")
	f.device.send(fmt.Sprintf(`{"type":"played","deliveryId":%q,"generation":%d,"itemId":"question-audio","samples":2,"complete":true}`, delivery.ID, delivery.PresentationGeneration))
	f.waitDelivery(t, delivery.ID, func(d hubcore.SpeechDelivery) bool { return len(d.Items) == 1 && d.Items[0].PlayedSamples == 2 })
	f.device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{"question-audio":2}}`, delivery.PresentationGeneration))
	f.device.send(`{"type":"input_start","sequence":1}`)
	voiceJourneyEvent(t, f.device, "stop")
	truncate := peer.read()
	if truncate["type"] != "conversation.item.truncate" || truncate["item_id"] != "question-audio" || truncate["audio_end_ms"] != float64(0) {
		t.Fatalf("finished question was not retired at its played watermark: %v", truncate)
	}
	peer.send(`{"type":"conversation.item.truncated","item_id":"question-audio","content_index":0,"audio_end_ms":0}`)
	if peer.read()["type"] != "input_audio_buffer.clear" {
		t.Fatal("voice answer did not begin ordinary input")
	}
	peer.send(`{"type":"input_audio_buffer.cleared"}`)
	started := voiceJourneyEvent(t, f.device, "input_started")
	turnID := started["turnId"].(string)
	voiceBargeInterpret(t, peer, f.device, turnID, answer)
	voiceJourneyEvent(t, f.device, "intent")
	intent, err := f.journal.Intent(f.ctx, turnID)
	if err != nil || intent.Text != answer || intent.Method != appwire.MethodTurnStart || intent.State != hubcore.VoiceIntentAccepted {
		t.Fatalf("voice answer was not one ordinary free-text input: %+v %v", intent, err)
	}
	request := f.next(t)
	if !request.Contains(answer) {
		t.Fatal("actual manager request lost the free-text answer")
	}
	var replay appwire.TurnStartResponse
	if err := f.client.Request(f.ctx, intent.Method, intent.Params, &replay); err != nil || replay.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("ordinary answer retry was not idempotent: %+v %v", replay.Receipt, err)
	}
	resolved := f.read(t)
	if resolved.Thread.Evener.AskPending || resolved.Thread.Evener.PendingAsk == nil || len(resolved.Thread.Evener.PendingAsk.Calls) != 0 {
		t.Fatal("ordinary answer did not clear the existing pending owner")
	}
	count := 0
	for _, turn := range resolved.Thread.Turns {
		for _, item := range turn.Items {
			if item.Type == "userMessage" && item.ClientMutationID == intent.MutationID {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("one voice answer produced %d canonical inputs", count)
	}
	_ = f.device.conn.CloseNow()
	f.connect(t)
	f.device.send(fmt.Sprintf(`{"type":"interrupt","generation":%d,"playedSamples":{}}`, delivery.PresentationGeneration+1))
	voiceJourneyEvent(t, f.device, "stop")
	request.RespondToolCall("communicate", communicateArgs(reply))
	freshStart := voiceJourneyEvent(t, f.device, "speech_start")
	freshPeer := f.openSpeaker(t)
	fresh := f.speechRequest(t, freshPeer, freshStart, reply)
	if fresh.CanonicalSourceItemID == source.ID {
		t.Fatal("reconnect replayed the resolved question")
	}
	voiceCorrectionAudio(freshPeer, fresh, "answer-response", "answer-audio", "AgADAA==")
	voiceCorrectionDone(freshPeer, fresh, "answer-response", "answer-audio")
	f.expectFinishedAudio(t, fresh, "AgADAA==")
}
