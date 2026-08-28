package tui

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuipick"
	"primeradiant.com/evener/internal/appserver"
)

func TestSpawnPlugins_FollowsDirAndRefreshesOnTransition(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.spawnPluginPreviewLoaded = true
	m.setSpawnFocus(hubSpawnFieldDir)
	m.advanceSpawnFocus(1)
	if m.spawnFocus != hubSpawnFieldPlugins {
		t.Fatalf("focus=%v, want plugins after dir", m.spawnFocus)
	}
	if m.spawnPluginPreviewRevision == 0 {
		t.Fatal("leaving Dir did not schedule a preview refresh")
	}
}

func TestSpawnPlugins_SummaryShowsInspectionDuringRefresh(t *testing.T) {
	values := []string{"alpha"}
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m := newHubModel(client, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.spawnPluginPreview = appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{
		{Name: "alpha", Selected: true},
		{Name: "beta"},
	}}
	m.spawnPluginPreviewLoaded = true
	m.spawnLaunchOverrides = &appwire.LaunchConfigLayer{EnabledPlugins: &values}
	if got := m.spawnPluginsSummary(); got != "1/2 enabled" {
		t.Fatalf("initial summary=%q, want count", got)
	}
	if cmd := m.requestSpawnPluginPreview(); cmd == nil {
		t.Fatal("refresh did not return a command")
	}
	if got := m.spawnPluginsSummary(); got != "Inspecting plugins…" {
		t.Fatalf("refresh summary=%q, want inspection state", got)
	}
}

func TestSpawnPlugins_ExplicitSelectionDoesNotSubmitWhilePreviewLoading(t *testing.T) {
	values := []string{"alpha"}
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m := newHubModel(client, "http://hub.test")
	m.mode = hubModeSpawn
	m.spawnHarness = "evener"
	m.spawnHarnessKinds = map[string]string{"evener": "evener"}
	m.spawnModel = "openai/gpt-5"
	m.spawnModels = []tuipick.ModelPickerItem{{ID: "openai/gpt-5", Display: "GPT 5"}}
	m.session.setInputValue("do something")
	m.spawnLaunchOverrides = &appwire.LaunchConfigLayer{EnabledPlugins: &values}
	m.spawnPluginPreviewLoaded = true
	m.spawnPluginPreviewLoading = true
	m.spawnPluginPreview = appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "alpha", Selected: true}}}

	updated, cmd := m.submitSpawnForm()
	m = updated.(hubModel)
	if cmd != nil || m.spawnSubmitting {
		t.Fatalf("loading preview submit cmd=%v submitting=%v", cmd != nil, m.spawnSubmitting)
	}
}

func TestSpawnPlugins_FailedPreviewWithoutKnownSelectionErrorCanSubmit(t *testing.T) {
	values := []string{"alpha"}
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m := newHubModel(client, "http://hub.test")
	m.mode = hubModeSpawn
	m.spawnHarness = "evener"
	m.spawnHarnessKinds = map[string]string{"evener": "evener"}
	m.spawnModel = "openai/gpt-5"
	m.spawnModels = []tuipick.ModelPickerItem{{ID: "openai/gpt-5", Display: "GPT 5"}}
	m.session.setInputValue("do something")
	m.spawnLaunchOverrides = &appwire.LaunchConfigLayer{EnabledPlugins: &values}
	m.spawnPluginPreviewLoaded = true
	m.spawnPluginPreviewErr = errors.New("preview unavailable")
	m.spawnPluginPreview = appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "alpha", Selected: true}}}

	updated, cmd := m.submitSpawnForm()
	m = updated.(hubModel)
	if cmd == nil || !m.spawnSubmitting {
		t.Fatalf("failed preview submit cmd=%v submitting=%v err=%v", cmd != nil, m.spawnSubmitting, m.err)
	}
}

