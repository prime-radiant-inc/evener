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
					Source: "self",
					Target: runtimeMessageAliasCaller,
					Note:   "wake me",
					Cadence: []WatchCadenceInfo{{
						Kind: "after", Seconds: 600,
						DerivedNextFireAt: frozenTestTime.Add(600 * time.Second).UTC().Format(time.RFC3339Nano),
					}},
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
					Source: "self",
					Target: runtimeMessageAliasCaller,
					Cadence: []WatchCadenceInfo{{
						Kind: "every", Seconds: 300,
						DerivedNextFireAt: frozenTestTime.Add(300 * time.Second).UTC().Format(time.RFC3339Nano),
					}},
					Active: true,
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

// TestWatchCadencesCarryEventEveryAndFilter pins the events cadence row's two
// extra fields. A watch that fires every Nth matching event, or that filters
// on tool name/status, must be distinguishable on the wire from one that
// fires on every match - the model-facing watchConditionSummary already
// renders both, and the panel reads the structured row. A plain event watch
// and a wildcard watch carry neither, so their row stays exactly as before.
func TestWatchCadencesCarryEventEveryAndFilter(t *testing.T) {
	t.Parallel()

	throttled := watchCadencesOf(&watchConfig{
		events:       []string{"assistant.tool"},
		triggerEvery: 3,
		eventFilter:  &watchEventFilter{ToolName: "Bash", Status: "error"},
	})
	wantThrottled := []WatchCadenceInfo{{Kind: "events", Every: 3, Filter: "tool_name=Bash, status=error"}}
	if !reflect.DeepEqual(throttled, wantThrottled) {
		t.Fatalf("throttled+filtered event cadence = %+v, want %+v", throttled, wantThrottled)
	}

	// The count and filter agree with the prose the model already sees.
	cfg := &watchConfig{
		events:       []string{"assistant.tool"},
		triggerEvery: 3,
		eventFilter:  &watchEventFilter{ToolName: "Bash", Status: "error"},
	}
	if summary := watchConditionSummary(cfg); summary != "events: [assistant.tool] every 3 where tool_name=Bash, status=error" {
		t.Fatalf("watchConditionSummary = %q, want the prose the cadence mirrors", summary)
	}

	plain := watchCadencesOf(&watchConfig{events: []string{"assistant.tool"}})
	if !reflect.DeepEqual(plain, []WatchCadenceInfo{{Kind: "events"}}) {
		t.Fatalf("plain event cadence = %+v, want a bare events row so old labels stay byte-identical", plain)
	}

	wildcard := watchCadencesOf(&watchConfig{wildcardEvents: true, eventFilter: &watchEventFilter{ToolName: "Bash"}})
	if !reflect.DeepEqual(wildcard, []WatchCadenceInfo{{Kind: "events"}}) {
		t.Fatalf("wildcard event cadence = %+v, want a bare events row (prose renders no count or filter)", wildcard)
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
	// A clock cadence now also carries its derived next-fire instant, so this
	// checks the interval fields and that the instant is present rather than a
	// DeepEqual against the pre-derivation shape.
	timerCadence := timer.Cadence
	if timer.Note != "wake me" || len(timerCadence) != 1 || timerCadence[0].Kind != "after" ||
		timerCadence[0].Seconds != 600 || timerCadence[0].DerivedNextFireAt == "" || !timer.Active {
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

// TestSessionDetailedStatusOrdersWatches pins the order of the projected rows.
// The projection walks the live-watch map, so without an explicit order two
// snapshots of unchanged state can differ run to run: the wire output is
// unstable and a consumer rebuilding its rows sees churn where nothing changed.
// Every other projection of these rows orders them by (source, id), and this one
// must agree. Enough rows are installed that a map walk cannot plausibly land in
// sorted order by chance.
func TestSessionDetailedStatusOrdersWatches(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	const timers = 8
	for i := range timers {
		if _, err := s.jobManager.configureWatch(watchArgs{
			Operation: "create", Source: "self", Target: runtimeMessageAliasCaller,
			AfterSeconds: 600, Note: "wake me",
		}); err != nil {
			t.Fatalf("install timer watch %d: %v", i, err)
		}
	}
	// A job-sourced row too, so the assertion exercises both legs of the
	// (source, id) order rather than only the id leg.
	rec, err := s.jobManager.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	if _, err := s.jobManager.configureWatch(watchArgs{
		Operation: "create", Target: rec.JobID, OutputMatch: "ready",
	}); err != nil {
		t.Fatalf("install output watch: %v", err)
	}

	const want = timers + 1
	ds := s.DetailedStatus()
	if len(ds.Watches) != want {
		t.Fatalf("DetailedStatus.Watches = %+v, want %d rows", ds.Watches, want)
	}
	for i := 1; i < len(ds.Watches); i++ {
		prev, cur := ds.Watches[i-1], ds.Watches[i]
		if prev.Source > cur.Source || (prev.Source == cur.Source && prev.ID >= cur.ID) {
			t.Fatalf("DetailedStatus.Watches row %d is out of order: (%q, %q) then (%q, %q) in\n%+v",
				i, prev.Source, prev.ID, cur.Source, cur.ID, ds.Watches)
		}
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

// TestFormatWatchStatusesIsPureAndOrdersRows exercises the extracted formatter
// directly: it is a free function over configs, so it runs with no manager and no
// lock, and it must render each instant as the same RFC3339Nano string the live
// projection always did. The input arrives in reverse (source, id) order so the
// assertion pins that the ordering comes from the formatter and not from the
// caller that collected the configs.
func TestFormatWatchStatusesIsPureAndOrdersRows(t *testing.T) {
	t.Parallel()
	created := time.Date(2024, 5, 6, 7, 8, 9, 123456789, time.UTC)
	firstFire := created.Add(90 * time.Second)
	secondFire := created.Add(2 * time.Minute)

	jobWatch := &watchConfig{
		id: "watch_a", sourcePublic: "job_1", target: "job_1",
		outputMatch: "ready", createdAt: created,
	}
	timerWatch := &watchConfig{
		id: "watch_b", sourcePublic: "self", target: runtimeMessageAliasCaller,
		timer: true, timerSeconds: 300, progressIntervalMS: 300000,
		createdAt: created, deliveries: 2,
		deliveryTimes: []time.Time{firstFire, secondFire},
	}

	got := formatWatchStatuses([]*watchConfig{timerWatch, jobWatch})
	want := []WatchStatusInfo{
		{
			ID: "watch_a", Source: "job_1", Target: "job_1",
			OutputMatch: "ready",
			Cadence:     []WatchCadenceInfo{{Kind: "output"}},
			CreatedAt:   "2024-05-06T07:08:09.123456789Z",
			Active:      true,
		},
		{
			ID: "watch_b", Source: "self", Target: runtimeMessageAliasCaller,
			Cadence:       []WatchCadenceInfo{{Kind: "every", Seconds: 300, DerivedNextFireAt: "2024-05-06T07:13:09.123456789Z"}},
			Deliveries:    2,
			DeliveryTimes: []string{"2024-05-06T07:09:39.123456789Z", "2024-05-06T07:10:09.123456789Z"},
			CreatedAt:     "2024-05-06T07:08:09.123456789Z",
			Active:        true,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("formatWatchStatuses = %+v, want %+v", got, want)
	}
}

// TestLiveWatchStatusesProjectionMatchesPureFormatter pins both halves of the
// projection: the manager walk (under jm.mu) selects exactly the configs the
// formatter turns into the projection's rows, and the rows come back in
// (source, id) order regardless of the order the collector produced. Repeated
// calls over unchanged state must agree.
func TestLiveWatchStatusesProjectionMatchesPureFormatter(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	for i := 0; i < 3; i++ {
		if _, err := jm.configureWatch(watchArgs{
			Operation: "create", Source: "self", Target: runtimeMessageAliasCaller,
			RepeatSeconds: 300,
		}); err != nil {
			t.Fatalf("install timer watch %d: %v", i, err)
		}
	}
	// One output watch per job: a watch's key is (visible session, target, send,
	// receiver), so a second watch on the same job replaces the first.
	for i := 0; i < 2; i++ {
		rec, err := jm.createShell(createShellOpts{Command: "x"})
		if err != nil {
			t.Fatalf("create shell %d: %v", i, err)
		}
		if _, err := jm.configureWatch(watchArgs{
			Operation: "create", Target: rec.JobID, OutputMatch: "ready",
		}); err != nil {
			t.Fatalf("install output watch %d: %v", i, err)
		}
	}

	cfgs := jm.visibleWatchConfigSnapshots(jm.sessionID)
	if len(cfgs) != 5 {
		t.Fatalf("visibleWatchConfigSnapshots = %d configs, want the 5 installed", len(cfgs))
	}
	// Reverse the walk's order so the formatter, not the collector, owns the
	// order the two must agree on.
	for i, j := 0, len(cfgs)-1; i < j; i, j = i+1, j-1 {
		cfgs[i], cfgs[j] = cfgs[j], cfgs[i]
	}
	want := formatWatchStatuses(cfgs)

	got := jm.liveWatchStatuses()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("liveWatchStatuses = %+v, want the pure formatter's %+v", got, want)
	}
	for i := 1; i < len(got); i++ {
		prev, cur := got[i-1], got[i]
		if prev.Source > cur.Source || (prev.Source == cur.Source && prev.ID >= cur.ID) {
			t.Fatalf("row %d is out of (source, id) order: (%q, %q) then (%q, %q) in\n%+v",
				i, prev.Source, prev.ID, cur.Source, cur.ID, got)
		}
	}
	if again := jm.liveWatchStatuses(); !reflect.DeepEqual(again, got) {
		t.Fatalf("liveWatchStatuses is not stable across calls:\n%+v\n%+v", got, again)
	}

	// A manager with no watches keeps the non-nil empty answer the projection's
	// callers rely on to tell "no watches now" from "cannot answer for this
	// session".
	empty := newTestJM(t)
	if rows := empty.liveWatchStatuses(); rows == nil || len(rows) != 0 {
		t.Fatalf("liveWatchStatuses for a manager with no watches = %#v, want a non-nil empty slice", rows)
	}
}

// TestLiveWatchSummariesProjectionMatchesPureFormatter pins job_list's
// model-facing projection the same way: liveWatchSummaries must equal
// formatWatchSummaries over the configs the locked walk selected, in (source, id)
// order, so moving the formatting out of the lock changed nothing observable.
func TestLiveWatchSummariesProjectionMatchesPureFormatter(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	if _, err := jm.configureWatch(watchArgs{
		Operation: "create", Source: "self", Target: runtimeMessageAliasCaller,
		AfterSeconds: 600, Note: "wake me",
	}); err != nil {
		t.Fatalf("install timer watch: %v", err)
	}
	if _, err := jm.configureWatch(watchArgs{
		Operation: "create", Target: rec.JobID, OutputMatch: "ready",
	}); err != nil {
		t.Fatalf("install output watch: %v", err)
	}

	want := formatWatchSummaries(jm.visibleWatchConfigSnapshots(jm.sessionID))
	got := jm.liveWatchSummaries()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("liveWatchSummaries = %+v, want the pure formatter's %+v", got, want)
	}
	for i := 1; i < len(got); i++ {
		prev, cur := got[i-1], got[i]
		if prev.Source > cur.Source || (prev.Source == cur.Source && prev.ID >= cur.ID) {
			t.Fatalf("row %d is out of (source, id) order: (%q, %q) then (%q, %q) in\n%+v",
				i, prev.Source, prev.ID, cur.Source, cur.ID, got)
		}
	}
}
