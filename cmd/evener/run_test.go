package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/llm"
)

// A selection that cannot be honoured stops the launch before it seeds
// marketplaces. Ensuring the config dirs runs first and is not that work: it
// carries the legacy-data guard, which has to see the config root before
// anything — plugin resolution included — creates it.
func TestRunPluginSelectionValidationPrecedesMarketplaceSeeding(t *testing.T) {
	selected := []string{"missing-plugin"}
	var order []string
	oldResolve := runResolvePlugins
	oldEnsure := runEnsureUserConfigDirs
	oldSeed := runSeedMarketplaces
	t.Cleanup(func() {
		runResolvePlugins = oldResolve
		runEnsureUserConfigDirs = oldEnsure
		runSeedMarketplaces = oldSeed
	})
	runResolvePlugins = func(context.Context, []string, *[]string) (plugins.LaunchPluginResolution, error) {
		order = append(order, "resolve")
		return plugins.LaunchPluginResolution{SelectionErrors: []plugins.PluginSelectionError{{Name: "missing-plugin", Reason: "no valid plugin candidate"}}}, nil
	}
	runEnsureUserConfigDirs = func() error {
		order = append(order, "ensure-config")
		return nil
	}
	runSeedMarketplaces = func(context.Context) error {
		order = append(order, "seed-marketplaces")
		return nil
	}

	err := run(context.Background(), runConfig{
		workDir: t.TempDir(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, enabledPlugins: &selected,
	})
	if err == nil || !strings.Contains(err.Error(), "enabled plugin selection is unavailable") {
		t.Fatalf("run error = %v, want strict selection error", err)
	}
	if !reflect.DeepEqual(order, []string{"ensure-config", "resolve"}) {
		t.Fatalf("startup order = %v, want the config-dir guard and then the resolver", order)
	}
}

func TestRunPassesResolvedPluginDirsToSessionConfig(t *testing.T) {
	installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
	selectedDir := t.TempDir()
	selected := []string{"alpha"}
	oldResolve := runResolvePlugins
	oldProvision := runProvisionSandbox
	t.Cleanup(func() { runResolvePlugins = oldResolve; runProvisionSandbox = oldProvision })
	runResolvePlugins = func(context.Context, []string, *[]string) (plugins.LaunchPluginResolution, error) {
		return plugins.LaunchPluginResolution{SelectedDirs: []string{selectedDir}}, nil
	}
	var got []string
	runProvisionSandbox = func(_ *execenv.LocalExecutionEnvironment, cfg *agent.SessionConfig, _ string) error {
		got = append([]string(nil), cfg.PluginDirs...)
		return errors.New("stop after config")
	}
	err := run(context.Background(), runConfig{
		prompt: "prompt", model: "openai/gpt-test", workDir: t.TempDir(), stateDir: t.TempDir(),
		noDefaultMarketplaces: true, enabledPlugins: &selected, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "stop after config") {
		t.Fatalf("run error = %v", err)
	}
	if !reflect.DeepEqual(got, []string{selectedDir}) {
		t.Fatalf("session plugin dirs = %v, want %v", got, []string{selectedDir})
	}
}

func TestRunResumeRestoresRecordedPluginDirs(t *testing.T) {
	installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
	oldResolve := runResolvePlugins
	oldRestore := runRestoreSession
	oldEnsure := runEnsureUserConfigDirs
	oldAttach := runAttachAPILogger
	t.Cleanup(func() {
		runResolvePlugins = oldResolve
		runRestoreSession = oldRestore
		runEnsureUserConfigDirs = oldEnsure
		runAttachAPILogger = oldAttach
	})
	runEnsureUserConfigDirs = func() error { return nil }
	fresh := []string{"/plugins/fresh"}
	runResolvePlugins = func(context.Context, []string, *[]string) (plugins.LaunchPluginResolution, error) {
		return plugins.LaunchPluginResolution{SelectedDirs: fresh}, nil
	}
	var got []string
	runRestoreSession = func(_ *llm.Client, _ *provider.Profile, _ execenv.ExecutionEnvironment, meta schema.SessionMeta, _ agent.RestoreSessionConfig) (*agent.Session, error) {
		got = append([]string(nil), meta.Config.PluginDirs...)
		return nil, errors.New("stop after restore config")
	}
	runAttachAPILogger = func(*llm.Client, string, io.Writer) (func(string) error, func() error, error) {
		return func(string) error { return nil }, func() error { return nil }, nil
	}

	for _, tc := range []struct {
		name string
		set  func(*runConfig, string)
	}{
		{name: "resume", set: func(cfg *runConfig, id string) { cfg.resume = id }},
		{name: "resume-last", set: func(cfg *runConfig, _ string) { cfg.resumeLast = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
			old := []string{"/plugins/historical"}
			if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
				ID: sessionID, ProfileID: "openai", Model: "gpt-test",
				Config: schema.ConfigSnapshot{PluginDirs: old},
			}); err != nil {
				t.Fatalf("SaveSessionMeta: %v", err)
			}
			cfg := runConfig{workDir: stateDir, stateDir: stateDir, noDefaultMarketplaces: true, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
			tc.set(&cfg, sessionID)
			err := run(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), "stop after restore config") {
				t.Fatalf("run error = %v", err)
			}
			if !reflect.DeepEqual(got, old) {
				t.Fatalf("restored PluginDirs = %v, want persisted %v", got, old)
			}
		})
	}
}

func TestRunResumeRejectsEnabledPluginSelection(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  runConfig
		want string
	}{
		{name: "resume", cfg: runConfig{resume: "session"}, want: "--enabled-plugins cannot be used with --resume"},
		{name: "resume-last", cfg: runConfig{resumeLast: true}, want: "--enabled-plugins cannot be used with --resume-last"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected := []string{"new-plugin"}
			tc.cfg.enabledPlugins = &selected
			err := run(context.Background(), tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run error = %v, want selection rejection", err)
			}
		})
	}
}

