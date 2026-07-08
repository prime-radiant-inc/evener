package appwire

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestThreadItemOutputImagesJSONRoundTrip(t *testing.T) {
	item := ThreadItem{
		Type:     "commandExecution",
		ID:       "item_tool_1",
		ToolName: "shell",
		OutputImages: []OutputImage{{
			Source:    "shell-path",
			Name:      "out.png",
			MediaType: "image/png",
			Size:      67,
			URL:       "/doc/image?session=01ABC&path=out.png",
			SHA:       "abc123",
			Path:      "out.png",
		}},
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"outputImages"`) {
		t.Fatalf("encoded item missing outputImages: %s", data)
	}
	var got ThreadItem
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.OutputImages) != 1 {
		t.Fatalf("OutputImages length=%d, want 1", len(got.OutputImages))
	}
	img := got.OutputImages[0]
	if img.Source != "shell-path" || img.Name != "out.png" || img.MediaType != "image/png" || img.Size != 67 ||
		img.URL != "/doc/image?session=01ABC&path=out.png" || img.SHA != "abc123" || img.Path != "out.png" {
		t.Fatalf("OutputImages[0]=%+v", img)
	}
}

// TestDiagnosticCauseJSONRoundTrip (kata cmfz) verifies the wire shape of
// DiagnosticCause: camelCase JSON tags (per the appwire camelCase
// carve-out) and omitempty on all optional fields so a nil provider
// payload encodes as an empty object rather than spurious zero fields.
func TestDiagnosticCauseJSONRoundTrip(t *testing.T) {
	in := DiagnosticCause{
		Kind:     "provider",
		Provider: "anthropic",
		Model:    "claude-opus-4-7",
		Status:   503,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{`"kind":"provider"`, `"provider":"anthropic"`, `"model":"claude-opus-4-7"`, `"status":503`} {
		if !strings.Contains(got, want) {
			t.Fatalf("marshal=%s missing %s", got, want)
		}
	}
	var out DiagnosticCause
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip=%+v, want %+v", out, in)
	}
}

// TestSerfDiagnosticsJobsJSONRoundTrip verifies the job-control diagnostics
// wire shape uses the job surface, not the legacy subagent one.
func TestSerfDiagnosticsJobsJSONRoundTrip(t *testing.T) {
	exitCode := 2
	in := SerfDiagnostics{
		Jobs: []SerfJobInfo{
			{
				JobID:         "job_1",
				JobType:       "delegate",
				Status:        "failed",
				Reason:        "exit",
				ExitCode:      &exitCode,
				OutputBytes:   0,
				TranscriptRef: "local:child",
				FromWatch:     true,
			},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`"jobs"`,
		`"jobId":"job_1"`,
		`"jobType":"delegate"`,
		`"status":"failed"`,
		`"reason":"exit"`,
		`"exitCode":2`,
		`"outputBytes":0`,
		`"transcriptRef":"local:child"`,
		`"fromWatch":true`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("marshal=%s missing %s", got, want)
		}
	}
	for _, banned := range []string{`"subagents"`, `"turnsUsed"`, `"job_id"`, `"job_type"`} {
		if strings.Contains(got, banned) {
			t.Fatalf("marshal=%s should not contain %s", got, banned)
		}
	}
	var out SerfDiagnostics
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Jobs) != 1 {
		t.Fatalf("roundtrip jobs len=%d, want 1", len(out.Jobs))
	}
	job := out.Jobs[0]
	if job.JobID != "job_1" || job.JobType != "delegate" || job.Status != "failed" ||
		job.Reason != "exit" || job.ExitCode == nil || *job.ExitCode != exitCode ||
		job.OutputBytes != 0 || job.TranscriptRef != "local:child" || !job.FromWatch {
		t.Fatalf("roundtrip job=%+v", job)
	}
}

// TestInstanceListResponseJSONRoundTrip verifies the wire shape of
// InstanceListResponse and InstanceEntry: camelCase JSON tags and correct
// field round-trip for a populated entry.
func TestInstanceListResponseJSONRoundTrip(t *testing.T) {
	in := InstanceListResponse{
		Instances: []InstanceEntry{
			{
				Name:           "my-openai",
				Type:           "openai",
				APIStyle:       "openai",
				BaseURL:        "https://api.openai.com/v1",
				IsDefault:      true,
				AuthModes:      []string{"apiKey"},
				ActiveSource:   "file",
				HasStoredFile:  true,
				HasStoredOAuth: false,
				EnvVar:         "OPENAI_API_KEY",
				StoredEmail:    "",
			},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`"instances"`,
		`"name":"my-openai"`,
		`"type":"openai"`,
		`"apiStyle":"openai"`,
		`"baseUrl":"https://api.openai.com/v1"`,
		`"isDefault":true`,
		`"authModes":["apiKey"]`,
		`"activeSource":"file"`,
		`"hasStoredFile":true`,
		`"hasStoredOAuth":false`,
		`"envVar":"OPENAI_API_KEY"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("marshal=%s missing %s", got, want)
		}
	}
	var out InstanceListResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Instances) != 1 {
		t.Fatalf("roundtrip instances len=%d, want 1", len(out.Instances))
	}
	e := out.Instances[0]
	if e.Name != "my-openai" || e.Type != "openai" || e.APIStyle != "openai" ||
		e.BaseURL != "https://api.openai.com/v1" || !e.IsDefault ||
		len(e.AuthModes) != 1 || e.AuthModes[0] != "apiKey" ||
		e.ActiveSource != "file" || !e.HasStoredFile || e.HasStoredOAuth ||
		e.EnvVar != "OPENAI_API_KEY" {
		t.Fatalf("roundtrip entry=%+v", e)
	}
}

