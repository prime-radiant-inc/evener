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

func TestHarnessFeedbackVariablesAreRegistered(t *testing.T) {
	for _, tc := range []struct {
		v      Var
		name   string
		secret bool
	}{
		{EVENERHarnessFeedbackWebhook, "EVENER_HARNESS_FEEDBACK_WEBHOOK", true},
		{EVENERHarnessFeedbackCommand, "EVENER_HARNESS_FEEDBACK_COMMAND", false},
		{EVENERHarnessFeedbackFile, "EVENER_HARNESS_FEEDBACK_FILE", false},
	} {
		if tc.v.Name != tc.name || tc.v.Visibility != Public || tc.v.Secret != tc.secret {
			t.Fatalf("%+v mismatch: want name=%q visibility=public secret=%v", tc.v, tc.name, tc.secret)
		}
		if got, ok := Find(tc.name); !ok || got != tc.v {
			t.Fatalf("Find(%q) = %+v, %v", tc.name, got, ok)
		}
	}
}