func TestSpawnPlugins_PreviewStaleKeyDroppedAndApplyMergesOverride(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.spawnPluginPreviewRequestKey = "new"
	updated, _ := m.updateImpl(launchconfig.PluginPreviewResultMsg{Key: "old", Response: appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "old"}}}})
	m = updated.(hubModel)
	if m.spawnPluginPreviewLoaded {
		t.Fatal("stale preview response was applied")
	}
	m.spawnPluginPreviewRequestKey = "new"
	updated, _ = m.updateImpl(launchconfig.PluginPreviewResultMsg{Key: "new", Response: appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "alpha", Selected: true}, {Name: "beta"}}}})
	m = updated.(hubModel)
	updated, _ = m.updateImpl(launchconfig.PluginsForLaunchResultMsg{Applied: true, EnabledPlugins: func() *[]string { v := []string{"alpha"}; return &v }()})
	m = updated.(hubModel)
	if m.spawnLaunchOverrides == nil || !reflect.DeepEqual(*m.spawnLaunchOverrides.EnabledPlugins, []string{"alpha"}) {
		t.Fatalf("overrides=%+v, want enabledPlugins alpha", m.spawnLaunchOverrides)
	}
}

func TestSpawnPlugins_ExplicitEmptyIsSentAndStartFailurePreservesSelection(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.spawnLaunchOverrides = &appwire.LaunchConfigLayer{EnabledPlugins: func() *[]string { v := []string{}; return &v }()}
	m.spawnSubmitting = false
	updated, _ := m.updateImpl(launchconfig.PluginPreviewResultMsg{Key: "", Response: appwire.PluginPreviewResponse{}})
	m = updated.(hubModel)
	if m.spawnLaunchOverrides.EnabledPlugins == nil || len(*m.spawnLaunchOverrides.EnabledPlugins) != 0 {
		t.Fatal("explicit empty selection was lost")
	}
	updated, _ = m.updateImpl(hubSpawnMsg{err: tea.ErrProgramKilled})
	m = updated.(hubModel)
	if m.spawnLaunchOverrides == nil || m.spawnLaunchOverrides.EnabledPlugins == nil {
		t.Fatal("failed start cleared explicit selection")
	}
}

func TestSpawnPlugins_CancelRestoresAndSuccessfulStartClears(t *testing.T) {
	values := []string{"alpha"}
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.spawnLaunchOverrides = &appwire.LaunchConfigLayer{EnabledPlugins: &values}
	p := launchconfig.NewPluginsForLaunchPanel(appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "alpha", Selected: true}}}, &values, 80)
	m.spawnPluginsPanel = &p
	updated, cmd := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(hubModel)
	if cmd == nil {
		t.Fatal("cancel did not return result")
	}
	updated, _ = m.updateImpl(cmd())
	m = updated.(hubModel)
	if m.spawnLaunchOverrides == nil || !reflect.DeepEqual(*m.spawnLaunchOverrides.EnabledPlugins, values) {
		t.Fatalf("cancel changed one-shot selection: %+v", m.spawnLaunchOverrides)
	}
	updated, _ = m.updateImpl(hubSpawnMsg{resp: hubSpawnResponse{Ref: "local:02NEW"}})
	m = updated.(hubModel)
	if m.spawnLaunchOverrides != nil {
		t.Fatalf("successful start retained one-shot selection: %+v", m.spawnLaunchOverrides)
	}
}

func TestSpawnPlugins_FailureRetryAndOpenPanelRefresh(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m := newHubModel(client, "http://hub.test")
	m.mode = hubModeSpawn
	m.spawnPluginPreviewRequestKey = "current"
	updated, _ := m.updateImpl(launchconfig.PluginPreviewResultMsg{Key: "current", Err: errors.New("hub unavailable")})
	m = updated.(hubModel)
	if !strings.Contains(m.spawnPluginsSummary(), "Couldn't inspect plugins") {
		t.Fatalf("failure summary=%q", m.spawnPluginsSummary())
	}
	m.setSpawnFocus(hubSpawnFieldPlugins)
	updated, cmd := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(hubModel)
	if cmd == nil {
		t.Fatal("failure state did not expose retry command")
	}
	if _, ok := cmd().(launchconfig.PluginPreviewRequestMsg); !ok {
		t.Fatalf("retry command message=%T, want PluginPreviewRequestMsg", cmd())
	}
	preview := appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "fresh"}}}
	p := launchconfig.NewPluginsForLaunchPanel(preview, nil, 80)
	m.spawnPluginsPanel = &p
	m.spawnPluginPreviewRequestKey = "open"
	updated, _ = m.updateImpl(launchconfig.PluginPreviewResultMsg{Key: "stale", Response: appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "stale"}}}})
	m = updated.(hubModel)
	if strings.Contains(m.spawnPluginsPanel.View(), "stale") {
		t.Fatal("stale preview replaced candidates in open panel")
	}
	updated, _ = m.updateImpl(launchconfig.PluginPreviewResultMsg{Key: "open", Response: appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "new"}}}})
	m = updated.(hubModel)
	if m.spawnPluginsPanel == nil || !strings.Contains(m.spawnPluginsPanel.View(), "new") {
		t.Fatal("open panel did not receive refreshed candidates")
	}
	updated, _ = m.updateImpl(launchconfig.PluginPreviewResultMsg{Key: "open", Err: errors.New("temporary failure")})
	m = updated.(hubModel)
	if !strings.Contains(m.spawnPluginsPanel.View(), "Couldn't inspect plugins") {
		t.Fatal("open panel did not show honest refresh failure")
	}
	updated, cmd = m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(hubModel)
	if cmd == nil {
		t.Fatal("open panel did not expose host retry")
	}
	if !cmd().(launchconfig.PluginsForLaunchResultMsg).Retry {
		t.Fatal("open panel enter did not request retry")
	}
}