func TestRunResumeWithCreatesFreshPluginSnapshot(t *testing.T) {
	installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
	oldResolve := runResolvePlugins
	oldRestore := runRestoreSession
	oldEnsure := runEnsureUserConfigDirs
	oldAttach := runAttachAPILogger
	t.Cleanup(func() {
		runResolvePlugins = oldResolve
		runRestoreSession = oldRestore
		runEnsureUserConfigDirs = oldEnsure
		runAttachAPILogger = oldAttach
	})
	runEnsureUserConfigDirs = func() error { return nil }
	fresh := []string{"/plugins/fresh-alpha", "/plugins/fresh-beta"}
	runResolvePlugins = func(context.Context, []string, *[]string) (plugins.LaunchPluginResolution, error) {
		return plugins.LaunchPluginResolution{SelectedDirs: fresh}, nil
	}
	var restored schema.SessionMeta
	var persisted schema.SessionMeta
	stateDir := t.TempDir()
	var reserved string
	runAttachAPILogger = func(*llm.Client, string, io.Writer) (func(string) error, func() error, error) {
		return func(id string) error { reserved = id; return nil }, func() error { return nil }, nil
	}
	runRestoreSession = func(_ *llm.Client, _ *provider.Profile, _ execenv.ExecutionEnvironment, meta schema.SessionMeta, _ agent.RestoreSessionConfig) (*agent.Session, error) {
		restored = meta
		var err error
		persisted, err = schema.LoadSessionMeta(stateDir, meta.ID)
		if err != nil {
			return nil, fmt.Errorf("reload child metadata: %w", err)
		}
		return nil, errors.New("stop after resume-with restore")
	}

	const sourceID = "02wMz5Txv1C3Hut0M8GCeB"
	old := []string{"/plugins/historical"}
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sourceID, ProfileID: "openai", Model: "gpt-test",
		Config: schema.ConfigSnapshot{PluginDirs: old},
	}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	transcriptPath := filepath.Join(stateDir, "sessions", sourceID+".transcript.jsonl")
	writer, err := transcript.NewWriter(transcriptPath, transcript.Header{SessionID: sourceID, ProfileID: "openai", Model: "gpt-test", WorkingDir: stateDir})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := writer.Append(schema.NewTurn(schema.TurnUserInput, llm.User("source context"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	err = run(context.Background(), runConfig{
		resumeWith: sourceID, prompt: "new prompt", workDir: stateDir, stateDir: stateDir,
		noDefaultMarketplaces: true, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "stop after resume-with restore") {
		t.Fatalf("run error = %v", err)
	}
	if restored.ID == "" || restored.ID == sourceID || restored.ParentSessionID != sourceID {
		t.Fatalf("resume-with meta identity = %#v", restored)
	}
	if !reflect.DeepEqual(restored.Config.PluginDirs, fresh) {
		t.Fatalf("resume-with PluginDirs = %v, want %v", restored.Config.PluginDirs, fresh)
	}
	if !reflect.DeepEqual(persisted.Config.PluginDirs, fresh) {
		t.Fatalf("persisted resume-with PluginDirs = %v, want %v", persisted.Config.PluginDirs, fresh)
	}
	if reserved != restored.ID {
		t.Fatalf("reserved session = %q, want new session %q", reserved, restored.ID)
	}
	source, err := schema.LoadSessionMeta(stateDir, sourceID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(source): %v", err)
	}
	if !reflect.DeepEqual(source.Config.PluginDirs, old) {
		t.Fatalf("source PluginDirs = %v, want unchanged %v", source.Config.PluginDirs, old)
	}
	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".api.jsonl", ".log.jsonl"} {
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", restored.ID+suffix)); !os.IsNotExist(err) {
			t.Fatalf("failed resume-with child artifact %q remains: %v", suffix, err)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", restored.ID)); !os.IsNotExist(err) {
		t.Fatalf("failed resume-with child jobs directory remains: %v", err)
	}
}

func TestRunResumeWithNonLockReservationFailureRemovesChild(t *testing.T) {
	installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
	oldAttach := runAttachAPILogger
	oldEnsure := runEnsureUserConfigDirs
	oldResolve := runResolvePlugins
	oldRestore := runRestoreSession
	t.Cleanup(func() {
		runAttachAPILogger = oldAttach
		runEnsureUserConfigDirs = oldEnsure
		runResolvePlugins = oldResolve
		runRestoreSession = oldRestore
	})
	runEnsureUserConfigDirs = func() error { return nil }
	runResolvePlugins = func(context.Context, []string, *[]string) (plugins.LaunchPluginResolution, error) {
		return plugins.LaunchPluginResolution{}, nil
	}
	runRestoreSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, schema.SessionMeta, agent.RestoreSessionConfig) (*agent.Session, error) {
		t.Fatal("restore called after reservation failure")
		return nil, nil
	}

	stateDir := t.TempDir()
	reservationErr := errors.New("quarantine API log target")
	var childID string
	runAttachAPILogger = func(*llm.Client, string, io.Writer) (func(string) error, func() error, error) {
		return func(id string) error {
			childID = id
			return reservationErr
		}, func() error { return nil }, nil
	}

	const sourceID = "02wMz5Txv1C3Hut0M8GCeD"
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sourceID, ProfileID: "openai", Model: "gpt-test",
	}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	writer, err := transcript.NewWriter(
		filepath.Join(stateDir, "sessions", sourceID+".transcript.jsonl"),
		transcript.Header{SessionID: sourceID, ProfileID: "openai", Model: "gpt-test", WorkingDir: stateDir},
	)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	err = run(context.Background(), runConfig{
		resumeWith: sourceID, prompt: "new prompt", workDir: stateDir, stateDir: stateDir,
		noDefaultMarketplaces: true, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
	})
	if !errors.Is(err, reservationErr) {
		t.Fatalf("run error = %v, want %v", err, reservationErr)
	}
	if childID == "" {
		t.Fatal("resume-with child was never reserved")
	}
	for _, suffix := range []string{".meta.json", ".transcript.jsonl", ".api.jsonl", ".log.jsonl"} {
		if _, err := os.Stat(filepath.Join(stateDir, "sessions", childID+suffix)); !os.IsNotExist(err) {
			t.Fatalf("failed resume-with child artifact %q remains: %v", suffix, err)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", childID)); !os.IsNotExist(err) {
		t.Fatalf("failed resume-with child jobs directory remains: %v", err)
	}
}

// TestRunWithArgs verifies that the run function processes a prompt from CLI args
// and produces output on stdout.
func TestRunWithArgs(t *testing.T) {
	installRunScriptedProvider(t, &scriptedProvider{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return scriptedCommunicate("PONG") },
		},
	})

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt:  "Reply with exactly the word PONG and nothing else.",
		model:   "openai/gpt-test",
		workDir: t.TempDir(),
		stdout:  &stdout,
		stderr:  &stderr,
	})
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(strings.ToUpper(stdout.String()), "PONG") {
		t.Fatalf("expected stdout to contain PONG, got: %q", stdout.String())
	}
}

