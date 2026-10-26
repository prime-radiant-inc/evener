package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Decode the public record, so this test can demonstrate missing metadata before
// the schema has an authorship field.
func assertTranscriptAuthor(t *testing.T, value any, author map[string]any, inherited bool) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"author": author, "inherited": inherited}
	if !reflect.DeepEqual(got["authorship"], want) {
		t.Errorf("authorship = %#v, want %#v", got["authorship"], want)
	}
}

func TestTranscriptAuthorshipParentInputs(t *testing.T) {
	t.Parallel()
	t.Run("active steer", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 1, 1)
		seedDelegateControllerRunning(t, c, "dlg_target", "")
		fs := afero.NewMemMapFs()
		attachDelegateSteerRuntime(t, c, "dlg_target", fs)
		if _, err := c.Steer(context.Background(), rootDelegateActor("root-session"), "dlg_target", "authorship-steer"); err != nil {
			t.Fatal(err)
		}
		entries := decodeTranscriptEntries(t, fs, "/dlg_target.jsonl")
		if len(entries) != 1 {
			t.Fatalf("entries = %d", len(entries))
		}
		assertTranscriptAuthor(t, entries[0].Turn, map[string]any{"kind": "agent", "session_id": "root-session"}, false)
	})
	t.Run("create and completed send", func(t *testing.T) {
		root, client, _ := newDelegateResourceBootstrapSession(t)
		client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return finalResponse("authorship-answer") }}})
		result := root.createDelegate(context.Background(), delegateArgs{Task: "authorship-input", Name: "named-child", DelegationAllowance: new(0)})
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		sub := root.getSub(result.ChildSessionID)
		sub.mu.Lock()
		done := sub.done
		sub.mu.Unlock()
		<-done
		adapter := newTask6FrozenDescriptorAdapter()
		client.Register(adapter)
		t.Cleanup(adapter.releaseRun)
		outcome := (delegateRuntime{owner: root}).send(context.Background(), result.DelegateID, "authorship-followup", 0)
		if outcome.result.Err != nil {
			t.Fatal(outcome.result.Err)
		}
		<-adapter.entered
		data, err := readTranscriptFull(transcriptPath(root.stateDir, result.ChildSessionID), "")
		if err != nil {
			t.Fatal(err)
		}
		inputs := 0
		for _, e := range data.Entries {
			if e.Turn.Kind == schema.TurnUserInput {
				inputs++
				assertTranscriptAuthor(t, e.Turn, map[string]any{"kind": "agent", "session_id": root.id}, false)
			}
		}
		if inputs != 2 {
			t.Fatalf("inputs = %d", inputs)
		}
	})
}

func TestTranscriptAuthorshipForkCopies(t *testing.T) {
	t.Parallel()
	// Legacy records supply no author evidence, even when they look human.
	input := schema.NewTurn(schema.TurnUserInput, llm.User("legacy-human-shaped-input"))
	answer := schema.NewTurn(schema.TurnAssistant, llm.Assistant("legacy-answer"))
	original := []transcript.Entry{{Turn: input}, {Turn: answer}}
	copied := delegateContextEntries(original)
	if len(copied) != 2 {
		t.Fatalf("copied = %d", len(copied))
	}
	for _, e := range copied {
		assertTranscriptAuthor(t, e.Turn, map[string]any{"kind": "unknown"}, true)
	}
	b, _ := json.Marshal(original[0].Turn)
	var source map[string]any
	_ = json.Unmarshal(b, &source)
	if _, ok := source["authorship"]; ok {
		t.Fatal("copy mutated source")
	}
}

