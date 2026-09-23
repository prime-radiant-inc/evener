package hub

// Regression test for the #1136 roborev Medium: the auth mutation entry points
// and hubInstancesController.Remove used to resolve the endpoint fingerprint key
// inside their exclusive lock section. Resolving it is not a read - a key file
// that is missing or unusable is repaired there, an inter-process lock and a
// write (endpointFingerprintKeyState) - so a slow repair inside credMu, and for
// Remove inside mu as well, held every listing and credential op behind it.
// That is exactly the stall List's own key resolution before its locks exists to
// avoid (app_instances_listkey_test.go).
//
// The fix resolves the key once through the resolveEndpointFingerprintKey seam
// before taking either lock and threads the result - with the error an unusable
// key produced - into verifyEndpointFingerprintWithKey / verifyFlowEndpointWithKey
// inside the locked section. The probe below swaps that seam for one that records
// the call count and, on the spot, whether the controller's locks were held:
// TryLock fails whenever any goroutine holds the lock, the probe's own goroutine
// included, so lock freedom is observed directly rather than modelled.

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/internal/credentials"
)

// tryLocker is the shape the two controller locks share here: TryLock reports
// whether the lock is free right now - false when a reader or a writer holds it,
// including one held by this goroutine - and Unlock releases the probe's own
// successful TryLock.
type tryLocker interface {
	TryLock() bool
	Unlock()
}

// namedLock pairs one watched lock with the name a failure message uses.
type namedLock struct {
	name string
	lock tryLocker
}

// keyResolutionProbe is what one installed probe observed. The seam runs
// synchronously on the goroutine under test and these fields are read only after
// that call returns, so they need no lock of their own.
type keyResolutionProbe struct {
	// calls is how many times the seam served a resolution.
	calls int
	// held names every watched lock that was held when a resolution ran.
	held []string
}

// installKeyResolutionProbe replaces the resolveEndpointFingerprintKey seam for
// the duration of the test - restoring it in t.Cleanup - with a probe that
// answers every resolution with the real key (endpointFingerprintKeyState) while
// recording whether the watched locks were held.
func installKeyResolutionProbe(t *testing.T, locks ...namedLock) *keyResolutionProbe {
	t.Helper()
	prev := resolveEndpointFingerprintKey
	probe := &keyResolutionProbe{}
	resolveEndpointFingerprintKey = func(stateDir string) ([]byte, error) {
		probe.calls++
		for _, watched := range locks {
			if watched.lock.TryLock() {
				watched.lock.Unlock()
				continue
			}
			probe.held = append(probe.held, watched.name)
		}
		return endpointFingerprintKeyState(stateDir)
	}
	t.Cleanup(func() { resolveEndpointFingerprintKey = prev })
	return probe
}

// assertResolvedBeforeTheLocks fails unless at least one resolution ran and
// every resolution the probe saw ran with every watched lock free. A resolution
// that ran with a lock held is the regression: the repair it can make would hold
// that lock while it worked, stalling whatever waits on it.
func (p *keyResolutionProbe) assertResolvedBeforeTheLocks(t *testing.T, what string) {
	t.Helper()
	if p.calls == 0 {
		t.Fatalf("%s never resolved the fingerprint key through the seam, so its lock freedom cannot be observed", what)
	}
	if len(p.held) > 0 {
		t.Fatalf("%s resolved the fingerprint key while %s held, so a key repair would stall every listing and credential op behind it", what, strings.Join(p.held, ", "))
	}
}

