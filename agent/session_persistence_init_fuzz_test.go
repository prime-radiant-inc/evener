//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
)

// FuzzPersistentSessionInitRestoreProgram drives the persistent constructor
// boundary without ever starting a model turn. It combines real Session setup
// and persistence with the three external seams that make the program
// deterministic: a scripted provider for prompt hooks, a deny execution
// environment, and a fake clock. The only filesystem surface is t.TempDir:
// the session state, prompt files, and plugin tree are all materialized below
// it. In particular, no Git, shell, MCP server, network request, or ambient
// user configuration participates in this target.
//
// The program covers fresh NewSession and RestoreSessionFromMetaWithConfig
// setup, including prompt composition, plugin prompt-hook delivery, durable
// meta/transcript creation, persisted config recovery, tool policy rebuilding,
// and the deferred-resume hook state. It intentionally does not process user
// input, create jobs, run shell tools, discover transcripts, or drive
// ask/goal/compaction workflows; those stateful lanes have dedicated targets.
func FuzzPersistentSessionInitRestoreProgram(f *testing.F) {
	for strategy := range pifStrategies {
		// The repeated byte vector makes every finite selector reachable from the
		// fixed corpus that the coverage runner replays, not just from long fuzzing.
		f.Add([]byte{byte(strategy), byte(strategy), byte(strategy), byte(strategy), byte(strategy), byte(strategy)})
	}
	f.Add([]byte{0, 0, 0, 0, 0, 0})
	f.Add([]byte{8, 3, 1, 1, 1, 1})

	f.Fuzz(func(t *testing.T, data []byte) {
		program := decodePIFProgram(data)
		root := t.TempDir()
		t.Setenv(envvars.XDGConfigHome.Name, filepath.Join(root, "xdg"))
		t.Setenv(envvars.SERFSessionOrigin.Name, "test")

		stateDir, workspace, pluginDir, promptFile, appendFile := pifMaterializeFixture(t, root)
		newClock := agenttest.NewFakeClock()
		newClient, newAdapter := pifClient(t)
		newEnv := pifNewDenyEnv(workspace, uint64(program.seed))
		cfg := pifSessionConfig(program, stateDir, pluginDir, promptFile, appendFile, newClock)

		fresh, err := NewSession(newClient, NewOpenAIProfile("gpt-5.2"), newEnv, cfg)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		pifAssertFreshSession(t, fresh, program, workspace, promptFile, appendFile, newAdapter)

		meta, err := schema.LoadSessionMeta(stateDir, fresh.ID())
		if err != nil {
			fresh.Close()
			t.Fatalf("load fresh meta: %v", err)
		}
		pifAssertPersistedMeta(t, meta, fresh.ID(), program, workspace, pluginDir, promptFile, appendFile)
		freshEvents := pifDrainEvents(fresh)
		pifAssertStartEvents(t, freshEvents, false, true)

		fresh.Close()
		pifAssertInitialTranscript(t, fresh.TranscriptPath(), fresh.ID(), workspace)

		restoreClock := agenttest.NewFakeClock()
		restoreClient, restoreAdapter := pifClient(t)
		restoreEnv := pifNewDenyEnv(workspace, uint64(program.seed)+1)
		restored, err := RestoreSessionFromMetaWithConfig(
			restoreClient,
			NewOpenAIProfile("gpt-5.2"),
			restoreEnv,
			meta,
			RestoreSessionConfig{
				StateDir:                stateDir,
				deferRestoreSideEffects: program.deferRestoreSideEffects,
				clock:                   restoreClock,
				testOnly:                pifTestConfig(),
			},
		)
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
		}
		defer restored.Close()

		pifAssertRestoredSession(t, restored, meta, program, workspace, promptFile, appendFile, restoreAdapter)
		restoredEvents := pifDrainEvents(restored)
		pifAssertStartEvents(t, restoredEvents, true, false)
	})
}

