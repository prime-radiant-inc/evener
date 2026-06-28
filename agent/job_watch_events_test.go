package agent

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
)

func TestEventWatchFiresAndNotifiesCaller(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"communicate"}})
	onSessionEventKD(jm, events.EventCommunicate, nil)

	if len(notified) != 1 {
		t.Fatalf("a communicate event must notify the caller once, got %d", len(notified))
	}
	if notified[0].JobID != "" {
		t.Fatalf("session event notification job_id = %q, want empty", notified[0].JobID)
	}
}

func TestEventWatchFiltersAssistantToolByNameAndStatus(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"assistant.tool"},
		EventFilter: &watchEventFilter{
			ToolName: "read_file",
			Status:   "ok",
		},
	})
	onSessionEventKD(jm, events.EventToolCallEnd, events.ToolCallEndData{ToolName: "job_list", Output: "{}"})
	onSessionEventKD(jm, events.EventToolCallEnd, events.ToolCallEndData{ToolName: "read_file", Error: "failed"})
	if len(notified) != 0 {
		t.Fatalf("non-matching tool events fired watch: %+v", notified)
	}

	onSessionEventKD(jm, events.EventToolCallEnd, events.ToolCallEndData{ToolName: "read_file", Output: "ok"})
	if len(notified) != 1 {
		t.Fatalf("matching assistant.tool event fired %d notifications, want 1", len(notified))
	}
}

func TestWildcardEventWatchOnlyFiresSupportedEvents(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"*"}})
	onSessionEventKD(jm, events.EventSteeringInjected, events.SteeringInjectedData{Text: "internal"})
	if len(notified) != 0 {
		t.Fatalf("internal event fired wildcard watch: %+v", notified)
	}

	onSessionEventKD(jm, events.EventAssistantTextEnd, nil)
	if len(notified) != 0 {
		t.Fatalf("assistant text event fired wildcard watch after assistant.message removal: %+v", notified)
	}

	onSessionEventKD(jm, events.EventCommunicate, nil)
	if len(notified) != 1 {
		t.Fatalf("supported event fires = %d, want 1", len(notified))
	}
}

func TestWildcardJobEventWatchNotifiesConcreteJob(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	if _, err := jm.configureWatch(watchArgs{Target: "*", Events: []string{"job.notification"}}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{JobID: "job_worker", JobType: "delegate", Status: "completed"})

	if len(notified) != 1 {
		t.Fatalf("job.notification event must notify the caller once, got %d", len(notified))
	}
	if notified[0].JobID != "job_worker" {
		t.Fatalf("job event notification job_id = %q, want concrete triggering job", notified[0].JobID)
	}
}

func TestConcreteJobEventWatchIgnoresOtherJobsBeforeEveryCount(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	watched, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create watched shell: %v", err)
	}
	other, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create other shell: %v", err)
	}
	t.Cleanup(func() {
		finishRunningTestJob(t, jm, watched.JobID)
		finishRunningTestJob(t, jm, other.JobID)
	})

	if _, err := jm.configureWatch(watchArgs{
		Target: watched.JobID,
		Events: []string{"job.notification"},
		Every:  2,
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := onlyWatchConfigForTest(t, jm)

	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{JobID: other.JobID, JobType: "shell", Status: "completed"})
	if cfg.eventCount != 0 {
		t.Fatalf("unrelated job eventCount = %d, want 0", cfg.eventCount)
	}
	if len(notified) != 0 {
		t.Fatalf("unrelated job event notified: %+v", notified)
	}

	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{JobID: watched.JobID, JobType: "shell", Status: "completed"})
	if cfg.eventCount != 1 {
		t.Fatalf("first watched job eventCount = %d, want 1", cfg.eventCount)
	}
	if len(notified) != 0 {
		t.Fatalf("first watched event with every=2 notified: %+v", notified)
	}

	onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{JobID: watched.JobID, JobType: "shell", Status: "failed"})
	if len(notified) != 1 {
		t.Fatalf("second watched event notifications = %d, want 1", len(notified))
	}
	if notified[0].JobID != watched.JobID {
		t.Fatalf("notification job_id = %q, want %q", notified[0].JobID, watched.JobID)
	}
}

