package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	middleFinalStarted                chan struct{}
	postIdle                          bool
	postIdleObserved, finishPostIdle  chan struct{}
	requests                          []string
	unexpected                        func(string)
	diagnoseMiddle                    func(llm.Request) string
	notificationAfter                 func(string, llm.Request) bool
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
	diagnose := p.diagnoseMiddle
	after := p.notificationAfter
	p.scriptMu.Unlock()
	if role == "middle" && call == 5 && diagnose != nil {
		diagnostic := diagnose(req)
		p.scriptMu.Lock()
		p.requests = append(p.requests, "middle diagnostic="+diagnostic)
		p.scriptMu.Unlock()
	}
	switch role {
	case "root":
		if call == 1 {
			args := map[string]any{"prompt": "middle-task", "name": "Middle", "delegation_allowance": 1}
			if p.postIdle {
				args["watch_parent"] = true
			}
			return parityResponse(parityCall("create-middle", "delegate", args)), nil
		}
		if call == 2 {
			if err := authorshipGate(ctx, p.finishRoot); err != nil {
				return llm.Response{}, err
			}
			return parityCommunicate("root-final", "root finished", true), nil
		}
		if call == 3 && after("root", req) {
			return parityCommunicate("root-notification", "root acknowledged middle completion", true), nil
		}
		if p.postIdle && call == 4 {
			return parityCommunicate("post-idle-publication", "post-idle source publication", true), nil
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
			close(p.middleFinalStarted)
			if err := authorshipGate(ctx, p.finishMiddle); err != nil {
				return llm.Response{}, err
			}
			return parityCommunicate("middle-final", "middle finished", true), nil
		case 5:
			if after("middle", req) {
				if p.postIdle {
					return parityResponse(parityCall("post-idle-watch", "job_watch", map[string]any{
						"operation": "create", "source": "parent", "events": []string{"communicate"}, "every": 3,
					})), nil
				}
				return parityCommunicate("middle-notification", "middle acknowledged leaf completion", true), nil
			}
		case 6:
			// job_watch is nonterminal. Its checked result gets one ordinary
			// continuation inside this same notification input, not another run.
			if p.postIdle && after("watch-created", req) {
				return parityCommunicate("middle-notification", "middle acknowledged leaf completion", true), nil
			}
		case 7:
			if p.postIdle && authorshipNotificationAfter(req, "middle-notification") && after("post-idle", req) {
				close(p.postIdleObserved)
				if err := authorshipGate(ctx, p.finishPostIdle); err != nil {
					return llm.Response{}, err
				}
				return parityCommunicate("post-idle-observed", "middle observed post-idle publication", true), nil
			}
		}
	case "leaf":
		if call == 2 {
			// Publish after the middle's last ordinary request starts. The real
			// delivery is pending when its accepted terminal enters finalization.
			if err := authorshipGate(ctx, p.middleFinalStarted); err != nil {
				return llm.Response{}, err
			}
		}
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

// Read the real journals without importing the agent's internal folds. These
// wire coordinates independently bind canonical custody to source settlement.
type namedParentWatchKey struct {
	VisibleSessionID        string `json:"visible_session_id"`
	WatchID                 string `json:"watch_id"`
	WatchTarget             string `json:"watch_target"`
	ResolvedWatchedIdentity string `json:"resolved_watched_identity"`
	ResolvedSendTo          string `json:"resolved_send_to"`
	WatchGeneration         string `json:"watch_generation"`
	RestartSeq              int64  `json:"restart_seq"`
}

type namedParentWatchRegistration struct {
	Generation       string `json:"generation"`
	OwnerSessionID   string `json:"owner_session_id"`
	VisibleSessionID string `json:"visible_session_id"`
	Target           string `json:"target"`
	SendTo           string `json:"send_to"`
	ConfigHash       string `json:"config_hash"`
	Config           *struct {
		Target                   string   `json:"target"`
		Events                   []string `json:"events"`
		Every                    int      `json:"every"`
		ReceiverSessionID        string   `json:"receiver_session_id"`
		ReceiverDelegateID       string   `json:"receiver_delegate_id"`
		Source                   string   `json:"source"`
		SourceDelegateID         string   `json:"source_delegate_id"`
		SourceDelegateGeneration uint64   `json:"source_delegate_generation"`
		StableReceiver           bool     `json:"stable_receiver"`
	} `json:"config"`
}

type namedParentWatchSend struct {
	Key                      namedParentWatchKey `json:"key"`
	DeliveryID               string              `json:"delivery_id"`
	UpdateSeq                uint64              `json:"update_seq"`
	Frame                    string              `json:"frame"`
	ReceiverSessionID        string              `json:"receiver_session_id"`
	ReceiverDelegateID       string              `json:"receiver_delegate_id"`
	SourceDelegateID         string              `json:"source_delegate_id"`
	SourceDelegateGeneration uint64              `json:"source_delegate_generation"`
	StableReceiver           bool                `json:"stable_receiver"`
	CallbackFrame            bool                `json:"callback_frame"`
	EndNotice                bool                `json:"end_notice"`
}

type namedParentWatchBinding struct {
	watchID      string
	registration namedParentWatchRegistration
	key          namedParentWatchKey
}

type namedParentSourceEvent struct {
	Kind       string                        `json:"kind"`
	DelegateID string                        `json:"delegate_id"`
	WatchID    string                        `json:"watch_id"`
	Watch      *namedParentWatchRegistration `json:"watch"`
	RunStarted *struct {
		Generation uint64 `json:"generation"`
	} `json:"run_started"`
	RunFinished *struct {
		Generation uint64 `json:"generation"`
		DeliveryID string `json:"delivery_id"`
	} `json:"run_finished"`
	DeliveryAcknowledged *struct {
		DeliveryID string `json:"delivery_id"`
	} `json:"delivery_acknowledged"`
	WatchSend *namedParentWatchSend `json:"watch_send"`
}

func namedParentJournal(t *testing.T, path string) []namedParentSourceEvent {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var events []namedParentSourceEvent
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event namedParentSourceEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func namedParentRuns(t *testing.T, journal, delegateID string, starts, finishes uint64) map[uint64]string {
	t.Helper()
	var gotStarts, gotFinishes []uint64
	deliveries := make(map[uint64]string)
	for _, event := range namedParentJournal(t, journal) {
		if event.DelegateID != delegateID {
			continue
		}
		if event.RunStarted != nil {
			gotStarts = append(gotStarts, event.RunStarted.Generation)
		}
		if event.RunFinished != nil {
			gotFinishes = append(gotFinishes, event.RunFinished.Generation)
			deliveries[event.RunFinished.Generation] = event.RunFinished.DeliveryID
		}
	}
	sequence := func(n uint64) []uint64 {
		var generations []uint64
		for generation := uint64(1); generation <= n; generation++ {
			generations = append(generations, generation)
		}
		return generations
	}
	if !reflect.DeepEqual(gotStarts, sequence(starts)) || !reflect.DeepEqual(gotFinishes, sequence(finishes)) {
		t.Errorf("%s starts=%v finishes=%v, want %v/%v", delegateID, gotStarts, gotFinishes, sequence(starts), sequence(finishes))
	}
	return deliveries
}

// This is not a broadened last-role classifier. Only an accepted terminal plus
// the exact genuine canonical delivery, actually covered and not yet receipted,
// admits the one independently specified finalization request.
func namedParentPendingAfterTerminal(t *testing.T, req llm.Request, callID, path, id string) bool {
	t.Helper()
	accepted := false
	for _, message := range req.Messages {
		for _, part := range message.Content {
			result := part.ToolResult
			if result == nil || result.ToolCallID != callID || result.IsError {
				continue
			}
			raw, _ := json.Marshal(result.Content)
			if text, ok := result.Content.(string); ok {
				raw = []byte(text)
			}
			var outcome struct {
				Accepted bool `json:"accepted"`
				EndTurn  bool `json:"end_turn"`
			}
			if json.Unmarshal(raw, &outcome) == nil && outcome.Accepted && outcome.EndTurn {
				accepted = true
			}
		}
	}
	carriers, receipts := 0, 0
	covered := false
	for _, turn := range transcriptTurns(t, path) {
		if id != "" && turn.AttentionID == id {
			carriers++
			if text := turn.Message.Text(); text != "" {
				for _, message := range req.Messages {
					covered = covered || strings.Contains(message.Text(), text)
				}
			}
		}
		for _, observed := range turn.ObservedNotificationIDs {
			if observed == id {
				receipts++
			}
		}
	}
	if !accepted || carriers != 1 || !covered || receipts != 0 {
		t.Errorf("%s pending finalization proof: accepted=%t id=%q carriers=%d covered=%t receipts=%d", callID, accepted, id, carriers, covered, receipts)
		return false
	}
	return true
}

func requireNamedParentObservedOnce(t *testing.T, path, id string) {
	t.Helper()
	carriers, receipts, consumed := 0, 0, 0
	for _, turn := range transcriptTurns(t, path) {
		if turn.AttentionID == id {
			carriers++
		}
		for _, observed := range turn.ObservedNotificationIDs {
			if observed == id {
				if turn.Kind != schema.TurnAssistant || turn.TurnID == "" {
					t.Errorf("receipt for %s lacks successful assistant identity: %+v", id, turn)
				}
				receipts++
			}
		}
		if resolution := turn.AttentionResolution; resolution != nil && resolution.AttentionID == id {
			if resolution.Disposition != "consumed" {
				t.Errorf("%s disposition = %s", id, resolution.Disposition)
			}
			consumed++
		}
	}
	if id == "" || carriers != 1 || receipts != 1 || consumed != 1 {
		t.Fatalf("%s carriers=%d receipts=%d consumed=%d, want one each", id, carriers, receipts, consumed)
	}
}

func requireNamedParentSourceAck(t *testing.T, journal, id string) {
	t.Helper()
	acks := 0
	for _, event := range namedParentJournal(t, journal) {
		if event.DeliveryAcknowledged != nil && event.DeliveryAcknowledged.DeliveryID == id {
			acks++
		}
	}
	if id == "" || acks != 1 {
		t.Fatalf("source %q acknowledgments=%d, want one", id, acks)
	}
}

func namedParentCreatedWatch(t *testing.T, req llm.Request, journal, ownerID, receiverSessionID, receiverDelegateID string) namedParentWatchBinding {
	t.Helper()
	var result struct {
		WatchID          string   `json:"watch_id"`
		Source           string   `json:"source"`
		Watching         bool     `json:"watching"`
		Events           []string `json:"events"`
		ReplacedExisting bool     `json:"replaced_existing"`
		Fired            bool     `json:"fired"`
	}
	results := 0
	for _, message := range req.Messages {
		for _, part := range message.Content {
			toolResult := part.ToolResult
			if toolResult == nil || toolResult.ToolCallID != "post-idle-watch" {
				continue
			}
			results++
			// job_watch renders prose in Content and puts its public structured
			// result in ToolState, unlike communicate's JSON response Content.
			if toolResult.Name != "job_watch" || toolResult.IsError || json.Unmarshal(toolResult.ToolState, &result) != nil {
				t.Fatalf("post-idle watch creation failed: %+v", toolResult)
			}
		}
	}
	if results != 1 || result.WatchID == "" || result.Source != "parent" || !result.Watching || !slices.Equal(result.Events, []string{"communicate"}) || result.ReplacedExisting || result.Fired {
		t.Fatalf("post-idle watch results=%d state=%+v", results, result)
	}
	binding := namedParentWatchBinding{watchID: result.WatchID}
	registrations := 0
	for _, event := range namedParentJournal(t, journal) {
		if event.WatchID != result.WatchID || event.Watch == nil {
			continue
		}
		if event.Kind != "watch_registered" {
			t.Fatalf("watch ended during installation: %s", event.Kind)
		}
		registrations++
		binding.registration = *event.Watch
	}
	registration := binding.registration
	config := registration.Config
	if registrations != 1 || registration.Generation == "" || registration.ConfigHash == "" || registration.OwnerSessionID != ownerID || registration.VisibleSessionID != ownerID || registration.Target != "caller" || registration.SendTo != "stable_watch_receiver" || config == nil {
		t.Fatalf("post-idle registration count=%d value=%+v", registrations, registration)
	}
	if config.Target != registration.Target || config.Source != "parent" || !config.StableReceiver || config.ReceiverSessionID != receiverSessionID || config.ReceiverDelegateID != receiverDelegateID || config.SourceDelegateID != "" || config.SourceDelegateGeneration != 0 || config.Every != 3 || !slices.Equal(config.Events, []string{"communicate"}) {
		t.Fatalf("post-idle watcher ownership/source mismatch: %+v", config)
	}
	binding.key = namedParentWatchKey{
		VisibleSessionID: registration.VisibleSessionID, WatchID: binding.watchID,
		WatchTarget: registration.Target, ResolvedWatchedIdentity: registration.Target,
		ResolvedSendTo: registration.SendTo, WatchGeneration: registration.Generation,
	}
	return binding
}

// Before publication there must be no frame. At provider entry the canonical
// wake may precede source acknowledgement. After the publisher returns the same
// pending identity must have exactly one delivered transition, never a drop.
func namedParentPostIdleSource(t *testing.T, journal string, binding namedParentWatchBinding, phase string) string {
	t.Helper()
	pending, delivered, registrations := 0, 0, 0
	var admitted namedParentWatchSend
	for _, event := range namedParentJournal(t, journal) {
		if event.WatchID == binding.watchID && event.Watch != nil {
			if event.Kind != "watch_registered" || !reflect.DeepEqual(*event.Watch, binding.registration) {
				t.Fatalf("post-idle watch changed: %+v", event)
			}
			registrations++
		}
		state := event.WatchSend
		if state == nil || state.Key.WatchID != binding.watchID && state.ReceiverSessionID != binding.registration.Config.ReceiverSessionID {
			continue
		}
		if state.Key != binding.key || state.ReceiverSessionID != binding.registration.Config.ReceiverSessionID || state.ReceiverDelegateID != binding.registration.Config.ReceiverDelegateID || !state.StableReceiver || state.SourceDelegateID != "" || state.SourceDelegateGeneration != 0 || state.CallbackFrame || state.EndNotice || state.DeliveryID == "" || state.UpdateSeq != 1 || state.Frame == "" {
			t.Fatalf("post-idle frame has wrong watch/receiver/source identity: %+v", state)
		}
		switch event.Kind {
		case "watch_send_pending":
			pending++
			if pending != 1 || delivered != 0 {
				t.Fatalf("duplicate or out-of-order pending frame: %+v", state)
			}
			admitted = *state
		case "watch_send_delivered":
			delivered++
			if pending != 1 || delivered != 1 || *state != admitted {
				t.Fatalf("duplicate, unmatched or out-of-order delivered frame: %+v", state)
			}
		default:
			t.Fatalf("unexpected post-idle source transition %s", event.Kind)
		}
	}
	wantPending := 1
	if phase == "before" {
		wantPending = 0
	}
	if registrations != 1 || pending != wantPending || phase == "before" && delivered != 0 || phase == "settled" && delivered != 1 {
		t.Fatalf("post-idle source phase=%s registrations=%d pending=%d delivered=%d", phase, registrations, pending, delivered)
	}
	if pending == 0 {
		return ""
	}
	return fmt.Sprintf("watch:%s:%d", admitted.DeliveryID, admitted.UpdateSeq)
}

// Log only fixture outcome fields and causal coordinates, never request bodies.
// Request indices show which accepted notification carriers this request held;
// observed IDs distinguish those carriers from a durable response receipt.
func namedParentMiddleDiagnostic(req llm.Request, turns []schema.Turn) string {
	outcomes := []map[string]any{}
	for index, message := range req.Messages {
		for _, part := range message.Content {
			result := part.ToolResult
			if result == nil || result.ToolCallID != "middle-final" || len(outcomes) >= 4 {
				continue
			}
			outcome := map[string]any{"request_index": index, "is_error": result.IsError}
			// The response fields live in Content. ToolState separately carries
			// the accepted end reason and must not hide the response outcome.
			var raw []byte
			if text, ok := result.Content.(string); ok {
				raw = []byte(text)
			} else {
				raw, _ = json.Marshal(result.Content)
			}
			var fields map[string]any
			outcome["structured"] = json.Unmarshal(raw, &fields) == nil
			for _, key := range []string{"accepted", "end_turn"} {
				if value, ok := fields[key].(bool); ok {
					outcome[key] = value
				}
			}
			for _, key := range []string{"reason", "delivery"} {
				if value, ok := fields[key].(string); ok {
					outcome[key] = value[:min(len(value), 128)]
				}
			}
			outcomes = append(outcomes, outcome)
		}
	}
	carriers, receipts := []map[string]any{}, []map[string]any{}
	for index, turn := range turns {
		if turn.AttentionID != "" && len(carriers) < 32 {
			coverage := []int{}
			if text := turn.Message.Text(); text != "" {
				for requestIndex, message := range req.Messages {
					if strings.Contains(message.Text(), text) && len(coverage) < 16 {
						coverage = append(coverage, requestIndex)
					}
				}
			}
			carriers = append(carriers, map[string]any{
				"entry": index + 1, "id": turn.AttentionID, "request_indices": coverage,
			})
		}
		if len(turn.ObservedNotificationIDs) != 0 && len(receipts) < 32 {
			receipts = append(receipts, map[string]any{
				"entry": index + 1, "ids": turn.ObservedNotificationIDs[:min(len(turn.ObservedNotificationIDs), 16)],
			})
		}
	}
	raw, _ := json.Marshal(map[string]any{"middle_final": outcomes, "carriers": carriers, "receipts": receipts})
	return string(raw)
}

func TestNamedParentMiddleDiagnosticResponseContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content any
		state   json.RawMessage
		isError bool
		want    map[string]any
	}{
		{
			name:    "accepted_with_end_state",
			content: `{"accepted":true,"end_turn":true,"delivery":"delivered"}`,
			state:   json.RawMessage(`{"communicate_end_reason":"done"}`),
			want: map[string]any{
				"request_index": float64(0), "is_error": false, "structured": true,
				"accepted": true, "end_turn": true, "delivery": "delivered",
			},
		},
		{
			name:    "refused",
			content: map[string]any{"accepted": false, "end_turn": false, "reason": "stale_wait_target"},
			want: map[string]any{
				"request_index": float64(0), "is_error": false, "structured": true,
				"accepted": false, "end_turn": false, "reason": "stale_wait_target",
			},
		},
		{
			name:    "error",
			content: "opaque-error",
			isError: true,
			want:    map[string]any{"request_index": float64(0), "is_error": true, "structured": false},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := llm.ToolResult("middle-final", tc.content, tc.isError)
			message.Content[0].ToolResult.ToolState = tc.state
			raw := namedParentMiddleDiagnostic(llm.Request{Messages: []llm.Message{message}}, nil)
			var got struct {
				MiddleFinal []map[string]any `json:"middle_final"`
			}
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.MiddleFinal, []map[string]any{tc.want}) {
				t.Fatalf("middle-final outcome=%v want=%v", got.MiddleFinal, tc.want)
			}
		})
	}
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
	testTranscriptAuthorshipNamedParentPublication(t, false)
}

