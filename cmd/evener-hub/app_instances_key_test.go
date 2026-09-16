package hub

// Tests for the endpoint-fingerprint key's publication on filesystems that
// cannot hard-link. See publishFreshEndpointFingerprintKey.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// stubLinkUnavailable stands in for a filesystem without hard links (FAT/exFAT,
// some FUSE/SMB mounts): every publish attempt fails with an unsupported error
// that is not os.ErrExist, which is what such a filesystem reports. The real
// primitive is restored when the test ends.
func stubLinkUnavailable(t *testing.T) {
	t.Helper()
	original := endpointFingerprintKeyLink
	endpointFingerprintKeyLink = func(string, string) error {
		return &os.LinkError{Op: "link", Old: "key.tmp", New: endpointFingerprintKeyFile, Err: errors.ErrUnsupported}
	}
	t.Cleanup(func() { endpointFingerprintKeyLink = original })
}

// TestInstances_EndpointFingerprintKeyPublishesWithoutHardLinks: a key that
// cannot be published is a hub whose fingerprint guards refuse every credential
// write, so the publish has to land the key by another atomic step where linking
// is unavailable.
func TestInstances_EndpointFingerprintKeyPublishesWithoutHardLinks(t *testing.T) {
	stubLinkUnavailable(t)
	dir := t.TempDir()
	path := filepath.Join(dir, endpointFingerprintKeyFile)

	key, err := repairEndpointFingerprintKey(path)
	if err != nil {
		t.Fatalf("repairEndpointFingerprintKey without hard links: %v", err)
	}
	if len(key) == 0 {
		t.Fatal("the fallback published no key")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if !bytes.Equal(raw, key) {
		t.Fatalf("the published key is %q, want the repaired key %q", raw, key)
	}
	read, err := endpointFingerprintKeyState(dir)
	if err != nil {
		t.Fatalf("endpointFingerprintKeyState: %v", err)
	}
	if !bytes.Equal(read, key) {
		t.Fatalf("the key path yields %q, want %q", read, key)
	}
}

// TestInstances_EndpointFingerprintKeyWithoutHardLinksKeepsAUsableKey: the
// fallback keeps "a usable key is never replaced" - a key already at the path
// (another hub's, published before this repair) is adopted, not clobbered.
func TestInstances_EndpointFingerprintKeyWithoutHardLinksKeepsAUsableKey(t *testing.T) {
	stubLinkUnavailable(t)
	dir := t.TempDir()
	path := filepath.Join(dir, endpointFingerprintKeyFile)
	published := []byte("a-key-another-hub-already-published")
	if err := os.WriteFile(path, published, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}

	key, err := repairEndpointFingerprintKey(path)
	if err != nil {
		t.Fatalf("repairEndpointFingerprintKey: %v", err)
	}
	if !bytes.Equal(key, published) {
		t.Fatalf("repair returned %q, want the usable key already at the path", key)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if !bytes.Equal(raw, published) {
		t.Fatalf("the fallback replaced a usable key: %q", raw)
	}
}
