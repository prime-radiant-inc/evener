package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
)

// parentSourceWatchArgs is the shape installParentSourceWatchForChild builds
// for a delegate(watch_parent=true) child: a caller-target watch on the
// PARENT's session stream whose deliveries route to the observer's delegate
// job. It is the only model-reachable configuration that carries an internal
// send target, so it is the only configuration that can mint a read grant.
func parentSourceWatchArgs(observerSessionID, observerDelegateID string, eventKinds ...string) watchArgs {
	return watchArgs{
		Source:             "parent",
		Target:             runtimeMessageAliasCaller,
		Events:             eventKinds,
		ReceiverSessionID:  observerSessionID,
		ReceiverDelegateID: observerDelegateID,
	}
}

// TestJobNotificationDeliveryMintsObserverReadGrant is the reachable mint
// (spec §5.1): a parent-source watch delivering a job.notification frame
// grants the receiving observer SESSION a read on the concrete job the event
// payload names. The observer never named that job — the watched session's own
// delivery did.
func TestJobNotificationDeliveryMintsObserverReadGrant(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)

	if _, err := jm.configureWatch(parentSourceWatchArgs("child_job_obs", "dlg_obs", "job.notification")); err != nil {
		t.Fatalf("configure parent-source watch: %v", err)
	}
	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{
		JobID: "job_worker", JobType: "delegate", Status: "completed", DelegateID: "dlg_worker",
	})

	grants := loadGrantTable(t, jm)
	if !grants["child_job_obs"]["job_worker"] {
		t.Fatalf("grants after job.notification delivery = %+v, want child_job_obs -> job_worker", grants)
	}
	if got := countWatchReadGrantEvents(t, jm); got != 1 {
		t.Fatalf("grant events = %d, want 1 (one delivery mints once)", got)
	}

	// The per-fire dedup keys on (send target, watched job): a second delivery
	// naming the same job appends no second grant event. Duplicates would fold
	// harmlessly, so this is append-noise control, not correctness.
	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{
		JobID: "job_worker", JobType: "delegate", Status: "failed", DelegateID: "dlg_worker",
	})
	if got := countWatchReadGrantEvents(t, jm); got != 1 {
		t.Fatalf("grant events after a repeat delivery = %d, want 1", got)
	}
}

// TestNonJobEventDeliveryMintsNoReadGrant pins spec non-goal 3: communicate and
// assistant.tool payloads carry no structured job reference, so a delivery of
// either grants nothing. Parsing a job id out of their text would be a guess,
// and a capability minted from a guess is worse than no capability.
func TestNonJobEventDeliveryMintsNoReadGrant(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)

	if _, err := jm.configureWatch(parentSourceWatchArgs("child_job_obs", "dlg_obs", "communicate", "assistant.tool")); err != nil {
		t.Fatalf("configure parent-source watch: %v", err)
	}
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "job_worker finished"})
	onSessionEventKD(jm, events.EventToolCallEnd, events.ToolCallEndData{ToolName: "shell", Output: "job_worker"})

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) == 0 {
		t.Fatal("no frames were delivered; the mint assertion below would be vacuous")
	}
	if grants := loadGrantTable(t, jm); len(grants) != 0 {
		t.Fatalf("grants after non-job frames = %+v, want none", grants)
	}
}

// TestWatchSendGrantSkipsReceiverOwnDelegateJob is Jesse's ruling 5: a
// source:"parent" watch on job.notification fires for EVERY job the parent
// completes, including the observer's own resumed callback jobs. Granting the
// observer a read on its own output confers nothing, and grants are never
// revoked (spec §5.1), so an accepted self-grant row would be permanent junk
// and would surface the observer as its own observer in the hub's grant
// projection. Two cases: the receiver's own delegate job is skipped, any other
// delegate's completion still mints.
func TestWatchSendGrantSkipsReceiverOwnDelegateJob(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)

	if _, err := jm.configureWatch(parentSourceWatchArgs("child_job_obs", "dlg_obs", "job.notification")); err != nil {
		t.Fatalf("configure parent-source watch: %v", err)
	}

	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{
		JobID: "job_obs_round_one", JobType: "delegate", Status: "completed", DelegateID: "dlg_obs",
	})
	if grants := loadGrantTable(t, jm); len(grants) != 0 {
		t.Fatalf("grants after the observer's own callback job = %+v, want none", grants)
	}

	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{
		JobID: "job_worker", JobType: "delegate", Status: "completed", DelegateID: "dlg_worker",
	})
	grants := loadGrantTable(t, jm)
	if !grants["child_job_obs"]["job_worker"] {
		t.Fatalf("grants after another delegate's completion = %+v, want child_job_obs -> job_worker", grants)
	}
	if grants["child_job_obs"]["job_obs_round_one"] {
		t.Fatalf("self-grant appeared after the second fire: %+v", grants)
	}
}

// TestJobNotificationFrameNamesTheGrantedRead: the frame is the observer's only
// teaching surface for a capability it acquires mid-run, so a delivery that
// minted a grant names the one call that spends it. A delivery that minted
// nothing must not advertise a read it cannot make.
func TestJobNotificationFrameNamesTheGrantedRead(t *testing.T) {
	t.Parallel()
	const wantLine = `read with: read_transcript(transcript_ref="job:job_worker")`

	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	if _, err := jm.configureWatch(parentSourceWatchArgs("child_job_obs", "dlg_obs", "job.notification")); err != nil {
		t.Fatalf("configure parent-source watch: %v", err)
	}
	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{
		JobID: "job_worker", JobType: "delegate", Status: "completed", DelegateID: "dlg_worker",
	})
	if frame := onlyPendingWatchFrame(t, jm); !strings.Contains(frame, wantLine) {
		t.Fatalf("granted frame = %q, want it to contain %q", frame, wantLine)
	}

	ungranted := newTestJM(t)
	seedCommonWatchSendTargets(t, ungranted)
	if _, err := ungranted.configureWatch(parentSourceWatchArgs("child_job_obs", "dlg_obs", "job.notification")); err != nil {
		t.Fatalf("configure ungranted parent-source watch: %v", err)
	}
	onSessionEventKD(ungranted, events.EventJobFinished, events.JobFinishedData{
		JobID: "job_obs_round_one", JobType: "delegate", Status: "completed", DelegateID: "dlg_obs",
	})
	if frame := onlyPendingWatchFrame(t, ungranted); strings.Contains(frame, "read_transcript") {
		t.Fatalf("self-grant-skipped frame = %q, want no read_transcript line", frame)
	}
}

func onlyPendingWatchFrame(t *testing.T, jm *jobManager) string {
	t.Helper()
	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending watch sends = %d, want exactly 1", len(pending))
	}
	for _, state := range pending {
		return state.Frame
	}
	return ""
}
