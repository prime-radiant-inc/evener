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

func TestGoogleVertexExpressVariablesAreRegistered(t *testing.T) {
	key, ok := Find("GOOGLE_VERTEX_API_KEY")
	if !ok || !key.Secret || key.Visibility != Public {
		t.Fatalf("GOOGLE_VERTEX_API_KEY = %+v, ok=%v; want a public secret", key, ok)
	}
	base, ok := Find("GOOGLE_VERTEX_EXPRESS_BASE_URL")
	if !ok || base.Secret || base.Visibility != Public {
		t.Fatalf("GOOGLE_VERTEX_EXPRESS_BASE_URL = %+v, ok=%v; want a public non-secret", base, ok)
	}
}
