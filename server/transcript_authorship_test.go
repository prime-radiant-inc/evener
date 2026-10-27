package server

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
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
	t.Cleanup(func() { ps.close(t) })
	ctx := t.Context()
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

func TestTranscriptAuthorshipOrdinaryForkCommunicate(t *testing.T) {
	adapter := &authorshipStreamProvider{release: make(chan struct{})}
	close(adapter.release)
	sess := newMutationReplaySessionWithAdapter(t, adapter)
	for _, input := range []string{"fork-origin", "fork-target"} {
		if _, err := sess.ProcessInput(t.Context(), input, nil); err != nil {
			t.Fatal(err)
		}
	}
	parent := transcriptTurns(t, sess.TranscriptPath())
	divergence := slices.IndexFunc(parent, func(turn schema.Turn) bool {
		return turn.Kind == schema.TurnUserInput && turn.Message.Text() == "fork-target"
	})
	if divergence < 0 {
		t.Fatal("missing divergence input")
	}
	prefix := parent[:divergence]
	var exchange []schema.TurnKind
	deliveryEntry := 0
	for i, turn := range prefix {
		switch turn.Kind {
		case schema.TurnAssistant:
			for _, part := range turn.Message.Content {
				if part.ToolCall != nil && part.ToolCall.ID == "authorship-call" {
					exchange = append(exchange, turn.Kind)
				}
			}
		case schema.TurnCommunicate:
			if turn.Communicate != nil && turn.Communicate.CallID == "authorship-call" {
				exchange = append(exchange, turn.Kind)
				deliveryEntry = i + 1
			}
		case schema.TurnToolResults:
			for _, part := range turn.Message.Content {
				if part.ToolResult != nil && part.ToolResult.ToolCallID == "authorship-call" && !part.ToolResult.IsError {
					exchange = append(exchange, turn.Kind)
				}
			}
		}
	}
	if !reflect.DeepEqual(exchange, []schema.TurnKind{schema.TurnAssistant, schema.TurnCommunicate, schema.TurnToolResults}) {
		t.Fatalf("real delivery exchange=%v", exchange)
	}
	for _, mode := range []string{"edited", "at-user-turn"} {
		t.Run(mode, func(t *testing.T) {
			var childID string
			var err error
			if mode == "edited" {
				childID, err = agent.ForkSession(sess.StateDir(), sess.ID(), divergence+1, "fork-edit", "")
			} else {
				var original string
				childID, original, err = agent.ForkSessionAtUserTurn(sess.StateDir(), sess.ID(), divergence+1, "")
				if original != "fork-target" {
					t.Fatalf("fork target=%q", original)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			path := schema.SessionTranscriptPath(sess.StateDir(), childID)
			child := transcriptTurns(t, path)
			wantEntries := len(prefix)
			if mode == "edited" {
				wantEntries++
			}
			if len(child) != wantEntries {
				t.Fatalf("fork entries=%d want=%d", len(child), wantEntries)
			}
			for i, source := range prefix {
				if child[i].Kind != source.Kind || child[i].Authorship == nil || !child[i].Authorship.Inherited {
					t.Fatalf("copied entry %d=%+v", i, child[i])
				}
			}
			var delivered []appwire.ThreadItem
			for _, turn := range fileHistoryTurns(t, path) {
				for _, item := range turn.Items {
					if item.CallID == "authorship-call" {
						delivered = append(delivered, item)
					}
				}
			}
			if len(delivered) != 1 {
				t.Fatalf("fork delivered %d messages for one call: %+v", len(delivered), delivered)
			}
			want := &appwire.TranscriptAuthorship{Author: appwire.TranscriptAuthor{Kind: appwire.TranscriptAuthorAgent, SessionRef: "local:" + sess.ID()}, Inherited: true}
			if delivered[0].ID != fmt.Sprintf("item_assistant_%d_0", deliveryEntry) || delivered[0].Type != "agentMessage" || delivered[0].Text != "delivered" || !reflect.DeepEqual(delivered[0].Authorship, want) {
				t.Fatalf("fork delivery=%+v want author=%+v", delivered[0], want)
			}
		})
	}
}

// The script controls only model responses. Real delegate tools create and
// steer the leaf, and server callbacks publish the resulting recorded entries.
type namedParentAuthorshipProvider struct {
	mutationProjectionAdapter
	scriptMu                          sync.Mutex
	rootCalls, middleCalls, leafCalls int
	streamed                          bool
	leafID                            string
	identity                          chan string
	leafStarted, steered              chan struct{}
	finishPreview, sendCompleted      chan struct{}
	finishMiddle, finishRoot          chan struct{}
	requests                          []string
	unexpected                        func(string)
}

func authorshipGate(ctx context.Context, gate <-chan struct{}) error {
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *namedParentAuthorshipProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if !requestHasTool(req, "communicate") {
		return llm.Response{Message: llm.Assistant("fixture title")}, nil
	}
	texts := userTexts(req)
	p.scriptMu.Lock()
	role, call := "root", 0
	switch {
	case slices.Contains(texts, "leaf-create"):
		role = "leaf"
		p.leafCalls++
		call = p.leafCalls
	case slices.Contains(texts, "middle-task"):
		role = "middle"
		p.middleCalls++
		call = p.middleCalls
	default:
		p.rootCalls++
		call = p.rootCalls
	}
	request := fmt.Sprintf("role=%s call=%d messages=%d last-role=%s", role, call, len(req.Messages), req.Messages[len(req.Messages)-1].Role)
	p.requests = append(p.requests, request)
	p.scriptMu.Unlock()
	switch role {
	case "root":
		if call == 1 {
			return parityResponse(parityCall("create-middle", "delegate", map[string]any{"prompt": "middle-task", "name": "Middle", "delegation_allowance": 1})), nil
		}
		if call == 2 {
			if err := authorshipGate(ctx, p.finishRoot); err != nil {
				return llm.Response{}, err
			}
			return parityCommunicate("root-final", "root finished", true), nil
		}
		if call == 3 && authorshipNotificationAfter(req, "root-final") {
			return parityCommunicate("root-notification", "root acknowledged middle completion", true), nil
		}
	case "middle":
		switch call {
		case 1:
			return parityResponse(parityCall("create-leaf", "delegate", map[string]any{"prompt": "leaf-create", "name": "Leaf", "delegation_allowance": 0})), nil
		case 2:
			select {
			case p.leafID = <-p.identity:
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
			if err := authorshipGate(ctx, p.leafStarted); err != nil {
				return llm.Response{}, err
			}
			return parityResponse(parityCall("steer-leaf", "delegate_send", map[string]any{"to": p.leafID, "message": "leaf-active-steer", "max_wait_ms": 0})), nil
		case 3:
			close(p.steered)
			if err := authorshipGate(ctx, p.sendCompleted); err != nil {
				return llm.Response{}, err
			}
			return parityResponse(parityCall("resume-leaf", "delegate_send", map[string]any{"to": p.leafID, "message": "leaf-completed-send", "max_wait_ms": 0})), nil
		case 4:
			if err := authorshipGate(ctx, p.finishMiddle); err != nil {
				return llm.Response{}, err
			}
			return parityCommunicate("middle-final", "middle finished", true), nil
		case 5:
			if authorshipNotificationAfter(req, "middle-final") {
				return parityCommunicate("middle-notification", "middle acknowledged leaf completion", true), nil
			}
		}
	case "leaf":
		if call == 1 || call == 2 {
			return parityCommunicate(fmt.Sprintf("leaf-final-%d", call), fmt.Sprintf("leaf final %d", call), true), nil
		}
	}
	p.scriptMu.Lock()
	requests := slices.Clone(p.requests)
	p.scriptMu.Unlock()
	p.unexpected(fmt.Sprintf("unexpected fixture request: %s, requests=%v", request, requests))
	return llm.Response{}, context.Canceled
}

// A notification follows the fixture's terminal tool result with a user-role
// carrier. Match that structure and our call ID, never notification prose.
func authorshipNotificationAfter(req llm.Request, callID string) bool {
	if req.Messages[len(req.Messages)-1].Role != llm.RoleUser {
		return false
	}
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if part.ToolCall != nil && part.ToolCall.ID == callID {
				return true
			}
		}
	}
	return false
}

func (p *namedParentAuthorshipProvider) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	if !requestHasTool(req, "communicate") || !slices.Contains(userTexts(req), "leaf-create") {
		return nil, llm.ErrStreamUnsupported
	}
	p.scriptMu.Lock()
	first := !p.streamed
	p.streamed = true
	p.scriptMu.Unlock()
	if !first {
		return nil, llm.ErrStreamUnsupported
	}
	close(p.leafStarted)
	stream := llm.NewChanStream(nil)
	go func() {
		defer stream.CloseSend()
		if authorshipGate(ctx, p.steered) != nil {
			return
		}
		resp := parityResponse(llm.ContentPart{Kind: llm.ContentText, Text: "leaf answer"}, llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "leaf thought"}}, parityCommunicateCall("leaf-preview", "leaf progress", false))
		resp.Provider, resp.Model = p.Name(), req.Model
		call := resp.Message.Content[2].ToolCall
		stream.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
		stream.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, Delta: "leaf answer"})
		stream.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "leaf thought"})
		stream.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name, Type: "function"}})
		stream.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: call, Delta: string(call.Arguments)})
		if authorshipGate(ctx, p.finishPreview) != nil {
			return
		}
		stream.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: call})
		stream.Send(llm.StreamEvent{Type: llm.StreamEventFinish, Response: &resp})
	}()
	return stream, nil
}