func TestSpawnPlugins_ChangedPreviewFailureDoesNotReportPreviousSuccess(t *testing.T) {
	m := newHubModel(&appwire.Client{}, "http://hub.test")
	m.mode = hubModeSpawn
	m.spawnHarness = "evener"
	m.spawnHarnessKinds = map[string]string{"evener": "evener"}
	m.spawnDir = "/tmp/request-a"

	aCmd := m.requestSpawnPluginPreview()
	aRequest := aCmd().(launchconfig.PluginPreviewRequestMsg)
	updated, _ := m.updateImpl(launchconfig.PluginPreviewResultMsg{
		Key: aRequest.Key,
		Response: appwire.PluginPreviewResponse{
			Plugins: []appwire.PluginLaunchCandidate{{Name: "plugin-a", Selected: true}},
		},
	})
	m = updated.(hubModel)

	m.spawnDir = "/tmp/request-b"
	bCmd := m.requestSpawnPluginPreview()
	bRequest := bCmd().(launchconfig.PluginPreviewRequestMsg)
	updated, _ = m.updateImpl(launchconfig.PluginPreviewResultMsg{Key: bRequest.Key, Err: errors.New("preview unavailable")})
	m = updated.(hubModel)

	if m.spawnPluginPreviewLoaded {
		t.Fatal("failed changed preview remained marked loaded")
	}
	if len(m.spawnPluginPreview.Plugins) != 0 {
		t.Fatalf("failed changed preview retained candidates: %+v", m.spawnPluginPreview.Plugins)
	}
	if got, want := m.spawnPluginsSummary(), "Couldn't inspect plugins — press Enter to retry"; got != want {
		t.Fatalf("summary=%q, want %q", got, want)
	}
}