// TestRunEmitsToolEvents verifies that tool call events are written to stderr
// when the model uses tools.
func TestRunEmitsToolEvents(t *testing.T) {
	tmpDir := t.TempDir()
	installRunScriptedProvider(t, &scriptedProvider{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return scriptedToolCalls(scriptedWriteFileCall("write_1", "test.txt", "hello"))
			},
			func(llm.Request) llm.Response { return scriptedCommunicate("created test.txt") },
		},
	})

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt:  "Create a file called test.txt in " + tmpDir + " with content 'hello'. Use the write_file tool.",
		model:   "openai/gpt-test",
		workDir: tmpDir,
		stdout:  &stdout,
		stderr:  &stderr,
	})
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}

	// stderr should contain tool call info.
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "write_file") {
		t.Fatalf("expected stderr to mention write_file tool call, got: %q", stderrStr)
	}

	// File should exist.
	content, err := os.ReadFile(tmpDir + "/test.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(content), "hello") {
		t.Fatalf("expected file to contain 'hello', got: %q", string(content))
	}
}

// TestRunMissingPrompt verifies that run returns an error when no prompt is provided.
func TestRunMissingPrompt(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt: "",
		model:  "openai/gpt-5.4-mini",
		stdout: &stdout,
		stderr: &stderr,
	})
	if err == nil {
		t.Fatal("expected error for empty prompt")
	}
	if !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("expected error to mention 'prompt', got: %v", err)
	}
}

// TestRunMissingAPIKey verifies that run returns an error when no API keys
// are available.
func TestRunMissingAPIKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	for _, k := range []string{"ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		t.Setenv(k, "")
	}

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt: "do something",
		model:  "openai/gpt-5.4-mini",
		stdout: &stdout,
		stderr: &stderr,
	})
	if err == nil {
		t.Fatal("expected error when no API keys configured")
	}
}

func TestRunBareModelRejected(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt: "do something",
		model:  "gpt-5.2",
		stdout: &stdout,
		stderr: &stderr,
	})
	if err == nil {
		t.Fatal("expected error when model is not provider-qualified")
	}
	if !strings.Contains(err.Error(), "provider/model") {
		t.Fatalf("expected provider/model guidance, got: %v", err)
	}
}

// TestRunInvalidOutputSchema verifies that run returns an error when
// --output-schema contains malformed JSON. This is the black-box wire-through
// test — it confirms cfg.outputSchema reaches buildInitialProfile.
func TestRunInvalidOutputSchema(t *testing.T) {
	installRunScriptedProvider(t, &scriptedProvider{name: "openai"})

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt:       "do something",
		model:        "openai/gpt-5.2",
		outputSchema: "{not json",
		workDir:      t.TempDir(),
		stateDir:     t.TempDir(),
		stdout:       &stdout,
		stderr:       &stderr,
	})
	if err == nil {
		t.Fatal("expected error for invalid --output-schema JSON")
	}
	if !strings.Contains(err.Error(), "invalid --output-schema") {
		t.Fatalf("error %q, want to contain 'invalid --output-schema'", err.Error())
	}
}

func TestRunFastCheapModelValidation(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	t.Setenv("OPENAI_API_KEY", "dummy-for-wire-test")
	t.Setenv("ANTHROPIC_API_KEY", "")

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt:         "do something",
		model:          "openai/gpt-5.2",
		fastCheapModel: "anthropic/claude-haiku-4-5-20251001",
		workDir:        t.TempDir(),
		stateDir:       t.TempDir(),
		stdout:         &stdout,
		stderr:         &stderr,
	})
	if err == nil {
		t.Fatal("expected unavailable --fast-cheap-model provider error")
	}
	if !strings.Contains(err.Error(), "--fast-cheap-model provider") {
		t.Fatalf("error %q, want --fast-cheap-model provider guidance", err.Error())
	}
}

// TestRunMissingModel verifies that run returns an error when no --model is
// provided and EVENER_MODEL is unset.
func TestRunMissingModel(t *testing.T) {
	t.Setenv("EVENER_MODEL", "")

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt: "do something",
		model:  "",
		stdout: &stdout,
		stderr: &stderr,
	})
	if err == nil {
		t.Fatal("expected error when no model specified")
	}
	if !strings.Contains(err.Error(), "model") {
		t.Fatalf("expected error to mention 'model', got: %v", err)
	}
}

// --- Session resume tests ---

