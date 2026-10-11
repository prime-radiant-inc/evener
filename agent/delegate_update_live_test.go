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
	sends, updates := rootAnswerCalls(t, root)
	answer := "none"
	switch {
	case sends > 0:
		answer = "delegate_send"
	case updates > 0:
		answer = "communicate"
	}
	t.Logf("RESULT model=%s update_reached_root=%v parent_answer=%s child_received=%v", *delegateUpdateEvalModel, sawUpdate, answer, childReceived)
}

// rootAnswerCalls counts the root's delegate_send calls and its
// communicate(end_turn=false) calls: the two tools a parent can answer a
// delegate with.
func rootAnswerCalls(t *testing.T, root *Session) (sends, updates int) {
	t.Helper()
	data, err := readTranscriptFull(root.TranscriptPath(), "")
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	for _, e := range data.Entries {
		for _, part := range e.Turn.Message.Content {
			if part.Kind != llm.ContentToolCall || part.ToolCall == nil {
				continue
			}
			switch part.ToolCall.Name {
			case "delegate_send":
				sends++
			case "communicate":
				var args struct {
					EndTurn bool `json:"end_turn"`
				}
				if json.Unmarshal(part.ToolCall.Arguments, &args) == nil && !args.EndTurn {
					updates++
				}
			}
		}
	}
	return sends, updates
}
