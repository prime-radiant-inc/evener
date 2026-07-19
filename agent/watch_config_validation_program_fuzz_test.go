//go:build serffuzz

package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
)

// FuzzWatchConfigValidationProgram exercises the job_watch installation boundary
// from its parsed arguments through the durable job-manager state. It uses only
// test-owned job stores and a fake clock: createShell creates a record and output
// file but never starts a process, and no Session/provider/network boundary is
// reached. The semantic oracles are configuration canonicalization (equivalent
// argument order yields an equivalent config), validation determinism, and live
// watch-state consistency after install, replace, clear, and terminal catch-up.
func FuzzWatchConfigValidationProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		{1, 2, 3, 4},
		{255, 254, 1, 0, 17, 99},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &wcvpReader{data: data}
		wcvpAssertPureContracts(t, r)
		wcvpAssertSendTargetValidation(t, r)
		wcvpAssertManagerTargetValidation(t, r)
		wcvpAssertConfigureLifecycle(t, r)
		wcvpAssertTokenAndSnapshotContracts(t, r)
		wcvpAssertClosedStoreFailures(t)
	})
}

type wcvpReader struct {
	data []byte
	pos  int
}

func (r *wcvpReader) byte() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *wcvpReader) text() string {
	parts := []string{"alpha", " beta ", "\n", "ready", "tool", "_"}
	n := int(r.byte()%5) + 1
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(parts[int(r.byte())%len(parts)])
	}
	return b.String()
}

func wcvpNewManager(t *testing.T) *jobManager {
	t.Helper()
	jm := newTestJM(t)
	jm.clock = agenttest.NewFakeClock()
	jm.closeGrace = 0
	t.Cleanup(func() {
		jm.abandonRunningJobs()
		_ = jm.close()
	})
	return jm
}

func wcvpErrorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func wcvpRequireErrorPrefix(t *testing.T, err error, prefix string) {
	t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("error = %v, want prefix %q", err, prefix)
	}
}

