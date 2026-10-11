package hub

import (
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// sharedCodexAuthSchemePath is the checked-in value the hub's own Codex gate
// and the web UI's sign-in gate are BOTH pinned to, next to this file. Its
// header states the whole contract; the two tests that read it are
// TestCodexAuthSchemePinMatchesRegistry here and oauthFlow.test.ts's pin
// assertion on the frontend side (which reads it through the relative path
// from cmd/evener-hub/frontend/src/panes/settings/sections/credentials).
const sharedCodexAuthSchemePath = "codex_auth_scheme.txt"

// readSharedCodexAuthScheme parses the checked-in pin: exactly one non-empty,
// non-comment line, the same shape as host_request_methods.txt.
func readSharedCodexAuthScheme(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(sharedCodexAuthSchemePath)
	if err != nil {
		t.Fatalf("read %s: %v (the cross-language pin is only real while both sides can read it)", sharedCodexAuthSchemePath, err)
	}
	var schemes []string
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		schemes = append(schemes, line)
	}
	if len(schemes) != 1 {
		t.Fatalf("%s names %d schemes, want exactly 1: %q", sharedCodexAuthSchemePath, len(schemes), schemes)
	}
	return schemes[0]
}

// TestCodexAuthSchemePinMatchesRegistry pins the CROSS-LANGUAGE half of the
// Codex sign-in gate. The hub accepts an instance for the device-code flow
// when its resolved transport auth is registry.AuthOAuthOpenAICodex
// (app_auth.go's instanceIsCodex), and the web UI offers "Sign in on host" for
// the scheme it spells locally (oauthFlow.ts's CODEX_AUTH_SCHEME). Nothing tied
// the two spellings together, so renaming the Go constant - or editing the
// frontend literal - left the other end green while the browser offered a
// sign-in the host would refuse, or withheld one it would accept. Both now read
// the checked-in value at codex_auth_scheme.txt; this asserts the Go constant
// still IS it.
func TestCodexAuthSchemePinMatchesRegistry(t *testing.T) {
	if got := readSharedCodexAuthScheme(t); got != registry.AuthOAuthOpenAICodex {
		t.Errorf("%s names %q but llm/registry/types.go's AuthOAuthOpenAICodex is %q; the web UI's sign-in gate and the hub's own gate have diverged",
			sharedCodexAuthSchemePath, got, registry.AuthOAuthOpenAICodex)
	}
}
