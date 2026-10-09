package appprojector

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/activitybound"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// A settled reported packet carries the bounded preview, and the logical owner
// is the nearest ancestor session when the frame names one, else the owner.
func TestDelegateProjection_ReportPreviewAndLogicalOwner(t *testing.T) {
	p := NewAppEventProjector("root", "local:root")
	direct := delegateProjectionFixture()
	direct.OwnerSessionID = "root"
	direct.PacketKind = string(activitybound.PacketReported)
	direct.Terminal = true
	direct.Message = json.RawMessage(`"the short report"`)
	params := requireDelegateProjection(t, p.Project(delegateProjectionEvent("root", direct)))
	if params.Delegate.LogicalOwnerSessionID != "root" {
		t.Fatalf("direct child logical owner = %q, want root", params.Delegate.LogicalOwnerSessionID)
	}
	if params.Delegate.ReportPreview != "the short report" || params.Delegate.ReportPreviewTruncated {
		t.Fatalf("direct child preview = %q truncated=%v", params.Delegate.ReportPreview, params.Delegate.ReportPreviewTruncated)
	}
	if params.Delegate.ProjectionRevision != direct.ProjectionRevision {
		t.Fatalf("projection revision = %d, want %d", params.Delegate.ProjectionRevision, direct.ProjectionRevision)
	}

	// A deeper descendant's nearest ancestor session is AncestorSessionIDs[0].
	p2 := NewAppEventProjector("root", "local:root")
	deep := delegateProjectionFixture()
	deep.DelegateID = "dlg_deep"
	deep.OwnerSessionID = "root"
	deep.AncestorSessionIDs = []string{"mid"}
	deep.ProjectionRevision = 8
	params2 := requireDelegateProjection(t, p2.Project(delegateProjectionEvent("root", deep)))
	if params2.Delegate.LogicalOwnerSessionID != "mid" {
		t.Fatalf("descendant logical owner = %q, want mid", params2.Delegate.LogicalOwnerSessionID)
	}
}

// The preview is bounded and truncated with the shared helper, and the frame's
// prose fields are capped exactly as the read caps them.
func TestDelegateProjection_BoundsPreviewAndProseWithSharedHelper(t *testing.T) {
	p := NewAppEventProjector("root", "local:root")
	data := delegateProjectionFixture()
	data.OwnerSessionID = "root"
	data.PacketKind = string(activitybound.PacketReported)
	data.Terminal = true
	data.Message = json.RawMessage(`"` + strings.Repeat("界", activitybound.MaxDelegateProseRunes*2) + `"`)
	data.Task = strings.Repeat("t", activitybound.MaxDelegateProseRunes+50)
	data.Description = strings.Repeat("d", activitybound.MaxDelegateProseRunes+50)
	data.Model = strings.Repeat("m", activitybound.MaxLabelRunes+50)
	data.Worktree = &events.DelegateWorktreeData{
		Path:    strings.Repeat("p", activitybound.MaxDelegateProseRunes+50),
		Branch:  strings.Repeat("b", activitybound.MaxLabelRunes+50),
		HeadSHA: strings.Repeat("h", activitybound.MaxLabelRunes+50),
	}
	got := requireDelegateProjection(t, p.Project(delegateProjectionEvent("root", data))).Delegate

	wantPreview, wantTruncated := activitybound.ReportPreview(data.Message)
	if got.ReportPreview != wantPreview || got.ReportPreviewTruncated != wantTruncated {
		t.Fatalf("preview drift from the shared helper: got %q/%v want %q/%v", got.ReportPreview, got.ReportPreviewTruncated, wantPreview, wantTruncated)
	}
	if got.ReportPreview != strings.Repeat("界", activitybound.MaxDelegateProseRunes-1)+"…" || !got.ReportPreviewTruncated {
		t.Fatalf("preview not truncated to the prose cap: runes=%d truncated=%v", len([]rune(got.ReportPreview)), got.ReportPreviewTruncated)
	}
	for name, field := range map[string]string{"task": got.Task, "description": got.Description, "model": got.Model} {
		limit := activitybound.MaxDelegateProseRunes
		if name == "model" {
			limit = activitybound.MaxLabelRunes
		}
		if len([]rune(field)) != limit {
			t.Fatalf("%s = %d runes, want %d", name, len([]rune(field)), limit)
		}
	}
	if got.Worktree == nil || len([]rune(got.Worktree.Path)) != activitybound.MaxDelegateProseRunes || len([]rune(got.Worktree.Branch)) != activitybound.MaxLabelRunes || len([]rune(got.Worktree.HeadSHA)) != activitybound.MaxLabelRunes {
		t.Fatalf("worktree strings not bounded: %+v", got.Worktree)
	}
}