func TestSpawnPlugins_CachedFailureOpensRemovalOnlyPanelAndClearsSelection(t *testing.T) {
	values := []string{"stale"}
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.spawnPluginPreviewLoaded = true
	m.spawnPluginPreview = appwire.PluginPreviewResponse{
		Plugins:         []appwire.PluginLaunchCandidate{{Name: "fresh"}},
		SelectionErrors: []appwire.PluginSelectionError{{Name: "stale", Reason: "no valid plugin candidate"}},
	}
	m.spawnPluginPreviewParamsDigest = "same-request"
	m.spawnPluginPreviewLastSuccess = "same-request"
	m.spawnLaunchOverrides = &appwire.LaunchConfigLayer{EnabledPlugins: &values}
	m.spawnPluginPreviewRequestKey = "refresh"
	if !m.spawnPluginSelectionBlocked() {
		t.Fatal("cached selection error did not block Start")
	}

	updated, _ := m.updateImpl(launchconfig.PluginPreviewResultMsg{Key: "refresh", Err: errors.New("temporary failure")})
	m = updated.(hubModel)
	if !m.spawnPluginPreviewLoaded || m.spawnPluginPreviewErr == nil {
		t.Fatalf("failed refresh lost cached state: loaded=%v err=%v", m.spawnPluginPreviewLoaded, m.spawnPluginPreviewErr)
	}

	m.setSpawnFocus(hubSpawnFieldPlugins)
	updated, cmd := m.updateImpl(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(hubModel)
	if cmd != nil || m.spawnPluginsPanel == nil {
		t.Fatalf("open cached failure panel: cmd=%v panel=%v", cmd != nil, m.spawnPluginsPanel != nil)
	}
	if view := m.spawnPluginsPanel.View(); !strings.Contains(view, "Couldn't inspect plugins") || !strings.Contains(view, "N none") {
		t.Fatalf("cached failure panel view=%q", view)
	}

	updated, _ = m.updateImpl(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	m = updated.(hubModel)
	updated, cmd = m.updateImpl(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(hubModel)
	if cmd == nil {
		t.Fatal("cleared cached failure did not apply")
	}
	updated, _ = m.updateImpl(cmd())
	m = updated.(hubModel)
	if m.spawnPluginsPanel != nil {
		t.Fatal("applied cached failure panel remained open")
	}
	if m.spawnLaunchOverrides == nil || m.spawnLaunchOverrides.EnabledPlugins == nil || len(*m.spawnLaunchOverrides.EnabledPlugins) != 0 {
		t.Fatalf("cleared cached failure overrides=%+v, want explicit empty enabledPlugins", m.spawnLaunchOverrides)
	}
}

func TestSpawnPlugins_DirectoryRefreshIsTransitionDriven(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()
	m := newHubModel(client, "http://hub.test")
	m.mode = hubModeSpawn
	m.spawnDir = "/tmp/old"
	m.spawnDirInput.SetValue(m.spawnDir)
	m.spawnPluginPreviewLoaded = true
	params := appwire.PluginPreviewParams{CWD: m.spawnDir}
	raw, _ := json.Marshal(params)
	m.spawnPluginPreviewParamsDigest = string(raw)
	m.spawnPluginPreviewRevision = 1
	m.setSpawnFocus(hubSpawnFieldDir)
	for _, r := range "typed" {
		updated, _ := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(hubModel)
	}
	before := m.spawnPluginPreviewRevision
	m.spawnDir = "/tmp/new"
	m.spawnDirInput.SetValue(m.spawnDir)
	updated, cmd := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(hubModel)
	if cmd == nil || m.spawnPluginPreviewRevision != before+1 {
		t.Fatalf("dir enter refresh revision=%d before=%d cmd=%v", m.spawnPluginPreviewRevision, before, cmd != nil)
	}
}

func TestSpawnPlugins_HarnessSwitchInvalidatesInFlightPreview(t *testing.T) {
	values := []string{"alpha"}
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()

	m := newHubModel(client, "http://hub.test")
	m.mode = hubModeSpawn
	m.spawnHarnesses = []string{"evener", "codex"}
	m.spawnHarnessKinds = map[string]string{"evener": "evener", "codex": "codex"}
	m.spawnLaunchOverrides = &appwire.LaunchConfigLayer{EnabledPlugins: &values}
	m.spawnHarness = "evener"
	oldCmd := m.requestSpawnPluginPreview()
	oldRequest := oldCmd().(launchconfig.PluginPreviewRequestMsg)

	m.setSpawnFocus(hubSpawnFieldHarness)
	updated, cmd := m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(hubModel)
	if cmd != nil || m.spawnHarness != "codex" {
		t.Fatalf("switch to Codex: harness=%q cmd=%v", m.spawnHarness, cmd != nil)
	}
	if m.spawnPluginPreviewLoaded || m.spawnPluginPreviewLoading || m.spawnPluginPreviewRequestKey != "" {
		t.Fatalf("switch to Codex retained preview state: loaded=%v loading=%v key=%q", m.spawnPluginPreviewLoaded, m.spawnPluginPreviewLoading, m.spawnPluginPreviewRequestKey)
	}

	updated, _ = m.updateImpl(launchconfig.PluginPreviewResultMsg{
		Key:      oldRequest.Key,
		Response: appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "alpha"}}},
	})
	m = updated.(hubModel)
	if m.spawnPluginPreviewLoaded || m.spawnPluginPreviewLoading || len(m.spawnPluginPreview.Plugins) != 0 {
		t.Fatalf("late Evener preview changed Codex state: loaded=%v loading=%v preview=%+v", m.spawnPluginPreviewLoaded, m.spawnPluginPreviewLoading, m.spawnPluginPreview)
	}

	m.setSpawnFocus(hubSpawnFieldHarness)
	updated, cmd = m.updateSpawnKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(hubModel)
	if m.spawnHarness != "evener" || cmd == nil {
		t.Fatalf("switch back to Evener: harness=%q cmd=%v", m.spawnHarness, cmd != nil)
	}
	freshRequest := cmd().(launchconfig.PluginPreviewRequestMsg)
	if freshRequest.Key == oldRequest.Key || !m.spawnPluginPreviewLoading {
		t.Fatalf("Evener revalidation key=%q old=%q loading=%v", freshRequest.Key, oldRequest.Key, m.spawnPluginPreviewLoading)
	}
}

