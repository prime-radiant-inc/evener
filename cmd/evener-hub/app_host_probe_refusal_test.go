package hub

// The probe classifier's shared helper-gate conversion is a kept surface (the
// deploy pipeline's plan probe and orphan-resolve's verify arm both ride it), so
// its coverage lives here after the crash-fencing bootstrap tests were removed.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostfence"
)

// TestHelperGateRefusalsRideTheConflictClass pins §8:161-162's classification:
// a helper-gate refusal is the typed conflict-class `fencing-helper-*`, never
// `probe-failed`, even on the probe classifier that every other read failure
// goes through.
func TestHelperGateRefusalsRideTheConflictClass(t *testing.T) {
	m := testHostManager(nil, nil)

	absent := hostfence.VerifyHelper("alpha", hostfence.HelperVersion, hostfence.HelperProbe{})
	err := m.operationProbeRefusal("alpha", absent)
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("absent gate refusal classified as %T (%v), want an appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeConflict {
		t.Fatalf("absent gate code = %d, want the conflict class %d", wire.Code, appwire.CodeConflict)
	}
	data, ok := wire.Data.(appwire.FencingHelperErrorData)
	if !ok {
		t.Fatalf("absent gate data = %T, want FencingHelperErrorData", wire.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorFencingHelperAbsent {
		t.Fatalf("absent discriminator = %q, want %q", data.EvenerErrorInfo, appwire.ErrorFencingHelperAbsent)
	}
	if data.Host != "alpha" || data.Version != strconv.Itoa(hostfence.HelperVersion) {
		t.Fatalf("absent data = %+v, want the host and the pinned version string", data)
	}

	// The wire message carries the refusal's own detail — a gate wrapped with
	// the reason the operation refused (a lost claim race, a live foreign
	// process) — not just the gate's generic text.
	wrapped := fmt.Errorf("%w (a lost claim race)", &hostfence.HelperGateError{
		Host: "alpha", Discriminator: hostfence.DiscriminatorHelperAbsent, PinnedVersion: hostfence.HelperVersion,
	})
	err = m.operationProbeRefusal("alpha", wrapped)
	wire, ok = errors.AsType[appwire.WireError](err)
	if !ok || !strings.Contains(wire.Message, "a lost claim race") {
		t.Fatalf("absent wire message = %q, want the wrapped refusal detail", wire.Message)
	}

	untrusted := hostfence.VerifyHelper("alpha", hostfence.HelperVersion, hostfence.HelperProbe{Present: true, Reported: true, Version: 99})
	err = m.operationProbeRefusal("alpha", untrusted)
	wire, ok = errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("untrusted gate refusal classified as %T (%v), want an appwire.WireError", err, err)
	}
	data, ok = wire.Data.(appwire.FencingHelperErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorFencingHelperUntrusted {
		t.Fatalf("untrusted data = %#v, want the %s arm", wire.Data, appwire.ErrorFencingHelperUntrusted)
	}
	// An unknown discriminator is refused as an internal error naming the class,
	// never misclassified as absent (and never as probe-failed).
	unknown := &hostfence.HelperGateError{Host: "alpha", Discriminator: "fencing-helper-future", PinnedVersion: hostfence.HelperVersion}
	err = m.operationProbeRefusal("alpha", unknown)
	wire, ok = errors.AsType[appwire.WireError](err)
	if !ok || wire.Code != appwire.CodeInternalError {
		t.Fatalf("unknown discriminator = (%v, %v), want an internal-class refusal", wire, err)
	}
	if data, ok := wire.Data.(appwire.ErrorData); !ok || data.EvenerErrorInfo != appwire.ErrorInternal {
		t.Fatalf("unknown discriminator data = %#v, want internal", wire.Data)
	}

	// An ordinary read failure still classifies as probe-failed.
	err = m.operationProbeRefusal("alpha", errors.New("read failed"))
	wire, ok = errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("ordinary probe failure classified as %T (%v), want an appwire.WireError", err, err)
	}
	if data, ok := wire.Data.(appwire.ProbeFailedErrorData); !ok || data.EvenerErrorInfo != appwire.ErrorProbeFailed {
		t.Fatalf("ordinary probe failure data = %#v, want the %s arm", wire.Data, appwire.ErrorProbeFailed)
	}
}
