package tui

import (
	"context"
	"encoding/json"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	pendingpkg "primeradiant.com/evener/cmd/evener-tui/internal/pending"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
	"primeradiant.com/evener/internal/appserver"
)

// --- hub_notifications.go ---

// TestCovMarkPendingFailedByID exercises marking a pending message as failed.
func TestCovMarkPendingFailedByID(t *testing.T) {
	m := hubModel{session: newModel(nil)}
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "hello", PendingID: 42, Pending: true},
	}
	m.markPendingFailedByID(42, "network error")
	if !m.session.messages[0].Failed {
		t.Fatal("should mark as failed")
	}
	if m.session.messages[0].Pending {
		t.Fatal("should clear pending")
	}
	if m.session.messages[0].Reason != "network error" {
		t.Fatalf("reason = %q, want 'network error'", m.session.messages[0].Reason)
	}

	// Non-matching ID: no-op.
	m = hubModel{session: newModel(nil)}
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "hello", PendingID: 1, Pending: true},
	}
	m.markPendingFailedByID(99, "error")
	if m.session.messages[0].Failed {
		t.Fatal("should not mark non-matching ID")
	}
}

// TestCovRemovePendingByID exercises removing a pending message.
func TestCovRemovePendingByID(t *testing.T) {
	m := hubModel{session: newModel(nil)}
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "first", PendingID: 1},
		{Kind: transcript.MsgUser, Text: "second", PendingID: 2},
	}
	m.removePendingByID(1)
	if len(m.session.messages) != 1 {
		t.Fatalf("len = %d, want 1", len(m.session.messages))
	}
	if m.session.messages[0].Text != "second" {
		t.Fatalf("text = %q, want 'second'", m.session.messages[0].Text)
	}

	// Non-matching ID: no-op.
	m.removePendingByID(99)
	if len(m.session.messages) != 1 {
		t.Fatalf("len = %d, want 1 (non-matching)", len(m.session.messages))
	}
}

// TestCovRefreshPluginsPanel exercises plugin panel refresh.
func TestCovRefreshPluginsPanel(t *testing.T) {
	var calls []string
	var browseParams appwire.MarketplaceBrowseParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceList, func(context.Context, appwire.EmptyParams) (appwire.MarketplaceListResponse, error) {
			calls = append(calls, appwire.MethodEvenerMarketplaceList)
			return appwire.MarketplaceListResponse{Marketplaces: []appwire.MarketplaceEntry{{Name: "official"}}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerPluginList, func(context.Context, appwire.EmptyParams) (appwire.PluginListResponse, error) {
			calls = append(calls, appwire.MethodEvenerPluginList)
			return appwire.PluginListResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerMarketplaceBrowse, func(_ context.Context, params appwire.MarketplaceBrowseParams) (appwire.MarketplaceBrowseResponse, error) {
			calls = append(calls, appwire.MethodEvenerMarketplaceBrowse)
			browseParams = params
			return appwire.MarketplaceBrowseResponse{
				Name:        "official",
				Description: "response-only catalog",
				Plugins:     []appwire.MarketplaceCatalogPlugin{{Name: "sentinel-plugin"}},
			}, nil
		})
	})
	defer cleanup()
	panel := launchconfig.NewPluginsPanel()
	updated, _ := panel.Update(launchconfig.MarketplaceListResultMsg{List: appwire.MarketplaceListResponse{
		Marketplaces: []appwire.MarketplaceEntry{{Name: "official"}},
	}})
	panel = updated.(launchconfig.PluginsPanel)
	updated, _ = panel.Update(tea.KeyMsg{Type: tea.KeyRight})
	panel = updated.(launchconfig.PluginsPanel)
	updated, openBrowse := panel.Update(tea.KeyMsg{Type: tea.KeyEnter})
	panel = updated.(launchconfig.PluginsPanel)
	if panel.BrowseMarketplace() != "official" {
		t.Fatalf("open marketplace = %q, want official", panel.BrowseMarketplace())
	}
	if openBrowse == nil {
		t.Fatal("opening marketplace did not return browse request command")
	}
	request, ok := openBrowse().(launchconfig.MarketplaceBrowseRequestMsg)
	if !ok || request.Name != "official" {
		t.Fatalf("open-browse request = %#v", request)
	}
	m := newHubModel(client, "http://hub.test")
	m.pluginsPanel = &panel
	batch, ok := m.refreshPluginsPanel()().(tea.BatchMsg)
	if !ok || len(batch) != 3 {
		t.Fatalf("refresh result = %#v, want three-command batch", batch)
	}
	marketplaceSeen, pluginSeen, browseSeen := false, false, false
	for _, command := range batch {
		message := command()
		switch result := message.(type) {
		case launchconfig.MarketplaceListResultMsg:
			if result.Err != nil || len(result.List.Marketplaces) != 1 || result.List.Marketplaces[0].Name != "official" {
				t.Fatalf("marketplace refresh result = %#v", result)
			}
			marketplaceSeen = true
		case launchconfig.PluginListResultMsg:
			if result.Err != nil {
				t.Fatalf("plugin refresh result = %#v", result)
			}
			pluginSeen = true
		case launchconfig.MarketplaceBrowseResultMsg:
			if result.Err != nil || result.Name != "official" || result.Response.Description != "response-only catalog" || len(result.Response.Plugins) != 1 || result.Response.Plugins[0].Name != "sentinel-plugin" {
				t.Fatalf("browse refresh result = %#v", result)
			}
			browseSeen = true
		default:
			t.Fatalf("refresh command returned %T", message)
		}
	}
	if !marketplaceSeen || !pluginSeen || !browseSeen {
		t.Fatalf("refresh results seen: marketplace=%v plugin=%v browse=%v", marketplaceSeen, pluginSeen, browseSeen)
	}
	if len(calls) != 3 || calls[0] != appwire.MethodEvenerMarketplaceList || calls[1] != appwire.MethodEvenerPluginList || calls[2] != appwire.MethodEvenerMarketplaceBrowse {
		t.Fatalf("refresh calls = %#v", calls)
	}
	if browseParams.Name != "official" {
		t.Fatalf("browse params = %#v, want official", browseParams)
	}
}

