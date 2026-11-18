package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/internal/testwait"
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
		client.Register(newFakeAdapter("openai", func(llm.Request) llm.Response { return agenttest.FinalResponse("authorship-answer") }))
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
		data, err := readTranscriptFull(context.Background(), transcriptPath(root.stateDir, result.ChildSessionID), "")
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

	// The middle child's prompt namer is parked inside its provider call for the
	// length of this test, so the close below meets a namer that cannot finish.
	// The namer's own wait group is the only thing that can prove it ended: the
	// close joins sendersWG (which also counts delegate runs) and only within the
	// close budget, which gives up on a straggler.
	namerGate := make(chan struct{})
	releaseNamer := sync.OnceFunc(func() { close(namerGate) })
	// Registered before the fixture's own Close: a failure anywhere below must
	// not leave the parked namer for that Close to wait out.
	t.Cleanup(releaseNamer)
	namerEntered := make(chan struct{})
	// Park exactly the FIRST naming call. The root never names (it accepts no
	// prompt), so the first is the middle's, and the leaf's namer the test
	// starts later answers at once.
	var parkFirst sync.Once
	// Replaces the fixture's own namer adapter on the root's client, which is
	// also the middle's: the child config is otherwise untouched.
	client.Register(&agenttest.ScriptedAdapter{Provider: testSessionNamerProvider, Responder: func(request llm.Request) llm.Response {
		first := false
		parkFirst.Do(func() { first = true })
		if first {
			close(namerEntered)
			<-namerGate
		}
		return llm.Response{Provider: testSessionNamerProvider, Model: request.Model, Message: llm.Assistant(`{"name":"Parked Middle Name"}`)}
	}})

	// The middle's own turn is parked too. The close below needs a session with
	// no run in flight: a close that begins mid-run may abandon the delegate stop
	// and return while that run still owes its finalization. The reply is a
	// terminal communicate result, so opening the gate settles the run instead of
	// returning bare text the run would nudge and re-enter on. The call also
	// honors cancellation, so a close that begins while it is parked ends the
	// run through that cancellation rather than blocking on the adapter.
	middleRun := &authorshipGatedAdapter{name: "openai", entered: make(chan struct{}), gate: make(chan struct{})}
	t.Cleanup(middleRun.release)
	client.Register(middleRun)
	middleResult := root.createDelegate(context.Background(), delegateArgs{Task: "middle-input", Name: "Middle", DelegationAllowance: new(1)})
	if middleResult.Err != nil {
		t.Fatal(middleResult.Err)
	}
	<-middleRun.entered
	// Await the namer's own entry rather than arming a helper goroutine on it: a
	// helper that timed out here would leave its goroutine blocked on this
	// channel for the rest of the process, and the failure would come from the
	// helper instead of from the condition.
	testwait.Until(t, "the middle child's prompt namer reaching its provider call", func() bool {
		return channelClosed(namerEntered)
	})
	middleSub := root.getSub(middleResult.ChildSessionID)
	middle := middleSub.sess
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
	// The session's config is guarded by mu: the prompt namer this session
	// started reaches metaWithNotes' whole-config snapshot on its own
	// goroutine, so an unlocked write here races that read.
	middle.mu.Lock()
	middle.cfg.testOnly.childClientFactory = func() *llm.Client {
		c := llm.NewClient()
		// The leaf inherits the test namer as its naming provider, so its own
		// client must answer that provider: without this its naming request
		// fails provider resolution, and the two scripted OpenAI replies below
		// stay reserved for the leaf's turns.
		registerTestSessionNamer(c)
		c.Register(newFakeAdapter("openai", func(llm.Request) llm.Response { return agenttest.FinalResponse("nested-answer") }, func(llm.Request) llm.Response { return agenttest.FinalResponse("nested-second-answer") }))
		return c
	}
	middle.mu.Unlock()
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
	leaf := sub.sess
	// Joining the namers is a cleanup, not just a tail statement. A failure at
	// any assertion below exits the test goroutine before the explicit join
	// runs, and the fixture's own Close cannot replace it: closeOnce is already
	// spent by the expired close, so that Close is a no-op and the parked namer
	// would still be running when the temporary directories are removed. This
	// runs before the fixture's cleanups and releases the gates itself, so every
	// exit path ends with both namers joined.
	joinNamers := func() {
		// TRIPWIRE: a wait group cannot report a namer that exits without calling
		// Done, and there is no channel to select on, so this ceiling is the only
		// way a missing Done fails this test by name instead of stalling the shard
		// until the binary's timeout with the fixture never cleaned up. With the
		// gates released both namers answer from a scripted adapter with no real
		// I/O, so this normally returns in well under a second; 30s only fires on
		// a missing Done.
		awaitWithin(t, 30*time.Second, "the middle and leaf children's namers to finish", func() {
			joinRetirementTreeEmitters(middle)
			joinRetirementTreeEmitters(leaf)
		})
	}
	t.Cleanup(func() {
		releaseNamer()
		middleRun.release()
		joinNamers()
	})
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
	data, err := readTranscriptFull(context.Background(), transcriptPath(root.stateDir, result.ChildSessionID), "")
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

	// Settle the middle delegate's own run before the close below, so the close
	// meets nothing but the parked namer. A close that begins with that run still
	// in flight can abandon the delegate stop and finish with the run owing its
	// finalization, which would then land while the fixture's directories are
	// already being removed. The settle happens here, not after the close: the
	// namer gate stays shut until the post-close assertion below, so the parked
	// namer this test exists to join is still parked when the close begins.
	middleRun.release()
	// A finished generation closes its done channel and the next admitted run
	// replaces it, and the middle is woken again when its leaf reports, so
	// settling it means waiting out every generation that starts while this
	// waits. Each wait is on a channel; nothing here polls or sleeps.
	// TRIPWIRE: every generation runs against the released scripted adapter with
	// no real I/O, so this normally returns in well under a second. 10s only
	// fires on a wedged run.
	awaitWithin(t, 10*time.Second, "the middle delegate's run to settle", func() {
		for {
			middleSub.mu.Lock()
			generation := middleSub.done
			middleSub.mu.Unlock()
			<-generation
			middleSub.mu.Lock()
			replaced := middleSub.done != generation
			middleSub.mu.Unlock()
			if !replaced {
				return
			}
		}
	})

	// The fixture's close must not outlive the namers this test started. The
	// close budget is spent before the close begins, so closeEventStream's
	// joinWithinCloseBudget gives up on the parked namer and returns with it
	// still running, and closeOnce is spent by that pass. The proof that the
	// namer died is therefore the cleanup's own unbudgeted wait on sendersWG:
	// both delegate runs were settled above, so the only emitter these sessions
	// still hold is the parked namer. The close is bounded here by the tripwire,
	// not by that join: a close that stopped giving up would otherwise hold the
	// test goroutine until the binary's timeout instead of failing here.
	// The run's state is read here, next to the close and outside the settle
	// above, so moving that settle after the close leaves this reading a run in
	// flight and fails below instead of passing on a run the close stopped.
	middleSub.mu.Lock()
	settledBeforeClose := channelClosed(middleSub.done) && !middleSub.running
	middleSub.mu.Unlock()
	// The budget is spent before the close begins, with no clock involved: a
	// context cancelled up front is already done, so every join in the cascade
	// gives up on its first select.
	expired, cancelExpired := context.WithCancel(context.Background())
	cancelExpired()
	// TRIPWIRE: with the budget already spent this close returns in milliseconds
	// over in-memory state. 30s only fires if a join stopped honoring the budget.
	awaitWithin(t, 30*time.Second, "the expired-budget close to return", func() {
		root.close(expired, closeOptions{cleanupEnv: true})
	})
	middle.eventsMu.RLock()
	middleStreamClosed := middle.eventsClosed
	middle.eventsMu.RUnlock()
	middle.mu.Lock()
	pendingAfterClose := middle.naming.pending
	middle.mu.Unlock()
	if !settledBeforeClose {
		t.Fatal("the middle delegate's run had not settled when the expired close began, so the close would have had to abandon it")
	}
	if !middleStreamClosed {
		t.Fatal("the expired-budget close returned without closing the middle child's event stream, so it never reached the join this test exists to outlive")
	}
	if pendingAfterClose == 0 {
		t.Fatal("the close reached the middle child's event stream with its namer already done, so this test never crossed the window it exists to prove")
	}
	releaseNamer()
	// The join is the same one the cleanup registers, run here so a failure in
	// the pending assertion below is reported against the join that caused it;
	// the cleanup's second run is a no-op on an already-drained group.
	joinNamers()
	middle.mu.Lock()
	stillNaming := middle.naming.pending
	middle.mu.Unlock()
	if stillNaming != 0 {
		t.Fatalf("the namer join returned with %d naming attempt(s) still pending", stillNaming)
	}
}

