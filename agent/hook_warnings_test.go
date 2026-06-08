package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// writePluginHooks creates a plugin directory named name with the given
// hooks.json contents and returns the plugin dir.
func writePluginHooks(t *testing.T, name, hooksJSON string) string {
	t.Helper()
	dir := makePluginDir(t, name)
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(hooksJSON), 0644); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}
	return dir
}

// sessionWarnings builds a session that loads the given plugin dir, closes it,
// and returns every WarningData emitted on the session event stream.
func sessionWarnings(t *testing.T, pluginDir string) []events.WarningData {
	t.Helper()
	client := llm.NewClient()
	workDir := t.TempDir()
	cfg := SessionConfig{PluginDirs: []string{pluginDir}}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.Close()

	var warnings []events.WarningData
	for ev := range sess.Events() {
		if w, ok := ev.Data.(events.WarningData); ok {
			warnings = append(warnings, w)
		}
	}
	return warnings
}

// TestInitPlugins_UnknownHookEventWarnsLoudly asserts that a plugin declaring a
// hook for an unknown event name (a typo such as "PreToolUze") produces a loud
// WARNING on the session stream naming the plugin and the offending event, and
// saying the hook will never fire. Silent non-execution is the failure mode this
// guards against.
func TestInitPlugins_UnknownHookEventWarnsLoudly(t *testing.T) {
	dir := writePluginHooks(t, "typo-plugin", `{
		"hooks": {
			"PreToolUze": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "echo oops"}]}
			]
		}
	}`)

	warnings := sessionWarnings(t, dir)

	var found *events.WarningData
	for i := range warnings {
		if warnings[i].EventName == "PreToolUze" {
			found = &warnings[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected a WARNING naming the unknown event PreToolUze; got %+v", warnings)
	}
	if found.PluginName != "typo-plugin" {
		t.Errorf("PluginName = %q, want %q", found.PluginName, "typo-plugin")
	}
	msg := strings.ToLower(found.Message)
	if !strings.Contains(msg, "not") || !strings.Contains(msg, "fire") {
		t.Errorf("warning %q should say the event is not recognized and the hook will not fire", found.Message)
	}
}

// TestInitPlugins_UnsupportedHookEventWarns asserts that a plugin declaring a
// hook for a recognized-but-unsupported Claude event (one serf does not fire
// yet, e.g. PostToolUseFailure) produces a visible WARNING that the hook is
// declared for a reserved event serf does not yet fire, so it will not run.
func TestInitPlugins_UnsupportedHookEventWarns(t *testing.T) {
	dir := writePluginHooks(t, "reserved-plugin", `{
		"hooks": {
			"PostToolUseFailure": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "echo reserved"}]}
			]
		}
	}`)

	warnings := sessionWarnings(t, dir)

	var found *events.WarningData
	for i := range warnings {
		if warnings[i].EventName == "PostToolUseFailure" {
			found = &warnings[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected a WARNING naming the reserved event PostToolUseFailure; got %+v", warnings)
	}
	if found.PluginName != "reserved-plugin" {
		t.Errorf("PluginName = %q, want %q", found.PluginName, "reserved-plugin")
	}
	msg := strings.ToLower(found.Message)
	if !strings.Contains(msg, "not") {
		t.Errorf("warning %q should explain the event is not fired yet", found.Message)
	}
}

// TestInitPlugins_ValidHooksProduceNoWarning is the negative control: a plugin
// whose hooks are all well-formed and target supported events must produce no
// hook-misconfiguration warning (this is purely additive diagnostics; it must
// not warn on healthy configs).
func TestInitPlugins_ValidHooksProduceNoWarning(t *testing.T) {
	dir := writePluginHooks(t, "good-plugin", `{
		"hooks": {
			"PreToolUse": [
				{"matcher": "Bash|Edit", "hooks": [{"type": "command", "command": "echo ok"}]}
			]
		}
	}`)

	for _, w := range sessionWarnings(t, dir) {
		if w.PluginName == "good-plugin" {
			t.Errorf("well-formed hooks must not warn; got %+v", w)
		}
	}
}
