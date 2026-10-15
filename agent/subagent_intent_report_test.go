package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// The child side of the intent channel (docs/specs/2026-10-11-delegate-latest-
// intent-design.md, "Capture" and "Clear"): a running child reports each
// started tool call's stated intent to its own delegate row through the parent
// binding, a call stating none reports nothing, and the child clears its line
// at its execution start. The tests drive real child sessions end to end on a
// scripted provider, so the capture goes through the real tool-call start path
// and the fence (a grandchild never lands on its parent's row) through the
// real per-child binding.

// intentReportFixture is a parent session whose children run a scripted
// provider, with note.txt available for read_file calls.
type intentReportFixture struct {
	parent   *Session
	adapter  llm.ProviderAdapter
	recorder sessionEventRecorder
	stop     chan struct{}
}

// newIntentReportFixtureWithAdapter builds the parent around the adapter and
// collects the parent's own authoritative event stream — the stream a pushed
// delegate/updated frame reaches, the way the daemon's clients do.
func newIntentReportFixtureWithAdapter(t *testing.T, workspace string, adapter llm.ProviderAdapter) *intentReportFixture {
	t.Helper()
	client := llm.NewClient()
	client.Register(adapter)
	parent, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(parent.Close)
	f := &intentReportFixture{parent: parent, adapter: adapter, stop: make(chan struct{})}
	stream := parent.Events()
	go func() {
		for {
			select {
			case ev, ok := <-stream:
				if !ok {
					return
				}
				f.recorder.record(ev)
			case <-f.stop:
				// Events already buffered when stop closes are drained here;
				// select picks at random among ready cases.
				for {
					select {
					case ev, ok := <-stream:
						if !ok {
							return
						}
						f.recorder.record(ev)
					default:
						return
					}
				}
			}
		}
	}()
	t.Cleanup(func() { close(f.stop) })
	return f
}

// newIntentReportFixture scripts the provider by position, for runs whose every
// request comes from one session.
func newIntentReportFixture(t *testing.T, steps ...func(llm.Request) llm.Response) *intentReportFixture {
	t.Helper()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write note.txt: %v", err)
	}
	return newIntentReportFixtureWithAdapter(t, workspace, &fakeAdapter{name: "openai", steps: steps})
}

// newIntentReportDispatchFixture serves the provider from a responder that
// dispatches on request content rather than position: the delegate tool call
// is detached, so a spawning child's continuation rounds interleave racily
// with the spawned child's rounds and a positional script cannot tell the
// sessions apart.
func newIntentReportDispatchFixture(t *testing.T, responder func(llm.Request) llm.Response) *intentReportFixture {
	t.Helper()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "note.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write note.txt: %v", err)
	}
	return newIntentReportFixtureWithAdapter(t, workspace, &agenttest.ScriptedAdapter{Provider: "openai", Responder: responder})
}

// spawn spawns the scripted child and returns its delegate id and the
// subagent.
func (f *intentReportFixture) spawn(t *testing.T) (string, *subagent) {
	t.Helper()
	result := f.parent.createDelegate(context.Background(), delegateArgs{Task: "run the scripted steps"})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	_, childID, err := decodeRef(result.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef(%q): %v", result.TranscriptRef, err)
	}
	child := f.parent.subagents.get(childID)
	if child == nil || child.sess == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	return result.DelegateID, child
}

func waitSubagentRun(t *testing.T, child *subagent) {
	t.Helper()
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	select {
	case <-done:
		// TRIPWIRE: a scripted run settles in milliseconds; 30s only fires on
		// a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("the scripted delegate run did not finish")
	}
}

// blockStep builds a scripted step that signals it was entered and then holds
// the run until released, so mid-run assertions see a live child.
func blockStep(entered, release chan struct{}, after func() llm.Response) func(llm.Request) llm.Response {
	return func(llm.Request) llm.Response {
		close(entered)
		<-release
		return after()
	}
}

func readFileCall(id, intent string) llm.ToolCallData {
	args := map[string]any{"file_path": "note.txt"}
	if intent != "" {
		args["intent"] = intent
	}
	raw, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return llm.ToolCallData{ID: id, Name: "read_file", Type: "function", Arguments: raw}
}

func applyPatchCall(id, intent, file string) llm.ToolCallData {
	raw, err := json.Marshal(map[string]any{
		"intent": intent,
		"patch":  "*** Begin Patch\n*** Add File: " + file + "\n+" + file + " content\n*** End Patch",
	})
	if err != nil {
		panic(err)
	}
	return llm.ToolCallData{ID: id, Name: "apply_patch", Type: "function", Arguments: raw}
}

