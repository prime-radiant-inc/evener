package agent

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
)

// ---- job_watch.go pure functions ----

// TestCovAvailableEventKindNames covers availableEventKindNames (job_watch.go line 29).
func TestCovAvailableEventKindNames(t *testing.T) {
	names := availableEventKindNames()
	if !reflect.DeepEqual(names, WatchEventKindNames) {
		t.Fatalf("event names = %v, want %v", names, WatchEventKindNames)
	}
	// Verify it returns a copy.
	names[0] = "modified"
	names2 := availableEventKindNames()
	if names2[0] == "modified" {
		t.Fatal("availableEventKindNames should return a copy")
	}
}

// TestCovQuietWatchdogMessage covers quietWatchdogMessage and formatQuietWindow
// (job_watch.go lines 76-89).
func TestCovQuietWatchdogMessage(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	got := quietWatchdogMessage(10*time.Minute, now)
	if !strings.Contains(got, "10m") || !strings.Contains(got, "2025-01-15T10:30:00") {
		t.Fatalf("quietWatchdogMessage = %q", got)
	}

	// Sub-minute window — uses default duration string.
	got = quietWatchdogMessage(50*time.Millisecond, now)
	if !strings.Contains(got, "50ms") {
		t.Fatalf("sub-minute: %q", got)
	}

	// Non-whole-minute window.
	got = quietWatchdogMessage(90*time.Second, now)
	if !strings.Contains(got, "1m30s") {
		t.Fatalf("non-whole-minute: %q", got)
	}
}

// TestCovFormatQuietWindow covers formatQuietWindow (job_watch.go lines 84-89).
func TestCovFormatQuietWindow(t *testing.T) {
	if got := formatQuietWindow(10 * time.Minute); got != "10m" {
		t.Fatalf("10m = %q", got)
	}
	if got := formatQuietWindow(time.Minute); got != "1m" {
		t.Fatalf("1m = %q", got)
	}
	if got := formatQuietWindow(90 * time.Second); got != "1m30s" {
		t.Fatalf("1m30s = %q", got)
	}
	if got := formatQuietWindow(50 * time.Millisecond); got != "50ms" {
		t.Fatalf("50ms = %q", got)
	}
}

// TestCovWatchEndedUnfiredMessage covers watchEndedUnfiredMessage
// (job_watch.go lines 95-100).
func TestCovWatchEndedUnfiredMessage(t *testing.T) {
	got := watchEndedUnfiredMessage("job_1", jobstore.StatusCompleted, "done", 4096)
	if !strings.Contains(got, "job_1") || !strings.Contains(got, "completed") || !strings.Contains(got, "done") || !strings.Contains(got, "4096") {
		t.Fatalf("watchEndedUnfiredMessage = %q", got)
	}
}

// TestCovWatchArgsHasCondition covers watchArgsHasCondition (job_watch.go lines 731-736).
func TestCovWatchArgsHasCondition(t *testing.T) {
	// No condition → false.
	if watchArgsHasCondition(watchArgs{}) {
		t.Fatal("empty args should not have condition")
	}

	// OutputMatch → true.
	if !watchArgsHasCondition(watchArgs{OutputMatch: "READY"}) {
		t.Fatal("output_match should be a condition")
	}

	// ProgressIntervalMS → true.
	if !watchArgsHasCondition(watchArgs{ProgressIntervalMS: 1000}) {
		t.Fatal("progress_interval_ms should be a condition")
	}

	// Events → true.
	if !watchArgsHasCondition(watchArgs{Events: []string{"assistant.tool"}}) {
		t.Fatal("events should be a condition")
	}

	// Receiver session target → true (target must be a session target).
	if !watchArgsHasCondition(watchArgs{ReceiverSessionID: "sess1", Target: "caller"}) {
		t.Fatal("receiver session target should be a condition")
	}
}

// TestCovWatchArgsIsOutputMatchOnly covers watchArgsIsOutputMatchOnly (job_watch.go lines 743-749).
func TestCovWatchArgsIsOutputMatchOnly(t *testing.T) {
	if !watchArgsIsOutputMatchOnly(watchArgs{OutputMatch: "READY"}) {
		t.Fatal("output_match only should be true")
	}
	if watchArgsIsOutputMatchOnly(watchArgs{OutputMatch: "READY", Events: []string{"assistant.tool"}}) {
		t.Fatal("with events should be false")
	}
	if watchArgsIsOutputMatchOnly(watchArgs{OutputMatch: "READY", Clear: true}) {
		t.Fatal("with clear should be false")
	}
	if watchArgsIsOutputMatchOnly(watchArgs{OutputMatch: "READY", Every: 2}) {
		t.Fatal("with every should be false")
	}
	if watchArgsIsOutputMatchOnly(watchArgs{OutputMatch: "READY", ProgressIntervalMS: 1000}) {
		t.Fatal("with progress should be false")
	}
	if watchArgsIsOutputMatchOnly(watchArgs{}) {
		t.Fatal("empty should be false")
	}
}