// An open run still owns its predecessor's packet, so it carries no preview.
func TestDelegateProjection_OpenRunHasNoPreview(t *testing.T) {
	p := NewAppEventProjector("root", "local:root")
	data := delegateProjectionFixture()
	data.OwnerSessionID = "root"
	data.PacketKind = string(activitybound.PacketReported)
	data.Message = json.RawMessage(`"stale report"`)
	data.Terminal = false
	got := requireDelegateProjection(t, p.Project(delegateProjectionEvent("root", data))).Delegate
	if got.ReportPreview != "" || got.ReportPreviewTruncated {
		t.Fatalf("open run carried a preview: %q/%v", got.ReportPreview, got.ReportPreviewTruncated)
	}
}

// The new fields merge as ordinary revision-ordered fields: a stale frame never
// rewrites them, and a newer frame replaces them.
func TestMergeAppwireDelegateInfo_NewFieldsFollowRevision(t *testing.T) {
	current := appwire.EvenerDelegateInfo{
		DelegateID: "dlg", ProjectionRevision: 9, LogicalOwnerSessionID: "mid",
		ReportPreview: "newer", ReportPreviewTruncated: true, LatestActivityAt: "2026-08-15T01:00:00Z",
	}
	stale := appwire.EvenerDelegateInfo{
		DelegateID: "dlg", ProjectionRevision: 8, LogicalOwnerSessionID: "other",
		ReportPreview: "older", LatestActivityAt: "2026-08-15T02:00:00Z",
	}
	merged, changed := mergeAppwireDelegateInfo(current, stale)
	if !changed {
		t.Fatal("a later activity time should still publish")
	}
	if merged.ReportPreview != "newer" || merged.LogicalOwnerSessionID != "mid" || !merged.ReportPreviewTruncated {
		t.Fatalf("stale frame rewrote the new fields: %+v", merged)
	}
	if merged.LatestActivityAt != stale.LatestActivityAt {
		t.Fatalf("latest activity = %q, want max %q", merged.LatestActivityAt, stale.LatestActivityAt)
	}

	newer := stale
	newer.ProjectionRevision = 10
	newer.ReportPreview = "newest"
	merged, changed = mergeAppwireDelegateInfo(current, newer)
	if !changed || merged.ReportPreview != "newest" || merged.LogicalOwnerSessionID != "other" {
		t.Fatalf("newer frame did not replace the fields: %+v changed=%v", merged, changed)
	}
}

func TestDelegateProjection_DescendantOrdinaryEventsReachRootTransport(t *testing.T) {
	p := NewAppEventProjector("child", "local:child")
	stable := delegateProjectionFixture()
	stable.OwnerSessionID = "child"
	if got := p.Project(delegateProjectionEvent("child", stable)); len(got) != 1 {
		t.Fatalf("stable update notifications = %+v, want one", got)
	}
	out := p.Project(events.SessionEvent{Kind: events.EventNotesUpdated, SessionID: "child", Data: events.NotesUpdatedData{AgentNote: "ordinary descendant note"}})
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerNotesUpdated || out[0].ThreadID != "child" {
		t.Fatalf("ordinary descendant projection = %+v", out)
	}
}

