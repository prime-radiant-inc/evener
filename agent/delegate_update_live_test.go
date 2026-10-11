//go:build eval

package agent

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
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
	if _, err := root.ProcessInput(ctx, brief, nil); err != nil && ctx.Err() == nil {
		t.Fatalf("ProcessInput: %v", err)
	}
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
				if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil && ctx.Err() == nil {
					t.Fatalf("root notification turn: %v", err)
				}
			}
		case <-ctx.Done():
			break wait
		}
	}

	sawUpdate := slices.ContainsFunc(rootAttentionFold(t, root).order, func(id string) bool { return strings.Contains(id, "/update/") })
	answer := classifyParentAnswer(toolCallsAfterQuestion(rootEntries(t, root)), codename)
	childEntries := delegateEntries(t, root)
	// A provider or runtime failure in the delegate is not model behavior, so
	// it stops the run instead of being tallied as an unanswered question.
	if failure := firstTurnFailure(childEntries); failure != "" {
		t.Fatalf("the delegate's turn failed, so this run says nothing about the model: %s", failure)
	}
	received, briefLeaked := childReceivedCodename(childEntries, codename)
	t.Logf("RESULT model=%s update_reached_root=%v parent_answer=%s child_received=%v brief_leaked=%v report_written=%v", *delegateUpdateEvalModel, sawUpdate, answer, received, briefLeaked, reportWritten)
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

// firstTurnFailure is the message of the first failed turn, or "".
func firstTurnFailure(entries []transcript.Entry) string {
	for _, e := range entries {
		if e.Turn.Kind == schema.TurnFailure && e.Turn.Error != nil {
			return e.Turn.Error.Message
		}
	}
	return ""
}

// childReceivedCodename reports whether the delegate was handed the codename
// after its first input, which is its brief: received is true only when a
// later user-role entry (the parent's steering) carries it. A codename already
// in the brief means the delegate never had to ask, so briefLeaked marks runs
// that cannot show an answer arriving. Files the delegate wrote do not count.
func childReceivedCodename(entries []transcript.Entry, codename string) (received, briefLeaked bool) {
	seenBrief := false
	for _, e := range entries {
		if e.Turn.Message.Role != llm.RoleUser {
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
	} {
		received, leaked := childReceivedCodename(tc.entries, "HALCYON-42")
		if received != tc.wantReceived || leaked != tc.wantLeaked {
			t.Errorf("%s: got received=%v leaked=%v, want %v %v", tc.name, received, leaked, tc.wantReceived, tc.wantLeaked)
		}
	}
}

func TestFirstTurnFailure(t *testing.T) {
	var ok, failed transcript.Entry
	failed.Turn.Kind = schema.TurnFailure
	failed.Turn.Error = &schema.TurnFailureInfo{Message: "provider 429"}
	if got := firstTurnFailure([]transcript.Entry{ok, failed}); got != "provider 429" {
		t.Errorf("got %q, want the failure message", got)
	}
	if got := firstTurnFailure([]transcript.Entry{ok}); got != "" {
		t.Errorf("got %q, want none", got)
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
