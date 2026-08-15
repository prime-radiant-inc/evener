package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/identifier"
)

func TestStableDelegateReadOnly_HistoricalSendRendersWithoutLiveAlias(t *testing.T) {
	got := toolInputSummary("job_send_message", json.RawMessage(`{"target":"job_historical_activation","message":"what happened?"}`))
	if got != "job_historical_activation `what happened?`" {
		t.Fatalf("historical send summary = %q", got)
	}
}

func TestStableDelegateReadOnly_ActivityPreservesTimingUsageQuietWorktreeAndDiagnostics(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
		withoutGitSnapshot(),
	)
	descriptor := stableReadonlyDescriptor(s, "dlg_rich")
	descriptor.Task = "inspect retained evidence"
	descriptor.Description = "keep all terminal fields"
	descriptor.AgentType = "explorer"
	descriptor.RequestedModel = "frontier"
	descriptor.ResolvedProfileID = "openai"
	descriptor.ResolvedModel = "gpt-5.2"
	descriptor.Config.ReasoningEffort = "high"
	descriptor.DelegationAllowance = 3
	descriptor.ParentWatchGranted = true
	descriptor.ResultSchema = json.RawMessage(`{"type":"string"}`)
	startedAt := time.Unix(10, 0).UTC()
	latestAt := time.Unix(14, 0).UTC()
	endedAt := time.Unix(18, 0).UTC()
	finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
		result:                  "complete",
		communicated:            true,
		structuredResult:        nil,
		structuredResultPresent: true,
		descriptor:              descriptor,
		startedAt:               startedAt,
		latestActivityAt:        latestAt,
		endedAt:                 endedAt,
		usage: schema.CumulativeUsage{
			InputTokens: 7, OutputTokens: 11, CacheReadTokens: 3, TotalTokens: 21,
		},
		warnings: []string{"cleanup retained one diagnostic"},
		worktree: &delegateWorktreeReport{
			Path: "/tmp/delegate-rich", Branch: "delegate-rich", HeadSHA: "abc123", Ahead: 2, Dirty: true,
		},
	})
	seedStableReadonlyFinish(t, s, "dlg_rich", descriptor, startedAt, finish, true)

	tree, err := s.JobActivityTree(appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("JobActivityTree: %v", err)
	}
	row := stableReadonlyActivityRow(t, tree, "dlg_rich")
	for key, want := range map[string]any{
		"type":                   "delegate",
		"status":                 "idle",
		"outcome":                "completed",
		"task":                   "inspect retained evidence",
		"description":            "keep all terminal fields",
		"agentType":              "explorer",
		"requestedModel":         "frontier",
		"resolvedProfileId":      "openai",
		"resolvedModel":          "gpt-5.2",
		"reasoningEffort":        "high",
		"runStartedAt":           startedAt.Format(time.RFC3339Nano),
		"runEndedAt":             endedAt.Format(time.RFC3339Nano),
		"latestActivityAt":       endedAt.Format(time.RFC3339Nano),
		"durationMs":             float64(8000),
		"structuredResultValid":  false,
		"structuredResultReason": structuredResultReasonSchemaValidationFailed,
		"delegationAllowance":    float64(3),
		"parentWatchGranted":     true,
	} {
		if got := row[key]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, want %#v; row=%#v", key, got, want, row)
		}
	}
	if got, present := row["structuredResult"]; !present || got != nil {
		t.Fatalf("explicit-null structured result = (%#v, %v), want present null", got, present)
	}
	if row["message"] != "complete" {
		t.Fatalf("message = %#v", row["message"])
	}
	warnings, _ := row["warnings"].([]any)
	if len(warnings) != 1 || warnings[0] != "cleanup retained one diagnostic" {
		t.Fatalf("warnings = %#v", row["warnings"])
	}
	usage, _ := row["usage"].(map[string]any)
	if usage["inputTokens"] != float64(7) || usage["outputTokens"] != float64(11) || usage["cacheReadTokens"] != float64(3) || usage["totalTokens"] != float64(21) {
		t.Fatalf("usage = %#v", usage)
	}
	worktree, _ := row["worktree"].(map[string]any)
	if worktree["path"] != "/tmp/delegate-rich" || worktree["branch"] != "delegate-rich" || worktree["headSha"] != "abc123" || worktree["ahead"] != float64(2) || worktree["dirty"] != true {
		t.Fatalf("worktree = %#v", worktree)
	}
}