func TestListSessions_PrintsFormattedList(t *testing.T) {
	dir := t.TempDir()

	meta1 := schema.SessionMeta{
		ID:        "02wMz5Txv1C3Hut0M8GCeB",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		CreatedAt: time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 15, 10, 5, 0, 0, time.UTC),
		TurnCount: 2,
	}
	meta2 := schema.SessionMeta{
		ID:        "02wMz5Txv2enqVTitaig6F",
		ProfileID: "anthropic",
		Model:     "claude-opus-4-6",
		CreatedAt: time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
		TurnCount: 1,
	}
	for _, m := range []schema.SessionMeta{meta1, meta2} {
		if err := schema.SaveSessionMeta(dir, m); err != nil {
			t.Fatalf("SaveSessionMeta: %v", err)
		}
	}

	var out bytes.Buffer
	cfg := runConfig{
		listSessions: true,
		workDir:      dir,
		stateDir:     dir,
		stdout:       &out,
		stderr:       &bytes.Buffer{},
	}
	err := run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	output := out.String()
	// Most recent first (snap2).
	if !strings.Contains(output, meta2.ID) {
		t.Fatalf("expected snap2 ID in output, got:\n%s", output)
	}
	if !strings.Contains(output, meta1.ID) {
		t.Fatalf("expected snap1 ID in output, got:\n%s", output)
	}
}

func TestListSessions_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	cfg := runConfig{
		listSessions: true,
		workDir:      dir,
		stateDir:     dir,
		stdout:       &out,
		stderr:       &bytes.Buffer{},
	}
	err := run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "No saved sessions") {
		t.Fatalf("expected 'No saved sessions' message, got: %q", out.String())
	}
}

func TestResume_NonexistentID(t *testing.T) {
	dir := t.TempDir()
	cfg := runConfig{
		resume:   "NONEXISTENT",
		workDir:  dir,
		stateDir: dir,
		stdout:   &bytes.Buffer{},
		stderr:   &bytes.Buffer{},
	}
	err := run(context.Background(), cfg)
	if err == nil {
		t.Fatalf("expected error for nonexistent session")
	}
	if !strings.Contains(err.Error(), "NONEXISTENT") {
		t.Fatalf("expected error to mention session ID, got: %v", err)
	}
}

func TestResumeLast_NoSessions(t *testing.T) {
	dir := t.TempDir()
	cfg := runConfig{
		resumeLast: true,
		workDir:    dir,
		stateDir:   dir,
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
	}
	err := run(context.Background(), cfg)
	if err == nil {
		t.Fatalf("expected error when no sessions exist")
	}
	if !strings.Contains(err.Error(), "no saved sessions") {
		t.Fatalf("expected error about no sessions, got: %v", err)
	}
}

func TestRunResumeRunningReservesBeforeRestore(t *testing.T) {
	adapter := &scriptedProvider{name: "openai"}
	installRunScriptedProvider(t, adapter)
	oldRestore := runRestoreSession
	t.Cleanup(func() { runRestoreSession = oldRestore })

	tests := []struct {
		name      string
		configure func(*runConfig, string)
	}{
		{name: "resume", configure: func(cfg *runConfig, id string) { cfg.resume = id }},
		{name: "resume-last", configure: func(cfg *runConfig, _ string) { cfg.resumeLast = true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stateDir := t.TempDir()
			const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
			meta := schema.SessionMeta{
				ID: sessionID, ProfileID: "openai", Model: "gpt-test",
				CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(),
			}
			if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
				t.Fatalf("SaveSessionMeta: %v", err)
			}
			sessionsDir := filepath.Join(stateDir, "sessions")
			transcriptPath := filepath.Join(sessionsDir, sessionID+".transcript.jsonl")
			if err := os.WriteFile(transcriptPath, []byte("transcript sentinel\n"), 0o600); err != nil {
				t.Fatalf("write transcript sentinel: %v", err)
			}
			jobsDir := filepath.Join(sessionsDir, sessionID)
			if err := os.Mkdir(jobsDir, 0o700); err != nil {
				t.Fatalf("mkdir jobs: %v", err)
			}
			jobsPath := filepath.Join(jobsDir, "jobs.jsonl")
			if err := os.WriteFile(jobsPath, []byte("jobs sentinel\n"), 0o600); err != nil {
				t.Fatalf("write jobs sentinel: %v", err)
			}
			apiPath := filepath.Join(sessionsDir, sessionID+".api.jsonl")
			owner, err := llm.NewAPILogger(apiPath)
			if err != nil {
				t.Fatalf("NewAPILogger owner: %v", err)
			}
			before := readResumeArtifacts(t, stateDir, sessionID, transcriptPath, jobsPath, apiPath)

			restoreCalled := false
			runRestoreSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, schema.SessionMeta, agent.RestoreSessionConfig) (*agent.Session, error) {
				restoreCalled = true
				return nil, errors.New("restore reached")
			}
			cfg := runConfig{
				workDir: stateDir, stateDir: stateDir, noDefaultMarketplaces: true,
				stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
			}
			tt.configure(&cfg, sessionID)
			runErr := run(context.Background(), cfg)
			if closeErr := owner.Close(); closeErr != nil {
				t.Fatalf("owner Close: %v", closeErr)
			}
			if runErr == nil || !strings.Contains(runErr.Error(), "already running") || !strings.Contains(runErr.Error(), "send work") || !strings.Contains(runErr.Error(), "fork") {
				t.Fatalf("run error = %v, want live-session or fork guidance", runErr)
			}
			if restoreCalled {
				t.Fatal("RestoreSessionFromMetaWithConfig was called before API-log ownership")
			}
			if got := len(adapter.Requests()); got != 0 {
				t.Fatalf("provider requests = %d, want 0", got)
			}
			after := readResumeArtifacts(t, stateDir, sessionID, transcriptPath, jobsPath, apiPath)
			if !equalResumeArtifacts(before, after) {
				t.Fatalf("resume lock conflict mutated session artifacts:\n before=%q\n  after=%q", before, after)
			}
		})
	}
}

