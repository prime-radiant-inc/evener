package identifier

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExistingDirectory_StatError covers the branch where os.Stat returns an
// error (path does not exist).
func TestExistingDirectory_StatError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	err := existingDirectory(missing)
	if err == nil {
		t.Fatal("expected error for non-existent path")
	}
}

// TestMustDomainID_PanicOnFailure covers the panic branch in mustDomainID
// (domains.go:44-45) where the underlying ID generator returns an error.
func TestMustDomainID_PanicOnFailure(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic from mustDomainID with failing generator")
		}
	}()
	mustDomainID(func() (string, error) { return "", os.ErrInvalid })
}
