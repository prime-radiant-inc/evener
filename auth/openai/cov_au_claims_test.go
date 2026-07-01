package openai

import "testing"

// TestClaimNestedStringHandlesNonMapParent covers the two guard arms of
// claimNestedString: a missing parent key and a parent whose value is not a
// nested object.
func TestClaimNestedStringHandlesNonMapParent(t *testing.T) {
	raw := map[string]any{
		"https://api.openai.com/profile": "not-a-map",
	}

	if got := claimNestedString(raw, "https://api.openai.com/profile", "email"); got != "" {
		t.Fatalf("claimNestedString(non-map parent) = %q, want empty", got)
	}
	if got := claimNestedString(raw, "missing-parent", "email"); got != "" {
		t.Fatalf("claimNestedString(missing parent) = %q, want empty", got)
	}
}

// TestClaimStringUnwrapsIDObject covers the map-with-id arm of claimString,
// where a claim value is an object carrying an "id" field.
func TestClaimStringUnwrapsIDObject(t *testing.T) {
	raw := map[string]any{
		"account": map[string]any{"id": "acct-nested"},
	}
	if got := claimString(raw, "account"); got != "acct-nested" {
		t.Fatalf("claimString(id object) = %q, want acct-nested", got)
	}

	// A map without a string "id" yields the empty string (falls through).
	rawNoID := map[string]any{"account": map[string]any{"other": 1}}
	if got := claimString(rawNoID, "account"); got != "" {
		t.Fatalf("claimString(map without id) = %q, want empty", got)
	}
}
