package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

// The real manager executes ask_user and communicate in one batch. A foreground
// process waits on an external HTTP response, keeping persistToolResults pending.
// Feed those real projected reads to the real selector at the exact interleaving
// rather than hoping the socket loop wins a scheduler race before persistence.
func TestE2E_VoiceQuestionCompletionKeepsSpeechOrder(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("external tool barrier requires curl")
	}
	batches := make(chan []map[string]any, 1)
	f := newVoiceCorrectionStackOnProvider(t, func(upstream string) string {
		return voiceQuestionBatchProvider(t, upstream, batches)
	})
	_ = f.device.conn.CloseNow()
	baseline := f.read(t)
	bridge, device, speaker := voiceQuestionSelector(t, f, baseline)
	entered, release := make(chan struct{}), make(chan struct{})
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(gate.Close)
	t.Cleanup(func() { close(release) })
	question := map[string]any{"question": "BATCH-QUESTION-127", "options": []any{
		map[string]any{"label": "BATCH-A-131", "detail": "BATCH-A-DETAIL-137"},
		map[string]any{"label": "BATCH-B-139", "detail": "BATCH-B-DETAIL-149"},
	}}
	message := communicateArgs("BATCH-COMMUNICATE-151")
	message["end_turn"] = false
	batches <- []map[string]any{
		voiceQuestionTool("batch-ask", "ask_user", question),
		voiceQuestionTool("batch-communicate", "communicate", message),
		voiceQuestionTool("batch-gate", "shell", map[string]any{"command": "curl -fsS " + gate.URL, "mode": "foreground", "intent": "Holding the external batch boundary"}),
	}
	f.admit(t, "BATCH-INPUT-157", false)
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal("real batch never reached its external process barrier")
	}
	interim := f.read(t)
	ask := voiceQuestionCall(interim, "batch-ask")
	committed := voiceCorrectionItem(interim, "BATCH-COMMUNICATE-151")
	if ask.Status != appwire.TurnStatusInProgress || ask.Position == nil || ask.ConversationProvenance == nil || committed.Position == nil || appitempaging.ComparePosition(*ask.Position, *committed.Position) >= 0 {
		t.Fatalf("batch did not expose an unfinished earlier question and committed later message: ask=%+v message=%+v", ask, committed)
	}
	t.Logf("real batch boundary reached: unfinished question=%s position=%+v, later communicate=%s position=%+v", ask.ID, *ask.Position, committed.ID, *committed.Position)
	oldCursor := appwire.ClonePointer(bridge.speechCursor)
	if err := bridge.selectSpeech(interim); err != nil {
		t.Fatal(err)
	}
	if bridge.delivery != nil || !reflect.DeepEqual(oldCursor, bridge.speechCursor) {
		t.Fatalf("selector advanced past unfinished question: delivery=%+v cursor=%+v", bridge.delivery, bridge.speechCursor)
	}
	// Unblock without relying on elapsed time; the subsequent real history
	// notification is the completion barrier for canonical question state.
	release <- struct{}{}
	completed := f.waitRead(t, "batched question committed", func(read appwire.ThreadReadResponse) bool {
		return voiceQuestionCall(read, "batch-ask").Status == appwire.TurnStatusCompleted && read.Thread.Evener.ActiveTurnID == ""
	})
	finished := voiceQuestionCall(completed, "batch-ask")
	if finished.ID != ask.ID || !reflect.DeepEqual(finished.Position, ask.Position) || completed.Thread.Evener.PendingAsk == nil || len(completed.Thread.Evener.PendingAsk.Calls) != 1 {
		t.Fatal("question completion changed its opener identity or lost the pending owner")
	}
	if err := bridge.selectSpeech(completed); err != nil {
		t.Fatal(err)
	}
	start := voiceJourneyEvent(t, device, "speech_start")
	if start["sourceItemId"] != ask.ID {
		t.Fatal("completed unanswered question was skipped for the later communicate")
	}
	canonical := start["canonicalText"].(string)
	if !strings.Contains(canonical, "BATCH-QUESTION-127") || !strings.Contains(canonical, "BATCH-A-131") || !strings.Contains(canonical, "BATCH-B-139") {
		t.Fatal("selected source lost canonical public question data")
	}
	delivery := f.speechRequest(t, speaker, start, canonical)
	voiceCorrectionAudio(speaker, delivery, "batch-response", "batch-audio", "AQACAA==")
	select {
	case result := <-bridge.work:
		if result.err != nil {
			t.Fatal(result.err)
		}
		bridge.workCancel()
		bridge.workCancel, bridge.workKind = nil, ""
	case <-f.ctx.Done():
		t.Fatal("question speech setup did not complete")
	}
	// Simulate only the external playback boundary. Production provider parsing,
	// delivery persistence and cursor selection remain real.
	voiceCorrectionDone(speaker, delivery, "batch-response", "batch-audio")
	for !bridge.delivery.OutputComplete {
		select {
		case event := <-bridge.provider.Events():
			if err := bridge.providerEvent(event); err != nil {
				t.Fatal(err)
			}
		case <-f.ctx.Done():
			t.Fatal("question output never completed")
		}
	}
	for _, kind := range []string{"audio", "speech_end", "delivery_end"} {
		event := voiceJourneyEvent(t, device, kind)
		if event["deliveryId"] != delivery.ID || event["generation"] != float64(delivery.PresentationGeneration) {
			t.Fatal("question playback events changed delivery identity")
		}
	}
	if err := bridge.recordPlayed(voiceControl{DeliveryID: delivery.ID, Generation: delivery.PresentationGeneration, ItemID: "batch-audio", Samples: 2, Complete: true}); err != nil {
		t.Fatal(err)
	}
	if err := bridge.selectSpeech(completed); err != nil {
		t.Fatal(err)
	}
	next := voiceJourneyEvent(t, device, "speech_start")
	if next["sourceItemId"] != committed.ID {
		t.Fatal("deferring the unfinished question lost the subsequent communicate")
	}
}

