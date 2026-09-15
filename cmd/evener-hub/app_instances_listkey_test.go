package hub

// Regression tests for the #1136 roborev low: List used to resolve (and, when
// the key file was missing or unusable, repair) the endpoint fingerprint key
// inside its credential-lock section, so a pure read could block every
// credential writer on a cross-process lock and write files while holding the
// read lock. The fix resolves the key once, before either lock is taken, and
// passes it to every row.
//
// The tests below swap the resolveEndpointFingerprintKey seam for a counting
// probe. The probe records whether it ran while no reader held credMu (a
// TryLock on the write side fails exactly when any reader or writer holds the
// lock, so it is an uncontended-lock probe) and the listing is then asserted
// on the count, the lock observation and the rows it produced.

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// fingerprintableInstances creates two authored instances whose endpoints
// resolve, so a listing builds at least two fingerprint-bearing rows. The
// fixture state root starts empty, so the first resolution also exercises the
// create-the-key path.
func fingerprintableInstances(t *testing.T, f *instancesFixture) []string {
	t.Helper()
	names := []string{"work", "home"}
	for _, name := range names {
		if err := f.ctl.Create(appwire.InstanceCreateParams{
			Name:    name,
			Base:    "openai",
			BaseURL: "https://" + name + ".example.test/v1",
		}); err != nil {
			t.Fatalf("Create(%q): %v", name, err)
		}
	}
	return names
}

// TestInstances_ListResolvesTheFingerprintKeyOnceBeforeTheCredentialLock pins
// the shape of the fix: a listing resolves the key through the seam exactly
// once, and that resolution happens before List takes the credential read
// lock. It also asserts the rows were actually keyed, so a resolution that
// happened but was not reused would still be caught.
func TestInstances_ListResolvesTheFingerprintKeyOnceBeforeTheCredentialLock(t *testing.T) {
	f := newInstancesFixture(t, nil)
	names := fingerprintableInstances(t, f)

	var (
		calls        int
		ranUnderLock bool
	)
	prev := resolveEndpointFingerprintKey
	resolveEndpointFingerprintKey = func(stateDir string) ([]byte, error) {
		calls++
		// TryLock succeeds exactly when the lock is uncontended: it fails when
		// any reader (including List itself) or writer holds credMu.
		if f.ctl.auth.credMu.TryLock() {
			f.ctl.auth.credMu.Unlock()
		} else {
			ranUnderLock = true
		}
		return endpointFingerprintKeyState(stateDir)
	}
	t.Cleanup(func() { resolveEndpointFingerprintKey = prev })

	resp := f.ctl.List()

	if calls != 1 {
		t.Fatalf("resolveEndpointFingerprintKey ran %d times for one listing, want exactly 1", calls)
	}
	if ranUnderLock {
		t.Fatal("List resolved the fingerprint key while a reader held credMu, so a key repair could stall every credential writer")
	}
	for _, name := range names {
		if fp := entry(t, resp, name).EndpointFingerprint; len(fp) != 64 {
			t.Fatalf("instance %q EndpointFingerprint = %q, want the listing's one resolved key reused for every row", name, fp)
		}
	}
}

// TestInstances_ListKeyDiagnosticReportsAFailedResolution pins the other half
// of resolving the key once: the listing's key diagnostic is derived from that
// single resolution's error, and a resolution that yields no key leaves every
// row's fingerprint empty.
func TestInstances_ListKeyDiagnosticReportsAFailedResolution(t *testing.T) {
	f := newInstancesFixture(t, nil)
	names := fingerprintableInstances(t, f)

	var calls int
	prev := resolveEndpointFingerprintKey
	resolveEndpointFingerprintKey = func(string) ([]byte, error) {
		calls++
		return nil, errors.New("key file is unusable")
	}
	t.Cleanup(func() { resolveEndpointFingerprintKey = prev })

	resp := f.ctl.List()

	if calls != 1 {
		t.Fatalf("resolveEndpointFingerprintKey ran %d times for one listing, want exactly 1", calls)
	}
	for _, name := range names {
		if fp := entry(t, resp, name).EndpointFingerprint; fp != "" {
			t.Fatalf("instance %q EndpointFingerprint = %q, want it omitted when the listing has no key", name, fp)
		}
	}
	var found []string
	for _, d := range resp.Diagnostics {
		if strings.Contains(d, endpointFingerprintKeyFile) {
			found = append(found, d)
		}
	}
	if len(found) != 1 {
		t.Fatalf("Diagnostics = %v, want exactly one entry naming %s", resp.Diagnostics, endpointFingerprintKeyFile)
	}
	if !strings.Contains(found[0], "key file is unusable") {
		t.Fatalf("diagnostic = %q, want it to carry the resolution error", found[0])
	}
}
