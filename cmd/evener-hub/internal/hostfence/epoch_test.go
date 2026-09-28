package hostfence

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// The epoch model tests pin crash-fencing spec §4's epoch contract: the
// (controller boot id, per-host op sequence) pair, its within-boot order, the
// guard-file rules that decide a compare-and-advance, and the shape of the
// record's `fencingEpoch` raw field, which hostops keeps verbatim because the
// crash-fencing spec owns it.

func TestEpochValidate(t *testing.T) {
	valid := Epoch{BootID: "boot-1", OpSeq: 3}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate(%+v) = %v, want nil", valid, err)
	}
	invalid := map[string]Epoch{
		"empty boot id":      {OpSeq: 3},
		"zero op sequence":   {BootID: "boot-1"},
		"space in boot id":   {BootID: "boot 1", OpSeq: 3},
		"slash in boot id":   {BootID: "boot/1", OpSeq: 3},
		"quote in boot id":   {BootID: `boot"1`, OpSeq: 3},
		"newline in boot id": {BootID: "boot\n1", OpSeq: 3},
	}
	for name, epoch := range invalid {
		if err := epoch.Validate(); !errors.Is(err, ErrInvalidEpoch) {
			t.Errorf("%s: Validate(%+v) = %v, want ErrInvalidEpoch", name, epoch, err)
		}
	}
}

func TestEpochCompareOrdersOnlyOneBoot(t *testing.T) {
	first := Epoch{BootID: "boot-1", OpSeq: 3}
	cases := []struct {
		name  string
		other Epoch
		want  Order
		ok    bool
	}{
		{"same boot lower", Epoch{BootID: "boot-1", OpSeq: 2}, OrderNewer, true},
		{"same boot equal", Epoch{BootID: "boot-1", OpSeq: 3}, OrderEqual, true},
		{"same boot higher", Epoch{BootID: "boot-1", OpSeq: 4}, OrderOlder, true},
		// §4: the pair alone "has no defined cross-restart order" — the remote
		// guard file's sequence is the total order, so two boot ids compare no
		// further.
		{"other boot has no order", Epoch{BootID: "boot-2", OpSeq: 3}, 0, false},
	}
	for _, tc := range cases {
		got, ok := first.Compare(tc.other)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("%s: Compare(%+v, %+v) = (%v, %v), want (%v, %v)", tc.name, first, tc.other, got, ok, tc.want, tc.ok)
		}
	}
}

func TestEpochFromRecordRawField(t *testing.T) {
	record := hostops.Record{
		ID:            "00000000000000000001",
		Host:          "h1",
		Kind:          hostops.KindDeploy,
		State:         hostops.StatePending,
		Generation:    7,
		IncarnationID: "inc-1",
		FencingEpoch:  json.RawMessage(`{"bootId":"boot-9","opSeq":12}`),
	}
	epoch, ok := EpochFromRecord(record)
	if !ok {
		t.Fatal("EpochFromRecord(record) = false, want true")
	}
	if want := (Epoch{BootID: "boot-9", OpSeq: 12}); epoch != want {
		t.Fatalf("EpochFromRecord(record) = %+v, want %+v", epoch, want)
	}
	for name, raw := range map[string]json.RawMessage{
		"absent":        nil,
		"null":          json.RawMessage(`null`),
		"zero op seq":   json.RawMessage(`{"bootId":"boot-9","opSeq":0}`),
		"empty boot id": json.RawMessage(`{"bootId":"","opSeq":1}`),
		"not an object": json.RawMessage(`"boot-9"`),
		"unknown field": json.RawMessage(`{"bootId":"boot-9","opSeq":1,"helperVersion":1}`),
	} {
		record.FencingEpoch = raw
		if _, ok := EpochFromRecord(record); ok {
			t.Errorf("%s: EpochFromRecord(record) = true, want false", name)
		}
	}
}

