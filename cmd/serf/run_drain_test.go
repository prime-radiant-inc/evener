package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

// requestFullText concatenates every request message's text, tool-call
// arguments, and tool-result content so a scripted step can route on any of
// them (delegate task text lives in the assistant tool-call arguments; the
// re-drive turn is marked by a <job-notification> user message).
func requestFullText(req llm.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(m.Text())
		b.WriteByte('\n')
		for _, p := range m.Content {
			if p.ToolCall != nil {
				b.Write(p.ToolCall.Arguments)
				b.WriteByte('\n')
			}
			if p.ToolResult != nil {
				b.WriteString(fmt.Sprint(p.ToolResult.Content))
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

func scriptedDelegateCall(id, task string) llm.Response {
	args, _ := json.Marshal(map[string]any{"task": task, "max_wait_ms": 0})
	return scriptedToolCalls(llm.ToolCallData{ID: id, Name: "delegate", Arguments: args, Type: "function"})
}

// TestRunDrainsDelegatedJobTreeBeforeExit is the PRI-2441 B1 regression: a
// one-shot `serf run` whose coordinator fires a fire-and-return delegate must
// keep re-driving until the delegated work completes, instead of SIGKILLing the
// child at Close(). The coordinator's real final answer (BUILD-COMPLETE) is only
// produced on the post-completion <job-notification> turn, so its presence on
// stdout proves the drain ran.
func TestRunDrainsDelegatedJobTreeBeforeExit(t *testing.T) {
	tmp := t.TempDir()
	childArtifact := filepath.Join(tmp, "child-artifact.txt")

	const (
		rootPrompt = "ROOT-BUILD-PROMPT"
		childTask  = "CHILD-TASK-WRITE the artifact"
		finalMsg   = "BUILD-COMPLETE"
	)

	step := func(req llm.Request) llm.Response {
		text := requestFullText(req)
		isRoot := strings.Contains(text, rootPrompt)
		if isRoot {
			switch {
			case strings.Contains(text, "<job-notification"):
				// The delegate finished and its completion was drained back to the
				// coordinator: emit the real final answer.
				return scriptedCommunicate(finalMsg)
			case strings.Contains(text, "CHILD-TASK-WRITE"):
				// Delegate already dispatched; end the coordinator's turn while the
				// child runs (this is where ProcessInput returns and Close() would
				// otherwise kill the child).
				return scriptedCommunicate("waiting on delegate")
			default:
				return scriptedDelegateCall("del_1", childTask)
			}
		}
		// Child session: write the artifact, then report done.
		if _, err := os.Stat(childArtifact); err != nil {
			return scriptedToolCalls(scriptedWriteFileCall("cw_1", childArtifact, "artifact"))
		}
		return scriptedCommunicate("child wrote artifact")
	}

	// One shared step function serves both root and child turns; supply plenty of
	// slots so neither session is starved.
	steps := make([]func(llm.Request) llm.Response, 0, 16)
	for i := 0; i < 16; i++ {
		steps = append(steps, step)
	}
	installRunScriptedProvider(t, &scriptedProvider{name: "openai", steps: steps})

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt:  rootPrompt + ": delegate the build to a subagent and report BUILD-COMPLETE when it finishes.",
		model:   "openai/gpt-test",
		workDir: tmp,
		verbose: true,
		stdout:  &stdout,
		stderr:  &stderr,
	})
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), finalMsg) {
		t.Fatalf("expected stdout to contain %q (proves the delegate completion was drained); got stdout=%q", finalMsg, stdout.String())
	}
	if _, statErr := os.Stat(childArtifact); statErr != nil {
		t.Fatalf("expected child artifact %s to exist (child ran to completion, was not killed); stat err: %v", childArtifact, statErr)
	}
	if strings.Contains(stderr.String(), "stopped_by_parent") {
		t.Fatalf("child was SIGKILLed by Close() (stopped_by_parent) instead of draining to completion; stderr=%s", stderr.String())
	}
}
