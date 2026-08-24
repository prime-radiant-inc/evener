package sandbox

import (
	"errors"
	"testing"
)

// TestCovAsDenied covers AsDenied (denial.go lines 14-19).
func TestCovAsDenied(t *testing.T) {
	// Non-denied error.
	d, ok := AsDenied(errors.New("other"))
	if ok || d != nil {
		t.Fatalf("non-denied: d=%v ok=%v", d, ok)
	}

	// Nil error.
	d, ok = AsDenied(nil)
	if ok || d != nil {
		t.Fatalf("nil: d=%v ok=%v", d, ok)
	}

	// DeniedError.
	original := &DeniedError{Tool: "read_file", Reason: "outside read roots"}
	d, ok = AsDenied(original)
	if !ok || d != original {
		t.Fatalf("denied: d=%v ok=%v", d, ok)
	}

	// Wrapped DeniedError.
	wrapped := errors.Join(original)
	_, ok = AsDenied(wrapped)
	if !ok {
		t.Fatal("wrapped denied should be found")
	}
}

// TestCovCurable covers DenialReason.Curable (denial.go lines 104-111).
func TestCovCurable(t *testing.T) {
	// Curable reasons.
	if !DenialOutsideReadRoots.Curable() {
		t.Fatal("DenialOutsideReadRoots should be curable")
	}
	if !DenialOutsideWriteRoots.Curable() {
		t.Fatal("DenialOutsideWriteRoots should be curable")
	}
	if !DenialWritesDisabled.Curable() {
		t.Fatal("DenialWritesDisabled should be curable")
	}

	// Not curable.
	if DenialRootTarget.Curable() {
		t.Fatal("DenialRootTarget should not be curable")
	}
	if DenialMasked.Curable() {
		t.Fatal("DenialMasked should not be curable")
	}
	if DenialGitProtected.Curable() {
		t.Fatal("DenialGitProtected should not be curable")
	}
	if DenialSymlink.Curable() {
		t.Fatal("DenialSymlink should not be curable")
	}
	if DenialEscape.Curable() {
		t.Fatal("DenialEscape should not be curable")
	}
	// Zero value.
	if DenialUnspecified.Curable() {
		t.Fatal("DenialUnspecified should not be curable")
	}
}

// TestCovPolicy covers Wrapper.Policy (backend.go line 61).
func TestCovPolicy(t *testing.T) {
	w := &Wrapper{policy: ResolvedPolicy{Mode: ModeOff}}
	if got := w.Policy(); got.Mode != ModeOff {
		t.Fatalf("Policy = %+v", got)
	}
}

// TestCovSessionTmp covers Wrapper.SessionTmp (backend.go line 64).
func TestCovSessionTmp(t *testing.T) {
	w := &Wrapper{sessionTmp: "/tmp/scratch"}
	if got := w.SessionTmp(); got != "/tmp/scratch" {
		t.Fatalf("SessionTmp = %q", got)
	}
}

// TestCovPolicyModeIsOff covers ModeIsOff (policy.go line 72).
func TestCovPolicyModeIsOff(t *testing.T) {
	if !ModeIsOff("off") {
		t.Fatal("ModeIsOff(\"off\") should be true")
	}
	if !ModeIsOff("") {
		t.Fatal("ModeIsOff(\"\") should be true")
	}
	if !ModeIsOff("  OFF  ") {
		t.Fatal("ModeIsOff(\"  OFF  \") should be true")
	}
	if ModeIsOff("restricted") {
		t.Fatal("ModeIsOff(\"restricted\") should be false")
	}
}
