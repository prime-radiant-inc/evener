package tui

import (
	"context"
	"testing"

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
	tests := []struct {
		name string
		resp appwire.UpgradeResponse
		want string
	}{
		{name: "channel", resp: appwire.UpgradeResponse{Channel: "stable"}, want: "Evener upgraded to stable."},
		{name: "release fallback", resp: appwire.UpgradeResponse{Release: "v1.2.3"}, want: "Evener upgraded to v1.2.3."},
		{name: "requested fallback", resp: appwire.UpgradeResponse{}, want: "Evener upgraded to requested channel."},
		{
			name: "all installation details",
			resp: appwire.UpgradeResponse{
				Channel:        "stable",
				Archive:        "/tmp/archive.tar.gz",
				ShareBinDir:    "/usr/local/share",
				BinDir:         "/usr/local/bin",
				RestartMessage: "Restart to apply",
			},
			want: "Evener upgraded to stable.\nArchive: /tmp/archive.tar.gz\nInstalled: /usr/local/share\nSymlinks: /usr/local/bin\nRestart to apply",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatHubUpgradeResult(tc.resp); got != tc.want {
				t.Fatalf("formatHubUpgradeResult() = %q, want %q", got, tc.want)
			}
		})
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
	var goals []appwire.GoalSetParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodGoalSet, func(_ context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
			goals = append(goals, params)
			return appwire.GoalSetResponse{Started: params.Objective != ""}, nil
		})
	})
	defer cleanup()
	m := newSessionHubModel(client)

	// /goal status: uses cached snapshot, no cmd.
	m.detail.Goal = &appwire.GoalState{Status: "in_progress", Iterations: 3}
	cmd := m.runHubGoal("status")
	if cmd != nil {
		t.Fatal("status should produce no cmd")
	}
	if got := m.session.messages[len(m.session.messages)-1].Text; got != "Goal: in_progress 3" {
		t.Fatalf("status message = %q, want cached goal status", got)
	}

	// /goal status with nil goal: shows hint.
	m.detail.Goal = nil
	cmd = m.runHubGoal("status")
	if cmd != nil {
		t.Fatal("status with nil goal should produce no cmd")
	}
	if got := m.session.messages[len(m.session.messages)-1].Text; got != "No goal set. Use /goal <objective> to set one." {
		t.Fatalf("nil-goal status message = %q", got)
	}

	// /goal clear: sends clear to hub.
	cmd = m.runHubGoal("clear")
	if cmd == nil {
		t.Fatal("clear should produce a cmd")
	}
	clearMsg, ok := cmd().(hubGoalMsg)
	if !ok || clearMsg.err != nil || !clearMsg.cleared || clearMsg.started {
		t.Fatalf("clear result = %#v, want successful cleared goal", clearMsg)
	}

	// /goal <objective>: sends set to hub.
	cmd = m.runHubGoal("build the feature")
	if cmd == nil {
		t.Fatal("set objective should produce a cmd")
	}
	setMsg, ok := cmd().(hubGoalMsg)
	if !ok || setMsg.err != nil || setMsg.cleared || !setMsg.started {
		t.Fatalf("set result = %#v, want successful started goal", setMsg)
	}
	if len(goals) != 2 || goals[0].Objective != "" || goals[1].Objective != "build the feature" {
		t.Fatalf("goal calls = %#v, want clear then objective", goals)
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
	var got appwire.TurnStartParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			got = params
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn-7"}}, nil
		})
	})
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	cmd := sendHubInput(client, ref, "hello", "draft", nil)
	msg, ok := cmd().(hubSendMsg)
	if !ok || msg.err != nil || msg.turnID != "turn-7" || msg.ref != ref.String() || msg.text != "hello" || msg.draft != "draft" {
		t.Fatalf("send result = %#v", msg)
	}
	if got.Ref != ref.String() || len(got.Input) != 1 || got.Input[0].Type != "text" || got.Input[0].Text != "hello" || got.ClientMutationID == "" {
		t.Fatalf("turn/start params = %#v", got)
	}
}