func TestRunResumePassesTimeoutLifetimeToRestore(t *testing.T) {
	adapter := &scriptedProvider{name: "openai"}
	installRunScriptedProvider(t, adapter)
	oldRestore := runRestoreSession
	t.Cleanup(func() { runRestoreSession = oldRestore })

	stateDir := t.TempDir()
	meta := schema.SessionMeta{
		ID:        "02wMz5Txv1C3Hut0M8GCeB",
		ProfileID: "openai",
		Model:     "gpt-test",
		CreatedAt: time.Unix(1, 0).UTC(),
		UpdatedAt: time.Unix(2, 0).UTC(),
	}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	wantErr := errors.New("restore lifetime observed")
	var restoredLifetime context.Context
	runRestoreSession = func(_ *llm.Client, _ *provider.Profile, _ execenv.ExecutionEnvironment, _ schema.SessionMeta, cfg agent.RestoreSessionConfig) (*agent.Session, error) {
		restoredLifetime = cfg.LifetimeContext
		return nil, wantErr
	}
	startedAt := time.Now()
	err := run(context.Background(), runConfig{
		resume:                meta.ID,
		workDir:               stateDir,
		stateDir:              stateDir,
		runTimeout:            time.Hour,
		noDefaultMarketplaces: true,
		stdout:                &bytes.Buffer{},
		stderr:                &bytes.Buffer{},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("run error = %v, want restore sentinel", err)
	}
	if restoredLifetime == nil {
		t.Fatal("restore did not receive the one-shot lifetime context")
	}
	deadline, ok := restoredLifetime.Deadline()
	if !ok {
		t.Fatal("restored lifetime has no --timeout deadline")
	}
	if remaining := deadline.Sub(startedAt); remaining < 59*time.Minute || remaining > 61*time.Minute {
		t.Fatalf("restored lifetime deadline = %s after start, want approximately one hour", remaining)
	}
	select {
	case <-restoredLifetime.Done():
	case <-time.After(time.Second):
		t.Fatal("restored lifetime remained live after run returned")
	}
}

func readResumeArtifacts(t *testing.T, stateDir, sessionID string, paths ...string) map[string]string {
	t.Helper()
	paths = append(paths, filepath.Join(stateDir, "sessions", sessionID+".meta.json"))
	artifacts := make(map[string]string, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read resume artifact %s: %v", path, err)
		}
		artifacts[path] = string(data)
	}
	return artifacts
}

func equalResumeArtifacts(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for path, content := range left {
		if right[path] != content {
			return false
		}
	}
	return true
}

// --- Drain event tests ---

func testEvents() []events.SessionEvent {
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	return []events.SessionEvent{
		{Kind: events.EventSessionStart, Timestamp: now, SessionID: "sess1", Data: events.SessionStartData{Model: "gpt-5.2", Profile: "openai"}},
		{Kind: events.EventAssistantTextEnd, Timestamp: now, SessionID: "sess1", Data: events.AssistantTextEndData{
			Text:         "here is my answer",
			Reasoning:    "let me think carefully",
			FinishReason: "stop",
			Model:        "gpt-5.2",
			Usage:        llm.Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150, CacheReadTokens: new(80), CacheWriteTokens: new(20)},
		}},
		{Kind: events.EventToolCallStart, Timestamp: now, SessionID: "sess1", Data: events.ToolCallStartData{
			ToolName:      "write_file",
			CallID:        "call_1",
			ArgumentsJSON: `{"file_path":"/tmp/test.txt","content":"hello world this is a longer argument string for testing truncation behavior"}`,
		}},
		{Kind: events.EventToolCallEnd, Timestamp: now, SessionID: "sess1", Data: events.ToolCallEndData{
			ToolName: "write_file",
			CallID:   "call_1",
		}},
		{Kind: events.EventWarning, Timestamp: now, SessionID: "sess1", Data: events.WarningData{Message: "context window 80% full"}},
		{Kind: events.EventError, Timestamp: now, SessionID: "sess1", Data: events.ErrorData{Error: "something went wrong"}},
	}
}

func feedEvents(evs []events.SessionEvent) <-chan events.SessionEvent {
	ch := make(chan events.SessionEvent, len(evs))
	for _, ev := range evs {
		ch <- ev
	}
	close(ch)
	return ch
}

func TestDrainEventsVerbose(t *testing.T) {
	evs := testEvents()
	ch := feedEvents(evs)
	var buf bytes.Buffer
	done := drainEventsVerbose(ch, &buf)
	<-done

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != len(evs) {
		t.Fatalf("expected %d NDJSON lines, got %d:\n%s", len(evs), len(lines), buf.String())
	}

	// Each line must be valid JSON with a kind field.
	for i, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("line %d is not valid JSON: %v\nline: %s", i, err, line)
		}
		kind, _ := obj["kind"].(string)
		if kind == "" {
			t.Fatalf("line %d missing 'kind' field: %s", i, line)
		}
	}

	// Verify first line is SESSION_START.
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first["kind"] != "SESSION_START" {
		t.Fatalf("first event kind: got %q want SESSION_START", first["kind"])
	}

	// Verify usage data is present in ASSISTANT_TEXT_END line.
	var assistantEnd map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &assistantEnd); err != nil {
		t.Fatal(err)
	}
	data, _ := assistantEnd["data"].(map[string]any)
	if data == nil {
		t.Fatalf("ASSISTANT_TEXT_END missing data field")
	}
	usage, _ := data["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("ASSISTANT_TEXT_END missing usage in data")
	}
	if usage["input_tokens"] != float64(100) {
		t.Fatalf("usage.input_tokens: got %v want 100", usage["input_tokens"])
	}
}

