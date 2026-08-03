package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/llm"
)

// wantDisposalSentence is the spec §P2 nudge, copy pinned, for delegate id
// dlg_01ISODISPOSALHINT0000001.
const wantDisposalSentence = "When you're done with this delegate's work (e.g., after merging it), dispose its worktree and branch: manage_worktree op=dispose id=dlg_01ISODISPOSALHINT0000001."

func disposalHintLane(t *testing.T, r *wtDlgRepo, id string) *jobstore.DelegateRestoreDescriptor {
	t.Helper()
	lane, _, _, _, _, err := r.s.createDelegateWorktree(context.Background(), id)
	if err != nil {
		t.Fatalf("createDelegateWorktree: %v", err)
	}
	return &jobstore.DelegateRestoreDescriptor{
		Isolation:       "worktree",
		WorkingDir:      lane,
		ParentSessionID: r.s.id,
	}
}

// An owned, finished isolated delegate in a session holding the dispose op
// carries the exact §P2 sentence on both the report and the inline tool result.
func TestDisposalHint_OwnedDelegateInlineResultCarriesSentence(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	r := newWtDlgRepo(t, c)
	if r.s.reg.Get("manage_worktree") == nil {
		t.Fatal("test setup: top-level session should hold manage_worktree")
	}

	desc := disposalHintLane(t, r, "dlg_01ISODISPOSALHINT0000001")
	report := r.s.isolatedDelegateWorktreeReport(desc)
	if report == nil {
		t.Fatal("report is nil")
	}
	if report.DisposalHint != wantDisposalSentence {
		t.Errorf("report.DisposalHint = %q, want %q", report.DisposalHint, wantDisposalSentence)
	}
	out := delegateWorktreeToolResultFrom(report)
	if out == nil || out.DisposalHint != wantDisposalSentence {
		t.Errorf("tool result DisposalHint = %+v, want %q", out, wantDisposalSentence)
	}
}

// The inline tool result JSON emits disposal_hint (exported-field regression:
// an unexported field would be silently dropped by encoding/json).
func TestDisposalHint_JSONRoundTripEmitsField(t *testing.T) {
	t.Parallel()
	in := &delegateWorktreeToolResult{Path: "/lane", Branch: "b", HeadSHA: "abc123", DisposalHint: wantDisposalSentence}
	blob, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(blob), `"disposal_hint":`) {
		t.Fatalf("JSON missing disposal_hint key: %s", blob)
	}
	var back delegateWorktreeToolResult
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.DisposalHint != wantDisposalSentence {
		t.Errorf("round-trip DisposalHint = %q, want %q", back.DisposalHint, wantDisposalSentence)
	}
	if back.HeadSHA != "abc123" {
		t.Errorf("round-trip HeadSHA = %q, want abc123", back.HeadSHA)
	}
}

// The empty-hint case omits the field entirely (omitempty).
func TestDisposalHint_EmptyHintOmittedFromJSON(t *testing.T) {
	t.Parallel()
	blob, err := json.Marshal(&delegateWorktreeToolResult{Path: "/lane", Branch: "b"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "disposal_hint") {
		t.Fatalf("empty hint must be omitted, got: %s", blob)
	}
}

// The background notification block carries the sentence when the lane report's
// DisposalHint is set.
func TestDisposalHint_NotificationBlockCarriesSentence(t *testing.T) {
	t.Parallel()
	n := jobNotification{JobID: "job_1", JobType: string(jobstore.JobDelegate), Status: "finished"}
	ex := notificationExcerpt{worktree: &delegateWorktreeReport{Path: "/lane", Branch: "b", DisposalHint: wantDisposalSentence}}
	block := formatJobNotificationBlock(n, ex)
	if !strings.Contains(block, wantDisposalSentence) {
		t.Errorf("notification block missing sentence.\nblock:\n%s", block)
	}
}

// A report with no hint produces a notification block without the nudge.
func TestDisposalHint_NotificationBlockOmitsWhenNoHint(t *testing.T) {
	t.Parallel()
	n := jobNotification{JobID: "job_1", JobType: string(jobstore.JobDelegate), Status: "finished"}
	ex := notificationExcerpt{worktree: &delegateWorktreeReport{Path: "/lane", Branch: "b"}}
	block := formatJobNotificationBlock(n, ex)
	if strings.Contains(block, "dispose its worktree") {
		t.Errorf("notification block must omit the nudge when hint is empty.\nblock:\n%s", block)
	}
}

// An ancestor receiving a forwarded descendant descriptor (ParentSessionID is
// the original creator, not this session) gets NO hint.
func TestDisposalHint_ForwardedDescriptorInAncestorGetsNoHint(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	r := newWtDlgRepo(t, c)

	desc := disposalHintLane(t, r, "dlg_01ISODISPOSALHINT0000001")
	desc.ParentSessionID = "some-other-session-id"
	report := r.s.isolatedDelegateWorktreeReport(desc)
	if report == nil {
		t.Fatal("report is nil")
	}
	if report.DisposalHint != "" {
		t.Errorf("forwarded descriptor must get no hint, got %q", report.DisposalHint)
	}
}

// A session without the dispose op (a leaf delegate whose manage_worktree was
// stripped) gets NO hint even for an owned delegate.
func TestDisposalHint_SessionWithoutDisposeOpGetsNoHint(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	r := newWtDlgRepo(t, c)

	desc := disposalHintLane(t, r, "dlg_01ISODISPOSALHINT0000001")
	r.s.reg.Remove("manage_worktree")
	report := r.s.isolatedDelegateWorktreeReport(desc)
	if report == nil {
		t.Fatal("report is nil")
	}
	if report.DisposalHint != "" {
		t.Errorf("session without dispose op must get no hint, got %q", report.DisposalHint)
	}
}

// A non-isolated delegate has no lane report at all, so no hint surfaces.
func TestDisposalHint_NonIsolatedDelegateHasNoReport(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	r := newWtDlgRepo(t, c)

	desc := &jobstore.DelegateRestoreDescriptor{WorkingDir: r.mainRoot, ParentSessionID: r.s.id}
	if report := r.s.isolatedDelegateWorktreeReport(desc); report != nil {
		t.Errorf("non-isolated delegate must have no lane report, got %+v", report)
	}
}

// Rendering the hint on either surface runs no git: a git-recording shim is
// installed, a report is hand-built (no git), and the render paths are
// exercised. The shim log must stay empty.
func TestDisposalHint_RenderRunsNoGit(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	r := newWtDlgRepo(t, c)

	logPath := gitArgvRecordingRepoShim(t, r.mainRoot)
	report := &delegateWorktreeReport{Path: "/lane", Branch: "b", DisposalHint: wantDisposalSentence}

	_ = delegateWorktreeToolResultFrom(report)
	_ = formatJobNotificationBlock(
		jobNotification{JobID: "job_1", JobType: string(jobstore.JobDelegate), Status: "finished"},
		notificationExcerpt{worktree: report},
	)

	if data, err := os.ReadFile(logPath); err == nil && len(strings.TrimSpace(string(data))) != 0 {
		t.Fatalf("render invoked git:\n%s", data)
	}
}