func TestTranscriptAuthorshipNamedParentPostIdlePublication(t *testing.T) {
	testTranscriptAuthorshipNamedParentPublication(t, true)
}

func testTranscriptAuthorshipNamedParentPublication(t *testing.T, postIdle bool) {
	p := &namedParentAuthorshipProvider{identity: make(chan string, 1), leafStarted: make(chan struct{}), steered: make(chan struct{}), finishPreview: make(chan struct{}), sendCompleted: make(chan struct{}), finishMiddle: make(chan struct{}), finishRoot: make(chan struct{}), middleFinalStarted: make(chan struct{}), postIdle: postIdle, postIdleObserved: make(chan struct{}), finishPostIdle: make(chan struct{})}
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
	journal := filepath.Join(sess.StateDir(), "sessions", sess.ID(), "delegates.jsonl")
	watchJournal := filepath.Join(sess.StateDir(), "sessions", sess.ID(), "jobs.jsonl")
	var postIdleWatch namedParentWatchBinding
	var postIdleID string
	p.scriptMu.Lock()
	p.diagnoseMiddle = func(req llm.Request) string {
		return namedParentMiddleDiagnostic(req, transcriptTurns(t, path(middle.ChildSessionID)))
	}
	p.notificationAfter = func(role string, req llm.Request) bool {
		switch role {
		case "middle":
			namedParentRuns(t, journal, middle.DelegateID, 1, 0)
			deliveries := namedParentRuns(t, journal, leaf.DelegateID, 2, 2)
			return namedParentPendingAfterTerminal(t, req, "middle-final", path(middle.ChildSessionID), "delegate:"+deliveries[2])
		case "root":
			deliveries := namedParentRuns(t, journal, middle.DelegateID, 1, 1)
			return namedParentPendingAfterTerminal(t, req, "root-final", sess.TranscriptPath(), "delegate:"+deliveries[1])
		case "watch-created":
			namedParentRuns(t, journal, middle.DelegateID, 1, 0)
			postIdleWatch = namedParentCreatedWatch(t, req, watchJournal, sess.ID(), middle.ChildSessionID, middle.DelegateID)
			namedParentPostIdleSource(t, watchJournal, postIdleWatch, "before")
			return true
		case "post-idle":
			namedParentRuns(t, journal, middle.DelegateID, 2, 1)
			// The watch's canonical arm precedes its delivered append. A provider
			// can see zero or one ack here, but never a second identity/transition.
			postIdleID = namedParentPostIdleSource(t, watchJournal, postIdleWatch, "provider")
			return namedParentPendingAfterTerminal(t, req, "middle-notification", path(middle.ChildSessionID), postIdleID)
		}
		return false
	}
	p.scriptMu.Unlock()
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
		t.Helper()
		t.Logf("await idle delegate=%s generation=%d", id, generation)
		completed := false
		defer func() {
			if completed {
				return
			}
			p.scriptMu.Lock()
			requests := slices.Clone(p.requests)
			p.scriptMu.Unlock()
			ps.mu.Lock()
			seen := slices.Clone(ps.seen)
			next := ps.next
			ps.mu.Unlock()
			t.Logf("unmet idle delegate=%s generation=%d cursor=%d provider requests=%v", id, generation, next, requests)
			for index, event := range seen {
				if data, ok := event.Data.(events.DelegateUpdatedData); ok {
					t.Logf("retained update[%d]: delegate=%s generation=%d lifecycle=%s outcome=%s", index, data.DelegateID, data.RunGeneration, data.Lifecycle, data.Outcome)
				}
			}
		}()
		ps.await(t, events.EventDelegateUpdated, func(ev events.SessionEvent) bool {
			d, ok := ev.Data.(events.DelegateUpdatedData)
			return ok && d.DelegateID == id && d.RunGeneration == generation && d.Lifecycle == "idle" && d.Outcome == "completed"
		})
		completed = true
	}
	awaitIdle(leaf.DelegateID, 1)
	close(p.sendCompleted)
	awaitIdle(leaf.DelegateID, 2)
	close(p.finishMiddle)
	awaitIdle(middle.DelegateID, 1)
	close(p.finishRoot)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sourceWake := make(chan struct{}, 1)
	sess.SetNotifyFunc(func() {
		select {
		case sourceWake <- struct{}{}:
		default:
		}
	})
	if _, err := sess.DrainJobTree(ctx, sourceWake); err != nil {
		t.Fatalf("settle already-observed source debt: %v", err)
	}
	// Accepted end_turn did not finish the middle's generation before its
	// pending leaf delivery was observed in call 5. Root call 3 likewise covers
	// the middle's genuine result after its accepted terminal.
	leafDeliveries := namedParentRuns(t, journal, leaf.DelegateID, 2, 2)
	middleDeliveries := namedParentRuns(t, journal, middle.DelegateID, 1, 1)
	requireNamedParentObservedOnce(t, path(middle.ChildSessionID), "delegate:"+leafDeliveries[2])
	requireNamedParentSourceAck(t, journal, leafDeliveries[2])
	requireNamedParentObservedOnce(t, sess.TranscriptPath(), "delegate:"+middleDeliveries[1])
	requireNamedParentSourceAck(t, journal, middleDeliveries[1])
	if postIdle {
		rootCommunicates := func(want []string) {
			t.Helper()
			var got []string
			for _, turn := range transcriptTurns(t, sess.TranscriptPath()) {
				if turn.Kind == schema.TurnCommunicate && turn.Communicate != nil {
					got = append(got, turn.Communicate.CallID)
				}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("root communicate sequence=%v want=%v", got, want)
			}
		}
		// The middle installed its own parent watch in generation 1. Its every:3
		// condition suppresses these two root communicates, with no early frame
		// or generation 2. Only the third, published after actual idle, can fire.
		rootCommunicates([]string{"root-final", "root-notification"})
		namedParentPostIdleSource(t, watchJournal, postIdleWatch, "before")
		for _, turn := range transcriptTurns(t, path(middle.ChildSessionID)) {
			if strings.HasPrefix(turn.AttentionID, "watch:") {
				t.Fatalf("watch carrier preceded post-idle publication: %s", turn.AttentionID)
			}
		}
		if _, err := sess.ProcessInput(ctx, "publish after middle idle", nil); err != nil {
			t.Fatal(err)
		}
		select {
		case <-p.postIdleObserved:
		case <-time.After(30 * time.Second): // TRIPWIRE: only a missing real post-idle provider request reaches this guard.
			t.Fatal("middle did not receive post-idle source publication")
		}
		// Publisher return fences the source acknowledgement, not provider entry.
		// Keep the middle response gated until the SAME source identity settles.
		if id := namedParentPostIdleSource(t, watchJournal, postIdleWatch, "settled"); id != postIdleID {
			t.Fatalf("post-idle source identity changed: provider=%s settled=%s", postIdleID, id)
		}
		rootCommunicates([]string{"root-final", "root-notification", "post-idle-publication"})
		close(p.finishPostIdle)
		awaitIdle(middle.DelegateID, 2)
		namedParentRuns(t, journal, middle.DelegateID, 2, 2)
		if id := namedParentPostIdleSource(t, watchJournal, postIdleWatch, "settled"); id != postIdleID {
			t.Fatalf("post-idle source identity changed after idle: provider=%s settled=%s", postIdleID, id)
		}
		requireNamedParentObservedOnce(t, path(middle.ChildSessionID), postIdleID)
		for _, turns := range [][]appwire.Turn{readWholeThread(t, ps.srv, middle.ChildSessionID).Thread.Turns, fileHistoryTurns(t, path(middle.ChildSessionID))} {
			assertItems(turns, []string{"middle observed post-idle publication"}, parentAuthor)
		}
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
	wantRoot, wantMiddle := 3, 5
	if postIdle {
		wantRoot, wantMiddle = 4, 7
	}
	if p.rootCalls != wantRoot || p.middleCalls != wantMiddle || p.leafCalls != 2 || !p.streamed {
		t.Fatalf("script calls root=%d middle=%d leaf=%d streamed=%v", p.rootCalls, p.middleCalls, p.leafCalls, p.streamed)
	}
}
