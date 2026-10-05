//go:build !linux && !darwin

package execenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfinedFileUnsupportedHost(t *testing.T) {
	t.Parallel()
	host := t.TempDir()
	if root, err := NewConfinedFileRoot(host, "memory/personal"); root != nil || err == nil || err.Error() != errSandboxUnsupported().Error() {
		t.Fatalf("unsupported root = %v, error = %v", root, err)
	}
	if env, err := NewConfinedFileEnvironment(host, "memory/personal"); env != nil || err == nil || err.Error() != errSandboxUnsupported().Error() {
		t.Fatalf("unsupported environment = %v, error = %v", env, err)
	}
	if _, err := os.Stat(filepath.Join(host, "memory")); !os.IsNotExist(err) {
		t.Fatalf("unsupported memory created scope: %v", err)
	}
	env := NewLocalExecutionEnvironment(host)
	defer env.Cleanup()
	if _, err := env.WriteFile("ordinary.txt", "fixture ordinary data"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(host, "ordinary.txt")); err != nil || string(data) != "fixture ordinary data" {
		t.Fatalf("ordinary write = %q, error = %v", data, err)
	}
}
