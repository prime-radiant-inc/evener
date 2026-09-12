package agent

import (
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// TestLiveWatchStatuses_StructuredFields pins the structured projection for
// each trigger kind. It installs one real watch per case through the same
// configureWatch path job_watch uses, then compares the row
// liveWatchStatuses returns against the fields read straight off the config.
func TestLiveWatchStatuses_StructuredFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		install func(t *testing.T, jm *jobManager) (string, WatchStatusInfo)
	}{
		{
			name: "one-shot timer",
			install: func(t *testing.T, jm *jobManager) (string, WatchStatusInfo) {
				res, err := jm.configureWatch(watchArgs{
					Operation: "create", Source: "self", Target: runtimeMessageAliasCaller,
					AfterSeconds: 600, Note: "wake me",
				})
				if err != nil {
					t.Fatalf("configure one-shot timer: %v", err)
				}
				return res.WatchID, WatchStatusInfo{
					Source:     "self",
					Target:     runtimeMessageAliasCaller,
					Note:       "wake me",
					Cadence:    []WatchCadenceInfo{{Kind: "after", Seconds: 600}},
					Active:     true,
					Deliveries: 0,
				}
			},
		},
		{
			name: "repeating timer",
			install: func(t *testing.T, jm *jobManager) (string, WatchStatusInfo) {
				res, err := jm.configureWatch(watchArgs{
					Operation: "create", Source: "self", Target: runtimeMessageAliasCaller,
					RepeatSeconds: 300,
				})
				if err != nil {
					t.Fatalf("configure repeating timer: %v", err)
				}
				return res.WatchID, WatchStatusInfo{
					Source:  "self",
					Target:  runtimeMessageAliasCaller,
					Cadence: []WatchCadenceInfo{{Kind: "every", Seconds: 300}},
					Active:  true,
				}
			},
		},
		{
			name: "output match",
			install: func(t *testing.T, jm *jobManager) (string, WatchStatusInfo) {
				rec, _ := jm.createShell(createShellOpts{Command: "x"})
				res, err := jm.configureWatch(watchArgs{
					Operation: "create", Target: rec.JobID, OutputMatch: "ready", Note: "deploy gate",
				})
				if err != nil {
					t.Fatalf("configure output match: %v", err)
				}
				return res.WatchID, WatchStatusInfo{
					Source:      rec.JobID,
					Target:      rec.JobID,
					Note:        "deploy gate",
					Cadence:     []WatchCadenceInfo{{Kind: "output"}},
					OutputMatch: "ready",
					Active:      true,
				}
			},
		},
		{
			name: "event watch",
			install: func(t *testing.T, jm *jobManager) (string, WatchStatusInfo) {
				res, err := jm.configureWatch(watchArgs{
					Operation: "create", Source: "self", Target: runtimeMessageAliasCaller,
					Events: []string{"assistant.tool"},
				})
				if err != nil {
					t.Fatalf("configure event watch: %v", err)
				}
				return res.WatchID, WatchStatusInfo{
					Source:  "self",
					Target:  runtimeMessageAliasCaller,
					Cadence: []WatchCadenceInfo{{Kind: "events"}},
					Events:  []string{"assistant.tool"},
					Active:  true,
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			jm := newTestJM(t)
			watchID, want := tc.install(t, jm)
			want.ID = watchID
			want.CreatedAt = frozenTestTime.UTC().Format(time.RFC3339Nano)

			got := jm.liveWatchStatuses()
			if len(got) != 1 {
				t.Fatalf("liveWatchStatuses = %+v, want exactly one row", got)
			}
			if !reflect.DeepEqual(got[0], want) {
				t.Fatalf("liveWatchStatuses[0] = %+v, want %+v", got[0], want)
			}
		})
	}
}