// TestCovApplyQueueState exercises queue state application.
func TestCovApplyQueueState(t *testing.T) {
	m := hubModel{session: newModel(nil)}

	// Empty ref: no-op.
	m.applyQueueState("", appwire.QueueState{})
	if m.sessionQueue != nil {
		t.Fatal("should not set queue for empty ref")
	}

	// Zero depth: clears queue.
	m.applyQueueState("local:01TEST", appwire.QueueState{})
	if m.sessionQueue != nil {
		t.Fatal("should clear queue for zero depth")
	}

	// With entries.
	m.applyQueueState("local:01TEST", appwire.QueueState{
		Depth:   2,
		Preview: []string{"msg1", "msg2"},
	})
	if len(m.sessionQueue) != 2 {
		t.Fatalf("queue len = %d, want 2", len(m.sessionQueue))
	}
	if m.sessionQueueRef != "local:01TEST" {
		t.Fatalf("ref = %q, want 'local:01TEST'", m.sessionQueueRef)
	}
}

// TestCovUpdateDashboardRowModel exercises row model update.
func TestCovUpdateDashboardRowModel(t *testing.T) {
	m := hubModel{
		rows: []hubRow{
			{ref: appwire.Ref{SourceID: "local", ThreadID: "01TEST"}, model: "old"},
		},
	}
	m.updateDashboardRowModel("local:01TEST", "new-model")
	if m.rows[0].model != "new-model" {
		t.Fatalf("model = %q, want 'new-model'", m.rows[0].model)
	}

	// Empty ref: no-op.
	m.updateDashboardRowModel("", "new")
	// Empty model: no-op.
	m.updateDashboardRowModel("local:01TEST", "")
	// Non-matching ref: no-op.
	m.updateDashboardRowModel("local:other", "x")
}

