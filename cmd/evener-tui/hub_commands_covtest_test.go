package tui

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuipick"
	"primeradiant.com/evener/internal/appserver"
)

// TestCovPrettifyModelDisplayName exercises name prettification.
func TestCovPrettifyModelDisplayName(t *testing.T) {
	// Dated snapshot suffix stripped.
	got := prettifyModelDisplayName("openai/gpt-5-20240101")
	if got != "Openai/gpt 5" {
		t.Fatalf("got %q, want 'Openai/gpt 5'", got)
	}

	// With version suffix.
	got = prettifyModelDisplayName("openai/gpt-5-20240101-v2")
	if got != "Openai/gpt 5" {
		t.Fatalf("got %q, want 'Openai/gpt 5'", got)
	}

	// No suffix.
	got = prettifyModelDisplayName("claude-3-opus")
	if got != "Claude 3 Opus" {
		t.Fatalf("got %q, want 'Claude 3 Opus'", got)
	}

	// Empty first segment: uppercased to space-prefixed.
	got = prettifyModelDisplayName("-test")
	if got != " Test" {
		t.Fatalf("got %q, want ' Test'", got)
	}
}

// TestCovFormatModelContextWindow exercises compact window formatting.
func TestCovFormatModelContextWindow(t *testing.T) {
	if got := formatModelContextWindow(1_500_000); got != "1M" {
		t.Fatalf("got %q, want 1M", got)
	}
	if got := formatModelContextWindow(128_000); got != "128K" {
		t.Fatalf("got %q, want 128K", got)
	}
	if got := formatModelContextWindow(500); got != "500" {
		t.Fatalf("got %q, want 500", got)
	}
}