// TestCovValidateWatchEventArgs covers validateWatchEventArgs (job_watch.go lines 751+).
func TestCovValidateWatchEventArgs(t *testing.T) {
	// Wildcard event → ok.
	if err := validateWatchEventArgs(watchArgs{Events: []string{"*"}}); err != nil {
		t.Fatalf("wildcard: %v", err)
	}

	// Valid event → ok.
	if err := validateWatchEventArgs(watchArgs{Events: []string{"assistant.tool"}}); err != nil {
		t.Fatalf("assistant.tool: %v", err)
	}

	// Invalid event name.
	err := validateWatchEventArgs(watchArgs{Events: []string{"bogus"}})
	if err == nil || !strings.Contains(err.Error(), "unknown event kind") {
		t.Fatalf("bogus event: %v", err)
	}

	// assistant.message is explicitly rejected.
	err = validateWatchEventArgs(watchArgs{Events: []string{"assistant.message"}})
	if err == nil || !strings.Contains(err.Error(), "assistant.message is not watchable") {
		t.Fatalf("assistant.message: %v", err)
	}

	// Every with multiple events → error.
	err = validateWatchEventArgs(watchArgs{Events: []string{"assistant.tool", "communicate"}, Every: 2})
	if err == nil || !strings.Contains(err.Error(), "every requires exactly one") {
		t.Fatalf("every multi: %v", err)
	}

	// Every with wildcard → error.
	err = validateWatchEventArgs(watchArgs{Events: []string{"*"}, Every: 2})
	if err == nil || !strings.Contains(err.Error(), "not \"*\"") {
		t.Fatalf("every wildcard: %v", err)
	}

	// Event filter without events → error.
	err = validateWatchEventArgs(watchArgs{EventFilter: &watchEventFilter{}})
	if err == nil || !strings.Contains(err.Error(), "event_filter requires events") {
		t.Fatalf("event_filter no events: %v", err)
	}

	// Event filter with wrong event kind → error.
	err = validateWatchEventArgs(watchArgs{Events: []string{"job.notification"}, EventFilter: &watchEventFilter{}})
	if err == nil {
		t.Fatalf("event_filter wrong kind: %v", err)
	}
}

// TestCovNormalizeWatchArgs covers normalizeWatchArgs (job_watch.go lines 505-529).
func TestCovNormalizeWatchArgs(t *testing.T) {
	// Negative progress → error.
	err := normalizeWatchArgs(&watchArgs{ProgressIntervalMS: -1})
	if err == nil || !strings.Contains(err.Error(), "progress_interval_ms must be non-negative") {
		t.Fatalf("negative progress: %v", err)
	}

	// Below minimum → clamped.
	a := watchArgs{ProgressIntervalMS: 100}
	_ = normalizeWatchArgs(&a)
	if a.ProgressIntervalMS != minWatchProgressIntervalMS {
		t.Fatalf("clamped to min = %d, want %d", a.ProgressIntervalMS, minWatchProgressIntervalMS)
	}

	// Above maximum → clamped.
	a = watchArgs{ProgressIntervalMS: maxWatchProgressIntervalMS + 100}
	_ = normalizeWatchArgs(&a)
	if a.ProgressIntervalMS != maxWatchProgressIntervalMS {
		t.Fatalf("clamped to max = %d, want %d", a.ProgressIntervalMS, maxWatchProgressIntervalMS)
	}

	// Every:1 → 0.
	a = watchArgs{Every: 1}
	_ = normalizeWatchArgs(&a)
	if a.Every != 0 {
		t.Fatalf("Every:1 should normalize to 0, got %d", a.Every)
	}

	// EventFilter with whitespace → trimmed and lowercased, empty → nil.
	a = watchArgs{EventFilter: &watchEventFilter{ToolName: "  read_file  ", Status: "  OK  "}}
	_ = normalizeWatchArgs(&a)
	if a.EventFilter.ToolName != "read_file" || a.EventFilter.Status != "ok" {
		t.Fatalf("filter not trimmed: %+v", a.EventFilter)
	}

	// EventFilter both empty after trim → nil.
	a = watchArgs{EventFilter: &watchEventFilter{ToolName: "  ", Status: ""}}
	_ = normalizeWatchArgs(&a)
	if a.EventFilter != nil {
		t.Fatalf("empty filter should be nil: %+v", a.EventFilter)
	}

	// Nil EventFilter → stays nil.
	a = watchArgs{}
	_ = normalizeWatchArgs(&a)
	if a.EventFilter != nil {
		t.Fatal("nil filter should stay nil")
	}
}