func wcvpAssertPureContracts(t *testing.T, r *wcvpReader) {
	t.Helper()

	names := availableEventKindNames()
	if len(names) != len(WatchEventKindNames) {
		t.Fatalf("available event names = %v, want copy of %v", names, WatchEventKindNames)
	}
	names[0] = "mutated"
	if WatchEventKindNames[0] == "mutated" {
		t.Fatal("availableEventKindNames leaked the canonical backing slice")
	}
	if got := formatQuietWindow(2 * time.Minute); got != "2m" {
		t.Fatalf("whole-minute quiet window = %q, want 2m", got)
	}
	if got := formatQuietWindow(1500 * time.Millisecond); got != "1.5s" {
		t.Fatalf("sub-minute quiet window = %q, want 1.5s", got)
	}
	if got := quietWatchdogMessage(2*time.Minute, frozenTestTime); !strings.Contains(got, "2m") || !strings.Contains(got, frozenTestTime.Format(time.RFC3339Nano)) {
		t.Fatalf("quiet watchdog message = %q", got)
	}
	if got := watchBudgetClearedMessage("job_wcvp"); !strings.Contains(got, "job_wcvp") || !strings.Contains(got, fmt.Sprint(watchDeliveryBudget)) {
		t.Fatalf("budget-cleared message = %q", got)
	}

	for _, tc := range []struct {
		input string
		kind  watchSourceKind
		ok    bool
	}{
		{" self ", watchSourceSelfSession, true},
		{"parent", watchSourceParentSession, true},
		{"job_wcvp", watchSourceConcreteJob, true},
		{"", 0, false},
		{"dlg_wcvp", 0, false},
	} {
		got, err := normalizeWatchSource(tc.input)
		if (err == nil) != tc.ok || (tc.ok && got.Kind != tc.kind) {
			t.Fatalf("normalizeWatchSource(%q) = (%+v, %v), want kind=%d ok=%v", tc.input, got, err, tc.kind, tc.ok)
		}
	}
	if got := watchPublicSource("", runtimeMessageAliasCaller); got != "self" {
		t.Fatalf("watchPublicSource caller fallback = %q", got)
	}
	if got := watchPublicSource(" ", "job_wcvp"); got != "job_wcvp" {
		t.Fatalf("watchPublicSource target fallback = %q", got)
	}
	if got := watchPublicSource(" parent ", "job_wcvp"); got != "parent" {
		t.Fatalf("watchPublicSource explicit source = %q", got)
	}

	negative := watchArgs{ProgressIntervalMS: -1}
	wcvpRequireErrorPrefix(t, normalizeWatchArgs(&negative), "invalid_request: progress_interval_ms")
	normalized := watchArgs{
		ProgressIntervalMS: 1,
		Every:              1,
		EventFilter:        &watchEventFilter{ToolName: "  ", Status: "  "},
	}
	if err := normalizeWatchArgs(&normalized); err != nil {
		t.Fatalf("normalize valid args: %v", err)
	}
	if normalized.ProgressIntervalMS != minWatchProgressIntervalMS || normalized.Every != 0 || normalized.EventFilter != nil {
		t.Fatalf("normalized args = %+v", normalized)
	}
	clamped := watchArgs{ProgressIntervalMS: maxWatchProgressIntervalMS + 1, EventFilter: &watchEventFilter{ToolName: "  read_file ", Status: " ERROR "}}
	if err := normalizeWatchArgs(&clamped); err != nil {
		t.Fatalf("normalize clamped args: %v", err)
	}
	if clamped.ProgressIntervalMS != maxWatchProgressIntervalMS || clamped.EventFilter.ToolName != "read_file" || clamped.EventFilter.Status != "error" {
		t.Fatalf("clamped args = %+v", clamped)
	}

	for _, tc := range []struct {
		args   watchArgs
		prefix string
	}{
		{watchArgs{Events: []string{"assistant.message"}}, "invalid_request: assistant.message"},
		{watchArgs{Events: []string{"not-real"}}, "invalid_request: unknown event"},
		{watchArgs{Every: 2}, "invalid_request: every requires exactly"},
		{watchArgs{Events: []string{"*"}, Every: 2}, "invalid_request: every requires a single concrete"},
		{watchArgs{EventFilter: &watchEventFilter{}}, "invalid_request: event_filter requires events"},
		{watchArgs{Events: []string{"communicate"}, EventFilter: &watchEventFilter{}}, "invalid_request: event_filter matches assistant.tool events; parent"},
		{watchArgs{Events: []string{"job.notification"}, EventFilter: &watchEventFilter{}}, "invalid_request: event_filter matches assistant.tool events; use"},
		{watchArgs{Events: []string{"assistant.tool"}, EventFilter: &watchEventFilter{Status: "bad"}}, "invalid_request: event_filter.status"},
	} {
		wcvpRequireErrorPrefix(t, validateWatchEventArgs(tc.args), tc.prefix)
	}
	if err := validateWatchEventArgs(watchArgs{Events: []string{"assistant.tool"}, Every: 2, EventFilter: &watchEventFilter{ToolName: "read_file", Status: "ok"}}); err != nil {
		t.Fatalf("valid event args: %v", err)
	}
	wcvpRequireErrorPrefix(t, validateWatchTriggerShape(watchArgs{Target: runtimeMessageAliasCaller, ProgressIntervalMS: 1000, Events: []string{"communicate"}}), "invalid_request: session event watches")
	if err := validateWatchTriggerShape(watchArgs{Target: "job_wcvp", ProgressIntervalMS: 1000, Events: []string{"communicate"}}); err != nil {
		t.Fatalf("concrete trigger shape: %v", err)
	}

	if !isSupportedWatchEventKind(events.EventToolCallEnd) || isSupportedWatchEventKind(events.EventError) {
		t.Fatal("supported event-kind boundary is inconsistent")
	}
	resolved, wildcard := resolveEventKinds([]string{"assistant.tool", "not-real", "*", "communicate"})
	if !wildcard || !resolved[events.EventToolCallEnd] || !resolved[events.EventCommunicate] || len(resolved) != 2 {
		t.Fatalf("resolved event kinds = %v wildcard=%v", resolved, wildcard)
	}
	if got := canonicalWatchEvents([]string{"communicate", "assistant.tool"}); strings.Join(got, ",") != "assistant.tool,communicate" {
		t.Fatalf("canonical events = %v", got)
	}
	if canonicalWatchEvents(nil) != nil {
		t.Fatal("nil events must remain nil")
	}

	a := watchArgs{
		Target:             "job_wcvp",
		OutputMatch:        "ready",
		Events:             []string{"assistant.tool"},
		Every:              2,
		EventFilter:        &watchEventFilter{ToolName: "read_file", Status: "ok"},
		Send:               &watchSendArgs{To: "dlg_wcvp", Message: r.text(), IncludeExcerpt: true},
		ReceiverSessionID:  " receiver ",
		ReceiverDelegateID: " dlg_wcvp ",
	}
	cfg, err := newWatchConfig(a, frozenTestTime)
	if err != nil {
		t.Fatalf("newWatchConfig: %v", err)
	}
	if cfg.watchID == "" || cfg.generation == "" || cfg.triggerEvery != 2 || cfg.triggerKind != events.EventToolCallEnd || cfg.createdAt != frozenTestTime {
		t.Fatalf("new watch config = %+v", cfg)
	}
	if cfg.send == a.Send || cfg.eventFilter == a.EventFilter || strings.Join(cfg.events, ",") != "assistant.tool" {
		t.Fatalf("config did not snapshot mutable arguments: %+v", cfg)
	}
	a.Send.Message = "mutated"
	a.EventFilter.ToolName = "mutated"
	a.Events[0] = "job.notification"
	if cfg.send.Message == "mutated" || cfg.eventFilter.ToolName == "mutated" || cfg.events[0] != "assistant.tool" {
		t.Fatalf("config changed after caller mutation: %+v", cfg)
	}
	permuted := watchArgs{
		Target:             "job_wcvp",
		OutputMatch:        "ready",
		Events:             []string{"assistant.tool"},
		Every:              2,
		EventFilter:        &watchEventFilter{ToolName: "read_file", Status: "ok"},
		Send:               &watchSendArgs{To: "dlg_wcvp", Message: cfg.send.Message, IncludeExcerpt: true},
		ReceiverSessionID:  "receiver",
		ReceiverDelegateID: "dlg_wcvp",
	}
	if got := normalizedWatchConfigHash(permuted); got != cfg.configHash {
		t.Fatalf("equivalent config hash = %q, want %q", got, cfg.configHash)
	}
	permuted.ReceiverDelegateID = "dlg_other"
	if normalizedWatchConfigHash(permuted) == cfg.configHash {
		t.Fatal("receiver identity must contribute to config hash")
	}
	wcvpRequireErrorPrefix(t, func() error {
		_, err := newWatchConfig(watchArgs{Target: "job_wcvp", OutputMatch: "("}, frozenTestTime)
		return err
	}(), "invalid_request: output_match")
	receiver := watchArgs{ReceiverDelegateID: " dlg_receiver "}
	applyReceiverWatchSend(&receiver)
	if receiver.Send == nil || receiver.Send.To != "dlg_receiver" || !receiver.ReceiverSendInternal {
		t.Fatalf("receiver send projection = %+v", receiver)
	}

	delivery := watchSendDelivery{}.withSelfInfluence(selfInfluence{self: true, gradientDepth: 2, fuseDepth: 3, truncated: true})
	if !delivery.selfInfluence || delivery.gradientDepth != 2 || delivery.fuseDepth != 3 || !delivery.truncated {
		t.Fatalf("self influence projection = %+v", delivery)
	}
}