// TestCovModelPickerItemProvider exercises provider extraction.
func TestCovModelPickerItemProvider(t *testing.T) {
	// From group.
	item := tuipick.ModelPickerItem{Group: "openai", ID: "openai/gpt-5", Display: "GPT 5"}
	if got := modelPickerItemProvider(item); got != "openai" {
		t.Fatalf("got %q, want openai", got)
	}

	// From display (no group).
	item = tuipick.ModelPickerItem{Display: "openai/gpt-5"}
	if got := modelPickerItemProvider(item); got != "openai" {
		t.Fatalf("got %q, want openai", got)
	}

	// From ID (no group, no display).
	item = tuipick.ModelPickerItem{ID: "openai/gpt-5"}
	if got := modelPickerItemProvider(item); got != "openai" {
		t.Fatalf("got %q, want openai", got)
	}

	// No slash: empty.
	item = tuipick.ModelPickerItem{ID: "gpt5", Display: "GPT 5"}
	if got := modelPickerItemProvider(item); got != "" {
		t.Fatalf("got %q, want empty", got)
	}

	// Empty everything.
	item = tuipick.ModelPickerItem{}
	if got := modelPickerItemProvider(item); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// TestCovModelDiagnosticDisabledReason exercises reason formatting.
func TestCovModelDiagnosticDisabledReason(t *testing.T) {
	// Title only.
	d := appwire.ModelListDiagnostic{Title: "Provider down"}
	if got := modelDiagnosticDisabledReason(d); got != "Provider down" {
		t.Fatalf("got %q, want 'Provider down'", got)
	}

	// Title + message (different).
	d = appwire.ModelListDiagnostic{Title: "Provider down", Message: "Connection refused"}
	if got := modelDiagnosticDisabledReason(d); got != "Provider down: Connection refused" {
		t.Fatalf("got %q, want 'Provider down: Connection refused'", got)
	}

	// Message only (no title).
	d = appwire.ModelListDiagnostic{Message: "Connection refused"}
	if got := modelDiagnosticDisabledReason(d); got != "Connection refused" {
		t.Fatalf("got %q, want 'Connection refused'", got)
	}

	// Message equals title: only title used.
	d = appwire.ModelListDiagnostic{Title: "Same", Message: "Same"}
	if got := modelDiagnosticDisabledReason(d); got != "Same" {
		t.Fatalf("got %q, want 'Same'", got)
	}

	// Nothing: default reason.
	d = appwire.ModelListDiagnostic{}
	if got := modelDiagnosticDisabledReason(d); got != "provider unavailable" {
		t.Fatalf("got %q, want 'provider unavailable'", got)
	}

	// With hint.
	d = appwire.ModelListDiagnostic{Title: "Down", Hint: "check API key"}
	if got := modelDiagnosticDisabledReason(d); got != "Down (check API key)" {
		t.Fatalf("got %q, want 'Down (check API key)'", got)
	}
}

// TestCovModelPickerItemsFromResponse exercises diagnostic and recent enrichment.
func TestCovModelPickerItemsFromResponse(t *testing.T) {
	// With diagnostics.
	resp := appwire.ModelListResponse{
		Data: []appwire.ModelDescriptor{
			{Model: "gpt-5", Provider: "openai"},
		},
		Diagnostics: []appwire.ModelListDiagnostic{
			{Provider: "openai", Title: "Rate limited"},
		},
	}
	items := modelPickerItemsFromResponse(resp, false)
	if len(items) == 0 {
		t.Fatal("should produce items")
	}
	if items[0].DisabledReason != "Rate limited" {
		t.Fatalf("disabled reason = %q, want 'Rate limited'", items[0].DisabledReason)
	}

	// With recent models.
	resp = appwire.ModelListResponse{
		Data: []appwire.ModelDescriptor{
			{Model: "gpt-5", Provider: "openai"},
		},
		Recent: []appwire.ModelDescriptor{
			{Model: "claude-4", Provider: "anthropic"},
		},
	}
	items = modelPickerItemsFromResponse(resp, false)
	if len(items) != 2 {
		t.Fatalf("len = %d, want 2 (recent + data)", len(items))
	}
	if items[0].Group != "Recent" {
		t.Fatalf("first item group = %q, want 'Recent'", items[0].Group)
	}

	// With diagnostics: empty provider (skipped).
	resp = appwire.ModelListResponse{
		Data: []appwire.ModelDescriptor{{Model: "gpt-5", Provider: "openai"}},
		Diagnostics: []appwire.ModelListDiagnostic{
			{Title: "No provider"},
		},
	}
	items = modelPickerItemsFromResponse(resp, false)
	// Diagnostic with empty provider is skipped.
	if len(items) == 0 {
		t.Fatal("should produce items")
	}

	// With raw model ID.
	resp = appwire.ModelListResponse{
		Data: []appwire.ModelDescriptor{{Model: "local-model"}},
	}
	items = modelPickerItemsFromResponse(resp, true)
	if len(items) != 1 || items[0].ID != "local-model" {
		t.Fatalf("items = %+v, want one with ID 'local-model'", items)
	}
}

// TestCovBuildModelPickerItems exercises item building.
func TestCovBuildModelPickerItems(t *testing.T) {
	// Normal models.
	items := buildModelPickerItems([]appwire.ModelDescriptor{
		{Model: "gpt-5", Provider: "openai"},
		{Model: "claude-4", Provider: "anthropic"},
	}, false)
	if len(items) != 2 {
		t.Fatalf("len = %d, want 2", len(items))
	}

	// Skip empty model.
	items = buildModelPickerItems([]appwire.ModelDescriptor{
		{Model: "", Provider: "openai"},
	}, false)
	if len(items) != 0 {
		t.Fatalf("len = %d, want 0 (empty model skipped)", len(items))
	}

	// Skip empty provider (non-raw).
	items = buildModelPickerItems([]appwire.ModelDescriptor{
		{Model: "gpt-5", Provider: ""},
	}, false)
	if len(items) != 0 {
		t.Fatalf("len = %d, want 0 (empty provider skipped)", len(items))
	}

	// Raw model ID: keeps empty-provider models.
	items = buildModelPickerItems([]appwire.ModelDescriptor{
		{Model: "gpt-5", Provider: ""},
	}, true)
	if len(items) != 1 {
		t.Fatalf("len = %d, want 1 (raw mode keeps empty provider)", len(items))
	}
}

// TestCovFormatHubUpgradeResult exercises upgrade result formatting.
func TestCovFormatHubUpgradeResult(t *testing.T) {
	// Channel.
	resp := appwire.UpgradeResponse{Channel: "stable"}
	if got := formatHubUpgradeResult(resp); !contains(got, "stable") {
		t.Fatalf("got %q, want to contain 'stable'", got)
	}

	// No channel: falls to release.
	resp = appwire.UpgradeResponse{Release: "v1.2.3"}
	if got := formatHubUpgradeResult(resp); !contains(got, "v1.2.3") {
		t.Fatalf("got %q, want to contain 'v1.2.3'", got)
	}

	// No channel or release: fallback.
	resp = appwire.UpgradeResponse{}
	if got := formatHubUpgradeResult(resp); !contains(got, "requested channel") {
		t.Fatalf("got %q, want to contain 'requested channel'", got)
	}

	// With all fields.
	resp = appwire.UpgradeResponse{
		Channel:        "stable",
		Archive:        "/tmp/archive.tar.gz",
		ShareBinDir:    "/usr/local/share",
		BinDir:         "/usr/local/bin",
		RestartMessage: "Restart to apply",
	}
	got := formatHubUpgradeResult(resp)
	for _, want := range []string{"stable", "/tmp/archive.tar.gz", "/usr/local/share", "/usr/local/bin", "Restart to apply"} {
		if !contains(got, want) {
			t.Fatalf("got %q, missing %q", got, want)
		}
	}
}

// TestCovFetchHubTasksSync exercises the sync tasks fetch.
func TestCovFetchHubTasksSync(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerTasksList, func(ctx context.Context, p appwire.TaskListParams) (appwire.TaskListResponse, error) {
			return appwire.TaskListResponse{}, nil
		})
	})
	defer cleanup()
	tasks, err := fetchHubTasksSync(context.Background(), client, appwire.Ref{SourceID: "local", ThreadID: "01TEST"})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if tasks != nil {
		t.Fatalf("tasks = %+v, want nil for empty response", tasks)
	}
}

