package server

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

type authorshipStreamProvider struct {
	mutationProjectionAdapter
	release chan struct{}
}

func (a *authorshipStreamProvider) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if !requestHasTool(req, "communicate") {
		return nil, llm.ErrStreamUnsupported
	}
	resp := parityResponse(llm.ContentPart{Kind: llm.ContentText, Text: "answer"}, llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thought"}}, parityCommunicateCall("authorship-call", "delivered", true))
	resp.Provider = a.Name()
	resp.Model = req.Model
	stream := llm.NewChanStream(nil)
	go func() {
		defer stream.CloseSend()
		stream.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
		stream.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: "answer"})
		stream.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "thought"})
		call := resp.Message.Content[2].ToolCall
		stream.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name, Type: "function"}})
		stream.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: call, Delta: string(call.Arguments)})
		select {
		case <-a.release:
		case <-ctx.Done():
			return
		}
		stream.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: call})
		stream.Send(llm.StreamEvent{Type: llm.StreamEventFinish, Response: &resp})
	}()
	return stream, nil
}

func TestTranscriptAuthorshipLiveReplay(t *testing.T) {
	adapter := &authorshipStreamProvider{release: make(chan struct{})}
	sess := newMutationReplaySessionWithAdapter(t, adapter)
	ps := bridgeParitySession(t, sess, sess.StateDir())
	defer ps.close(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "authorship", ExpectedInstanceID: sess.ID(), Input: []appwire.InputItem{{Type: "text", Text: "question"}}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := sess.ProcessClientMutationStart(ctx, nil); done <- err }()
	ps.await(t, events.EventCommunicatePreviewDelta, nil)
	assert := func(item appwire.ThreadItem, kind string) {
		t.Helper()
		raw, _ := json.Marshal(item)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		var got any
		if len(fields["authorship"]) > 0 {
			if err := json.Unmarshal(fields["authorship"], &got); err != nil {
				t.Fatal(err)
			}
		}
		author := map[string]any{"kind": kind}
		if kind == "agent" {
			author["sessionRef"] = "local:" + sess.ID()
		}
		want := map[string]any{"author": author, "inherited": false}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s %q authorship=%v want=%v", item.Type, item.Text, got, want)
		}
	}
	before := readWholeThread(t, ps.srv, sess.ID())
	streams := 0
	for _, item := range before.Overlay {
		if item.Kind == appwire.OverlayStream || item.Kind == appwire.OverlayPreview {
			streams++
			assert(item.Item, "agent")
		}
	}
	if streams != 3 {
		t.Errorf("streams=%d want 3", streams)
	}
	client := dialServerAppWire(t, ps.srv)
	rejoin, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + sess.ID(), Subscribe: true, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range rejoin.Overlay {
		if item.Kind == appwire.OverlayStream || item.Kind == appwire.OverlayPreview {
			assert(item.Item, "agent")
		}
	}
	close(adapter.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	live := ps.liveTurns(t)
	for _, turns := range [][]appwire.Turn{live, readWholeThread(t, ps.srv, sess.ID()).Thread.Turns, fileHistoryTurns(t, sess.TranscriptPath())} {
		counts := map[string]int{}
		for _, turn := range turns {
			for _, item := range turn.Items {
				switch item.Type {
				case "userMessage":
					assert(item, "human")
					counts[item.Text]++
				case "agentMessage", "reasoning":
					assert(item, "agent")
					counts[item.Text]++
				}
			}
		}
		for _, text := range []string{"question", "answer", "thought", "delivered"} {
			if counts[text] != 1 {
				t.Errorf("%q count=%d", text, counts[text])
			}
		}
	}
	assertParity(t, "authorship", ps, nil)
}