// TestCovSendHubAction exercises the action command builder.
func TestCovSendHubAction(t *testing.T) {
	var calls []string
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnInterrupt, func(_ context.Context, params appwire.TurnInterruptParams) (appwire.EmptyResponse, error) {
			calls = append(calls, "interrupt:"+params.Ref)
			if params.ClientMutationID == "" {
				t.Error("interrupt client mutation ID is empty")
			}
			return appwire.EmptyResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadCompactStart, func(_ context.Context, params appwire.ThreadCompactStartParams) (appwire.EmptyResponse, error) {
			calls = append(calls, "compact:"+params.Ref)
			return appwire.EmptyResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadShutdown, func(_ context.Context, params appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
			calls = append(calls, "shutdown:"+params.Ref)
			return appwire.EmptyResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadModelSet, func(_ context.Context, params appwire.ThreadModelSetParams) (appwire.EmptyResponse, error) {
			calls = append(calls, "model:"+params.Ref+":"+params.ModelProvider+"/"+params.Model)
			return appwire.EmptyResponse{}, nil
		})
	})
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	for _, action := range []string{"interrupt", "compact", "shutdown", "openai/gpt-5"} {
		cmd := sendHubAction(client, ref, action)
		msg, ok := cmd().(hubActionMsg)
		if !ok || msg.err != nil {
			t.Fatalf("action %q result = %#v", action, msg)
		}
	}
	want := []string{"interrupt:local:01TEST", "compact:local:01TEST", "shutdown:local:01TEST", "model:local:01TEST:openai/gpt-5"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls = %#v, want %#v", calls, want)
		}
	}
}

// TestCovFetchHubStatus exercises status fetch.
func TestCovFetchHubStatus(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			if params.Ref != "local:01TEST" || !params.IncludeTurns || params.ItemsView != "full" {
				t.Errorf("thread/read params = %#v", params)
			}
			return appwire.ThreadReadResponse{Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: params.Ref}}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerTasksList, func(ctx context.Context, p appwire.TaskListParams) (appwire.TaskListResponse, error) {
			return appwire.TaskListResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerAuthStatus, func(context.Context, appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
			return appwire.AuthStatusResponse{Provider: "openai", Supported: true}, nil
		})
	})
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	msg, ok := fetchHubStatus(client, ref)().(hubStatusMsg)
	if !ok || msg.err != nil || msg.taskErr != nil || msg.authErr != nil || !msg.auth.Supported {
		t.Fatalf("status result = %#v", msg)
	}
}

// TestCovFetchHubTranscriptTargets exercises target fetch.
func TestCovFetchHubTranscriptTargets(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerThreadTranscriptsList, func(_ context.Context, params appwire.ThreadTranscriptListParams) (appwire.ThreadTranscriptListResponse, error) {
			if params.Ref != "local:01TEST" {
				t.Errorf("ref = %q, want local:01TEST", params.Ref)
			}
			return appwire.ThreadTranscriptListResponse{Data: []appwire.ThreadTranscriptTarget{{Ref: "local:01CHILD"}}}, nil
		})
	})
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	msg, ok := fetchHubTranscriptTargets(client, ref)().(hubTranscriptTargetsMsg)
	if !ok || msg.err != nil || len(msg.targets) != 1 || msg.targets[0].Ref != "local:01CHILD" {
		t.Fatalf("transcript targets result = %#v", msg)
	}
}

// TestCovFetchHubModelsForHarness exercises model fetch for harness.
func TestCovFetchHubModelsForHarness(t *testing.T) {
	var calls []appwire.ModelListParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(_ context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
			calls = append(calls, params)
			return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}, nil
		})
	})
	defer cleanup()
	msg, ok := fetchHubModelsForHarness(client, "codex", "/tmp")().(hubModelsMsg)
	if !ok || msg.err != nil || msg.harness != "codex" || len(msg.models) != 1 || msg.models[0].ID != "gpt-5" {
		t.Fatalf("harness model result = %#v", msg)
	}

	// Empty harness (trimmed).
	msg, ok = fetchHubModelsForHarness(client, "  ", "/tmp")().(hubModelsMsg)
	if !ok || msg.err != nil || msg.harness != "" || len(msg.models) != 1 || msg.models[0].ID != "openai/gpt-5" {
		t.Fatalf("default model result = %#v", msg)
	}
	if len(calls) != 2 || calls[0].Harness != "codex" || calls[0].CWD != "/tmp" || calls[1].Harness != "" || calls[1].CWD != "/tmp" {
		t.Fatalf("model/list calls = %#v", calls)
	}
}

