package appprojector

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

func TestDelegateProjection_DescendantOrdinaryEventsReachRootTransport(t *testing.T) {
	p := NewAppEventProjector("child", "local:child")
	stable := delegateProjectionFixture()
	stable.OwnerSessionID = "child"
	if got := p.Project(delegateProjectionEvent("child", stable)); len(got) != 1 {
		t.Fatalf("stable update notifications = %+v, want one", got)
	}
	out := p.Project(events.SessionEvent{Kind: events.EventWarning, SessionID: "child", Data: events.WarningData{Message: "ordinary descendant warning"}})
	if len(out) != 1 || out[0].Method != appwire.NotifyWarning || out[0].ThreadID != "child" {
		t.Fatalf("ordinary descendant projection = %+v", out)
	}
}

func TestDelegateProjection_LateRootReceivesStableDelegateSnapshot(t *testing.T) {
	p := NewAppEventProjector("root", "local:root")
	stable := delegateProjectionFixture()
	stable.OwnerSessionID = "root"
	params := requireDelegateProjection(t, p.Project(delegateProjectionEvent("root", stable)))
	if params.Delegate.DelegateID != "dlg_projection" || params.Delegate.ProjectionRevision != 7 {
		t.Fatalf("late stable snapshot = %+v", params.Delegate)
	}
}

func TestDelegateProjection_OwnerRootFencesForeignUpdates(t *testing.T) {
	p := NewAppEventProjector("owner", "local:owner")
	foreign := delegateProjectionFixture()
	foreign.OwnerSessionID = "foreign"
	if out := p.Project(delegateProjectionEvent("owner", foreign)); len(out) != 0 {
		t.Fatalf("foreign stable update crossed owner fence: %+v", out)
	}
	owned := delegateProjectionFixture()
	owned.OwnerSessionID = "owner"
	if out := p.Project(delegateProjectionEvent("owner", owned)); len(out) != 1 {
		t.Fatalf("owner stable update notifications = %+v, want one", out)
	}
}

func TestDelegateProjection_RevisionRejectsStaleStateButMergesLatestActivityByMax(t *testing.T) {
	p := NewAppEventProjector("owner", "local:owner")
	newer := delegateProjectionFixture()
	newer.ProjectionRevision = 8
	newer.Phase = "running"
	newer.Status = "running"
	newer.LatestActivityAt = "2026-08-15T01:00:00Z"
	requireDelegateProjection(t, p.Project(delegateProjectionEvent("owner", newer)))

	stale := delegateProjectionFixture()
	stale.ProjectionRevision = 7
	stale.Phase = "idle"
	stale.Status = "idle"
	stale.LatestActivityAt = "2026-08-15T02:00:00Z"
	merged := requireDelegateProjection(t, p.Project(delegateProjectionEvent("owner", stale))).Delegate
	if merged.ProjectionRevision != 8 || merged.Phase != "running" || merged.Status != "running" {
		t.Fatalf("stale lifecycle regressed projection: %+v", merged)
	}
	if merged.LatestActivityAt != stale.LatestActivityAt {
		t.Fatalf("latest activity = %q, want max %q", merged.LatestActivityAt, stale.LatestActivityAt)
	}

	olderActivity := stale
	olderActivity.LatestActivityAt = "2026-08-15T00:30:00Z"
	if out := p.Project(delegateProjectionEvent("owner", olderActivity)); len(out) != 0 {
		t.Fatalf("fully stale update emitted: %+v", out)
	}
}

