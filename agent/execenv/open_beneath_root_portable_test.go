package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestOpenRegularBeneathRootNoFollowPortable_RefusesEscapingPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(filepath.Dir(root), "outside.log")
	openerCalls := 0
	f, err := openRegularBeneathRootNoFollowPortable(path, root, func(_, _ string) (*os.File, error) {
		openerCalls++
		return nil, nil
	})
	if f != nil {
		_ = f.Close()
	}
	if openerCalls != 0 {
		t.Fatalf("opener calls = %d, want 0 for escaping path", openerCalls)
	}
	if err == nil || !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("error = %v, want legible root-escape refusal", err)
	}
}

func TestOpenRegularBeneathRootNoFollowPortable_RefusesRootPath(t *testing.T) {
	root := t.TempDir()
	openerCalls := 0
	f, err := openRegularBeneathRootNoFollowPortable(root, root, func(_, _ string) (*os.File, error) {
		openerCalls++
		return nil, nil
	})
	if f != nil {
		_ = f.Close()
	}
	if openerCalls != 0 {
		t.Fatalf("opener calls = %d, want 0 when path is root", openerCalls)
	}
	if err == nil || !strings.Contains(err.Error(), "root directory, not a file") {
		t.Fatalf("error = %v, want root-is-not-file refusal", err)
	}
}

func TestOpenRegularBeneathRootNoFollowPortable_RefusesRelError(t *testing.T) {
	root := t.TempDir()
	path := "relative-output.log"
	if _, err := filepath.Rel(root, path); err == nil {
		t.Fatalf("test setup: filepath.Rel(%q, %q) unexpectedly succeeded", root, path)
	}
	openerCalls := 0
	f, err := openRegularBeneathRootNoFollowPortable(path, root, func(_, _ string) (*os.File, error) {
		openerCalls++
		return nil, nil
	})
	if f != nil {
		_ = f.Close()
	}
	if openerCalls != 0 {
		t.Fatalf("opener calls = %d, want 0 after filepath.Rel error", openerCalls)
	}
	if err == nil || !strings.Contains(err.Error(), "beneath") {
		t.Fatalf("error = %v, want legible filepath.Rel refusal", err)
	}
}

func TestOpenRegularBeneathRootNoFollowPortable_OpensDirectChild(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "output.log")
	if err := os.WriteFile(path, []byte("payload\n"), 0o600); err != nil {
		t.Fatalf("write direct child: %v", err)
	}
	openerCalls := 0
	f, err := openRegularBeneathRootNoFollowPortable(path, root, func(gotPath, gotRoot string) (*os.File, error) {
		openerCalls++
		if gotPath != path || gotRoot != root {
			t.Fatalf("opener args = (%q, %q), want (%q, %q)", gotPath, gotRoot, path, root)
		}
		return os.Open(gotPath)
	})
	if err != nil {
		t.Fatalf("open direct child: %v", err)
	}
	defer func() { _ = f.Close() }()
	if openerCalls != 1 {
		t.Fatalf("opener calls = %d, want 1 for direct child", openerCalls)
	}
}
