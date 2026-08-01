package doctor

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

const (
	endNoticeWatchID = "watch_02wMz5TxvEMoJEDTDGOTa4"
	endNoticeJobID   = "job_02wMz5TxvEMoJEDTDGOTb4"
	endNoticeGen     = "wg_02wMz5TxvEMoJEDTDGOTc4"
	firedWatchID     = "watch_02wMz5TxvEMoJEDTDGOTa5"
	firedJobID       = "job_02wMz5TxvEMoJEDTDGOTb5"
	firedGen         = "wg_02wMz5TxvEMoJEDTDGOTc5"
	endNoticeReason  = "watch ended: " + endNoticeJobID +
		" is terminal (status=stopped reason=run_timeout output_bytes=0); condition never matched"
)

// endNoticeFixture reproduces what the runtime writes now that a watch which
// never fired says so before it ends: the end notice rides the send rail like
// any other frame (two pending lines, one delivered) and is marked as the
// teardown frame it is. Next to it sits a watch that actually matched, so the
// report has to tell the two apart instead of counting them the same.
func endNoticeFixture(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid = sidA
	writeSession(t, bucket, sid)
	exitTimeout := -1

	endNoticeKey := jobstore.WatchSendKey{
		VisibleSessionID: sidA, WatchID: endNoticeWatchID, WatchTarget: endNoticeJobID,
		ResolvedWatchedIdentity: endNoticeJobID, ResolvedSendTo: "dlg_obs", WatchGeneration: endNoticeGen,
	}
	firedKey := jobstore.WatchSendKey{
		VisibleSessionID: sidA, WatchID: firedWatchID, WatchTarget: firedJobID,
		ResolvedWatchedIdentity: firedJobID, ResolvedSendTo: "dlg_obs", WatchGeneration: firedGen,
	}

	writeJobsEvents(t, filepath.Join(bucket, "sessions", sid, "jobs.jsonl"), []jobstore.Event{
		{Kind: jobstore.EventJobStarted, JobID: endNoticeJobID, Type: jobstore.JobShell,
			Command: "go test ./...", OwnerSessionID: sidA, VisibleToSession: sidA, StartedAt: &jobStartedAt},
		{Kind: jobstore.EventJobFinished, JobID: endNoticeJobID, Status: jobstore.StatusStopped,
			Reason: "run_timeout", ExitCode: &exitTimeout, EndedAt: &jobEndedAt, OutputBytes: 0, TerminalGen: "tg_end"},
		{Kind: jobstore.EventWatchRegistered, WatchID: endNoticeWatchID, Watch: &jobstore.WatchEvent{
			Generation: endNoticeGen, OwnerSessionID: sidA, VisibleSessionID: sidA,
			Target: endNoticeJobID, SendTo: "dlg_obs", Condition: "output_match: (FAIL|ok  |PASS)", ConfigHash: "cfg_end"}},
		{Kind: jobstore.EventWatchCleared, WatchID: endNoticeWatchID, Watch: &jobstore.WatchEvent{
			Generation: endNoticeGen, EndReason: "auto_removed_terminal"}},
		{Kind: jobstore.EventWatchSendPending, WatchID: endNoticeWatchID, WatchSend: &jobstore.WatchSendState{
			Key: endNoticeKey, DeliveryID: "wd_end", UpdateSeq: 1, EndNotice: true,
			TriggerIdentity: endNoticeJobID, TriggerReason: endNoticeReason}},
		{Kind: jobstore.EventWatchSendPending, WatchID: endNoticeWatchID, WatchSend: &jobstore.WatchSendState{
			Key: endNoticeKey, DeliveryID: "wd_end", UpdateSeq: 1, EndNotice: true,
			TriggerIdentity: endNoticeJobID, TriggerReason: endNoticeReason}},
		{Kind: jobstore.EventWatchSendDelivered, WatchID: endNoticeWatchID, WatchSend: &jobstore.WatchSendState{
			Key: endNoticeKey, DeliveryID: "wd_end", UpdateSeq: 1, EndNotice: true,
			TriggerIdentity: endNoticeJobID, TriggerReason: endNoticeReason}},

		{Kind: jobstore.EventJobStarted, JobID: firedJobID, Type: jobstore.JobShell,
			Command: "npm run dev", OwnerSessionID: sidA, VisibleToSession: sidA, StartedAt: &jobStartedAt},
		{Kind: jobstore.EventWatchRegistered, WatchID: firedWatchID, Watch: &jobstore.WatchEvent{
			Generation: firedGen, OwnerSessionID: sidA, VisibleSessionID: sidA,
			Target: firedJobID, SendTo: "dlg_obs", Condition: "output_match: ready", ConfigHash: "cfg_fired"}},
		{Kind: jobstore.EventWatchSendPending, WatchID: firedWatchID, WatchSend: &jobstore.WatchSendState{
			Key: firedKey, DeliveryID: "wd_fired", UpdateSeq: 1,
			TriggerIdentity: firedJobID, TriggerReason: "output_match: server ready"}},
		{Kind: jobstore.EventWatchSendDelivered, WatchID: firedWatchID, WatchSend: &jobstore.WatchSendState{
			Key: firedKey, DeliveryID: "wd_fired", UpdateSeq: 1,
			TriggerIdentity: firedJobID, TriggerReason: "output_match: server ready"}},
	})
	return base, sid
}

