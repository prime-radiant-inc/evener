//go:build eval

package agent

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
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
	childReceived := false
wait:
	for {
		if data, err := os.ReadFile(report); err == nil && strings.TrimSpace(string(data)) == codename {
			childReceived = true
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

	sawUpdate := false
	for _, id := range rootAttentionFold(t, root).order {
		sawUpdate = sawUpdate || strings.Contains(id, "/update/")
	}
	answer := classifyParentAnswer(rootToolCalls(t, root), codename)
	t.Logf("RESULT model=%s update_reached_root=%v parent_answer=%s child_received=%v", *delegateUpdateEvalModel, sawUpdate, answer, childReceived)
}

// rootToolCalls lists the root's tool calls in transcript order.
func rootToolCalls(t *testing.T, root *Session) []llm.ToolCallData {
	t.Helper()
	data, err := readTranscriptFull(root.TranscriptPath(), "")
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	var calls []llm.ToolCallData
	for _, e := range data.Entries {
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