// TestCovCloneWatchEventFilter covers cloneWatchEventFilter (job_watch.go lines 1100-1106).
func TestCovCloneWatchEventFilter(t *testing.T) {
	// Nil → nil.
	if got := cloneWatchEventFilter(nil); got != nil {
		t.Fatal("nil should return nil")
	}

	// Non-nil → clone.
	orig := &watchEventFilter{ToolName: "exec", Status: "ok"}
	clone := cloneWatchEventFilter(orig)
	if clone.ToolName != "exec" || clone.Status != "ok" {
		t.Fatalf("clone = %+v", clone)
	}
	clone.ToolName = "modified"
	if orig.ToolName == "modified" {
		t.Fatal("clone should be independent")
	}
}

// TestCovCloneWatchSendArgs covers cloneWatchSendArgs (job_watch.go lines 1118-1124).
func TestCovCloneWatchSendArgs(t *testing.T) {
	// Nil → nil.
	if got := cloneWatchSendArgs(nil); got != nil {
		t.Fatal("nil should return nil")
	}

	orig := &watchSendArgs{To: "caller", Message: "hello", IncludeExcerpt: true}
	clone := cloneWatchSendArgs(orig)
	if clone.To != "caller" || clone.Message != "hello" || !clone.IncludeExcerpt {
		t.Fatalf("clone = %+v", clone)
	}
	clone.To = "modified"
	if orig.To == "modified" {
		t.Fatal("clone should be independent")
	}
}

// TestCovWatchEventFilterSummary covers watchEventFilterSummary (job_watch.go lines 2152-2163).
func TestCovWatchEventFilterSummary(t *testing.T) {
	// Nil → "".
	if got := watchEventFilterSummary(nil); got != "" {
		t.Fatalf("nil = %q", got)
	}

	// Empty → "".
	if got := watchEventFilterSummary(&watchEventFilter{}); got != "" {
		t.Fatalf("empty = %q", got)
	}

	// ToolName only.
	got := watchEventFilterSummary(&watchEventFilter{ToolName: "exec"})
	if got != "tool_name=exec" {
		t.Fatalf("tool_name = %q", got)
	}

	// Status only.
	got = watchEventFilterSummary(&watchEventFilter{Status: "ok"})
	if got != "status=ok" {
		t.Fatalf("status = %q", got)
	}

	// Both.
	got = watchEventFilterSummary(&watchEventFilter{ToolName: "exec", Status: "ok"})
	if !strings.Contains(got, "tool_name=exec") || !strings.Contains(got, "status=ok") {
		t.Fatalf("both = %q", got)
	}
}

// TestCovWatchConditionSummary covers watchConditionSummary (job_watch.go lines 2126-2150).
func TestCovWatchConditionSummary(t *testing.T) {
	// Empty → "".
	if got := watchConditionSummary(&watchConfig{}); got != "" {
		t.Fatalf("empty = %q", got)
	}

	// OutputMatch only.
	cfg := &watchConfig{outputMatch: "READY"}
	if got := watchConditionSummary(cfg); !strings.Contains(got, "output_match: READY") {
		t.Fatalf("output_match = %q", got)
	}

	// Progress only.
	cfg = &watchConfig{progressIntervalMS: 5000}
	if got := watchConditionSummary(cfg); !strings.Contains(got, "progress_interval_ms: 5000") {
		t.Fatalf("progress = %q", got)
	}

	// Wildcard events.
	cfg = &watchConfig{wildcardEvents: true}
	if got := watchConditionSummary(cfg); !strings.Contains(got, "events: [*]") {
		t.Fatalf("wildcard = %q", got)
	}

	// Named events.
	cfg = &watchConfig{events: []string{"assistant.tool"}}
	if got := watchConditionSummary(cfg); !strings.Contains(got, "events: [assistant.tool]") {
		t.Fatalf("named events = %q", got)
	}

	// Named events with every.
	cfg = &watchConfig{events: []string{"assistant.tool"}, triggerEvery: 3}
	got := watchConditionSummary(cfg)
	if !strings.Contains(got, "every 3") {
		t.Fatalf("every = %q", got)
	}

	// Named events with filter.
	cfg = &watchConfig{events: []string{"assistant.tool"}, eventFilter: &watchEventFilter{ToolName: "exec"}}
	got = watchConditionSummary(cfg)
	if !strings.Contains(got, "where tool_name=exec") {
		t.Fatalf("filter = %q", got)
	}

	// Multiple conditions.
	cfg = &watchConfig{outputMatch: "READY", progressIntervalMS: 5000}
	got = watchConditionSummary(cfg)
	if !strings.Contains(got, "output_match: READY") || !strings.Contains(got, "progress_interval_ms: 5000") {
		t.Fatalf("multi = %q", got)
	}
}

