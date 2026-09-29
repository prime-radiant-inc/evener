package hub

// Host-free unit tests for the shared disposable-host helpers in
// app_host_disposable_e2e_test.go. They need no ssh and no host, so they run in
// ordinary `go test ./...` and pin the mechanisms the live checks rest on: the
// launch's config-override isolation, the bounded health probe, the guard's
// actual-path resolution, and the guard's falsification hook.

import (
	"strings"
	"testing"

	"primeradiant.com/evener/cmdutil"
)

// TestHostHubLaunchScriptUnsestsConfigOverrides pins the isolation net: the
// disposable hub may not inherit EVENER_CREDENTIALS_CONFIG or
// EVENER_PROVIDERS_CONFIG, either of which outranks XDG_CONFIG_HOME and would
// let the "disposable" hub read and write the host's real provider and
// credential files. The unsets live in the launch's own `env` invocation.
func TestHostHubLaunchScriptUnsestsConfigOverrides(t *testing.T) {
	script := hostHubLaunchScript("/host/bin/evener", "/host/dir/hub.toml", "/host/dir", "127.0.0.1:19183")
	for _, want := range []string{
		"env -u EVENER_CREDENTIALS_CONFIG",
		"-u EVENER_PROVIDERS_CONFIG",
		"XDG_CONFIG_HOME=/host/dir",
		"/host/bin/evener hub",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("hostHubLaunchScript = %q, want it to contain %q; an inherited path override outranks XDG_CONFIG_HOME", script, want)
		}
	}
}

// TestHostHubHealthProbeIsBounded pins that the readiness probe cannot hang: a
// curl with no connect or total timeout stalls the 60s poll until the outer
// attach timeout, so the bound is load-bearing.
func TestHostHubHealthProbeIsBounded(t *testing.T) {
	cmd := hostHubHealthProbeCommand("http://127.0.0.1:19183/api/health")
	for _, want := range []string{"--connect-timeout 2", "--max-time 5"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("health probe = %q, want it to carry %q so a hung connection cannot stall the poll", cmd, want)
		}
	}
}

// TestHostAmbientCredentialsPathMatchesCmdutil pins that the guard hashes the
// path the PRODUCT actually resolves. The resolver mirrors cmdutil's tri-state
// credential-path rule, and the test compares it against cmdutil.CredentialsPath
// under the same environment, so a drift in either is caught.
func TestHostAmbientCredentialsPathMatchesCmdutil(t *testing.T) {
	const home = "/home/dev"
	cases := []struct {
		name                  string
		creds, providers, xdg string
	}{
		{"defaults fall to home", "", "", ""},
		{"xdg config home wins over home", "", "", "/xdg-config"},
		{"credentials override wins", "/c/creds.toml", "/c/providers.toml", "/xdg-config"},
		{"providers sibling when credentials unset", "", "/c/providers.toml", "/xdg-config"},
		{"whitespace credentials falls through", "   ", "", ""},
		{"whitespace providers falls through", "", "   ", ""},
		{"padded credentials used raw", " /p ", "", ""},
		{"whitespace xdg used raw", "", "", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("EVENER_CREDENTIALS_CONFIG", tc.creds)
			t.Setenv("EVENER_PROVIDERS_CONFIG", tc.providers)
			t.Setenv("XDG_CONFIG_HOME", tc.xdg)
			t.Setenv("HOME", home)
			want := cmdutil.CredentialsPath()
			got := hostAmbientCredentialsPath(tc.creds, tc.providers, tc.xdg, home)
			if got != want {
				t.Fatalf("hostAmbientCredentialsPath = %q, cmdutil.CredentialsPath (same env) = %q; the guard must hash the path the product resolves", got, want)
			}
		})
	}
}

// TestHostGuardedCredentialsPathHookMovesOnlyGuard pins the guard's selection:
// the host's real store by default, and the test-owned disposable store only
// under the falsification hook. An empty ambient hook value must not move it.
func TestHostGuardedCredentialsPathHookMovesOnlyGuard(t *testing.T) {
	const hook = "EVENER_SSH_E2E_PUSH_GUARD_DISPOSABLE"
	const realPath = "/home/dev/.config/evener/credentials.toml"
	const disposablePath = "/home/dev/evener-push-e2e-1/evener/credentials.toml"
	t.Setenv(hook, "")
	if got := hostGuardedCredentialsPath(hook, realPath, disposablePath); got != realPath {
		t.Fatalf("default guard = %q, want the host's real store %q", got, realPath)
	}
	t.Setenv(hook, "1")
	if got := hostGuardedCredentialsPath(hook, realPath, disposablePath); got != disposablePath {
		t.Fatalf("falsification-hook guard = %q, want the disposable store %q", got, disposablePath)
	}
}
