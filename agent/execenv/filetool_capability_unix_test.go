//go:build linux || darwin

package execenv

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestFileToolEnforceableRequiresSecureOpen(t *testing.T) {
	original := securePathCapabilityProbe
	t.Cleanup(func() { securePathCapabilityProbe = original })

	for _, probeErr := range []error{unix.ENOSYS, unix.EPERM} {
		securePathCapabilityProbe = func() error { return probeErr }
		if FileToolEnforceable() {
			t.Errorf("secure-open failure %v reported file-tool enforcement available", probeErr)
		}
	}

	securePathCapabilityProbe = func() error { return nil }
	if !FileToolEnforceable() {
		t.Error("a successful secure-open probe reported file-tool enforcement unavailable")
	}

	securePathCapabilityProbe = func() error { return errors.New("unexpected secure-open failure") }
	if FileToolEnforceable() {
		t.Error("an unexpected secure-open failure reported file-tool enforcement available")
	}
}