// TestCovRunHubGoal exercises the /goal command dispatch.
func TestCovRunHubGoal(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m := newSessionHubModel(client)

	// /goal status: uses cached snapshot, no cmd.
	m.detail.Goal = &appwire.GoalState{Status: "in_progress", Iterations: 3}
	cmd := m.runHubGoal("status")
	if cmd != nil {
		t.Fatal("status should produce no cmd")
	}

	// /goal status with nil goal: shows hint.
	m.detail.Goal = nil
	cmd = m.runHubGoal("status")
	if cmd != nil {
		t.Fatal("status with nil goal should produce no cmd")
	}

	// /goal clear: sends clear to hub.
	cmd = m.runHubGoal("clear")
	if cmd == nil {
		t.Fatal("clear should produce a cmd")
	}

	// /goal <objective>: sends set to hub.
	cmd = m.runHubGoal("build the feature")
	if cmd == nil {
		t.Fatal("set objective should produce a cmd")
	}

	// /goal with invalid ref.
	m.detail.Ref = "invalid-ref"
	cmd = m.runHubGoal("clear")
	if cmd != nil {
		t.Fatal("clear with invalid ref should produce no cmd")
	}
}

// TestCovSendHubInput exercises the send hub input command builder.
func TestCovSendHubInput(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	cmd := sendHubInput(client, ref, "hello", "draft", nil)
	if cmd == nil {
		t.Fatal("should produce a cmd")
	}
}

// TestCovSendHubAction exercises the action command builder.
func TestCovSendHubAction(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	for _, action := range []string{"interrupt", "compact", "shutdown", "openai/gpt-5"} {
		cmd := sendHubAction(client, ref, action)
		if cmd == nil {
			t.Fatalf("action %q should produce a cmd", action)
		}
	}
}

// TestCovFetchHubStatus exercises status fetch.
func TestCovFetchHubStatus(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerTasksList, func(ctx context.Context, p appwire.TaskListParams) (appwire.TaskListResponse, error) {
			return appwire.TaskListResponse{}, nil
		})
	})
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	cmd := fetchHubStatus(client, ref)
	if cmd == nil {
		t.Fatal("should produce a cmd")
	}
}

// TestCovFetchHubTranscriptTargets exercises target fetch.
func TestCovFetchHubTranscriptTargets(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	cmd := fetchHubTranscriptTargets(client, ref)
	if cmd == nil {
		t.Fatal("should produce a cmd")
	}
}

// TestCovFetchHubModelsForHarness exercises model fetch for harness.
func TestCovFetchHubModelsForHarness(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	cmd := fetchHubModelsForHarness(client, "codex", "/tmp")
	if cmd == nil {
		t.Fatal("should produce a cmd")
	}

	// Empty harness (trimmed).
	cmd = fetchHubModelsForHarness(client, "  ", "/tmp")
	if cmd == nil {
		t.Fatal("should produce a cmd even with empty harness")
	}
}

// TestCovFetchHubSessionModels exercises session model fetch.
func TestCovFetchHubSessionModels(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	cmd := fetchHubSessionModels(client, "/tmp")
	if cmd == nil {
		t.Fatal("should produce a cmd")
	}
}

// TestCovFetchHubSpawnOptions exercises spawn options fetch.
func TestCovFetchHubSpawnOptions(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerProjectsRecent, func(ctx context.Context, p appwire.ProjectsRecentParams) (appwire.ProjectsRecentResponse, error) {
			return appwire.ProjectsRecentResponse{Data: []string{"/tmp/recent"}}, nil
		})
	})
	defer cleanup()
	cmd := fetchHubSpawnOptions(client, "/tmp")
	if cmd == nil {
		t.Fatal("should produce a cmd")
	}
}

// TestCovSubscribeChildActivity exercises the subscription command.
func TestCovSubscribeChildActivity(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	cmd := subscribeChildActivity(client, "local:01CHILD")
	if cmd == nil {
		t.Fatal("should produce a cmd")
	}
}