func TestTranscriptAuthorshipNestedParent(t *testing.T) {
	t.Parallel()
	root, client, _ := newDelegateResourceBootstrapSession(t)
	middleAdapter := newTask6FrozenDescriptorAdapter()
	client.Register(middleAdapter)
	t.Cleanup(middleAdapter.releaseRun)
	middleResult := root.createDelegate(context.Background(), delegateArgs{Task: "middle-input", Name: "Middle", DelegationAllowance: new(1)})
	if middleResult.Err != nil {
		t.Fatal(middleResult.Err)
	}
	<-middleAdapter.entered
	middle := root.getSub(middleResult.ChildSessionID).sess
	human := schema.NewTurn(schema.TurnUserInput, llm.User("nested-original-human"))
	human.Authorship = humanTranscriptAuthorship()
	middle.recordTurn(human, human)
	answer := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "nested-original-answer"}, {Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "nested-original-thought"}}}})
	answer.Authorship = &schema.TranscriptAuthorship{Author: middle.transcriptAuthor()}
	middle.recordTurn(answer, answer)
	call := schema.NewTurn(schema.TurnAssistant, forkContextToolCall("nested-copy-comm", "communicate"))
	call.Authorship = answer.Authorship.Clone()
	middle.recordTurn(call, call)
	middle.appendTurn(schema.TurnToolResults, llm.ToolResult("nested-copy-comm", `{"delivery":"delivered"}`, false))
	middle.cfg.testOnly.childClientFactory = func() *llm.Client {
		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return finalResponse("nested-answer") }, func(llm.Request) llm.Response { return finalResponse("nested-second-answer") }}})
		return c
	}
	c := root.delegateController
	c.mu.Lock()
	lease := c.live[middleResult.DelegateID].binding.lease
	c.mu.Unlock()
	ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
	result := middle.createDelegate(ctx, delegateArgs{Task: "nested-input", Name: "Leaf", ForkContext: true, DelegationAllowance: new(0)})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	sub := middle.getSub(result.ChildSessionID)
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	<-done
	outcome := (delegateRuntime{owner: middle}).send(ctx, result.DelegateID, "nested-followup", 0)
	if outcome.result.Err != nil {
		t.Fatal(outcome.result.Err)
	}
	sub.mu.Lock()
	done = sub.done
	sub.mu.Unlock()
	<-done
	data, err := readTranscriptFull(transcriptPath(root.stateDir, result.ChildSessionID), "")
	if err != nil {
		t.Fatal(err)
	}
	inputs, answers, inherited := 0, 0, 0
	for _, e := range data.Entries {
		if e.Turn.Authorship != nil && e.Turn.Authorship.Inherited {
			switch {
			case e.Turn.Message.Text() == "middle-input":
				inherited++
				assertTranscriptAuthor(t, e.Turn, map[string]any{"kind": "agent", "session_id": root.id}, true)
			case e.Turn.Message.Text() == "nested-original-human":
				inherited++
				assertTranscriptAuthor(t, e.Turn, map[string]any{"kind": "human"}, true)
			case e.Turn.Kind == schema.TurnAssistant:
				inherited++
				assertTranscriptAuthor(t, e.Turn, map[string]any{"kind": "agent", "session_id": middle.id, "owner_session_id": root.id, "delegate_id": middleResult.DelegateID, "delegate_name": "Middle"}, true)
			default:
				assertTranscriptAuthor(t, e.Turn, map[string]any{"kind": "unknown"}, true)
			}
			continue
		}
		switch e.Turn.Kind {
		case schema.TurnUserInput:
			if e.Turn.Message.Text() != "nested-input" && e.Turn.Message.Text() != "nested-followup" {
				if e.Turn.Authorship != nil {
					t.Fatal("internal child input borrowed human attribution")
				}
				continue
			}
			inputs++
			assertTranscriptAuthor(t, e.Turn, map[string]any{"kind": "agent", "session_id": middle.id, "owner_session_id": root.id, "delegate_id": middleResult.DelegateID, "delegate_name": "Middle"}, false)
		case schema.TurnAssistant:
			answers++
			assertTranscriptAuthor(t, e.Turn, map[string]any{"kind": "agent", "session_id": result.ChildSessionID, "owner_session_id": middle.id, "delegate_id": result.DelegateID, "delegate_name": "Leaf"}, false)
		}
	}
	if inputs != 2 || answers != 2 || inherited != 4 {
		t.Fatalf("inputs=%d answers=%d inherited=%d", inputs, answers, inherited)
	}
	c.mu.Lock()
	physicalOwner := c.durable[result.DelegateID].Descriptor.OwnerSessionID
	c.mu.Unlock()
	if physicalOwner != root.id || physicalOwner == middle.id {
		t.Fatalf("physical owner = %q", physicalOwner)
	}
}