func TestStableDelegateReadOnly_OneSampledClockDrivesQuietRunningAndDuration(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	seedStableToolRunningDelegate(t, s, "dlg_clock_a", "", time.Unix(100, 0).UTC())
	seedStableToolRunningDelegate(t, s, "dlg_clock_b", "", time.Unix(200, 0).UTC())
	now := time.Unix(1000, 0).UTC()
	counting := &stableReadonlyCountingClock{Clock: s.sclock(), now: now}
	s.clock = counting

	tree, err := s.JobActivityTree(appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("JobActivityTree: %v", err)
	}
	if got := counting.calls.Load(); got != 1 {
		t.Fatalf("projection sampled clock %d times, want exactly 1", got)
	}
	for _, tc := range []struct {
		id      string
		started time.Time
	}{
		{id: "dlg_clock_a", started: time.Unix(100, 0).UTC()},
		{id: "dlg_clock_b", started: time.Unix(200, 0).UTC()},
	} {
		row := stableReadonlyActivityRow(t, tree, tc.id)
		want := float64(now.Sub(tc.started).Milliseconds())
		if row["runningForMs"] != want || row["quietForMs"] != want {
			t.Errorf("%s timing = running:%#v quiet:%#v, want %v from one sampled instant", tc.id, row["runningForMs"], row["quietForMs"], want)
		}
		if _, exists := row["durationMs"]; exists {
			t.Errorf("%s running row unexpectedly has durationMs: %#v", tc.id, row)
		}
	}
}

func TestStableDelegateReadOnly_ColdAndLiveProjectionMatch(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
		withoutGitSnapshot(),
	)
	descriptor := stableReadonlyDescriptor(s, "dlg_parity")
	startedAt := time.Unix(100, 0).UTC()
	finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
		result: "done", communicated: true, descriptor: descriptor,
		startedAt: startedAt, latestActivityAt: startedAt.Add(time.Second), endedAt: startedAt.Add(2 * time.Second),
	})
	seedStableReadonlyFinish(t, s, "dlg_parity", descriptor, startedAt, finish, true)

	live, err := s.JobActivityTree(appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("live JobActivityTree: %v", err)
	}
	cold, err := LoadSessionJobActivityTree(stateDir, s.ID(), appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("cold LoadSessionJobActivityTree: %v", err)
	}
	liveRow := stableReadonlyActivityRow(t, live, "dlg_parity")
	coldRow := stableReadonlyActivityRow(t, cold, "dlg_parity")
	if !reflect.DeepEqual(coldRow, liveRow) {
		t.Fatalf("cold/live stable delegate mismatch:\n cold=%#v\n live=%#v", coldRow, liveRow)
	}
}

func TestStableDelegateReadOnly_MissingFilesRemainMissing(t *testing.T) {
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	sessionDir := jobsDir(stateDir, sessionID)
	if _, err := LoadSessionJobActivityTree(stateDir, sessionID, appwire.JobsListParams{}); err != nil {
		t.Fatalf("LoadSessionJobActivityTree missing state: %v", err)
	}
	if _, err := LoadSessionHistoricalJobRecords(stateDir, sessionID); err != nil {
		t.Fatalf("LoadSessionHistoricalJobRecords missing state: %v", err)
	}
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("read-only cold projection created %s: %v", sessionDir, err)
	}
}