// recordedIntentFrames returns the DelegateUpdatedData frames the parent's
// event stream carried for delegateID.
func recordedIntentFrames(f *intentReportFixture, delegateID string) []events.DelegateUpdatedData {
	var out []events.DelegateUpdatedData
	for _, event := range f.recorder.snapshot() {
		if event.Kind != events.EventDelegateUpdated {
			continue
		}
		data, ok := event.Data.(events.DelegateUpdatedData)
		if !ok || data.DelegateID != delegateID {
			continue
		}
		out = append(out, data)
	}
	return out
}

// intentRequestHasToolCall reports whether the request's history already
// contains a call to the named tool.
func intentRequestHasToolCall(req llm.Request, name string) bool {
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if part.ToolCall != nil && part.ToolCall.Name == name {
				return true
			}
		}
	}
	return false
}

// intentRequestLastUserText is the newest non-empty user message's text, the
// spawned child's assignment.
func intentRequestLastUserText(req llm.Request) string {
	for _, message := range slices.Backward(req.Messages) {
		if message.Role == llm.RoleUser {
			if text := message.Text(); text != "" {
				return text
			}
		}
	}
	return ""
}

// TestChildReportsStatedIntentToTheController drives a real child through a
// tool call that states an intent and pins the whole journey: the line reaches
// the controller's journal and live state, and the next publication of any
// kind carries it in a pushed frame — held long enough that the ride is not a
// coincidence of the coalescing window.
func TestChildReportsStatedIntentToTheController(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	f := newIntentReportFixture(t,
		func(llm.Request) llm.Response {
			return toolCallResponse(readFileCall("r1", "Reading the board's row tests."))
		},
		blockStep(entered, release, func() llm.Response { return finalResponse("done") }),
	)
	delegateID, child := f.spawn(t)

	select {
	case <-entered:
		// TRIPWIRE: the scripted rounds answer in milliseconds; 30s only
		// fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("the child never reached its second round")
	}
	if got := durableIntentState(t, f.parent.delegateController, delegateID).LatestIntent; got != "Reading the board's row tests." {
		t.Fatalf("mid-run the folded line = %q, want the stated intent", got)
	}
	if got := liveIntentState(t, f.parent.delegateController, delegateID).intent; got != "Reading the board's row tests." {
		t.Fatalf("mid-run the live line = %q, want the stated intent", got)
	}
	// Hold past the activity publication window so the pending intent's ride
	// on the next publication is the deterministic outcome, not a race.
	time.Sleep(1200 * time.Millisecond)
	close(release)
	waitSubagentRun(t, child)

	var published, settled bool
	// The recorder consumes the parent's buffered event channel
	// asynchronously, so run completion only queues the settled frame: wait
	// for the recorder to have taken in both frames the assertions read.
	// TRIPWIRE: the recorder's dedicated consumer drains a queued frame in
	// microseconds; 30s only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "the recorder to take in the published and settled frames", func() bool {
		published, settled = false, false
		for _, frame := range recordedIntentFrames(f, delegateID) {
			if frame.LatestIntent == "Reading the board's row tests." {
				published = true
			}
			// The clear must be settlement's own frame: the run's initial
			// frame also carries an empty line, so any empty frame proves
			// nothing.
			if frame.LatestIntent == "" && frame.Terminal {
				settled = true
			}
		}
		return published && settled
	})
	if !published {
		t.Fatal("no pushed frame carried the stated intent")
	}
	if !settled {
		t.Fatal("no settled frame carried the cleared line")
	}
}

// TestChildNoIntentCallLeavesThePreviousLineStanding pins the meter's
// precedent on the child: a call that states no intent reports nothing, so the
// previous line keeps standing while the run stays open.
func TestChildNoIntentCallLeavesThePreviousLineStanding(t *testing.T) {
	firstEntered := make(chan struct{})
	firstRelease := make(chan struct{})
	secondEntered := make(chan struct{})
	secondRelease := make(chan struct{})
	f := newIntentReportFixture(t,
		func(llm.Request) llm.Response {
			return toolCallResponse(readFileCall("r1", "Reading the board's row tests."))
		},
		blockStep(firstEntered, firstRelease, func() llm.Response {
			return toolCallResponse(readFileCall("r2", ""))
		}),
		blockStep(secondEntered, secondRelease, func() llm.Response { return finalResponse("done") }),
	)
	delegateID, child := f.spawn(t)

	<-firstEntered
	close(firstRelease)
	<-secondEntered
	// The no-intent call has started and finished inside this round; the line
	// from the first call must still be standing.
	if got := durableIntentState(t, f.parent.delegateController, delegateID).LatestIntent; got != "Reading the board's row tests." {
		t.Fatalf("a call stating no intent changed the standing line to %q", got)
	}
	close(secondRelease)
	waitSubagentRun(t, child)
}