// TestCovWatchConfigMatchesReceiver covers watchConfigMatchesReceiver
// (job_watch.go lines 2098-2104).
func TestCovWatchConfigMatchesReceiver(t *testing.T) {
	// Nil → false.
	if watchConfigMatchesReceiver(nil, "sess1", "") {
		t.Fatal("nil should be false")
	}

	cfg := &watchConfig{receiverSessionID: "sess1", receiverDelegateID: "dlg_1"}
	if !watchConfigMatchesReceiver(cfg, "sess1", "dlg_1") {
		t.Fatal("exact match should be true")
	}
	if watchConfigMatchesReceiver(cfg, "sess2", "dlg_1") {
		t.Fatal("session mismatch should be false")
	}
	if watchConfigMatchesReceiver(cfg, "sess1", "dlg_2") {
		t.Fatal("delegate mismatch should be false")
	}
}

// TestCovWatchConfigVisibleToSession covers watchConfigVisibleToSession
// (job_watch.go lines 2106-2111).
func TestCovWatchConfigVisibleToSession(t *testing.T) {
	// Nil → false.
	if watchConfigVisibleToSession(nil, "sess1") {
		t.Fatal("nil should be false")
	}

	// Empty receiver → visible to all.
	cfg := &watchConfig{}
	if !watchConfigVisibleToSession(cfg, "sess1") {
		t.Fatal("empty receiver should be visible to all")
	}

	// Matching session.
	cfg = &watchConfig{receiverSessionID: "sess1"}
	if !watchConfigVisibleToSession(cfg, "sess1") {
		t.Fatal("matching session should be visible")
	}

	// Non-matching session.
	if watchConfigVisibleToSession(cfg, "sess2") {
		t.Fatal("non-matching session should not be visible")
	}
}

// TestCovWatchHistoryMatchesReceiver covers watchHistoryMatchesReceiver
// (job_watch.go lines 2113-2116).
func TestCovWatchHistoryMatchesReceiver(t *testing.T) {
	h := watchHistoryEntry{receiverSessionID: "sess1", receiverDelegateID: "dlg_1"}
	if !watchHistoryMatchesReceiver(h, "sess1", "dlg_1") {
		t.Fatal("exact match should be true")
	}
	if watchHistoryMatchesReceiver(h, "sess2", "dlg_1") {
		t.Fatal("session mismatch should be false")
	}
}

// TestCovWatchHistoryVisibleToSession covers watchHistoryVisibleToSession
// (job_watch.go lines 2118-2120).
func TestCovWatchHistoryVisibleToSession(t *testing.T) {
	// Empty receiver → visible to all.
	h := watchHistoryEntry{}
	if !watchHistoryVisibleToSession(h, "sess1") {
		t.Fatal("empty receiver should be visible to all")
	}

	// Matching session.
	h = watchHistoryEntry{receiverSessionID: "sess1"}
	if !watchHistoryVisibleToSession(h, "sess1") {
		t.Fatal("matching session should be visible")
	}

	// Non-matching session.
	if watchHistoryVisibleToSession(h, "sess2") {
		t.Fatal("non-matching session should not be visible")
	}
}

// TestCovSelfInfluenceNotice covers selfInfluenceNotice (job_watch.go lines 2401-2413).
func TestCovSelfInfluenceNotice(t *testing.T) {
	// Not self-influenced → empty.
	if got := selfInfluenceNotice(false, 0, false); got != "" {
		t.Fatalf("not self = %q", got)
	}

	// Self, shallow → default message.
	got := selfInfluenceNotice(true, 1, false)
	if !strings.Contains(got, "this turn responded to your last message") {
		t.Fatalf("shallow = %q", got)
	}

	// Self, deep → depth message.
	got = selfInfluenceNotice(true, 3, false)
	if !strings.Contains(got, "~3 exchanges deep") {
		t.Fatalf("deep = %q", got)
	}

	// Self, truncated → truncated message.
	got = selfInfluenceNotice(true, 0, true)
	if !strings.Contains(got, "many exchanges deep") {
		t.Fatalf("truncated = %q", got)
	}
}

// TestCovWatchSendKeyMatchesWatchKey covers watchSendKeyMatchesWatchKey
// (job_watch.go lines 4084-4094).
func TestCovWatchSendKeyMatchesWatchKey(t *testing.T) {
	pending := jobstore.WatchSendKey{
		VisibleSessionID:        "sess1",
		WatchTarget:             "job_1",
		ResolvedSendTo:          "caller",
		ResolvedWatchedIdentity: "watched",
	}
	key := watchKey{
		VisibleSessionID: "sess1",
		Target:           "job_1",
		SendTo:           "caller",
	}

	// Exact match.
	if !watchSendKeyMatchesWatchKey(pending, key) {
		t.Fatal("exact match should be true")
	}

	// Mismatched session.
	key.VisibleSessionID = "sess2"
	if watchSendKeyMatchesWatchKey(pending, key) {
		t.Fatal("session mismatch should be false")
	}
	key.VisibleSessionID = "sess1"

	// Mismatched target.
	key.Target = "job_2"
	if watchSendKeyMatchesWatchKey(pending, key) {
		t.Fatal("target mismatch should be false")
	}
	key.Target = "job_1"

	// Empty SendTo → matches.
	key.SendTo = ""
	if !watchSendKeyMatchesWatchKey(pending, key) {
		t.Fatal("empty SendTo should match")
	}

	// runtimeMessageAliasWatched → matches via ResolvedWatchedIdentity.
	pending.ResolvedSendTo = pending.ResolvedWatchedIdentity
	key.SendTo = runtimeMessageAliasWatched
	if !watchSendKeyMatchesWatchKey(pending, key) {
		t.Fatal("watched alias should match")
	}

	// Mismatched SendTo.
	key.SendTo = "other"
	if watchSendKeyMatchesWatchKey(pending, key) {
		t.Fatal("send mismatch should be false")
	}
}

