//go:build darwin || linux

package daemonprocess

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if os.Getenv("EVENER_DAEMONPROCESS_HELPER") == "1" {
		file, err := os.OpenFile(os.Args[2], os.O_RDWR, 0)
		if err != nil {
			os.Exit(2)
		}
		if os.Args[3] != "unlocked" {
			if unix.Flock(int(file.Fd()), unix.LOCK_EX) != nil {
				os.Exit(3)
			}
		}
		if os.Args[3] == "released" {
			if unix.Flock(int(file.Fd()), unix.LOCK_UN) != nil {
				os.Exit(4)
			}
		}
		_, _ = os.Stdout.Write([]byte{1})
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = file.Close()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestNativeControllerKillsOnlyOwnedChild(t *testing.T) {
	for _, mode := range []string{"locked", "unlocked", "released"} {
		t.Run(mode, func(t *testing.T) {
			target, cmd := startNativeChild(t, mode)
			p, err := NewController().Open(target)
			if mode != "locked" {
				if err == nil {
					_ = p.Close()
					t.Fatal("accepted child without current lock")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := p.Wait(ctx); err == nil {
				t.Fatal("live child declared exited")
			}
			if err := p.Kill(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := p.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			if err := p.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd
		})
	}
}

func TestNativeControllerRefusesReplacedLog(t *testing.T) {
	target, _ := startNativeChild(t, "locked")
	path := filepath.Join(target.StateDir, "sessions", target.SessionID+".api.jsonl")
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := NewController().Open(target)
	if err == nil {
		_ = p.Close()
		t.Fatal("accepted a different API-log inode")
	}
}

func startNativeChild(t *testing.T, mode string) (Target, *exec.Cmd) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sessions", "session.api.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "serve", path, mode)
	cmd.Env = append(os.Environ(), "EVENER_DAEMONPROCESS_HELPER=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); _ = cmd.Wait() })
	var ready [1]byte
	if _, err := io.ReadFull(out, ready[:]); err != nil {
		t.Fatal(err)
	}
	target := Target{PID: cmd.Process.Pid, SessionID: "session", StateDir: dir}
	target.StartedAt = fixtureRendezvousTime(t, target)
	return target, cmd
}