// TestWatchConditionSummaryAndListUnchanged proves the structured projection
// left job_list's model-facing output alone: watchConditionSummary still
// renders the exact same prose, and liveWatchSummaries still returns the same
// five fields with the same values.
func TestWatchConditionSummaryAndListUnchanged(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cfg  *watchConfig
		want string
	}{
		{"output match with note", &watchConfig{outputMatch: "ready", note: "why I care"}, "output_match: ready; note: why I care"},
		{"one-shot timer", &watchConfig{timer: true, oneShot: true, timerSeconds: 600, progressIntervalMS: 600000}, "after_seconds: 600"},
		{"repeating timer", &watchConfig{timer: true, timerSeconds: 300, progressIntervalMS: 300000, note: "n"}, "repeat_seconds: 300; note: n"},
		{"progress interval", &watchConfig{progressIntervalMS: 300000}, "progress_interval_ms: 300000"},
		{"event watch", &watchConfig{events: []string{"assistant.tool"}}, "events: [assistant.tool]"},
		{"wildcard events", &watchConfig{wildcardEvents: true}, "events: [*]"},
	} {
		if got := watchConditionSummary(tc.cfg); got != tc.want {
			t.Errorf("watchConditionSummary(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}

	jm := newTestJM(t)
	res, err := jm.configureWatch(watchArgs{
		Operation: "create", Source: "self", Target: runtimeMessageAliasCaller,
		AfterSeconds: 600, Note: "wake me",
	})
	if err != nil {
		t.Fatalf("configure timer: %v", err)
	}
	live := jm.liveWatchSummaries()
	wantEntry := watchListEntry{
		ID:         res.WatchID,
		Source:     "self",
		Condition:  "after_seconds: 600; note: wake me",
		Deliveries: 0,
		CreatedAt:  frozenTestTime.UTC().Format(time.RFC3339Nano),
	}
	if len(live) != 1 || live[0] != wantEntry {
		t.Fatalf("liveWatchSummaries = %+v, want [%+v]", live, wantEntry)
	}
}

// TestSessionDetailedStatusProjectsWatches proves DetailedStatus.Watches is
// populated from the session's live watches for both a timer and an output
// watch, with the structured cadence the wire projection consumes.
func TestSessionDetailedStatusProjectsWatches(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	if _, err := s.jobManager.configureWatch(watchArgs{
		Operation: "create", Source: "self", Target: runtimeMessageAliasCaller,
		AfterSeconds: 600, Note: "wake me",
	}); err != nil {
		t.Fatalf("install timer watch: %v", err)
	}
	rec, err := s.jobManager.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	if _, err := s.jobManager.configureWatch(watchArgs{
		Operation: "create", Target: rec.JobID, OutputMatch: "ready",
	}); err != nil {
		t.Fatalf("install output watch: %v", err)
	}

	ds := s.DetailedStatus()
	if len(ds.Watches) != 2 {
		t.Fatalf("DetailedStatus.Watches = %+v, want 2 rows", ds.Watches)
	}
	bySource := map[string]WatchStatusInfo{}
	for _, w := range ds.Watches {
		bySource[w.Source] = w
	}
	timer, ok := bySource["self"]
	if !ok {
		t.Fatalf("timer watch missing from %+v", ds.Watches)
	}
	if timer.Note != "wake me" || !reflect.DeepEqual(timer.Cadence, []WatchCadenceInfo{{Kind: "after", Seconds: 600}}) || !timer.Active {
		t.Fatalf("timer watch = %+v, want note/cadence/active", timer)
	}
	output, ok := bySource[rec.JobID]
	if !ok {
		t.Fatalf("output watch missing from %+v", ds.Watches)
	}
	if output.OutputMatch != "ready" || !reflect.DeepEqual(output.Cadence, []WatchCadenceInfo{{Kind: "output"}}) || !output.Active {
		t.Fatalf("output watch = %+v, want output_match/cadence/active", output)
	}
}

// installOutputWatchForDeliveryTimes installs a real output-match watch through
// the same configureWatch path job_watch uses and returns its id, target, and a
// feeder that drives exactly one real delivery per call. The watch carries no
// send, so each match counts a model-facing delivery through the notification
// rail and never settles a frame.
func installOutputWatchForDeliveryTimes(t *testing.T, jm *jobManager) (string, string, func()) {
	t.Helper()
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	res, err := jm.configureWatch(watchArgs{
		Operation: "create", Source: rec.JobID, Target: rec.JobID, OutputMatch: "hit",
	})
	if err != nil {
		t.Fatalf("configureWatch: %v", err)
	}
	if res.WatchID == "" {
		t.Fatal("configureWatch returned no watch id")
	}
	var offset int64
	feed := func() {
		chunk := []byte("hit\n")
		offset += int64(len(chunk))
		jm.feedJobOutput(rec.JobID, chunk, offset)
	}
	return res.WatchID, rec.JobID, feed
}

// TestWatchDeliveryTimesRingCapsOldestFirst installs a real output-match watch
// and drives more deliveries than the ring holds, stepping the job manager
// clock once per delivery. After every delivery the count and the ring must
// have advanced together; once the ring is full it keeps exactly
// watchDeliveryTimeCap instants, drops the oldest, and projects them
// oldest-first while the delivery count still reports every delivery.
func TestWatchDeliveryTimesRingCapsOldestFirst(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var seq atomic.Int64
	jm.now = func() time.Time { return frozenTestTime.Add(time.Duration(seq.Load()) * time.Second) }

	watchID, _, feed := installOutputWatchForDeliveryTimes(t, jm)

	const dropped = 5
	total := watchDeliveryTimeCap + dropped
	for step := 1; step <= total; step++ {
		seq.Store(int64(step))
		feed()

		jm.mu.Lock()
		_, cfg, ok := jm.watchConfigByIDLocked(watchID)
		var deliveries, ringLen int
		var newest time.Time
		if ok && cfg != nil {
			deliveries = cfg.deliveries
			ringLen = len(cfg.deliveryTimes)
			newest = cfg.deliveryTimes[ringLen-1]
		}
		jm.mu.Unlock()
		if !ok {
			t.Fatalf("watch %s left the live set after delivery %d", watchID, step)
		}
		if deliveries != step {
			t.Fatalf("after delivery %d: deliveries = %d, want the count to track every delivery", step, deliveries)
		}
		if want := min(step, watchDeliveryTimeCap); ringLen != want {
			t.Fatalf("after delivery %d: deliveryTimes holds %d instants, want %d", step, ringLen, want)
		}
		if want := frozenTestTime.Add(time.Duration(step) * time.Second); !newest.Equal(want) {
			t.Fatalf("after delivery %d: newest instant = %s, want the job manager clock's %s", step, newest, want)
		}
	}

	statuses := jm.liveWatchStatuses()
	if len(statuses) != 1 {
		t.Fatalf("liveWatchStatuses = %+v, want exactly the one live watch", statuses)
	}
	if statuses[0].Deliveries != total {
		t.Fatalf("Deliveries = %d, want %d", statuses[0].Deliveries, total)
	}
	got := statuses[0].DeliveryTimes
	if len(got) != watchDeliveryTimeCap {
		t.Fatalf("DeliveryTimes holds %d instants, want the %d-instant cap", len(got), watchDeliveryTimeCap)
	}
	prev := time.Time{}
	for i, stamp := range got {
		want := frozenTestTime.Add(time.Duration(dropped+i+1) * time.Second).Format(time.RFC3339Nano)
		if stamp != want {
			t.Fatalf("DeliveryTimes[%d] = %q, want %q (oldest-first, oldest %d dropped)", i, stamp, want, dropped)
		}
		parsed, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			t.Fatalf("DeliveryTimes[%d] = %q is not RFC3339Nano: %v", i, stamp, err)
		}
		if i > 0 && parsed.Before(prev) {
			t.Fatalf("DeliveryTimes[%d] = %s moved backwards from %s", i, parsed, prev)
		}
		prev = parsed
	}
}

// TestWatchDeliveryTimesStampFromTheJobManagerClock pins the instant source:
// a delivery is stamped with jm.now() at delivery time, not with the config's
// createdAt, and an empty ring projects as nil so the wire field stays omitted.
func TestWatchDeliveryTimesStampFromTheJobManagerClock(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	watchID, _, feed := installOutputWatchForDeliveryTimes(t, jm)

	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(watchID)
	var before []string
	var createdAt time.Time
	if ok && cfg != nil {
		before = append([]string(nil), watchDeliveryTimesOf(cfg)...)
		createdAt = cfg.createdAt
	}
	jm.mu.Unlock()
	if !ok {
		t.Fatalf("watch %s is not installed", watchID)
	}
	if before != nil {
		t.Fatalf("a watch with no deliveries projects %#v, want nil", before)
	}

	deliveredAt := frozenTestTime.Add(90 * time.Second)
	jm.now = func() time.Time { return deliveredAt }
	feed()

	statuses := jm.liveWatchStatuses()
	if len(statuses) != 1 {
		t.Fatalf("liveWatchStatuses = %+v, want one row", statuses)
	}
	want := []string{deliveredAt.UTC().Format(time.RFC3339Nano)}
	if !reflect.DeepEqual(statuses[0].DeliveryTimes, want) {
		t.Fatalf("DeliveryTimes = %#v, want %#v", statuses[0].DeliveryTimes, want)
	}
	if statuses[0].CreatedAt == want[0] || !createdAt.Equal(frozenTestTime) {
		t.Fatalf("createdAt = %s, want the distinct install time %s (a delivery must not re-stamp it)", statuses[0].CreatedAt, frozenTestTime)
	}
}