// TestCovNotificationMatchesCurrentSession exercises session matching.
func TestCovNotificationMatchesCurrentSession(t *testing.T) {
	m := hubModel{detail: hubSessionDetail{Ref: "local:01TEST", SessionID: "01TEST"}}

	// Matching ref.
	n := appwire.Notification{Params: mustJSON(appwire.NotificationRef{Ref: "local:01TEST"})}
	if !m.notificationMatchesCurrentSession(n) {
		t.Fatal("should match current session by ref")
	}

	// Non-matching ref.
	n = appwire.Notification{Params: mustJSON(appwire.NotificationRef{Ref: "local:other"})}
	if m.notificationMatchesCurrentSession(n) {
		t.Fatal("should not match other session")
	}

	// Matching by ThreadID.
	n = appwire.Notification{Params: mustJSON(appwire.NotificationRef{ThreadID: "01TEST"})}
	if !m.notificationMatchesCurrentSession(n) {
		t.Fatal("should match by ThreadID")
	}

	// No ref, no ThreadID: defaults to true.
	n = appwire.Notification{Params: mustJSON(appwire.NotificationRef{})}
	if !m.notificationMatchesCurrentSession(n) {
		t.Fatal("should default to true for no ref")
	}

	// Invalid JSON: defaults to true.
	n = appwire.Notification{Params: json.RawMessage(`invalid`)}
	if !m.notificationMatchesCurrentSession(n) {
		t.Fatal("should default to true for invalid JSON")
	}
}

// TestCovHandleChildActivityFrame exercises child activity routing.
func TestCovHandleChildActivityFrame(t *testing.T) {
	// No watched children.
	m := hubModel{}
	_, handled := m.handleChildActivityFrame(appwire.Notification{Method: appwire.NotifyItemStarted})
	if handled {
		t.Fatal("should not handle when no watched children")
	}

	// Wrong method.
	m = hubModel{watchedChildRefs: map[string]bool{"local:child": true}}
	_, handled = m.handleChildActivityFrame(appwire.Notification{Method: appwire.NotifyTurnCompleted})
	if handled {
		t.Fatal("should not handle wrong method")
	}

	// Matching child ref, item started.
	m = hubModel{
		watchedChildRefs: map[string]bool{"local:child": true},
		session:          newModel(nil),
		detail:           hubSessionDetail{Ref: "local:parent"},
	}
	n := appwire.Notification{
		Method: appwire.NotifyItemStarted,
		Params: mustJSON(appwire.ItemLifecycleParams{
			Ref: "local:child",
			Item: appwire.ThreadItem{
				ToolName:    "read_file",
				Description: "reading file",
			},
		}),
	}
	_, handled = m.handleChildActivityFrame(n)
	if !handled {
		t.Fatal("should handle matching child activity")
	}

	// Matching child ref but invalid JSON params for ref: not handled.
	n = appwire.Notification{
		Method: appwire.NotifyItemStarted,
		Params: json.RawMessage(`invalid`),
	}
	_, handled = m.handleChildActivityFrame(n)
	if handled {
		t.Fatal("should not handle invalid JSON for ref parse")
	}

	// Matching ref, valid ref JSON but invalid item params: handled=true.
	// Use a valid ref but make ItemLifecycleParams unmarshal fail by using wrong types.
	n = appwire.Notification{
		Method: appwire.NotifyItemStarted,
		Params: mustJSON(map[string]any{
			"ref":  "local:child",
			"item": "not-an-object",
		}),
	}
	_, handled = m.handleChildActivityFrame(n)
	if !handled {
		t.Fatal("should still handle (return true) for matching ref even when item params fail")
	}
}

// TestCovChildActivityFromItem exercises activity extraction.
func TestCovChildActivityFromItem(t *testing.T) {
	// Tool name with detail.
	item := appwire.ThreadItem{ToolName: "read_file", Description: "reading /tmp"}
	if got := childActivityFromItem(item); got != "read_file: reading /tmp" {
		t.Fatalf("got %q, want 'read_file: reading /tmp'", got)
	}

	// Tool name only.
	item = appwire.ThreadItem{ToolName: "write_file"}
	if got := childActivityFromItem(item); got != "write_file" {
		t.Fatalf("got %q, want 'write_file'", got)
	}

	// Agent message.
	item = appwire.ThreadItem{Type: "agentMessage"}
	if got := childActivityFromItem(item); got != "responding" {
		t.Fatalf("got %q, want 'responding'", got)
	}

	// Text fallback.
	item = appwire.ThreadItem{Text: "some text"}
	if got := childActivityFromItem(item); got != "some text" {
		t.Fatalf("got %q, want 'some text'", got)
	}

	// Status fallback.
	item = appwire.ThreadItem{Status: "running"}
	if got := childActivityFromItem(item); got != "running" {
		t.Fatalf("got %q, want 'running'", got)
	}
}

