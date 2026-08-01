package liveeval

import (
	"path/filepath"
	"testing"
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
	stateHome := filepath.Join(t.TempDir(), "state")
	userHome := filepath.Join(t.TempDir(), "home")

	gotStateHome, gotProviders := Paths(stateHome, userHome)
	if gotStateHome != stateHome {
		t.Fatalf("Paths configured state home = %q, want %q", gotStateHome, stateHome)
	}
	if want := filepath.Join(userHome, ".serf", "providers.toml"); gotProviders != want {
		t.Fatalf("Paths configured providers file = %q, want %q", gotProviders, want)
	}
}

func TestPathsDefaultStateHomeFollowsUserHome(t *testing.T) {
	userHome := filepath.Join(t.TempDir(), "home")

	gotStateHome, gotProviders := Paths("", userHome)
	if want := filepath.Join(userHome, ".local", "state"); gotStateHome != want {
		t.Fatalf("Paths default state home = %q, want %q", gotStateHome, want)
	}
	if want := filepath.Join(userHome, ".serf", "providers.toml"); gotProviders != want {
		t.Fatalf("Paths default providers file = %q, want %q", gotProviders, want)
	}
}
