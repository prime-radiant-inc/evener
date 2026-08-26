package tui

import (
	"reflect"
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