// TestGuardAdmitsOnlyCurrentUnfencedEpoch pins §4's server-side check: "A step
// whose presented epoch no longer equals the guard refuses server-side".
// TestEpochFromRawNarrowsHostopsShape pins the deliberate asymmetry with
// hostops: the store's reader admits any object carrying bootId/opSeq (the
// retention tests persist an extra key), while the fencing layer refuses a
// shape the spec never defined.
func TestEpochFromRawNarrowsHostopsShape(t *testing.T) {
	raw := json.RawMessage(`{"bootId":"boot-9","opSeq":12,"pad":true}`)
	record := hostops.Record{FencingEpoch: raw}
	if _, ok := record.FencingEpochValue(); !ok {
		t.Fatal("hostops refused the raw field its own retention tests persist")
	}
	if _, ok := EpochFromRaw(raw); ok {
		t.Fatal("hostfence admitted an epoch carrying a key the spec never defined")
	}
}

func TestGuardAdmitsOnlyCurrentUnfencedEpoch(t *testing.T) {
	current := Epoch{BootID: "boot-1", OpSeq: 5}
	guard := GuardState{Version: 1, GuardEpoch: 4, Epoch: &current, Holder: &current}
	if !guard.Admits(current) {
		t.Fatal("Admits(current) = false, want true")
	}
	older := Epoch{BootID: "boot-1", OpSeq: 4}
	if guard.Admits(older) {
		t.Error("Admits(older) = true, want false")
	}
	fenced := guard
	fenceEpoch := Epoch{BootID: "boot-2", OpSeq: 1}
	fenced.Fence = &FenceState{Epoch: fenceEpoch, Superseded: &current, GuardEpoch: 5}
	if fenced.Admits(current) {
		t.Error("Admits(current) with a fence pending = true, want false")
	}
}

// TestGuardAdvanceRules pins §4's compare-and-advance: only the fence's epoch
// may advance, an older epoch never overwrites a newer one, and a replay is
// idempotent.
func TestGuardAdvanceRules(t *testing.T) {
	oldEpoch := Epoch{BootID: "boot-1", OpSeq: 4}
	newEpoch := Epoch{BootID: "boot-1", OpSeq: 5}
	pending := GuardState{
		Version: 1, GuardEpoch: 6, Epoch: &oldEpoch, Holder: &newEpoch,
		Fence: &FenceState{Epoch: newEpoch, Superseded: &oldEpoch, GuardEpoch: 6},
	}
	next, err := pending.Advance(newEpoch)
	if err != nil {
		t.Fatalf("Advance(newEpoch) = %v, want nil", err)
	}
	if next.Epoch == nil || *next.Epoch != newEpoch {
		t.Fatalf("after advance epoch = %+v, want %+v", next.Epoch, newEpoch)
	}
	if next.Fence != nil {
		t.Fatalf("after advance fence = %+v, want cleared", next.Fence)
	}
	if next.GuardEpoch <= pending.GuardEpoch {
		t.Fatalf("after advance guardEpoch = %d, want > %d", next.GuardEpoch, pending.GuardEpoch)
	}
	if _, err := pending.Advance(oldEpoch); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("Advance(oldEpoch) while a newer fence pends = %v, want ErrStaleEpoch", err)
	}
	settled := next
	if replay, err := settled.Advance(newEpoch); err != nil || replay.GuardEpoch != settled.GuardEpoch {
		t.Fatalf("Advance(replay) = (%+v, %v), want idempotent success", replay, err)
	}
	if _, err := settled.Advance(oldEpoch); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("Advance(older) after settle = %v, want ErrStaleEpoch", err)
	}
}

