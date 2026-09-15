package hub

// Regression tests for the #1136 roborev Medium on the endpoint-fingerprint
// key's mode check: readEndpointFingerprintKey used to require the key file to
// be exactly 0600, so a file an operator had tightened to 0400 was judged
// corrupt - the hub repaired it, rotating the key and invalidating every
// endpoint fingerprint clients already held. The check now asks whether the
// mode is owner-only with owner read set (endpointFingerprintKeyModeAccepted),
// which accepts 0600 or stricter and still refuses a mode another local user
// can read.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
)

// requireJudgedKeyModes skips where the platform does not report POSIX
// permission bits: the mode seam answers unjudged there (fileowner_other.go),
// so the check under test never runs and no mode a test writes on such a host
// can exercise it.
func requireJudgedKeyModes(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	if _, judged := endpointFingerprintKeyMode(info); !judged {
		t.Skip("this platform does not report POSIX permission bits; the key-mode check is skipped there")
	}
}

// TestInstances_KeyModeAcceptsAnOwnerOnlyKeyAt0400: tightening the key file to
// 0400 makes it no less secret than the 0600 the hub writes, so the hub has to
// read it as its own key. Refusing it would send the read through the repair,
// which rotates the key and stops every fingerprint a client was already shown
// from matching.
func TestInstances_KeyModeAcceptsAnOwnerOnlyKeyAt0400(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	keyPath := filepath.Join(f.stateDir, endpointFingerprintKeyFile)

	// The first listing publishes the hub's own key there, which is what the
	// operator then tightens.
	before := entry(t, f.ctl.List(), "work").EndpointFingerprint
	if before == "" {
		t.Fatal("fixture drift: the endpoint must be fingerprintable here")
	}
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", endpointFingerprintKeyFile, err)
	}
	if len(keyBefore) == 0 {
		t.Fatalf("%s is empty, want the key the listing just published", endpointFingerprintKeyFile)
	}
	requireJudgedKeyModes(t, keyPath)

	if err := os.Chmod(keyPath, 0o400); err != nil {
		t.Fatalf("Chmod(%s, 0400): %v", endpointFingerprintKeyFile, err)
	}

	after := entry(t, f.ctl.List(), "work").EndpointFingerprint
	if after != before {
		t.Fatalf("EndpointFingerprint = %q after the key was tightened to 0400, want the %q clients already hold; the hub rotated a key it must accept", after, before)
	}
	keyAfter, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", endpointFingerprintKeyFile, err)
	}
	if !bytes.Equal(keyBefore, keyAfter) {
		t.Fatalf("the key file was rewritten under an accepted 0400 mode: read %q, want the key left in place %q", keyAfter, keyBefore)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Stat(%s): %v", endpointFingerprintKeyFile, err)
	}
	if perm := info.Mode().Perm(); perm != 0o400 {
		t.Fatalf("%s is %04o after the listing, want the 0400 an accepted mode leaves alone", endpointFingerprintKeyFile, perm)
	}
}

// TestInstances_KeyModeRotatesAKeyOthersCanRead is the control on the same
// judgement: the fingerprints' guarantee is that only a holder of the key can
// recompute them, so a key file a group or other member can read must not be
// served silently. This is what the hub does today with a 0644 file: it refuses
// it, repairs the path with a fresh 0600 key, and the fingerprints - derived
// from the key - change. The exposed key stops describing anything.
func TestInstances_KeyModeRotatesAKeyOthersCanRead(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	keyPath := filepath.Join(f.stateDir, endpointFingerprintKeyFile)
	exposed := []byte("a-key-another-local-user-could-read")
	if err := os.WriteFile(keyPath, exposed, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", endpointFingerprintKeyFile, err)
	}
	requireJudgedKeyModes(t, keyPath)

	// The key is readable by its owner only here, so this fingerprint is the
	// exposed key's.
	keyedByExposed := entry(t, f.ctl.List(), "work").EndpointFingerprint
	if keyedByExposed == "" {
		t.Fatal("fixture drift: the endpoint must be fingerprintable here")
	}

	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("Chmod(%s, 0644): %v", endpointFingerprintKeyFile, err)
	}

	served := entry(t, f.ctl.List(), "work").EndpointFingerprint
	if served == keyedByExposed {
		t.Fatal("the fingerprint still came from a key file another local user can read; the hub kept serving a key that may have leaked")
	}
	if served == "" {
		t.Fatal("a key file the hub repairs should leave it serving fingerprints")
	}
	rotated, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", endpointFingerprintKeyFile, err)
	}
	if bytes.Equal(rotated, exposed) {
		t.Fatal("the exposed key is still on disk under a readable mode: a key others can read has to be rotated, not used")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Stat(%s): %v", endpointFingerprintKeyFile, err)
	}
	assertKeyFileMode0600(t, info, "the repaired key file")
}