// TestCovWatchConfigReceiverMatchesWatchKey covers watchConfigReceiverMatchesWatchKey
// (job_watch.go lines 4122+).
func TestCovWatchConfigReceiverMatchesWatchKey(t *testing.T) {
	// Nil → false.
	if watchConfigReceiverMatchesWatchKey(nil, watchKey{}) {
		t.Fatal("nil should be false")
	}

	// Empty key receiver → matches.
	cfg := &watchConfig{}
	if !watchConfigReceiverMatchesWatchKey(cfg, watchKey{}) {
		t.Fatal("empty key receiver should match")
	}

	// Matching receiver.
	cfg = &watchConfig{receiverSessionID: "sess1", receiverDelegateID: "dlg_1"}
	key := watchKey{ReceiverSessionID: "sess1", ReceiverDelegateID: "dlg_1"}
	if !watchConfigReceiverMatchesWatchKey(cfg, key) {
		t.Fatal("matching receiver should be true")
	}

	// Mismatched receiver.
	key.ReceiverSessionID = "sess2"
	if watchConfigReceiverMatchesWatchKey(cfg, key) {
		t.Fatal("mismatched receiver should be false")
	}
}

// TestCovLimitWatchText covers limitWatchText (job_watch.go lines 4881-4891).
func TestCovLimitWatchText(t *testing.T) {
	// No limit → unchanged.
	if got := limitWatchText("hello", 0); got != "hello" {
		t.Fatalf("no limit = %q", got)
	}

	// Within limit → unchanged.
	if got := limitWatchText("hello", 10); got != "hello" {
		t.Fatalf("within limit = %q", got)
	}

	// Exceeds limit → truncated with indicator.
	long := strings.Repeat("x", 100)
	got := limitWatchText(long, 20)
	if !strings.Contains(got, "[truncated]") {
		t.Fatalf("should contain truncated indicator: %q", got)
	}
	if len([]rune(got)) > 20 {
		t.Fatalf("should be at most 20 runes: %d", len([]rune(got)))
	}

	// Limit smaller than indicator → hard truncation.
	got = limitWatchText(long, 3)
	if len([]rune(got)) > 3 {
		t.Fatalf("should be at most 3 runes: %d", len([]rune(got)))
	}
}

// TestCovInheritWatchLineage covers inheritWatchLineage (job_watch.go lines 2334-2340).
func TestCovInheritWatchLineage(t *testing.T) {
	// No existing lineage.
	cfg := &watchConfig{watchID: "w3"}
	got := inheritWatchLineage(cfg)
	if len(got) != 1 || got[0] != "w3" {
		t.Fatalf("no lineage = %v", got)
	}

	// With existing lineage.
	cfg = &watchConfig{watchID: "w3", lineageWatchIDs: []string{"w1", "w2"}}
	got = inheritWatchLineage(cfg)
	if len(got) != 3 || got[0] != "w1" || got[1] != "w2" || got[2] != "w3" {
		t.Fatalf("with lineage = %v", got)
	}

	// Over cap → trimmed.
	long := make([]string, watchLineageCap+5)
	for i := range long {
		long[i] = "w" + string(rune('a'+i))
	}
	cfg = &watchConfig{watchID: "w_new", lineageWatchIDs: long}
	got = inheritWatchLineage(cfg)
	if len(got) != watchLineageCap {
		t.Fatalf("over cap = %d, want exactly %d", len(got), watchLineageCap)
	}
	want := append(append([]string{}, long[len(long)-watchLineageCap+1:]...), "w_new")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("retained lineage = %v, want most-recent lineage %v", got, want)
	}
}