// TestCovFetchHubSessionRead exercises the session read command builder.
func TestCovFetchHubSessionRead(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	feed := newHubFrameFeed()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	cmd := fetchHubSessionRead(feed, client, ref, "", 0, true, true)
	if cmd == nil {
		t.Fatal("should produce a cmd")
	}

	// Without feed (nil).
	cmd = fetchHubSessionRead(nil, client, ref, "expected", 1, false, false)
	if cmd == nil {
		t.Fatal("should produce a cmd even with nil feed")
	}
}

// TestCovReasoningEffortLevelKnown exercises the level check.
func TestCovReasoningEffortLevelKnown(t *testing.T) {
	levels := []string{"low", "medium", "high"}
	if !reasoningEffortLevelKnown(levels, "high") {
		t.Fatal("should find 'high'")
	}
	if !reasoningEffortLevelKnown(levels, "HIGH") {
		t.Fatal("should find 'HIGH' (case-insensitive)")
	}
	if reasoningEffortLevelKnown(levels, "xhigh") {
		t.Fatal("should not find 'xhigh'")
	}
	if reasoningEffortLevelKnown(nil, "high") {
		t.Fatal("should not find in nil levels")
	}
}

// TestCovHubGoalStatusText exercises goal status rendering.
func TestCovHubGoalStatusText(t *testing.T) {
	// Nil goal.
	if got := hubGoalStatusText(nil); got != "No goal set. Use /goal <objective> to set one." {
		t.Fatalf("got %q", got)
	}

	// With goal.
	goal := &appwire.GoalState{Status: "in_progress", Iterations: 5}
	if got := hubGoalStatusText(goal); !contains(got, "in_progress") || !contains(got, "5") {
		t.Fatalf("got %q, want to contain 'in_progress' and '5'", got)
	}
}

// TestCovIsDatedSnapshotModelID exercises the dated snapshot check.
func TestCovIsDatedSnapshotModelID(t *testing.T) {
	if !isDatedSnapshotModelID("openai/gpt-5-20240101") {
		t.Fatal("should detect dated snapshot")
	}
	if !isDatedSnapshotModelID("openai/gpt-5-20240101-v1") {
		t.Fatal("should detect dated snapshot with version")
	}
	if isDatedSnapshotModelID("openai/gpt-5") {
		t.Fatal("should not detect non-dated model")
	}
}

// TestCovModelInfoMetaTail exercises catalog meta tail rendering.
func TestCovModelInfoMetaTail(t *testing.T) {
	// Nil.
	if got := modelInfoMetaTail(nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// TestCovAppendTextInput exercises input appending.
func TestCovAppendTextInput(t *testing.T) {
	items := appendTextInput("hello", []appwire.InputItem{{Type: "image", Path: "/tmp/x.png"}})
	if len(items) != 2 {
		t.Fatalf("len = %d, want 2", len(items))
	}
}

// TestCovTextInput exercises text input building.
func TestCovTextInput(t *testing.T) {
	// Non-empty text.
	items := textInput("hello")
	if len(items) != 1 || items[0].Text != "hello" {
		t.Fatalf("items = %+v", items)
	}

	// Empty/whitespace text: nil.
	items = textInput("  ")
	if items != nil {
		t.Fatalf("items = %+v, want nil", items)
	}
}

// TestCovSendHubClear exercises clear command builder.
func TestCovSendHubClear(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	cmd := sendHubClear(client, ref)
	if cmd == nil {
		t.Fatal("should produce a cmd")
	}
}

// TestCovSendHubFork exercises fork command builder.
func TestCovSendHubFork(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	cmd := sendHubFork(client, ref, hubForkRequest{EntryIndex: 1, EditedMessage: "edited", Label: "test"})
	if cmd == nil {
		t.Fatal("should produce a cmd")
	}
}

// TestCovSendHubGoalCmd exercises goal command builder.
func TestCovSendHubGoalCmd(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	cmd := sendHubGoal(client, ref, "build it")
	if cmd == nil {
		t.Fatal("should produce a cmd")
	}

	// Empty objective: cleared=true.
	cmd = sendHubGoal(client, ref, "")
	if cmd == nil {
		t.Fatal("should produce a cmd for clear")
	}
}

// TestCovSendHubEffortAction exercises effort command builder.
func TestCovSendHubEffortAction(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	cmd := sendHubEffortAction(client, ref, "high")
	if cmd == nil {
		t.Fatal("should produce a cmd")
	}
}

// TestCovSendHubUpgrade exercises upgrade command builder.
func TestCovSendHubUpgrade(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	cmd := sendHubUpgrade(client, "stable")
	if cmd == nil {
		t.Fatal("should produce a cmd")
	}

	// With whitespace.
	cmd = sendHubUpgrade(client, "  stable  ")
	if cmd == nil {
		t.Fatal("should produce a cmd")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ensure imports are used
var _ = errors.New
var _ tea.Msg = hubTreeMsg{}
