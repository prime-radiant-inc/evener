package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"primeradiant.com/serf/agent"
)

func TestRunSkipsDrainAfterFatalModelError(t *testing.T) {
	adapter := &scriptedProvider{name: "openai"}
	installRunScriptedProvider(t, adapter)

	processErr := errors.New("fatal initial model error")
	drainCalls := 0
	oldProcessInput := runProcessInput
	oldDrainJobTree := runDrainJobTree
	runProcessInput = func(*agent.Session, context.Context, string) (string, error) {
		return "", processErr
	}
	runDrainJobTree = func(*agent.Session, context.Context) (string, error) {
		drainCalls++
		return "", errors.New("unexpected drain error")
	}
	t.Cleanup(func() {
		runProcessInput = oldProcessInput
		runDrainJobTree = oldDrainJobTree
	})

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt:                "fail the initial model turn",
		model:                 "openai/gpt-test",
		workDir:               t.TempDir(),
		stateDir:              t.TempDir(),
		noDefaultMarketplaces: true,
		stdout:                &stdout,
		stderr:                &stderr,
	})
	if err != processErr || !errors.Is(err, processErr) {
		t.Fatalf("run error = %v, want original process error %v", err, processErr)
	}
	if drainCalls != 0 {
		t.Fatalf("drain calls = %d, want 0 after fatal initial model error", drainCalls)
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("provider requests = %d, want no notification turn", len(requests))
	}
}