func TestTranscriptAuthorshipForkCopiesKnown(t *testing.T) {
	t.Parallel()
	root, client, _ := newDelegateResourceBootstrapSession(t)
	human := schema.NewTurn(schema.TurnUserInput, llm.User("original-human"))
	human.Authorship = humanTranscriptAuthorship()
	root.recordTurn(human, human)
	answer := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "original-answer"}, {Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "original-thought"}}}})
	answer.Authorship = &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: root.id}}
	root.recordTurn(answer, answer)
	call := schema.NewTurn(schema.TurnAssistant, forkContextToolCall("comm-copy", "communicate"))
	call.Message.Content[0].ToolCall.Arguments = []byte(`{"message":"original-communicate","end_turn":false}`)
	call.Authorship = answer.Authorship.Clone()
	root.recordTurn(call, call)
	root.appendTurn(schema.TurnToolResults, llm.ToolResult("comm-copy", `{"delivery":"delivered"}`, false))
	for _, opt := range []bool{false, true} {
		adapter := newTask6FrozenDescriptorAdapter()
		client.Register(adapter)
		t.Cleanup(adapter.releaseRun)
		result := root.createDelegate(context.Background(), delegateArgs{Task: "fork-input", ForkContext: opt, DelegationAllowance: new(0)})
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		<-adapter.entered
		data, err := readTranscriptFull(transcriptPath(root.stateDir, result.ChildSessionID), "")
		if err != nil {
			t.Fatal(err)
		}
		seen := 0
		for _, e := range data.Entries {
			if e.Turn.Message.Text() == "original-human" || e.Turn.Message.Text() == "original-answer" || (e.Turn.Kind == schema.TurnAssistant && len(e.Turn.Message.Content) > 0 && e.Turn.Message.Content[0].ToolCall != nil && e.Turn.Message.Content[0].ToolCall.ID == "comm-copy") {
				seen++
				expected := map[string]any{"kind": "agent", "session_id": root.id}
				if e.Turn.Kind == schema.TurnUserInput {
					expected = map[string]any{"kind": "human"}
				}
				assertTranscriptAuthor(t, e.Turn, expected, true)
			}
		}
		if opt && seen != 3 || !opt && seen != 0 {
			t.Fatalf("opt=%v copied=%d", opt, seen)
		}
		if opt {
			nested := delegateContextEntries(data.Entries)
			for _, e := range nested {
				if e.Turn.Message.Text() == "original-answer" {
					assertTranscriptAuthor(t, e.Turn, map[string]any{"kind": "agent", "session_id": root.id}, true)
					e.Turn.Authorship.Author.SessionID = "changed"
				}
			}
			if answer.Authorship.Author.SessionID != root.id || answer.Authorship.Inherited {
				t.Fatal("copy changed original")
			}
		}
		adapter.releaseRun()
	}
	// Ordinary edited forks use the same durable evidence but a fresh human edit.
	root.appendTurn(schema.TurnUserInput, llm.User("edit-target"))
	data, err := readTranscriptFull(transcriptPath(root.stateDir, root.id), "")
	if err != nil {
		t.Fatal(err)
	}
	childID, err := ForkSession(root.stateDir, root.id, len(data.Entries), "edited-human", "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := readTranscriptFull(transcriptPath(root.stateDir, childID), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range child.Entries {
		if e.Turn.Message.Text() == "original-human" {
			assertTranscriptAuthor(t, e.Turn, map[string]any{"kind": "human"}, true)
		}
		if e.Turn.Message.Text() == "edited-human" {
			assertTranscriptAuthor(t, e.Turn, map[string]any{"kind": "human"}, false)
		}
	}
}