// TestInstanceCreateParamsJSONRoundTrip verifies the wire shape of
// InstanceCreateParams: camelCase JSON tags and field preservation.
func TestInstanceCreateParamsJSONRoundTrip(t *testing.T) {
	in := InstanceCreateParams{
		Type:     "openai",
		Name:     "my-openai",
		APIStyle: "openai",
		BaseURL:  "https://api.openai.com/v1",
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`"type":"openai"`,
		`"name":"my-openai"`,
		`"apiStyle":"openai"`,
		`"baseUrl":"https://api.openai.com/v1"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("marshal=%s missing %s", got, want)
		}
	}
	var out InstanceCreateParams
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip=%+v, want %+v", out, in)
	}
}

// TestDiagnosticCauseOmitEmpty (kata cmfz) verifies that the optional
// fields drop out of the JSON encoding when zero, so kind-only causes
// stay compact on the wire.
func TestDiagnosticCauseOmitEmpty(t *testing.T) {
	raw, err := json.Marshal(DiagnosticCause{Kind: "provider"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, banned := range []string{`"provider":`, `"model":`, `"status":`} {
		if strings.Contains(got, banned) {
			t.Fatalf("marshal=%s should have omitted %s", got, banned)
		}
	}
	if !strings.Contains(got, `"kind":"provider"`) {
		t.Fatalf("marshal=%s missing kind", got)
	}
}

// TestSerfThreadMetricsJSONRoundTrip (WS2 A7) verifies the wire shape of the
// live working-state/token metrics on SerfThread: camelCase JSON tags and a
// correct round trip for a populated set of values.
func TestSerfThreadMetricsJSONRoundTrip(t *testing.T) {
	in := SerfThread{
		Usage:               &SerfUsage{InputTokens: 1},
		WorkMillis:          2,
		ActiveTurnStartedAt: 3,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		`"usage":{"inputTokens":1}`,
		`"workMillis":2`,
		`"activeTurnStartedAt":3`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("marshal=%s missing %s", got, want)
		}
	}
	var out SerfThread
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Usage == nil || *out.Usage != *in.Usage {
		t.Fatalf("roundtrip usage=%+v, want %+v", out.Usage, in.Usage)
	}
	if out.WorkMillis != in.WorkMillis || out.ActiveTurnStartedAt != in.ActiveTurnStartedAt {
		t.Fatalf("roundtrip workMillis/activeTurnStartedAt=%d/%d, want %d/%d",
			out.WorkMillis, out.ActiveTurnStartedAt, in.WorkMillis, in.ActiveTurnStartedAt)
	}
}

// TestSerfThreadMetricsOmitEmpty (WS2 A7) verifies that a zero-value
// SerfThread omits usage, workMillis, and activeTurnStartedAt entirely — a
// nil Usage pointer (rather than a rendered zero SerfUsage) and the omitempty
// scalars both drop out, so fresh/old-daemon/codex threads don't render ↑0 ↓0.
func TestSerfThreadMetricsOmitEmpty(t *testing.T) {
	raw, err := json.Marshal(SerfThread{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, banned := range []string{`"usage"`, `"workMillis"`, `"activeTurnStartedAt"`} {
		if strings.Contains(got, banned) {
			t.Fatalf("marshal=%s should have omitted %s", got, banned)
		}
	}
}

// TestModelListResponseRecentJSONRoundTrip verifies the model picker's
// Recent group rides ModelListResponse as an ordinary struct field (no new
// appwire method), snake_case on the wire, and round-trips.
func TestModelListResponseRecentJSONRoundTrip(t *testing.T) {
	in := ModelListResponse{
		Data:   []ModelDescriptor{{Provider: "anthropic", Model: "claude-opus-4-6"}},
		Recent: []ModelDescriptor{{Provider: "openai", Model: "gpt-5.2"}},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, `"recent":[{"provider":"openai","model":"gpt-5.2"}]`) {
		t.Fatalf("marshal=%s missing recent", got)
	}
	var out ModelListResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Recent) != 1 || out.Recent[0] != in.Recent[0] {
		t.Fatalf("roundtrip recent=%+v, want %+v", out.Recent, in.Recent)
	}
}

// TestModelListResponseRecentOmitEmpty verifies a response with no recent
// models (fresh install, no history) omits the field entirely rather than
// rendering an empty array.
func TestModelListResponseRecentOmitEmpty(t *testing.T) {
	raw, err := json.Marshal(ModelListResponse{Data: []ModelDescriptor{{Provider: "a", Model: "b"}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"recent"`) {
		t.Fatalf("marshal=%s should have omitted recent", raw)
	}
}