func TestReceiverWatchNotificationWithoutCallbackDoesNotNotifyOwner(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	if _, err := jm.configureWatch(watchArgs{
		Source:            rec.JobID,
		Target:            rec.JobID,
		ReceiverSessionID: "ROOT",
		OutputMatch:       "READY",
	}); err != nil {
		t.Fatalf("configure receiver watch: %v", err)
	}

	feedJob(jm, rec.JobID, []byte("server READY\n"))
	if len(notified) != 0 {
		t.Fatalf("owner notifications = %+v, want no fallback delivery", notified)
	}
}

func TestEventWatchTriggerEveryNth(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var fires int
	jm.enqueue = func(jobNotification) { fires++ }

	installWatchBelowValidation(t, jm, watchArgs{
		Target: "caller",
		Events: []string{"communicate"},
		Every:  3,
	})
	for i := 0; i < 7; i++ {
		onSessionEventKD(jm, events.EventCommunicate, nil)
	}
	if fires != 2 {
		t.Errorf("every=3 over 7 events should fire twice, got %d", fires)
	}
}

func TestEventWatchIgnoresUnwatchedKind(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var fires int
	jm.enqueue = func(jobNotification) { fires++ }
	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"communicate"}})
	onSessionEventKD(jm, events.EventToolCallEnd, nil)
	if fires != 0 {
		t.Errorf("an unwatched event kind must not fire; fires = %d", fires)
	}
}

func TestConcreteJobEventWatchSendsFrame(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, err := jm.configureWatch(watchArgs{
		Target: rec.JobID,
		Events: []string{"assistant.tool"},
		Send:   &watchSendArgs{To: "dlg_obs", Message: "observe"},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	onSessionEventKD(jm, events.EventToolCallEnd, nil)

	// Observation records the send as pending (frame snapshot included); delivery
	// is the loop-owned drain's job.
	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("concrete job event watch must record one pending send, got %d", len(pending))
	}
	var state *jobstore.WatchSendState
	for _, p := range pending {
		state = p
	}
	if state.Key.ResolvedSendTo != "dlg_obs" {
		t.Fatalf("pending target = %q, want dlg_obs", state.Key.ResolvedSendTo)
	}
	if state.Key.WatchID == "" {
		t.Fatalf("pending watch_id is empty: %+v", state.Key)
	}
	if !strings.Contains(state.Frame, "observe") ||
		!strings.Contains(state.Frame, rec.JobID) ||
		!strings.Contains(state.Frame, "event: TOOL_CALL_END") {
		t.Fatalf("pending frame = %q, want configured message, job id, and trigger", state.Frame)
	}
}

func TestOutputMatchWatchFiresOnAppendedBytes(t *testing.T) {
	t.Parallel()
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

// TestOutputMatchHonorsScanOffsetThroughFeedPath proves the end offset threaded
// from the store reaches FeedAt in the matcher's lifetime-byte space: a chunk
// landing entirely below an attach-time scan offset must not fire, while a later
// chunk above it must. A stale matcher-local counter (the old Feed wrapper)
// would start at 0, sit below the scan offset, and silently drop both.
func TestOutputMatchHonorsScanOffsetThroughFeedPath(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(?i)ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: ""}]
	if cfg == nil || cfg.outputMatcher == nil {
		t.Fatal("output_match watch not installed")
	}

	// Mark the first 100 lifetime bytes as covered by an attach-time scan.
	const scanOffset = 100
	cfg.outputMatcher.SetScanOffset(scanOffset)

	// A chunk whose end offset is at or below the scan offset is already covered:
	// it must not fire.
	below := []byte("server ready\n")
	jm.feedJobOutput(rec.JobID, below, scanOffset)
	if len(notified) != 0 {
		t.Fatalf("a chunk ending at the scan offset must not fire; got %d", len(notified))
	}

	// A later chunk whose end offset is past the scan offset must fire.
	above := []byte("server ready\n")
	jm.feedJobOutput(rec.JobID, above, scanOffset+int64(len(above)))
	if len(notified) != 1 {
		t.Fatalf("a post-scan chunk must fire once; got %d", len(notified))
	}
}