// The zero that started the 2026-07-31 diagnosis has to survive the end notice.
// "deliveries" answers "did this watch's condition ever produce anything", and a
// teardown frame the watch sent to say the condition NEVER matched must not be
// the thing that makes the answer look like yes.
func TestWatches_EndNoticeIsNotCountedAsADelivery(t *testing.T) {
	base, sid := endNoticeFixture(t)
	r, err := Watches(base, sid, WatchOpts{})
	if err != nil {
		t.Fatalf("Watches: %v", err)
	}

	w := findWatch(r, endNoticeWatchID)
	if w == nil {
		t.Fatalf("%s missing from report", endNoticeWatchID)
	}
	if w.DistinctDeliveries != 0 {
		t.Errorf("DistinctDeliveries = %d, want 0 — the watch never fired", w.DistinctDeliveries)
	}
	if w.Delivered != 0 {
		t.Errorf("Delivered = %d, want 0 — the only settled frame is the end notice", w.Delivered)
	}
	if w.EndNotices != 1 {
		t.Errorf("EndNotices = %d, want 1", w.EndNotices)
	}
	if w.PendingLines != 2 {
		t.Errorf("PendingLines = %d, want 2 (the raw pending events are unchanged)", w.PendingLines)
	}
	if len(w.Deliveries) != 1 || !w.Deliveries[0].EndNotice {
		t.Fatalf("delivery list = %+v, want the end notice kept as evidence and marked", w.Deliveries)
	}
	if w.Deliveries[0].Terminal != "delivered" {
		t.Errorf("end notice terminal = %q, want delivered", w.Deliveries[0].Terminal)
	}

	fired := findWatch(r, firedWatchID)
	if fired == nil {
		t.Fatalf("%s missing from report", firedWatchID)
	}
	if fired.DistinctDeliveries != 1 || fired.Delivered != 1 {
		t.Errorf("fired watch = %d distinct / %d delivered, want 1/1", fired.DistinctDeliveries, fired.Delivered)
	}
	if fired.EndNotices != 0 {
		t.Errorf("fired watch EndNotices = %d, want 0", fired.EndNotices)
	}
}

// The summary-by-default view is what the doctoring workflow reads first, so the
// distinction has to be legible there and not only in the verbose delivery list.
func TestWatches_RenderSeparatesEndNoticeFromDeliveries(t *testing.T) {
	base, sid := endNoticeFixture(t)
	r, _ := Watches(base, sid, WatchOpts{WatchID: endNoticeWatchID})
	out := RenderWatches(r)

	for _, want := range []string{
		"(ended: auto_removed_terminal)",
		"deliveries: 0 distinct",
		"end notices: 1",
		"[end notice]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "deliveries: 1 distinct") {
		t.Errorf("render counts the end notice as a delivery; got:\n%s", out)
	}
}
