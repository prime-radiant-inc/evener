//go:build eval

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all"
)

var delegateUpdateEvalModel = flag.String("delegate-update-eval-model", "", "registry model selector for the explicitly opted-in delegate update micro-test")

// TestDelegateUpdateGetsItsParentsAnswerLive is the spec's optional live
// micro-test. A delegate needs a fact only its parent has, asks for it with
// communicate(end_turn=false), and the update wakes the parent. The test
// records how the parent answers (delegate_send is right, communicate is
// wrong) and whether the delegate receives the answer, logging one RESULT
// line per run. It fails only when the harness breaks, never on model
// behavior, so repeated runs can be tallied.
//
// Run: EVENER_LIVE_TESTS=1 go test -tags eval ./agent/ -run TestDelegateUpdateGetsItsParentsAnswerLive -delegate-update-eval-model <selector> -v -timeout 15m
func TestDelegateUpdateGetsItsParentsAnswerLive(t *testing.T) {
	r, _ := liveEvalRegistry(t)
	if *delegateUpdateEvalModel == "" {
		t.Fatal("-delegate-update-eval-model is required")
	}
	client := llm.NewClient(llm.WithRegistry(r))
	prof, err := provider.Resolve(client.Registry(), *delegateUpdateEvalModel)
	if err != nil {
		t.Fatalf("resolve %s: %v", *delegateUpdateEvalModel, err)
	}
	dir := t.TempDir()
	root, err := NewSession(client, prof, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer root.Close()
	wakes := make(chan struct{}, 1)
	root.SetNotifyFunc(func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})

	const codename = "HALCYON-42"
	brief := "Start exactly one delegate with this prompt, word for word: \"Write a file named report.txt in the working directory whose only line is the codename your parent agent holds. " +
		"You do not know the codename: ask your parent for it with communicate(end_turn=false), then list the working directory with the shell while you wait, " +
		"and write the file once your parent's answer arrives. Finish with communicate(end_turn=true).\" " +
		"When the delegate asks you for the codename, answer it. The codename is " + codename + ". Do not write report.txt yourself."
	// TRIPWIRE: real provider round trips across two sessions; only a wedged
	// live call reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	var modelFailures []string
	_, err = root.ProcessInput(ctx, brief, nil)
	modelFailures = recordRootStep(ctx, t, modelFailures, err)
	report := filepath.Join(dir, "report.txt")
	reportWritten := false
wait:
	for {
		if data, err := os.ReadFile(report); err == nil && strings.TrimSpace(string(data)) == codename {
			reportWritten = true
			break
		}
		select {
		case <-wakes:
			if root.hasPendingRootDelegateAttention() {
				_, err := root.ProcessInputKind(ctx, "", nil, EntryNotification)
				modelFailures = recordRootStep(ctx, t, modelFailures, err)
			}
		case <-ctx.Done():
			break wait
		}
	}

	sawUpdate := slices.ContainsFunc(rootAttentionFold(t, root).order, func(id string) bool { return strings.Contains(id, "/update/") })
	answer := classifyParentAnswer(toolCallsAfterQuestion(rootEntries(t, root)), codename)
	// TRIPWIRE: a delegate that already reported settles within moments; the
	// bound only keeps a wedged one from hanging the run.
	settleCtx, stopSettling := context.WithTimeout(context.Background(), time.Minute)
	defer stopSettling()
	delegates := delegateRuns(settleCtx, t, root)
	unsettled, runErrUnknown := 0, 0
	// A provider or runtime failure in a delegate is not model behavior, so it
	// stops the run instead of being tallied as an unanswered question. That
	// covers a failed turn in its transcript and the run error the session
	// keeps when no failed turn was recorded. A model that answers with empty
	// or bare text is model behavior: it is tallied and named in the RESULT
	// line.
	received, briefLeaked := false, false
	for _, d := range delegates {
		if d.unsettled {
			unsettled++
		}
		if d.runErrUnknown {
			runErrUnknown++
		}
		// A delegate the model stopped (job_stop) ends canceled, and one that
		// kept returning empty or bare text ends with that error: model
		// behavior, named in the RESULT line. Any other error, alone or joined
		// to those, is a harness failure.
		failures, harnessFailure := delegateModelFailures(d)
		if harnessFailure != nil {
			t.Fatalf("a delegate failed, so this run says nothing about the model: %v", harnessFailure)
		}
		modelFailures = append(modelFailures, failures...)
		r, leaked := childReceivedCodename(d.entries, codename)
		received, briefLeaked = received || r, briefLeaked || leaked
	}
	t.Logf("RESULT model=%s update_reached_root=%v parent_answer=%s delegates=%d unsettled=%d run_error_unknown=%d child_received=%v brief_leaked=%v report_written=%v model_failures=%q", *delegateUpdateEvalModel, sawUpdate, answer, len(delegates), unsettled, runErrUnknown, received, briefLeaked, reportWritten, modelFailures)
}

