package envvars

import "testing"

func TestEVENERScratchDirIsRegisteredInternalVariable(t *testing.T) {
	if EVENERScratchDir.Name != "EVENER_SCRATCH_DIR" {
		t.Fatalf("EVENERScratchDir.Name = %q", EVENERScratchDir.Name)
	}
	if EVENERScratchDir.Visibility != Internal {
		t.Fatalf("EVENERScratchDir.Visibility = %q, want %q", EVENERScratchDir.Visibility, Internal)
	}
	if got, ok := Find(EVENERScratchDir.Name); !ok || got != EVENERScratchDir {
		t.Fatalf("Find(%q) = %+v, %v", EVENERScratchDir.Name, got, ok)
	}
}