func TestSpawnPlugins_UnsupportedPreviewRequestClearsState(t *testing.T) {
	m := newHubModel(&appwire.Client{}, "http://hub.test")
	m.mode = hubModeSpawn
	m.spawnHarness = "codex"
	m.spawnHarnessKinds = map[string]string{"codex": "codex"}
	m.spawnPluginPreview = appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "stale"}}}
	m.spawnPluginPreviewLoaded = true
	m.spawnPluginPreviewLoading = true
	m.spawnPluginPreviewErr = errors.New("stale error")
	m.spawnPluginPreviewRequestKey = "stale"

	if cmd := m.requestSpawnPluginPreview(); cmd != nil {
		t.Fatal("unsupported harness returned a preview command")
	}
	if m.spawnPluginPreviewLoaded || m.spawnPluginPreviewLoading || m.spawnPluginPreviewErr != nil || m.spawnPluginPreviewRequestKey != "" || len(m.spawnPluginPreview.Plugins) != 0 {
		t.Fatalf("unsupported request retained preview state: loaded=%v loading=%v err=%v key=%q preview=%+v", m.spawnPluginPreviewLoaded, m.spawnPluginPreviewLoading, m.spawnPluginPreviewErr, m.spawnPluginPreviewRequestKey, m.spawnPluginPreview)
	}
}

func TestSpawnPlugins_SummaryCountsOnlyCurrentCandidates(t *testing.T) {
	values := []string{"alpha", "missing"}
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.spawnHarness = "evener"
	m.spawnHarnessKinds = map[string]string{"evener": "evener"}
	m.spawnLaunchOverrides = &appwire.LaunchConfigLayer{EnabledPlugins: &values}
	m.spawnPluginPreviewLoaded = true
	m.spawnPluginPreview = appwire.PluginPreviewResponse{
		Plugins:         []appwire.PluginLaunchCandidate{{Name: "alpha", Selected: true}},
		SelectionErrors: []appwire.PluginSelectionError{{Name: "missing", Reason: "no valid plugin candidate"}},
	}

	if got, want := m.spawnPluginsSummary(), "1/1 enabled (unavailable: missing)"; got != want {
		t.Fatalf("summary=%q, want %q", got, want)
	}
	if !m.spawnPluginSelectionBlocked() {
		t.Fatal("unavailable explicit selection no longer blocks Start")
	}
}

func TestSpawnPlugins_CodexIgnoresStaleSelectionForLaunch(t *testing.T) {
	var started appwire.ThreadStartParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadStart, func(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
			started = params
			return appwire.ThreadStartResponse{Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: "codex:01NEW"}}}, nil
		})
	})
	defer cleanup()

	selected := []string{"missing"}
	m := newHubModel(client, "http://hub.test")
	m.mode = hubModeSpawn
	m.spawnHarness = "codex"
	m.spawnHarnesses = []string{"evener", "codex"}
	m.spawnHarnessKinds = map[string]string{"evener": "evener", "codex": "codex"}
	m.spawnLaunchOverrides = &appwire.LaunchConfigLayer{Sandbox: "read-only", EnabledPlugins: &selected}
	m.spawnPluginPreviewLoaded = true
	m.spawnPluginPreview = appwire.PluginPreviewResponse{SelectionErrors: []appwire.PluginSelectionError{{Name: "missing"}}}

	if view := m.spawnView(); strings.Contains(view, "Plugins:") {
		t.Fatalf("Codex spawn should hide plugin field:\n%s", view)
	}
	m.setSpawnFocus(hubSpawnFieldDir)
	if cmd := m.advanceSpawnFocus(1); m.spawnFocus != hubSpawnFieldPrompt || cmd != nil {
		t.Fatalf("Codex focus advanced to field=%v with cmd=%v, want prompt without preview", m.spawnFocus, cmd != nil)
	}

	m.session.setInputValue("launch")
	updated, cmd := m.submitSpawnForm()
	m = updated.(hubModel)
	if m.err != nil || cmd == nil {
		t.Fatalf("Codex launch blocked: err=%v cmd=%v", m.err, cmd != nil)
	}
	if msg := cmd().(hubSpawnMsg); msg.err != nil || msg.resp.Ref != "codex:01NEW" {
		t.Fatalf("Codex launch result=%+v", msg)
	}
	if started.LaunchOverrides == nil || started.LaunchOverrides.Sandbox != "read-only" || started.LaunchOverrides.EnabledPlugins != nil {
		t.Fatalf("Codex launch overrides=%+v, want unrelated override preserved and plugins omitted", started.LaunchOverrides)
	}
}

