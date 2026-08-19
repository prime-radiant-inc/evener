package liveeval

import (
	"path/filepath"
	"testing"

	"primeradiant.com/evener/envvars"
)

func TestEnabledRequiresExplicitOptIn(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{value: "", want: false},
		{value: "0", want: false},
		{value: "true", want: false},
		{value: "1", want: true},
	} {
		if got := Enabled(tc.value); got != tc.want {
			t.Errorf("Enabled(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestPathsUseConfiguredStateAndHomeRoots(t *testing.T) {
	clearProviderPathEnv(t)
	stateHome := filepath.Join(t.TempDir(), "state")
	userHome := filepath.Join(t.TempDir(), "home")

	gotStateHome, gotProviders := Paths(stateHome, userHome)
	if gotStateHome != stateHome {
		t.Fatalf("Paths configured state home = %q, want %q", gotStateHome, stateHome)
	}
	if want := filepath.Join(userHome, ".config", "evener", "providers.toml"); gotProviders != want {
		t.Fatalf("Paths configured providers file = %q, want %q", gotProviders, want)
	}
}

func TestPathsDefaultStateHomeFollowsUserHome(t *testing.T) {
	clearProviderPathEnv(t)
	userHome := filepath.Join(t.TempDir(), "home")

	gotStateHome, gotProviders := Paths("", userHome)
	if want := filepath.Join(userHome, ".local", "state"); gotStateHome != want {
		t.Fatalf("Paths default state home = %q, want %q", gotStateHome, want)
	}
	if want := filepath.Join(userHome, ".config", "evener", "providers.toml"); gotProviders != want {
		t.Fatalf("Paths default providers file = %q, want %q", gotProviders, want)
	}
}

func TestPathsProviderConfigEnvOverridesConfigHomeAndHome(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "config")
	userHome := filepath.Join(t.TempDir(), "home")
	explicit := filepath.Join(t.TempDir(), "explicit.toml")
	t.Setenv(envvars.EVENERProvidersConfig.Name, explicit)
	t.Setenv(envvars.XDGConfigHome.Name, configHome)

	_, got := Paths("", userHome)
	if got != explicit {
		t.Fatalf("Paths explicit provider config = %q, want %q", got, explicit)
	}
}

func TestPathsProviderConfigUsesXDGConfigHomeBeforeHome(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "config")
	userHome := filepath.Join(t.TempDir(), "home")
	t.Setenv(envvars.EVENERProvidersConfig.Name, "")
	t.Setenv(envvars.XDGConfigHome.Name, configHome)

	_, got := Paths("", userHome)
	want := filepath.Join(configHome, "evener", "providers.toml")
	if got != want {
		t.Fatalf("Paths XDG_CONFIG_HOME provider config = %q, want %q", got, want)
	}
}

func clearProviderPathEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envvars.EVENERProvidersConfig.Name, "")
	t.Setenv(envvars.XDGConfigHome.Name, "")
}
