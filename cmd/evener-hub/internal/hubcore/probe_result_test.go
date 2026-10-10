package hubcore

import (
	"context"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// The spawned-thread read and the full probe build a ProbeResult from one
// thread row, so every field a row reads from that row must survive both
// (#2962). These are the fields the probe already carried and the spawned read
// used to drop: its ProbeResult literal set a different subset.
func TestRosterReadSpawnedThreadCarriesTheThreadRowsFields(t *testing.T) {
	ended := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	tally := appwire.SubagentTally{Running: 1, Failed: 1, Done: 2}
	activity := &appwire.ThreadActivity{Minutes: []int{1, 2, 3}, LastActivityAt: ended.UnixMilli()}
	tasks := taskProgress(3, 1, 0, &appwire.TaskSummary{ID: 2, Description: "Resume the migration"})
	thread := appwire.Thread{
		ID: "01SPAWNED", SessionID: "01SPAWNED", ModelProvider: "gpt-5.6",
		Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive, ActiveFlags: []string{"resumeRequired"}},
		Evener: appwire.EvenerThread{
			AskPending:      true,
			Profile:         "codex-jesse-fsck.com",
			LastMessage:     "the opening line",
			Tasks:           tasks,
			Activity:        activity,
			Subagents:       &tally,
			LastTurnEndedAt: ended.UnixMilli(),
		},
	}
	r, entry := newSpawnedRoster(t)
	if _, err := r.ReadSpawnedThread(t.Context(), entry, func(context.Context) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: thread}, nil
	}); err != nil {
		t.Fatal(err)
	}
	live, ok := r.Find("01SPAWNED")
	if !ok {
		t.Fatal("the confirmed daemon was not published into the roster")
	}
	if live.Subagents != tally {
		t.Errorf("published subagent tally = %+v, want the read's %+v", live.Subagents, tally)
	}
	if !reflect.DeepEqual(live.Activity, activity) {
		t.Errorf("published activity = %+v, want the read's %+v", live.Activity, activity)
	}
	if !live.LastTurnEndedAt.Equal(ended) {
		t.Errorf("published last turn ended = %v, want the read's %v", live.LastTurnEndedAt, ended)
	}
	// The fields the read path already carried must keep surviving it too.
	if !reflect.DeepEqual(live.Tasks, tasks) || live.Profile != "codex-jesse-fsck.com" ||
		live.LastMessage != "the opening line" || live.CurrentModel != "gpt-5.6" ||
		live.Status != appwire.ThreadStatusActive || !live.PendingAsk ||
		!reflect.DeepEqual(live.ActiveFlags, []string{"resumeRequired"}) {
		t.Fatalf("published entry = %+v, want every field the read's thread row carried", live)
	}
	// The full probe builds its ProbeResult with this same constructor, so the
	// two paths agree on every field the thread row carries (#2962).
	want := ProbeResultFromThread(thread)
	if want.Subagents != live.Subagents || !reflect.DeepEqual(want.Activity, live.Activity) ||
		!want.LastTurnEndedAt.Equal(live.LastTurnEndedAt) {
		t.Fatalf("probe result %+v and spawned read %+v disagree on the thread row's fields", want, live)
	}
}