func wcvpAssertSendTargetValidation(t *testing.T, r *wcvpReader) {
	t.Helper()
	owned := "S1"
	resumable := true
	notResumable := false
	delegates := map[string]*jobstore.DelegateRecord{
		"dlg_good":    {DelegateID: "dlg_good", OwnerSessionID: owned, CurrentJobID: "job_good", Resumable: true},
		"dlg_latest":  {DelegateID: "dlg_latest", OwnerSessionID: owned, LatestJobID: "job_good", Resumable: true},
		"dlg_foreign": {DelegateID: "dlg_foreign", OwnerSessionID: "OTHER", CurrentJobID: "job_good", Resumable: true},
		"dlg_nojob":   {DelegateID: "dlg_nojob", OwnerSessionID: owned, Resumable: true},
		"dlg_desc":    {DelegateID: "dlg_desc", OwnerSessionID: owned, CurrentJobID: "job_desc", Resumable: true},
		"dlg_shell":   {DelegateID: "dlg_shell", OwnerSessionID: owned, CurrentJobID: "job_shell", Resumable: true},
		"dlg_off":     {DelegateID: "dlg_off", OwnerSessionID: owned, CurrentJobID: "job_good", Resumable: false},
		"dlg_status":  {DelegateID: "dlg_status", OwnerSessionID: owned, CurrentJobID: "job_good", Resumable: true, Status: jobstore.DelegateNotResumable},
		"dlg_term":    {DelegateID: "dlg_term", OwnerSessionID: owned, CurrentJobID: "job_term", Resumable: true},
		"dlg_finderr": {DelegateID: "dlg_finderr", OwnerSessionID: owned, CurrentJobID: "job_finderr", Resumable: true},
	}
	records := map[string]*jobstore.JobRecord{
		"job_good":  {JobID: "job_good", Type: jobstore.JobDelegate, OwnerSessionID: owned, Status: jobstore.StatusRunning, Resumable: &resumable},
		"job_desc":  {JobID: "job_desc", Type: jobstore.JobDelegate, OwnerSessionID: "CHILD", Status: jobstore.StatusRunning, Resumable: &resumable},
		"job_shell": {JobID: "job_shell", Type: jobstore.JobShell, OwnerSessionID: owned, Status: jobstore.StatusRunning, Resumable: &resumable},
		"job_term":  {JobID: "job_term", Type: jobstore.JobDelegate, OwnerSessionID: owned, Status: jobstore.StatusCompleted, Resumable: &notResumable},
	}
	resolver := watchSendTargetResolver{
		sessionID:     owned,
		hasJobManager: true,
		loadDelegates: func() (map[string]*jobstore.DelegateRecord, error) { return delegates, nil },
		findJobRecord: func(jobID string) (*jobstore.JobRecord, error) {
			if jobID == "job_finderr" {
				return nil, errors.New("injected job lookup")
			}
			return records[jobID], nil
		},
	}
	cases := []struct {
		target string
		prefix string
	}{
		{"", "invalid_request: internal watch delivery target is required"},
		{runtimeMessageAliasCaller, ""},
		{runtimeMessageAliasWatched, "invalid_request: watched"},
		{"main", "target_not_found:"},
		{"*", "target_not_found:"},
		{"job_wcvp", "invalid_request: job_id"},
		{"unknown", "target_not_found:"},
		{"dlg_missing", "target_not_found: delegate"},
		{"dlg_foreign", "not_controllable: delegate"},
		{"dlg_nojob", "target_not_resumable: delegate"},
		{"dlg_finderr", "target_not_found:"},
		{"dlg_desc", "not_controllable: delegate job"},
		{"dlg_shell", "target_not_messageable: job"},
		{"dlg_off", "target_not_resumable: delegate"},
		{"dlg_status", "target_not_resumable: delegate"},
		{"dlg_term", "target_not_resumable: delegate job"},
		{"dlg_good", ""},
		{"dlg_latest", ""},
	}
	for _, tc := range cases {
		args := watchArgs{Source: r.text()}
		first := wcvpErrorString(validateWatchSendDeliveryTarget(tc.target, args, resolver))
		second := wcvpErrorString(validateWatchSendDeliveryTarget(tc.target, args, resolver))
		if first != second {
			t.Fatalf("send validation is not deterministic for %q: %q then %q", tc.target, first, second)
		}
		if tc.prefix == "" {
			if first != "" {
				t.Fatalf("send validation %q = %q, want success", tc.target, first)
			}
		} else if !strings.HasPrefix(first, tc.prefix) {
			t.Fatalf("send validation %q = %q, want prefix %q", tc.target, first, tc.prefix)
		}
	}

	loadErr := resolver
	loadErr.loadDelegates = func() (map[string]*jobstore.DelegateRecord, error) {
		return nil, errors.New("injected delegate lookup")
	}
	if got := wcvpErrorString(validateWatchSendDeliveryTarget("dlg_good", watchArgs{}, loadErr)); got != "injected delegate lookup" {
		t.Fatalf("delegate lookup failure = %q", got)
	}
}