// TestCovRunStillRunning exercises the running check.
func TestCovRunStillRunning(t *testing.T) {
	for _, status := range []string{"completed", "done", "failed", "cancelled", "stopped", "succeeded", "exhausted"} {
		if runStillRunning(status) {
			t.Fatalf("runStillRunning(%q) = true, want false", status)
		}
	}
	for _, status := range []string{"running", "idle", "", "pending"} {
		if !runStillRunning(status) {
			t.Fatalf("runStillRunning(%q) = false, want true", status)
		}
	}
}

// TestCovSubscribeNewChildren exercises child subscription.
func TestCovSubscribeNewChildren(t *testing.T) {
	// No client: no-op.
	m := hubModel{session: newModel(nil)}
	if cmd := m.subscribeNewChildren(); cmd != nil {
		t.Fatal("should return nil cmd without client")
	}

	// With client but no subagent messages.
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	if cmd := m.subscribeNewChildren(); cmd != nil {
		t.Fatal("should return nil cmd with no subagent messages")
	}
}

// TestCovReconcilePendingFromNotification exercises pending reconciliation.
func TestCovReconcilePendingFromNotification(t *testing.T) {
	pending := pendingpkg.NewPendingCoordinator(pendingpkg.RealClock{}, func(tea.Msg) {})

	// Steering injected.
	pending.Register(appwire.MethodTurnDrainAsSteer, "queued steering", "local:01TEST")
	n := appwire.Notification{
		Method: appwire.NotifyEvenerSteeringInjected,
		Params: mustJSON(appwire.EvenerSteeringInjectedParams{Ref: "local:01TEST", Text: "steer text"}),
	}
	reconcilePendingFromNotification(pending, n)
	if pending.TryReconcile(appwire.MethodTurnDrainAsSteer, "anything", "local:01TEST") {
		t.Fatal("steering notification left its pending mutation registered")
	}

	// Item started (userMessage).
	pending.Register(appwire.MethodTurnStart, "hello", "local:01TEST")
	n = appwire.Notification{
		Method: appwire.NotifyItemStarted,
		Params: mustJSON(appwire.ItemLifecycleParams{
			Ref:  "local:01TEST",
			Item: appwire.ThreadItem{Type: "userMessage", Text: "hello"},
		}),
	}
	reconcilePendingFromNotification(pending, n)
	if pending.TryReconcile(appwire.MethodTurnStart, "hello", "local:01TEST") {
		t.Fatal("item-started notification left its pending turn registered")
	}

	// Turn completed.
	pending.Register(appwire.MethodTurnStart, "completed input", "local:01TEST")
	n = appwire.Notification{
		Method: appwire.NotifyTurnCompleted,
		Params: mustJSON(appwire.TurnCompletedParams{
			Ref: "local:01TEST",
			Turn: appwire.Turn{
				Items: []appwire.ThreadItem{
					{Type: "userMessage", Text: "completed input"},
				},
			},
		}),
	}
	reconcilePendingFromNotification(pending, n)
	if pending.TryReconcile(appwire.MethodTurnStart, "completed input", "local:01TEST") {
		t.Fatal("turn-completed notification left its pending turn registered")
	}

	// Invalid JSON must not reconcile an unrelated pending mutation.
	pending.Register(appwire.MethodTurnStart, "still pending", "local:01TEST")
	n = appwire.Notification{
		Method: appwire.NotifyTurnCompleted,
		Params: json.RawMessage(`invalid`),
	}
	reconcilePendingFromNotification(pending, n)
	if !pending.TryReconcile(appwire.MethodTurnStart, "still pending", "local:01TEST") {
		t.Fatal("invalid notification unexpectedly removed pending mutation")
	}
}

