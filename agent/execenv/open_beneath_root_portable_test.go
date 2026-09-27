package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRegularBeneathRootNoFollowPortable_RefusesSymlinkedFirstComponent(t *testing.T) {
	root := t.TempDir()
	attackerBucket := t.TempDir()
	attackerPath := filepath.Join(attackerBucket, "sessions", "output.log")
	if err := os.MkdirAll(filepath.Dir(attackerPath), 0o700); err != nil {
		t.Fatalf("create attacker tree: %v", err)
	}
	if err := os.WriteFile(attackerPath, []byte("attacker\n"), 0o600); err != nil {
		t.Fatalf("write attacker output: %v", err)
	}
	bucket := filepath.Join(root, "bucket")
	if err := os.Symlink(attackerBucket, bucket); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	path := filepath.Join(bucket, "sessions", "output.log")

	f, err := openRegularBeneathRootNoFollowPortable(path, root, func(path, _ string) (*os.File, error) {
		return os.Open(path)
	})
	if f != nil {
		_ = f.Close()
		t.Fatal("portable fallback followed a symlinked first component")
	}
	if !errors.Is(err, ErrNonTraversableRoot) {
		t.Fatalf("error = %v, want ErrNonTraversableRoot", err)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Path != bucket {
		t.Fatalf("error = %v, want PathError naming first component %q", err, bucket)
	}
}

func TestOpenRegularBeneathRootNoFollowPortable_RefusesNonDirectoryFirstComponent(t *testing.T) {
	root := t.TempDir()
	component := filepath.Join(root, "bucket")
	if err := os.WriteFile(component, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatalf("write first component: %v", err)
	}
	path := filepath.Join(component, "sessions", "output.log")

	f, err := openRegularBeneathRootNoFollowPortable(path, root, func(path, _ string) (*os.File, error) {
		return os.Open(path)
	})
	if f != nil {
		_ = f.Close()
		t.Fatal("portable fallback accepted a non-directory first component")
	}
	if !errors.Is(err, ErrNonTraversableRoot) {
		t.Fatalf("error = %v, want ErrNonTraversableRoot", err)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Path != component {
		t.Fatalf("error = %v, want PathError naming first component %q", err, component)
	}
}
