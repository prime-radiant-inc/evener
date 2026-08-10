package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

func TestRunDetachedCommandSurvivesExit(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("detached execution is unsupported on this platform")
	}

	workDir := t.TempDir()
	pidPath := filepath.Join(workDir, "detached.pid")
	releasePath := filepath.Join(workDir, "release")
	completedPath := filepath.Join(workDir, "completed")
	command := fmt.Sprintf(
		"printf '%%s' $$ > %s; while [ ! -f %s ]; do sleep 0.01; done; printf completed > %s",
		strconv.Quote(pidPath), strconv.Quote(releasePath), strconv.Quote(completedPath),
	)
	released := false
	t.Cleanup(func() {
		if released || !t.Failed() {
			return
		}
		pidData, ok := waitForFileContent(pidPath, time.Second)
		if !ok {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(pidData))
		if err != nil {
			return
		}
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
		}
	})

	adapter := &scriptedProvider{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				args, _ := json.Marshal(map[string]any{"command": command, "mode": "detached"})
				return scriptedToolCalls(llm.ToolCallData{ID: "detached_shell", Name: "shell", Arguments: args, Type: "function"})
			},
			func(llm.Request) llm.Response { return scriptedCommunicate("detached command started") },
		},
	}
	installRunScriptedProvider(t, adapter)

	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), runConfig{
		prompt:  "Start the supplied command in detached mode, then report that it started.",
		model:   "openai/gpt-test",
		workDir: workDir,
		stdout:  &stdout,
		stderr:  &stderr,
	}); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "detached command started") {
		t.Fatalf("stdout = %q, want scripted final response", stdout.String())
	}

	requests := adapter.Requests()
	if len(requests) != 2 {
		t.Fatalf("scripted provider requests = %d, want 2", len(requests))
	}
	toolResult, ok := requestToolResult(requests[1], "detached_shell")
	if !ok {
		t.Fatal("second scripted request did not include the detached shell result")
	}
	if toolResult.IsError {
		t.Fatalf("detached shell result is an error: %#v", toolResult)
	}
	resultContent, ok := toolResult.Content.(string)
	if !ok {
		t.Fatalf("detached shell result content = %T, want string", toolResult.Content)
	}
	var detachedResult struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal([]byte(resultContent), &detachedResult); err != nil {
		t.Fatalf("decode detached shell result %q: %v", toolResult.Content, err)
	}
	if detachedResult.PID <= 0 {
		t.Fatalf("detached shell result PID = %d, want positive PID", detachedResult.PID)
	}

	pidData, ok := waitForFileContent(pidPath, 5*time.Second)
	if !ok {
		t.Fatalf("detached command did not write PID file for returned PID %d", detachedResult.PID)
	}
	launchedPID, err := strconv.Atoi(strings.TrimSpace(pidData))
	if err != nil {
		t.Fatalf("parse detached PID %q: %v", pidData, err)
	}
	if launchedPID != detachedResult.PID {
		t.Fatalf("detached PID file = %d, tool result PID = %d", launchedPID, detachedResult.PID)
	}
	if _, err := os.Stat(completedPath); !os.IsNotExist(err) {
		t.Fatalf("detached command completed before release: %v", err)
	}

	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release detached command: %v", err)
	}
	released = true
	completed, ok := waitForFileContent(completedPath, 5*time.Second)
	if !ok {
		t.Fatalf("detached command PID %d did not survive run exit", detachedResult.PID)
	}
	if completed != "completed" {
		t.Fatalf("completion marker = %q, want completed", completed)
	}
}