func TestDrainEventsHuman(t *testing.T) {
	evs := testEvents()
	ch := feedEvents(evs)
	var buf bytes.Buffer
	done := drainEventsHuman(ch, &buf)
	<-done

	output := buf.String()

	// Should contain model info from SESSION_START.
	if !strings.Contains(output, "[model]") {
		t.Fatalf("expected [model] line in output:\n%s", output)
	}
	if !strings.Contains(output, "gpt-5.2") {
		t.Fatalf("expected model name in output:\n%s", output)
	}

	// Should contain assistant message.
	if !strings.Contains(output, "[assistant]") {
		t.Fatalf("expected [assistant] line in output:\n%s", output)
	}
	if !strings.Contains(output, "here is my answer") {
		t.Fatalf("expected assistant text in output:\n%s", output)
	}

	// Should contain thinking summary.
	if !strings.Contains(output, "[thinking]") {
		t.Fatalf("expected [thinking] line in output:\n%s", output)
	}

	// Should contain tool call with args.
	if !strings.Contains(output, "[tool] write_file") {
		t.Fatalf("expected [tool] write_file in output:\n%s", output)
	}

	// Should contain usage.
	if !strings.Contains(output, "[usage]") {
		t.Fatalf("expected [usage] line in output:\n%s", output)
	}
	if !strings.Contains(output, "in=100") {
		t.Fatalf("expected 'in=100' in usage line:\n%s", output)
	}
	if !strings.Contains(output, "cache_read=80") {
		t.Fatalf("expected 'cache_read=80' in usage line:\n%s", output)
	}
	if !strings.Contains(output, "cache_write=20") {
		t.Fatalf("expected 'cache_write=20' in usage line:\n%s", output)
	}

	// Should contain warning.
	if !strings.Contains(output, "[warning]") {
		t.Fatalf("expected [warning] in output:\n%s", output)
	}

	// Should contain error.
	if !strings.Contains(output, "[error]") {
		t.Fatalf("expected [error] in output:\n%s", output)
	}
}

// TestDrainEventsHuman_CommunicateEchoesAssistantText pins the non-interactive
// printer's dedupe (kata sc17): a model that streams assistant text and then
// calls communicate with the same content produced one answer, so the user
// sees it once. Different communicate text is a second thing to say and still
// prints. The comparison is apptranscript.EchoesAssistantText, shared with the
// live projector so the two surfaces cannot drift.
func TestDrainEventsHuman_CommunicateEchoesAssistantText(t *testing.T) {
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	assistantText := func(text string) events.SessionEvent {
		return events.SessionEvent{Kind: events.EventAssistantTextEnd, Timestamp: now, SessionID: "sess1", Data: events.AssistantTextEndData{
			Text:  text,
			Usage: llm.Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150},
		}}
	}
	communicate := func(message string, endTurn bool) events.SessionEvent {
		return events.SessionEvent{Kind: events.EventCommunicate, Timestamp: now, SessionID: "sess1", Data: events.CommunicateData{
			Message: message,
			EndTurn: endTurn,
		}}
	}
	drain := func(t *testing.T, evs ...events.SessionEvent) string {
		t.Helper()
		var buf bytes.Buffer
		done := drainEventsHuman(feedEvents(evs), &buf)
		<-done
		return buf.String()
	}

	t.Run("end_turn echo prints the answer once", func(t *testing.T) {
		out := drain(t, assistantText("the answer"), communicate("the answer", true))
		if got := strings.Count(out, "the answer"); got != 1 {
			t.Fatalf("answer printed %d times, want 1:\n%s", got, out)
		}
		if !strings.Contains(out, "[assistant] the answer") {
			t.Fatalf("expected the surviving line to be the [assistant] one:\n%s", out)
		}
		// Suppressing the echo must not swallow the rest of the stream.
		if !strings.Contains(out, "[usage] in=100 out=50 total=150") {
			t.Fatalf("expected [usage] line in output:\n%s", out)
		}
	})

	t.Run("different communicate text still prints", func(t *testing.T) {
		out := drain(t, assistantText("the answer"), communicate("and one more thing", true))
		if !strings.Contains(out, "[assistant] the answer") {
			t.Fatalf("expected [assistant] line in output:\n%s", out)
		}
		if !strings.Contains(out, "[communicate:end_turn] and one more thing") {
			t.Fatalf("expected communicate line in output:\n%s", out)
		}
	})

	t.Run("blank communicate does not forget the answer", func(t *testing.T) {
		out := drain(t, assistantText("the answer"), communicate("", false), communicate("the answer", true))
		if got := strings.Count(out, "the answer"); got != 1 {
			t.Fatalf("answer printed %d times, want 1:\n%s", got, out)
		}
	})

	// The projector's dedupe ignores EndTurn, and so does this one: a mid-turn
	// communicate that repeats the streamed text is the same duplicate. Padding
	// pins the TrimSpace-equality semantics the shared comparison uses.
	t.Run("mid-turn echo is suppressed too", func(t *testing.T) {
		out := drain(t, assistantText("the answer"), communicate("  the answer\n", false))
		if got := strings.Count(out, "the answer"); got != 1 {
			t.Fatalf("answer printed %d times, want 1:\n%s", got, out)
		}
		if strings.Contains(out, "[communicate]") {
			t.Fatalf("expected no [communicate] line for the echo:\n%s", out)
		}
	})
}

