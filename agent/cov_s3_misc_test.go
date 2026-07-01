package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

func TestS3Cov_ToolDefinitions(t *testing.T) {
	t.Parallel()
	s := newSession(t)
	defs := s.ToolDefinitions()
	if len(defs) == 0 {
		t.Fatal("expected some advertised tool definitions")
	}
	// The returned slice must be a copy — mutating it must not affect the cache.
	defs[0] = llm.ToolDefinition{Name: "clobbered"}
	again := s.ToolDefinitions()
	if again[0].Name == "clobbered" {
		t.Fatal("ToolDefinitions must return a defensive copy")
	}
}

func TestS3Cov_ToolResultStateOrContent(t *testing.T) {
	t.Parallel()
	if got := toolResultStateOrContent(nil); got != "" {
		t.Fatalf("nil => %q", got)
	}
	// ToolState wins over Content when present.
	r := &llm.ToolResultData{ToolState: []byte(`{"state":1}`), Content: "content"}
	if got := toolResultStateOrContent(r); got != `{"state":1}` {
		t.Fatalf("state => %q", got)
	}
	// Falls back to Content when no state.
	if got := toolResultStateOrContent(&llm.ToolResultData{Content: "body"}); got != "body" {
		t.Fatalf("content => %q", got)
	}
}

func TestS3Cov_ExtractJobResult(t *testing.T) {
	t.Parallel()
	info, ok := extractJobResult(`{"job_id":"J1","status":"completed","transcript_ref":"local:x"}`)
	if !ok || info.jobID != "J1" || info.status != "completed" || info.transcriptRef != "local:x" {
		t.Fatalf("extract => %+v ok=%v", info, ok)
	}
	if _, ok := extractJobResult(`not json`); ok {
		t.Fatal("non-json should be false")
	}
	if _, ok := extractJobResult(`{"status":"x"}`); ok {
		t.Fatal("no job id nor ref should be false")
	}
}

func TestS3Cov_JobToolResultMaxChars(t *testing.T) {
	t.Parallel()
	if got := jobToolResultMaxChars(nil, "job_status"); got != jobToolResultDefaultMaxChar {
		t.Fatalf("nil reg => %d", got)
	}
	reg := tool.NewRegistry()
	// Unregistered tool → default.
	if got := jobToolResultMaxChars(reg, "nope"); got != jobToolResultDefaultMaxChar {
		t.Fatalf("unregistered => %d", got)
	}
}

func TestS3Cov_ShellToolResultMaxChars(t *testing.T) {
	t.Parallel()
	if got := shellToolResultMaxChars(nil); got != shellToolResultDefaultMaxChars {
		t.Fatalf("nil reg => %d", got)
	}
	reg := tool.NewRegistry()
	if got := shellToolResultMaxChars(reg); got != shellToolResultDefaultMaxChars {
		t.Fatalf("unregistered => %d", got)
	}
}

func TestS3Cov_SessionJobManager_Nil(t *testing.T) {
	t.Parallel()
	if _, err := sessionJobManager(nil); err == nil {
		t.Fatal("expected error for nil session")
	}
	if _, err := sessionJobManager(&Session{}); err == nil {
		t.Fatal("expected error for session without job manager")
	}
}

func TestS3Cov_MarshalBoundedJSON(t *testing.T) {
	t.Parallel()
	// Fits.
	got, err := marshalBoundedJSON(map[string]any{"k": "v"}, 0)
	if err != nil || got == "" {
		t.Fatalf("fit: %q %v", got, err)
	}
	// Exceeds cap → error.
	if _, err := marshalBoundedJSON(map[string]any{"k": "vvvvvvvvvvvvvvvvvvvv"}, 5); err == nil {
		t.Fatal("expected over-cap error")
	}
}
