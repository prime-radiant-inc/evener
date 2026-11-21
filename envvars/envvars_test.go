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

func TestGoogleVertexExpressVariablesAreRetired(t *testing.T) {
	if v, ok := Find("GOOGLE_VERTEX_API_KEY"); ok {
		t.Fatalf("GOOGLE_VERTEX_API_KEY must be retired, still found %+v", v)
	}
	if v, ok := Find("GOOGLE_VERTEX_EXPRESS_BASE_URL"); ok {
		t.Fatalf("GOOGLE_VERTEX_EXPRESS_BASE_URL must be retired, still found %+v", v)
	}
}