// TestRunPluginDirsPassthrough verifies that pluginDirs on runConfig flows
// through to SessionConfig.PluginDirs, causing the named plugin to be loaded.
// A probe plugin directory produces a PLUGIN_LOADED event that drainEventsHuman
// writes to stderr; if run() drops pluginDirs the event never fires.
func TestRunPluginDirsPassthrough(t *testing.T) {
	// Build a minimal plugin directory that session init will scan and load.
	pluginDir := t.TempDir()
	metaDir := filepath.Join(pluginDir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"),
		[]byte(`{"name": "passthrough-probe"}`), 0644); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}

	installRunScriptedProvider(t, &scriptedProvider{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return scriptedCommunicate("PONG") },
		},
	})

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt:     "ping",
		model:      "openai/gpt-test",
		workDir:    t.TempDir(),
		pluginDirs: []string{pluginDir},
		stdout:     &stdout,
		stderr:     &stderr,
	})
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	// drainEventsHuman writes "[plugin] loaded <name>" for every PLUGIN_LOADED
	// event. This line only appears when run() correctly forwards pluginDirs to
	// the SessionConfig — deleting that field assignment makes the test fail.
	if !strings.Contains(stderr.String(), "[plugin] loaded passthrough-probe") {
		t.Fatalf("expected '[plugin] loaded passthrough-probe' in stderr; pluginDirs may not be forwarded\nstderr: %s", stderr.String())
	}
}

// TestRun_SlashCommandExpansion verifies headless `evener /name args` (design
// §10's "the server-side expander also runs for headless evener /name args"):
// a plugin command loaded via --plugin-dir is expanded before it reaches the
// model, not sent to the model as the literal "/greet the-world" text. The
// scripted step inspects the actual request it receives and only answers
// PONG when it sees the expanded body, so a regression that drops the
// interception (or a future change to processOneInput that reorders it after
// something that consumes the raw input) fails this test rather than passing
// vacuously.
func TestRun_SlashCommandExpansion(t *testing.T) {
	pluginDir := t.TempDir()
	metaDir := filepath.Join(pluginDir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(`{"name": "cmd-probe"}`), 0644); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
	commandsDir := filepath.Join(pluginDir, "commands")
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "greet.md"),
		[]byte("---\ndescription: greet someone\n---\nSay hello to $ARGUMENTS"), 0644); err != nil {
		t.Fatalf("write command: %v", err)
	}

	installRunScriptedProvider(t, &scriptedProvider{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				last := req.Messages[len(req.Messages)-1].Text()
				if !strings.Contains(last, "Say hello to the-world") {
					return scriptedCommunicate("FAIL: model saw " + last)
				}
				return scriptedCommunicate("PONG")
			},
		},
	})

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt:     "/greet the-world",
		model:      "openai/gpt-test",
		workDir:    t.TempDir(),
		pluginDirs: []string{pluginDir},
		stdout:     &stdout,
		stderr:     &stderr,
	})
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "PONG") {
		t.Fatalf("expected PONG (slash command expanded before reaching the model), got: %q\nstderr: %s", stdout.String(), stderr.String())
	}
}

func TestDrainEventsHuman_PluginEvents(t *testing.T) {
	ch := make(chan events.SessionEvent, 3)
	ch <- events.SessionEvent{Kind: events.EventPluginLoaded, Data: events.PluginLoadedData{
		Name: "test-plugin", SkillCount: 2, AgentCount: 1, MCPCount: 0,
	}}
	ch <- events.SessionEvent{Kind: events.EventHookStart, Data: events.HookStartData{
		Event: "PreToolUse", HookType: "command", Matcher: "Write",
	}}
	ch <- events.SessionEvent{Kind: events.EventHookEnd, Data: events.HookEndData{
		Event: "PreToolUse", HookType: "command", Matcher: "Write", DurationMS: 42,
	}}
	close(ch)
	var buf bytes.Buffer
	done := drainEventsHuman(ch, &buf)
	<-done
	out := buf.String()
	if !strings.Contains(out, "test-plugin") {
		t.Errorf("expected plugin name in output, got: %q", out)
	}
	if !strings.Contains(out, "2 skills") {
		t.Errorf("expected skill count in output, got: %q", out)
	}
	if !strings.Contains(out, "1 agents") {
		t.Errorf("expected agent count in output, got: %q", out)
	}
	if !strings.Contains(out, "PreToolUse") {
		t.Errorf("expected hook event name in output, got: %q", out)
	}
	if !strings.Contains(out, "Write") {
		t.Errorf("expected matcher in output, got: %q", out)
	}
	if !strings.Contains(out, "42ms") {
		t.Errorf("expected duration in output, got: %q", out)
	}
}

func TestRunWithContextStrategy_DoesNotError(t *testing.T) {
	installRunScriptedProvider(t, &scriptedProvider{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return scriptedCommunicate("PONG") },
		},
	})

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt:          "Reply with exactly the word PONG and nothing else.",
		model:           "openai/gpt-test",
		workDir:         t.TempDir(),
		contextStrategy: "compact",
		stdout:          &stdout,
		stderr:          &stderr,
	})
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(strings.ToUpper(stdout.String()), "PONG") {
		t.Fatalf("expected stdout to contain PONG, got: %q", stdout.String())
	}
}

// Nothing may create the user config root before the legacy-data guard has
// looked at it. The guard reads an existing <config>/evener as "already
// migrated", so a bundled plugin materialized into <config>/evener/plugins
// would silently strand a user's <config>/serf — configuration and
// credentials included. EnsureUserConfigDirs carries that guard, so it runs
// before any plugin resolution.
func TestLaunchChecksForLegacyDataBeforeResolvingPlugins(t *testing.T) {
	tests := []struct {
		name  string
		start func(t *testing.T) error
	}{
		{
			name: "run",
			start: func(t *testing.T) error {
				return run(context.Background(), runConfig{
					prompt: "hello", workDir: t.TempDir(), stateDir: t.TempDir(),
					enabledPlugins: &[]string{"coordinator-workflow"}, noDefaultMarketplaces: true,
					stdout: io.Discard, stderr: io.Discard,
				})
			},
		},
		{
			name: "serve",
			start: func(t *testing.T) error {
				return runServeWithDeps([]string{
					"--enabled-plugins=coordinator-workflow", "--dir", t.TempDir(), "--state-dir", t.TempDir(),
				}, defaultServeDeps())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			config := filepath.Join(home, ".config")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", config)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			if err := os.MkdirAll(filepath.Join(config, "serf"), 0o700); err != nil {
				t.Fatal(err)
			}

			err := tt.start(t)
			if err == nil || !strings.Contains(err.Error(), "legacy Serf data") {
				t.Fatalf("error = %v, want the legacy-data guard to stop the startup", err)
			}
			if _, err := os.Stat(filepath.Join(config, "evener")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the config root was created before the guard ran: stat err = %v", err)
			}
		})
	}
}