// delegateModelFailures lists the model-behavior failures of one delegate,
// each once: its recorded failed turns, plus its run error when no failed turn
// already carries it (the final failure is usually both). harnessFailure is
// the first failure that is not the model's doing.
func delegateModelFailures(d delegateRun) (failures []string, harnessFailure error) {
	for _, failure := range turnFailures(d.entries) {
		if !isModelResponseFailure(failure) {
			return nil, errors.New(failure)
		}
		failures = append(failures, failure)
	}
	if d.runErr != nil {
		if !isModelBehaviorError(d.runErr) {
			return nil, d.runErr
		}
		if !slices.Contains(failures, d.runErr.Error()) {
			failures = append(failures, d.runErr.Error())
		}
	}
	return failures, nil
}

// delegateRun is one delegate the root started: its transcript and the run
// error its session settled with. unsettled marks a run still going when the
// settle wait ran out, whose error is not yet known.
type delegateRun struct {
	entries   []transcript.Entry
	runErr    error
	unsettled bool
	// runErrUnknown marks a delegate this root cannot look up: one the
	// session already reclaimed, or a deeper descendant it does not own. Its
	// run error is unknown and reported as such, never assumed clean.
	runErrUnknown bool
}

// delegateRuns reads every delegate the root started: each transcript in the
// root's state directory other than the root's own. The model may start more
// than one, so the count is part of the result rather than an assumption. A
// delegate still running is given until ctx ends to settle, because its run
// error is assigned as it finishes.
func delegateRuns(ctx context.Context, t *testing.T, root *Session) []delegateRun {
	t.Helper()
	paths, err := filepath.Glob(transcriptPath(root.stateDir, "*"))
	if err != nil {
		t.Fatalf("glob transcripts: %v", err)
	}
	paths = slices.DeleteFunc(paths, func(path string) bool { return path == root.TranscriptPath() })
	runs := make([]delegateRun, 0, len(paths))
	for _, path := range paths {
		run := delegateRun{}
		sub := root.getSub(strings.TrimSuffix(filepath.Base(path), ".transcript.jsonl"))
		if sub == nil {
			run.runErrUnknown = true
		} else {
			sub.mu.Lock()
			done := sub.done
			sub.mu.Unlock()
			// An already-settled run counts as settled even when the shared
			// deadline has passed, so look at done before waiting on either.
			select {
			case <-done:
			default:
				select {
				case <-done:
				case <-ctx.Done():
					run.unsettled = true
				}
			}
			sub.mu.Lock()
			run.runErr = sub.err
			sub.mu.Unlock()
		}
		run.entries = readEntries(t, path)
		runs = append(runs, run)
	}
	return runs
}

// readEntries reads a transcript's entries.
func readEntries(t *testing.T, path string) []transcript.Entry {
	t.Helper()
	data, err := readTranscriptFull(path, "")
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	return data.Entries
}