func TestTranscriptAuthorshipNamedParentPublication(t *testing.T) {
	p := &namedParentAuthorshipProvider{identity: make(chan string, 1), leafStarted: make(chan struct{}), steered: make(chan struct{}), finishPreview: make(chan struct{}), sendCompleted: make(chan struct{}), finishMiddle: make(chan struct{}), finishRoot: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	p.unexpected = func(message string) {
		t.Error(message)
		cancel()
	}
	sess := newMutationReplaySessionWithAdapter(t, p)
	ps := bridgeParitySession(t, sess, sess.StateDir())
	defer ps.close(t)
	defer cancel()
	path := func(id string) string { return filepath.Join(sess.StateDir(), "sessions", id+".transcript.jsonl") }
	ps.srv.SetDescendantTranscriptPathFunc(path)
	sess.SetDescendantEventFunc(func(ev events.SessionEvent) {
		ps.srv.RecordDescendantAppEvent(sess.ID(), ev)
		ps.mu.Lock()
		ps.seen = append(ps.seen, ev)
		ps.mu.Unlock()
		ps.cond.Broadcast()
	})
	done := make(chan error, 1)
	go func() { _, err := sess.ProcessInput(ctx, "root-task", nil); done <- err }()
	created := func(call string) events.DelegateUpdatedData {
		ev := ps.await(t, events.EventDelegateUpdated, func(ev events.SessionEvent) bool {
			d, ok := ev.Data.(events.DelegateUpdatedData)
			return ok && d.OriginToolCallID == call && d.ChildSessionID != "" && d.Lifecycle == "running"
		})
		return ev.Data.(events.DelegateUpdatedData)
	}
	middle, leaf := created("create-middle"), created("create-leaf")
	ps.await(t, events.EventRoundStarted, func(ev events.SessionEvent) bool { return ev.SessionID == leaf.ChildSessionID })
	if middle.OwnerSessionID != sess.ID() || leaf.OwnerSessionID != sess.ID() || leaf.ParentDelegateID != middle.DelegateID || middle.ChildSessionID == sess.ID() {
		t.Fatalf("physical/logical tree mismatch: middle=%+v leaf=%+v", middle, leaf)
	}
	parentAuthor := appwire.TranscriptAuthorship{Author: appwire.TranscriptAuthor{Kind: appwire.TranscriptAuthorAgent, SessionRef: "local:" + middle.ChildSessionID, OwnerRef: "local:" + sess.ID(), DelegateID: middle.DelegateID, DelegateName: "Middle"}}
	leafAuthor := appwire.TranscriptAuthorship{Author: appwire.TranscriptAuthor{Kind: appwire.TranscriptAuthorAgent, SessionRef: "local:" + leaf.ChildSessionID, OwnerRef: "local:" + middle.ChildSessionID, DelegateID: leaf.DelegateID, DelegateName: "Leaf"}}
	matchesText := func(got, text string) bool {
		// Active steering includes the existing parent-attribution wrapper.
		// Select only by our sentinel, never by production wording.
		return got == text || text == "leaf-active-steer" && strings.HasSuffix(got, text)
	}
	assertItems := func(turns []appwire.Turn, texts []string, want appwire.TranscriptAuthorship) {
		t.Helper()
		for _, text := range texts {
			var matches []appwire.ThreadItem
			for _, turn := range turns {
				for _, item := range turn.Items {
					if matchesText(item.Text, text) {
						matches = append(matches, item)
					}
				}
			}
			if len(matches) != 1 {
				t.Fatalf("published %q count=%d, want 1", text, len(matches))
			}
			if !reflect.DeepEqual(matches[0].Authorship, &want) {
				t.Fatalf("published %q author=%+v want=%+v", text, matches[0].Authorship, want)
			}
		}
	}
	client := dialServerAppWire(t, ps.srv)
	initial, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + leaf.ChildSessionID, Subscribe: true, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	assertItems(initial.Thread.Turns, []string{"leaf-create"}, parentAuthor)
	p.identity <- leaf.DelegateID
	ps.await(t, events.EventCommunicatePreviewDelta, func(ev events.SessionEvent) bool { return ev.SessionID == leaf.ChildSessionID })
	live := readWholeThread(t, ps.srv, leaf.ChildSessionID)
	assertItems(live.Thread.Turns, []string{"leaf-create", "leaf-active-steer"}, parentAuthor)
	var chunks []appwire.ThreadItem
	for _, overlay := range live.Overlay {
		if overlay.Kind == appwire.OverlayStream || overlay.Kind == appwire.OverlayPreview {
			chunks = append(chunks, overlay.Item)
		}
	}
	if len(chunks) != 3 {
		t.Fatalf("leaf first chunks=%d, want answer, thought and preview", len(chunks))
	}
	assertItems([]appwire.Turn{{Items: chunks}}, []string{"leaf answer", "leaf thought", "leaf progress"}, leafAuthor)
	firstUpserts := map[string]appwire.OverlayItem{}
	for _, notification := range ps.srv.appNotifier.ReplayAfter(0, "") {
		if notification.Notification.Method != appwire.NotifyOverlayUpserted {
			continue
		}
		upsert := notificationParams[appwire.OverlayUpsertedParams](t, notification)
		if upsert.ThreadID == leaf.ChildSessionID && (upsert.Item.Kind == appwire.OverlayStream || upsert.Item.Kind == appwire.OverlayPreview) {
			if _, seen := firstUpserts[upsert.Item.Key]; !seen {
				firstUpserts[upsert.Item.Key] = upsert.Item
			}
		}
	}
	if len(firstUpserts) != 3 {
		t.Fatalf("first leaf upsert publications=%d, want 3", len(firstUpserts))
	}
	for key, overlay := range firstUpserts {
		if !reflect.DeepEqual(overlay.Item.Authorship, &leafAuthor) {
			t.Fatalf("first published %s author=%+v want=%+v", key, overlay.Item.Authorship, leafAuthor)
		}
	}
	close(p.finishPreview)
	awaitIdle := func(id string, generation uint64) {
		ps.await(t, events.EventDelegateUpdated, func(ev events.SessionEvent) bool {
			d, ok := ev.Data.(events.DelegateUpdatedData)
			return ok && d.DelegateID == id && d.RunGeneration == generation && d.Lifecycle == "idle" && d.Outcome == "completed"
		})
	}
	awaitIdle(leaf.DelegateID, 1)
	close(p.sendCompleted)
	awaitIdle(leaf.DelegateID, 2)
	close(p.finishMiddle)
	awaitIdle(middle.DelegateID, 1)
	awaitIdle(middle.DelegateID, 2)
	close(p.finishRoot)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	inputs := []string{"leaf-create", "leaf-active-steer", "leaf-completed-send"}
	outputs := []string{"leaf answer", "leaf thought", "leaf progress", "leaf final 1", "leaf final 2"}
	entries := transcriptTurns(t, path(leaf.ChildSessionID))
	ps.published.await(t, ps.srv.appHistoryForID(leaf.ChildSessionID), int64(len(entries)))
	// Replay actual history publications onto the subscribed initial snapshot.
	replayed := newHistoryClient(initial)
	updates := 0
	for _, notification := range ps.srv.appNotifier.ReplayAfter(0, "") {
		if notification.Notification.Method == appwire.NotifyHistoryUpdated {
			update := notificationParams[appwire.HistoryUpdatedParams](t, notification)
			if update.ThreadID == leaf.ChildSessionID {
				updates++
				replayed.apply(t, update)
			}
		}
	}
	if updates == 0 {
		t.Fatal("no leaf history publications")
	}
	for _, turns := range [][]appwire.Turn{replayed.turnsInOrder(), readWholeThread(t, ps.srv, leaf.ChildSessionID).Thread.Turns, fileHistoryTurns(t, path(leaf.ChildSessionID))} {
		assertItems(turns, inputs, parentAuthor)
		assertItems(turns, outputs, leafAuthor)
	}
	for _, text := range inputs {
		var matches []schema.Turn
		for _, turn := range entries {
			if (turn.Kind == schema.TurnUserInput || turn.Kind == schema.TurnSteering) && matchesText(turn.Message.Text(), text) {
				matches = append(matches, turn)
			}
		}
		if len(matches) != 1 {
			t.Fatalf("persisted %q count=%d, want 1", text, len(matches))
		}
		if text == "leaf-active-steer" && (matches[0].Kind != schema.TurnSteering || matches[0].SteeringSource != events.SteeringSourceParent) {
			t.Fatalf("active input lost its parent steering source: %+v", matches[0])
		}
		want := &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: middle.ChildSessionID, OwnerSessionID: sess.ID(), DelegateID: middle.DelegateID, DelegateName: "Middle"}}
		if !reflect.DeepEqual(matches[0].Authorship, want) {
			t.Fatalf("persisted %q author=%+v want=%+v", text, matches[0].Authorship, want)
		}
	}
	p.scriptMu.Lock()
	defer p.scriptMu.Unlock()
	if p.rootCalls != 3 || p.middleCalls != 5 || p.leafCalls != 2 || !p.streamed {
		t.Fatalf("script calls root=%d middle=%d leaf=%d streamed=%v", p.rootCalls, p.middleCalls, p.leafCalls, p.streamed)
	}
}
