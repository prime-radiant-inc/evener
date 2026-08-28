package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/llm"
)

func writeSkillBodyFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: simplify\ndescription: test\n---\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func drainSlashEvents(s *Session) []events.SessionEvent {
	var got []events.SessionEvent
	for {
		select {
		case ev := <-s.Events():
			got = append(got, ev)
		default:
			return got
		}
	}
}

func TestExpandSlashCommandStandaloneSkillResolution(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		skills     map[string]skill.SkillMeta
		want       string
		wantOK     bool
		wantActive string
	}{
		{
			name:  "body and context",
			input: "/simplify this diff",
			skills: map[string]skill.SkillMeta{
				"simplify": {Name: "simplify", SkillFile: writeSkillBodyFile(t, "follow these steps")},
			},
			want:       "follow these steps\n\nUser context:\nthis diff",
			wantOK:     true,
			wantActive: "simplify",
		},
		{
			name:  "plugin qualified",
			input: "/plugin:simplify",
			skills: map[string]skill.SkillMeta{
				"plugin:simplify": {Name: "simplify", SkillFile: writeSkillBodyFile(t, "plugin steps")},
			},
			want:       "plugin steps",
			wantOK:     true,
			wantActive: "plugin:simplify",
		},
		{
			name:  "tabs and newlines around context",
			input: " \n/simplify \t this diff \n ",
			skills: map[string]skill.SkillMeta{
				"simplify": {Name: "simplify", SkillFile: writeSkillBodyFile(t, "follow these steps")},
			},
			want:       "follow these steps\n\nUser context:\nthis diff",
			wantOK:     true,
			wantActive: "simplify",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSession(t)
			s.skills = tt.skills
			_ = drainSlashEvents(s)

			got, ok := s.expandSlashCommand(context.Background(), tt.input)
			if ok != tt.wantOK || got != tt.want {
				t.Fatalf("expanded = %q, %v; want %q, %v", got, ok, tt.want, tt.wantOK)
			}
			var activated []string
			for _, ev := range drainSlashEvents(s) {
				if ev.Kind != events.EventSkillActivated {
					continue
				}
				data, ok := ev.Data.(events.SkillActivatedData)
				if ok {
					activated = append(activated, data.Name)
				}
			}
			if len(activated) != 1 || activated[0] != tt.wantActive {
				t.Fatalf("activation names = %v, want [%q]", activated, tt.wantActive)
			}
		})
	}
}

func TestExpandSlashCommandStandalonePreservesCommandPrecedence(t *testing.T) {
	s := newTestSession(t)
	s.skills = map[string]skill.SkillMeta{
		"review": {Name: "review", SkillFile: writeSkillBodyFile(t, "skill body")},
	}
	s.pluginCommands = map[string]plugin.Command{
		"review": {Name: "review", Body: "command $ARGUMENTS", Source: "project"},
	}
	_ = drainSlashEvents(s)

	got, ok := s.expandSlashCommand(context.Background(), "/review diff")
	if !ok || got != "command diff" {
		t.Fatalf("expanded = %q, %v; want command expansion", got, ok)
	}
	for _, ev := range drainSlashEvents(s) {
		if ev.Kind == events.EventSkillActivated {
			t.Fatal("command precedence emitted a skill activation")
		}
	}
}

func TestExpandSlashCommandStandaloneUnknownAndAmbiguousFallThrough(t *testing.T) {
	tests := []struct {
		name   string
		skills map[string]skill.SkillMeta
		input  string
	}{
		{name: "unknown", skills: nil, input: "/missing context"},
		{
			name: "ambiguous suffix",
			skills: map[string]skill.SkillMeta{
				"one:review": {Name: "review", SkillFile: writeSkillBodyFile(t, "one")},
				"two:review": {Name: "review", SkillFile: writeSkillBodyFile(t, "two")},
			},
			input: "/review context",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSession(t)
			s.skills = tt.skills
			_ = drainSlashEvents(s)
			got, ok := s.expandSlashCommand(context.Background(), tt.input)
			if ok || got != tt.input {
				t.Fatalf("expanded = %q, %v; want unchanged input", got, ok)
			}
			for _, ev := range drainSlashEvents(s) {
				if ev.Kind == events.EventSkillActivated {
					t.Fatal("fall-through emitted a skill activation")
				}
			}
		})
	}
}