func TestTranscriptAuthorshipForkCopiesKnown(t *testing.T) {
	t.Parallel()
	root, client, _ := newDelegateResourceBootstrapSession(t)
	human := schema.NewTurn(schema.TurnUserInput, llm.User("original-human"))
	human.Authorship = humanTranscriptAuthorship()
	root.recordTurn(human, human)
	note := schema.NewTurn(schema.TurnSteering, llm.User("fork-note-7f21"))
	note.Authorship = humanTranscriptAuthorship()
	note.SteeringSource, note.SteeringKind = "user", "human-note"
	root.recordTurn(note, note)
	assertNotes := func(entries []transcript.Entry, want int) {
		t.Helper()
		seen := 0
		for index, entry := range entries {
			turn := entry.Turn
			if turn.Message.Text() != note.Message.Text() {
				continue
			}
			seen++
			if turn.Kind != schema.TurnSteering || turn.SteeringSource != "user" || turn.SteeringKind != "human-note" {
				t.Errorf("inherited note kind=%q source=%q steering kind=%q", turn.Kind, turn.SteeringSource, turn.SteeringKind)
			}
			assertTranscriptAuthor(t, turn, map[string]any{"kind": "human"}, true)
			items := apptranscript.ProjectTurn("fork-note", index, turn, nil, nil, nil)
			if len(items) != 1 || items[0].Type != "steering" || items[0].Source != "user" || items[0].SteeringKind != "human-note" || items[0].Text != note.Message.Text() {
				t.Errorf("projected inherited note = %+v", items)
			} else {
				assertTranscriptAuthor(t, items[0], map[string]any{"kind": "human"}, true)
			}
		}
		if seen != want {
			t.Errorf("inherited notes=%d, want %d", seen, want)
		}
	}
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
		data, err := readTranscriptFull(context.Background(), transcriptPath(root.stateDir, result.ChildSessionID), "")
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
			assertNotes(data.Entries, 1)
			nested := delegateContextEntries(data.Entries)
			assertNotes(nested, 1)
			for _, e := range nested {
				if e.Turn.Message.Text() == "original-answer" {
					assertTranscriptAuthor(t, e.Turn, map[string]any{"kind": "agent", "session_id": root.id}, true)
					e.Turn.Authorship.Author.SessionID = "changed"
				}
			}
			if answer.Authorship.Author.SessionID != root.id || answer.Authorship.Inherited {
				t.Fatal("copy changed original")
			}
		} else {
			assertNotes(data.Entries, 0)
		}
		adapter.releaseRun()
	}
	// Ordinary edited forks use the same durable evidence but a fresh human edit.
	root.appendTurn(schema.TurnUserInput, llm.User("edit-target"))
	data, err := readTranscriptFull(context.Background(), transcriptPath(root.stateDir, root.id), "")
	if err != nil {
		t.Fatal(err)
	}
	childID, err := ForkSession(root.stateDir, root.id, len(data.Entries), "edited-human", "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := readTranscriptFull(context.Background(), transcriptPath(root.stateDir, childID), "")
	if err != nil {
		t.Fatal(err)
	}
	for text, inherited := range map[string]bool{"original-human": true, "edited-human": false} {
		var matches []schema.Turn
		for _, e := range child.Entries {
			if e.Turn.Kind == schema.TurnUserInput && e.Turn.Message.Text() == text {
				matches = append(matches, e.Turn)
			}
		}
		if len(matches) != 1 {
			t.Fatalf("fork input %q count=%d, want 1", text, len(matches))
		}
		assertTranscriptAuthor(t, matches[0], map[string]any{"kind": "human"}, inherited)
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
	fixture := newColdDelegateFixture(t, "")
	bare := func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("bare response")} }
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{bare, bare, bare, bare, func(llm.Request) llm.Response { return agenttest.FinalResponse("recovered") }})
	root := restoreSupervisionRoot(t, fixture, nil)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "inspect", 60_000)
	abortUnpersistedDelegateOutcome(t, outcome)
	if requests := fixture.adapter.Requests(); len(requests) != 5 {
		t.Fatalf("provider requests=%d, want four bare replies and terminal reply", len(requests))
	}
	sub := root.getSub(fixture.childID)
	if sub == nil {
		t.Fatal("missing child")
	}
	data, err := readTranscriptFull(context.Background(), sub.sess.TranscriptPath(), "")
	if err != nil {
		t.Fatal(err)
	}
	var inputs []schema.Turn
	var reminders []schema.Turn
	var repliesBeforeInput []int
	replies := 0
	for _, e := range data.Entries {
		if e.Turn.Kind == schema.TurnAssistant {
			replies++
		}
		if e.Turn.Kind == schema.TurnUserInput {
			inputs = append(inputs, e.Turn)
			repliesBeforeInput = append(repliesBeforeInput, replies)
		}
		if e.Turn.Kind == schema.TurnSteering && e.Turn.SteeringKind == events.SteeringKindDelegateNudge {
			reminders = append(reminders, e.Turn)
		}
	}
	// The empty cold transcript receives our instruction, four bare replies,
	// one runtime reminder, then the terminal reply. The reminder is a daemon
	// steering turn, not a user input, so no production wording can select it as
	// caller speech.
	if len(inputs) != 1 || inputs[0].Message.Text() != "inspect" || !reflect.DeepEqual(repliesBeforeInput, []int{0}) || replies != 5 {
		t.Fatalf("inputs=%d replies before inputs=%v total replies=%d", len(inputs), repliesBeforeInput, replies)
	}
	if len(reminders) != 1 {
		t.Fatalf("runtime reminders = %d, want one unlabeled daemon steer", len(reminders))
	}
	if reminders[0].Authorship != nil {
		t.Fatal("runtime reminder labeled human")
	}
	// The same child accepts explicit human/client input without borrowing the
	// harness origin or its parent. No provider call is needed to accept it.
	if err := sub.sess.acceptUserInput(context.Background(), "explicit human", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	data, err = readTranscriptFull(context.Background(), sub.sess.TranscriptPath(), "")
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
	client.Register(newFakeAdapter("openai", func(llm.Request) llm.Response { return agenttest.FinalResponse("first answer") }, func(llm.Request) llm.Response { return agenttest.FinalResponse("second answer") }))
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
	data, err := readTranscriptFull(context.Background(), s.TranscriptPath(), "")
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

// authorshipGatedAdapter parks one provider call until release, and ends that
// call on cancellation. Both halves matter to the caller: release settles a
// parked run on its own terms, while cancellation lets a close that begins
// while the call is parked end the run through its own stop instead of
// blocking on the adapter.
type authorshipGatedAdapter struct {
	name    string
	entered chan struct{}
	gate    chan struct{}
	// gateOnce guards the gate against a second release from a cleanup.
	gateOnce sync.Once
}

func (a *authorshipGatedAdapter) Name() string { return a.name }

func (a *authorshipGatedAdapter) release() { a.gateOnce.Do(func() { close(a.gate) }) }

func (a *authorshipGatedAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	select {
	case <-a.entered:
	default:
		close(a.entered)
	}
	select {
	case <-a.gate:
		resp := agenttest.FinalResponse("middle-answer")
		resp.Provider = a.name
		resp.Model = req.Model
		return resp, nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}

func (a *authorshipGatedAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
