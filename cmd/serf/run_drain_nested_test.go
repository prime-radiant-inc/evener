package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

func scriptedDelegateCallWithAllowance(id, task string, allowance int) llm.Response {
	args, _ := json.Marshal(map[string]any{"task": task, "max_wait_ms": 0, "delegation_allowance": allowance})
	return scriptedToolCalls(llm.ToolCallData{ID: id, Name: "delegate", Arguments: args, Type: "function"})
}

// TestRunDrainsNestedDelegateSubtree is the roborev nested regression: a
// one-shot run whose root delegate (a coordinator) itself fire-and-returns a
// worker delegate must drain the WHOLE subtree before Close(). If the drain
// only watched the root's own jobs it could quiesce once the coordinator went
// idle and let Close() cancel the still-running worker.
//
// root --delegate(allowance=1)--> coordinator --delegate--> worker (writes file)
func TestRunDrainsNestedDelegateSubtree(t *testing.T) {
	tmp := t.TempDir()
	workerArtifact := filepath.Join(tmp, "worker-artifact.txt")

	const (
		rootPrompt = "ROOT-BUILD-PROMPT"
		coordTask  = "COORD-TASK coordinate the worker"
		workerTask = "WORKER-TASK write the artifact"
		finalMsg   = "BUILD-COMPLETE"
	)

	step := func(req llm.Request) llm.Response {
		text := requestFullText(req)
		switch {
		case strings.Contains(text, rootPrompt):
			// Root coordinator.
			switch {
			case strings.Contains(text, "<job-notification"):
				return scriptedCommunicate(finalMsg)
			case strings.Contains(text, "COORD-TASK"):
				return scriptedCommunicate("root waiting on coordinator")
			default:
				return scriptedDelegateCallWithAllowance("del_coord", coordTask, 1)
			}
		case strings.Contains(text, "COORD-TASK"):
			// Mid-tree coordinator: delegate the worker, then wait for it.
			switch {
			case strings.Contains(text, "<job-notification"):
				return scriptedCommunicate("coordinator done")
			case strings.Contains(text, "WORKER-TASK"):
				return scriptedCommunicate("coordinator waiting on worker")
			default:
				return scriptedDelegateCall("del_worker", workerTask)
			}
		default:
			// Worker: write the artifact, then report done.
			if _, err := os.Stat(workerArtifact); err != nil {
				return scriptedToolCalls(scriptedWriteFileCall("ww_1", workerArtifact, "artifact"))
			}
			return scriptedCommunicate("worker wrote artifact")
		}
	}

	steps := make([]func(llm.Request) llm.Response, 0, 40)
	for i := 0; i < 40; i++ {
		steps = append(steps, step)
	}
	installRunScriptedProvider(t, &scriptedProvider{name: "openai", steps: steps})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	err := run(ctx, runConfig{
		prompt:  rootPrompt + ": delegate a coordinator that delegates a worker; report BUILD-COMPLETE when the whole tree finishes.",
		model:   "openai/gpt-test",
		workDir: tmp,
		verbose: true,
		stdout:  &stdout,
		stderr:  &stderr,
	})
	if err != nil {
		t.Fatalf("run: %v\nstderr tail: %s", err, tailString(stderr.String(), 2000))
	}

	if _, statErr := os.Stat(workerArtifact); statErr != nil {
		t.Fatalf("expected nested worker artifact %s to exist (subtree drained, worker not killed); stat err: %v", workerArtifact, statErr)
	}
	if !strings.Contains(stdout.String(), finalMsg) {
		t.Fatalf("expected stdout to contain %q (whole subtree drained back to root); got stdout=%q", finalMsg, stdout.String())
	}
	if strings.Contains(stderr.String(), "stopped_by_parent") {
		t.Fatalf("a subtree job was SIGKILLed by Close() (stopped_by_parent) instead of draining; stderr tail: %s", tailString(stderr.String(), 2000))
	}
}

func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
