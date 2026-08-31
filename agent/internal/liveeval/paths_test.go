package liveeval

import (
	"os"
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

	gotStateHome, gotProviders, noUserLayer := Paths(stateHome, userHome)
	if gotStateHome != stateHome {
		t.Fatalf("Paths configured state home = %q, want %q", gotStateHome, stateHome)
	}
	if want := filepath.Join(userHome, ".config", "evener", "providers.toml"); gotProviders != want {
		t.Fatalf("Paths configured providers file = %q, want %q", gotProviders, want)
	}
	if noUserLayer {
		t.Fatal("an unset EVENER_PROVIDERS_CONFIG is the default path, not \"no user layer\"")
	}
}

func TestPathsDefaultStateHomeFollowsUserHome(t *testing.T) {
	clearProviderPathEnv(t)
	userHome := filepath.Join(t.TempDir(), "home")

	gotStateHome, gotProviders, _ := Paths("", userHome)
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

	_, got, noUserLayer := Paths("", userHome)
	if got != explicit {
		t.Fatalf("Paths explicit provider config = %q, want %q", got, explicit)
	}
	if noUserLayer {
		t.Fatalf("a named EVENER_PROVIDERS_CONFIG is a user layer")
	}
}

// The tri-state's third state (spec §10, §14.1): present and empty means "no
// user layer at all", which is not the default path.
func TestPathsEmptyProviderConfigMeansNoUserLayer(t *testing.T) {
	userHome := filepath.Join(t.TempDir(), "home")
	t.Setenv(envvars.EVENERProvidersConfig.Name, "")
	t.Setenv(envvars.XDGConfigHome.Name, filepath.Join(t.TempDir(), "config"))

	stateHome, got, noUserLayer := Paths("", userHome)
	if !noUserLayer {
		t.Fatalf("an empty EVENER_PROVIDERS_CONFIG must report no user layer; got path %q", got)
	}
	if got != "" {
		t.Fatalf("no user layer must name no file; got %q", got)
	}
	if want := filepath.Join(userHome, ".local", "state"); stateHome != want {
		t.Fatalf("state home = %q, want %q", stateHome, want)
	}
}

func TestPathsProviderConfigUsesXDGConfigHomeBeforeHome(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "config")
	userHome := filepath.Join(t.TempDir(), "home")
	clearProviderPathEnv(t)
	t.Setenv(envvars.XDGConfigHome.Name, configHome)

	_, got, _ := Paths("", userHome)
	want := filepath.Join(configHome, "evener", "providers.toml")
	if got != want {
		t.Fatalf("Paths XDG_CONFIG_HOME provider config = %q, want %q", got, want)
	}
}

// clearProviderPathEnv leaves EVENER_PROVIDERS_CONFIG genuinely unset — the
// tri-state's first state — and restores whatever the developer had after the
// test. t.Setenv registers that restore; os.Unsetenv is what "unset" means.
func clearProviderPathEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envvars.EVENERProvidersConfig.Name, "")
	if err := os.Unsetenv(envvars.EVENERProvidersConfig.Name); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envvars.XDGConfigHome.Name, "")
	if err := os.Unsetenv(envvars.XDGConfigHome.Name); err != nil {
		t.Fatal(err)
	}
}