func TestSpawnPlugins_EvenerSubmitsSelectedPlugins(t *testing.T) {
	var started appwire.ThreadStartParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadStart, func(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
			started = params
			return appwire.ThreadStartResponse{Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:01NEW"}}}, nil
		})
	})
	defer cleanup()

	selected := []string{"alpha"}
	m := newHubModel(client, "http://hub.test")
	m.mode = hubModeSpawn
	m.spawnHarness = "evener"
	m.spawnHarnessKinds = map[string]string{"evener": "evener"}
	m.spawnModel = "openai/gpt-5"
	m.spawnModels = []tuipick.ModelPickerItem{{ID: "openai/gpt-5"}}
	m.spawnLaunchOverrides = &appwire.LaunchConfigLayer{Sandbox: "read-only", EnabledPlugins: &selected}
	m.spawnPluginPreviewLoaded = true
	m.spawnPluginPreview = appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "alpha", Selected: true}}}
	m.session.setInputValue("launch")

	updated, cmd := m.submitSpawnForm()
	m = updated.(hubModel)
	if m.err != nil || cmd == nil {
		t.Fatalf("Evener launch blocked: err=%v cmd=%v", m.err, cmd != nil)
	}
	if msg := cmd().(hubSpawnMsg); msg.err != nil || msg.resp.Ref != "local:01NEW" {
		t.Fatalf("Evener launch result=%+v", msg)
	}
	if started.LaunchOverrides == nil || started.LaunchOverrides.Sandbox != "read-only" || !reflect.DeepEqual(*started.LaunchOverrides.EnabledPlugins, selected) {
		t.Fatalf("Evener launch overrides=%+v, want sandbox and selected plugins", started.LaunchOverrides)
	}
}

func TestSpawnPlugins_LatePreviewFromClosedFormCannotUpdateReopenedForm(t *testing.T) {
	m := newHubModel(&appwire.Client{}, "http://hub.test")
	m.openSpawnForm()
	m.setSpawnDir("/tmp/project")
	oldCmd := m.requestSpawnPluginPreview()
	if oldCmd == nil {
		t.Fatal("old form preview request was not created")
	}
	oldRequest := oldCmd().(launchconfig.PluginPreviewRequestMsg)

	m.closeSpawnForm()
	m.openSpawnForm()
	m.setSpawnDir("/tmp/project")
	newCmd := m.requestSpawnPluginPreview()
	if newCmd == nil {
		t.Fatal("reopened form preview request was not created")
	}
	newRequest := newCmd().(launchconfig.PluginPreviewRequestMsg)
	if oldRequest.Key == newRequest.Key {
		t.Fatalf("preview request key reused after reopen: %q", newRequest.Key)
	}

	updated, _ := m.updateImpl(launchconfig.PluginPreviewResultMsg{
		Key:      oldRequest.Key,
		Response: appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "old"}}},
	})
	m = updated.(hubModel)
	if m.spawnPluginPreviewLoaded || len(m.spawnPluginPreview.Plugins) != 0 {
		t.Fatalf("late old preview changed reopened form: loaded=%v preview=%+v", m.spawnPluginPreviewLoaded, m.spawnPluginPreview)
	}

	updated, _ = m.updateImpl(launchconfig.PluginPreviewResultMsg{
		Key:      newRequest.Key,
		Response: appwire.PluginPreviewResponse{Plugins: []appwire.PluginLaunchCandidate{{Name: "new"}}},
	})
	m = updated.(hubModel)
	if !m.spawnPluginPreviewLoaded || len(m.spawnPluginPreview.Plugins) != 1 || m.spawnPluginPreview.Plugins[0].Name != "new" {
		t.Fatalf("current preview was not applied: loaded=%v preview=%+v", m.spawnPluginPreviewLoaded, m.spawnPluginPreview)
	}
}
