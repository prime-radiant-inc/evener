package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestW2Tail_derefString(t *testing.T) {
	if derefString(nil) != "" {
		t.Errorf("nil deref != empty")
	}
	v := "hi"
	if derefString(&v) != "hi" {
		t.Errorf("deref wrong")
	}
}

func TestW2Tail_projectDelegateRecord(t *testing.T) {
	if got := projectDelegateRecord(nil); got != (delegateListEntry{}) {
		t.Errorf("nil record = %+v", got)
	}
	rec := &jobstore.DelegateRecord{
		DelegateID: "dlg_1", Status: "running", CurrentJobID: "job_9",
		TranscriptRef: "local:x", Resumable: true, ParentDelegateID: "dlg_0",
	}
	got := projectDelegateRecord(rec)
	if got.DelegateID != "dlg_1" || got.Status != "running" || got.CurrentJobID != "job_9" || !got.Resumable {
		t.Errorf("projected = %+v", got)
	}
}

func TestW2Tail_marshalBoundedJSON(t *testing.T) {
	// Fits.
	s, err := marshalBoundedJSON(map[string]string{"a": "b"}, 100)
	if err != nil || !strings.Contains(s, `"a":"b"`) {
		t.Fatalf("fit path: %q err=%v", s, err)
	}
	// Exceeds bound.
	if _, err := marshalBoundedJSON(map[string]string{"a": "bbbbbbbbbb"}, 5); err == nil {
		t.Errorf("expected exceeds-bound error")
	}
	// Unbounded (maxChars <= 0) always fits.
	if _, err := marshalBoundedJSON(map[string]string{"a": "b"}, 0); err != nil {
		t.Errorf("unbounded err = %v", err)
	}
}

func TestW2Tail_marshalBoundedJSONWithFit(t *testing.T) {
	if _, ok, err := marshalBoundedJSONWithFit(map[string]int{"a": 1}, 100); !ok || err != nil {
		t.Errorf("fit: ok=%v err=%v", ok, err)
	}
	if _, ok, err := marshalBoundedJSONWithFit(map[string]string{"a": "bbbbbbbbbb"}, 5); ok || err != nil {
		t.Errorf("no-fit: ok=%v err=%v", ok, err)
	}
}

func TestW2Tail_jobToolResultMaxChars(t *testing.T) {
	if got := jobToolResultMaxChars(nil, "job_status"); got != jobToolResultDefaultMaxChar {
		t.Errorf("nil reg = %d", got)
	}
	reg := tool.NewRegistry()
	if got := jobToolResultMaxChars(reg, "absent"); got != jobToolResultDefaultMaxChar {
		t.Errorf("absent tool = %d", got)
	}

	register := func(name string, maxChars int) {
		t.Helper()
		if err := reg.Register(tool.RegisteredTool{
			Tool:  llm.Tool{Definition: llm.ToolDefinition{Name: name, Description: "d"}},
			Limit: schema.ToolOutputLimit{MaxChars: maxChars},
			Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
				return "", nil
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Below the JSON floor is clamped up to the floor.
	register("job_status", 100)
	if got := jobToolResultMaxChars(reg, "job_status"); got != jobToolResultMinJSONChars {
		t.Errorf("below-floor = %d, want %d", got, jobToolResultMinJSONChars)
	}
	// A comfortable limit is used verbatim.
	register("job_list", 9_000)
	if got := jobToolResultMaxChars(reg, "job_list"); got != 9_000 {
		t.Errorf("verbatim = %d", got)
	}
}

func TestW2Tail_validateJobGrepPattern(t *testing.T) {
	if err := validateJobGrepPattern("ok", 20_000); err != nil {
		t.Errorf("small pattern err = %v", err)
	}
	big := strings.Repeat("x", maxJobGrepPatternBytes+1)
	if err := validateJobGrepPattern(big, 20_000); err == nil {
		t.Errorf("expected too-many-bytes error")
	}
}

func TestW2Tail_jobTranscriptRef(t *testing.T) {
	if jobTranscriptRef(nil) != "" {
		t.Errorf("nil ref")
	}
	if got := jobTranscriptRef(&jobstore.JobRecord{TranscriptRef: "local:z"}); got != "local:z" {
		t.Errorf("explicit ref = %q", got)
	}
	if got := jobTranscriptRef(&jobstore.JobRecord{Type: jobstore.JobShell, JobID: "job_5"}); got != "job:job_5" {
		t.Errorf("shell ref = %q", got)
	}
	if got := jobTranscriptRef(&jobstore.JobRecord{Type: jobstore.JobDelegate}); got != "" {
		t.Errorf("delegate w/o ref = %q", got)
	}
}

// watchArgsFromToolArgs validates each operation and rejects malformed args.
func TestW2Tail_watchArgsFromToolArgs(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		ok   bool
	}{
		{"missing operation", map[string]any{}, false},
		{"target rejected", map[string]any{"operation": "create", "target": "x"}, false},
		{"send rejected", map[string]any{"operation": "create", "send": "x"}, false},
		{"create ok", map[string]any{"operation": "create", "source": "self"}, true},
		{"create empty source", map[string]any{"operation": "create"}, false},
		{"create dlg source", map[string]any{"operation": "create", "source": "dlg_1"}, false},
		{"list ok", map[string]any{"operation": "list"}, true},
		{"list with source", map[string]any{"operation": "list", "source": "self"}, false},
		{"inspect no id", map[string]any{"operation": "inspect"}, false},
		{"inspect ok", map[string]any{"operation": "inspect", "watch_id": "w1"}, true},
		{"clear ok", map[string]any{"operation": "clear", "watch_id": "w1"}, true},
		{"unsupported op", map[string]any{"operation": "frobnicate"}, false},
		{"wildcard source", map[string]any{"operation": "create", "source": "*"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := watchArgsFromToolArgs(tc.args)
			if tc.ok && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Errorf("expected error")
			}
		})
	}
}

// readJobOutputSnapshot returns the non-closed-store lookup error (not the
// closed-store retry) when the job id is unknown.
func TestW2Tail_readJobOutputSnapshot_MissingJob(t *testing.T) {
	s := newSession(t)
	jm, err := sessionJobManager(s)
	if err != nil {
		t.Fatalf("sessionJobManager: %v", err)
	}
	if _, err := s.readJobOutputSnapshot(jm, s, "job_missing", 100, true, nil); err == nil {
		t.Fatalf("expected error for missing job")
	}
}
