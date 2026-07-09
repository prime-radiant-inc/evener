package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionTmpLifecycle(t *testing.T) {
	base := t.TempDir()
	s, err := NewSessionTmp(base)
	if err != nil {
		t.Fatalf("NewSessionTmp: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(s.Dir), sessionTmpPrefix) {
		t.Errorf("session tmp %q must carry the serf-sandbox prefix", s.Dir)
	}
	if fi, err := os.Stat(s.Dir); err != nil || !fi.IsDir() {
		t.Fatalf("session tmp must be a real directory: %v", err)
	}
	// It is writable scratch.
	if err := os.WriteFile(filepath.Join(s.Dir, "scratch"), []byte("x"), 0o600); err != nil {
		t.Fatalf("session tmp must be writable: %v", err)
	}
	if err := s.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Errorf("Cleanup must remove the session tmp, stat err = %v", err)
	}
}

func TestSessionTmpAgeSweepsOnlyStaleSerfDirs(t *testing.T) {
	base := t.TempDir()

	stale := filepath.Join(base, sessionTmpPrefix+"crashed")
	fresh := filepath.Join(base, sessionTmpPrefix+"live")
	foreign := filepath.Join(base, "not-serf-keepme")
	for _, d := range []string{stale, fresh, foreign} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	// An old FOREIGN dir must survive too — the sweep only touches serf's prefix.
	if err := os.Chtimes(foreign, old, old); err != nil {
		t.Fatal(err)
	}

	s, err := NewSessionTmp(base)
	if err != nil {
		t.Fatalf("NewSessionTmp: %v", err)
	}
	defer s.Cleanup() //nolint:errcheck

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale crashed-session dir must be swept, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a fresh session dir must NOT be swept: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("a non-serf dir must never be swept, even when old: %v", err)
	}
}