func TestExpandSlashCommandStandaloneBodyLoadFailureWarnsWithoutActivation(t *testing.T) {
	s := newTestSession(t)
	s.skills = map[string]skill.SkillMeta{
		"simplify": {Name: "simplify", SkillFile: filepath.Join(t.TempDir(), "missing", "SKILL.md")},
	}
	_ = drainSlashEvents(s)

	got, ok := s.expandSlashCommand(context.Background(), "/simplify context")
	if ok || got != "/simplify context" {
		t.Fatalf("expanded = %q, %v; want unchanged input", got, ok)
	}
	var warned bool
	for _, ev := range drainSlashEvents(s) {
		if ev.Kind == events.EventSkillActivated {
			t.Fatal("failed skill load emitted an activation")
		}
		if ev.Kind == events.EventWarning {
			if data, ok := ev.Data.(events.WarningData); ok && strings.Contains(data.Message, "loading slash skill /simplify failed") {
				warned = true
			}
		}
	}
	if !warned {
		t.Fatal("failed skill load did not emit a warning")
	}
}

// writeEvenerwideCommandFile creates <workDir>/.evener/commands/<name>.md.
func writeEvenerwideCommandFile(t *testing.T, workDir, name, content string) {
	t.Helper()
	dir := filepath.Join(workDir, ".evener", "commands")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestEvenerwideCommand_LoadsWithNoPluginDirs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	client := llm.NewClient()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}}
	client.Register(adapter)
	workDir := t.TempDir()
	writeEvenerwideCommandFile(t, workDir, "review", "Review $ARGUMENTS")
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	cmd, ok := plugin.ResolveCommand(sess.pluginCommands, "review")
	if !ok || cmd.Source != "project" {
		t.Fatalf("pluginCommands = %v, want project command %q", sess.pluginCommands, "review")
	}
}

func TestEvenerwideCommand_ShadowsPluginBareName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	workDir := t.TempDir()
	writeEvenerwideCommandFile(t, workDir, "greet", "evener-wide body")
	pluginDir := writePluginCommand(t, "greeter", "greet", "plugin body")
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), SessionConfig{PluginDirs: []string{pluginDir}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	bare, ok := plugin.ResolveCommand(sess.pluginCommands, "greet")
	if !ok || bare.Source == "plugin" {
		t.Errorf("bare /greet resolved to %+v, want the evener-wide command", bare)
	}
	qualified, ok := plugin.ResolveCommand(sess.pluginCommands, "greeter:greet")
	if !ok || qualified.Source != "plugin" {
		t.Errorf("/greeter:greet resolved to %+v, want the plugin command", qualified)
	}
}

func TestEvenerwideCommand_DiscoveryWarningsQueued(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	workDir := t.TempDir()
	writeEvenerwideCommandFile(t, workDir, "bad name", "body") // whitespace guard fires
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	if len(sess.pluginCommands) != 0 {
		t.Errorf("pluginCommands = %v, want the guarded file skipped", sess.pluginCommands)
	}
	sess.Close()
	var warned bool
	for ev := range sess.Events() {
		if warning, ok := ev.Data.(events.WarningData); ok && warning.Title == "whitespace in command name" {
			warned = true
			break
		}
	}
	if !warned {
		t.Error("no discovery warning emitted; want the whitespace-guard warning")
	}
}

// writePluginCommand creates a plugin directory named pluginName with a single
// slash command file at commands/<cmdName>.md holding the given frontmatter+body
// content, and returns the plugin dir.
func writePluginCommand(t *testing.T, pluginName, cmdName, content string) string {
	t.Helper()
	dir := makePluginDir(t, pluginName)
	commandsDir := filepath.Join(dir, "commands")
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatalf("mkdir commands: %v", err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, cmdName+".md"), []byte(content), 0644); err != nil {
		t.Fatalf("write command %s: %v", cmdName, err)
	}
	return dir
}

func newTestSessionWithPlugins(t *testing.T, pluginDirs ...string) (*Session, *fakeAdapter) {
	t.Helper()
	client := llm.NewClient()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			return finalResponse("captured: " + req.Messages[len(req.Messages)-1].Text())
		},
	}}
	client.Register(adapter)
	workDir := t.TempDir()
	cfg := SessionConfig{PluginDirs: pluginDirs}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess, adapter
}