// TestCovWatchFrameJob covers watchFrameJob (job_watch.go lines 3247-3259).
func TestCovWatchFrameJob(t *testing.T) {
	// JobFinishedData value.
	jobID, delegateID, ok := watchFrameJob(events.JobFinishedData{JobID: "job_1", DelegateID: "dlg_1"})
	if !ok || jobID != "job_1" || delegateID != "dlg_1" {
		t.Fatalf("value: jobID=%q delegateID=%q ok=%v", jobID, delegateID, ok)
	}

	// Pointer JobFinishedData.
	d := &events.JobFinishedData{JobID: "job_2", DelegateID: "dlg_2"}
	jobID, delegateID, ok = watchFrameJob(d)
	if !ok || jobID != "job_2" || delegateID != "dlg_2" {
		t.Fatalf("pointer: jobID=%q delegateID=%q ok=%v", jobID, delegateID, ok)
	}

	// Nil pointer.
	_, _, ok = watchFrameJob((*events.JobFinishedData)(nil))
	if ok {
		t.Fatal("nil pointer should be false")
	}

	// Empty JobID → ok=false.
	_, _, ok = watchFrameJob(events.JobFinishedData{JobID: ""})
	if ok {
		t.Fatal("empty JobID should be false")
	}

	// Other event type → false.
	_, _, ok = watchFrameJob(events.WarningData{})
	if ok {
		t.Fatal("non-JobFinished should be false")
	}
}

// TestCovJobFinishedEventData covers jobFinishedEventData (job_watch.go lines 3263-3273).
func TestCovJobFinishedEventData(t *testing.T) {
	// Value.
	data, ok := jobFinishedEventData(events.JobFinishedData{JobID: "job_1"})
	if !ok || data.JobID != "job_1" {
		t.Fatalf("value: data=%+v ok=%v", data, ok)
	}

	// Pointer.
	d := &events.JobFinishedData{JobID: "job_2"}
	data, ok = jobFinishedEventData(d)
	if !ok || data.JobID != "job_2" {
		t.Fatalf("pointer: data=%+v ok=%v", data, ok)
	}

	// Nil pointer.
	_, ok = jobFinishedEventData((*events.JobFinishedData)(nil))
	if ok {
		t.Fatal("nil pointer should be false")
	}

	// Other type.
	_, ok = jobFinishedEventData(events.WarningData{})
	if ok {
		t.Fatal("non-JobFinished should be false")
	}
}

// TestCovWatchEventFilterMatches covers watchEventFilterMatches
// (job_watch.go lines 2415+).
func TestCovWatchEventFilterMatches(t *testing.T) {
	// Nil filter → matches everything.
	if !watchEventFilterMatches(nil, events.SessionEvent{Kind: events.EventToolCallEnd}) {
		t.Fatal("nil filter should match")
	}

	// Non-ToolCallEnd event → false.
	filter := &watchEventFilter{ToolName: "exec"}
	if watchEventFilterMatches(filter, events.SessionEvent{Kind: events.EventWarning}) {
		t.Fatal("non-ToolCallEnd should not match")
	}

	// Matching tool name.
	ev := events.SessionEvent{
		Kind: events.EventToolCallEnd,
		Data: events.ToolCallEndData{ToolName: "exec"},
	}
	if !watchEventFilterMatches(filter, ev) {
		t.Fatal("matching tool name should pass")
	}

	// Non-matching tool name.
	ev.Data = events.ToolCallEndData{ToolName: "read_file"}
	if watchEventFilterMatches(filter, ev) {
		t.Fatal("non-matching tool name should fail")
	}
}

// ---- jobs.go pure functions ----

// TestCovCloneJobRecord covers cloneJobRecord (jobs.go lines 2355-2361).
func TestCovCloneJobRecord(t *testing.T) {
	// Nil → nil.
	if got := cloneJobRecord(nil); got != nil {
		t.Fatal("nil should return nil")
	}

	orig := &jobstore.JobRecord{JobID: "job_1", Status: jobstore.StatusRunning}
	clone := cloneJobRecord(orig)
	if clone.JobID != "job_1" || clone.Status != jobstore.StatusRunning {
		t.Fatalf("clone = %+v", clone)
	}
	clone.Status = jobstore.StatusCompleted
	if orig.Status == jobstore.StatusCompleted {
		t.Fatal("clone should be independent")
	}
}

// TestCovStringOutputResult covers stringOutputResult (jobs.go lines 2168-2173).
func TestCovStringOutputResult(t *testing.T) {
	// With error.
	wantErr := errors.New("read error")
	out, total, truncated, err := stringOutputResult(nil, 100, true, wantErr)
	if !errors.Is(err, wantErr) || out != "" || total != 100 || !truncated {
		t.Fatalf("error result = (%q, %d, %v, %v)", out, total, truncated, err)
	}

	// Without error.
	got, total, truncated, err := stringOutputResult([]byte("hello"), 5, false, nil)
	if err != nil || got != "hello" || total != 5 || truncated {
		t.Fatalf("got=%q total=%d truncated=%v err=%v", got, total, truncated, err)
	}
}