func wcvpAppendStoreJob(t *testing.T, jm *jobManager, jobID, owner string, status jobstore.Status) {
	t.Helper()
	now := jm.now()
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		Type:             jobstore.JobShell,
		OwnerSessionID:   owner,
		VisibleToSession: jm.sessionID,
		StartedAt:        &now,
	}); err != nil {
		t.Fatalf("seed job %q: %v", jobID, err)
	}
	if !status.IsTerminal() {
		return
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:    jobstore.EventJobFinished,
		TS:      now,
		JobID:   jobID,
		Status:  status,
		Reason:  "wcvp terminal",
		EndedAt: &now,
	}); err != nil {
		t.Fatalf("finish seed job %q: %v", jobID, err)
	}
}

func wcvpAssertManagerTargetValidation(t *testing.T, r *wcvpReader) {
	t.Helper()

	runtimeJM := wcvpNewManager(t)
	runRec, err := runtimeJM.createShell(createShellOpts{Command: "wcvp record only"})
	if err != nil {
		t.Fatalf("create runtime target: %v", err)
	}
	if err := runtimeJM.validateWatchTarget(runRec.JobID); err != nil {
		t.Fatalf("running target validation: %v", err)
	}
	if status, terminal, err := runtimeJM.terminalWatchTargetStatus(runRec.JobID); err != nil || terminal || status != "" {
		t.Fatalf("running target status = (%q, %v, %v)", status, terminal, err)
	}
	runtimeJM.mu.Lock()
	runtimeJM.running[runRec.JobID].terminal = &terminalJob{status: jobstore.StatusCompleted}
	runtimeJM.mu.Unlock()
	wcvpRequireErrorPrefix(t, runtimeJM.validateWatchTarget(runRec.JobID), "target_terminal:")
	if status, terminal, err := runtimeJM.terminalWatchTargetStatus(runRec.JobID); err != nil || !terminal || status != jobstore.StatusCompleted {
		t.Fatalf("runtime terminal status = (%q, %v, %v)", status, terminal, err)
	}
	runtimeJM.mu.Lock()
	runtimeJM.running[runRec.JobID].terminal = nil
	runtimeJM.running[runRec.JobID].finalize = &finalizeAttempt{done: make(chan struct{})}
	runtimeJM.mu.Unlock()
	wcvpRequireErrorPrefix(t, runtimeJM.validateWatchTarget(runRec.JobID), "target_terminal: job")
	if status, terminal, err := runtimeJM.terminalWatchTargetStatus(runRec.JobID); err != nil || terminal || status != "" {
		t.Fatalf("finalizing target status = (%q, %v, %v)", status, terminal, err)
	}

	storeJM := wcvpNewManager(t)
	wcvpAppendStoreJob(t, storeJM, "job_wcvp_live", storeJM.sessionID, jobstore.StatusRunning)
	wcvpAppendStoreJob(t, storeJM, "job_wcvp_terminal", storeJM.sessionID, jobstore.StatusFailed)
	wcvpAppendStoreJob(t, storeJM, "job_wcvp_nested", "CHILD", jobstore.StatusRunning)
	if err := storeJM.validateWatchTarget(runtimeMessageAliasCaller); err != nil {
		t.Fatalf("caller target validation: %v", err)
	}
	if err := storeJM.validateWatchTarget("*"); err != nil {
		t.Fatalf("wildcard target validation: %v", err)
	}
	if err := storeJM.validateWatchTarget("job_wcvp_live"); err != nil {
		t.Fatalf("stored running target validation: %v", err)
	}
	wcvpRequireErrorPrefix(t, storeJM.validateWatchTarget("job_wcvp_terminal"), "target_terminal:")
	wcvpRequireErrorPrefix(t, storeJM.validateWatchTarget("job_wcvp_nested"), "target_not_watchable:")
	wcvpRequireErrorPrefix(t, storeJM.validateWatchTarget("job_wcvp_missing"), "target_not_found:")
	if status, terminal, err := storeJM.terminalWatchTargetStatus("job_wcvp_terminal"); err != nil || !terminal || status != jobstore.StatusFailed {
		t.Fatalf("stored terminal target status = (%q, %v, %v)", status, terminal, err)
	}
	if status, terminal, err := storeJM.terminalWatchTargetStatus("job_wcvp_nested"); err != nil || terminal || status != "" {
		t.Fatalf("nested target catch-up status = (%q, %v, %v)", status, terminal, err)
	}
	if status, terminal, err := storeJM.terminalWatchTargetStatus("job_wcvp_missing"); err != nil || terminal || status != "" {
		t.Fatalf("missing target catch-up status = (%q, %v, %v)", status, terminal, err)
	}
	if status, terminal, err := storeJM.terminalWatchTargetStatus(runtimeMessageAliasCaller); err != nil || terminal || status != "" {
		t.Fatalf("session catch-up status = (%q, %v, %v)", status, terminal, err)
	}

	// The public helper normalizes an explicit source, while receiver identity
	// and target validation trim only the fields their
	// contracts name. This string changes the valid message payload but never
	// changes target selection, so it gives the native fuzzer structured input
	// without introducing ambient behavior.
	if got := watchPublicSource("source-"+r.text(), "job_wcvp_live"); got == "" {
		t.Fatal("a generated explicit source unexpectedly became empty")
	}
}