func TestDelegateProjection_PreservesNullValidationExhaustionAndTurnSlots(t *testing.T) {
	p := NewAppEventProjector("owner", "local:owner")
	data := delegateProjectionFixture()
	valid := true
	resumable := false
	data.Message = json.RawMessage("null")
	data.StructuredResult = json.RawMessage("null")
	data.StructuredValid = &valid
	data.StructuredReason = "schema accepted explicit null"
	data.ExhaustionBudget = "max_tool_rounds_per_input"
	data.ExhaustionLimit = 4
	data.ExhaustionResumable = &resumable
	got := requireDelegateProjection(t, p.Project(delegateProjectionEvent("owner", data))).Delegate
	if !bytes.Equal(got.Message, []byte("null")) || !bytes.Equal(got.StructuredResult, []byte("null")) {
		t.Fatalf("explicit nulls changed: message=%s structured=%s", got.Message, got.StructuredResult)
	}
	if got.StructuredValid == nil || !*got.StructuredValid || got.StructuredReason != data.StructuredReason {
		t.Fatalf("structured validation = %+v", got)
	}
	if got.ExhaustionBudget != data.ExhaustionBudget || got.ExhaustionLimit != 4 || got.ExhaustionResumable == nil || *got.ExhaustionResumable {
		t.Fatalf("typed exhaustion = %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("waitIgnoredReason")) || bytes.Contains(raw, []byte("wait_ignored_reason")) {
		t.Fatalf("stable snapshot leaked call-scoped wait result: %s", raw)
	}
}

func TestDelegateProjection_PreservesTimingUsageQuietWorktreeWarningsAndDiagnostics(t *testing.T) {
	p := NewAppEventProjector("owner", "local:owner")
	data := delegateProjectionFixture()
	running, quiet, duration := int64(1200), int64(300), int64(900)
	data.RunningForMS, data.QuietForMS, data.DurationMS = &running, &quiet, &duration
	data.Usage = &events.DelegateUsageData{InputTokens: 11, OutputTokens: 7, CacheReadTokens: 3, TotalTokens: 18}
	data.Worktree = &events.DelegateWorktreeData{Path: "/tmp/lane", Branch: "delegate/lane", HeadSHA: "abc", Ahead: 2, Dirty: true}
	data.Warnings = []string{"salvaged draft"}
	data.Diagnostics = []string{"metadata retained"}
	got := requireDelegateProjection(t, p.Project(delegateProjectionEvent("owner", data))).Delegate
	if got.RunningForMS == nil || *got.RunningForMS != running || got.QuietForMS == nil || *got.QuietForMS != quiet || got.DurationMS == nil || *got.DurationMS != duration {
		t.Fatalf("timing = %+v", got)
	}
	if got.Usage == nil || got.Usage.InputTokens != 11 || got.Usage.CacheReadTokens != 3 || got.Usage.TotalTokens != 18 {
		t.Fatalf("usage = %+v", got.Usage)
	}
	if got.Worktree == nil || got.Worktree.Path != "/tmp/lane" || !got.Worktree.Dirty || !reflect.DeepEqual(got.Warnings, data.Warnings) || !reflect.DeepEqual(got.Diagnostics, data.Diagnostics) {
		t.Fatalf("worktree/warnings/diagnostics = %+v", got)
	}
}

func TestDelegateProjection_ShellUsesParentDelegateID(t *testing.T) {
	p := NewAppEventProjector("child", "local:child")
	out := p.Project(events.SessionEvent{Kind: events.EventJobStarted, SessionID: "child", Data: events.JobStartedData{
		JobID: "job_shell", JobType: "shell", Status: "running", ParentDelegateID: "dlg_parent",
	}})
	if len(out) != 1 || out[0].Method != appwire.NotifySerfJobStarted {
		t.Fatalf("shell projection = %+v", out)
	}
	params, ok := out[0].Params.(appwire.SerfJobParams)
	if !ok || params.Job.ParentDelegateID != "dlg_parent" || params.Job.DelegateID != "" {
		t.Fatalf("shell parent projection = %#v", out[0].Params)
	}
	if leaked := p.Project(events.SessionEvent{Kind: events.EventJobStarted, SessionID: "child", Data: events.JobStartedData{JobID: "job_activation", JobType: "delegate", Status: "running", DelegateID: "dlg_parent"}}); len(leaked) != 0 {
		t.Fatalf("delegate activation leaked through shell event: %+v", leaked)
	}
}

func TestDelegateProjection_TranscriptPreseedSubscriptionAndThreadReadRemainAvailable(t *testing.T) {
	p := NewAppEventProjector("child", "local:child")
	p.SeedPersistedTurns(4)
	stable := delegateProjectionFixture()
	stable.OwnerSessionID = "child"
	requireDelegateProjection(t, p.Project(delegateProjectionEvent("child", stable)))
	out := p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "child", Timestamp: time.Unix(10, 0), Data: events.UserInputData{Text: "after restore", Turn: 5}})
	turn := notificationTurn(t, out, appwire.NotifyTurnStarted)
	if turn.ID != "turn_5" {
		t.Fatalf("preseeded descendant turn id = %q, want turn_5", turn.ID)
	}
}

func delegateProjectionFixture() events.DelegateUpdatedData {
	return events.DelegateUpdatedData{
		DelegateID: "dlg_projection", OwnerSessionID: "owner", RootSessionID: "root", ChildSessionID: "child",
		TranscriptRef: "local:child", Type: "delegate", Lifecycle: "idle", Phase: "idle", Status: "idle",
		Resumable: true, ProjectionRevision: 7, Task: "inspect", Description: "inspect carefully", AgentType: "explorer",
		RequestedModel: "openai/gpt-5", ResolvedProfileID: "openai", ResolvedModel: "gpt-5", Model: "gpt-5",
		ReasoningEffort: "high", LatestActivityAt: "2026-08-15T00:00:00Z", DelegationAllowance: 2, ParentWatchGranted: true,
	}
}

func delegateProjectionEvent(sessionID string, data events.DelegateUpdatedData) events.SessionEvent {
	return events.SessionEvent{Kind: events.EventDelegateUpdated, SessionID: sessionID, Timestamp: time.Unix(1, 0).UTC(), Data: data}
}

func requireDelegateProjection(t testing.TB, out []AppNotification) appwire.SerfDelegateParams {
	t.Helper()
	if len(out) != 1 || out[0].Method != appwire.NotifySerfDelegateUpdated {
		t.Fatalf("delegate notifications = %+v, want one %s", out, appwire.NotifySerfDelegateUpdated)
	}
	params, ok := out[0].Params.(appwire.SerfDelegateParams)
	if !ok {
		t.Fatalf("delegate params type = %T", out[0].Params)
	}
	return params
}
