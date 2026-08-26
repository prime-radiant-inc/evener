package tui

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/launchconfig"
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