func TestExpandSlashCommand_PlainTextUnchanged(t *testing.T) {
	t.Parallel()
	sess, _ := newTestSessionWithPlugins(t)
	got, ok := sess.expandSlashCommand(context.Background(), "just chatting, not a command")
	if ok {
		t.Fatalf("expected ok=false for plain text, got expanded %q", got)
	}
	if got != "just chatting, not a command" {
		t.Errorf("got %q, want input unchanged", got)
	}
}

func TestExpandSlashCommand_UnknownCommandUnchanged(t *testing.T) {
	t.Parallel()
	dir := writePluginCommand(t, "greeter", "greet", "---\nname: greet\ndescription: greet\n---\nHi $ARGUMENTS")
	sess, _ := newTestSessionWithPlugins(t, dir)
	got, ok := sess.expandSlashCommand(context.Background(), "/nonexistent some args")
	if ok {
		t.Fatalf("expected ok=false for an unrecognized command, got expanded %q", got)
	}
	if got != "/nonexistent some args" {
		t.Errorf("got %q, want input unchanged", got)
	}
}

func TestExpandSlashCommand_BareSlashUnchanged(t *testing.T) {
	t.Parallel()
	sess, _ := newTestSessionWithPlugins(t)
	got, ok := sess.expandSlashCommand(context.Background(), "/")
	if ok {
		t.Fatalf("expected ok=false for a bare slash, got expanded %q", got)
	}
	if got != "/" {
		t.Errorf("got %q, want input unchanged", got)
	}
}

func TestExpandSlashCommand_ExpandsKnownCommand(t *testing.T) {
	t.Parallel()
	dir := writePluginCommand(t, "greeter", "greet", "---\nname: greet\ndescription: greet\n---\nHi $ARGUMENTS")
	sess, _ := newTestSessionWithPlugins(t, dir)
	got, ok := sess.expandSlashCommand(context.Background(), "/greet world")
	if !ok {
		t.Fatal("expected ok=true for a recognized command")
	}
	if got != "Hi world" {
		t.Errorf("got %q, want %q", got, "Hi world")
	}
}

// execRecordingEnv wraps a local execution environment and records
// ExecCommand calls, so tests can assert a !` span never executed.
// probeCapabilities legitimately calls ExecCommand from two goroutines at
// once (its git probe and tool probe run concurrently by design), so calls
// is an atomic counter rather than a plain int.
type execRecordingEnv struct {
	execenv.ExecutionEnvironment
	calls atomic.Int64
}

func (e *execRecordingEnv) ExecCommand(ctx context.Context, command string, timeoutMs int, dir string, env map[string]string) (execenv.ExecResult, error) {
	e.calls.Add(1)
	return e.ExecutionEnvironment.ExecCommand(ctx, command, timeoutMs, dir, env)
}

func TestExpandSlashCommand_EvenerwideDoesNotExecute(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}})
	workDir := t.TempDir()
	writeEvenerwideCommandFile(t, workDir, "deploy", "Deploying !`touch SHOULD_NOT_EXIST` for $1")
	env := &execRecordingEnv{ExecutionEnvironment: execenv.NewLocalExecutionEnvironment(workDir)}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	// NewSession may inspect the workspace through ExecCommand; only calls
	// made while expanding the slash command are relevant to this assertion.
	env.calls.Store(0)

	got, ok := sess.expandSlashCommand(context.Background(), "/deploy v2")
	if !ok {
		t.Fatal("expected ok=true for a evener-wide command")
	}
	if calls := env.calls.Load(); calls != 0 {
		t.Errorf("ExecCommand called %d times; evener-wide expansion must never execute", calls)
	}
	if !strings.Contains(got, "!`touch SHOULD_NOT_EXIST`") || !strings.Contains(got, "for v2") {
		t.Errorf("expanded %q, want the !` span kept as text with $1 substituted", got)
	}
	if _, statErr := os.Stat(filepath.Join(workDir, "SHOULD_NOT_EXIST")); !os.IsNotExist(statErr) {
		t.Error("the !` span executed: SHOULD_NOT_EXIST exists")
	}
}