// recordRootStep adds the failure of one root turn to modelFailures when it
// is the session giving up on a model that kept returning empty or bare text.
// Any other error, a provider or runtime failure, stops the run. A turn that
// only ended because the run's own deadline passed records nothing.
func recordRootStep(ctx context.Context, t *testing.T, modelFailures []string, err error) []string {
	t.Helper()
	switch {
	case err == nil || ctx.Err() != nil:
		return modelFailures
	case isModelBehaviorError(err):
		return append(modelFailures, err.Error())
	}
	t.Fatalf("the root's turn failed, so this run says nothing about the model: %v", err)
	return modelFailures
}

// isModelBehaviorError reports whether every error inside err, looking through
// wrapping and joins, is the model's own doing: the session giving up on empty
// or bare-text responses, or a cancellation (a stopped delegate). One
// provider or runtime error anywhere in a join makes the whole error not
// model behavior.
func isModelBehaviorError(err error) bool {
	if err == nil {
		return false
	}
	// A provider error that wraps a cancellation is still a provider error.
	if _, ok := errors.AsType[llm.Error](err); ok {
		return false
	}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		inner := joined.Unwrap()
		return len(inner) > 0 && !slices.ContainsFunc(inner, func(e error) bool { return !isModelBehaviorError(e) })
	}
	return errors.Is(err, errEmptyResponseExhausted) || errors.Is(err, errBareTextWithoutResultTool) || errors.Is(err, context.Canceled)
}

// turnFailures lists the message of every failed turn, in order.
func turnFailures(entries []transcript.Entry) []string {
	var failures []string
	for _, e := range entries {
		if e.Turn.Kind == schema.TurnFailure && e.Turn.Error != nil {
			failures = append(failures, e.Turn.Error.Message)
		}
	}
	return failures
}

// isModelResponseFailure reports whether a failed turn's message is the
// session giving up on a model that kept returning empty or bare text, as
// opposed to a provider or runtime error.
func isModelResponseFailure(message string) bool {
	return message == (&emptyResponseExhaustedError{retries: maxEmptyRetries}).Error() ||
		message == (&bareTextWithoutResultToolError{toolName: tool.DefaultResultToolName, retries: maxBareTextRetries}).Error()
}

// childReceivedCodename reports whether the delegate was handed the codename
// after its brief. The parent's delegate_send arrives in the delegate's
// transcript as a user-input entry when the delegate was idle, as does the
// brief (the first one), or as steering sourced from the parent when it was
// running; harness entries such as environment and notes context, the
// harness's own steering nudges and compaction summaries are not the parent
// and never count. received is true only when a later such entry carries the
// codename. A codename already
// in the brief means the delegate never had to ask, so briefLeaked marks runs
// that cannot show an answer arriving. Files the delegate wrote do not count.
func childReceivedCodename(entries []transcript.Entry, codename string) (received, briefLeaked bool) {
	seenBrief := false
	for _, e := range entries {
		fromParent := e.Turn.Kind == schema.TurnSteering && e.Turn.SteeringSource == events.SteeringSourceParent
		if e.Turn.Kind != schema.TurnUserInput && !fromParent {
			continue
		}
		carries := strings.Contains(e.Turn.Message.Text(), codename)
		if !seenBrief {
			seenBrief, briefLeaked = true, carries
			continue
		}
		received = received || carries
	}
	return received && !briefLeaked, briefLeaked
}