func TestTranscriptAuthorshipAcceptedHuman(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "failed"}[fail], func(t *testing.T) {
			s := newQueuePersistTestSession(t, t.TempDir())
			t.Cleanup(s.Close)
			params := appwire.TurnStartParams{ClientMutationID: "author-input", Input: []appwire.InputItem{{Type: "text", Text: "human-input"}}}
			started, err := s.AcceptClientMutationStart(params)
			if err != nil {
				t.Fatal(err)
			}
			claimed, ok, err := s.claimClientMutationStart()
			if err != nil || !ok {
				t.Fatalf("claim: %v %v", ok, err)
			}
			injected := errors.New("authorship failure")
			if fail {
				s.clientMutationPreAppendFailure = func(schema.Turn) error { return injected }
			}
			err = s.acceptUserInput(withQueuedClientMutation(context.Background(), claimed), claimed.Text, claimed.Images, nil, false)
			if fail && !errors.Is(err, injected) || !fail && err != nil {
				t.Fatalf("accept: %v", err)
			}
			found := 0
			for _, turn := range s.history {
				if turn.Kind == schema.TurnUserInput && turn.ClientMutationID == params.ClientMutationID {
					found++
					assertTranscriptAuthor(t, turn, map[string]any{"kind": "human"}, false)
					if turn.StableTurnID != started.Turn.ID {
						t.Fatal("mutation identity changed")
					}
				}
			}
			if found != 1 {
				t.Fatalf("inputs=%d", found)
			}
		})
	}
}

func TestTranscriptAuthorshipInternalNudgeAndExplicitHuman(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	bare := func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("bare response")} }
	fixture.adapter.steps = []func(llm.Request) llm.Response{bare, bare, bare, bare, func(llm.Request) llm.Response { return finalResponse("recovered") }}
	root := restoreSupervisionRoot(t, fixture, nil)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
	abortUnpersistedStableDelegateOutcome(t, outcome)
	assertSingleRecoveryNudge(t, fixture.adapter)
	sub := root.getSub(fixture.childID)
	if sub == nil {
		t.Fatal("missing child")
	}
	data, err := readTranscriptFull(sub.sess.TranscriptPath(), "")
	if err != nil {
		t.Fatal(err)
	}
	nudges := 0
	for _, e := range data.Entries {
		if e.Turn.Kind == schema.TurnUserInput && e.Turn.Message.Text() == communicateNudge(sub.sess.resultToolName()) {
			nudges++
			if e.Turn.Authorship != nil {
				t.Fatal("harness nudge labeled human")
			}
		}
	}
	if nudges != 1 {
		t.Fatalf("nudges=%d", nudges)
	}
	// The same child accepts explicit human/client input without borrowing the
	// harness origin or its parent. No provider call is needed to accept it.
	if err := sub.sess.acceptUserInput(context.Background(), "explicit human", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	data, err = readTranscriptFull(sub.sess.TranscriptPath(), "")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, e := range data.Entries {
		if e.Turn.Kind == schema.TurnUserInput && e.Turn.Message.Text() == "explicit human" {
			found++
			assertTranscriptAuthor(t, e.Turn, map[string]any{"kind": "human"}, false)
		}
	}
	if found != 1 {
		t.Fatalf("explicit human inputs=%d", found)
	}
}

func TestTranscriptAuthorshipQueuedExecution(t *testing.T) {
	s, client, _ := newDelegateResourceBootstrapSession(t)
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return finalResponse("first answer") }, func(llm.Request) llm.Response { return finalResponse("second answer") }}})
	s.SetClientMutationStartWakeFunc(func() {})
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "first", Input: []appwire.InputItem{{Type: "text", Text: "first"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "second", Input: []appwire.InputItem{{Type: "text", Text: "second"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := s.ProcessClientMutationStart(context.Background(), nil); err != nil || !ran {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	data, err := readTranscriptFull(s.TranscriptPath(), "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, e := range data.Entries {
		if e.Turn.Kind == schema.TurnUserInput {
			seen[e.Turn.ClientMutationID]++
			assertTranscriptAuthor(t, e.Turn, map[string]any{"kind": "human"}, false)
		}
	}
	if !reflect.DeepEqual(seen, map[string]int{"first": 1, "second": 1}) {
		t.Fatal(seen)
	}
}
