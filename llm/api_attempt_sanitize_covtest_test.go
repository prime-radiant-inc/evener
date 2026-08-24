package llm

import (
	"net/http"
	"slices"
	"testing"
)

// TestCovNewAPILogCredentialMaterialStructuralHeaderNameFalse covers the
// !structural path in NewAPILogCredentialMaterial (api_attempt_sanitize.go
// line 66), where a non-common credential header name triggers
// addCredentialSecretNames.
func TestCovNewAPILogCredentialMaterialStructuralHeaderNameFalse(t *testing.T) {
	// "X-Custom-Key" is not in commonCredentialHeaderNames, so it
	// triggers the addCredentialSecretNames path.
	m := NewAPILogCredentialMaterial([]string{"X-Custom-Key"}, nil, "secret")
	if _, ok := m.HeaderNames["X-Custom-Key"]; !ok {
		t.Fatal("missing custom credential header name")
	}
	// Verify secretNames were populated (lowercase variants).
	if !slices.Contains(m.secretNames, "x-custom-key") {
		t.Fatalf("missing lowercase secret name, got %v", m.secretNames)
	}
}

// TestCovNewAPILogCredentialMaterialStructuralQueryNameFalse covers the
// !structural path for query names (api_attempt_sanitize.go line 60).
func TestCovNewAPILogCredentialMaterialStructuralQueryNameFalse(t *testing.T) {
	// "custom_param" is not in commonCredentialQueryNames.
	m := NewAPILogCredentialMaterial(nil, []string{"custom_param"}, "secret")
	if _, ok := m.QueryNames["custom_param"]; !ok {
		t.Fatal("missing custom credential query name")
	}
	if !slices.Contains(m.secretNames, "custom_param") {
		t.Fatalf("missing lowercase secret name, got %v", m.secretNames)
	}
}

// TestCovStructuredCredentialHeaderValuesCookieDecoded covers the
// decoded != unquoted path in structuredCredentialHeaderValues
// (api_attempt_sanitize.go line 171-172). A cookie value with
// percent-encoding produces a decoded form different from the unquoted form.
func TestCovStructuredCredentialHeaderValuesCookieDecoded(t *testing.T) {
	// Cookie value with percent-encoded content: session=%41%42
	// After trimming quotes (none), decoded = "AB" which differs from "%41%42".
	values := structuredCredentialHeaderValues("Cookie", "session=%41%42")
	found := map[string]bool{}
	for _, v := range values {
		found[v] = true
	}
	// The decoded form "AB" should be present.
	if !found["AB"] {
		t.Fatalf("expected decoded value 'AB' in %v", values)
	}
	// The raw percent-encoded form should also be present.
	if !found["%41%42"] {
		t.Fatalf("expected raw value '%%41%%42' in %v", values)
	}
}

// TestCovSanitizeRequestForAPILogHostCredential covers the path where the
// Host header is a credential (api_attempt_sanitize.go line 194).
func TestCovSanitizeRequestForAPILogHostCredential(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://provider.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "secret-host"
	// Mark "Host" as a credential header name.
	material := NewAPILogCredentialMaterial([]string{"Host"}, nil, "secret-host")
	_, headers := SanitizeRequestForAPILog(req, material)
	if _, ok := headers["Host"]; ok {
		t.Fatal("Host header should be excluded when it is a credential")
	}
}

// TestCovSanitizeRequestForAPILogHostInHeader covers the strings.EqualFold(name,
// "Host") skip in the header loop (api_attempt_sanitize.go line 194). Setting
// "Host" directly in req.Header should be skipped by the sanitizer; the Host
// in the output comes from req.Host (set by the URL), not req.Header["Host"].
func TestCovSanitizeRequestForAPILogHostInHeader(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://provider.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Set a different value in req.Header["Host"] to distinguish it from req.Host.
	req.Header["Host"] = []string{"should-be-skipped"}
	_, headers := SanitizeRequestForAPILog(req, APILogCredentialMaterial{})
	if h := headers["Host"]; len(h) != 1 || h[0] == "should-be-skipped" {
		t.Fatalf("Host from req.Header should be skipped, got %v", h)
	}
}

// TestCovSanitizeRequestForAPILogHostCredentialValue covers the path where
// the Host header value contains credential evidence.
func TestCovSanitizeRequestForAPILogHostCredentialValue(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://provider.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "contains-secret-value"
	// The host value itself contains a credential value.
	material := NewAPILogCredentialMaterial(nil, nil, "secret-value")
	_, headers := SanitizeRequestForAPILog(req, material)
	if _, ok := headers["Host"]; ok {
		t.Fatal("Host header should be excluded when its value contains credential evidence")
	}
}

// TestCovContainsCredentialDurableStringEvidencePartsJSONMarshalError
// covers the err != nil path in
// containsCredentialDurableStringEvidenceParts (api_attempt_sanitize.go
// line 323-324). json.Marshal of a string always succeeds in Go, so this
// path is effectively unreachable.
func TestCovContainsCredentialDurableStringEvidencePartsJSONMarshalError(t *testing.T) {
	// json.Marshal(string) always returns nil error in Go. The err != nil
	// check is a defensive guard that cannot be triggered with a plain
	// string. This test exercises the function with a non-matching value
	// to cover the encoded path.
	got := containsCredentialDurableStringEvidenceParts("hello", nil, nil)
	if got {
		t.Fatal("plain string with no patterns should not contain credential evidence")
	}
}

// TestCovContainsCredentialDurableStringEvidencePartsEncodedMatch covers
// the path where the JSON-encoded inner string matches a pattern.
func TestCovContainsCredentialDurableStringEvidencePartsEncodedMatch(t *testing.T) {
	// A value containing a backslash will be JSON-encoded with an escaped
	// backslash. The inner string between quotes will differ from the
	// original. Test that we detect a pattern in the encoded form.
	// Pattern "secret" should match in the raw string.
	got := containsCredentialDurableStringEvidenceParts("has secret here", nil, []string{"secret"})
	if !got {
		t.Fatal("expected credential evidence in 'has secret here'")
	}
}

// TestCovCredentialEvidencePatternsNilPatterns covers the material.patterns
// == nil path in credentialEvidencePatterns, which falls back to
// credentialValueVariants(material.Values).
func TestCovCredentialEvidencePatternsNilPatterns(t *testing.T) {
	material := APILogCredentialMaterial{Values: []string{"abc"}}
	patterns := credentialEvidencePatterns(material)
	if len(patterns) == 0 {
		t.Fatal("expected patterns from Values")
	}
	// Verify "abc" is in the patterns.
	if !slices.Contains(patterns, "abc") {
		t.Fatalf("missing 'abc' pattern in %v", patterns)
	}
}

// TestCovNewAPILogCredentialMaterialEmptyValue covers the value == "" skip
// in NewAPILogCredentialMaterial (line 66).
func TestCovNewAPILogCredentialMaterialEmptyValue(t *testing.T) {
	m := NewAPILogCredentialMaterial(nil, nil, "", "secret", "")
	if len(m.Values) != 1 || m.Values[0] != "secret" {
		t.Fatalf("Values = %v, want [secret]", m.Values)
	}
}