// TestOutputMatchEndToEndOffsetThreadsFromStore reproduces the T2 attach flow that
// the store-derived end offset exists to make correct: a running job's output store
// already holds bytes the matcher was never fed (the watch did not exist while they
// were produced), attach sets the scan offset to that lifetime length, and live
// output is then appended via the real appendJobOutput -> feedJobOutput path.
//
// The byte counts are chosen so the two feed strategies diverge: the matcher-local
// 0-based counter the old Feed wrapper maintained would compute an end offset at or
// below the scan offset for the first live chunk and SILENTLY DISCARD it, never
// firing on live output; the store's lifetime Len() is already past the scan offset,
// so the production FeedAt path fires. Reverting feedJobOutput's FeedAt(chunk,
// endOffset) call to Feed(chunk) makes this test fail at the "first live chunk must
// fire" assertion.
func TestOutputMatchEndToEndOffsetThreadsFromStore(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	output := jm.running[rec.JobID].output

	// Pre-watch output: 100 lifetime bytes the matcher never sees. appendJobOutput
	// feeds only installed watches, and none exists yet, so the store advances while
	// any future matcher's internal counter stays at 0 -- the exact pre-attach state.
	preExisting := bytes.Repeat([]byte("x"), 100)
	if _, err := jm.appendJobOutput(rec.JobID, output, preExisting); err != nil {
		t.Fatalf("pre-watch append: %v", err)
	}
	if got := output.Len(); got != 100 {
		t.Fatalf("store lifetime length after pre-watch append = %d, want 100", got)
	}
	if len(notified) != 0 {
		t.Fatalf("pre-watch output must not fire (no watch installed); got %d", len(notified))
	}

	// Attach: install the watch (its matcher's feedOffset starts at 0, having seen
	// none of the 100 pre-existing bytes) and set the scan offset to the current
	// lifetime length, marking those 100 bytes as already covered.
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(?i)ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: ""}]
	if cfg == nil || cfg.outputMatcher == nil {
		t.Fatal("output_match watch not installed")
	}
	const scanOffset = 100
	if output.Len() != scanOffset {
		t.Fatalf("scan offset must equal current lifetime length; Len()=%d, scanOffset=%d", output.Len(), scanOffset)
	}
	cfg.outputMatcher.SetScanOffset(scanOffset)

	// First live chunk (13 bytes). Its store lifetime end is 113 > scanOffset, so
	// FeedAt fires. The stale 0-based counter would put its end at 13 <= scanOffset
	// and discard it -- this assertion is what catches a Feed regression.
	live := []byte("server READY\n")
	if len(live) > scanOffset {
		t.Fatalf("test setup: live chunk (%d bytes) must be <= scanOffset (%d) so a stale 0-based counter would wrongly discard it", len(live), scanOffset)
	}
	if _, err := jm.appendJobOutput(rec.JobID, output, live); err != nil {
		t.Fatalf("live append: %v", err)
	}
	if len(notified) != 1 {
		t.Fatalf("first live chunk past the scan offset must fire once via the real path; got %d", len(notified))
	}

	// A second live chunk fires again, confirming the offset keeps advancing with
	// the store across successive appends.
	if _, err := jm.appendJobOutput(rec.JobID, output, []byte("still READY\n")); err != nil {
		t.Fatalf("second live append: %v", err)
	}
	if len(notified) != 2 {
		t.Fatalf("second live chunk must fire again; got %d total", len(notified))
	}
}

// TestOutputMatchDropsOnEndOffsetRegression confirms the monotonicity guard: a
// chunk whose end offset regresses versus the last seen offset for the job is
// dropped (no match) and raises exactly one warning notification.
func TestOutputMatchDropsOnEndOffsetRegression(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(?i)ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	// First feed at offset 200 fires and arms the last-seen offset.
	jm.feedJobOutput(rec.JobID, []byte("server ready\n"), 200)
	if len(notified) != 1 {
		t.Fatalf("first feed must fire once; got %d", len(notified))
	}
	notified = notified[:0]

	// A regressed end offset (100 < 200) must drop the chunk before the matcher
	// and raise exactly one warning notification.
	jm.feedJobOutput(rec.JobID, []byte("server ready\n"), 100)
	if len(notified) != 1 {
		t.Fatalf("a regressed feed must enqueue exactly one warning; got %d: %+v", len(notified), notified)
	}
	if !strings.Contains(notified[0].Reason, "offset") {
		t.Fatalf("regression warning reason = %q, want it to mention the offset regression", notified[0].Reason)
	}
}