// TestCovNotificationPendingRef exercises ref extraction.
func TestCovNotificationPendingRef(t *testing.T) {
	// From Ref.
	n := appwire.Notification{Params: mustJSON(appwire.NotificationRef{Ref: "local:01TEST"})}
	if got := notificationPendingRef(n); got != "local:01TEST" {
		t.Fatalf("got %q, want 'local:01TEST'", got)
	}

	// From ThreadID.
	n = appwire.Notification{Params: mustJSON(appwire.NotificationRef{ThreadID: "01TEST"})}
	if got := notificationPendingRef(n); got != "01TEST" {
		t.Fatalf("got %q, want '01TEST'", got)
	}

	// Invalid JSON.
	n = appwire.Notification{Params: json.RawMessage(`invalid`)}
	if got := notificationPendingRef(n); got != "" {
		t.Fatalf("got %q, want empty for invalid JSON", got)
	}
}

// TestCovApplySandboxEscalation exercises escalation enqueue.
func TestCovApplySandboxEscalation(t *testing.T) {
	// Empty ref: no-op.
	m := hubModel{}
	m.applySandboxEscalation(appwire.SandboxEscalationRequested{EscalationID: "esc1"}, "")
	if len(m.escalationsByRef) != 0 {
		t.Fatal("should not enqueue for empty ref")
	}

	// Valid ref, not viewed.
	m = hubModel{detail: hubSessionDetail{Ref: "local:other"}}
	m.applySandboxEscalation(appwire.SandboxEscalationRequested{
		EscalationID: "esc1", Tool: "write_file", DeniedPath: "/tmp/secret", Mode: "workspace-write",
	}, "local:01TEST")
	if len(m.escalationsByRef["local:01TEST"]) != 1 {
		t.Fatal("should enqueue for valid ref")
	}

	// Valid ref, viewed, first escalation: prompts.
	m = hubModel{detail: hubSessionDetail{Ref: "local:01TEST"}, session: newModel(nil)}
	m.applySandboxEscalation(appwire.SandboxEscalationRequested{
		EscalationID: "esc1", Tool: "write_file", DeniedPath: "/tmp/secret", Mode: "workspace-write",
	}, "local:01TEST")
	if len(m.escalationsByRef["local:01TEST"]) != 1 {
		t.Fatal("should enqueue for viewed session")
	}

	// Second escalation for viewed session: queues with count message.
	m.applySandboxEscalation(appwire.SandboxEscalationRequested{
		EscalationID: "esc2", Tool: "read_file", DeniedPath: "/tmp/other", Mode: "read-only",
	}, "local:01TEST")
	if len(m.escalationsByRef["local:01TEST"]) != 2 {
		t.Fatal("should enqueue second escalation")
	}
}

// TestCovResolveHeadEscalation exercises escalation resolution.
func TestCovResolveHeadEscalation(t *testing.T) {
	// No escalation.
	m := hubModel{}
	if cmd := m.resolveHeadEscalation(true); cmd != nil {
		t.Fatal("should return nil for no escalation")
	}

	// With escalation, no client.
	m = hubModel{
		detail:  hubSessionDetail{Ref: "local:01TEST"},
		session: newModel(nil),
		escalationsByRef: map[string][]*hubEscalation{
			"local:01TEST": {{id: "esc1", ref: "local:01TEST"}},
		},
	}
	cmd := m.resolveHeadEscalation(true)
	if cmd != nil {
		t.Fatal("should return nil for no client")
	}
	if !m.escalationsByRef["local:01TEST"][0].resolving {
		t.Fatal("should mark as resolving")
	}

	// Already resolving: no-op.
	cmd = m.resolveHeadEscalation(false)
	if cmd != nil {
		t.Fatal("should return nil when already resolving")
	}
}

