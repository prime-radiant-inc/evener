package agent

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
)

func TestConfigureWatchRequiresCondition(t *testing.T) {
	jm := newTestJM(t)
	_, err := jm.configureWatch(watchArgs{Target: "caller"})
	if err == nil {
		t.Fatal("a watch with no condition and clear=false must error")
	}
}

func TestConfigureWatchTargetNotFound(t *testing.T) {
	jm := newTestJM(t)
	_, err := jm.configureWatch(watchArgs{Target: "job_does_not_exist", OutputMatch: "ready"})
	if err == nil {
		t.Fatal("an unknown concrete job target must error (target_not_found)")
	}
}

func TestConfigureWatchIdempotentAndReplace(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	first, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if first.ReplacedExisting {
		t.Error("first watch must not report replaced_existing")
	}

	same, _ := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if same.ReplacedExisting {
		t.Error("identical re-config must be idempotent, not a replacement")
	}

	diff, _ := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "blocked"})
	if !diff.ReplacedExisting {
		t.Error("changed config on the same key must report replaced_existing")
	}
}

func TestClearWatchRemovesIt(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, _ = jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if jm.watchCount() != 0 {
		t.Errorf("clear must remove the watch; count = %d", jm.watchCount())
	}
}

func TestEventWatchFiresAndNotifiesCaller(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	if _, err := jm.configureWatch(watchArgs{Target: "caller", Events: []string{"assistant.message"}}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.onSessionEvent(events.EventAssistantTextEnd, nil)

	if len(notified) != 1 {
		t.Fatalf("an assistant.message event must notify the caller once, got %d", len(notified))
	}
}

func TestEventWatchTriggerEveryNth(t *testing.T) {
	jm := newTestJM(t)
	var fires int
	jm.enqueue = func(jobNotification) { fires++ }

	_, err := jm.configureWatch(watchArgs{
		Target:       "caller",
		Events:       []string{"assistant.message"},
		TriggerEvent: "assistant.message",
		TriggerEvery: 3,
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	for i := 0; i < 7; i++ {
		jm.onSessionEvent(events.EventAssistantTextEnd, nil)
	}
	if fires != 2 {
		t.Errorf("trigger.every=3 over 7 events should fire twice, got %d", fires)
	}
}

func TestEventWatchIgnoresUnwatchedKind(t *testing.T) {
	jm := newTestJM(t)
	var fires int
	jm.enqueue = func(jobNotification) { fires++ }
	_, _ = jm.configureWatch(watchArgs{Target: "caller", Events: []string{"assistant.message"}})
	jm.onSessionEvent(events.EventToolCallEnd, nil)
	if fires != 0 {
		t.Errorf("an unwatched event kind must not fire; fires = %d", fires)
	}
}

func TestOutputMatchWatchFiresOnAppendedBytes(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(?i)ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("booting\nserver READY\n")); err != nil {
		t.Fatalf("append: %v", err)
	}

	if len(notified) != 1 {
		t.Fatalf("output_match must fire once on the matching appended line, got %d", len(notified))
	}
}

func TestProgressTimerFiresPeriodically(t *testing.T) {
	jm := newTestJM(t)
	fired := make(chan struct{}, 4)
	jm.enqueue = func(jobNotification) { fired <- struct{}{} }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, ProgressIntervalMS: 1000}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("progress timer did not fire within 3s")
	}
	_, _ = jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true})
}

func TestProgressTimerStopsOnClose(t *testing.T) {
	jm := newTestJM(t)
	fired := make(chan struct{}, 16)
	jm.enqueue = func(jobNotification) { fired <- struct{}{} }

	if _, err := jm.configureWatch(watchArgs{Target: "caller", ProgressIntervalMS: 10}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("progress timer did not fire before close")
	}
	if err := jm.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	for {
		select {
		case <-fired:
		default:
			goto drained
		}
	}

drained:
	if count := jm.watchCount(); count != 0 {
		t.Fatalf("close must remove watches; count = %d", count)
	}
	select {
	case <-fired:
		t.Fatal("progress timer fired after close")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatchEventKindNamesResolve(t *testing.T) {
	if len(WatchEventKindNames) != len(modelEventKinds) {
		t.Fatalf("WatchEventKindNames has %d names, modelEventKinds has %d", len(WatchEventKindNames), len(modelEventKinds))
	}
	for _, name := range WatchEventKindNames {
		if _, ok := modelEventKinds[name]; !ok {
			t.Errorf("WatchEventKindNames includes unresolved event kind %q", name)
		}
	}
}

var _ = jobstore.JobShell
