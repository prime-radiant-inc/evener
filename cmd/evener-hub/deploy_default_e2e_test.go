package hub

import (
	"debug/buildinfo"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// TestDefaultDeploySourceIsTheHubExecutableE2E is the default's integration
// check, and the answer to the one thing a stubbed seam cannot show: that the
// binary a production hub is run from is accepted as its own deploy artifact.
// Every other test here — and every test in sshconn — reaches the default through
// hubExecutable stubs, necessarily, because the process running a test is the
// test binary and not an evener build. None of them proves the identity a real
// hub presents. This one runs the real thing: it starts a hub from the shared
// ./cmd/evener build with no deploy flags set — the production shape, `evener
// hub` — and reads the startup line that records the source the hub adopted.
//
// It is what keeps the default honest in the direction the stubs cannot cover:
// the artifact the hub adopts must be the executable it is running as, whose
// main package is the evener runtime (asserted here against the built file, not
// against a fixture), and the hub must actually adopt it rather than silently
// starting unwired. A packaging change that gave the hub a non-evener executable
// of its own would fail this test's line assertion at startup, not in production.
func TestDefaultDeploySourceIsTheHubExecutableE2E(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: runs a hub to read the deploy source it adopts at startup")
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	bin := filepath.Join(liveStackBinaries(t, repoRoot), "evener")

	// The identity the artifact check reads, taken from the real binary rather
	// than a stub: a hub run from this file presents exactly this buildinfo.
	info, err := buildinfo.ReadFile(bin)
	if err != nil {
		t.Fatalf("read buildinfo of %s: %v", bin, err)
	}
	if info.Path != evenerMainPackage {
		t.Fatalf("the evener build's main package = %q, want %q", info.Path, evenerMainPackage)
	}

	provider, err := fakellm.New()
	if err != nil {
		t.Fatalf("start fake provider: %v", err)
	}
	t.Cleanup(provider.Close)
	// The hub is started from the binary this test asserts about, passed
	// explicitly: the identity under test is an input here, not whatever binary
	// the stack helper happens to pick. No deploy flags are passed — a hub
	// started without them is the shape production runs.
	stack := startHubStackOnProviderWithEvener(t, fmt.Sprintf(`
default = "fake"

[providers.fake]
base     = "openai-compatible"
base_url = %q
api_key  = "fakellm-not-a-secret"
`, provider.BaseURL()), "fake/"+fakellm.ModelID, bin)

	body, err := os.ReadFile(filepath.Join(stack.home, "hub.log"))
	if err != nil {
		t.Fatalf("read the hub log: %v", err)
	}
	want, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatalf("canonicalize %s: %v", bin, err)
	}
	line := "[hub] deploy path: -deploy-binary " + want + " (default: this hub's own executable)"
	if !strings.Contains(string(body), line) {
		t.Fatalf("a hub run from the evener binary did not adopt its own executable as the deploy default; want %q in:\n%s", line, body)
	}
}