// Provisioning takes the session scratch and the flock lease under it, and
// nothing releases either until a session owns the environment and its Close
// does. A launch that ends before that hand-off owes them itself: a fresh
// session that could not be built, or a resume whose restore fails after
// re-provisioning the environment from the persisted mode.
func TestRunDisposesTheSandboxScratchWhenNoSessionTakesTheEnvironment(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(t *testing.T, cfg *runConfig)
		wantErr string
	}{
		{
			name: "a fresh session that cannot be created",
			arrange: func(t *testing.T, _ *runConfig) {
				oldProvision, oldNew := runProvisionSandbox, runNewSession
				t.Cleanup(func() { runProvisionSandbox = oldProvision; runNewSession = oldNew })
				runProvisionSandbox = func(env *execenv.LocalExecutionEnvironment, _ *agent.SessionConfig, _ string) error {
					return launchScratchThatMustBeDisposed(t, env)
				}
				runNewSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, agent.SessionConfig) (*agent.Session, error) {
					return nil, errors.New("no session today")
				}
			},
			wantErr: "session creation",
		},
		{
			name: "a resume whose restore fails",
			arrange: func(t *testing.T, cfg *runConfig) {
				const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
				if err := schema.SaveSessionMeta(cfg.stateDir, schema.SessionMeta{
					ID: sessionID, ProfileID: "openai", Model: "gpt-test",
				}); err != nil {
					t.Fatalf("SaveSessionMeta: %v", err)
				}
				cfg.resume = sessionID
				oldRestore := runRestoreSession
				t.Cleanup(func() { runRestoreSession = oldRestore })
				runRestoreSession = func(_ *llm.Client, _ *provider.Profile, env execenv.ExecutionEnvironment, _ schema.SessionMeta, _ agent.RestoreSessionConfig) (*agent.Session, error) {
					// What RestoreSessionFromMetaWithConfig does before the
					// steps that can still fail: it re-provisions this env's
					// sandbox from the session's persisted mode.
					local, ok := env.(*execenv.LocalExecutionEnvironment)
					if !ok {
						t.Fatalf("restore got a %T, want the local environment run built", env)
					}
					if err := launchScratchThatMustBeDisposed(t, local); err != nil {
						return nil, err
					}
					return nil, errors.New("restore failed after the sandbox was provisioned")
				}
			},
			wantErr: "restore session",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installRunScriptedProvider(t, &scriptedProvider{name: "openai"})
			oldEnsure := runEnsureUserConfigDirs
			t.Cleanup(func() { runEnsureUserConfigDirs = oldEnsure })
			runEnsureUserConfigDirs = func() error { return nil }
			dir := t.TempDir()
			cfg := runConfig{
				prompt: "hello", model: "openai/gpt-test", workDir: dir, stateDir: dir,
				noDefaultMarketplaces: true, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
			}
			tt.arrange(t, &cfg)

			err := run(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("run error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// A launch the caller has already given up on stops at the resolver, whatever
// it selected. The inventory failing is fail-soft when nothing had to be
// honoured — a launch still runs with whatever could be listed — but a
// cancellation is not that kind of failure: everything after it, seeding
// marketplaces first of all, takes the plugin store lock and writes config for
// nobody.
func TestLaunchRefusesACancelledLaunchWithNoPluginSelection(t *testing.T) {
	tests := []struct {
		name  string
		start func(t *testing.T, seeded *bool) error
	}{
		{
			name: "run",
			start: func(t *testing.T, seeded *bool) error {
				oldResolve, oldSeed, oldEnsure := runResolvePlugins, runSeedMarketplaces, runEnsureUserConfigDirs
				t.Cleanup(func() {
					runResolvePlugins, runSeedMarketplaces, runEnsureUserConfigDirs = oldResolve, oldSeed, oldEnsure
				})
				runEnsureUserConfigDirs = func() error { return nil }
				runResolvePlugins = func(ctx context.Context, _ []string, _ *[]string) (plugins.LaunchPluginResolution, error) {
					return plugins.LaunchPluginResolution{}, ctx.Err()
				}
				runSeedMarketplaces = func(context.Context) error { *seeded = true; return nil }
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return run(ctx, runConfig{
					prompt: "hello", model: "openai/gpt-test", workDir: t.TempDir(), stateDir: t.TempDir(),
					stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
				})
			},
		},
		{
			name: "serve",
			start: func(t *testing.T, seeded *bool) error {
				deps := defaultServeDeps()
				deps.ensureConfigDirs = func() error { return nil }
				deps.notifyContext = func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
					next, stop := context.WithCancel(ctx)
					stop()
					return next, stop
				}
				deps.resolvePlugins = func(ctx context.Context, _ []string, _ *[]string) (plugins.LaunchPluginResolution, error) {
					return plugins.LaunchPluginResolution{}, ctx.Err()
				}
				deps.seedMarketplaces = func(context.Context) error { *seeded = true; return nil }
				return runServeWithDeps([]string{
					"--model", "openai/gpt-test", "--dir", t.TempDir(), "--state-dir", t.TempDir(),
				}, deps)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			seeded := false
			err := test.start(t, &seeded)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want the cancellation that ended the launch", err)
			}
			if seeded {
				t.Error("seeded marketplaces for a launch that had already been given up on")
			}
		})
	}
}