func TestChildReceivedCodename(t *testing.T) {
	user := func(text string) transcript.Entry {
		var e transcript.Entry
		e.Turn.Kind = schema.TurnUserInput
		e.Turn.Message = llm.User(text)
		return e
	}
	harness := func(kind schema.TurnKind, text string) transcript.Entry {
		var e transcript.Entry
		e.Turn.Kind = kind
		e.Turn.Message = llm.User(text)
		return e
	}
	environment := func(text string) transcript.Entry { return harness(schema.TurnEnvironment, text) }
	notes := func(text string) transcript.Entry { return harness(schema.TurnNotesContext, text) }
	assistant := func(text string) transcript.Entry {
		var e transcript.Entry
		e.Turn.Message = llm.Assistant(text)
		return e
	}
	for _, tc := range []struct {
		name         string
		entries      []transcript.Entry
		wantReceived bool
		wantLeaked   bool
	}{
		{"no delegate", nil, false, false},
		{"answer steered in", []transcript.Entry{user("ask me"), assistant("HALCYON-42?"), user("It is HALCYON-42")}, true, false},
		{"delegate wrote the codename itself", []transcript.Entry{user("ask me"), assistant("I will write HALCYON-42")}, false, false},
		{"parent never answered", []transcript.Entry{user("ask me"), assistant("waiting"), user("keep going")}, false, false},
		{"codename in the brief", []transcript.Entry{user("the codename is HALCYON-42"), user("HALCYON-42 again")}, false, true},
		{"codename in the brief after environment context", []transcript.Entry{environment("cwd: /work"), user("the codename is HALCYON-42")}, false, true},
		{"parent steering into a running delegate", []transcript.Entry{user("ask me"), {Turn: parentSteeringTurn("It is HALCYON-42", "turn-1", time.Time{})}}, true, false},
		{"steering nudge carries the codename", []transcript.Entry{user("ask me"), harness(schema.TurnSteering, "HALCYON-42")}, false, false},
		{"compaction summary carries the codename", []transcript.Entry{user("ask me"), harness(schema.TurnSummary, "HALCYON-42")}, false, false},
		{"notes context carries the codename after the brief", []transcript.Entry{user("ask me"), notes("note: HALCYON-42")}, false, false},
		{"codename before the brief is not an answer", []transcript.Entry{environment("HALCYON-42"), user("ask me")}, false, false},
		{"answer steered in after environment context", []transcript.Entry{environment("cwd: /work"), user("ask me"), user("It is HALCYON-42")}, true, false},
	} {
		received, leaked := childReceivedCodename(tc.entries, "HALCYON-42")
		if received != tc.wantReceived || leaked != tc.wantLeaked {
			t.Errorf("%s: got received=%v leaked=%v, want %v %v", tc.name, received, leaked, tc.wantReceived, tc.wantLeaked)
		}
	}
}

func TestIsModelResponseFailure(t *testing.T) {
	for message, want := range map[string]bool{
		(&emptyResponseExhaustedError{retries: maxEmptyRetries}).Error():                                             true,
		(&bareTextWithoutResultToolError{toolName: tool.DefaultResultToolName, retries: maxBareTextRetries}).Error(): true,
		"provider 429": false,
		"":             false,
	} {
		if got := isModelResponseFailure(message); got != want {
			t.Errorf("isModelResponseFailure(%q) = %v, want %v", message, got, want)
		}
	}
}

func TestDelegateModelFailuresCountsTheFinalFailureOnce(t *testing.T) {
	exhausted := &emptyResponseExhaustedError{retries: maxEmptyRetries}
	failed := func(message string) transcript.Entry {
		var e transcript.Entry
		e.Turn.Kind = schema.TurnFailure
		e.Turn.Error = &schema.TurnFailureInfo{Message: message}
		return e
	}
	// The final failure is both a recorded turn and the run error; an earlier
	// one that the delegate recovered from is recorded once.
	got, harness := delegateModelFailures(delegateRun{
		entries: []transcript.Entry{failed((&bareTextWithoutResultToolError{toolName: tool.DefaultResultToolName, retries: maxBareTextRetries}).Error()), failed(exhausted.Error())},
		runErr:  exhausted,
	})
	if harness != nil || len(got) != 2 {
		t.Errorf("got %q, harness %v; want the two distinct failures once each", got, harness)
	}
	// A run error with no recorded turn is still counted.
	got, _ = delegateModelFailures(delegateRun{runErr: exhausted})
	if len(got) != 1 {
		t.Errorf("got %q, want the run error once", got)
	}
}

