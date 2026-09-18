package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestProjectBoundedActivityTree_CapsTheSessionLabel pins the fix for an
// envelope whose own label alone exceeds the limit. A session with no
// generated name labels itself with its OriginalPrompt verbatim, so a pasted
// multi-megabyte prompt used to make every page reporting that session — its
// own and any continuation carrying it as an ancestor — go out over
// activityMaxEncodedBytes with no entry left to drop. The label is capped at
// projection time instead, and the page fits.
func TestProjectBoundedActivityTree_CapsTheSessionLabel(t *testing.T) {
	t.Parallel()
	prompt := strings.Repeat("p", 4<<20) // a 4MB OriginalPrompt
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		Label: prompt,
	}
	got, err := projectBoundedActivityTree(snap, "root", 0, 0, 0, time.Unix(10, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > activityMaxEncodedBytes {
		t.Fatalf("page = %d bytes, over the %d-byte limit", len(raw), activityMaxEncodedBytes)
	}
	if got.Root.Branch.Error != "" {
		t.Fatalf("branch error = %q, want none: the label is capped, not reported as unfittable", got.Root.Branch.Error)
	}
	if n := len([]rune(got.Root.Label)); n > activityMaxLabelRunes {
		t.Fatalf("label = %d runes, want at most %d", n, activityMaxLabelRunes)
	}
	if !strings.HasSuffix(got.Root.Label, "…") {
		t.Fatalf("label %q does not say it was cut", got.Root.Label)
	}
}

// TestProjectActivitySession_CapsDelegateProse pins that a delegate's
// unbounded prose fields — its Mandate/Task and Description — are capped at
// projection time. They sit on every delegate in the ancestor chain a
// continuation page carries, so an unbounded brief is a fixed part of the
// envelope that trimming entries cannot shrink.
func TestProjectActivitySession_CapsDelegateProse(t *testing.T) {
	t.Parallel()
	prose := strings.Repeat("t", 3*activityMaxDelegateProseRunes)
	row := stableActivitySnapshot("dlg_1", "root", "child", prose)
	row.descriptor.Description = prose
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		StableDelegates: map[string]delegateSnapshot{"dlg_1": row},
	}
	got := projectActivitySession(snap, newActivityBudget())
	if len(got.Entries) != 1 || got.Entries[0].Delegate == nil {
		t.Fatalf("entries = %+v, want one delegate", got.Entries)
	}
	delegate := got.Entries[0].Delegate
	for name, value := range map[string]string{
		"Mandate":     delegate.Mandate,
		"Task":        delegate.Task,
		"Description": delegate.Description,
	} {
		if n := len([]rune(value)); n > activityMaxDelegateProseRunes {
			t.Fatalf("delegate %s = %d runes, want at most %d", name, n, activityMaxDelegateProseRunes)
		}
		if !strings.HasSuffix(value, "…") {
			t.Fatalf("delegate %s does not say it was cut", name)
		}
	}
}

// TestProjectActivitySession_CollapsesUnsupportedTypeErrors pins that a
// session whose journal holds many records this projection cannot render
// reports them once, counted, rather than appending one sentence per record
// to Branch.Error — an unbounded envelope input, and unreadable besides.
func TestProjectActivitySession_CollapsesUnsupportedTypeErrors(t *testing.T) {
	t.Parallel()
	const n = 64
	records := make([]*jobstore.JobRecord, 0, n)
	for i := range n {
		records = append(records, &jobstore.JobRecord{
			JobID:          fmt.Sprintf("job_%d", i),
			Type:           jobstore.JobType("unknown"),
			OwnerSessionID: "root",
			Status:         jobstore.StatusRunning,
		})
	}
	snap := activitySessionSnapshot{SessionID: "root", Ref: "local:root", Jobs: records}
	got := projectActivitySession(snap, newActivityBudget())
	if c := strings.Count(got.Branch.Error, `job "`); c != 1 {
		t.Fatalf("branch error = %q, want it to name exactly one offender, got %d", got.Branch.Error, c)
	}
	if !strings.Contains(got.Branch.Error, "unsupported type") {
		t.Fatalf("branch error = %q, want it to say unsupported type", got.Branch.Error)
	}
	if want := fmt.Sprintf("%d", n); !strings.Contains(got.Branch.Error, want) {
		t.Fatalf("branch error = %q, want the count %s of collapsed records", got.Branch.Error, want)
	}
}

// TestProjectActivitySession_SingleUnsupportedTypeKeepsOriginalWording pins
// that the common one-offender case reads exactly as it always did rather
// than being reworded into the counted form.
func TestProjectActivitySession_SingleUnsupportedTypeKeepsOriginalWording(t *testing.T) {
	t.Parallel()
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root",
		Jobs: []*jobstore.JobRecord{{JobID: "j1", Type: jobstore.JobType("unknown"), OwnerSessionID: "root", Status: jobstore.StatusRunning}},
	}
	got := projectActivitySession(snap, newActivityBudget())
	want := fmt.Sprintf("job %q has unsupported type %q", "j1", jobstore.JobType("unknown"))
	if got.Branch.Error != want {
		t.Fatalf("branch error = %q, want %q", got.Branch.Error, want)
	}
}
