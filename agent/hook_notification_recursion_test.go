package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// TestEmitWarning_ConcurrentIndependentWarningsEachFireNotificationHook is the
// regression gate for the over-suppression bug: two GENUINE, independent warnings
// emitted concurrently from two goroutines (e.g. the session-namer goroutine's
// "log open failed" overlapping the turn loop's "context length exceeded") must
// EACH fire the Notification hook. A session-wide guard held for the full
// synchronous duration of runNotificationHook makes the second emit lose the
// CompareAndSwap and permanently drops its hook (no queue, no retry) — the hook
// runs only once. The hook sleeps briefly so the two runs genuinely overlap, then
// appends a line; the file must end with exactly two lines.
func TestEmitWarning_ConcurrentIndependentWarningsEachFireNotificationHook(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "ran.log")
	// The sleep makes the first hook run hold any session-wide guard long enough
	// for the concurrent second emit to collide with it.
	dir := writePluginHooks(t, "notif-plugin", `{
		"hooks": {
			"Notification": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "sleep 0.4; echo x >> `+counter+`"}]}
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
	drainEvents(sess) // keep the event channel moving; emit is best-effort anyway

	// Two independent goroutines emit a genuine warning at the same time. emit runs
	// the Notification hook synchronously in the calling goroutine, so both hook
	// runs are in flight together — the collision the guard used to lose.
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			sess.emit(events.EventWarning, events.WarningData{
				Message: fmt.Sprintf("independent warning %d", n),
			})
		}(i)
	}
	close(start)
	wg.Wait()

	data, _ := os.ReadFile(counter)
	if got := strings.Count(string(data), "x"); got != 2 {
		t.Fatalf("two concurrent independent warnings fired the Notification hook %d time(s), want 2 (the second was dropped by an over-broad guard); counter=%q", got, string(data))
	}
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

// TestNotificationHook_SystemMessageOutputDoesNotRecurse is the Phase B recursion
// gate: a Notification hook whose OWN output is a systemMessage must not re-fire
// the Notification hook. The systemMessage is delivered via deliverHookUserMessage
// → emitDiagnosticWarning (which does not run the Notification hook); if it were
// delivered via emit, the EventWarning would re-enter runNotificationHook and
// recurse. The hook appends to a counter and prints a systemMessage; firing one
// warning must run the hook exactly once.
func TestNotificationHook_SystemMessageOutputDoesNotRecurse(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "ran.log")
	dir := writePluginHooks(t, "notif-sysmsg-plugin", `{
		"hooks": {
			"Notification": [
				{"matcher": "*", "hooks": [{"type": "command", "command": "echo x >> `+counter+`; echo '{\"systemMessage\":\"notice\"}'"}]}
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
	drainEvents(sess)

	// Fire one genuine warning; emit runs the Notification hook synchronously.
	sess.emit(events.EventWarning, events.WarningData{Message: "trigger"})

	data, _ := os.ReadFile(counter)
	if got := strings.Count(string(data), "x"); got != 1 {
		t.Fatalf("Notification hook ran %d time(s), want 1 — a systemMessage output must not re-fire the Notification hook; counter=%q", got, string(data))
	}
}