// TestCovFetchHubSessionModels exercises session model fetch.
func TestCovFetchHubSessionModels(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(_ context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
			if params.CWD != "/tmp/work" || params.Harness != "" {
				t.Errorf("model/list params = %#v", params)
			}
			return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}, nil
		})
	})
	defer cleanup()
	msg, ok := fetchHubSessionModels(client, " /tmp/work ")().(hubSessionModelsMsg)
	if !ok || msg.err != nil || len(msg.models) != 1 || msg.models[0].ID != "openai/gpt-5" {
		t.Fatalf("session models result = %#v", msg)
	}
}

// TestCovFetchHubSpawnOptions exercises spawn options fetch.
func TestCovFetchHubSpawnOptions(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerProjectsRecent, func(ctx context.Context, p appwire.ProjectsRecentParams) (appwire.ProjectsRecentResponse, error) {
			return appwire.ProjectsRecentResponse{Data: []string{"/tmp/recent"}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodModelList, func(_ context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
			if params.CWD != "/tmp" {
				t.Errorf("model/list cwd = %q, want /tmp", params.CWD)
			}
			return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}}, nil
		})
	})
	defer cleanup()
	msg, ok := fetchHubSpawnOptions(client, "/tmp")().(hubSpawnOptionsMsg)
	if !ok || msg.err != nil || msg.modelErr != nil || len(msg.harnesses) != 1 || msg.harnesses[0] != "evener" || msg.harnessKinds["evener"] != "evener" || len(msg.models) != 1 || len(msg.recentDirs) != 1 || msg.recentDirs[0] != "/tmp/recent" {
		t.Fatalf("spawn options result = %#v", msg)
	}
}

// TestCovSubscribeChildActivity exercises the subscription command.
func TestCovSubscribeChildActivity(t *testing.T) {
	called := false
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			called = true
			if params.Ref != "local:01CHILD" || params.IncludeTurns || !params.Subscribe || params.ReplaceSubscription {
				t.Errorf("child subscription params = %#v", params)
			}
			return appwire.ThreadReadResponse{}, nil
		})
	})
	defer cleanup()
	if msg := subscribeChildActivity(client, "local:01CHILD")(); msg != nil {
		t.Fatalf("fire-and-forget result = %#v, want nil", msg)
	}
	if !called {
		t.Fatal("thread/read subscription was not called")
	}
}

// TestCovFetchHubSessionRead exercises the session read command builder.
func TestCovFetchHubSessionRead(t *testing.T) {
	var calls []appwire.ThreadReadParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			calls = append(calls, params)
			return appwire.ThreadReadResponse{Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: params.Ref}}}, nil
		})
	})
	defer cleanup()
	feed := newHubFrameFeed()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	msg, ok := fetchHubSessionRead(feed, client, ref, "", 0, true, true)().(hubSessionMsg)
	if !ok || msg.err != nil || msg.ref != ref.String() || msg.capture == nil {
		t.Fatalf("captured session read result = %#v", msg)
	}
	msg.capture.Release()

	// Without feed (nil).
	msg, ok = fetchHubSessionRead(nil, client, ref, "expected", 1, false, false)().(hubSessionMsg)
	if !ok || msg.err != nil || msg.expectedState != "expected" || msg.expectedRefreshToken != 1 || msg.capture != nil {
		t.Fatalf("uncaptured session read result = %#v", msg)
	}
	if len(calls) != 2 || !calls[0].Subscribe || !calls[0].ReplaceSubscription || calls[1].Subscribe || calls[1].ReplaceSubscription {
		t.Fatalf("thread/read calls = %#v", calls)
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
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadClear, func(_ context.Context, params appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
			if params.Ref != "local:01TEST" {
				t.Errorf("ref = %q", params.Ref)
			}
			return appwire.ThreadClearResponse{Ref: "local:01CLEARED"}, nil
		})
	})
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	msg, ok := sendHubClear(client, ref)().(hubClearMsg)
	if !ok || msg.err != nil || msg.resp.Ref != "local:01CLEARED" {
		t.Fatalf("clear result = %#v", msg)
	}
}

