package jobstore

import "testing"

func TestApplyPreservesTerminalAndWatchFold(t *testing.T) {
	t.Parallel()
	events := []Event{
		{Seq: 1, Kind: EventJobStarted, JobID: "job_one", Type: JobShell},
		{Seq: 2, Kind: EventJobFinished, JobID: "job_one", Status: StatusCompleted, TerminalGen: "first"},
		{Seq: 3, Kind: EventJobFinished, JobID: "job_one", Status: StatusFailed, TerminalGen: "second"},
		{Seq: 4, Kind: EventWatchRegistered, WatchID: "watch_one", Watch: &WatchEvent{Generation: "g", OwnerSessionID: "owner", VisibleSessionID: "owner", Target: "job_one", ConfigHash: "hash", Config: &WatchConfigSnapshot{ReceiverSessionID: "parent"}}},
		{Seq: 5, Kind: EventWatchCleared, WatchID: "watch_one", Watch: &WatchEvent{Generation: "wrong", EndReason: "wrong"}},
		{Seq: 6, Kind: EventWatchCleared, WatchID: "watch_one", Watch: &WatchEvent{Generation: "g", EndReason: "cleared"}},
	}
	jobs := make(map[string]*JobRecord)
	watches := make(map[string]*WatchRecord)
	for _, event := range events {
		Apply(jobs, watches, event)
	}
	if jobs["job_one"].Status != StatusCompleted || jobs["job_one"].TerminalGen != "first" {
		t.Fatalf("terminal authority regressed: %+v", jobs["job_one"])
	}
	if watch := watches["watch_one"]; watch.Active || watch.EndReason != "cleared" || watch.ReceiverSessionID != "parent" {
		t.Fatalf("watch generation or receiver lost: %+v", watch)
	}
}