// TestGuardTakeoverRules pins §4's preemptive fence-takeover: the guard file's
// monotonic sequence advances by compare-and-swap, the superseded epoch is
// recorded, and an epoch the guard already superseded never takes over again.
func TestGuardTakeoverRules(t *testing.T) {
	oldEpoch := Epoch{BootID: "boot-1", OpSeq: 4}
	newEpoch := Epoch{BootID: "boot-1", OpSeq: 5}
	settled := GuardState{Version: 1, GuardEpoch: 3, Epoch: &oldEpoch, Holder: &oldEpoch}

	next, err := settled.Takeover(newEpoch)
	if err != nil {
		t.Fatalf("Takeover(newEpoch) = %v, want nil", err)
	}
	if next.GuardEpoch != settled.GuardEpoch+1 {
		t.Fatalf("takeover guardEpoch = %d, want %d", next.GuardEpoch, settled.GuardEpoch+1)
	}
	if next.Fence == nil || next.Fence.Epoch != newEpoch {
		t.Fatalf("takeover fence = %+v, want epoch %+v", next.Fence, newEpoch)
	}
	if next.Fence.Superseded == nil || *next.Fence.Superseded != oldEpoch {
		t.Fatalf("takeover superseded = %+v, want %+v", next.Fence.Superseded, oldEpoch)
	}
	if next.Holder == nil || *next.Holder != newEpoch {
		t.Fatalf("takeover holder = %+v, want %+v", next.Holder, newEpoch)
	}
	// The guard has not advanced: the old epoch still names the guard until the
	// advance lands, and no mutating step is admitted meanwhile.
	if next.Epoch == nil || *next.Epoch != oldEpoch || next.Admits(oldEpoch) {
		t.Fatalf("after takeover epoch = %+v admits(old) = %v, want old epoch and refusal", next.Epoch, next.Admits(oldEpoch))
	}
	// A replay of the same takeover is idempotent.
	if replay, err := next.Takeover(newEpoch); err != nil || replay.GuardEpoch != next.GuardEpoch {
		t.Fatalf("Takeover(replay) = (%+v, %v), want idempotent success", replay, err)
	}
	// The superseded epoch never takes over again.
	if _, err := next.Takeover(oldEpoch); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("Takeover(superseded) = %v, want ErrStaleEpoch", err)
	}
	// A same-boot lower op sequence never takes over.
	lower := Epoch{BootID: "boot-1", OpSeq: 1}
	if _, err := settled.Takeover(lower); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("Takeover(same-boot lower) = %v, want ErrStaleEpoch", err)
	}
}

// TestGuardAdvancedPastAndFreshOperation pins the two decisions later slices
// consume: the guard has advanced past a fencing (fence cleared, epoch named),
// and §5's fresh-operation rule — "A fresh operation starts only after local
// reap completion under a new epoch with the guard advanced past kill/wait of
// the superseded epoch."
func TestGuardAdvancedPastAndFreshOperation(t *testing.T) {
	epoch := Epoch{BootID: "boot-1", OpSeq: 5}
	pending := GuardState{
		Version: 1, GuardEpoch: 6, Epoch: &epoch,
		Fence: &FenceState{Epoch: epoch, GuardEpoch: 6},
	}
	if pending.AdvancedPast(epoch) {
		t.Error("AdvancedPast while the fence pends = true, want false")
	}
	if FreshOperationPermitted(true, pending, epoch) {
		t.Error("FreshOperationPermitted with a pending fence = true, want false")
	}
	settled := pending
	settled.Fence = nil
	if !settled.AdvancedPast(epoch) {
		t.Error("AdvancedPast after advance = false, want true")
	}
	if FreshOperationPermitted(false, settled, epoch) {
		t.Error("FreshOperationPermitted with the reap incomplete = true, want false")
	}
	if !FreshOperationPermitted(true, settled, epoch) {
		t.Error("FreshOperationPermitted on a reaped, advanced guard = false, want true")
	}
	other := Epoch{BootID: "boot-1", OpSeq: 6}
	if FreshOperationPermitted(true, settled, other) {
		t.Error("FreshOperationPermitted for another epoch = true, want false")
	}
}

func TestLayoutHelperConstants(t *testing.T) {
	if HelperVersion != 1 {
		t.Errorf("HelperVersion = %d, want 1 (§6 pins version 1)", HelperVersion)
	}
	if HelperName != "evener-fence" {
		t.Errorf("HelperName = %q, want %q", HelperName, "evener-fence")
	}
	if HelperInstallPath != "~/.local/share/evener/fence" {
		t.Errorf("HelperInstallPath = %q, want the §6 install path", HelperInstallPath)
	}
	if !strings.Contains(string(HelperScript()), HelperName) {
		t.Error("HelperScript() does not name the helper")
	}
}
