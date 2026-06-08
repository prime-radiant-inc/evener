package hooks

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/plugin"
)

// TestMatchHooks_NoDispatchEmitBreaksRecursion reproduces the production crash
// cycle at the Runner boundary: a Notification hook with an invalid matcher whose
// dispatch (RunNotification → runAll → MatchHooks) used to emit an EventWarning
// via onEvent. In production onEvent is Session.emit, and emit(EventWarning) calls
// runNotificationHook → RunNotification → MatchHooks → onEvent(EventWarning) → …,
// an unbounded recursion that overflows the stack.
//
// Here onEvent re-enters RunNotification on every EventWarning to mimic that
// wiring, with a hard depth bound. BEFORE the fix MatchHooks emits on the invalid
// matcher, so the bound is exceeded and the test fails. AFTER the fix MatchHooks
// skips silently, the chain never starts, and the bound is never reached.
func TestMatchHooks_NoDispatchEmitBreaksRecursion(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookNotification, plugin.RegisteredHook{
		Matcher:    "(", // invalid regex — the armament
		Type:       "command",
		Command:    "echo should-not-run",
		Timeout:    5,
		PluginName: "broken-plugin",
	})

	const bound = 50
	depth := 0
	r.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		if kind != events.EventWarning {
			return
		}
		depth++
		if depth > bound {
			// Stop the runaway so the test process survives to report the failure.
			return
		}
		// Mirror production: an EventWarning re-enters the Notification dispatch.
		r.RunNotification(context.Background(), Input{HookEventName: string(plugin.HookNotification)})
	})

	// Triggers the first dispatch, mimicking emit(EventWarning) → runNotificationHook.
	r.RunNotification(context.Background(), Input{HookEventName: string(plugin.HookNotification)})

	if depth > bound {
		t.Fatalf("invalid-matcher dispatch recursed past depth %d; MatchHooks must not emit on dispatch", bound)
	}
}

// TestMatchHooks_InvalidMatcherSkipsSilently verifies that an invalid-regex
// matcher is skipped at DISPATCH time WITHOUT emitting any event. Dispatch-time
// emission is the root of the EventWarning→Notification→MatchHooks recursion and
// of the per-tool-call warning storm; invalid-matcher diagnostics now happen once
// at load time via Validate, never on the dispatch path.
func TestMatchHooks_InvalidMatcherSkipsSilently(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher:    "(", // invalid regex
		Type:       "command",
		Command:    "echo should-not-run",
		Timeout:    5,
		PluginName: "broken-plugin",
	})

	var emitted int
	r.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		emitted++
	})

	// Dispatch three times; the invalid matcher must be skipped and NOTHING emitted.
	for i := 0; i < 3; i++ {
		if got := r.MatchHooks(plugin.HookPreToolUse, "Bash"); len(got) != 0 {
			t.Fatalf("invalid regex: hook must be skipped; got %d matches", len(got))
		}
	}
	if emitted != 0 {
		t.Fatalf("MatchHooks must not emit on the dispatch path; got %d events", emitted)
	}
}

// TestValidate_ReportsInvalidMatcherOnce verifies that Validate detects each
// invalid-regex matcher exactly once and reports the plugin, event, and matcher.
// Valid matchers (exact, pipe-list, wildcard, valid regex) produce no diagnostics.
func TestValidate_ReportsInvalidMatcherOnce(t *testing.T) {
	r := newRunner(nil, "")
	r.Add(plugin.HookPreToolUse,
		plugin.RegisteredHook{Matcher: "(", Type: "command", Command: "echo x", PluginName: "broken-plugin"},
		plugin.RegisteredHook{Matcher: "Bash|Edit", Type: "command", Command: "echo ok", PluginName: "good-plugin"},
		plugin.RegisteredHook{Matcher: "*", Type: "command", Command: "echo all", PluginName: "good-plugin"},
		plugin.RegisteredHook{Matcher: "mcp__.*", Type: "command", Command: "echo mcp", PluginName: "good-plugin"},
	)

	diags := r.Validate()
	if len(diags) != 1 {
		t.Fatalf("expected exactly 1 invalid-matcher diagnostic, got %d: %+v", len(diags), diags)
	}
	d := diags[0]
	if d.PluginName != "broken-plugin" {
		t.Errorf("PluginName = %q, want %q", d.PluginName, "broken-plugin")
	}
	if d.Event != string(plugin.HookPreToolUse) {
		t.Errorf("Event = %q, want %q", d.Event, plugin.HookPreToolUse)
	}
	if !strings.Contains(d.Message, "(") {
		t.Errorf("message %q should name the offending matcher", d.Message)
	}
	if !strings.Contains(strings.ToLower(d.Message), "matcher") {
		t.Errorf("message %q should explain the matcher is invalid", d.Message)
	}
}