// TestAttachScanFiresOnceForAlreadyPrintedToken proves the level-trigger: a
// watch attached to a running job whose retained output ALREADY contains the
// pattern on several lines fires exactly once at attach (not once per matching
// line), the fire carries the LAST matching line, and the create result reports
// fired=true (spec §7.1 "Running target" + "Attach-scan fire cardinality").
func TestAttachScanFiresOnceForAlreadyPrintedToken(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	// Three matching lines retained BEFORE the watch is configured. No watch exists,
	// so appendJobOutput advances the store but fires nothing.
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("ready\nready\nready\n")); err != nil {
		t.Fatalf("pre-watch append: %v", err)
	}
	if len(notified) != 0 {
		t.Fatalf("pre-watch output must not fire (no watch installed); got %d", len(notified))
	}

	res, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !res.Fired {
		t.Fatal("create result must report fired=true when retained output already matches")
	}
	if len(notified) != 1 {
		t.Fatalf("attach scan must fire exactly once regardless of matching-line count; got %d", len(notified))
	}
	if notified[0].Reason != "output_match: ready" {
		t.Fatalf("attach fire reason = %q, want \"output_match: ready\" (the last matching line)", notified[0].Reason)
	}
}

// TestAttachScanSendArmFiresOnce is the send-arm counterpart: a sidecar watch
// (send.to=delegate) attached to a running job with already-printed output
// records exactly one pending send whose trigger carries the last matching line,
// and the create result reports fired=true.
func TestAttachScanSendArmFiresOnce(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	seedCommonWatchSendTargets(t, jm)

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("ready one\nready two\nready three\n")); err != nil {
		t.Fatalf("pre-watch append: %v", err)
	}

	res, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !res.Fired {
		t.Fatal("create result must report fired=true for a sidecar attach-scan match")
	}

	jm.mu.Lock()
	cfg := jm.watches[watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "dlg_obs"}]
	var pendingCount int
	var lastReason string
	if cfg != nil {
		pendingCount = len(cfg.pending)
		for _, state := range cfg.pending {
			lastReason = state.TriggerReason
		}
	}
	jm.mu.Unlock()
	if pendingCount != 1 {
		t.Fatalf("attach scan must record exactly one pending send; got %d", pendingCount)
	}
	if lastReason != "output_match: ready three" {
		t.Fatalf("pending send trigger = %q, want \"output_match: ready three\" (the last matching line)", lastReason)
	}
}

// TestAttachScanTokenStraddlingBoundaryFiresOnce drives the no-double-fire-across
// -the-seam case under -race: a partial token is retained at attach (no newline),
// the watch attaches (seeding the carry + scan offset), then the rest of the
// token arrives via the real appendJobOutput->feedJobOutput path. The token must
// fire EXACTLY once — the attach scan sees no complete matching line, and the
// live FeedAt completes the seeded carry into one match.
func TestAttachScanTokenStraddlingBoundaryFiresOnce(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	output := jm.running[rec.JobID].output
	// Partial token with no newline retained before attach.
	if _, err := jm.appendJobOutput(rec.JobID, output, []byte("rea")); err != nil {
		t.Fatalf("partial append: %v", err)
	}

	res, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	// The retained tail "rea" is not a complete matching line, so the attach scan
	// fires nothing; the carry seed carries "rea" forward.
	if res.Fired {
		t.Fatal("create result must report fired=false: no complete matching line at attach")
	}
	if len(notified) != 0 {
		t.Fatalf("attach scan must not fire on an unterminated partial token; got %d", len(notified))
	}

	// The rest of the token arrives through the real append path; the seeded carry
	// completes "ready" and fires exactly once.
	if _, err := jm.appendJobOutput(rec.JobID, output, []byte("dy\n")); err != nil {
		t.Fatalf("completion append: %v", err)
	}
	if len(notified) != 1 {
		t.Fatalf("straddling token must fire exactly once via the carry+FeedAt seam; got %d", len(notified))
	}
	if notified[0].Reason != "output_match: ready" {
		t.Fatalf("seam fire reason = %q, want \"output_match: ready\"", notified[0].Reason)
	}
}

// TestAttachScanNoMatchThenLiveFires proves the empty case and that the live path
// still works after a no-fire attach: a running job with NO output gets an
// output_match watch (fired=false, no notification), then matching output appended
// through the real path fires once.
func TestAttachScanNoMatchThenLiveFires(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	output := jm.running[rec.JobID].output

	res, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(?i)ready"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if res.Fired {
		t.Fatal("create result must report fired=false when nothing is retained")
	}
	if len(notified) != 0 {
		t.Fatalf("attach scan on empty output must not fire; got %d", len(notified))
	}

	if _, err := jm.appendJobOutput(rec.JobID, output, []byte("server READY\n")); err != nil {
		t.Fatalf("live append: %v", err)
	}
	if len(notified) != 1 {
		t.Fatalf("live matching output must fire once after a no-match attach; got %d", len(notified))
	}
}

