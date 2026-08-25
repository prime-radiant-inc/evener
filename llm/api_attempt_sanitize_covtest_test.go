package llm

import (
	"net/http"
	"reflect"
	"slices"
	"strings"
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
	want := []string{"%41%42", "AB"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("structured cookie values = %q, want %q", values, want)
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
	req.Header.Set("X-Safe", "kept")
	// Mark "Host" as a credential header name.
	material := NewAPILogCredentialMaterial([]string{"Host"}, nil, "secret-host")
	endpoint, headers := SanitizeRequestForAPILog(req, material)
	wantHeaders := map[string][]string{"X-Safe": {"kept"}}
	if endpoint != "https://provider.test" || !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("sanitized request = (%q, %#v), want (%q, %#v)", endpoint, headers, "https://provider.test", wantHeaders)
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
	req.Host = "wire.provider.test"
	req.Header.Set("X-Safe", "kept")
	// Set a different value in req.Header["Host"] to distinguish it from req.Host.
	req.Header["Host"] = []string{"should-be-skipped"}
	endpoint, headers := SanitizeRequestForAPILog(req, APILogCredentialMaterial{})
	wantHeaders := map[string][]string{"Host": {"wire.provider.test"}, "X-Safe": {"kept"}}
	if endpoint != "https://provider.test" || !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("sanitized request = (%q, %#v), want (%q, %#v)", endpoint, headers, "https://provider.test", wantHeaders)
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
	req.Header.Set("X-Safe", "kept")
	// The host value itself contains a credential value.
	material := NewAPILogCredentialMaterial(nil, nil, "secret-value")
	endpoint, headers := SanitizeRequestForAPILog(req, material)
	wantHeaders := map[string][]string{"X-Safe": {"kept"}}
	if endpoint != "https://provider.test" || !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("sanitized request = (%q, %#v), want (%q, %#v)", endpoint, headers, "https://provider.test", wantHeaders)
	}
}

// TestCovContainsCredentialDurableStringEvidencePartsEncodedMatch covers
// the path where the JSON-encoded inner string matches a pattern.
func TestCovContainsCredentialDurableStringEvidencePartsEncodedMatch(t *testing.T) {
	raw := "line one\nline two"
	encodedPattern := `\n`
	if strings.Contains(raw, encodedPattern) {
		t.Fatal("fixture raw text already contains the JSON-encoded pattern")
	}
	got := containsCredentialDurableStringEvidenceParts(raw, []string{encodedPattern}, nil)
	if !got {
		t.Fatalf("expected credential evidence for encoded pattern %q in %q", encodedPattern, raw)
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
