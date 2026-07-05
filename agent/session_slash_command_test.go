package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

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
