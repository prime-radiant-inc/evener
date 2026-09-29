//go:build linux || darwin

package hub

// The orphan-resolve half of the helper-gate conversion test needs the
// Unix-tagged resolve fixtures, so this test carries the same build tag.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostfence"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestHelperGateUnknownDiscriminatorRefusedOnBothSurfaces pins the Low's one
// shared conversion and its single unknown-discriminator policy: the shared
// helper refuses an unknown class (never renders it as absent/untrusted), and
// both surfaces — the probe classifier and orphan-resolve's verify arm — refuse
// it explicitly as an internal-class error.
func TestHelperGateUnknownDiscriminatorRefusedOnBothSurfaces(t *testing.T) {
	unknown := &hostfence.HelperGateError{
		Host: "alpha", Discriminator: "fencing-helper-future", PinnedVersion: hostfence.HelperVersion,
	}
	if _, ok := helperGateWire(unknown, "message"); ok {
		t.Fatal("helperGateWire rendered an unknown discriminator as a known arm")
	}

	// The probe surface.
	m := testHostManager(nil, nil)
	err := m.operationProbeRefusal("alpha", unknown)
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok || wire.Code != appwire.CodeInternalError {
		t.Fatalf("probe surface = (%v, %v), want an internal-class refusal", wire, err)
	}

	// The orphan-resolve surface, through its verify seam.
	ops, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	orphan := newHubHostManager(nil, nil, hubcore.WebConfig{
		RemoteHostOpsStore: ops,
		RemoteHostOrphanVerify: func(context.Context, hostops.Record) error {
			return unknown
		},
	}, "", nil, nil)
	record := quarantinedHubRecord(t, ops, "h1")
	_, err = orphan.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: record.ID})
	wire, ok = errors.AsType[appwire.WireError](err)
	if !ok || wire.Code != appwire.CodeInternalError {
		t.Fatalf("orphan-resolve surface = (%v, %v), want an internal-class refusal", wire, err)
	}
	if !strings.Contains(wire.Message, "fencing-helper-future") {
		t.Fatalf("orphan-resolve message = %q, want the unknown discriminator named", wire.Message)
	}
}
