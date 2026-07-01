package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func TestS3Cov_DeliveredStatus(t *testing.T) {
	t.Parallel()
	if deliveredStatus(true) != "delivered" || deliveredStatus(false) != "not_delivered" {
		t.Fatal("deliveredStatus wrong")
	}
}

func TestS3Cov_FormatDelegateSend(t *testing.T) {
	t.Parallel()
	out := delegateSendResult{
		DelegateID:          "d-1",
		StartedJobID:        "j-2",
		Action:              "send",
		Status:              "running",
		RunningInBackground: true,
		Watching:            true,
		WaitIgnoredReason:   "already complete",
		Output:              s3cov_strptr("child reply"),
		Watches: []watchListEntry{
			{ID: "w1", Source: "src", Condition: "on_complete"},
		},
		StructuredResult:      map[string]any{"k": "v"},
		StructuredResultValid: func() *bool { b := true; return &b }(),
	}
	got := formatDelegateSend(out)
	for _, want := range []string{
		"child reply",
		"delegate_id d-1",
		"started_job_id j-2",
		"running in background",
		"watching",
		"wait ignored: already complete",
		"watches:",
		"w1 → src (on_complete)",
		"structured_result (valid=true)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatDelegateSend missing %q:\n%s", want, got)
		}
	}
}

func TestS3Cov_BoundedMatchLine(t *testing.T) {
	t.Parallel()
	short := "a short line"
	if boundedMatchLine(short) != short {
		t.Fatal("short line should pass through")
	}
	long := strings.Repeat("z", maxJobGrepLineBytes+50)
	got := boundedMatchLine(long)
	if len([]byte(got)) > maxJobGrepLineBytes {
		t.Fatalf("bounded line %d exceeds cap %d", len(got), maxJobGrepLineBytes)
	}
	if got == long {
		t.Fatal("long line should be truncated")
	}
}

func TestS3Cov_MaxJobGrepPatternJSONChars(t *testing.T) {
	t.Parallel()
	if got := maxJobGrepPatternJSONChars(16); got != 64 {
		t.Fatalf("floor: got %d want 64", got)
	}
	if got := maxJobGrepPatternJSONChars(4000); got != 1000 {
		t.Fatalf("quarter: got %d want 1000", got)
	}
}

func TestS3Cov_JobStatusArrayArg(t *testing.T) {
	t.Parallel()
	if got, err := jobStatusArrayArg(map[string]any{}, "status"); err != nil || got != nil {
		t.Fatalf("absent: %v %v", got, err)
	}
	if _, err := jobStatusArrayArg(map[string]any{"status": "notarray"}, "status"); err == nil {
		t.Fatal("expected array error")
	}
	if _, err := jobStatusArrayArg(map[string]any{"status": []any{"bogus"}}, "status"); err == nil {
		t.Fatal("expected invalid-status error")
	}
	got, err := jobStatusArrayArg(map[string]any{"status": []any{"running", "completed"}}, "status")
	if err != nil || len(got) != 2 || got[0] != jobstore.StatusRunning {
		t.Fatalf("valid: %v %v", got, err)
	}
}

func TestS3Cov_JobTypeArrayArg(t *testing.T) {
	t.Parallel()
	if got, err := jobTypeArrayArg(map[string]any{}, "type"); err != nil || got != nil {
		t.Fatalf("absent: %v %v", got, err)
	}
	if _, err := jobTypeArrayArg(map[string]any{"type": "x"}, "type"); err == nil {
		t.Fatal("expected array error")
	}
	got, err := jobTypeArrayArg(map[string]any{"type": []any{"shell", "delegate"}}, "type")
	if err != nil || len(got) != 2 {
		t.Fatalf("valid: %v %v", got, err)
	}
}

func TestS3Cov_WatchEventFilterArg(t *testing.T) {
	t.Parallel()
	if got, err := watchEventFilterArg(map[string]any{}); err != nil || got != nil {
		t.Fatalf("absent: %v %v", got, err)
	}
	if _, err := watchEventFilterArg(map[string]any{"event_filter": "x"}); err == nil {
		t.Fatal("expected object error")
	}
	if _, err := watchEventFilterArg(map[string]any{"event_filter": map[string]any{"tool_name": 1}}); err == nil {
		t.Fatal("expected string-value error")
	}
	if _, err := watchEventFilterArg(map[string]any{"event_filter": map[string]any{"unknown": "x"}}); err == nil {
		t.Fatal("expected unknown-field error")
	}
	// All-empty fields → nil filter.
	if got, err := watchEventFilterArg(map[string]any{"event_filter": map[string]any{"tool_name": "  "}}); err != nil || got != nil {
		t.Fatalf("empty filter: %v %v", got, err)
	}
	got, err := watchEventFilterArg(map[string]any{"event_filter": map[string]any{"tool_name": "grep", "status": "ok"}})
	if err != nil || got == nil || got.ToolName != "grep" || got.Status != "ok" {
		t.Fatalf("valid: %+v %v", got, err)
	}
}
