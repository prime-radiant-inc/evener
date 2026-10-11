package hostops

// Per-host gate tests (deploy pipeline 08b §5, §12's "Busy-holder classes"
// row). The gate vocabulary — holder classes and the typed busy error — is this
// package's; the production implementation over the Manager's existing per-host
// lock is sshconn's, and its own tests pin the sharing.

import (
	"errors"
	"strings"
	"testing"
)

// TestGateTryAcquireRendersTheBusyClasses pins §5's holder classes: an
// operation holder names its operation id, a plan holder renders the typed
// transient form with no operation reference, and a manager holder (attach /
// update / remove / teardown / supervisor) renders the transient form naming
// its activity — never an invented operation id.
func TestGateTryAcquireRendersTheBusyClasses(t *testing.T) {
	cases := []struct {
		name   string
		holder Holder
		want   []string
		absent []string
	}{
		{
			name:   "an operation-held gate names the operation",
			holder: Holder{Kind: HolderOperation, OperationID: "00000000000000000042"},
			want:   []string{`host "m4" busy`, "00000000000000000042"},
		},
		{
			name:   "a plan-held gate renders the transient form",
			holder: Holder{Kind: HolderPlan},
			want:   []string{`host "m4" busy (plan in progress)`},
		},
		{
			name:   "an attach-held gate names the activity, never an operation",
			holder: Holder{Kind: HolderManager, Activity: "attach"},
			want:   []string{`host "m4" busy`, "attach"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := NewGate()
			release, err := gate.TryAcquire("m4", tc.holder)
			if err != nil {
				t.Fatalf("first TryAcquire: %v", err)
			}
			_, err = gate.TryAcquire("m4", Holder{Kind: HolderPlan})
			var busy *BusyError
			if !errors.As(err, &busy) {
				t.Fatalf("second TryAcquire = %T (%v), want *BusyError", err, err)
			}
			if busy.Host != "m4" {
				t.Fatalf("busy host = %q, want m4", busy.Host)
			}
			if busy.Holder != tc.holder {
				t.Fatalf("busy holder = %+v, want the holder that acquired %+v", busy.Holder, tc.holder)
			}
			for _, want := range tc.want {
				if !strings.Contains(busy.Error(), want) {
					t.Fatalf("busy message %q does not carry %q", busy.Error(), want)
				}
			}
			release()
			// The release makes the gate reacquirable; a second release is a
			// programming error the gate must not tolerate into a double unlock.
			if _, err := gate.TryAcquire("m4", Holder{Kind: HolderPlan}); err != nil {
				t.Fatalf("TryAcquire after release: %v", err)
			}
		})
	}
}

// TestHoldAsPublishesThePromotedHolder pins §5's holder publication for the
// deploy/restart paths: the gate is acquired before the operation's record
// exists, so the pre-record window renders the typed transient form; once the
// consume-and-create write lands, the holder is updated and the busy refusal
// names the record id the open/wait-able reference resolves through. A
// promotion of a free gate refuses: there is no hold to update.
func TestHoldAsPublishesThePromotedHolder(t *testing.T) {
	gate := NewGate()
	release, err := gate.TryAcquire("m4", Holder{Kind: HolderOperation})
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}

	// The pre-record window: an operation holder with no record id yet renders
	// the transient form, never an invented operation reference.
	_, err = gate.TryAcquire("m4", Holder{Kind: HolderPlan})
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("pre-record busy = %T (%v), want *BusyError", err, err)
	}
	if busy.Holder.OperationID != "" {
		t.Fatalf("pre-record holder = %+v, want no operation id", busy.Holder)
	}

	// The promotion: the record exists, so contenders name it.
	if err := gate.HoldAs("m4", Holder{Kind: HolderOperation, OperationID: "00000000000000000042"}); err != nil {
		t.Fatalf("HoldAs: %v", err)
	}
	_, err = gate.TryAcquire("m4", Holder{Kind: HolderPlan})
	if !errors.As(err, &busy) {
		t.Fatalf("post-promotion busy = %T (%v), want *BusyError", err, err)
	}
	if busy.Holder.Kind != HolderOperation || busy.Holder.OperationID != "00000000000000000042" {
		t.Fatalf("post-promotion holder = %+v, want the promoted operation", busy.Holder)
	}
	if !strings.Contains(busy.Error(), "00000000000000000042") {
		t.Fatalf("post-promotion prose %q does not name the operation", busy.Error())
	}

	release()
	if err := gate.HoldAs("m4", Holder{Kind: HolderOperation, OperationID: "1"}); !errors.Is(err, ErrGateNotHeld) {
		t.Fatalf("HoldAs on a free gate = %v, want ErrGateNotHeld", err)
	}
}

// TestGateSerializesPerHostNotAcrossHosts pins the gate's scope: one host's
// held gate refuses only that host, and a different host's gate is free.
func TestGateSerializesPerHostNotAcrossHosts(t *testing.T) {
	gate := NewGate()
	release, err := gate.TryAcquire("m4", Holder{Kind: HolderPlan})
	if err != nil {
		t.Fatalf("TryAcquire(m4): %v", err)
	}
	defer release()
	if _, err := gate.TryAcquire("other", Holder{Kind: HolderPlan}); err != nil {
		t.Fatalf("a held gate for m4 refused another host: %v", err)
	}
}

// TestGateNilHolderReadsAsBusyNeverAsFree pins the fail-closed direction: a
// holder the gate cannot classify still renders a busy refusal — never a free
// gate.
func TestGateNilHolderReadsAsBusyNeverAsFree(t *testing.T) {
	gate := NewGate()
	release, err := gate.TryAcquire("m4", Holder{})
	if err != nil {
		t.Fatalf("TryAcquire with an unclassified holder: %v", err)
	}
	defer release()
	if _, err := gate.TryAcquire("m4", Holder{Kind: HolderPlan}); err == nil {
		t.Fatal("a held gate with an unclassified holder admitted a second holder")
	}
}

// TestGateBusyErrorFallsOutsideErrorsIsTargets pins that a busy refusal is not
// mistaken for any other sentinel this package exposes: callers classify it
// with errors.As on *BusyError, and nothing else matches.
func TestGateBusyErrorFallsOutsideErrorsIsTargets(t *testing.T) {
	err := Busy("m4", Holder{Kind: HolderPlan})
	for _, other := range []error{ErrTokenMissing, ErrFactsStale, ErrRecordNotFound, ErrStoreCorrupt} {
		if errors.Is(err, other) {
			t.Fatalf("a busy error matched %v", other)
		}
	}
}

// TestBusyErrorRendersAnEmptyOperationHolderAsTransientProse pins the prose
// half of the empty-id fallback: an operation holder without a record id must
// not render prose naming an operation while its wire data carries no
// open/wait-able reference.
func TestBusyErrorRendersAnEmptyOperationHolderAsTransientProse(t *testing.T) {
	err := Busy("m4", Holder{Kind: HolderOperation})
	want := `host "m4" busy (another host operation in progress)`
	if err.Error() != want {
		t.Fatalf("empty-operation-holder prose = %q, want %q", err.Error(), want)
	}
	withID := Busy("m4", Holder{Kind: HolderOperation, OperationID: "op-1"})
	if withID.Error() != `host "m4" busy: operation op-1 is in progress` {
		t.Fatalf("operation-holder prose = %q", withID.Error())
	}
}
