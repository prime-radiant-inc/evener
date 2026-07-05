package agent

import (
	"os/exec"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// mcpWarningFlushTruePath resolves the `true` binary used to build a dead
// inline MCP server spec ("deadsvc:<path>"): `true` exits immediately without
// speaking MCP, so its stdout closes before the initialize handshake
// completes and mcp.NewManager's Connect fails. This is the exact dead-server
// construction TestIntg_InitMCP_ConnectError uses (cov_intg_mcp_test.go).
func mcpWarningFlushTruePath(t *testing.T) string {
	t.Helper()
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("`true` not found: %v", err)
	}
	return truePath
}

// mcpWarningFlushDrainAll closes sess and returns every event on its stream in
// emission order. Mirrors drainWarnings (hook_notification_recursion_test.go),
// but keeps all events rather than filtering down to WarningData: this test
// needs the MCP warning's position relative to SESSION_START.
func mcpWarningFlushDrainAll(t *testing.T, sess *Session) []events.SessionEvent {
	t.Helper()
	sess.Close()
	var evs []events.SessionEvent
	for ev := range sess.Events() {
		evs = append(evs, ev)
	}
	return evs
}

// mcpWarningFlushAssert asserts that evs contains a WARNING event whose
// Message names the dead server "deadsvc" and contains "failed to", arriving
// strictly after SESSION_START. This keys on Message, not Source: enrichment
// rewrites an unrecognized "mcp" Source until Task 10 lands, but never
// touches Message. It also asserts no Notification hook fired — this session
// has no plugin dirs, so it has no hookRunner to fire in the first place, and
// no HOOK_START/HOOK_END should appear on the stream (mirrors the
// no-panic/no-extra-emission assertion style of the recursion tests in
// hook_notification_recursion_test.go).
func mcpWarningFlushAssert(t *testing.T, evs []events.SessionEvent) {
	t.Helper()
	sessionStartIdx := -1
	warningIdx := -1
	for i, ev := range evs {
		switch ev.Kind {
		case events.EventSessionStart:
			if sessionStartIdx == -1 {
				sessionStartIdx = i
			}
		case events.EventWarning:
			if w, ok := ev.Data.(events.WarningData); ok &&
				strings.Contains(w.Message, "deadsvc") && strings.Contains(w.Message, "failed to") &&
				warningIdx == -1 {
				warningIdx = i
			}
		case events.EventHookStart, events.EventHookEnd:
			t.Fatalf("Notification hook fired (%s event present) but the session has no hookRunner", ev.Kind)
		}
	}
	if sessionStartIdx == -1 {
		t.Fatalf("no SESSION_START event found; got %d events", len(evs))
	}
	if warningIdx == -1 {
		t.Fatalf(`no WARNING event naming the dead server "deadsvc" with "failed to" found; got %+v`, evs)
	}
	if warningIdx <= sessionStartIdx {
		t.Fatalf("MCP WARNING event (index %d) did not arrive after SESSION_START (index %d)", warningIdx, sessionStartIdx)
	}
}

// TestMCPWarningFlush_NewSession asserts that NewSession with a dead inline
// MCP server flushes the collected pendingMCPWarnings onto the event stream
// after SESSION_START, instead of silently accumulating them forever. Task 4
// populates pendingMCPWarnings in initMCP, but nothing drained it onto the
// stream until this task's flush loop in emitSessionStartEnvelope.
func TestMCPWarningFlush_NewSession(t *testing.T) {
	t.Parallel()
	truePath := mcpWarningFlushTruePath(t)

	client := llm.NewClient()
	cfg := SessionConfig{MCPInline: []string{"deadsvc:" + truePath}}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession must survive a dead MCP server, got: %v", err)
	}

	mcpWarningFlushAssert(t, mcpWarningFlushDrainAll(t, sess))
}

// TestMCPWarningFlush_Restore is the restore-path counterpart:
// emitSessionStartEnvelope is called by both NewSession and
// RestoreSessionFromMetaWithConfig, and restored sessions deliberately
// re-emit their MCP warnings (see the emitSessionStartEnvelope doc comment in
// session_events.go) since the dead server fails to connect again on every
// restore.
func TestMCPWarningFlush_Restore(t *testing.T) {
	t.Parallel()
	truePath := mcpWarningFlushTruePath(t)

	snap := SessionConfig{MCPInline: []string{"deadsvc:" + truePath}}.toSnapshot()
	meta := schema.SessionMeta{ID: "01MCPWARNFLUSHRESTORE0001", ProfileID: "openai", Model: "gpt-5.2", Config: snap}

	sess, err := RestoreSessionFromMetaWithConfig(
		w3init_restoreClient(), NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()), meta,
		RestoreSessionConfig{StateDir: t.TempDir()},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig must survive a dead MCP server, got: %v", err)
	}

	mcpWarningFlushAssert(t, mcpWarningFlushDrainAll(t, sess))
}
