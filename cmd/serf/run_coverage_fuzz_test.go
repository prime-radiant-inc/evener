//go:build serffuzz

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

func FuzzRunCoverage(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		t.Setenv("SERF_STATE_DIR", "")
		base := func(t *testing.T) runConfig {
			t.Helper()
			return runConfig{prompt: "ping", model: "openai/gpt-test", workDir: t.TempDir(), stateDir: t.TempDir(), noDefaultMarketplaces: true, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
		}
		withProvider := func(t *testing.T) {
			t.Helper()
			installRunScriptedProvider(t, &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return scriptedCommunicate("done") }}})
		}

		t.Run("startup failures", func(t *testing.T) {
			oldGetwd, oldEnsure, oldSeed := runGetwd, runEnsureUserConfigDirs, runSeedMarketplaces
			t.Cleanup(func() { runGetwd, runEnsureUserConfigDirs, runSeedMarketplaces = oldGetwd, oldEnsure, oldSeed })
			runGetwd = func() (string, error) { return "", errors.New("getwd") }
			if err := run(context.Background(), runConfig{}); err == nil {
				t.Fatal("want getwd error")
			}
			runGetwd = func() (string, error) { return t.TempDir(), nil }
			runEnsureUserConfigDirs = func() error { return errors.New("ensure") }
			if err := run(context.Background(), runConfig{}); err == nil {
				t.Fatal("want ensure error")
			}
			runEnsureUserConfigDirs = func() error { return nil }
			runSeedMarketplaces = func() error { return errors.New("seed") }
			if err := run(context.Background(), runConfig{stdout: io.Discard, stderr: io.Discard}); err == nil || !strings.Contains(err.Error(), "prompt") {
				t.Fatalf("error = %v", err)
			}
			t.Setenv("HOME", t.TempDir())
			if err := oldSeed(); err != nil {
				t.Fatal(err)
			}
		})

		t.Run("early validation", func(t *testing.T) {
			cfg := base(t)
			cfg.prompt = ""
			if err := run(context.Background(), cfg); err == nil {
				t.Fatal("want prompt error")
			}
			cfg = base(t)
			cfg.reasoningEffort = "bogus"
			if err := run(context.Background(), cfg); err == nil {
				t.Fatal("want effort error")
			}
			cfg = base(t)
			cfg.model = "bare"
			if err := run(context.Background(), cfg); err == nil {
				t.Fatal("want model error")
			}
			cfg = base(t)
			cfg.resume = "missing"
			if err := run(context.Background(), cfg); err == nil {
				t.Fatal("want resume error")
			}
		})

		t.Run("list sessions", func(t *testing.T) {
			dir := t.TempDir()
			var out bytes.Buffer
			if err := run(context.Background(), runConfig{workDir: dir, stateDir: dir, listSessions: true, noDefaultMarketplaces: true, stdout: &out, stderr: io.Discard}); err != nil {
				t.Fatal(err)
			}
			meta := schema.SessionMeta{ID: "session", Model: "model", UpdatedAt: time.Unix(1, 0), TurnCount: 2, EnvInfo: schema.EnvironmentInfo{GitBranch: "main"}}
			if err := schema.SaveSessionMeta(dir, meta); err != nil {
				t.Fatal(err)
			}
			meta.ID = "session-no-branch"
			meta.EnvInfo.GitBranch = ""
			if err := schema.SaveSessionMeta(dir, meta); err != nil {
				t.Fatal(err)
			}
			out.Reset()
			if err := listSessions(runConfig{stdout: &out}, dir); err != nil || !strings.Contains(out.String(), "main") {
				t.Fatalf("out=%q err=%v", out.String(), err)
			}
			bad := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(bad, []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := listSessions(runConfig{stdout: io.Discard}, bad); err == nil {
				t.Fatal("want list error")
			}
		})

		t.Run("client and construction failures", func(t *testing.T) {
			oldLoad, oldAttach, oldNew, oldProvision := runLoadClient, runAttachAPILogger, runNewSession, runProvisionSandbox
			t.Cleanup(func() {
				runLoadClient, runAttachAPILogger, runNewSession, runProvisionSandbox = oldLoad, oldAttach, oldNew, oldProvision
			})
			runLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
				return nil, providercfg.Config{}, false, errors.New("load")
			}
			if err := run(context.Background(), base(t)); err == nil {
				t.Fatal("want load error")
			}
			client := llm.NewClient()
			client.Register(&scriptedProvider{name: "openai"})
			runLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
				return client, scriptedProviderConfig("openai"), true, nil
			}
			runAttachAPILogger = func(*llm.Client, string, io.Writer) (func() error, error) { return nil, errors.New("log") }
			if err := run(context.Background(), base(t)); err == nil {
				t.Fatal("want logger error")
			}
			runAttachAPILogger = func(*llm.Client, string, io.Writer) (func() error, error) { return func() error { return nil }, nil }
			cfg := base(t)
			cfg.outputSchema = "{"
			if err := run(context.Background(), cfg); err == nil {
				t.Fatal("want schema error")
			}
			cfg = base(t)
			cfg.fastCheapModel = "missing/model"
			if err := run(context.Background(), cfg); err == nil {
				t.Fatal("want fast model error")
			}
			cfg = base(t)
			cfg.sandboxMode = "invalid"
			if err := run(context.Background(), cfg); err == nil {
				t.Fatal("want sandbox config error")
			}
			runProvisionSandbox = func(*execenv.LocalExecutionEnvironment, *agent.SessionConfig, string) error {
				return errors.New("provision")
			}
			if err := run(context.Background(), base(t)); err == nil {
				t.Fatal("want provision error")
			}
			runProvisionSandbox = oldProvision
			runNewSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, agent.SessionConfig) (*agent.Session, error) {
				return nil, errors.New("new")
			}
			if err := run(context.Background(), base(t)); err == nil {
				t.Fatal("want session error")
			}
		})

		t.Run("successful variants", func(t *testing.T) {
			withProvider(t)
			cfg := base(t)
			cfg.stateDir = ""
			cfg.maxSubagentDepth = 0
			cfg.reasoningEffort = "low"
			cfg.verbose = true
			if err := run(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			cfg = base(t)
			cfg.resumeWith = "missing"
			if err := run(context.Background(), cfg); err == nil {
				t.Fatal("want resume-with error")
			}
		})

		t.Run("resume variants", func(t *testing.T) {
			withProvider(t)
			oldRestore := runRestoreSession
			t.Cleanup(func() { runRestoreSession = oldRestore })
			dir := t.TempDir()
			meta := schema.SessionMeta{ID: "resume-id", ProfileID: "openai", Model: "gpt-old", UpdatedAt: time.Unix(2, 0), TurnCount: 3}
			if err := schema.SaveSessionMeta(dir, meta); err != nil {
				t.Fatal(err)
			}
			runRestoreSession = func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, _ schema.SessionMeta, rc agent.RestoreSessionConfig) (*agent.Session, error) {
				return agent.NewSession(client, profile, env, agent.SessionConfig{StateDir: rc.StateDir, NonInteractive: true, ResolveProfile: rc.ResolveProfile})
			}
			cfg := base(t)
			cfg.stateDir = dir
			cfg.resume = meta.ID
			cfg.prompt = ""
			cfg.model = ""
			cfg.reasoningEffort = "low"
			if err := run(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			withProvider(t)
			cfg = base(t)
			cfg.stateDir = dir
			cfg.resume = meta.ID
			cfg.model = "openai/gpt-new"
			if err := run(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			runRestoreSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, schema.SessionMeta, agent.RestoreSessionConfig) (*agent.Session, error) {
				return nil, errors.New("restore")
			}
			if err := run(context.Background(), cfg); err == nil {
				t.Fatal("want restore error")
			}
		})

		t.Run("processing outcomes", func(t *testing.T) {
			withProvider(t)
			oldProcess, oldDrain, oldLine := runProcessInput, runDrainJobTree, runSandboxLine
			t.Cleanup(func() { runProcessInput, runDrainJobTree, runSandboxLine = oldProcess, oldDrain, oldLine })
			runSandboxLine = func(execenv.ExecutionEnvironment) string { return "sandboxed" }
			runProcessInput = func(*agent.Session, context.Context, string) (string, error) { return "", errors.New("process") }
			if err := run(context.Background(), base(t)); err == nil {
				t.Fatal("want process error")
			}
			runProcessInput = func(*agent.Session, context.Context, string) (string, error) { return "first", nil }
			runDrainJobTree = func(*agent.Session, context.Context) (string, error) { return "", errors.New("drain") }
			if err := run(context.Background(), base(t)); err == nil {
				t.Fatal("want drain error")
			}
			runDrainJobTree = func(*agent.Session, context.Context) (string, error) { return "final", nil }
			if err := run(context.Background(), base(t)); err != nil {
				t.Fatal(err)
			}
		})

		t.Run("event formatting", func(t *testing.T) {
			cr, cw := 3, 4
			evs := []events.SessionEvent{
				{Kind: events.EventSessionStart, Data: events.SessionStartData{Model: "m"}}, {Kind: events.EventSessionStart, Data: events.WarningData{}},
				{Kind: events.EventPromptLoaded, Data: events.PromptLoadedData{Label: "p", Size: 1}}, {Kind: events.EventPromptLoaded, Data: events.WarningData{}},
				{Kind: events.EventAssistantTextEnd, Data: events.AssistantTextEndData{Text: " ", Usage: llm.Usage{CacheReadTokens: &cr, CacheWriteTokens: &cw}}}, {Kind: events.EventAssistantTextEnd, Data: events.WarningData{}},
				{Kind: events.EventAssistantTextEnd, Data: events.AssistantTextEndData{Text: "answer", Reasoning: "why"}},
				{Kind: events.EventToolCallStart, Data: events.ToolCallStartData{ToolName: "x", ArgumentsJSON: strings.Repeat("a", 101)}}, {Kind: events.EventToolCallStart, Data: events.WarningData{}},
				{Kind: events.EventToolCallEnd, Data: events.ToolCallEndData{ToolName: "x", Error: "e"}}, {Kind: events.EventToolCallEnd, Data: events.ToolCallEndData{ToolName: "x"}}, {Kind: events.EventToolCallEnd, Data: events.WarningData{}},
				{Kind: events.EventToolCallRepaired, Data: events.ToolCallRepairedData{ToolName: "x", Changes: []string{"c"}}}, {Kind: events.EventToolCallRepaired, Data: events.WarningData{}},
				{Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "m", EndTurn: true}}, {Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "m"}}, {Kind: events.EventCommunicate, Data: events.WarningData{}},
				{Kind: events.EventPluginLoaded, Data: events.PluginLoadedData{Name: "p"}}, {Kind: events.EventPluginLoaded, Data: events.WarningData{}},
				{Kind: events.EventHookStart, Data: events.HookStartData{}}, {Kind: events.EventHookStart, Data: events.WarningData{}}, {Kind: events.EventHookEnd, Data: events.HookEndData{}}, {Kind: events.EventHookEnd, Data: events.WarningData{}},
				{Kind: events.EventSkillActivated, Data: events.SkillActivatedData{Name: "s"}}, {Kind: events.EventSkillActivated, Data: events.WarningData{}}, {Kind: events.EventWarning, Data: events.WarningData{}}, {Kind: events.EventWarning, Data: events.WarningData{}}, {Kind: events.EventError, Data: events.ErrorData{}}, {Kind: events.EventError, Data: events.WarningData{}},
			}
			var out bytes.Buffer
			done := drainEventsHuman(feedEvents(evs), &out)
			<-done
			verbose := drainEventsVerbose(feedEvents(evs), io.Discard)
			<-verbose
		})
	})
}
