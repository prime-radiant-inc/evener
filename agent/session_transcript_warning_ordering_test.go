package agent

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// TestTranscriptCreateFailedWarningRidesAfterSessionStart (kata et0x) asserts
// that a transcript-create failure during NewSession no longer jumps the
// SESSION_START envelope. Before the fix this warning was emitted via a bare
// s.emit call at the point of failure — strictly before
// s.emitSessionStartEnvelope ran later in the same function — making it the
// one construction-time diagnostic a client could see before being told the
// session had started at all. It is now buffered into
// pendingTranscriptWarnings and flushed by emitSessionStartEnvelope, same as
// the pre-existing plugin/hook/MCP buffers.
//
// Mirrors cov_mcp_warning_flush_test.go's TestMCPWarningFlush_NewSession
// (same fault-injection-then-drain-then-assert-order shape), forcing the
// transcript.NewWriter failure via the sessionInitFault("new_transcript")
// test seam that session_init_seed100_exact_fuzz_test.go's fuzzSessionInitFaultBoundaries
// already exercises for a plain error-propagation check; this test instead
// asserts the WARNING's position on the stream.
func TestTranscriptCreateFailedWarningRidesAfterSessionStart(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})

	cfg := SessionConfig{}
	cfg.StateDir = t.TempDir()
	cfg.testOnly.sessionInitFault = func(point string) error {
		if point == "new_transcript" {
			return errTranscriptCreateFaultForTest
		}
		return nil
	}

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession must survive a transcript-create failure (fail-open), got: %v", err)
	}

	evs := transcriptWarningOrderingDrainAll(t, sess)
	sessionStartIdx := -1
	warningIdx := -1
	for i, ev := range evs {
		switch ev.Kind {
		case events.EventSessionStart:
			if sessionStartIdx == -1 {
				sessionStartIdx = i
			}
		case events.EventWarning:
			if w, ok := ev.Data.(events.WarningData); ok && strings.Contains(w.Message, "transcript create failed") && warningIdx == -1 {
				warningIdx = i
			}
		}
	}
	if sessionStartIdx == -1 {
		t.Fatalf("no SESSION_START event found; got %d events", len(evs))
	}
	if warningIdx == -1 {
		t.Fatalf(`no "transcript create failed" WARNING event found; got %+v`, evs)
	}
	if warningIdx <= sessionStartIdx {
		t.Fatalf("transcript-create-failed WARNING event (index %d) did not arrive after SESSION_START (index %d)", warningIdx, sessionStartIdx)
	}
}

var errTranscriptCreateFaultForTest = errors.New("injected transcript create fault")

// transcriptWarningOrderingDrainAll closes sess and returns every event on
// its stream in emission order (mirrors mcpWarningFlushDrainAll).
func transcriptWarningOrderingDrainAll(t *testing.T, sess *Session) []events.SessionEvent {
	t.Helper()
	sess.Close()
	var evs []events.SessionEvent
	for ev := range sess.Events() {
		evs = append(evs, ev)
	}
	return evs
}
