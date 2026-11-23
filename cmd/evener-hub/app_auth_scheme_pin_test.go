package hub

import (
	"os"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// sharedDeviceOAuthSchemesPath is the checked-in value set the hub's own
// device-code gates and the web UI's sign-in gate are BOTH pinned to, next to
// this file. Its header states the whole contract; the two tests that read it
// are TestDeviceOAuthSchemesPinMatchesRegistry here and oauthFlow.test.ts's pin
// assertion on the frontend side (which reads it through the relative path
// from cmd/evener-hub/frontend/src/panes/settings/sections/credentials).
const sharedDeviceOAuthSchemesPath = "device_oauth_auth_schemes.txt"

// readSharedDeviceOAuthSchemes parses the checked-in pin: exactly two
// non-empty, non-comment lines, the same shape as host_request_methods.txt.
func readSharedDeviceOAuthSchemes(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(sharedDeviceOAuthSchemesPath)
	if err != nil {
		t.Fatalf("read %s: %v (the cross-language pin is only real while both sides can read it)", sharedDeviceOAuthSchemesPath, err)
	}
	var schemes []string
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		schemes = append(schemes, line)
	}
	if len(schemes) != 2 {
		t.Fatalf("%s names %d schemes, want exactly 2: %q", sharedDeviceOAuthSchemesPath, len(schemes), schemes)
	}
	return schemes
}

// TestDeviceOAuthSchemesPinMatchesRegistry pins the CROSS-LANGUAGE half of the
// device-code sign-in gates. The hub accepts an instance for the device-code
// flow when its resolved transport auth is registry.AuthOAuthOpenAICodex
// (app_auth.go's instanceIsCodex) or registry.AuthOAuthGitHubCopilot
// (instanceIsCopilot), and the web UI offers "Sign in on host" for the
// schemes it spells locally (oauthFlow.ts's DEVICE_OAUTH_AUTH_SCHEMES). Nothing
// tied the spellings together, so renaming a Go constant - or editing a
// frontend literal - left the other end green while the browser offered a
// sign-in the host would refuse, or withheld one it would accept. Both now
// read the checked-in values at device_oauth_auth_schemes.txt; this asserts
// the Go constants still ARE that set.
func TestDeviceOAuthSchemesPinMatchesRegistry(t *testing.T) {
	got := readSharedDeviceOAuthSchemes(t)
	want := []string{registry.AuthOAuthGitHubCopilot, registry.AuthOAuthOpenAICodex}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s names %q but the hub's device-code schemes are %q; the web UI's sign-in gate and the hub's own gates have diverged",
			sharedDeviceOAuthSchemesPath, got, want)
	}
}
