package rvreg

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/rendezvous"
)

func TestRegistrationFailureAndNoopBranches(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := &Registration{}
	if err := reg.Register(filepath.Join(blocked, "run"), rendezvous.Entry{PID: 1}); err == nil {
		t.Fatal("expected register error")
	}
	if err := reg.Remove(); err != nil {
		t.Fatalf("unregistered Remove()=%v", err)
	}

	runDir := t.TempDir()
	if err := reg.Register(runDir, rendezvous.Entry{PID: 2}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(runDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runDir, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reg.Remove(); err == nil {
		t.Fatal("expected remove error")
	}
}

func FuzzRegistrationSessionID(f *testing.F) {
	f.Add("session-1")
	f.Add("")
	f.Add("  spaced  ")
	f.Fuzz(func(t *testing.T, sessionID string) {
		reg := &Registration{}
		if err := reg.UpdateSessionID(sessionID); (sessionID == "") != (err != nil) {
			t.Fatalf("UpdateSessionID(%q) error=%v", sessionID, err)
		}
	})
}

func FuzzRegistrationLifecycle(f *testing.F) {
	for scenario := uint8(0); scenario < 4; scenario++ {
		f.Add(scenario, "session-next")
	}
	f.Fuzz(func(t *testing.T, scenario uint8, sessionID string) {
		reg := &Registration{}
		if err := reg.Remove(); err != nil {
			t.Fatalf("unregistered Remove: %v", err)
		}
		switch scenario % 4 {
		case 0:
			runDir := t.TempDir()
			entry := rendezvous.Entry{PID: 17}
			if err := reg.Register(runDir, entry); err != nil {
				t.Fatal(err)
			}
			if sessionID == "" {
				sessionID = "session-next"
			}
			if err := reg.UpdateSessionID(sessionID); err != nil {
				t.Fatal(err)
			}
			if err := reg.Remove(); err != nil {
				t.Fatal(err)
			}
		case 1:
			blocked := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := reg.Register(filepath.Join(blocked, "run"), rendezvous.Entry{PID: 1}); err == nil {
				t.Fatal("expected register failure")
			}
		case 2:
			runDir := t.TempDir()
			if err := reg.Register(runDir, rendezvous.Entry{PID: 2}); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(runDir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(runDir, []byte("blocked"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := reg.UpdateSessionID("updated"); err == nil {
				t.Fatal("expected update failure")
			}
		case 3:
			runDir := t.TempDir()
			if err := reg.Register(runDir, rendezvous.Entry{PID: 3}); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(runDir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(runDir, []byte("blocked"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := reg.Remove(); err == nil {
				t.Fatal("expected remove failure")
			}
		}
	})
}