func TestTurnFailures(t *testing.T) {
	failure := func(message string) transcript.Entry {
		var e transcript.Entry
		e.Turn.Kind = schema.TurnFailure
		e.Turn.Error = &schema.TurnFailureInfo{Message: message}
		return e
	}
	model := (&emptyResponseExhaustedError{retries: maxEmptyRetries}).Error()
	got := turnFailures([]transcript.Entry{{}, failure(model), failure("provider 429")})
	if len(got) != 2 || !isModelResponseFailure(got[0]) || isModelResponseFailure(got[1]) {
		t.Errorf("a model failure followed by a provider failure: got %q, want both, in order", got)
	}
	if got := turnFailures([]transcript.Entry{{}}); len(got) != 0 {
		t.Errorf("got %q, want none", got)
	}
}

func TestIsModelBehaviorError(t *testing.T) {
	model := &emptyResponseExhaustedError{retries: maxEmptyRetries}
	runtime := errors.New("write transcript: disk full")
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"empty responses exhausted", model, true},
		{"wrapped bare text", fmt.Errorf("turn: %w", &bareTextWithoutResultToolError{toolName: "communicate", retries: maxBareTextRetries}), true},
		{"canceled", context.Canceled, true},
		{"runtime error", runtime, false},
		{"model error joined to a runtime error", errors.Join(model, runtime), false},
		{"cancellation joined to a runtime error", errors.Join(context.Canceled, runtime), false},
		{"model error joined to cancellation", errors.Join(model, context.Canceled), true},
		{"provider error wrapping a cancellation", llm.NewStreamError("openai", "stream broke", context.Canceled), false},
	} {
		if got := isModelBehaviorError(tc.err); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRecordRootStep(t *testing.T) {
	live := t.Context()
	expired, expire := context.WithCancel(context.Background())
	expire()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
		want int
	}{
		{"success", live, nil, 0},
		{"deadline passed", expired, errors.New("context canceled"), 0},
		{"empty responses exhausted", live, &emptyResponseExhaustedError{retries: maxEmptyRetries}, 1},
		{"bare text exhausted", live, &bareTextWithoutResultToolError{toolName: "communicate", retries: maxBareTextRetries}, 1},
	} {
		if got := recordRootStep(tc.ctx, t, nil, tc.err); len(got) != tc.want {
			t.Errorf("%s: recorded %d model failures, want %d", tc.name, len(got), tc.want)
		}
	}
}

// The classification is a tally aid, not a proof: a question that lands while
// a parent request is in flight can let that response's narration count, and
// child_received (read from the delegate's own transcript) is the ground truth
// for whether an answer arrived.
//
// toolCallsAfterQuestion lists the tool calls from the entry that delivered
// the delegate's question onward, in transcript order. Calls before it, such
// as narration while starting the delegate, cannot answer it.
func toolCallsAfterQuestion(entries []transcript.Entry) []llm.ToolCallData {
	var calls []llm.ToolCallData
	asked := false
	for _, e := range entries {
		asked = asked || strings.Contains(e.Turn.AttentionID, "/update/")
		if !asked {
			continue
		}
		for _, part := range e.Turn.Message.Content {
			if part.Kind == llm.ContentToolCall && part.ToolCall != nil {
				calls = append(calls, *part.ToolCall)
			}
		}
	}
	return calls
}

// classifyParentAnswer names the tool the parent used to hand the delegate
// the codename: delegate_send (right), communicate (wrong: it reaches the
// parent's own human partner), or none. Only a call whose arguments carry the
// codename answers; narration such as "starting the delegate" does not. The
// parent knows the codename from its brief, so it may repeat it in a
// communicate message as well; a delegate_send carrying it is the answer
// whenever there is one.
func classifyParentAnswer(calls []llm.ToolCallData, codename string) string {
	answer := "none"
	for _, call := range calls {
		if !strings.Contains(string(call.Arguments), codename) {
			continue
		}
		switch call.Name {
		case "delegate_send":
			return "delegate_send"
		case "communicate":
			answer = "communicate"
		}
	}
	return answer
}

func rootEntries(t *testing.T, root *Session) []transcript.Entry {
	return readEntries(t, root.TranscriptPath())
}

