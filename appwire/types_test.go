package appwire

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestLaunchConfigEnabledPluginsJSONPresence(t *testing.T) {
	nilRaw, err := json.Marshal(LaunchConfigLayer{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(nilRaw, []byte(`"enabledPlugins"`)) {
		t.Fatalf("nil encoded: %s", nilRaw)
	}

	empty := []string{}
	emptyRaw, err := json.Marshal(LaunchConfigLayer{EnabledPlugins: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(emptyRaw, []byte(`"enabledPlugins":[]`)) {
		t.Fatalf("empty lost: %s", emptyRaw)
	}

	var roundTrip LaunchConfigLayer
	if err := json.Unmarshal(emptyRaw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.EnabledPlugins == nil || len(*roundTrip.EnabledPlugins) != 0 {
		t.Fatalf("round trip = %#v", roundTrip.EnabledPlugins)
	}

	named := []string{"alpha", "beta"}
	namedRaw, err := json.Marshal(LaunchConfigLayer{EnabledPlugins: &named})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(namedRaw, []byte(`"enabledPlugins":["alpha","beta"]`)) {
		t.Fatalf("named selection lost: %s", namedRaw)
	}
	var namedRoundTrip LaunchConfigLayer
	if err := json.Unmarshal(namedRaw, &namedRoundTrip); err != nil {
		t.Fatal(err)
	}
	if namedRoundTrip.EnabledPlugins == nil || len(*namedRoundTrip.EnabledPlugins) != 2 || (*namedRoundTrip.EnabledPlugins)[0] != "alpha" || (*namedRoundTrip.EnabledPlugins)[1] != "beta" {
		t.Fatalf("named round trip = %#v", namedRoundTrip.EnabledPlugins)
	}
}

func TestPluginPreviewWireShape(t *testing.T) {
	empty := []string{}
	in := PluginPreviewParams{CWD: "/work", LaunchOverrides: &LaunchConfigLayer{EnabledPlugins: &empty}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"enabledPlugins":[]`) {
		t.Fatalf("explicit empty selection lost: %s", raw)
	}
	want := PluginPreviewResponse{Plugins: []PluginLaunchCandidate{{
		Name: "alpha", Version: "1.2.3", Description: "desc", Source: "directory",
		Marketplace: "acme", Path: "/plugins/alpha", Selected: true,
		SkillCount: 1, AgentCount: 2, CommandCount: 3, HookCount: 4, MCPCount: 5,
	}}, Diagnostics: []PluginDiagnostic{{Name: "bad", Path: "/bad", Source: "directory", Message: "invalid"}}, SelectionErrors: []PluginSelectionError{{Name: "missing", Reason: "no valid plugin candidate"}}}
	raw, err = json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got PluginPreviewResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Plugins) != 1 || got.Plugins[0].MCPCount != 5 || got.Diagnostics[0].Source != "directory" || got.SelectionErrors[0].Reason != "no valid plugin candidate" {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestThreadResumeParamsRemainSelectionFree(t *testing.T) {
	raw, err := json.Marshal(ThreadResumeParams{Ref: "local:session", Session: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "enabledPlugins") || strings.Contains(string(raw), "launchOverrides") {
		t.Fatalf("resume wire gained launch selection fields: %s", raw)
	}
}

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

// TestEvenerDiagnosticsJobsJSONRoundTrip verifies the job-control diagnostics
// wire shape uses the job surface, not the legacy subagent one.
func TestEvenerDiagnosticsJobsJSONRoundTrip(t *testing.T) {
	exitCode := 2
	in := EvenerDiagnostics{
		Jobs: []EvenerJobInfo{
			{
				JobID:            "job_1",
				JobType:          "shell",
				Status:           "failed",
				Reason:           "exit",
				ExitCode:         &exitCode,
				OutputBytes:      0,
				TranscriptRef:    "local:child",
				FromWatch:        true,
				ParentDelegateID: "dlg_parent",
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
		`"jobType":"shell"`,
		`"status":"failed"`,
		`"reason":"exit"`,
		`"exitCode":2`,
		`"outputBytes":0`,
		`"transcriptRef":"local:child"`,
		`"fromWatch":true`,
		`"parentDelegateId":"dlg_parent"`,
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
	var out EvenerDiagnostics
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Jobs) != 1 {
		t.Fatalf("roundtrip jobs len=%d, want 1", len(out.Jobs))
	}
	job := out.Jobs[0]
	if job.JobID != "job_1" || job.JobType != "shell" || job.Status != "failed" ||
		job.Reason != "exit" || job.ExitCode == nil || *job.ExitCode != exitCode ||
		job.OutputBytes != 0 || job.TranscriptRef != "local:child" || !job.FromWatch || job.ParentDelegateID != "dlg_parent" {
		t.Fatalf("roundtrip job=%+v", job)
	}
}

func TestNavigationReadWireTypesPreservePagingAndRawData(t *testing.T) {
	zero := uint32(0)
	params := NavigationReadParams{
		Resource:   "project_page",
		Section:    "live",
		SectionID:  "pin-a",
		Catalog:    "projects",
		ProjectKey: "project-a",
		Tier:       "recent",
		Ref:        "local:session-a",
		Offset:     &zero,
		Limit:      &zero,
		ETag:       "etag-a",
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal params fields: %v", err)
	}
	for name, want := range map[string]string{
		"resource":   `"project_page"`,
		"section":    `"live"`,
		"sectionId":  `"pin-a"`,
		"catalog":    `"projects"`,
		"projectKey": `"project-a"`,
		"tier":       `"recent"`,
		"ref":        `"local:session-a"`,
		"offset":     "0",
		"limit":      "0",
		"etag":       `"etag-a"`,
	} {
		if got := string(fields[name]); got != want {
			t.Fatalf("field %q = %s, want %s in %s", name, got, want, raw)
		}
	}
	var decoded NavigationReadParams
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if decoded.Offset == nil {
		t.Fatal("decoded offset is nil; explicit zero must remain distinguishable from omitted")
	}
	if *decoded.Offset != 0 {
		t.Fatalf("decoded offset = %d, want 0", *decoded.Offset)
	}
	if decoded.Limit == nil {
		t.Fatal("decoded limit is nil; explicit zero must remain distinguishable from omitted")
	}
	if *decoded.Limit != 0 {
		t.Fatalf("decoded limit = %d, want 0", *decoded.Limit)
	}

	withoutPage, err := json.Marshal(NavigationReadParams{Resource: "manifest"})
	if err != nil {
		t.Fatalf("marshal unpaged params: %v", err)
	}
	if got, want := string(withoutPage), `{"resource":"manifest"}`; got != want {
		t.Fatalf("omitted paging = %s, want %s", got, want)
	}

	response := NavigationReadResponse{
		Status:       "ok",
		GenerationID: "generation-a",
		Revision:     7,
		ETag:         "etag-a",
		Data:         json.RawMessage(`{"generation_id":"generation-a","revision":7,"sessions":[]}`),
	}
	raw, err = json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var decodedResponse NavigationReadResponse
	if err := json.Unmarshal(raw, &decodedResponse); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if decodedResponse.Status != "ok" || decodedResponse.GenerationID != "generation-a" || decodedResponse.Revision != 7 || decodedResponse.ETag != "etag-a" {
		t.Fatalf("decoded response envelope = %+v", decodedResponse)
	}
	if string(decodedResponse.Data) != string(response.Data) {
		t.Fatalf("decoded response data = %s, want %s", decodedResponse.Data, response.Data)
	}

	notModified, err := json.Marshal(NavigationReadResponse{
		Status:       "not_modified",
		GenerationID: "generation-a",
		Revision:     7,
		ETag:         "etag-a",
	})
	if err != nil {
		t.Fatalf("marshal not-modified response: %v", err)
	}
	if got, want := string(notModified), `{"status":"not_modified","generationId":"generation-a","revision":7,"etag":"etag-a"}`; got != want {
		t.Fatalf("not-modified response = %s, want %s", got, want)
	}
}

func TestEvenerDiagnosticsDelegatesJSONRoundTrip(t *testing.T) {
	valid := true
	exhaustionResumable := false
	runningForMS := int64(1200)
	in := EvenerDiagnostics{
		Delegates: []EvenerDelegateInfo{{
			DelegateID: "dlg_1", OwnerSessionID: "owner", RootSessionID: "root", ChildSessionID: "child", TranscriptRef: "local:child",
			ParentDelegateID: "dlg_parent", Type: "delegate", Lifecycle: "idle", Phase: "idle", Status: "idle", Resumable: true, NeedsAttention: true,
			ProjectionRevision: 9, Message: json.RawMessage("null"), StructuredResult: json.RawMessage("null"), StructuredValid: &valid,
			StructuredReason: "valid null", ExhaustionBudget: "max_tool_rounds_per_input", ExhaustionLimit: 4,
			ExhaustionResumable: &exhaustionResumable, RunningForMS: &runningForMS, Warnings: []string{"warning"}, Diagnostics: []string{"diagnostic"},
			Usage:    &EvenerUsage{InputTokens: 3, OutputTokens: 2, CacheReadTokens: 1, TotalTokens: 5},
			Worktree: &JobActivityWorktree{Path: "/tmp/lane", Branch: "delegate/lane", HeadSHA: "abc", Ahead: 2, Dirty: true},
		}},
		TurnSlots: &EvenerTurnSlots{InUse: 2, Cap: 50, Jobs: 1, Drives: 1},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal delegates diagnostics: %v", err)
	}
	for _, want := range []string{`"delegates"`, `"delegateId":"dlg_1"`, `"message":null`, `"structuredResult":null`, `"needsAttention":true`, `"projectionRevision":9`, `"turnSlots"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("marshal=%s missing %s", raw, want)
		}
	}
	for _, forbidden := range []string{"waitIgnoredReason", "jobId", "generation"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("stable delegate diagnostics leaked %s: %s", forbidden, raw)
		}
	}
	var out EvenerDiagnostics
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal delegates diagnostics: %v", err)
	}
	if len(out.Delegates) != 1 || out.Delegates[0].DelegateID != "dlg_1" || !out.Delegates[0].NeedsAttention || string(out.Delegates[0].Message) != "null" ||
		out.Delegates[0].Usage == nil || out.Delegates[0].Usage.TotalTokens != 5 || out.Delegates[0].Worktree == nil || !out.Delegates[0].Worktree.Dirty ||
		out.TurnSlots == nil || out.TurnSlots.InUse != 2 || out.TurnSlots.Drives != 1 {
		t.Fatalf("roundtrip delegates diagnostics = %+v", out)
	}
}

func TestEvenerJobInfo_ExhaustionFields(t *testing.T) {
	resumable := true
	in := EvenerJobInfo{
		JobID:            "job_exhausted",
		JobType:          "delegate",
		Status:           "exhausted",
		Reason:           "tool_round_budget_exhausted",
		ExhaustionBudget: "max_tool_rounds_per_input",
		ExhaustionLimit:  1,
		Resumable:        &resumable,
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	encoded := string(raw)
	for _, want := range []string{
		`"status":"exhausted"`,
		`"reason":"tool_round_budget_exhausted"`,
		`"exhaustionBudget":"max_tool_rounds_per_input"`,
		`"exhaustionLimit":1`,
		`"resumable":true`,
	} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("marshal=%s missing %s", encoded, want)
		}
	}
	for _, forbidden := range []string{"exhaustion_budget", "exhaustion_limit"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("marshal=%s should not contain %s", encoded, forbidden)
		}
	}

	var out EvenerJobInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ExhaustionBudget != "max_tool_rounds_per_input" || out.ExhaustionLimit != 1 || out.Resumable == nil || !*out.Resumable {
		t.Fatalf("roundtrip exhaustion metadata = %+v", out)
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

// TestEvenerThreadMetricsJSONRoundTrip (WS2 A7) verifies the wire shape of the
// live working-state/token metrics on EvenerThread: camelCase JSON tags and a
// correct round trip for a populated set of values.
func TestEvenerThreadMetricsJSONRoundTrip(t *testing.T) {
	in := EvenerThread{
		Usage:               &EvenerUsage{InputTokens: 1},
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
	var out EvenerThread
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

// TestEvenerThreadMetricsOmitEmpty (WS2 A7) verifies that a zero-value
// EvenerThread omits usage, workMillis, and activeTurnStartedAt entirely — a
// nil Usage pointer (rather than a rendered zero EvenerUsage) and the omitempty
// scalars both drop out, so fresh/old-daemon/codex threads don't render ↑0 ↓0.
func TestEvenerThreadMetricsOmitEmpty(t *testing.T) {
	raw, err := json.Marshal(EvenerThread{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, banned := range []string{`"usage"`, `"workMillis"`, `"activeTurnStartedAt"`, `"cost"`} {
		if strings.Contains(got, banned) {
			t.Fatalf("marshal=%s should have omitted %s", got, banned)
		}
	}
}

// TestEvenerThreadCostJSONRoundTrip verifies EvenerThread.Cost is the
// session-level estimated dollar total that rides the thread snapshot in
// camelCase, exactly like the per-turn Turn.Cost string — same "~$X.XX"
// shape, same omit-when-absent honesty.
func TestEvenerThreadCostJSONRoundTrip(t *testing.T) {
	in := EvenerThread{Usage: &EvenerUsage{InputTokens: 1}, Cost: "~$0.42"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, `"cost":"~$0.42"`) {
		t.Fatalf("marshal=%s missing cost", got)
	}
	var out EvenerThread
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Cost != in.Cost {
		t.Fatalf("roundtrip cost=%q, want %q", out.Cost, in.Cost)
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

func TestEvenerThread_AskPendingRoundTrips(t *testing.T) {
	th := EvenerThread{Ref: "local:01A", AskPending: true}
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
	in := Turn{Usage: &EvenerUsage{InputTokens: 1}, Cost: "~$0.01"}
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
// entirely (mirrors TestEvenerThreadMetricsOmitEmpty's pattern) — a turn with
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

// TestAuthDevicePollResponse_StatusOmittedUnlessAuthorized locks in that
// Status is now a pointer: encoding/json can genuinely omit a nil pointer,
// so "pending"/"expired" polls (which never set Status) no longer ship a
// zero-valued status object, matching the type's own doc comment ("Status is
// nil ... except when authorized") and the already-optional generated
// TypeScript type. The lone frontend consumer (oauthDialogs.tsx) branches
// only on State and never reads Status, so this is wire-safe.
func TestAuthDevicePollResponse_StatusOmittedUnlessAuthorized(t *testing.T) {
	pending, err := json.Marshal(AuthDevicePollResponse{State: "pending"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(pending), `"status"`) {
		t.Fatalf("marshal=%s should have omitted status for a pending poll", pending)
	}

	authorized, err := json.Marshal(AuthDevicePollResponse{
		State:  "authorized",
		Status: &AuthStatusResponse{Provider: "openai", SignedIn: true},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(authorized)
	if !strings.Contains(got, `"status":{"provider":"openai"`) {
		t.Fatalf("marshal=%s missing populated status", got)
	}
}