func TestSerfThread_AskPendingRoundTrips(t *testing.T) {
	th := SerfThread{Ref: "local:01A", AskPending: true}
	data, err := json.Marshal(th)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"askPending":true`) {
		t.Fatalf("expected askPending:true in wire JSON, got %s", data)
	}
}

// TestTurnUsageCostJSONRoundTrip verifies Turn.Usage/Turn.Cost are per-turn
// (not cumulative-session) fields that round-trip on the wire in camelCase,
// matching appwire's own JSON convention.
func TestTurnUsageCostJSONRoundTrip(t *testing.T) {
	in := Turn{Usage: &SerfUsage{InputTokens: 1}, Cost: "~$0.01"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, `"usage":{"inputTokens":1}`) {
		t.Fatalf("marshal=%s missing usage", got)
	}
	if !strings.Contains(got, `"cost":"~$0.01"`) {
		t.Fatalf("marshal=%s missing cost", got)
	}
}

// TestTurnUsageCostOmitEmpty verifies a zero-value Turn omits usage and cost
// entirely (mirrors TestSerfThreadMetricsOmitEmpty's pattern) — a turn with
// no computable usage/cost shouldn't render an empty cluster.
func TestTurnUsageCostOmitEmpty(t *testing.T) {
	raw, err := json.Marshal(Turn{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, banned := range []string{`"usage"`, `"cost"`} {
		if strings.Contains(got, banned) {
			t.Fatalf("marshal=%s should have omitted %s", got, banned)
		}
	}
}