func TestStableDelegateReadOnly_TornTailIsReportedButNotRepaired(t *testing.T) {
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	path := seedStableReadonlyTornJournal(t, stateDir, sessionID)
	want := mustReadonlyFileState(t, path)

	tree, err := LoadSessionJobActivityTree(stateDir, sessionID, appwire.JobsListParams{})
	if err != nil {
		t.Fatalf("LoadSessionJobActivityTree torn tail: %v", err)
	}
	diagnostics := stableReadonlyRootDiagnostics(t, tree)
	if !strings.Contains(strings.Join(diagnostics, "\n"), "delegate_journal_torn_tail") {
		t.Fatalf("torn-tail diagnostics = %#v", diagnostics)
	}
	if got := mustReadonlyFileState(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("torn-tail read repaired or mutated journal:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestStableDelegateReadOnly_FileBytesAndMetadataRemainUnchanged(t *testing.T) {
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	path := seedStableReadonlyTornJournal(t, stateDir, sessionID)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(12345, 0).UTC()
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	want := mustReadonlyFileState(t, path)

	if _, err := LoadSessionJobActivityTree(stateDir, sessionID, appwire.JobsListParams{}); err != nil {
		t.Fatalf("LoadSessionJobActivityTree: %v", err)
	}
	if got := mustReadonlyFileState(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("read-only projection changed bytes or metadata:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestStableDelegateReadOnly_NoSessionProviderOrWritableOpen(t *testing.T) {
	stateDir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	path := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := jobstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Unix(100, 0).UTC()
	if err := store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, JobID: "job_readonly", Type: jobstore.JobShell,
		OwnerSessionID: sessionID, StartedAt: &startedAt,
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	oldOpen := historicalJobsOpen
	var opens atomic.Int32
	historicalJobsOpen = func(string) (historicalJobStore, error) {
		opens.Add(1)
		return nil, errors.New("append-capable historical open invoked")
	}
	t.Cleanup(func() { historicalJobsOpen = oldOpen })

	if _, err := LoadSessionHistoricalJobRecords(stateDir, sessionID); err != nil {
		t.Fatalf("historical record projection constructed writable state: %v", err)
	}
	if _, err := LoadSessionJobActivityTree(stateDir, sessionID, appwire.JobsListParams{}); err != nil {
		t.Fatalf("historical activity projection constructed writable state: %v", err)
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("historical read invoked append-capable Open %d times", got)
	}
}

type stableReadonlyCountingClock struct {
	clock.Clock
	now   time.Time
	calls atomic.Int32
}

func (c *stableReadonlyCountingClock) Now() time.Time {
	call := c.calls.Add(1)
	return c.now.Add(time.Duration(call-1) * time.Hour)
}

func stableReadonlyDescriptor(s *Session, id string) delegatestore.Descriptor {
	return delegatestore.Descriptor{
		ChildSessionID:   "child-" + id,
		TranscriptRef:    "local:child-" + id,
		OwnerSessionID:   s.ID(),
		VisibleSessionID: s.ID(),
		Task:             "task " + id,
		Description:      "description " + id,
		AgentType:        "general",
		ResolvedModel:    "gpt-5.2",
		ToolNameCeiling:  []string{"communicate"},
		Resumable:        true,
	}
}

func seedStableReadonlyFinish(t *testing.T, s *Session, id string, descriptor delegatestore.Descriptor, startedAt time.Time, finish delegateFinish, acknowledge bool) {
	t.Helper()
	deliveryID := delegateDeliveryID(id, 1)
	finished := delegateRunFinishedEvent(delegateLease{delegateID: id, generation: 1}, finish.outcome, finish.disposition, finish.reason, finish.endedAt, deliveryID, finish.packet)
	finished.RunFinished.Outcome.ExhaustionBudget = finish.exhaustionBudget
	finished.RunFinished.Outcome.ExhaustionLimit = finish.exhaustionLimit
	if finish.exhaustionResumable != nil {
		value := *finish.exhaustionResumable
		finished.RunFinished.Outcome.Resumable = &value
	}
	events := []delegatestore.Event{
		{Kind: delegatestore.EventDelegateCreated, DelegateID: id, Created: &delegatestore.DelegateCreated{Descriptor: descriptor}},
		delegateControllerRunStartedEvent(id, 1, delegatestore.TriggerInitial, startedAt),
		finished,
	}
	if acknowledge {
		events = append(events, delegatestore.Event{
			Kind: delegatestore.EventDelegateDeliveryAcknowledged, DelegateID: id,
			DeliveryAcknowledged: &delegatestore.DeliveryAcknowledged{DeliveryID: deliveryID},
		})
	}
	s.delegateController.mu.Lock()
	_, err := s.delegateController.appendLocked(events...)
	s.delegateController.mu.Unlock()
	if err != nil {
		t.Fatalf("seed stable read-only delegate %s: %v", id, err)
	}
}

func stableReadonlyActivityRow(t *testing.T, tree appwire.JobActivityTree, delegateID string) map[string]any {
	t.Helper()
	for _, entry := range tree.Root.Entries {
		if entry.Delegate == nil || entry.Delegate.DelegateID != delegateID {
			continue
		}
		raw, err := json.Marshal(entry.Delegate)
		if err != nil {
			t.Fatal(err)
		}
		var row map[string]any
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		return row
	}
	t.Fatalf("stable delegate %s absent from activity tree: %#v", delegateID, tree.Root.Entries)
	return nil
}

func stableReadonlyRootDiagnostics(t *testing.T, tree appwire.JobActivityTree) []string {
	t.Helper()
	raw, err := json.Marshal(tree.Root)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	values, _ := root["diagnostics"].([]any)
	diagnostics := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			diagnostics = append(diagnostics, text)
		}
	}
	return diagnostics
}

func seedStableReadonlyTornJournal(t *testing.T, stateDir, sessionID string) string {
	t.Helper()
	path := filepath.Join(jobsDir(stateDir, sessionID), "delegates.jsonl")
	store, err := delegatestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := delegatestore.Descriptor{
		ChildSessionID: "child-torn", TranscriptRef: "local:child-torn",
		OwnerSessionID: sessionID, VisibleSessionID: sessionID,
		Task: "retain torn tail", AgentType: "general", ToolNameCeiling: []string{"communicate"}, Resumable: true,
	}
	startedAt := time.Unix(50, 0).UTC()
	_, _, err = store.AppendBatch(make(delegatestore.State), []delegatestore.Event{
		{Kind: delegatestore.EventDelegateCreated, DelegateID: "dlg_torn", Created: &delegatestore.DelegateCreated{Descriptor: descriptor}},
		delegateControllerRunStartedEvent("dlg_torn", 1, delegatestore.TriggerInitial, startedAt),
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"events":[`); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

type stableReadonlyFileState struct {
	Inode uint64
	Size  int64
	Mode  os.FileMode
	Mtime time.Time
	Bytes []byte
	Hash  [sha256.Size]byte
}

func mustReadonlyFileState(t *testing.T, path string) stableReadonlyFileState {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("file info for %s has %T system data", path, info.Sys())
	}
	return stableReadonlyFileState{
		Inode: stat.Ino,
		Size:  info.Size(),
		Mode:  info.Mode(),
		Mtime: info.ModTime(),
		Bytes: bytes.Clone(raw),
		Hash:  sha256.Sum256(raw),
	}
}
