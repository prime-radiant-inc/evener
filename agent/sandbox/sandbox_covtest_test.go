package sandbox

import (
	"errors"
	"testing"
)

func TestCovAsDenied(t *testing.T) {
	for _, err := range []error{nil, errors.New("other")} {
		d, ok := AsDenied(err)
		if ok || d != nil {
			t.Fatalf("AsDenied(%v) = (%v, %v), want (nil, false)", err, d, ok)
		}
	}

	original := &DeniedError{Tool: "read_file", Reason: "outside read roots"}
	d, ok := AsDenied(original)
	if !ok || d != original {
		t.Fatalf("AsDenied(original) = (%v, %v), want original pointer and true", d, ok)
	}

	wrapped := errors.Join(errors.New("outer"), original)
	d, ok = AsDenied(wrapped)
	if !ok || d != original {
		t.Fatalf("AsDenied(wrapped) = (%v, %v), want original pointer and true", d, ok)
	}
}

func TestCovCurable(t *testing.T) {
	tests := []struct {
		reason DenialReason
		want   bool
	}{
		{reason: DenialOutsideReadRoots, want: true},
		{reason: DenialOutsideWriteRoots, want: true},
		{reason: DenialWritesDisabled, want: true},
		{reason: DenialRootTarget, want: false},
		{reason: DenialMasked, want: false},
		{reason: DenialGitProtected, want: false},
		{reason: DenialSymlink, want: false},
		{reason: DenialEscape, want: false},
		{reason: DenialUnspecified, want: false},
	}
	for _, tc := range tests {
		if got := tc.reason.Curable(); got != tc.want {
			t.Errorf("%v.Curable() = %v, want %v", tc.reason, got, tc.want)
		}
	}
}

func TestCovPolicyModeIsOff(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "off", want: true},
		{value: "", want: true},
		{value: "  OFF  ", want: true},
		{value: "restricted", want: false},
	}
	for _, tc := range tests {
		if got := ModeIsOff(tc.value); got != tc.want {
			t.Errorf("ModeIsOff(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}