var pifStrategies = []string{
	"",
	"compact",
	"recall",
	"session-log",
	"ooda",
	"obs-mask",
	"checkpoint-pred",
	"memory-crystals",
	"recursive-distill",
}

type pifProgram struct {
	strategy                string
	startKind               plugin.SessionStartKind
	resultToolName          string
	nonInteractive          bool
	pluginAgent             bool
	deferRestoreSideEffects bool
	seed                    byte
}

type pifReader struct {
	data []byte
	pos  int
}

func (r *pifReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	v := r.data[r.pos]
	r.pos++
	return v
}

func (r *pifReader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next()) % n
}

func (r *pifReader) bool() bool { return r.next()%2 == 1 }

func decodePIFProgram(data []byte) pifProgram {
	r := &pifReader{data: data}
	startKinds := []plugin.SessionStartKind{
		"",
		plugin.SessionStartKindStartup,
		plugin.SessionStartKindClear,
		plugin.SessionStartKindCompact,
	}
	resultTools := []string{"", "pif_result"}
	return pifProgram{
		strategy:                pifStrategies[r.intn(len(pifStrategies))],
		startKind:               startKinds[r.intn(len(startKinds))],
		resultToolName:          resultTools[r.intn(len(resultTools))],
		nonInteractive:          r.bool(),
		pluginAgent:             r.bool(),
		deferRestoreSideEffects: r.bool(),
		seed:                    r.next(),
	}
}

// pifDenyEnv preserves DenyEnv's deterministic file-tool behavior while
// denying every command request. This makes the incidental Git-root probe in
// prompt/project-doc setup return its documented no-repository result without
// consulting the host Git binary or PATH.
type pifDenyEnv struct {
	*agenttest.DenyEnv
}

var _ execenv.ExecutionEnvironment = (*pifDenyEnv)(nil)

func pifNewDenyEnv(workspace string, seed uint64) *pifDenyEnv {
	return &pifDenyEnv{DenyEnv: &agenttest.DenyEnv{WorkDir: workspace, Seed: seed}}
}

func (e *pifDenyEnv) ExecCommand(context.Context, string, int, string, map[string]string) (execenv.ExecResult, error) {
	return execenv.ExecResult{ExitCode: 127}, errors.New("pif: command execution is disabled")
}