// TestExpandSlashCommand_ExpandErrorEmitsWarning proves an Expand failure is
// surfaced to the user instead of being silently swallowed. command.Expand's
// only error path is ctx already being done at entry (every per-token
// failure — a failing !`cmd`, a missing @file — degrades to inline text
// instead), so an already-canceled context is what reliably reproduces it
// here. The command must still fall back to submitting the literal input
// (ok=false) — "surface it" means "stop being silent about it", not "block
// the input" — but now with a warning naming the failed command.
func TestExpandSlashCommand_ExpandErrorEmitsWarning(t *testing.T) {
	t.Parallel()
	dir := writePluginCommand(t, "greeter", "greet", "---\nname: greet\ndescription: greet\n---\nHi $ARGUMENTS")
	sess, _ := newTestSessionWithPlugins(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, ok := sess.expandSlashCommand(ctx, "/greet world")
	if ok {
		t.Fatalf("expected ok=false when Expand errors, got expanded %q", got)
	}
	if got != "/greet world" {
		t.Errorf("got %q, want the literal input preserved as fallback", got)
	}

	sess.Close()
	var warned bool
	for ev := range sess.Events() {
		if w, isWarning := ev.Data.(events.WarningData); isWarning && strings.Contains(w.Message, "greet") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("expected an EventWarning naming the failed command when Expand errors")
	}
}

func TestExpandSlashCommand_QualifiedNameExpands(t *testing.T) {
	t.Parallel()
	dir := writePluginCommand(t, "greeter", "greet", "---\nname: greet\ndescription: greet\n---\nHi $ARGUMENTS")
	sess, _ := newTestSessionWithPlugins(t, dir)
	got, ok := sess.expandSlashCommand(context.Background(), "/greeter:greet world")
	if !ok {
		t.Fatal("expected ok=true for a fully-qualified command name")
	}
	if got != "Hi world" {
		t.Errorf("got %q, want %q", got, "Hi world")
	}
}

// TestProcessOneInput_SlashCommandExpandsForUserInput proves the interception
// wired into processOneInput actually reaches the model: the fake adapter
// receives the EXPANDED body, not the literal "/greet world" the user typed.
func TestProcessOneInput_SlashCommandExpandsForUserInput(t *testing.T) {
	t.Parallel()
	dir := writePluginCommand(t, "greeter", "greet", "---\nname: greet\ndescription: greet\n---\nHi $ARGUMENTS")
	sess, adapter := newTestSessionWithPlugins(t, dir)

	if _, err := sess.ProcessInput(context.Background(), "/greet world", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("expected at least one request to the model")
	}
	last := reqs[len(reqs)-1]
	if len(last.Messages) == 0 {
		t.Fatal("expected at least one message in the request")
	}
	gotText := last.Messages[len(last.Messages)-1].Text()
	if !strings.Contains(gotText, "Hi world") {
		t.Errorf("last message text = %q, want it to contain the expanded %q", gotText, "Hi world")
	}
	if strings.Contains(gotText, "/greet") {
		t.Errorf("last message text = %q, should not contain the literal slash command", gotText)
	}
}

// TestProcessOneInput_ContinuationDoesNotExpandSlashCommand proves the
// interception is gated on EntryUserInput: a system-framed continuation whose
// text happens to start with "/" is delivered to the model verbatim, not
// expanded as a plugin command.
func TestProcessOneInput_ContinuationDoesNotExpandSlashCommand(t *testing.T) {
	t.Parallel()
	dir := writePluginCommand(t, "greeter", "greet", "---\nname: greet\ndescription: greet\n---\nHi $ARGUMENTS")
	sess, adapter := newTestSessionWithPlugins(t, dir)

	_, _, err := sess.processOneInput(context.Background(), "/greet world", nil, EntryContinuation, nil)
	if err != nil {
		t.Fatalf("processOneInput: %v", err)
	}

	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("expected at least one request to the model")
	}
	last := reqs[len(reqs)-1]
	gotText := last.Messages[len(last.Messages)-1].Text()
	if !strings.Contains(gotText, "/greet world") {
		t.Errorf("last message text = %q, want the literal unexpanded continuation text", gotText)
	}
}

// --- Resume fail-soft (broken/duplicate plugin dirs must not brick NewSession) ---

