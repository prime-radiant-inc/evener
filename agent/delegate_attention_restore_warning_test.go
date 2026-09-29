package agent

import (
	"bytes"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// A cold delegate whose attention can't be restored is retried with backoff
// (up to every 5s) for as long as the attention is owed. One warning per
// attempt flooded the transcript with the same bare "restore delegate
// attention" line, and the line never said why. The session now warns once
// per failure episode, with the error in the message and its own code (so
// clients show it only at Full): again when the error changes, and again
// when the restore fails after having succeeded. Each warning also goes to
// the daemon log with the delegate and the full error, so the cause is
// recoverable where the transcript hides it.
//
// Not parallel: it swaps the process's default slog handler to read the log.
func TestDelegateAttentionRestoreWarnsOncePerFailureEpisode(t *testing.T) {
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	client := llm.NewClient()
	profile := testOpenAICompatProfile("restore-warning", "warning-model", 0)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	busy := errors.New("delegate runtime is busy")
	gone := errors.New("delegate transcript is missing")
	sess.warnDelegateAttentionRestoreFailed("d1", busy)
	sess.warnDelegateAttentionRestoreFailed("d1", busy)
	sess.warnDelegateAttentionRestoreFailed("d2", busy)
	sess.warnDelegateAttentionRestoreFailed("d1", gone)
	sess.warnDelegateAttentionRestoreFailed("d1", gone)
	sess.delegateAttentionRestored("d1")
	sess.warnDelegateAttentionRestoreFailed("d1", gone)
	sess.Close()

	var messages []string
	for _, warning := range warningEvents(<-eventsDone) {
		if warning.Code != events.WarningCodeDelegateAttentionRestore {
			t.Fatalf("restore warning code = %q, want %q", warning.Code, events.WarningCodeDelegateAttentionRestore)
		}
		messages = append(messages, warning.Message)
	}
	want := []string{
		"restore delegate attention: delegate runtime is busy",
		"restore delegate attention: delegate runtime is busy",
		"restore delegate attention: delegate transcript is missing",
		"restore delegate attention: delegate transcript is missing",
	}
	if !slices.Equal(messages, want) {
		t.Fatalf("warned %q, want %q: once per delegate's failure episode, again when its error changes or it fails after a restore", messages, want)
	}

	var logLines []string
	for line := range strings.SplitSeq(logged.String(), "\n") {
		if strings.Contains(line, "restore delegate attention failed") {
			logLines = append(logLines, line)
		}
	}
	wantLogged := []string{"delegate=d1 error=\"delegate runtime is busy\"", "delegate=d2 error=\"delegate runtime is busy\"", "delegate=d1 error=\"delegate transcript is missing\"", "delegate=d1 error=\"delegate transcript is missing\""}
	if len(logLines) != len(wantLogged) {
		t.Fatalf("daemon log lines = %q, want one per warning: %q", logLines, wantLogged)
	}
	for i, want := range wantLogged {
		if !strings.Contains(logLines[i], want) || !strings.Contains(logLines[i], "level=WARN") {
			t.Fatalf("daemon log line %d = %q, want a WARN naming %s", i, logLines[i], want)
		}
	}
}
