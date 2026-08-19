package liveeval

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/envvars"
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
	f.Add("", "", "", "", "")
	f.Add("1", "/state", "/home/evener", "", "")
	f.Add("0", "", "/home/evener", "", "")
	f.Add("true", "  /state  ", "  /home/evener  ", "", "")
	// All three provider-config precedence arms, and combinations of
	// empty/nonempty, so every arm has its own seed:
	f.Add("1", "", "/home/evener", "/config/providers.toml", "")            // EVENER_PROVIDERS_CONFIG wins outright
	f.Add("1", "", "/home/evener", "/config/providers.toml", "/xdg-config") //   ...even with XDG_CONFIG_HOME also set
	f.Add("1", "", "/home/evener", "", "/xdg-config")                       // XDG_CONFIG_HOME wins when config env is empty
	f.Add("1", "", "/home/evener", "", "")                                  // falls all the way through to userHome/.config/evener
	f.Add("", "", "", "", "  ")

	f.Fuzz(func(t *testing.T, enabledValue, stateHome, userHome, providersConfigEnv, configHomeEnv string) {
		if len(enabledValue) > 64 || len(stateHome) > 4096 || len(userHome) > 4096 || len(providersConfigEnv) > 4096 || len(configHomeEnv) > 4096 {
			return
		}
		// os.Setenv rejects a NUL byte (returns an error, which t.Setenv
		// turns into a fatal failure); that is an os/exec-level restriction
		// on environment values, not a Paths/Enabled contract to enforce.
		if strings.ContainsRune(providersConfigEnv, 0) || strings.ContainsRune(configHomeEnv, 0) {
			return
		}

		// Enabled is a one-line total function: true iff the value is
		// exactly "1". No other truthy spelling ("true", "yes", "01")
		// counts — the opt-in is deliberately narrow so a stray env value
		// never silently enables live provider calls.
		if got, want := Enabled(enabledValue), enabledValue == "1"; got != want {
			t.Fatalf("Enabled(%q) = %v, want %v", enabledValue, got, want)
		}

		t.Setenv(envvars.EVENERProvidersConfig.Name, providersConfigEnv)
		t.Setenv(envvars.XDGConfigHome.Name, configHomeEnv)

		gotStateHome, gotProviders := Paths(stateHome, userHome)

		trimmedStateHome := strings.TrimSpace(stateHome)
		trimmedUserHome := strings.TrimSpace(userHome)
		trimmedProvidersConfigEnv := strings.TrimSpace(providersConfigEnv)
		trimmedConfigHomeEnv := strings.TrimSpace(configHomeEnv)

		wantStateHome := trimmedStateHome
		if wantStateHome == "" {
			wantStateHome = filepath.Join(trimmedUserHome, ".local", "state")
		}
		if gotStateHome != wantStateHome {
			t.Fatalf("Paths(%q, %q) state home = %q, want %q", stateHome, userHome, gotStateHome, wantStateHome)
		}

		// Three-level precedence, per the doc comment: EVENER_PROVIDERS_CONFIG
		// wins outright; else XDG_CONFIG_HOME/evener/providers.toml; else
		// userHome/.config/evener/providers.toml.
		var wantProviders string
		switch {
		case trimmedProvidersConfigEnv != "":
			wantProviders = trimmedProvidersConfigEnv
		case trimmedConfigHomeEnv != "":
			wantProviders = filepath.Join(trimmedConfigHomeEnv, "evener", "providers.toml")
		default:
			wantProviders = filepath.Join(trimmedUserHome, ".config", "evener", "providers.toml")
		}
		if gotProviders != wantProviders {
			t.Fatalf("Paths(%q, %q) with EVENER_PROVIDERS_CONFIG=%q XDG_CONFIG_HOME=%q providers = %q, want %q",
				stateHome, userHome, providersConfigEnv, configHomeEnv, gotProviders, wantProviders)
		}
	})
}