func TestDelegateProjection_LateRootReceivesStableDelegateSnapshot(t *testing.T) {
	p := NewAppEventProjector("root", "local:root")
	stable := delegateProjectionFixture()
	stable.OwnerSessionID = "root"
	params := requireDelegateProjection(t, p.Project(delegateProjectionEvent("root", stable)))
	if params.Delegate.DelegateID != "dlg_projection" || params.Delegate.ProjectionRevision != 7 || params.Delegate.RunGeneration != stable.RunGeneration {
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

// A subagent's thread lists its subtree, so it takes an update for a delegate
// below it, which still names the tree's root as owner. A row outside its
// subtree stays fenced.
func TestDelegateProjection_AncestorSubagentThreadReceivesSubtreeUpdates(t *testing.T) {
	p := NewAppEventProjector("middle", "local:middle")
	below := delegateProjectionFixture()
	below.OwnerSessionID = "root"
	below.AncestorSessionIDs = []string{"middle"}
	params := requireDelegateProjection(t, p.Project(delegateProjectionEvent("middle", below)))
	if params.ThreadID != "middle" || params.Ref != "local:middle" || params.Delegate.OwnerSessionID != "root" || params.Delegate.DelegateID != below.DelegateID {
		t.Fatalf("subtree update on the middle thread = %+v", params)
	}
	elsewhere := delegateProjectionFixture()
	elsewhere.DelegateID = "dlg_elsewhere"
	elsewhere.OwnerSessionID = "root"
	elsewhere.AncestorSessionIDs = []string{"sibling"}
	if out := p.Project(delegateProjectionEvent("middle", elsewhere)); len(out) != 0 {
		t.Fatalf("a row outside the middle subtree crossed the fence: %+v", out)
	}
}

func TestDelegateProjection_RevisionRejectsStaleStateButMergesLatestActivityByMax(t *testing.T) {
	p := NewAppEventProjector("owner", "local:owner")
	newer := delegateProjectionFixture()
	newer.ProjectionRevision = 8
	newer.Phase = "running"
	newer.Status = "running"
	newer.NeedsAttention = true
	newer.LatestActivityAt = "2026-08-15T01:00:00Z"
	requireDelegateProjection(t, p.Project(delegateProjectionEvent("owner", newer)))

	stale := delegateProjectionFixture()
	stale.ProjectionRevision = 7
	stale.Phase = "idle"
	stale.Status = "idle"
	stale.NeedsAttention = false
	stale.LatestActivityAt = "2026-08-15T02:00:00Z"
	merged := requireDelegateProjection(t, p.Project(delegateProjectionEvent("owner", stale))).Delegate
	if merged.ProjectionRevision != 8 || merged.Phase != "running" || merged.Status != "running" || !merged.NeedsAttention {
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
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerJobStarted {
		t.Fatalf("shell projection = %+v", out)
	}
	params, ok := out[0].Params.(appwire.EvenerJobParams)
	if !ok || params.Job.ParentDelegateID != "dlg_parent" || params.Job.DelegateID != "" {
		t.Fatalf("shell parent projection = %#v", out[0].Params)
	}
	if leaked := p.Project(events.SessionEvent{Kind: events.EventJobStarted, SessionID: "child", Data: events.JobStartedData{JobID: "job_activation", JobType: "delegate", Status: "running", DelegateID: "dlg_parent"}}); len(leaked) != 0 {
		t.Fatalf("delegate activation leaked through shell event: %+v", leaked)
	}
}

func delegateProjectionFixture() events.DelegateUpdatedData {
	return events.DelegateUpdatedData{
		RunGeneration: 2, DelegateID: "dlg_projection", OwnerSessionID: "owner", RootSessionID: "root", ChildSessionID: "child",
		TranscriptRef: "local:child", Type: "delegate", Lifecycle: "idle", Phase: "idle", Status: "idle",
		Resumable: true, NeedsAttention: true, ProjectionRevision: 7, Task: "inspect", Description: "inspect carefully", AgentType: "explorer",
		RequestedModel: "openai/gpt-5", ResolvedProfileID: "openai", ResolvedModel: "gpt-5", Model: "gpt-5",
		ReasoningEffort: "high", LatestActivityAt: "2026-08-15T00:00:00Z", DelegationAllowance: 2, ParentWatchGranted: true,
	}
}

func delegateProjectionEvent(sessionID string, data events.DelegateUpdatedData) events.SessionEvent {
	return events.SessionEvent{Kind: events.EventDelegateUpdated, SessionID: sessionID, Timestamp: time.Unix(1, 0).UTC(), Data: data}
}

func requireDelegateProjection(t testing.TB, out []AppNotification) appwire.EvenerDelegateParams {
	t.Helper()
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerDelegateUpdated {
		t.Fatalf("delegate notifications = %+v, want one %s", out, appwire.NotifyEvenerDelegateUpdated)
	}
	params, ok := out[0].Params.(appwire.EvenerDelegateParams)
	if !ok {
		t.Fatalf("delegate params type = %T", out[0].Params)
	}
	return params
}