func wcvpAssertWatchConfigInvariants(t *testing.T, jm *jobManager) {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	seen := make(map[string]bool, len(jm.watches))
	for key, cfg := range jm.watches {
		if cfg == nil || cfg.watchID == "" || cfg.generation == "" || cfg.configHash == "" {
			t.Fatalf("invalid live watch for key %+v: %+v", key, cfg)
		}
		if seen[cfg.watchID] {
			t.Fatalf("duplicate live watch id %q", cfg.watchID)
		}
		seen[cfg.watchID] = true
		if !wcvpSortedStrings(cfg.events) {
			t.Fatalf("watch events are not canonical: %v", cfg.events)
		}
		for pendingKey, state := range cfg.pending {
			if state == nil || pendingKey != state.Key {
				t.Fatalf("invalid pending state for %+v: %+v", pendingKey, state)
			}
		}
	}
}

func wcvpSortedStrings(values []string) bool {
	for i := 1; i < len(values); i++ {
		if values[i-1] > values[i] {
			return false
		}
	}
	return true
}

func wcvpConfigure(t *testing.T, jm *jobManager, args watchArgs) watchResult {
	t.Helper()
	result, err := jm.configureWatch(args)
	if err != nil {
		t.Fatalf("configureWatch(%+v): %v", args, err)
	}
	wcvpAssertWatchConfigInvariants(t, jm)
	return result
}

