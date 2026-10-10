package agent

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

func delegateAttentionWarningMessages(t *testing.T, captured []events.SessionEvent) []string {
	t.Helper()
	var messages []string
	for _, warning := range warningEvents(captured) {
		if warning.Code == events.WarningCodeDelegateAttentionRestore {
			messages = append(messages, warning.Message)
		}
	}
	return messages
}

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
	sess.warnDelegateAttentionFailed(delegateAttentionRestoreLabel, "d1", busy)
	sess.warnDelegateAttentionFailed(delegateAttentionRestoreLabel, "d1", busy)
	sess.warnDelegateAttentionFailed(delegateAttentionRestoreLabel, "d2", busy)
	sess.warnDelegateAttentionFailed(delegateAttentionRestoreLabel, "d1", gone)
	sess.warnDelegateAttentionFailed(delegateAttentionRestoreLabel, "d1", gone)
	sess.delegateAttentionWarningResolved(delegateAttentionRestoreLabel, "d1")
	sess.warnDelegateAttentionFailed(delegateAttentionRestoreLabel, "d1", gone)
	sess.Close()

	messages := delegateAttentionWarningMessages(t, <-eventsDone)
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
		if strings.Contains(line, "delegate attention failed") {
			logLines = append(logLines, line)
		}
	}
	wantLogged := []string{"delegate=d1 error=\"delegate runtime is busy\"", "delegate=d2 error=\"delegate runtime is busy\"", "delegate=d1 error=\"delegate transcript is missing\"", "delegate=d1 error=\"delegate transcript is missing\""}
	if len(logLines) != len(wantLogged) {
		t.Fatalf("daemon log lines = %q, want one per warning: %q", logLines, wantLogged)
	}
	for i, want := range wantLogged {
		if !strings.Contains(logLines[i], want) || !strings.Contains(logLines[i], "level=WARN") || !strings.Contains(logLines[i], "action=\"restore delegate attention\"") {
			t.Fatalf("daemon log line %d = %q, want a restore WARN naming %s", i, logLines[i], want)
		}
	}
}

// The message carries the error's first line, at most 512 bytes of it, so
// one pathological error can't fill the transcript; the daemon log keeps the
// whole error. Not parallel: it swaps the default slog handler.
func TestDelegateAttentionWarningBoundsItsErrorText(t *testing.T) {
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	sess, err := NewSession(llm.NewClient(), testOpenAICompatProfile("bound-warning", "warning-model", 0),
		execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	long := strings.Repeat("é", 400) // 800 bytes, two per rune
	sess.warnDelegateAttentionFailed(delegateAttentionRestoreLabel, "d1", errors.New(long+"\nsecond line"))
	sess.Close()

	messages := delegateAttentionWarningMessages(t, <-eventsDone)
	if len(messages) != 1 {
		t.Fatalf("warnings = %q, want one", messages)
	}
	cause, _ := strings.CutPrefix(messages[0], "restore delegate attention: ")
	if cause != strings.Repeat("é", 256) {
		t.Fatalf("message cause = %d bytes %q..., want the first line cut to 512 bytes on a rune boundary", len(cause), cause[:min(len(cause), 20)])
	}
	if !strings.Contains(logged.String(), "second line") {
		t.Fatalf("daemon log = %q, want the whole error", logged.String())
	}
}

// A delegate's episode ends when its attention leaves the pending set
// (delivered, retired, escalated or fenced), not only on a successful
// restore: attention owed again later with the same error is news again.
func TestDelegateAttentionWarningEpisodeEndsWhenTheAttentionIsNoLongerOwed(t *testing.T) {
	t.Parallel()
	sess, err := NewSession(llm.NewClient(), testOpenAICompatProfile("episode-warning", "warning-model", 0),
		execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	controller := &delegateTreeController{attention: map[string]*delegateAttentionState{"d1": {wakeIDs: map[string]struct{}{"a1": {}}}}}
	sess.delegateController = controller
	busy := errors.New("delegate runtime is busy")

	sess.warnDelegateAttentionFailed(delegateAttentionRestoreLabel, "d1", busy)
	sess.forgetSettledDelegateAttentionWarnings()
	sess.warnDelegateAttentionFailed(delegateAttentionRestoreLabel, "d1", busy) // still owed: same episode
	controller.forgetDelegateAttention("d1", "a1")
	sess.forgetSettledDelegateAttentionWarnings()
	controller.mu.Lock()
	controller.replaceDelegateAttentionLocked("d1", []string{"a2"})
	controller.mu.Unlock()
	sess.warnDelegateAttentionFailed(delegateAttentionRestoreLabel, "d1", busy) // owed again: a new episode
	sess.delegateController = nil
	sess.Close()

	want := []string{"restore delegate attention: delegate runtime is busy", "restore delegate attention: delegate runtime is busy"}
	if messages := delegateAttentionWarningMessages(t, <-eventsDone); !slices.Equal(messages, want) {
		t.Fatalf("warned %q, want %q: once, then again once the attention was owed anew", messages, want)
	}
}

// Escalating fenced attention to the root runs from the same retry loop, so
// a failing escalation flooded the same way: it warns once per delegate's
// episode too, with its cause and the same code.
func TestFailedEscalationOfFencedAttentionWarnsOncePerEpisode(t *testing.T) {
	fenced := newFencedGrandchildAttention(t)
	root, fixture := fenced.root, fenced.fixture
	fenced.closeParent(t)
	// The grandchild's transcript turns into a directory, so every
	// escalation attempt fails to read the attention it owes.
	path := transcriptPath(fixture.stateDir, fenced.grandchildSessionID)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove grandchild transcript: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("replace grandchild transcript with a directory: %v", err)
	}
	eventsDone := captureSessionEvents(root)

	root.drivePendingStableDelegateAttention()
	root.drivePendingStableDelegateAttention()
	root.drivePendingStableDelegateAttention()
	root.Close()

	messages := delegateAttentionWarningMessages(t, <-eventsDone)
	if len(messages) != 1 || !strings.HasPrefix(messages[0], "escalate unreachable delegate attention: ") {
		t.Fatalf("escalation warnings = %q, want one naming its cause", messages)
	}
}
