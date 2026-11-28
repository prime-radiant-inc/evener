package schema

import "testing"

func TestValidSavedSystemPromptRejectsOnlyTheLegacyRenderFailureProse(t *testing.T) {
	t.Parallel()
	const (
		legacy    = "Template rendering failed: forced. Please report this bug."
		customHit = "Template rendering failed: an essay on why prompts fail."
	)
	for _, tc := range []struct {
		name, in, want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "prompt", in: "You are Evener.", want: "You are Evener."},
		{name: "legacy prose", in: legacy, want: ""},
		{name: "custom prompt opening with the diagnostic", in: customHit, want: customHit},
		{name: "custom prompt without the diagnostic", in: "Please report this bug.", want: "Please report this bug."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ValidSavedSystemPrompt(tc.in); got != tc.want {
				t.Fatalf("ValidSavedSystemPrompt(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