func wcvpAssertConfigureLifecycle(t *testing.T, r *wcvpReader) {
	t.Helper()
	jm := wcvpNewManager(t)
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")
	run, err := jm.createShell(createShellOpts{Command: "wcvp target record"})
	if err != nil {
		t.Fatalf("create config target: %v", err)
	}

	// Rejections must never leave an installed watch behind. Keep the before/after
	// invariant as the oracle rather than merely checking error text.
	rejects := []watchArgs{
		{},
		{Target: runtimeMessageAliasCaller, ProgressIntervalMS: -1},
		{Target: "job_wcvp_missing", OutputMatch: "ready", Clear: true},
		{Target: runtimeMessageAliasCaller, OutputMatch: "ready"},
		{Target: runtimeMessageAliasCaller, OutputMatch: "ready", Events: []string{"communicate"}},
		{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, Send: &watchSendArgs{To: runtimeMessageAliasCaller, IncludeExcerpt: true}},
		{Target: runtimeMessageAliasCaller, Events: []string{"not-real"}},
		{Target: runtimeMessageAliasCaller, Events: []string{"communicate", "job.notification"}, Every: 2},
		{Target: runtimeMessageAliasCaller, ProgressIntervalMS: 1000, Events: []string{"communicate"}},
		{Target: run.JobID, OutputMatch: "("},
		{Target: runtimeMessageAliasCaller, Events: []string{"job.notification"}, Send: &watchSendArgs{To: "dlg_missing", Message: r.text()}},
	}
	for _, args := range rejects {
		before := jm.watchCount()
		if _, err := jm.configureWatch(args); err == nil {
			t.Fatalf("invalid watch unexpectedly installed: %+v", args)
		}
		if jm.watchCount() != before {
			t.Fatalf("rejected watch changed count %d -> %d: %+v", before, jm.watchCount(), args)
		}
		wcvpAssertWatchConfigInvariants(t, jm)
	}

	// A session receiver is itself a condition. It must install as a real live
	// watch even without output/progress/events, then clear by the same routing
	// identity. This reaches the parent-observer shape before a child Session is
	// involved, without fabricating a subagent.
	receiverArgs := watchArgs{Target: runtimeMessageAliasCaller, ReceiverSessionID: " receiver-session "}
	receiver := wcvpConfigure(t, jm, receiverArgs)
	if !receiver.Watching || receiver.Source != "self" {
		t.Fatalf("receiver watch = %+v", receiver)
	}
	clearedReceiver := wcvpConfigure(t, jm, watchArgs{Target: runtimeMessageAliasCaller, ReceiverSessionID: "receiver-session", Clear: true})
	if clearedReceiver.Watching {
		t.Fatalf("receiver clear = %+v", clearedReceiver)
	}

	callerArgs := watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"job.notification", "assistant.tool"},
		Send:   &watchSendArgs{To: "dlg_obs", Message: "watch " + r.text()},
	}
	first := wcvpConfigure(t, jm, callerArgs)
	if !first.Watching || first.WatchID == "" || first.Send == nil || first.Send.To != "dlg_obs" {
		t.Fatalf("caller watch = %+v", first)
	}
	again := wcvpConfigure(t, jm, callerArgs)
	if again.WatchID != first.WatchID || again.ReplacedExisting {
		t.Fatalf("idempotent caller configure = %+v, first=%+v", again, first)
	}
	replacementArgs := callerArgs
	replacementArgs.Send = &watchSendArgs{To: "dlg_obs", Message: "replacement " + r.text()}
	replacement := wcvpConfigure(t, jm, replacementArgs)
	if replacement.WatchID == first.WatchID || !replacement.ReplacedExisting {
		t.Fatalf("caller replacement = %+v, first=%+v", replacement, first)
	}
	cleared := wcvpConfigure(t, jm, watchArgs{Target: runtimeMessageAliasCaller, Events: callerArgs.Events, Send: replacementArgs.Send, Clear: true})
	if cleared.Watching {
		t.Fatalf("caller clear = %+v, replacement=%+v", cleared, replacement)
	}

	progress := wcvpConfigure(t, jm, watchArgs{Target: run.JobID, ProgressIntervalMS: 1})
	if progress.ProgressIntervalMS != minWatchProgressIntervalMS {
		t.Fatalf("clamped concrete progress = %+v", progress)
	}
	if got := wcvpConfigure(t, jm, watchArgs{Target: run.JobID, ProgressIntervalMS: 1}); got.WatchID != progress.WatchID {
		t.Fatalf("idempotent progress configure = %+v, first=%+v", got, progress)
	}
	if got := wcvpConfigure(t, jm, watchArgs{Target: run.JobID, ProgressIntervalMS: maxWatchProgressIntervalMS + 1}); !got.ReplacedExisting || got.ProgressIntervalMS != maxWatchProgressIntervalMS {
		t.Fatalf("progress replacement = %+v", got)
	}
	if got := wcvpConfigure(t, jm, watchArgs{Target: run.JobID, ProgressIntervalMS: maxWatchProgressIntervalMS + 1, Clear: true}); got.Watching {
		t.Fatalf("progress clear = %+v", got)
	}

	concrete := wcvpConfigure(t, jm, watchArgs{Target: run.JobID, OutputMatch: "ready"})
	if !concrete.Watching || concrete.OutputMatch != "ready" {
		t.Fatalf("concrete output watch = %+v", concrete)
	}
	if got := wcvpConfigure(t, jm, watchArgs{Target: run.JobID, OutputMatch: "ready"}); got.WatchID != concrete.WatchID {
		t.Fatalf("idempotent concrete configure = %+v, first=%+v", got, concrete)
	}
	if got := wcvpConfigure(t, jm, watchArgs{Target: run.JobID, OutputMatch: "done"}); !got.ReplacedExisting || got.WatchID == concrete.WatchID {
		t.Fatalf("concrete replacement = %+v, first=%+v", got, concrete)
	}
	if got := wcvpConfigure(t, jm, watchArgs{Target: run.JobID, OutputMatch: "done", Clear: true}); got.Watching {
		t.Fatalf("concrete clear = %+v", got)
	}

	// Terminal catch-up is intentionally a one-shot, not an installed live watch.
	if _, err := jm.appendJobOutput(run.JobID, jm.running[run.JobID].output, []byte("ready terminal\n")); err != nil {
		t.Fatalf("append terminal output: %v", err)
	}
	if err := jm.finalize(run.JobID, jobstore.StatusCompleted, "wcvp terminal", nil); err != nil {
		t.Fatalf("finalize concrete target: %v", err)
	}
	catchup := wcvpConfigure(t, jm, watchArgs{Target: run.JobID, OutputMatch: "ready"})
	if catchup.Watching || !catchup.TerminalCatchup || !catchup.Fired || catchup.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("terminal catch-up = %+v", catchup)
	}
	if got := wcvpConfigure(t, jm, watchArgs{Target: run.JobID, Clear: true}); got.Watching {
		t.Fatalf("terminal clear = %+v", got)
	}
	if _, err := jm.configureWatch(watchArgs{Target: run.JobID, OutputMatch: "ready", Send: &watchSendArgs{To: "dlg_missing", Message: r.text()}}); err == nil {
		t.Fatal("terminal catch-up send to a missing delegate unexpectedly succeeded")
	}
	if got := wcvpConfigure(t, jm, watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, Clear: true}); got.Watching {
		t.Fatalf("empty session clear = %+v", got)
	}

	// The receiver-delegate form derives an internal send target. Its public
	// result intentionally hides Send because only the receiver may observe it.
	internal := wcvpConfigure(t, jm, watchArgs{
		Target:             runtimeMessageAliasCaller,
		ReceiverSessionID:  "observer-session",
		ReceiverDelegateID: "dlg_obs",
	})
	if !internal.Watching || internal.Send != nil {
		t.Fatalf("internal receiver watch = %+v", internal)
	}
	if got := wcvpConfigure(t, jm, watchArgs{Target: runtimeMessageAliasCaller, ReceiverSessionID: "observer-session", ReceiverDelegateID: "dlg_obs", Clear: true}); got.Watching {
		t.Fatalf("internal receiver clear = %+v", got)
	}
}