func pifClient(t *testing.T) (*llm.Client, *agenttest.ScriptedAdapter) {
	t.Helper()
	adapter := &agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(req llm.Request) llm.Response {
			if len(req.Messages) != 1 {
				t.Fatalf("prompt hook request has %d messages, want 1", len(req.Messages))
			}
			if got := req.Messages[0].Text(); !strings.Contains(got, "PIF_HOOK") {
				t.Fatalf("unexpected scripted-provider request %q", got)
			}
			return llm.Response{Message: llm.Assistant(`{"systemMessage":"PIF_HOOK_USER","hookSpecificOutput":{"additionalContext":"PIF_HOOK_CONTEXT"}}`)}
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	return client, adapter
}

func pifTestConfig() testConfig {
	return testConfig{
		skipGitSnapshot: true,
		environmentInfo: pifEnvironmentInfo,
		noSyncJobStore:  true,
	}
}

func pifEnvironmentInfo(env execenv.ExecutionEnvironment, clk clock.Clock) schema.EnvironmentInfo {
	return schema.EnvironmentInfo{
		WorkingDir: env.WorkingDirectory(),
		Platform:   "pif",
		OSVersion:  "pif-deny-env",
		Today:      clk.Now().UTC().Format("2006-01-02"),
	}
}

func pifSessionConfig(
	program pifProgram,
	stateDir, pluginDir, promptFile, appendFile string,
	clk clock.Clock,
) SessionConfig {
	agentName := ""
	if program.pluginAgent {
		agentName = "pif-init-plugin:pif-agent"
	}
	return SessionConfig{
		StateDir:           stateDir,
		MaxSubagentDepth:   1,
		PluginDirs:         []string{pluginDir},
		SystemPromptFile:   promptFile,
		SystemPromptAppend: []string{appendFile},
		NoProjectPrompts:   true,
		ContextStrategy:    program.strategy,
		ResultToolName:     program.resultToolName,
		NonInteractive:     program.nonInteractive,
		AgentName:          agentName,
		SessionStartKind:   program.startKind,
		clock:              clk,
		testOnly:           pifTestConfig(),
	}
}

func pifMaterializeFixture(t *testing.T, root string) (stateDir, workspace, pluginDir, promptFile, appendFile string) {
	t.Helper()
	stateDir = filepath.Join(root, "state")
	workspace = filepath.Join(root, "workspace")
	pluginDir = filepath.Join(root, "plugin")
	promptFile = filepath.Join(root, "base.md")
	appendFile = filepath.Join(root, "append.md")
	for _, dir := range []string{stateDir, workspace, pluginDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir parent %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(promptFile, "PIF_BASE_PROMPT")
	write(appendFile, "PIF_APPEND_PROMPT")
	write(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"), `{"name":"pif-init-plugin","version":"1.0.0"}`)
	write(filepath.Join(pluginDir, "agents", "pif-agent.md"), "---\nname: pif-agent\ndescription: pif init role\ntools:\n  - read_file\n---\nPIF_AGENT_ROLE")
	write(filepath.Join(pluginDir, "hooks", "hooks.json"), `{
  "SessionStart": [{
    "matcher": "startup|clear|compact|resume",
    "hooks": [{"type": "prompt", "prompt": "PIF_HOOK $MESSAGE"}]
  }]
}`)
	return stateDir, workspace, pluginDir, promptFile, appendFile
}

func pifAssertFreshSession(
	t *testing.T,
	s *Session,
	program pifProgram,
	workspace, promptFile, appendFile string,
	adapter *agenttest.ScriptedAdapter,
) {
	t.Helper()
	if s.ID() == "" {
		t.Fatal("NewSession returned an empty session ID")
	}
	if got := s.State(); got != SessionIdle {
		t.Fatalf("fresh state = %q, want %q", got, SessionIdle)
	}
	if s.TranscriptPath() == "" {
		t.Fatal("persistent session has an empty transcript path")
	}
	pifAssertPromptAndTools(t, s, program, workspace, promptFile, appendFile)
	pifAssertHookDelivery(t, s, adapter, true)
}

func pifAssertRestoredSession(
	t *testing.T,
	s *Session,
	meta schema.SessionMeta,
	program pifProgram,
	workspace, promptFile, appendFile string,
	adapter *agenttest.ScriptedAdapter,
) {
	t.Helper()
	if s.ID() != meta.ID {
		t.Fatalf("restored ID = %q, want %q", s.ID(), meta.ID)
	}
	if got := s.State(); got != SessionIdle {
		t.Fatalf("restored empty-history state = %q, want %q", got, SessionIdle)
	}
	if got := s.Meta().Origin; got != "test" {
		t.Fatalf("restored origin = %q, want persisted test origin", got)
	}
	pifAssertPromptAndTools(t, s, program, workspace, promptFile, appendFile)
	// Resume hooks are always deferred until an accepted user turn. The broader
	// restore-side-effects flag changes job/watch recovery timing, but does not
	// make a resume hook run during construction.
	pifAssertHookDelivery(t, s, adapter, false)
	if s.pendingSessionStartKind == nil || *s.pendingSessionStartKind != plugin.SessionStartKindResume {
		t.Fatalf("restored session has pending SessionStart kind %#v, want resume", s.pendingSessionStartKind)
	}
}

func pifAssertPromptAndTools(t *testing.T, s *Session, program pifProgram, workspace, promptFile, appendFile string) {
	t.Helper()
	if s.envInfo.WorkingDir != workspace {
		t.Fatalf("env working dir = %q, want %q", s.envInfo.WorkingDir, workspace)
	}
	prompt := s.cachedSystemPrompt
	for _, marker := range []string{"PIF_BASE_PROMPT", "PIF_APPEND_PROMPT"} {
		if !strings.Contains(prompt, marker) {
			t.Fatalf("cached prompt missing %q:\n%s", marker, prompt)
		}
	}
	if program.pluginAgent && !strings.Contains(prompt, "PIF_AGENT_ROLE") {
		t.Fatalf("plugin role prompt missing from cached system prompt:\n%s", prompt)
	}
	if !pifHasPromptSource(s.promptSourceLog, "cli:"+promptFile) {
		t.Fatalf("prompt source log lacks system prompt override %q: %+v", promptFile, s.promptSourceLog)
	}
	if !pifHasPromptSource(s.promptSourceLog, "append:"+appendFile) {
		t.Fatalf("prompt source log lacks append %q: %+v", appendFile, s.promptSourceLog)
	}

	resultTool := s.resultToolName()
	if resultTool == "" || s.reg.Get(resultTool) == nil {
		t.Fatalf("result tool %q is not registered", resultTool)
	}
	if !pifHasToolDefinition(s.ToolDefinitions(), resultTool) {
		t.Fatalf("result tool %q is not advertised", resultTool)
	}
	wantAsk := !program.nonInteractive
	if got := s.reg.Get("ask_user") != nil; got != wantAsk {
		t.Fatalf("ask_user registered=%v, want %v for nonInteractive=%v", got, wantAsk, program.nonInteractive)
	}
}

func pifAssertHookDelivery(t *testing.T, s *Session, adapter *agenttest.ScriptedAdapter, wantDelivered bool) {
	t.Helper()
	requests := adapter.Requests()
	if wantDelivered {
		if len(requests) != 1 {
			t.Fatalf("prompt hook requests = %d, want 1", len(requests))
		}
		found := false
		for _, entry := range s.SteeringQueueSnapshot() {
			if strings.Contains(entry.Text, "PIF_HOOK_CONTEXT") {
				found = true
			}
		}
		if !found {
			t.Fatalf("prompt-hook model context was not queued: %+v", s.SteeringQueueSnapshot())
		}
		return
	}
	if len(requests) != 0 {
		t.Fatalf("deferred resume executed %d prompt hooks", len(requests))
	}
	for _, entry := range s.SteeringQueueSnapshot() {
		if strings.Contains(entry.Text, "PIF_HOOK_CONTEXT") {
			t.Fatalf("deferred resume queued prompt-hook context: %+v", s.SteeringQueueSnapshot())
		}
	}
}

func pifAssertPersistedMeta(
	t *testing.T,
	meta schema.SessionMeta,
	id string,
	program pifProgram,
	workspace, pluginDir, promptFile, appendFile string,
) {
	t.Helper()
	if meta.ID != id {
		t.Fatalf("persisted ID = %q, want %q", meta.ID, id)
	}
	if meta.Origin != "test" {
		t.Fatalf("persisted origin = %q, want test", meta.Origin)
	}
	if meta.EnvInfo.WorkingDir != workspace {
		t.Fatalf("persisted working dir = %q, want %q", meta.EnvInfo.WorkingDir, workspace)
	}
	if meta.Config.ContextStrategy != program.strategy {
		t.Fatalf("persisted ContextStrategy = %q, want %q", meta.Config.ContextStrategy, program.strategy)
	}
	if meta.Config.ResultToolName != program.resultToolName {
		t.Fatalf("persisted ResultToolName = %q, want %q", meta.Config.ResultToolName, program.resultToolName)
	}
	if meta.Config.NonInteractive != program.nonInteractive {
		t.Fatalf("persisted NonInteractive = %v, want %v", meta.Config.NonInteractive, program.nonInteractive)
	}
	if meta.Config.SystemPromptFile != promptFile {
		t.Fatalf("persisted SystemPromptFile = %q, want %q", meta.Config.SystemPromptFile, promptFile)
	}
	if len(meta.Config.SystemPromptAppend) != 1 || meta.Config.SystemPromptAppend[0] != appendFile {
		t.Fatalf("persisted SystemPromptAppend = %v, want [%q]", meta.Config.SystemPromptAppend, appendFile)
	}
	if len(meta.Config.PluginDirs) != 1 || meta.Config.PluginDirs[0] != pluginDir {
		t.Fatalf("persisted PluginDirs = %v, want [%q]", meta.Config.PluginDirs, pluginDir)
	}
}

func pifAssertInitialTranscript(t *testing.T, path, id, workspace string) {
	t.Helper()
	header, entries, skipped, err := readTranscript(path)
	if err != nil {
		t.Fatalf("read initial transcript: %v", err)
	}
	if header.SessionID != id {
		t.Fatalf("transcript SessionID = %q, want %q", header.SessionID, id)
	}
	if header.WorkingDir != workspace {
		t.Fatalf("transcript WorkingDir = %q, want %q", header.WorkingDir, workspace)
	}
	if !strings.Contains(header.SystemPrompt, "PIF_BASE_PROMPT") || !strings.Contains(header.SystemPrompt, "PIF_APPEND_PROMPT") {
		t.Fatalf("transcript header lost composed prompt: %q", header.SystemPrompt)
	}
	// The fixture's SessionStart hook (pifMaterializeFixture) always fires on a
	// fresh session, and qm9y made a completed hook's exit unconditionally
	// persist as a TurnHookCompleted entry — so "clean" means exactly that one
	// entry now, not zero.
	if len(entries) != 1 || skipped != 0 {
		t.Fatalf("initial transcript entries=%d skipped=%d, want exactly one hook-completed entry", len(entries), skipped)
	}
	if entries[0].Turn.Kind != schema.TurnHookCompleted {
		t.Fatalf("initial transcript entry kind = %q, want %q", entries[0].Turn.Kind, schema.TurnHookCompleted)
	}
}

func pifDrainEvents(s *Session) []events.SessionEvent {
	var out []events.SessionEvent
	for {
		select {
		case event, ok := <-s.Events():
			if !ok {
				return out
			}
			out = append(out, event)
		default:
			return out
		}
	}
}

func pifAssertStartEvents(t *testing.T, eventsSeen []events.SessionEvent, restored, hookRan bool) {
	t.Helper()
	seen := map[events.EventKind]bool{}
	var start *events.SessionStartData
	for _, event := range eventsSeen {
		seen[event.Kind] = true
		if data, ok := event.Data.(events.SessionStartData); ok {
			copy := data
			start = &copy
		}
	}
	for _, kind := range []events.EventKind{events.EventSessionStart, events.EventPluginLoaded, events.EventPromptLoaded} {
		if !seen[kind] {
			t.Fatalf("missing %s in startup events: %v", kind, pifEventKinds(eventsSeen))
		}
	}
	if start == nil || start.Restored != restored {
		t.Fatalf("SessionStart restored=%v, want %v; events=%v", start, restored, pifEventKinds(eventsSeen))
	}
	if hookRan {
		for _, kind := range []events.EventKind{events.EventHookStart, events.EventHookEnd} {
			if !seen[kind] {
				t.Fatalf("missing %s for prompt hook: %v", kind, pifEventKinds(eventsSeen))
			}
		}
	} else if seen[events.EventHookStart] || seen[events.EventHookEnd] {
		t.Fatalf("deferred restore emitted hook events: %v", pifEventKinds(eventsSeen))
	}
}

func pifHasPromptSource(sources []promptSource, want string) bool {
	for _, source := range sources {
		if source.Label == want {
			return true
		}
	}
	return false
}

func pifHasToolDefinition(definitions []llm.ToolDefinition, want string) bool {
	for _, definition := range definitions {
		if definition.Name == want {
			return true
		}
	}
	return false
}

func pifEventKinds(eventsSeen []events.SessionEvent) []string {
	kinds := make([]string, len(eventsSeen))
	for i, event := range eventsSeen {
		kinds[i] = fmt.Sprintf("%s", event.Kind)
	}
	return kinds
}