// TestAuth_KeyResolutionHappensBeforeTheCredentialLock pins the ApiKeySet half:
// the write resolves its key before it takes credMu exclusively, so the
// resolution - and any repair it makes - runs while no listing or credential op
// is blocked behind the lock. The write carries the endpoint the pane showed the
// user, so the resolution's key is also the one the assertion inside the lock is
// checked against.
func TestAuth_KeyResolutionHappensBeforeTheCredentialLock(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	stateDir := t.TempDir()
	store, err := credentials.LoadStore(filepath.Join(stateDir, "credentials.toml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	c := newHubAuthControllerWithStore(stateDir, store)
	attachTestRegistry(t, c)

	// The assertion a client of the credentials pane sends, asked before the
	// probe is installed so this cannot be mistaken for one of its resolutions.
	shown := c.endpointFingerprintFor("anthropic")
	if shown == "" {
		t.Fatal("fixture drift: the hub must be able to key anthropic's endpoint fingerprint")
	}

	probe := installKeyResolutionProbe(t, namedLock{name: "the credential lock (credMu)", lock: &c.credMu})
	got, err := c.ApiKeySet(appwire.AuthApiKeySetParams{
		Provider:                    "anthropic",
		Value:                       "sk-ant-key-order",
		ExpectedEndpointFingerprint: shown,
	})
	if err != nil {
		t.Fatalf("ApiKeySet: %v", err)
	}
	if got.ActiveSource != "store" {
		t.Fatalf("ActiveSource = %q, want store: the write must have landed", got.ActiveSource)
	}
	probe.assertResolvedBeforeTheLocks(t, "ApiKeySet")
}

// TestAuth_ConditionalSetResolvesTheKeyBeforeTheCredentialLock pins the
// ApiKeyConditionalSet half of the same rule. The revision fence that call checks
// is keyed with the hub's endpoint-fingerprint key, and that key is resolved
// before credentialWriteConditional takes credMu. Resolving it is not a read - a
// missing or unusable key file is repaired there, an inter-process lock and a
// write (endpointFingerprintKeyState) - so a resolution inside the locked section
// would hold every listing and credential op behind it. ApiKeySet and the
// instances listing are already pinned for this; the conditional set is the third
// caller that resolves the seam, and nothing covered it.
func TestAuth_ConditionalSetResolvesTheKeyBeforeTheCredentialLock(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	stateDir := t.TempDir()
	store, err := credentials.LoadStore(filepath.Join(stateDir, "credentials.toml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	c := newHubAuthControllerWithStore(stateDir, store)
	attachTestRegistry(t, c)

	probe := installKeyResolutionProbe(t, namedLock{name: "the credential lock (credMu)", lock: &c.credMu})
	resp, err := c.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{
		Provider: "anthropic",
		Value:    "sk-ant-conditional-key-order",
	})
	if err != nil {
		t.Fatalf("ApiKeyConditionalSet: %v", err)
	}
	// The write landed, so the call really drove its fence and its set rather than
	// skipping before either: a skip would leave the resolution assertion below
	// saying nothing about the path under test.
	if resp.Action != appwire.ApiKeyConditionalSetActionAdded {
		t.Fatalf("Action = %q, want %q (reason %q): the conditional set must reach its fence and write", resp.Action, appwire.ApiKeyConditionalSetActionAdded, resp.Reason)
	}
	if resp.Status.ActiveSource != "store" {
		t.Fatalf("Status.ActiveSource = %q, want store: the write must have landed", resp.Status.ActiveSource)
	}
	probe.assertResolvedBeforeTheLocks(t, "ApiKeyConditionalSet")
}

// TestInstances_KeyResolutionHappensBeforeBothLocks pins the Remove half: its
// exclusive section holds mu and credMu at once and keeps the credential
// cleanup, the providers.toml rewrite and the reload inside it, so a key repair
// there would stall listings on either controller. The removal carries the
// fingerprint of the row the client listed, so the threaded key is exercised the
// way ApiKeySet's is.
func TestInstances_KeyResolutionHappensBeforeBothLocks(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{
		Name:    "work",
		Base:    "openai",
		BaseURL: "https://work.example.test/v1",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The fingerprint of the row the remove confirmation names, read from the
	// listing before the probe replaces the seam List resolves through.
	shown := entry(t, f.ctl.List(), "work").EndpointFingerprint
	if shown == "" {
		t.Fatal("fixture drift: the hub must be able to key work's endpoint fingerprint")
	}

	probe := installKeyResolutionProbe(t,
		namedLock{name: "the instances lock (mu)", lock: &f.ctl.mu},
		namedLock{name: "the credential lock (credMu)", lock: &f.ctl.auth.credMu},
	)
	if err := f.ctl.Remove(appwire.InstanceRemoveParams{
		Name:                        "work",
		ExpectedEndpointFingerprint: shown,
	}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	probe.assertResolvedBeforeTheLocks(t, "Remove")
	if row, ok := findInstanceRow(f.ctl.List(), "work"); ok {
		t.Fatalf("the removed instance is still listed: %+v", row)
	}
}
