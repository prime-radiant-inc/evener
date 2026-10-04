//go:build unix

package execenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/sandbox"
)

func TestDisposeSessionScratchRemovesScratchAndTmp(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	if _, err := env.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatal(err)
	}
	scratch, tmp := env.unsandboxedScratchDir(), env.unsandboxedTmpDir()
	if scratch == "" || tmp == "" {
		t.Fatalf("no scratch/tmp minted: %q %q", scratch, tmp)
	}
	if err := env.DisposeSessionScratch(); err != nil {
		t.Fatalf("DisposeSessionScratch: %v", err)
	}
	for _, dir := range []string{scratch, tmp} {
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Errorf("%s still present: %v", dir, err)
		}
	}
}

func TestDisposeSessionScratchKeepsADetachedCommandsTmp(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	if !env.DetachSupported() {
		t.Skip("detach unsupported here")
	}
	started, err := env.DetachCommand(context.Background(), "sleep 30", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(started.PID, syscall.SIGKILL)
		select {
		case <-started.Done:
		case <-time.After(5 * time.Second):
			t.Error("detached command did not exit after SIGKILL")
		}
	})
	scratch, tmp := env.unsandboxedScratchDir(), env.unsandboxedTmpDir()
	if scratch == "" || tmp == "" {
		t.Fatalf("no scratch/tmp minted: %q %q", scratch, tmp)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(tmp)) })
	if err := env.DisposeSessionScratch(); err != nil {
		t.Fatalf("DisposeSessionScratch: %v", err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Errorf("detached command's TMPDIR %s removed: %v", tmp, err)
	}
	if _, err := os.Lstat(scratch); !os.IsNotExist(err) {
		t.Errorf("session scratch %s kept: %v", scratch, err)
	}
}

// TestDetachedCommandFromAConfinedEnvKeepsItsTMPDIR: an unsandboxed env whose
// file tools are confined names its private scratch as TMPDIR, and that scratch
// is deleted when the session ends; a detached command outlives the session, so
// its TMPDIR must be the world-usable container the disposal keeps.
func TestDetachedCommandFromAConfinedEnvKeepsItsTMPDIR(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	if err := env.EnableSandbox(&sandbox.ResolvedPolicy{Mode: sandbox.ModeOff, WriteBlocked: true}); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	if !env.DetachSupported() {
		t.Skip("detach unsupported here")
	}
	out := filepath.Join(t.TempDir(), "tmpdir.txt")
	started, err := env.DetachCommand(context.Background(), `printf %s "$TMPDIR" > `+out+`.part && mv `+out+`.part `+out+` && sleep 30`, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(started.PID, syscall.SIGKILL)
		select {
		case <-started.Done:
		case <-time.After(5 * time.Second):
			t.Error("detached command did not exit after SIGKILL")
		}
	})
	data, ok := waitForTestFile(out, 10*time.Second)
	if !ok {
		t.Fatal("the detached command never wrote its TMPDIR")
	}
	tmpdir := string(data)
	if tmpdir == "" {
		t.Fatal("the detached command ran with no TMPDIR")
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(tmpdir)) })
	if err := env.DisposeSessionScratch(); err != nil {
		t.Fatalf("DisposeSessionScratch: %v", err)
	}
	if _, err := os.Stat(tmpdir); err != nil {
		t.Errorf("the detached command's TMPDIR %s was removed with the session's scratch: %v", tmpdir, err)
	}
}

func TestDetachedCommandHasNoScratchDirVar(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	if !env.DetachSupported() {
		t.Skip("detach unsupported here")
	}
	out := filepath.Join(t.TempDir(), "env.txt")
	if _, err := env.DetachCommand(context.Background(), "env > "+out+".part && mv "+out+".part "+out, "", nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.DisposeSessionScratch() })
	data, ok := waitForTestFile(out, 10*time.Second)
	if !ok {
		t.Fatal("the detached command never wrote its environment")
	}
	if strings.Contains(string(data), "EVENER_SCRATCH_DIR=") {
		t.Error("a detached command's environment names EVENER_SCRATCH_DIR, which is deleted when the session ends")
	}
	if !strings.Contains(string(data), "TMPDIR=") {
		t.Error("a detached command's environment lost TMPDIR")
	}
}