// TestAttachScanIdempotentReinstallDoesNotRefire pins that re-installing the
// identical watch (the idempotent no-op path) does NOT re-scan or re-fire: only a
// FRESH concrete-running output_match install scans.
func TestAttachScanIdempotentReinstallDoesNotRefire(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("ready\n")); err != nil {
		t.Fatalf("pre-watch append: %v", err)
	}

	first, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configure first: %v", err)
	}
	if !first.Fired || len(notified) != 1 {
		t.Fatalf("first install must fire once; fired=%v notifications=%d", first.Fired, len(notified))
	}

	// Identical re-install is a no-op: it must not scan again.
	second, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configure second: %v", err)
	}
	if second.Fired {
		t.Fatal("idempotent re-install must report fired=false (no fresh matcher installed)")
	}
	if len(notified) != 1 {
		t.Fatalf("idempotent re-install must not re-fire; total notifications = %d, want 1", len(notified))
	}
}

func TestConcreteWatchExpiresOnTerminal(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, _ = jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if jm.watchCount() != 1 {
		t.Fatalf("watch not registered")
	}
	code := 0
	jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code)
	if jm.watchCount() != 0 {
		t.Errorf("a concrete-job watch must expire when the job goes terminal; count = %d", jm.watchCount())
	}
}

func TestSessionWatchSurvivesAJobTerminal(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"communicate"}})
	code := 0
	jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code)
	if jm.watchCount() != 1 {
		t.Errorf("a session-alias watch must survive a job going terminal; count = %d", jm.watchCount())
	}
}

func TestConcreteWatchFlushesBeforeTerminalNotification(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var order []string
	jm.enqueue = func(n jobNotification) {
		if n.Status == jobNotificationEventWatch {
			order = append(order, "watch")
		} else {
			order = append(order, "terminal")
		}
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append: %v", err)
	}

	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if strings.Join(order, ",") != "watch,terminal" {
		t.Fatalf("notification order = %v, want watch before terminal", order)
	}
}

func TestProgressTimerFiresPeriodically(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	fired := make(chan struct{}, 4)
	jm.enqueue = func(jobNotification) { fired <- struct{}{} }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, ProgressIntervalMS: minWatchProgressIntervalMS}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	// configureWatch clamps progress_interval_ms to minWatchProgressIntervalMS (1 s);
	// bypass that floor by restarting the goroutine directly at 10 ms so the test
	// does not depend on wall-clock seconds.
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID}
	jm.mu.Lock()
	cfg := jm.watches[key]
	closeWatchConfig(cfg)          // stop the slow goroutine; resets cfg.progressStop
	cfg.progressIntervalMS = 10    // 10 ms test-scoped interval
	stop := cfg.initProgressStop() // new stop channel
	jm.mu.Unlock()
	jm.startProgressTimer(key, cfg, stop)
	select {
	case <-fired:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("progress timer did not fire within 200ms")
	}
	_, _ = jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true})
}

