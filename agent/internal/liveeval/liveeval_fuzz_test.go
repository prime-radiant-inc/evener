package liveeval

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/envvars"
)

// FuzzLiveEvalPaths drives the package's whole exported surface: Enabled (the
// live-eval opt-in gate) and Paths (the state-home / provider-config
// resolver). Both are pure, deterministic string/path logic with no
// execution, network, or filesystem access — liveeval's job is setup POLICY
// for provider-backed evals, not the live calls themselves — so there is no
// dangerous seam to avoid here and no execution/never-wedge concern; the
// thickest (really, only) seam is Paths' resolution precedence, which this
// target re-derives independently from the doc comment's documented
// precedence and asserts against, the same way FuzzPromptPaths pins
// globalPromptsDir's XDG precedence.
//
// Termination is not an interesting property for this package: neither
// function loops or recurses, so "never panics" is effectively the whole
// floor, and the precedence oracle below is the real, semantic invariant.
func FuzzLiveEvalPaths(f *testing.F) {
	f.Add("", "", "", "")
	f.Add("1", "/state", "/home/serf", "")
	f.Add("0", "", "/home/serf", "")
	f.Add("true", "  /state  ", "  /home/serf  ", "")
	f.Add("1", "", "/home/serf", "/config/providers.toml")
	f.Add("1", "", "/home/serf", "")
	f.Add("", "", "", "  ")

	f.Fuzz(func(t *testing.T, enabledValue, stateHome, userHome, providersConfigEnv string) {
		if len(enabledValue) > 64 || len(stateHome) > 4096 || len(userHome) > 4096 || len(providersConfigEnv) > 4096 {
			return
		}
		// os.Setenv rejects a NUL byte (returns an error, which t.Setenv
		// turns into a fatal failure); that is an os/exec-level restriction
		// on environment values, not a Paths/Enabled contract to enforce.
		if strings.ContainsRune(providersConfigEnv, 0) {
			return
		}

		// Enabled is a one-line total function: true iff the value is
		// exactly "1". No other truthy spelling ("true", "yes", "01")
		// counts — the opt-in is deliberately narrow so a stray env value
		// never silently enables live provider calls.
		if got, want := Enabled(enabledValue), enabledValue == "1"; got != want {
			t.Fatalf("Enabled(%q) = %v, want %v", enabledValue, got, want)
		}

		t.Setenv(envvars.SERFProvidersConfig.Name, providersConfigEnv)
		// SERF_STATE_DIR is exercised by the package's own table tests
		// (TestPathsProviderConfigUsesStateDirBeforeHome); pin it empty here
		// so this target's oracle only has to reason about the two
		// variables it varies, without losing that env-precedence coverage
		// (SERF_PROVIDERS_CONFIG alone already exercises the "env wins over
		// both params" arm; the unvaried SERF_STATE_DIR arm is covered by
		// the seed-replayed table tests, which run in the same `go test`).
		t.Setenv(envvars.SERFStateDir.Name, "")

		gotStateHome, gotProviders := Paths(stateHome, userHome)

		trimmedStateHome := strings.TrimSpace(stateHome)
		trimmedUserHome := strings.TrimSpace(userHome)
		trimmedProvidersConfigEnv := strings.TrimSpace(providersConfigEnv)

		wantStateHome := trimmedStateHome
		if wantStateHome == "" {
			wantStateHome = filepath.Join(trimmedUserHome, ".local", "state")
		}
		if gotStateHome != wantStateHome {
			t.Fatalf("Paths(%q, %q) state home = %q, want %q", stateHome, userHome, gotStateHome, wantStateHome)
		}

		wantProviders := trimmedProvidersConfigEnv
		if wantProviders == "" {
			wantProviders = filepath.Join(trimmedUserHome, ".serf", "providers.toml")
		}
		if gotProviders != wantProviders {
			t.Fatalf("Paths(%q, %q) with SERF_PROVIDERS_CONFIG=%q providers = %q, want %q", stateHome, userHome, providersConfigEnv, gotProviders, wantProviders)
		}
	})
}
