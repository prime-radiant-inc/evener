//go:build unix

package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestInstallLockReadOnlyDirReportsSentinel proves a managed directory that
// cannot host a lock file is reported as ErrInstallDirReadOnly — the one
// failure the restart path may continue past, with its digest re-verify.
func TestInstallLockReadOnlyDirReportsSentinel(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a 0555 directory")
	}
	dir := filepath.Join(t.TempDir(), "share")
	if err := os.MkdirAll(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, err := acquireInstallLockCtx(context.Background(), dir)
	if !errors.Is(err, ErrInstallDirReadOnly) {
		t.Fatalf("err = %v, want ErrInstallDirReadOnly", err)
	}
}
