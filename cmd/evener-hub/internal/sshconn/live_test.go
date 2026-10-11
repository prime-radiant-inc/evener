package sshconn

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// TestLiveSSHAttachE2E is the spec's live test (design §7): against a
// disposable host it preflights, deploys the matching build if the host's
// version differs, attaches, initializes, and calls thread/list.
//
// It is gated by BOTH EVENER_SSH_E2E=1 and EVENER_SSH_E2E_HOST so default
// `make test` performs no ssh and needs no host, and it skips under -short. Set
// EVENER_SSH_E2E_EVENER_PATH to exercise a specific install path, and
// EVENER_SSH_E2E_BUILD_SOURCE to build from a checkout other than this one.
func TestLiveSSHAttachE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live SSH test under -short")
	}
	if os.Getenv("EVENER_SSH_E2E") != "1" {
		t.Skip("set EVENER_SSH_E2E=1 and EVENER_SSH_E2E_HOST to run the live SSH test")
	}
	dest := os.Getenv("EVENER_SSH_E2E_HOST")
	if dest == "" {
		t.Skip("set EVENER_SSH_E2E_HOST to a disposable host to run the live SSH test")
	}

	host := hostreg.Host{
		Name:       "e2e",
		SSH:        dest,
		EvenerPath: os.Getenv("EVENER_SSH_E2E_EVENER_PATH"),
	}
	reg, err := hostreg.New([]hostreg.Host{host})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	// The deploy path needs an explicit BuildSource; without it localBuild fails
	// closed, so a version-differing host would abort instead of upgrading. Default
	// to this checkout: go test runs in the package directory, four levels below
	// the root. (Not runtime.Caller: under -trimpath it names a module path, not
	// a file on disk.)
	source := os.Getenv("EVENER_SSH_E2E_BUILD_SOURCE")
	if source == "" {
		if wd, err := os.Getwd(); err == nil {
			source = filepath.Join(wd, "..", "..", "..", "..")
		}
	}
	m := New(reg, Options{Logger: t.Logf, BuildSource: source})
	defer func() { _ = m.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Ensure runs preflight -> deploy-if-needed -> restart-if-needed -> attach ->
	// initialize.
	ch, err := m.Ensure(ctx, "e2e")
	if err != nil {
		t.Fatalf("Ensure (preflight/deploy/attach/initialize): %v", err)
	}
	pf := ch.Preflight()
	t.Logf("attached: os=%s arch=%s version=%s protocol=%s", pf.OS, pf.Arch, pf.Version, pf.Protocol)

	list, err := ch.Client().ThreadList(ctx, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("thread/list over the channel: %v", err)
	}
	t.Logf("thread/list: %+v", list)
}