// TestCovSendHubFork exercises fork command builder.
func TestCovSendHubFork(t *testing.T) {
	var got appwire.ThreadForkParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadFork, func(_ context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
			got = params
			return appwire.ThreadForkResponse{Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:01FORK"}}}, nil
		})
	})
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	msg, ok := sendHubFork(client, ref, hubForkRequest{EntryIndex: 7, EditedMessage: "edited", Label: "test"})().(hubForkMsg)
	if !ok || msg.err != nil || msg.resp.Ref != "local:01FORK" || msg.aside {
		t.Fatalf("fork result = %#v", msg)
	}
	if got.Ref != ref.String() || got.SourceTurnID != "7" || got.EditedInput != "edited" || got.Label != "test" {
		t.Fatalf("thread/fork params = %#v", got)
	}
}

// TestCovSendHubGoalCmd exercises goal command builder.
func TestCovSendHubGoalCmd(t *testing.T) {
	var got []appwire.GoalSetParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodGoalSet, func(_ context.Context, params appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
			got = append(got, params)
			return appwire.GoalSetResponse{Started: params.Objective != ""}, nil
		})
	})
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	set, ok := sendHubGoal(client, ref, "build it")().(hubGoalMsg)
	if !ok || set.err != nil || set.cleared || !set.started {
		t.Fatalf("set result = %#v", set)
	}

	// Empty objective: cleared=true.
	clear, ok := sendHubGoal(client, ref, "")().(hubGoalMsg)
	if !ok || clear.err != nil || !clear.cleared || clear.started {
		t.Fatalf("clear result = %#v", clear)
	}
	if len(got) != 2 || got[0].Ref != ref.String() || got[0].Objective != "build it" || got[1].Objective != "" {
		t.Fatalf("goal/set params = %#v", got)
	}
}

// TestCovSendHubEffortAction exercises effort command builder.
func TestCovSendHubEffortAction(t *testing.T) {
	var got appwire.ThreadReasoningEffortSetParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadReasoningEffortSet, func(_ context.Context, params appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
			got = params
			return appwire.EmptyResponse{}, nil
		})
	})
	defer cleanup()
	ref := appwire.Ref{SourceID: "local", ThreadID: "01TEST"}
	msg, ok := sendHubEffortAction(client, ref, "high")().(hubActionMsg)
	if !ok || msg.err != nil || msg.action != "effort" || got.Ref != ref.String() || got.ReasoningEffort != "high" {
		t.Fatalf("effort result = %#v, params = %#v", msg, got)
	}
}

// TestCovSendHubUpgrade exercises upgrade command builder.
func TestCovSendHubUpgrade(t *testing.T) {
	var requested []string
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerUpgrade, func(_ context.Context, params appwire.UpgradeParams) (appwire.UpgradeResponse, error) {
			requested = append(requested, params.Requested)
			return appwire.UpgradeResponse{Channel: params.Requested}, nil
		})
	})
	defer cleanup()
	msg, ok := sendHubUpgrade(client, "stable")().(hubUpgradeMsg)
	if !ok || msg.err != nil || msg.resp.Channel != "stable" {
		t.Fatalf("upgrade result = %#v", msg)
	}

	// With whitespace.
	msg, ok = sendHubUpgrade(client, "  snapshot  ")().(hubUpgradeMsg)
	if !ok || msg.err != nil || msg.resp.Channel != "snapshot" {
		t.Fatalf("trimmed upgrade result = %#v", msg)
	}
	if len(requested) != 2 || requested[0] != "stable" || requested[1] != "snapshot" {
		t.Fatalf("upgrade requests = %#v", requested)
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