func voiceQuestionCall(read appwire.ThreadReadResponse, callID string) appwire.ThreadItem {
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			if item.CallID == callID && item.ToolName == "ask_user" {
				return item
			}
		}
	}
	return appwire.ThreadItem{}
}

func voiceQuestionSelector(t *testing.T, f *voiceCorrectionStack, baseline appwire.ThreadReadResponse) (*voiceBridge, realtimePeer, realtimePeer) {
	t.Helper()
	config, peers := realtimeTestServer(t)
	conn, _, err := websocket.Dial(f.ctx, config.Endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	device := <-peers
	provider, speaker := openRealtimeTest(t)
	binding, err := f.journal.Binding(f.ctx, f.armed.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	b := &voiceBridge{ctx: ctx, cancel: cancel, conn: conn, client: f.client, journal: f.journal, binding: binding, provider: provider, generation: 100, work: make(chan voiceWorkResult)}
	b.captureSpeechBaseline(baseline)
	b.provenance = baseline.Thread.Evener.ConversationProvenance
	t.Cleanup(func() { cancel(); b.workers.Wait() })
	return b, device, speaker
}

func voiceQuestionTool(id, name string, args map[string]any) map[string]any {
	raw, _ := json.Marshal(args)
	return map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}
}

// Everything except the queued multi-call response is served by the ordinary
// scripted LLM. This HTTP server is the external model boundary, not an Evener
// admission/history mock.
func voiceQuestionBatchProvider(t *testing.T, upstream string, batches <-chan []map[string]any) string {
	t.Helper()
	target, err := url.Parse(strings.TrimSuffix(upstream, "/v1"))
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var request struct{ Tools []any }
			if json.Unmarshal(raw, &request) == nil && len(request.Tools) != 0 {
				select {
				case calls := <-batches:
					for i, call := range calls {
						call["index"] = i
					}
					w.Header().Set("Content-Type", "text/event-stream")
					for _, choice := range []map[string]any{
						{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": calls}},
						{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"},
					} {
						chunk, _ := json.Marshal(map[string]any{"id": "voice-batch", "choices": []any{choice}})
						_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
					}
					_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
					return
				default:
				}
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1"
}
