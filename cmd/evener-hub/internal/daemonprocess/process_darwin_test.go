package daemonprocess

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func fixtureRendezvousTime(*testing.T, Target) time.Time { return time.Now() }

func TestDarwinAuditTokenRefusesDifferentGeneration(t *testing.T) {
	target, _ := startNativeChild(t, "locked")
	h, err := openDarwinProcess(target.PID)
	if err != nil {
		t.Fatal(err)
	}
	p := h.(*darwinProcess)
	p.version++
	if err := p.kill(); !errors.Is(err, ErrExited) {
		t.Fatalf("wrong generation signal: %v", err)
	}
	if err := unix.Kill(target.PID, 0); err != nil {
		t.Fatalf("fixture child was signaled: %v", err)
	}
	gone, err := p.exited()
	if err != nil || !gone {
		t.Fatalf("old generation exit = %v, %v", gone, err)
	}
}