func wcvpAssertTokenAndSnapshotContracts(t *testing.T, r *wcvpReader) {
	t.Helper()
	root := wcvpNewManager(t)
	child := wcvpNewManager(t)
	parent := &Session{jobManager: root, subagents: newSubagentManager(nil, 0)}
	if got := (&Session{jobManager: root}).jobManagerForToken(&watchSendToken{ChildSessionID: "child-live"}); got != nil {
		t.Fatalf("child token without subagent manager = %p", got)
	}
	parent.subagents.track(&subagent{id: "child-live", sess: &Session{jobManager: child}})
	parent.subagents.track(&subagent{id: "child-empty"})
	if parent.jobManagerForToken(nil) != nil {
		t.Fatal("nil watch token resolved to a manager")
	}
	if got := parent.jobManagerForToken(&watchSendToken{}); got != root {
		t.Fatalf("root token manager = %p, want %p", got, root)
	}
	if got := parent.jobManagerForToken(&watchSendToken{ChildSessionID: "missing"}); got != nil {
		t.Fatalf("missing child token manager = %p", got)
	}
	if got := parent.jobManagerForToken(&watchSendToken{ChildSessionID: "child-empty"}); got != nil {
		t.Fatalf("empty child token manager = %p", got)
	}
	if got := parent.jobManagerForToken(&watchSendToken{ChildSessionID: "child-live"}); got != child {
		t.Fatalf("live child token manager = %p, want %p", got, child)
	}

	cfg, err := newWatchConfig(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"job.notification"}}, frozenTestTime)
	if err != nil {
		t.Fatalf("new token config: %v", err)
	}
	key := jobstore.WatchSendKey{
		VisibleSessionID:        root.sessionID,
		WatchID:                 cfg.watchID,
		WatchTarget:             cfg.target,
		ResolvedWatchedIdentity: "job_wcvp",
		ResolvedSendTo:          runtimeMessageAliasCaller,
		WatchGeneration:         cfg.generation,
	}
	state := jobstore.WatchSendState{Key: key, DeliveryID: "wd_wcvp", UpdateSeq: 2, Frame: "frame", TriggerReason: "event"}
	cfg.pending = map[jobstore.WatchSendKey]*jobstore.WatchSendState{key: &state}
	root.mu.Lock()
	root.watches[watchKey{VisibleSessionID: root.sessionID, Target: cfg.target}] = cfg
	root.mu.Unlock()
	token := &watchSendToken{Key: key, UpdateSeq: state.UpdateSeq, DeliveryID: state.DeliveryID}
	resolvedJM, resolvedCfg, gotState, ok := parent.resolveWatchSendToken(token)
	if !ok || resolvedJM != root || resolvedCfg != cfg || gotState.DeliveryID != state.DeliveryID {
		t.Fatalf("resolve current token = (%p, %p, %+v, %v)", resolvedJM, resolvedCfg, gotState, ok)
	}
	if _, _, _, ok := parent.resolveWatchSendToken(&watchSendToken{Key: key, UpdateSeq: state.UpdateSeq + 1}); ok {
		t.Fatal("stale token resolved as current")
	}
	if _, _, _, ok := parent.resolveWatchSendToken(&watchSendToken{Key: jobstore.WatchSendKey{WatchID: "missing"}}); ok {
		t.Fatal("missing token resolved as current")
	}

	// terminalFlush is part of the token lookup surface after a watch has been
	// detached but a caller delivery remains pending. The same key must resolve
	// there, too, until settlement removes it.
	root.mu.Lock()
	delete(root.watches, watchKey{VisibleSessionID: root.sessionID, Target: cfg.target})
	if root.terminalFlush == nil {
		root.terminalFlush = make(map[*watchConfig]bool)
	}
	root.terminalFlush[cfg] = true
	root.mu.Unlock()
	root.mu.Lock()
	terminalCfg := root.watchConfigForKeyLocked(key)
	root.mu.Unlock()
	if terminalCfg != cfg {
		t.Fatalf("terminal flush lookup = %p, want %p", terminalCfg, cfg)
	}
	if _, resolvedCfg, _, ok := parent.resolveWatchSendToken(token); !ok || resolvedCfg != cfg {
		t.Fatalf("terminal flush token resolve = (%p, %v)", resolvedCfg, ok)
	}

	stateForNotification := state
	stateForNotification.Provenance = provenance.WithWatch(nil, "watch_parent", "generation", "delivery", "S1", "job_wcvp")
	note := watchSendTokenNotification("child-live", stateForNotification)
	if note.JobID != state.Key.ResolvedWatchedIdentity || note.Status != jobNotificationEventWatch || note.WatchSend == nil || note.WatchSend.ChildSessionID != "child-live" {
		t.Fatalf("watch token notification = %+v", note)
	}
	if note.Provenance == stateForNotification.Provenance {
		t.Fatal("watch token notification leaked provenance pointer")
	}

	// Settle through the real durable event append and prove the delivery-id
	// ledger and pending slot move together. This is the exact completion edge
	// used by caller-token delivery, not an in-memory mock.
	root.mu.Lock()
	delete(root.terminalFlush, cfg)
	root.watches[watchKey{VisibleSessionID: root.sessionID, Target: cfg.target}] = cfg
	root.mu.Unlock()
	if err := root.settleWatchSendDelivered(cfg, state); err != nil {
		t.Fatalf("settle watch send: %v", err)
	}
	root.mu.Lock()
	delivered := root.watchSendDeliveredLocked(state.DeliveryID)
	_, pending := cfg.pending[key]
	root.mu.Unlock()
	if !delivered || pending {
		t.Fatalf("settlement ledger delivered=%v pending=%v", delivered, pending)
	}

	seedWatchSendDelegateTarget(t, root, "dlg_obs")
	watchCfg, err := newWatchConfig(watchArgs{Target: "job_wcvp", Events: []string{"assistant.tool"}, Send: &watchSendArgs{To: "dlg_obs", Message: r.text()}}, frozenTestTime)
	if err != nil {
		t.Fatalf("new snapshot config: %v", err)
	}
	jobProvenance := provenance.WithWatch(nil, "ancestor", "g0", "d0", root.sessionID, "job_wcvp")
	now := root.now()
	if err := root.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            "job_wcvp",
		Type:             jobstore.JobShell,
		OwnerSessionID:   root.sessionID,
		VisibleToSession: root.sessionID,
		StartedAt:        &now,
		Provenance:       jobProvenance,
	}); err != nil {
		t.Fatalf("seed stored provenance: %v", err)
	}
	if got := jobProvenanceForWatch(root, runtimeMessageAliasCaller); got != nil {
		t.Fatalf("session target provenance = %+v", got)
	}
	storedProvenance := jobProvenanceForWatch(root, "job_wcvp")
	if storedProvenance == nil || storedProvenance == jobProvenance {
		t.Fatalf("stored job provenance = %+v", storedProvenance)
	}
	if got := jobProvenanceForWatch(root, "job_missing"); got != nil {
		t.Fatalf("missing job provenance = %+v", got)
	}

	event := events.SessionEvent{SessionID: root.sessionID, Kind: events.EventToolCallEnd, Data: events.ToolCallEndData{ToolName: "read_file"}, Provenance: storedProvenance}
	delivery := root.watchSendSnapshot(watchCfg, "job_wcvp", "event: assistant.tool", event)
	if delivery.updateSeq != 1 || delivery.deliveryID == "" || delivery.delegateGeneration == "" || delivery.provenance == nil {
		t.Fatalf("watch send snapshot = %+v", delivery)
	}
	if second := root.watchSendSnapshot(watchCfg, "job_wcvp", "event: assistant.tool", event); second.updateSeq != 2 || second.deliveryID == delivery.deliveryID {
		t.Fatalf("second watch send snapshot = %+v", second)
	}
	if got := root.watchSendDelegateGeneration(runtimeMessageAliasCaller, "job_wcvp"); got != "" {
		t.Fatalf("caller delegate generation = %q", got)
	}
	if got := root.watchSendDelegateGeneration("dlg_missing", "job_wcvp"); got != "" {
		t.Fatalf("missing delegate generation = %q", got)
	}
}

func wcvpAssertClosedStoreFailures(t *testing.T) {
	t.Helper()
	jm := wcvpNewManager(t)
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close test store: %v", err)
	}
	if err := jm.validateWatchTarget("job_wcvp_missing"); err == nil {
		t.Fatal("closed store target validation unexpectedly succeeded")
	}
	if _, _, err := jm.terminalWatchTargetStatus("job_wcvp_missing"); err == nil {
		t.Fatal("closed store terminal-status lookup unexpectedly succeeded")
	}
	if got := jm.watchSendDelegateGeneration("dlg_wcvp", "job_wcvp"); got != "" {
		t.Fatalf("closed store delegate generation = %q", got)
	}
	if got := jobProvenanceForWatch(jm, "job_wcvp"); got != nil {
		t.Fatalf("closed store provenance = %+v", got)
	}
}