// TestCovAppendJobEvents covers appendJobEvents (jobs.go lines 265-278).
func TestCovAppendJobEvents(t *testing.T) {
	jm := &jobManager{}
	// Empty events → nil.
	if err := jm.appendJobEvents(nil); err != nil {
		t.Fatalf("empty: %v", err)
	}
}

// TestCovCurrentCausalProvenance covers currentCausalProvenance (jobs.go lines 283-288).
func TestCovCurrentCausalProvenance(t *testing.T) {
	// Nil manager.
	var jm *jobManager
	if got := jm.currentCausalProvenance(); got != nil {
		t.Fatal("nil manager should return nil")
	}

	// Manager with nil provenance source.
	jm = &jobManager{}
	if got := jm.currentCausalProvenance(); got != nil {
		t.Fatal("nil provenance source should return nil")
	}

	source := &provenance.Causal{
		WatchKeys:      []provenance.WatchKey{{WatchID: "watch_1", WatchGeneration: "gen_1"}},
		Chain:          []provenance.Entry{{Kind: "watch", DeliveryID: "delivery_1"}},
		ChainTruncated: true,
	}
	jm.currentProvenance = func() *provenance.Causal { return source }
	got := jm.currentCausalProvenance()
	if !reflect.DeepEqual(got, source) || got == source {
		t.Fatalf("provenance snapshot = %#v, want independent %#v", got, source)
	}
	got.WatchKeys[0].WatchID = "changed"
	got.Chain[0].DeliveryID = "changed"
	if source.WatchKeys[0].WatchID != "watch_1" || source.Chain[0].DeliveryID != "delivery_1" {
		t.Fatalf("mutating snapshot changed source: %#v", source)
	}
}

// TestCovRunningJobIDs covers runningJobIDs (jobs.go lines 1444-1454).
func TestCovRunningJobIDs(t *testing.T) {
	jm := &jobManager{running: map[string]*runningJob{}}
	// No running jobs.
	if got := jm.runningJobIDs(); len(got) != 0 {
		t.Fatalf("empty = %v", got)
	}

	// With durableStarted=true.
	jm.running["job_1"] = &runningJob{durableStarted: true}
	jm.running["job_2"] = &runningJob{durableStarted: false}
	got := jm.runningJobIDs()
	if len(got) != 1 || got[0] != "job_1" {
		t.Fatalf("durable only = %v", got)
	}
}

// TestCovLiveWorkHandles covers liveWorkHandles (jobs.go lines 407+).
func TestCovLiveWorkHandles(t *testing.T) {
	jm := &jobManager{running: map[string]*runningJob{}}
	// No running jobs.
	if got := jm.liveWorkHandles(); len(got) != 0 {
		t.Fatalf("empty = %v", got)
	}

	// With a shell job that has a working dir.
	jm.running["job_1"] = &runningJob{
		rec: &jobstore.JobRecord{Type: jobstore.JobShell, WorkingDir: "/work"},
	}
	jm.running["job_2"] = &runningJob{
		rec: &jobstore.JobRecord{Type: jobstore.JobType(delegateResourceType)},
	}
	jm.running["job_3"] = &runningJob{}
	got := jm.liveWorkHandles()
	if len(got) != 1 || got[0].dir != "/work" {
		t.Fatalf("shell only = %v", got)
	}
}

// TestCovLiveShellHandles covers liveShellHandles (jobs.go lines 429+).
func TestCovLiveShellHandles(t *testing.T) {
	jm := &jobManager{running: map[string]*runningJob{}}
	// No running jobs.
	if got := jm.liveShellHandles(); len(got) != 0 {
		t.Fatalf("empty = %v", got)
	}

	// With a shell job that has working dir.
	jm.running["job_1"] = &runningJob{
		rec: &jobstore.JobRecord{Type: jobstore.JobShell, WorkingDir: "/work", JobID: "job_1"},
	}
	got := jm.liveShellHandles()
	if len(got) != 1 || got[0].dir != "/work" {
		t.Fatalf("shell = %v", got)
	}

	// Non-shell job should be excluded.
	jm.running["job_2"] = &runningJob{
		rec: &jobstore.JobRecord{Type: jobstore.JobType(delegateResourceType)},
	}
	got = jm.liveShellHandles()
	if len(got) != 1 {
		t.Fatalf("non-shell excluded = %v", got)
	}

	// Shell without working dir excluded.
	jm.running["job_3"] = &runningJob{
		rec: &jobstore.JobRecord{Type: jobstore.JobShell},
	}
	got = jm.liveShellHandles()
	if len(got) != 1 {
		t.Fatalf("no-workdir shell excluded = %v", got)
	}
}

// ---- delegate_runtime.go pure functions ----

// TestCovDelegateQuietAttentionID covers delegateQuietAttentionID and
// delegateQuietAttentionIDForStretch (delegate_runtime.go lines 160-166).
func TestCovDelegateQuietAttentionID(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 2}
	got := delegateQuietAttentionID(lease)
	if got != "quiet:dlg_1:2:1" {
		t.Fatalf("got = %q", got)
	}

	got = delegateQuietAttentionIDForStretch(lease, 3)
	if got != "quiet:dlg_1:2:3" {
		t.Fatalf("stretch = %q", got)
	}
}

