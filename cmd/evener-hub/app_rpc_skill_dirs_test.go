package hub

import (
	"context"
	"fmt"
	"os"
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
	// No launch.toml exists under this root, so the global layer contributes no
	// env override and the roots follow the process environment.
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

func TestHubRPCSkillDirsGetAppliesGlobalLaunchEnvAtRequestTime(t *testing.T) {
	home := t.TempDir()
	processConfig := t.TempDir()
	launchRoot := t.TempDir()
	overrideConfig := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", processConfig)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{LaunchConfigRoot: launchRoot})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	request := func() appwire.SkillDirsResponse {
		t.Helper()
		var got appwire.SkillDirsResponse
		if err := client.Request(context.Background(), appwire.MethodEvenerSettingsSkillDirsGet, appwire.EmptyParams{}, &got); err != nil {
			t.Fatalf("get: %v", err)
		}
		return got
	}
	wantHome := filepath.Join(home, ".agents", "skills")
	before := appwire.SkillDirsResponse{
		HomeSkillsDir: wantHome,
		UserSkillsDir: filepath.Join(processConfig, "evener", "skills"),
	}
	if got := request(); got != before {
		t.Fatalf("before launch override = %+v, want %+v", got, before)
	}

	body := fmt.Sprintf("[env]\nXDG_CONFIG_HOME = %q\n", overrideConfig)
	if err := os.WriteFile(filepath.Join(launchRoot, "launch.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	after := appwire.SkillDirsResponse{
		HomeSkillsDir: wantHome,
		UserSkillsDir: filepath.Join(overrideConfig, "evener", "skills"),
	}
	if got := request(); got != after {
		t.Fatalf("after launch override = %+v, want %+v", got, after)
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