func TestProgressTimerStopsOnClose(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	fired := make(chan jobNotification, 16)
	jm.enqueue = func(n jobNotification) { fired <- n }

	if _, err := jm.configureWatch(watchArgs{Target: "caller", ProgressIntervalMS: minWatchProgressIntervalMS}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	// Swap the timer to 10 ms so the test does not depend on wall-clock seconds.
	// configureWatch clamps progress_interval_ms to minWatchProgressIntervalMS (1 s);
	// restart the goroutine directly at 10 ms after installation.
	key := watchKey{VisibleSessionID: jm.sessionID, Target: "caller"}
	jm.mu.Lock()
	cfg := jm.watches[key]
	closeWatchConfig(cfg)          // stop the slow goroutine; resets cfg.progressStop
	cfg.progressIntervalMS = 10    // 10 ms test-scoped interval
	stop := cfg.initProgressStop() // close() will close this via closeWatchConfig
	jm.mu.Unlock()
	jm.startProgressTimer(key, cfg, stop)
	select {
	case n := <-fired:
		if n.JobID != "" {
			t.Fatalf("session progress notification job_id = %q, want empty", n.JobID)
		}
	case <-time.After(200 * time.Millisecond):
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
	// close() must invoke closeWatchConfig which closes the stop channel, signalling
	// the goroutine to exit. If close(cfg.progressStop) were removed from
	// closeWatchConfig the channel would never close and this select would time out,
	// catching the goroutine-leak mutation.
	select {
	case <-stop:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("close() did not close progressStop channel; goroutine would leak")
	}
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
	t.Parallel()
	if len(WatchEventKindNames) != len(modelEventKinds) {
		t.Fatalf("WatchEventKindNames has %d names, modelEventKinds has %d", len(WatchEventKindNames), len(modelEventKinds))
	}
	for _, name := range WatchEventKindNames {
		if _, ok := modelEventKinds[name]; !ok {
			t.Errorf("WatchEventKindNames includes unresolved event kind %q", name)
		}
	}
}

func TestTerminalCatchupNoSendFiresNotification(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jobID := terminalShellWithOutput(t, jm, "line one\nserver ready\nline three\n")
	// Capture only post-finalize notifications: the job-completion notification
	// finalize enqueues is pre-existing and unrelated to catch-up.
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	res, err := jm.configureWatch(watchArgs{Target: jobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configureWatch terminal catch-up: %v", err)
	}
	if res.Watching {
		t.Fatalf("terminal catch-up must not install a live watch: %+v", res)
	}
	if !res.Fired || !res.TerminalCatchup {
		t.Fatalf("result = %+v, want fired+terminal_catchup", res)
	}
	if res.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want %q", res.Status, jobstore.StatusCompleted)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count after catch-up = %d, want 0", jm.watchCount())
	}
	if len(notified) != 1 {
		t.Fatalf("catch-up notifications = %d, want exactly 1: %+v", len(notified), notified)
	}
	// The frame carries the LAST matching line.
	if !strings.Contains(notified[0].Reason, "output_match: server ready") {
		t.Fatalf("notification reason = %q, want last matching line", notified[0].Reason)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("a no-send catch-up must enqueue no watch-send pending; got %+v", pending)
	}
}

// TestTerminalCatchupNoMatchReportsTerminalCatchup covers spec §7.1: a terminal
// output_match-only watch whose retained output does NOT match reports
// terminal_catchup with fired=false and enqueues nothing.
func TestTerminalCatchupNoMatchReportsTerminalCatchup(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	jobID := terminalShellWithOutput(t, jm, "nothing interesting here\n")
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	res, err := jm.configureWatch(watchArgs{Target: jobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configureWatch terminal catch-up: %v", err)
	}
	if res.Watching || res.Fired {
		t.Fatalf("result = %+v, want watching=false fired=false", res)
	}
	if !res.TerminalCatchup {
		t.Fatalf("result = %+v, want terminal_catchup=true", res)
	}
	if res.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want %q", res.Status, jobstore.StatusCompleted)
	}
	if len(notified) != 0 {
		t.Fatalf("a no-match catch-up must enqueue nothing; got %+v", notified)
	}
}

// TestTerminalCatchupFinalUnterminatedLineFires covers spec §7.1's documented
// T3-vs-T2 divergence: for a TERMINAL catch-up the final unterminated line counts
// (the job is dead; nothing will complete the tail), so grepOutput's EOF match
// fires. This is the opposite of T2's attach scan (ScanRetained ignores the tail).
func TestTerminalCatchupFinalUnterminatedLineFires(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	// Note: NO trailing newline on the matching final line.
	jobID := terminalShellWithOutput(t, jm, "warming up\nserver ready")
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	res, err := jm.configureWatch(watchArgs{Target: jobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configureWatch terminal catch-up: %v", err)
	}
	if !res.Fired || !res.TerminalCatchup {
		t.Fatalf("result = %+v, want fired+terminal_catchup for unterminated final matching line", res)
	}
	if len(notified) != 1 || !strings.Contains(notified[0].Reason, "output_match: server ready") {
		t.Fatalf("notifications = %+v, want one final-line match", notified)
	}
}

// TestTerminalCatchupRejectsEventsCondition covers spec §7.1: catch-up applies
// ONLY to pure output_match-only requests. A terminal target carrying events
// (even alongside output_match) still fails target_terminal — nothing can ever
// fire — and installs no watch and no catch-up.
func TestTerminalCatchupRejectsEventsCondition(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }
	jobID := terminalShellWithOutput(t, jm, "server ready\n")

	for _, tc := range []struct {
		name string
		args watchArgs
	}{
		{"events only", watchArgs{Target: jobID, Events: []string{"communicate"}}},
		{"output_match plus events", watchArgs{Target: jobID, OutputMatch: "ready", Events: []string{"communicate"}}},
		{"output_match plus progress", watchArgs{Target: jobID, OutputMatch: "ready", ProgressIntervalMS: 1000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(notified)
			res, err := jm.configureWatch(tc.args)
			if err == nil || !strings.Contains(err.Error(), "target_terminal") {
				t.Fatalf("result %+v err %v, want target_terminal", res, err)
			}
			if res.TerminalCatchup || res.Fired {
				t.Fatalf("non-output_match-only terminal request must not catch-up: %+v", res)
			}
			if len(notified) != before {
				t.Fatalf("non-output_match-only terminal request must enqueue nothing; new = %d", len(notified)-before)
			}
		})
	}
}

// TestTerminalCatchupNotFoundStillErrors covers spec §7.1: catch-up does not
// swallow target_not_found. An unknown target still fails target_not_found.
func TestTerminalCatchupNotFoundStillErrors(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	res, err := jm.configureWatch(watchArgs{Target: "job_missing", OutputMatch: "ready"})
	if err == nil || !strings.Contains(err.Error(), "target_not_found") {
		t.Fatalf("result %+v err %v, want target_not_found", res, err)
	}
	if res.TerminalCatchup {
		t.Fatalf("not-found must not report terminal_catchup: %+v", res)
	}
}

// TestMarshalWatchResultTerminalCatchupProjection covers spec §7.1: the new
// terminal_catchup/status/fired fields surface through the tool JSON projection
// for both the fired and not-fired arms.
func TestMarshalWatchResultTerminalCatchupProjection(t *testing.T) {
	t.Parallel()
	fired, err := marshalWatchResult(watchResult{
		Target:          "job_A",
		Watching:        false,
		Fired:           true,
		TerminalCatchup: true,
		Status:          "completed",
	}, 4096)
	if err != nil {
		t.Fatalf("marshal fired: %v", err)
	}
	var firedOut struct {
		Watching        bool   `json:"watching"`
		Fired           bool   `json:"fired"`
		TerminalCatchup bool   `json:"terminal_catchup"`
		Status          string `json:"status"`
	}
	if err := json.Unmarshal(handlerJSON(t, fired), &firedOut); err != nil {
		t.Fatalf("unmarshal fired: %v (%s)", err, fired)
	}
	if firedOut.Watching || !firedOut.Fired || !firedOut.TerminalCatchup || firedOut.Status != "completed" {
		t.Fatalf("fired projection = %+v, want fired+terminal_catchup+completed", firedOut)
	}
	firedState, ok := fired.(tooldefs.StateResult)
	if !ok {
		t.Fatalf("fired result type = %T, want StateResult", fired)
	}
	for _, want := range []string{"terminal catch-up", "fired", "completed"} {
		if !strings.Contains(firedState.Output, want) {
			t.Fatalf("fired model output missing %q: %s", want, firedState.Output)
		}
	}

	notFired, err := marshalWatchResult(watchResult{
		Target:          "job_A",
		Watching:        false,
		Fired:           false,
		TerminalCatchup: true,
		Status:          "failed",
	}, 4096)
	if err != nil {
		t.Fatalf("marshal not-fired: %v", err)
	}
	if !strings.Contains(string(handlerJSON(t, notFired)), `"terminal_catchup":true`) || !strings.Contains(string(handlerJSON(t, notFired)), `"status":"failed"`) {
		t.Fatalf("not-fired projection = %s, want terminal_catchup+status", notFired)
	}
	// Contract §7.1 promises "fired=false on none" — explicit, not omitted.
	if !strings.Contains(string(handlerJSON(t, notFired)), `"fired":false`) {
		t.Fatalf("not-fired projection must report explicit fired:false: %s", notFired)
	}
	notFiredState, ok := notFired.(tooldefs.StateResult)
	if !ok {
		t.Fatalf("not-fired result type = %T, want StateResult", notFired)
	}
	for _, want := range []string{"terminal catch-up", "not fired", "failed"} {
		if !strings.Contains(notFiredState.Output, want) {
			t.Fatalf("not-fired model output missing %q: %s", want, notFiredState.Output)
		}
	}
}
