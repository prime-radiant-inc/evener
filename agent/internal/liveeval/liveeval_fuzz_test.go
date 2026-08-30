package liveeval

import (
	"os"
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
	f.Add("", false, "", "", "", "")
	f.Add("1", false, "/state", "/home/evener", "", "")
	f.Add("0", false, "", "/home/evener", "", "")
	f.Add("true", false, "  /state  ", "  /home/evener  ", "", "")
	// Every arm of the tri-state and of the fallback chain gets its own seed:
	f.Add("1", true, "", "/home/evener", "/config/providers.toml", "")            // a named EVENER_PROVIDERS_CONFIG wins outright
	f.Add("1", true, "", "/home/evener", "/config/providers.toml", "/xdg-config") //   ...even with XDG_CONFIG_HOME also set
	f.Add("1", true, "", "/home/evener", "", "/xdg-config")                       // present and empty: no user layer
	f.Add("1", false, "", "/home/evener", "", "/xdg-config")                      // unset: XDG_CONFIG_HOME
	f.Add("1", false, "", "/home/evener", "", "")                                 // unset: falls through to userHome/.config/evener
	f.Add("", true, "", "", "", "  ")

	f.Fuzz(func(t *testing.T, enabledValue string, providersConfigSet bool, stateHome, userHome, providersConfigEnv, configHomeEnv string) {
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

		// t.Setenv always leaves the variable present, so the tri-state's
		// "unset" arm is reached by unsetting it after t.Setenv has registered
		// the restore.
		t.Setenv(envvars.EVENERProvidersConfig.Name, providersConfigEnv)
		if !providersConfigSet {
			if err := os.Unsetenv(envvars.EVENERProvidersConfig.Name); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv(envvars.XDGConfigHome.Name, configHomeEnv)

		gotStateHome, gotProviders, gotNoUserLayer := Paths(stateHome, userHome)

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

		// The tri-state, per the doc comment: EVENER_PROVIDERS_CONFIG present
		// and blank means no user layer at all; present and named wins
		// outright, verbatim; unset falls through to
		// XDG_CONFIG_HOME/evener/providers.toml, then
		// userHome/.config/evener/providers.toml.
		var wantProviders string
		wantNoUserLayer := providersConfigSet && trimmedProvidersConfigEnv == ""
		switch {
		case wantNoUserLayer:
			wantProviders = ""
		case providersConfigSet:
			wantProviders = providersConfigEnv
		case trimmedConfigHomeEnv != "":
			wantProviders = filepath.Join(trimmedConfigHomeEnv, "evener", "providers.toml")
		default:
			wantProviders = filepath.Join(trimmedUserHome, ".config", "evener", "providers.toml")
		}
		if gotProviders != wantProviders || gotNoUserLayer != wantNoUserLayer {
			t.Fatalf("Paths(%q, %q) with EVENER_PROVIDERS_CONFIG=%q (set=%v) XDG_CONFIG_HOME=%q = %q/%v, want %q/%v",
				stateHome, userHome, providersConfigEnv, providersConfigSet, configHomeEnv, gotProviders, gotNoUserLayer, wantProviders, wantNoUserLayer)
		}
	})
}
