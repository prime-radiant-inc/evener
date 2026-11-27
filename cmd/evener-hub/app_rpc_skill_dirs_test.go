package hub

import (
	"context"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubRPCSkillDirsGetReportsHostRoots(t *testing.T) {
	home := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	// The launch config root is independent of the environment: discovery in a
	// session resolves the user config root from the inherited environment.
	hub := newHubRPCTestServer(t, hubcore.WebConfig{LaunchConfigRoot: t.TempDir()})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	var got appwire.SkillDirsResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsSkillDirsGet, appwire.EmptyParams{}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	want := appwire.SkillDirsResponse{
		HomeSkillsDir: filepath.Join(home, ".agents", "skills"),
		UserSkillsDir: filepath.Join(configHome, "evener", "skills"),
	}
	if got != want {
		t.Fatalf("get = %+v, want %+v", got, want)
	}
}

func TestHubRPCSkillDirsGetOmitsRootsItCannotResolve(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	hub := newHubRPCTestServer(t, hubcore.WebConfig{})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	var got appwire.SkillDirsResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsSkillDirsGet, appwire.EmptyParams{}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != (appwire.SkillDirsResponse{}) {
		t.Fatalf("get = %+v, want an empty response", got)
	}
}