// TestChildBlankIntentCallLeavesThePreviousLineStanding pins the meter's
// precedent on a whitespace-only intent: such a line's excerpt is empty, and
// an empty report is the execution-start clear's word, so a call stating only
// whitespace reports nothing and the previous line keeps standing.
func TestChildBlankIntentCallLeavesThePreviousLineStanding(t *testing.T) {
	firstEntered := make(chan struct{})
	firstRelease := make(chan struct{})
	secondEntered := make(chan struct{})
	secondRelease := make(chan struct{})
	f := newIntentReportFixture(t,
		func(llm.Request) llm.Response {
			return toolCallResponse(readFileCall("r1", "Reading the board's row tests."))
		},
		blockStep(firstEntered, firstRelease, func() llm.Response {
			return toolCallResponse(readFileCall("r2", "  "))
		}),
		blockStep(secondEntered, secondRelease, func() llm.Response { return finalResponse("done") }),
	)
	delegateID, child := f.spawn(t)

	<-firstEntered
	close(firstRelease)
	<-secondEntered
	// The blank call has started and finished inside this round; its excerpt
	// is the empty clear word, which no tool call states — the line from the
	// first call must still be standing.
	if got := durableIntentState(t, f.parent.delegateController, delegateID).LatestIntent; got != "Reading the board's row tests." {
		t.Fatalf("a call stating only whitespace changed the standing line to %q", got)
	}
	close(secondRelease)
	waitSubagentRun(t, child)
}

// TestNewestStartedCallWinsWithinOneBatch pins the batch rule: within one
// batch, the newest started call's intent is the line the row keeps. The two
// calls are apply_patch calls, which the ordered-group algorithm serializes in
// content order, so the second call's start is deterministically the newest.
func TestNewestStartedCallWinsWithinOneBatch(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	f := newIntentReportFixture(t,
		func(llm.Request) llm.Response {
			return toolCallResponse(
				applyPatchCall("p1", "Adding the alpha notes.", "alpha.txt"),
				applyPatchCall("p2", "Fixing the beta parser.", "beta.txt"),
			)
		},
		blockStep(entered, release, func() llm.Response { return finalResponse("done") }),
	)
	delegateID, child := f.spawn(t)

	<-entered
	if got := durableIntentState(t, f.parent.delegateController, delegateID).LatestIntent; got != "Fixing the beta parser." {
		t.Fatalf("the row kept %q, want the newest started call's intent", got)
	}
	close(release)
	waitSubagentRun(t, child)
}

