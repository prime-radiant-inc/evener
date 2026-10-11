//go:build eval

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
	modelFailures = recordRootStep(t, modelFailures, ctx, err)
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
				modelFailures = recordRootStep(t, modelFailures, ctx, err)
			}
		case <-ctx.Done():
			break wait
		}
	}

	sawUpdate := slices.ContainsFunc(rootAttentionFold(t, root).order, func(id string) bool { return strings.Contains(id, "/update/") })
	answer := classifyParentAnswer(toolCallsAfterQuestion(rootEntries(t, root)), codename)
	childEntries := delegateEntries(t, root)
	// A provider or runtime failure in the delegate is not model behavior, so
	// it stops the run instead of being tallied as an unanswered question. A
	// model that answers with empty or bare text is model behavior: it is
	// tallied and named in the RESULT line.
	for _, failure := range turnFailures(childEntries) {
		if !isModelResponseFailure(failure) {
			t.Fatalf("the delegate's turn failed, so this run says nothing about the model: %s", failure)
		}
		modelFailures = append(modelFailures, failure)
	}
	received, briefLeaked := childReceivedCodename(childEntries, codename)
	t.Logf("RESULT model=%s update_reached_root=%v parent_answer=%s child_received=%v brief_leaked=%v report_written=%v model_failures=%q", *delegateUpdateEvalModel, sawUpdate, answer, received, briefLeaked, reportWritten, modelFailures)
}

// delegateEntries reads the transcript of the root's one delegate: the only
// other transcript in the root's state directory. It is nil when no delegate
// started.
func delegateEntries(t *testing.T, root *Session) []transcript.Entry {
	t.Helper()
	paths, err := filepath.Glob(transcriptPath(root.stateDir, "*"))
	if err != nil {
		t.Fatalf("glob transcripts: %v", err)
	}
	paths = slices.DeleteFunc(paths, func(path string) bool { return path == root.TranscriptPath() })
	if len(paths) == 0 {
		return nil
	}
	if len(paths) != 1 {
		t.Fatalf("want one delegate transcript, got %v", paths)
	}
	data, err := readTranscriptFull(paths[0], "")
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	return data.Entries
}

// recordRootStep adds the failure of one root turn to modelFailures when it
// is the session giving up on a model that kept returning empty or bare text.
// Any other error, a provider or runtime failure, stops the run; so does
// nothing when the turn only ended because the run's own deadline passed.
func recordRootStep(t *testing.T, modelFailures []string, ctx context.Context, err error) []string {
	t.Helper()
	switch {
	case err == nil || ctx.Err() != nil:
		return modelFailures
	case errors.Is(err, errEmptyResponseExhausted) || errors.Is(err, errBareTextWithoutResultTool):
		return append(modelFailures, err.Error())
	}
	t.Fatalf("the root's turn failed, so this run says nothing about the model: %v", err)
	return modelFailures
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
// after its brief, the first user-input entry (environment context is
// harness-written and precedes it): received is true only when a later
// user-role entry (the parent's steering) carries it. A codename already
// in the brief means the delegate never had to ask, so briefLeaked marks runs
// that cannot show an answer arriving. Files the delegate wrote do not count.
func childReceivedCodename(entries []transcript.Entry, codename string) (received, briefLeaked bool) {
	seenBrief := false
	for _, e := range entries {
		if e.Turn.Message.Role != llm.RoleUser {
			continue
		}
		carries := strings.Contains(e.Turn.Message.Text(), codename)
		if !seenBrief && e.Turn.Kind == schema.TurnUserInput {
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
	environment := func(text string) transcript.Entry {
		var e transcript.Entry
		e.Turn.Kind = schema.TurnEnvironment
		e.Turn.Message = llm.User(text)
		return e
	}
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

func TestRecordRootStep(t *testing.T) {
	live, cancel := context.WithCancel(context.Background())
	defer cancel()
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
		if got := recordRootStep(t, nil, tc.ctx, tc.err); len(got) != tc.want {
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
	t.Helper()
	data, err := readTranscriptFull(root.TranscriptPath(), "")
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	return data.Entries
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