// TestInitPlugins_BrokenPluginDirSkippedWithWarning proves a broken plugin dir
// in PluginDirs (e.g. a stale/edited dir named by a resumed session's
// persisted config, or any directly-constructed SessionConfig) no longer
// aborts NewSession/resume: the offender is skipped and a warning is queued
// instead of the whole session failing to start.
func TestInitPlugins_BrokenPluginDirSkippedWithWarning(t *testing.T) {
	t.Parallel()
	brokenDir := t.TempDir() // no .claude-plugin/plugin.json at all: Load fails.

	warnings := sessionWarnings(t, brokenDir)

	var found *string
	for i := range warnings {
		if warnings[i].Title == "broken plugin skipped" && strings.Contains(warnings[i].Message, brokenDir) {
			found = &warnings[i].Message
			break
		}
	}
	if found == nil {
		t.Fatalf("expected a warning naming the broken plugin dir %q; got %+v", brokenDir, warnings)
	}
}

// TestInitPlugins_DuplicatePluginNameSkippedWithWarning proves two plugin dirs
// sharing the same manifest name no longer abort NewSession: the second is
// skipped with a warning and the first's components still load.
func TestInitPlugins_DuplicatePluginNameSkippedWithWarning(t *testing.T) {
	t.Parallel()
	dir1 := makePluginDir(t, "same-name")
	dir2 := makePluginDir(t, "same-name")

	warnings := sessionWarnings(t, dir1, dir2)

	var found bool
	for _, w := range warnings {
		if w.Title == "duplicate plugin skipped" && w.PluginName == "same-name" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a duplicate-plugin warning naming %q; got %+v", "same-name", warnings)
	}
}

// TestInitPlugins_BrokenPluginDoesNotBlockHealthyPlugins proves a broken dir
// alongside a healthy one still lets the healthy plugin's components load.
func TestInitPlugins_BrokenPluginDoesNotBlockHealthyPlugins(t *testing.T) {
	t.Parallel()
	brokenDir := t.TempDir()
	healthyDir := makePluginDir(t, "healthy-plugin")
	skillDir := filepath.Join(healthyDir, "skills", "my-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: my-skill\ndescription: test\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}

	client := llm.NewClient()
	workDir := t.TempDir()
	cfg := SessionConfig{PluginDirs: []string{brokenDir, healthyDir}}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if _, ok := sess.skills["healthy-plugin:my-skill"]; !ok {
		t.Errorf("expected healthy-plugin's skill to load despite the broken dir, got skills: %v", keys(sess.skills))
	}
}

// --- §14 greenfield: model/allowed-tools are parsed but not enforced ---

func TestInitPlugins_CommandModelOverrideWarnsUnenforced(t *testing.T) {
	t.Parallel()
	dir := writePluginCommand(t, "model-plugin", "special", "---\nname: special\ndescription: d\nmodel: opus\n---\nBody")

	warnings := sessionWarnings(t, dir)

	var found bool
	for _, w := range warnings {
		if w.Title == "unenforced command override" && w.PluginName == "model-plugin" && strings.Contains(w.Message, "model") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an unenforced-model-override warning; got %+v", warnings)
	}
}

func TestInitPlugins_CommandAllowedToolsWarnsUnenforced(t *testing.T) {
	t.Parallel()
	dir := writePluginCommand(t, "tools-plugin", "special", "---\nname: special\ndescription: d\nallowed-tools:\n  - Bash\n---\nBody")

	warnings := sessionWarnings(t, dir)

	var found bool
	for _, w := range warnings {
		if w.Title == "unenforced command override" && w.PluginName == "tools-plugin" && strings.Contains(w.Message, "allowed-tools") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an unenforced-allowed-tools warning; got %+v", warnings)
	}
}

func TestInitPlugins_CommandWithoutOverridesNoWarning(t *testing.T) {
	t.Parallel()
	dir := writePluginCommand(t, "plain-plugin", "plain", "---\nname: plain\ndescription: d\n---\nBody")

	warnings := sessionWarnings(t, dir)

	for _, w := range warnings {
		if w.PluginName == "plain-plugin" {
			t.Fatalf("expected no warning for a command with no model/allowed-tools override; got %+v", w)
		}
	}
}