// TestGrandchildIntentNeverLandsOnItsParentRow pins ruling 1's own-session
// fence through a real grandchild: the child spawns a delegate, the grandchild
// states an intent on its own tool call, and the words land only on the
// grandchild's own row — never on the child's, whose only call stated none.
// The provider dispatches on request content because the delegate tool call
// is detached: the child's continuation rounds interleave with the
// grandchild's rounds.
func TestGrandchildIntentNeverLandsOnItsParentRow(t *testing.T) {
	const grandchildPrompt = "read the notes and report"
	childEntered := make(chan struct{})
	childRelease := make(chan struct{})
	var childEnteredOnce sync.Once
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	f := newIntentReportDispatchFixture(t, func(req llm.Request) llm.Response {
		switch {
		case intentRequestHasToolCall(req, "delegate"):
			// The spawning child's continuation round: hold it open so the
			// fence assertion sees a LIVE child row while the grandchild
			// states its intent — a settled row would reject a misbound
			// report as stale and the test would pass vacuously.
			childEnteredOnce.Do(func() { close(childEntered) })
			<-childRelease
			return finalResponse("child done")
		case intentRequestLastUserText(req) == grandchildPrompt && intentRequestHasToolCall(req, "read_file"):
			// The grandchild's second round: hold it so mid-run assertions see
			// the tree live, then let it report.
			enteredOnce.Do(func() { close(entered) })
			<-release
			return finalResponse("grandchild done")
		case intentRequestLastUserText(req) == grandchildPrompt:
			return toolCallResponse(readFileCall("g1", "Inspecting the grandchild's own notes."))
		default:
			// The child's first round: spawn the grandchild without stating
			// an intent of its own.
			raw, _ := json.Marshal(map[string]any{"prompt": grandchildPrompt})
			return toolCallResponse(llm.ToolCallData{ID: "spawn", Name: "delegate", Type: "function", Arguments: raw})
		}
	})
	childDelegateID, child := f.spawn(t)

	select {
	case <-childEntered:
		// TRIPWIRE: the detached spawn continues in milliseconds; 30s only
		// fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("the child never reached its continuation round")
	}
	select {
	case <-entered:
		// TRIPWIRE: the grandchild's rounds answer in milliseconds; 30s only
		// fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("the grandchild never reached its second round")
	}
	controller := f.parent.delegateController
	if got := durableIntentState(t, controller, childDelegateID).LatestIntent; got != "" {
		t.Fatalf("the child's row carries %q, want nothing: the child's own call stated no intent", got)
	}
	var grandchildID string
	controller.mu.Lock()
	for id, aggregate := range controller.durable {
		if id != childDelegateID && aggregate != nil && aggregate.Descriptor.ChildSessionID != child.sess.id {
			if grandchildID != "" {
				t.Fatalf("more than one non-child delegate row: %s and %s", grandchildID, id)
			}
			grandchildID = id
		}
	}
	controller.mu.Unlock()
	if grandchildID == "" {
		t.Fatal("the grandchild never got its own delegate row")
	}
	if got := durableIntentState(t, controller, grandchildID).LatestIntent; got != "Inspecting the grandchild's own notes." {
		t.Fatalf("the grandchild's own row carries %q, want its stated intent", got)
	}
	if got := durableIntentState(t, controller, childDelegateID).Phase; got != delegatestore.PhaseRunning {
		t.Fatalf("the child's row is %s, want running: the fence must be judged against a live row", got)
	}
	close(release)
	close(childRelease)
	waitSubagentRun(t, child)

	// Settlement retires the grandchild's line through the real run finish.
	// TRIPWIRE: the settle rides the run's own completion path in
	// milliseconds; 30s only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "the grandchild's row to settle with an empty line", func() bool {
		return durableIntentState(t, controller, grandchildID).LatestIntent == ""
	})
}

// TestSessionExecutionStartClearsTheReportedIntent pins both child-side sites
// in one real session: a tool call's stated intent reaches the report
// callback Excerpt-bounded, and the session's next execution start clears the
// reported line — the root meter's own boundary.
func TestSessionExecutionStartClearsTheReportedIntent(t *testing.T) {
	long := "Reading "
	for len(long) < 4*appwire.MaxIntentRunes {
		long += "the very long board's row tests "
	}
	want := appwire.Excerpt(long, appwire.MaxIntentRunes)
	f := newIntentReportFixture(t,
		func(llm.Request) llm.Response {
			return toolCallResponse(readFileCall("r1", long))
		},
		func(llm.Request) llm.Response { return finalResponse("done") },
	)
	sess := f.parent
	var reported []string
	sess.cfg.spawn.parentDelegateID = "dlg_report_target"
	sess.cfg.spawn.parentJobIntent = func(_, intent string) {
		reported = append(reported, intent)
	}

	if _, err := sess.ProcessInput(context.Background(), "do the reading", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if len(reported) != 2 || reported[0] != "" || reported[1] != want {
		t.Fatalf("the turn reported %q, want the execution-start clear then the bounded intent", reported)
	}
	sess.beginExecution("")
	if len(reported) != 3 || reported[2] != "" {
		t.Fatalf("after the next execution start the reports are %q, want a trailing clear", reported)
	}
}

// TestNoteParentJobIntentGuards mirrors noteParentJobActivity's own guards: a
// nil session, a missing callback, and a session without a parent delegate id
// all report nothing.
func TestNoteParentJobIntentGuards(t *testing.T) {
	var s *Session
	s.noteParentJobIntent("stated") // must not panic

	called := false
	bound := newTestSession(t)
	bound.cfg.spawn.parentDelegateID = "dlg_parent"
	bound.cfg.spawn.parentJobIntent = func(id, intent string) {
		if id != "dlg_parent" || intent != "stated" {
			t.Errorf("parentJobIntent called with id=%q intent=%q", id, intent)
		}
		called = true
	}
	bound.noteParentJobIntent("stated")
	if !called {
		t.Fatal("parentJobIntent callback should have been called")
	}

	called = false
	unparented := newTestSession(t)
	unparented.cfg.spawn.parentJobIntent = func(string, string) { called = true }
	unparented.noteParentJobIntent("stated")
	if called {
		t.Fatal("parentJobIntent should not be called without parentDelegateID")
	}
}
