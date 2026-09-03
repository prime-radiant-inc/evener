package agent

import (
	"strings"
	"sync"
	"testing"
)

func TestConfigureWatch_TimersCoexistAsSeparateWatches(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	a, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 300, Note: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 300, Note: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if a.WatchID == b.WatchID || b.ReplacedExisting {
		t.Fatalf("identical timer creates must be two watches: %+v %+v", a, b)
	}
	ev, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", Events: []string{"assistant.tool"}})
	if err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	live := len(jm.watches)
	jm.mu.Unlock()
	if live != 3 || ev.ReplacedExisting {
		t.Fatalf("timers must not collide with a self event watch: live=%d ev=%+v", live, ev)
	}
}

func TestConfigureWatch_TimerCapIsEightPerManager(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	for i := range maxLiveTimers {
		if _, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 60}); err != nil {
			t.Fatalf("timer %d: %v", i+1, err)
		}
	}
	_, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 60})
	if err == nil || !strings.Contains(err.Error(), "too many timers (8 live); clear one first") {
		t.Fatalf("ninth timer: err = %v", err)
	}
}

func TestConfigureWatch_ConcurrentNinthTimersBothFail(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	for range maxLiveTimers - 1 {
		if _, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 60}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", RepeatSeconds: 60})
		})
	}
	wg.Wait()
	failures := 0
	for _, err := range errs {
		if err != nil {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("exactly one of two concurrent creates at the cap may succeed; failures=%d errs=%v", failures, errs)
	}
}

func TestWatchKeyMatchesClearRequest_SlotIsExact(t *testing.T) {
	t.Parallel()
	timer := watchKey{VisibleSessionID: "s", Target: "caller", Slot: "w1"}
	request := watchKey{VisibleSessionID: "s", Target: "caller"}
	if watchKeyMatchesClearRequest(timer, request) {
		t.Fatal("a slot-less clear request must not match a timer")
	}
	if !watchKeyMatchesClearRequest(timer, watchKey{VisibleSessionID: "s", Target: "caller", Slot: "w1"}) {
		t.Fatal("an exact slot must match")
	}
}

func TestConfigureWatch_TimerCreateDoesNotSweepOtherWatches(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	if _, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", Events: []string{"assistant.tool"}}); err != nil {
		t.Fatal(err)
	}
	res, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: "caller", AfterSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if res.ReplacedExisting {
		t.Fatal("a timer create must never report replacing the self event watch")
	}
}
