package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// watchChannelNotices filters the owner queue down to the frames a watch put on
// the watch channel; the target's own terminal job notification rides the same
// queue but is a different channel (and does not exist for a cross-session
// watcher at all).
func watchChannelNotices(notified []jobNotification) []jobNotification {
	var out []jobNotification
	for _, n := range notified {
		if n.Status == jobNotificationEventWatch {
			out = append(out, n)
		}
	}
	return out
}

// A watch whose condition never matched must not end in silence. The watching
// session has nothing else to wake on, so it reads as quiesced while it waits
// for a match that can no longer happen.
func TestWatchEndNoticeOnTerminalTargetForNeverFiredWatch(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }
	rec, _ := jm.createShell(createShellOpts{Command: "go test ./..."})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(FAIL|ok  |PASS)"}); err != nil {
		t.Fatalf("install: %v", err)
	}

	exit := -1
	if err := jm.finalize(rec.JobID, jobstore.StatusStopped, "run_timeout", &exit); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	notices := watchChannelNotices(notified)
	if len(notices) != 1 {
		t.Fatalf("watch-channel notices = %d (%+v), want exactly one end notice", len(notices), notices)
	}
	if notices[0].JobID != rec.JobID {
		t.Errorf("end notice job_id = %q, want %q", notices[0].JobID, rec.JobID)
	}
	for _, want := range []string{"watch ended", "status=stopped", "reason=run_timeout", "output_bytes=0", "condition never matched"} {
		if !strings.Contains(notices[0].Reason, want) {
			t.Errorf("end notice missing %q; got %q", want, notices[0].Reason)
		}
	}
}

// A cross-session watcher has no owner-side backstop: the target's job-stopped
// notification goes to the job's owner, not to send_to. Without an end notice
// on the watch channel the send_to target hears nothing, ever.
func TestWatchEndNoticeReachesCrossSessionSendTarget(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec, _ := jm.createShell(createShellOpts{Command: "go test ./..."})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "(FAIL|ok  |PASS)",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "tell me when the tests land"},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}

	exit := -1
	if err := jm.finalize(rec.JobID, jobstore.StatusStopped, "run_timeout", &exit); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending watch sends = %d (%+v), want exactly one end notice", len(pending), pending)
	}
	var sent []sendMessageArgs
	if err := drainWatchSendsVia(t, jm, func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("deliveries to send target = %d (%+v), want exactly one end notice", len(sent), sent)
	}
	if sent[0].Target != "dlg_obs" {
		t.Errorf("end notice target = %q, want dlg_obs", sent[0].Target)
	}
	for _, want := range []string{"Watch frame", "watch ended", "status=stopped", "reason=run_timeout", "output_bytes=0", "condition never matched"} {
		if !strings.Contains(sent[0].Message, want) {
			t.Errorf("end notice frame missing %q; got:\n%s", want, sent[0].Message)
		}
	}
}

// The end notice is for watches that ended unheard. A watch that already
// delivered has told its watcher everything it promised, so a trailing notice
// would be pure noise.
func TestWatchThatFiredGetsNoEndNoticeOnTerminalTarget(t *testing.T) {
	t.Parallel()
	t.Run("notification watch", func(t *testing.T) {
		t.Parallel()
		jm := newTestJM(t)
		var notified []jobNotification
		jm.enqueue = func(n jobNotification) { notified = append(notified, n) }
		rec, _ := jm.createShell(createShellOpts{Command: "x"})
		if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
			t.Fatalf("install: %v", err)
		}
		feedJob(jm, rec.JobID, []byte("server ready\n"))
		code := 0
		if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
			t.Fatalf("finalize: %v", err)
		}

		notices := watchChannelNotices(notified)
		if len(notices) != 1 {
			t.Fatalf("watch-channel notices = %d (%+v), want only the match", len(notices), notices)
		}
		if !strings.HasPrefix(notices[0].Reason, "output_match: ") {
			t.Errorf("notice = %q, want only the output_match fire", notices[0].Reason)
		}
	})

	t.Run("send watch with an in-flight delivery", func(t *testing.T) {
		t.Parallel()
		jm := newTestJM(t)
		seedCommonWatchSendTargets(t, jm)
		rec, _ := jm.createShell(createShellOpts{Command: "x"})
		if _, err := jm.configureWatch(watchArgs{
			Target:      rec.JobID,
			OutputMatch: "ready",
			Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
		}); err != nil {
			t.Fatalf("install: %v", err)
		}
		// Fired but undelivered: the frame is pending, so the watch has already
		// spoken even though nothing has settled yet.
		feedJob(jm, rec.JobID, []byte("server ready\n"))
		code := 0
		if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
			t.Fatalf("finalize: %v", err)
		}

		var sent []sendMessageArgs
		if err := drainWatchSendsVia(t, jm, func(_ context.Context, a sendMessageArgs) sendMessageResult {
			sent = append(sent, a)
			return sendMessageResult{}
		}); err != nil {
			t.Fatalf("drain: %v", err)
		}
		if len(sent) != 1 {
			t.Fatalf("deliveries to send target = %d (%+v), want only the match", len(sent), sent)
		}
		if !strings.Contains(sent[0].Message, "output_match: server ready") {
			t.Errorf("delivery = %q, want only the output_match fire", sent[0].Message)
		}
	})
}

// The end notice is teardown, not a condition fire. recent_watches deliveries
// answers "did this watch's condition ever produce anything", which is exactly
// the zero the notice exists to explain; counting the notice would erase it.
func TestWatchEndNoticeIsNotCountedAsADelivery(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	hist := jm.recentWatchSummaries()
	if len(hist) != 1 {
		t.Fatalf("recent_watches = %+v, want one entry", hist)
	}
	if hist[0].Deliveries != 0 {
		t.Errorf("recent_watches deliveries = %d, want 0 (the end notice is not a condition fire)", hist[0].Deliveries)
	}
}