// TestCovDelegateQuietAttentionContent covers delegateQuietAttentionContent
// (delegate_runtime.go lines 168-174).
func TestCovDelegateQuietAttentionContent(t *testing.T) {
	lease := delegateLease{delegateID: "dlg_1", generation: 1}
	now := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	got := delegateQuietAttentionContent(lease, now)
	if !strings.Contains(got, "dlg_1") || !strings.Contains(got, "delegate-notification") {
		t.Fatalf("content = %q", got)
	}
}

// ---- subagents.go pure functions ----

// TestCovCommunicateNudge covers communicateNudge (subagents.go lines 1475-1480).
func TestCovCommunicateNudge(t *testing.T) {
	got := communicateNudge("communicate")
	if !strings.Contains(got, "communicate") || !strings.Contains(got, "end_turn=true") {
		t.Fatalf("nudge = %q", got)
	}
}

// TestCovFatalRunGatedSnapshot covers fatalRunGatedSnapshot (subagents.go lines 1456-1463).
func TestCovFatalRunGatedSnapshot(t *testing.T) {
	// Nil subagent.
	var a *subagent
	if a.fatalRunGatedSnapshot() {
		t.Fatal("nil subagent should return false")
	}

	// Non-nil with flag false.
	a = &subagent{}
	if a.fatalRunGatedSnapshot() {
		t.Fatal("false flag should return false")
	}

	// Non-nil with flag true.
	a = &subagent{fatalRunGated: true}
	if !a.fatalRunGatedSnapshot() {
		t.Fatal("true flag should return true")
	}
}

// TestCovChildFatalRunGated covers childFatalRunGated (subagents.go lines 1465-1471).
func TestCovChildFatalRunGated(t *testing.T) {
	// Nil session.
	var s *Session
	if s.childFatalRunGated("child1") {
		t.Fatal("nil session should return false")
	}

	// Empty child session ID.
	s = &Session{}
	if s.childFatalRunGated("") {
		t.Fatal("empty child should return false")
	}

	s.subagents = newSubagentManager(nil, 1)
	child := &subagent{id: "child1", fatalRunGated: true}
	s.subagents.track(child)
	if !s.childFatalRunGated("child1") {
		t.Fatal("tracked fatal-gated child should return true")
	}
	if s.childFatalRunGated("other") {
		t.Fatal("unknown child should return false")
	}
}

// ---- session_attention.go pure functions ----

// TestCovDelegateTranscriptPathFromRef covers delegateTranscriptPathFromRef
// (session_attention.go lines 22-31).
func TestCovDelegateTranscriptPathFromRef(t *testing.T) {
	// Valid ref with empty projectID.
	stateDir := t.TempDir()
	path, sessionID, err := delegateTranscriptPathFromRef(stateDir, "local:SESS123")
	if err != nil {
		t.Fatalf("valid ref: %v", err)
	}
	if sessionID != "SESS123" {
		t.Fatalf("sessionID = %q", sessionID)
	}
	if path == "" {
		t.Fatal("path should not be empty")
	}

	// Invalid ref.
	_, _, err = delegateTranscriptPathFromRef(stateDir, "not-a-ref")
	if err == nil {
		t.Fatal("invalid ref should error")
	}

	// Ref with non-empty projectID → error.
	_, _, err = delegateTranscriptPathFromRef(stateDir, "project1:SESS123")
	if err == nil {
		t.Fatal("non-empty projectID should error")
	}
}

// ---- jobs_nested.go pure functions ----

// TestCovKeepIncomingDescendantRow covers keepIncomingDescendantRow
// (jobs_nested.go lines 122-127).
func TestCovKeepIncomingDescendantRow(t *testing.T) {
	// Not seen → always keep.
	if !keepIncomingDescendantRow(false, false, false) {
		t.Fatal("not seen should keep")
	}

	// Seen, existing is owner, incoming is not → don't keep.
	if keepIncomingDescendantRow(true, true, false) {
		t.Fatal("existing owner should not keep non-owner")
	}

	// Seen, existing is owner, incoming IS owner → don't keep (keep shallower).
	if keepIncomingDescendantRow(true, true, true) {
		t.Fatal("existing owner should keep over incoming owner")
	}

	// Seen, existing is not owner, incoming IS owner → keep.
	if !keepIncomingDescendantRow(true, false, true) {
		t.Fatal("incoming owner should replace non-owner")
	}

	// Seen, neither is owner → don't keep (keep shallower).
	if keepIncomingDescendantRow(true, false, false) {
		t.Fatal("non-owner vs non-owner should not keep")
	}
}