// TestCovHandleEscalationResolved exercises the ACK handler.
func TestCovHandleEscalationResolved(t *testing.T) {
	// Success: pop.
	m := hubModel{
		detail:  hubSessionDetail{Ref: "local:01TEST"},
		session: newModel(nil),
		escalationsByRef: map[string][]*hubEscalation{
			"local:01TEST": {{id: "esc1", ref: "local:01TEST"}},
		},
	}
	m.handleEscalationResolved(hubEscalationResolvedMsg{ref: "local:01TEST", id: "esc1", approve: true})
	if len(m.escalationsByRef["local:01TEST"]) != 0 {
		t.Fatal("should pop on success")
	}

	// Not found: no-op.
	m = hubModel{escalationsByRef: map[string][]*hubEscalation{
		"local:01TEST": {{id: "esc1"}},
	}}
	m.handleEscalationResolved(hubEscalationResolvedMsg{ref: "local:01TEST", id: "unknown"})
	if len(m.escalationsByRef["local:01TEST"]) != 1 {
		t.Fatal("should not pop unknown id")
	}
}

// TestCovRemoveEscalationAt exercises removal.
func TestCovRemoveEscalationAt(t *testing.T) {
	m := hubModel{
		escalationsByRef: map[string][]*hubEscalation{
			"local:01TEST": {{id: "esc1"}, {id: "esc2"}, {id: "esc3"}},
		},
	}

	// Remove middle.
	m.removeEscalationAt("local:01TEST", 1)
	if len(m.escalationsByRef["local:01TEST"]) != 2 {
		t.Fatalf("len = %d, want 2", len(m.escalationsByRef["local:01TEST"]))
	}
	if m.escalationsByRef["local:01TEST"][1].id != "esc3" {
		t.Fatalf("last id = %q, want esc3", m.escalationsByRef["local:01TEST"][1].id)
	}

	// Remove last: deletes map entry.
	m.removeEscalationAt("local:01TEST", 0)
	m.removeEscalationAt("local:01TEST", 0)
	if _, ok := m.escalationsByRef["local:01TEST"]; ok {
		t.Fatal("should delete map entry when queue empty")
	}

	// Out of range: no-op.
	m.removeEscalationAt("nonexistent", 0)
	m.removeEscalationAt("nonexistent", -1)
}

// TestCovHandleEscalationKey exercises key handling.
func TestCovHandleEscalationKey(t *testing.T) {
	// No escalation: not handled.
	m := hubModel{}
	_, handled := m.handleEscalationKey(tea.KeyMsg{Type: tea.KeyCtrlY})
	if handled {
		t.Fatal("should not handle when no escalation")
	}

	// With escalation, ctrl+y.
	m = hubModel{
		detail:  hubSessionDetail{Ref: "local:01TEST"},
		session: newModel(nil),
		escalationsByRef: map[string][]*hubEscalation{
			"local:01TEST": {{id: "esc1", ref: "local:01TEST"}},
		},
	}
	_, handled = m.handleEscalationKey(tea.KeyMsg{Type: tea.KeyCtrlY})
	if !handled {
		t.Fatal("should handle ctrl+y")
	}

	// ctrl+g.
	m.escalationsByRef["local:01TEST"][0].resolving = false
	_, handled = m.handleEscalationKey(tea.KeyMsg{Type: tea.KeyCtrlG})
	if !handled {
		t.Fatal("should handle ctrl+g")
	}

	// Other key: not handled.
	_, handled = m.handleEscalationKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if handled {
		t.Fatal("should not handle other keys")
	}
}

// TestCovSendHubEscalationResolve exercises the resolve command builder.
func TestCovSendHubEscalationResolve(t *testing.T) {
	var params appwire.SandboxEscalationResolveParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerSandboxEscalationResolve, func(_ context.Context, got appwire.SandboxEscalationResolveParams) (appwire.EmptyResponse, error) {
			params = got
			return appwire.EmptyResponse{}, nil
		})
	})
	defer cleanup()
	msg, ok := sendHubEscalationResolve(client, "local:01TEST", "esc1", true)().(hubEscalationResolvedMsg)
	if !ok || msg.err != nil || msg.ref != "local:01TEST" || msg.id != "esc1" || !msg.approve {
		t.Fatalf("resolve result = %#v", msg)
	}
	if params.Ref != "local:01TEST" || params.EscalationID != "esc1" || !params.Approve {
		t.Fatalf("resolve params = %#v", params)
	}
}

