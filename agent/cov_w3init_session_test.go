package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// w3init_failInitEnv wraps a real execution environment but forces Initialize to
// fail, exercising the env-initialize guard in NewSession and
// RestoreSessionFromMetaWithConfig. All other interface methods are promoted from
// the embedded value, so the wrapper is only ever exercised at Initialize.
type w3init_failInitEnv struct {
	execenv.ExecutionEnvironment
}

func (w3init_failInitEnv) Initialize() error { return errors.New("w3init boom initialize") }

// w3init_badToolStrategy is a context strategy whose Tools() advertises a tool
// with an empty (invalid) name, so the registry rejects it. It drives the
// strategy-tool registration error arm in NewSession.
type w3init_badToolStrategy struct{}

func (w3init_badToolStrategy) Name() string { return "w3init-bad-tool" }

func (w3init_badToolStrategy) Tools() []tool.RegisteredTool {
	return []tool.RegisteredTool{{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: ""}},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return nil, nil
		},
	}}
}

func (w3init_badToolStrategy) ManageContext(context.Context, *[]schema.Turn, int, func(events.EventKind, events.EventData)) error {
	return nil
}

func (w3init_badToolStrategy) AfterAction(context.Context, []schema.Turn, *llm.Client) error {
	return nil
}

// TestW3Init_NewSession_NilArgGuards covers the nil client/profile/env guards.
func TestW3Init_NewSession_NilArgGuards(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	profile := NewOpenAIProfile("gpt-5.2")
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())

	if _, err := NewSession(nil, profile, env, SessionConfig{}); err == nil || !strings.Contains(err.Error(), "llm client is nil") {
		t.Fatalf("nil client err = %v", err)
	}
	if _, err := NewSession(client, nil, env, SessionConfig{}); err == nil || !strings.Contains(err.Error(), "profile is nil") {
		t.Fatalf("nil profile err = %v", err)
	}
	if _, err := NewSession(client, profile, nil, SessionConfig{}); err == nil || !strings.Contains(err.Error(), "execution environment is nil") {
		t.Fatalf("nil env err = %v", err)
	}
}

// TestW3Init_NewSession_EnvInitializeError covers the env.Initialize failure arm.
func TestW3Init_NewSession_EnvInitializeError(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	env := w3init_failInitEnv{execenv.NewLocalExecutionEnvironment(t.TempDir())}

	_, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{})
	if err == nil || !strings.Contains(err.Error(), "env initialize") {
		t.Fatalf("err = %v, want env initialize error", err)
	}
}

// TestW3Init_NewSession_StrategyToolRegisterError covers the arm where a context
// strategy advertises a tool the registry rejects.
func TestW3Init_NewSession_StrategyToolRegisterError(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	cfg := SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{contextStrategyOverride: w3init_badToolStrategy{}},
	}
	_, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err == nil || !strings.Contains(err.Error(), "register strategy tool") {
		t.Fatalf("err = %v, want register strategy tool error", err)
	}
}

// TestW3Init_NewSession_SystemPromptFileReadError covers the initSessionState arm
// that reads a system prompt override file for a root (depth 0) session.
func TestW3Init_NewSession_SystemPromptFileReadError(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	missing := filepath.Join(t.TempDir(), "does-not-exist.md")
	cfg := SessionConfig{MaxSubagentDepth: 1, SystemPromptFile: missing}

	_, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err == nil || !strings.Contains(err.Error(), "reading system prompt override") {
		t.Fatalf("err = %v, want system prompt override read error", err)
	}
}
