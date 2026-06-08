package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// drainWarnings closes the session and returns every WarningData emitted on its
// event stream. The session must already be constructed.
func drainWarnings(t *testing.T, sess *Session) []events.WarningData {
	t.Helper()
	sess.Close()
	var warnings []events.WarningData
	for ev := range sess.Events() {
		if w, ok := ev.Data.(events.WarningData); ok {
			warnings = append(warnings, w)
		}
	}
	return warnings
}

// TestNewSession_InvalidMatcherNotificationHookDoesNotRecurse is the regression
// gate for the stack-overflow crash. A plugin arms the cycle with a Notification
// hook whose matcher is an invalid regex "(", and triggers it with a typo'd event
// that produces a load-time config warning. Before the fix, emitting that warning
// ran the Notification hook → RunNotification → MatchHooks → emit(EventWarning) →
// … unbounded recursion → fatal stack overflow at NewSession. After the fix,
// NewSession returns normally and the warnings stream is finite.
//
// A bare unbounded recursion would crash the whole test binary; this test simply
// asserts NewSession completes and Close drains a finite stream — both impossible
// if the recursion were still present.
func TestNewSession_InvalidMatcherNotificationHookDoesNotRecurse(t *testing.T) {
	dir := writePluginHooks(t, "recur-plugin", `{
		"hooks": {
			"Notification": [
				{"matcher": "(", "hooks": [{"type": "command", "command": "echo notify"}]}
			],
			"PreToolUze": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "echo typo"}]}
			]
		}
	}`)

	client := llm.NewClient()
	cfg := SessionConfig{PluginDirs: []string{dir}}

	done := make(chan *Session, 1)
	go func() {
		sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
		if err != nil {
			t.Errorf("NewSession: %v", err)
			done <- nil
			return
		}
		done <- sess
	}()

	var sess *Session
	select {
	case sess = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("NewSession did not return within 10s — likely the warning→Notification recursion")
	}
	if sess == nil {
		return
	}

	warnings := drainWarnings(t, sess)
	// The config diagnostics (typo + invalid matcher) must still be visible.
	if len(warnings) == 0 {
		t.Fatalf("expected the hook-config diagnostics to be emitted; got none")
	}
}

// TestNewSession_HookConfigWarningDoesNotRunNotificationHook asserts that a
// load-time hook-CONFIG diagnostic (a typo'd event name) does NOT spuriously run
// the plugin's Notification command hook at session start. The Notification hook
// writes a marker file; the marker must not exist after the session starts,
// because config diagnostics are emitted via the non-Notification-firing path.
func TestNewSession_HookConfigWarningDoesNotRunNotificationHook(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "notification-ran")
	dir := writePluginHooks(t, "marker-plugin", `{
		"hooks": {
			"Notification": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "touch `+marker+`"}]}
			],
			"PreToolUze": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "echo typo"}]}
			]
		}
	}`)

	client := llm.NewClient()
	cfg := SessionConfig{PluginDirs: []string{dir}}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	warnings := drainWarnings(t, sess)

	// The typo diagnostic must be visible...
	var sawTypo bool
	for _, w := range warnings {
		if w.EventName == "PreToolUze" {
			sawTypo = true
		}
	}
	if !sawTypo {
		t.Fatalf("expected the PreToolUze typo diagnostic to be emitted; got %+v", warnings)
	}
	// ...but the Notification command hook must NOT have run.
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("Notification hook ran on a hook-config warning (marker %q exists); config diagnostics must not fire the Notification hook", marker)
	}
}

// TestEmitWarning_ReentrancyGuardBlocksNotificationHook is the defense-in-depth
// check: even setting aside the diagnostic path, a genuine EventWarning emitted
// while a Notification-hook run is already in progress must NOT re-trigger the
// hook. The hook appends a line to a counter file each time it runs; with the
// guard already engaged (inNotificationHook=true, as it would be mid-run), the
// emit must be a no-op for the hook, so no line is written.
func TestEmitWarning_ReentrancyGuardBlocksNotificationHook(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "ran.log")
	dir := writePluginHooks(t, "notif-plugin", `{
		"hooks": {
			"Notification": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "echo x >> `+counter+`"}]}
			]
		}
	}`)

	client := llm.NewClient()
	cfg := SessionConfig{PluginDirs: []string{dir}}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Simulate being inside a Notification-hook run, then emit a genuine warning.
	if !sess.inNotificationHook.CompareAndSwap(false, true) {
		t.Fatal("guard unexpectedly already set after NewSession")
	}
	sess.emit(events.EventWarning, events.WarningData{Message: "context length exceeded"})
	sess.inNotificationHook.Store(false)

	// The guard must have blocked the re-entrant hook run: no line written.
	if data, err := os.ReadFile(counter); err == nil && len(strings.TrimSpace(string(data))) != 0 {
		t.Fatalf("re-entrancy guard failed: Notification hook ran while one was in progress (counter=%q)", string(data))
	}

	// Sanity: with the guard clear, a genuine warning DOES run the hook exactly once.
	sess.emit(events.EventWarning, events.WarningData{Message: "context length exceeded"})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(counter); err == nil && strings.Count(string(data), "x") == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, _ := os.ReadFile(counter)
	t.Fatalf("genuine warning should run the Notification hook exactly once; counter=%q", string(data))
}

// TestNewSession_InvalidMatcherWarnsOnce asserts that an invalid PreToolUse matcher
// produces exactly one diagnostic warning at session start, regardless of how many
// dispatches occur, and that the matcher diagnostic names the plugin.
func TestNewSession_InvalidMatcherWarnsOnce(t *testing.T) {
	dir := writePluginHooks(t, "badmatch-plugin", `{
		"hooks": {
			"PreToolUse": [
				{"matcher": "(", "hooks": [{"type": "command", "command": "echo should-not-run"}]}
			]
		}
	}`)

	client := llm.NewClient()
	cfg := SessionConfig{PluginDirs: []string{dir}}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Dispatch the invalid-matcher event several times; this must NOT add warnings.
	for i := 0; i < 3; i++ {
		sess.hookRunner.MatchHooks("PreToolUse", "Bash")
	}

	warnings := drainWarnings(t, sess)

	var matcherWarnings []events.WarningData
	for _, w := range warnings {
		if strings.Contains(strings.ToLower(w.Message), "matcher") {
			matcherWarnings = append(matcherWarnings, w)
		}
	}
	if len(matcherWarnings) != 1 {
		t.Fatalf("expected exactly 1 invalid-matcher warning regardless of dispatch count, got %d: %+v", len(matcherWarnings), matcherWarnings)
	}
	if matcherWarnings[0].PluginName != "badmatch-plugin" {
		t.Errorf("PluginName = %q, want %q", matcherWarnings[0].PluginName, "badmatch-plugin")
	}
}