// TestCovHeadEscalation exercises head escalation lookup.
func TestCovHeadEscalation(t *testing.T) {
	// No escalations.
	m := hubModel{detail: hubSessionDetail{Ref: "local:01TEST"}}
	if m.headEscalation() != nil {
		t.Fatal("should return nil for no escalations")
	}

	// With escalations.
	m.escalationsByRef = map[string][]*hubEscalation{
		"local:01TEST": {{id: "esc1"}, {id: "esc2"}},
	}
	if m.headEscalation() == nil || m.headEscalation().id != "esc1" {
		t.Fatal("should return head escalation")
	}
}

// TestCovSurfaceEscalationsOnEntry exercises entry surface.
func TestCovSurfaceEscalationsOnEntry(t *testing.T) {
	m := hubModel{
		detail:  hubSessionDetail{Ref: "local:01TEST"},
		session: newModel(nil),
		escalationsByRef: map[string][]*hubEscalation{
			"local:01TEST": {{id: "esc1", ref: "local:01TEST"}},
		},
	}
	m.surfaceEscalationsOnEntry()
	if len(m.escalationsByRef["local:01TEST"]) != 1 {
		t.Fatal("should not remove existing escalation")
	}

	// With snapshot escalations.
	m = hubModel{
		detail: hubSessionDetail{
			Ref: "local:01TEST",
			PendingEscalations: []appwire.SandboxEscalationRequested{
				{EscalationID: "esc2", Tool: "write_file"},
			},
		},
		session: newModel(nil),
	}
	m.surfaceEscalationsOnEntry()
	if len(m.escalationsByRef["local:01TEST"]) != 1 {
		t.Fatal("should merge snapshot escalation")
	}
}

// TestCovMergeSnapshotEscalations exercises snapshot merge with dedup.
func TestCovMergeSnapshotEscalations(t *testing.T) {
	m := hubModel{
		escalationsByRef: map[string][]*hubEscalation{
			"local:01TEST": {{id: "esc1"}},
		},
	}
	detail := hubSessionDetail{
		Ref: "local:01TEST",
		PendingEscalations: []appwire.SandboxEscalationRequested{
			{EscalationID: "esc1"}, // duplicate
			{EscalationID: "esc2"}, // new
		},
	}
	m.mergeSnapshotEscalations(detail)
	if len(m.escalationsByRef["local:01TEST"]) != 2 {
		t.Fatalf("len = %d, want 2 (deduped)", len(m.escalationsByRef["local:01TEST"]))
	}

	// Empty ref: no-op.
	m = hubModel{}
	m.mergeSnapshotEscalations(hubSessionDetail{})
	if len(m.escalationsByRef) != 0 {
		t.Fatal("should not create map for empty ref")
	}
}

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// TestCovPromptHeadEscalation exercises prompt rendering.
func TestCovPromptHeadEscalation(t *testing.T) {
	m := hubModel{
		detail:  hubSessionDetail{Ref: "local:01TEST"},
		session: newModel(nil),
		escalationsByRef: map[string][]*hubEscalation{
			"local:01TEST": {{id: "esc1", tool: "write_file", path: "/tmp", mode: "workspace-write", ref: "local:01TEST"}},
		},
	}
	m.promptHeadEscalation()
	if len(m.session.messages) == 0 {
		t.Fatal("should add a system message")
	}

	// With more queued.
	m.escalationsByRef["local:01TEST"] = append(m.escalationsByRef["local:01TEST"], &hubEscalation{id: "esc2"})
	m.session.messages = nil
	m.promptHeadEscalation()
	if len(m.session.messages) == 0 {
		t.Fatal("should add a system message with more count")
	}

	// No escalation: no-op.
	m = hubModel{session: newModel(nil)}
	m.promptHeadEscalation()
	if len(m.session.messages) != 0 {
		t.Fatal("should not add message for no escalation")
	}
}
