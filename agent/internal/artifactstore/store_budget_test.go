package artifactstore

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestStoreBudgetRejectsWithoutEvicting(t *testing.T) {
	t.Parallel()
	s, err := newStore(t.TempDir(), budget{maxBytes: 10, maxRefs: 3})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	first, err := s.Put([]byte("012345"))
	if err != nil {
		t.Fatalf("Put 6 bytes: %v", err)
	}
	if _, err := s.Put([]byte("01234")); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("Put over byte budget = %v, want ErrBudgetExceeded", err)
	}

	opened, err := s.Open(first)
	if err != nil {
		t.Fatalf("Open issued ref after budget rejection: %v", err)
	}
	got, readErr := io.ReadAll(opened)
	_ = opened.Close()
	if readErr != nil {
		t.Fatalf("read issued ref after budget rejection: %v", readErr)
	}
	if !bytes.Equal(got, []byte("012345")) {
		t.Fatalf("issued ref after budget rejection = %q", got)
	}

	second, err := s.Put([]byte("0123"))
	if err != nil {
		t.Fatalf("rejected Put charged the byte budget: %v", err)
	}
	third, err := s.Put(nil)
	if err != nil {
		t.Fatalf("Put zero-byte artifact: %v", err)
	}
	if _, err := s.Put(nil); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("Put over artifact-count budget = %v, want ErrBudgetExceeded", err)
	}

	for _, ref := range []string{first, second, third} {
		opened, err := s.Open(ref)
		if err != nil {
			t.Fatalf("Open(%q) after budget exhaustion: %v", ref, err)
		}
		_ = opened.Close()
	}
}

func TestNewStoreDirectoryNamesOwner(t *testing.T) {
	t.Parallel()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	prefix := artifactDirPrefix + artifactDirPIDMarker + strconv.Itoa(os.Getpid()) + "-"
	if name := filepath.Base(s.dir); !strings.HasPrefix(name, prefix) {
		t.Fatalf("store directory %q does not name owner prefix %q", name, prefix)
	}
}

func TestStoreOwnerPID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		pid  int
		ok   bool
	}{
		{name: "evener-artifacts-pid-123-456", pid: 123, ok: true},
		{name: "evener-artifacts-123456789", ok: false},
		{name: "evener-artifacts-pid-", ok: false},
		{name: "evener-artifacts-pid--x", ok: false},
		{name: "evener-artifacts-pid-abc-x", ok: false},
		{name: "evener-artifacts-pid-0-x", ok: false},
	}
	for _, test := range tests {
		pid, ok := storeOwnerPID(test.name)
		if ok != test.ok || (ok && pid != test.pid) {
			t.Errorf("storeOwnerPID(%q) = (%d, %v), want (%d, %v)", test.name, pid, ok, test.pid, test.ok)
		}
	}
}

func TestStoreLeavesUnmarkedAndMalformedDirectories(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	keep := []string{
		"evener-artifacts-123456789",
		"evener-artifacts-pid-notanumber-x",
		"evener-artifacts-pid-0-x",
	}
	for _, name := range keep {
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "data"), []byte("keep"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	s, err := New(base)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(base, name)); err != nil {
			t.Fatalf("store creation removed unprovable directory %s: %v", name, err)
		}
	}
}

const (
	reclaimHelperEnv = "ARTIFACTSTORE_RECLAIM_HELPER"
	reclaimBaseEnv   = "ARTIFACTSTORE_RECLAIM_BASE"
)

func TestStoreReclaimsDeadRootButKeepsLiveRoot(t *testing.T) {
	if os.Getenv(reclaimHelperEnv) == "1" {
		store, err := New(os.Getenv(reclaimBaseEnv))
		if err != nil {
			os.Exit(2)
		}
		if _, err := store.Put([]byte("abandoned")); err != nil {
			os.Exit(3)
		}
		// Deliberately no Close: an abnormal root exit leaves the directory.
		os.Exit(0)
	}

	base := t.TempDir()
	live, err := New(base)
	if err != nil {
		t.Fatalf("create live store: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	cmd := exec.Command(os.Args[0], "-test.run=^TestStoreReclaimsDeadRootButKeepsLiveRoot$")
	cmd.Env = append(os.Environ(), reclaimHelperEnv+"=1", reclaimBaseEnv+"="+base)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v\n%s", err, out)
	}

	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read base: %v", err)
	}
	var dead []string
	for _, entry := range entries {
		path := filepath.Join(base, entry.Name())
		if entry.IsDir() && path != live.dir {
			dead = append(dead, path)
		}
	}
	if len(dead) != 1 {
		t.Fatalf("abandoned store directories = %v, want one", dead)
	}

	fresh, err := New(base)
	if err != nil {
		t.Fatalf("create reclaiming store: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	if _, err := os.Stat(dead[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dead root directory survived reclamation: %v", err)
	}
	if _, err := os.Stat(live.dir); err != nil {
		t.Fatalf("reclamation removed a live root: %v", err)
	}
}