// Narration that repeats the codename before the question arrives is not an
// answer, whether or not the question is answered afterwards.
func TestToolCallsAfterQuestionIgnoresEarlierNarration(t *testing.T) {
	entry := func(attentionID, tool, args string) transcript.Entry {
		var e transcript.Entry
		e.Turn.AttentionID = attentionID
		e.Turn.Message.Content = []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{Name: tool, Arguments: json.RawMessage(args)}}}
		return e
	}
	narration := entry("", "communicate", `{"message":"Starting a delegate to write HALCYON-42","end_turn":false}`)
	question := entry("dlg_1/update/7", "communicate", `{"message":"ok","end_turn":false}`)
	answer := entry("", "delegate_send", `{"message":"HALCYON-42"}`)

	if got := classifyParentAnswer(toolCallsAfterQuestion([]transcript.Entry{narration, question}), "HALCYON-42"); got != "none" {
		t.Errorf("unanswered question: got %s, want none", got)
	}
	if got := classifyParentAnswer(toolCallsAfterQuestion([]transcript.Entry{narration, question, answer}), "HALCYON-42"); got != "delegate_send" {
		t.Errorf("answered question: got %s, want delegate_send", got)
	}
}

func TestClassifyParentAnswer(t *testing.T) {
	call := func(name, args string) llm.ToolCallData {
		return llm.ToolCallData{Name: name, Arguments: json.RawMessage(args)}
	}
	for _, tc := range []struct {
		name  string
		calls []llm.ToolCallData
		want  string
	}{
		{"no calls", nil, "none"},
		{"narration is not an answer", []llm.ToolCallData{call("communicate", `{"message":"Starting the delegate","end_turn":false}`)}, "none"},
		{"delegate_send with the codename", []llm.ToolCallData{call("delegate", `{"prompt":"ask me"}`), call("delegate_send", `{"to":"dlg_1","message":"It is HALCYON-42"}`)}, "delegate_send"},
		{"communicate update with the codename", []llm.ToolCallData{call("communicate", `{"message":"HALCYON-42","end_turn":false}`)}, "communicate"},
		{"terminal communicate with the codename", []llm.ToolCallData{call("communicate", `{"message":"HALCYON-42","end_turn":true}`)}, "communicate"},
		{"delegate_send wins over an earlier communicate", []llm.ToolCallData{call("communicate", `{"message":"HALCYON-42","end_turn":false}`), call("delegate_send", `{"message":"HALCYON-42"}`)}, "delegate_send"},
	} {
		if got := classifyParentAnswer(tc.calls, "HALCYON-42"); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

// A model that starts more than one delegate is tallied, not rejected: every
// delegate's transcript is read, and a delegate that finished cleanly has no
// run error.
func TestDelegateRunsReadsEveryDelegate(t *testing.T) {
	adapter := newMessagingAdapter("FIRST-CHILD", "SECOND-CHILD")
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("one", "FIRST-CHILD: report the repository name.")
		},
		func(llm.Request) llm.Response {
			return delegateToolResponse("two", "SECOND-CHILD: report the repository name.")
		},
		func(llm.Request) llm.Response { return finalResponse("root done") },
	)
	root := newMessagingRoot(t, adapter, nil)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	adapter.waitForRequests(t, "FIRST-CHILD", 1)
	adapter.waitForRequests(t, "SECOND-CHILD", 1)
	runs := delegateRuns(ctx, t, root)
	if len(runs) != 2 {
		t.Fatalf("delegateRuns found %d delegates, want 2", len(runs))
	}
	expired, expire := context.WithCancel(t.Context())
	expire()
	for i, run := range delegateRuns(expired, t, root) {
		if run.unsettled {
			t.Errorf("delegate %d finished but counts as unsettled under an expired context", i)
		}
	}
	for i, run := range runs {
		if run.unsettled {
			t.Errorf("delegate %d did not settle", i)
		}
		if run.runErrUnknown {
			t.Errorf("delegate %d was reclaimed before its run error was read", i)
		}
		if len(run.entries) == 0 {
			t.Errorf("delegate %d has no transcript entries", i)
		}
		if run.runErr != nil {
			t.Errorf("delegate %d settled with %v, want a clean run", i, run.runErr)
		}
	}
}
