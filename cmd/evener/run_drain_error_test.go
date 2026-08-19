package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
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
	if err != processErr || !errors.Is(err, processErr) { //nolint:errorlint // Exact error identity is the contract under test.
		t.Fatalf("run error = %v, want original process error %v", err, processErr)
	}
	if drainCalls != 0 {
		t.Fatalf("drain calls = %d, want 0 after fatal initial model error", drainCalls)
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("provider requests = %d, want no notification turn", len(requests))
	}
}

func TestRunFatalModelErrorShutsDownHeldShellWithoutDrainOrNotificationTurn(t *testing.T) {
	processErr := llm.ErrorFromHTTPStatus("openai", 403, "fatal model error after shell launch", nil, nil)
	executor := newHeldShellExecutor("fatal shell", "unexpected shell completion", 0)
	oldNewSession := runNewSession
	oldDrainJobTree := runDrainJobTree
	runNewSession = func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		return oldNewSession(client, profile, &shellExecutorEnvironment{ExecutionEnvironment: env, executor: executor}, cfg)
	}
	drainCalls := 0
	runDrainJobTree = func(*agent.Session, context.Context) (string, error) {
		drainCalls++
		return "", errors.New("unexpected drain")
	}
	t.Cleanup(func() {
		executor.releaseShell()
		runNewSession = oldNewSession
		runDrainJobTree = oldDrainJobTree
	})
	adapter := &scriptedProvider{
		name: "openai",
		errorSteps: []func(llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				return scriptedToolCalls(scriptedShellCall("fatal_shell", "fatal shell", "background")), nil
			},
			func(llm.Request) (llm.Response, error) {
				return llm.Response{}, processErr
			},
		},
	}
	installRunScriptedProvider(t, adapter)

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt:                "launch a shell and then fail",
		model:                 "openai/gpt-test",
		workDir:               t.TempDir(),
		stateDir:              t.TempDir(),
		noDefaultMarketplaces: true,
		stdout:                &stdout,
		stderr:                &stderr,
	})
	if !errors.Is(err, processErr) {
		t.Fatalf("run error = %v, want original process error %v", err, processErr)
	}
	if drainCalls != 0 {
		t.Fatalf("drain calls = %d, want 0 after fatal model error", drainCalls)
	}
	if requests := adapter.Requests(); len(requests) != 2 {
		t.Fatalf("provider requests = %d, want shell launch and fatal model turn only", len(requests))
	}
	select {
	case <-executor.release:
	default:
		t.Fatal("ordinary run shutdown did not signal the held shell")
	}
}
