package llm

import (
	"net/http"
	"reflect"
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
	material := NewAPILogCredentialMaterial([]string{"X-Custom-Key"}, nil, "credential-secret")
	want := APILogCredentialMaterial{
		HeaderNames: map[string]struct{}{"X-Custom-Key": {}},
		Values:      []string{"credential-secret"},
		secretNames: []string{"x-custom-key"},
		patterns:    []string{"credential-secret"},
	}
	if !reflect.DeepEqual(material, want) {
		t.Fatalf("custom-header credential material = %#v, want %#v", material, want)
	}

	req := newCredentialMaterialSanitizationRequest(t)
	endpoint, headers := SanitizeRequestForAPILog(req, material)
	wantHeaders := map[string][]string{
		"Host":                  {"safe-host.provider.test"},
		"X-custom_param-Marker": {"opaque-query-name-value"},
		"X-Safe":                {"safe-marker"},
	}
	if endpoint != "https://provider.test/v1/safe-marker" || !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("sanitized custom-header request = (%q, %#v), want (%q, %#v)", endpoint, headers, "https://provider.test/v1/safe-marker", wantHeaders)
	}
}

// TestCovNewAPILogCredentialMaterialStructuralQueryNameFalse covers the
// !structural path for query names (api_attempt_sanitize.go line 60).
func TestCovNewAPILogCredentialMaterialStructuralQueryNameFalse(t *testing.T) {
	// "custom_param" is not in commonCredentialQueryNames.
	material := NewAPILogCredentialMaterial(nil, []string{"custom_param"}, "credential-secret")
	want := APILogCredentialMaterial{
		QueryNames:  map[string]struct{}{"custom_param": {}},
		Values:      []string{"credential-secret"},
		secretNames: []string{"custom_param"},
		patterns:    []string{"credential-secret"},
	}
	if !reflect.DeepEqual(material, want) {
		t.Fatalf("custom-query credential material = %#v, want %#v", material, want)
	}

	req := newCredentialMaterialSanitizationRequest(t)
	endpoint, headers := SanitizeRequestForAPILog(req, material)
	wantHeaders := map[string][]string{
		"Host":         {"safe-host.provider.test"},
		"X-Custom-Key": {"opaque-header-value"},
		"X-Safe":       {"safe-marker"},
	}
	if endpoint != "https://provider.test/v1/safe-marker" || !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("sanitized custom-query request = (%q, %#v), want (%q, %#v)", endpoint, headers, "https://provider.test/v1/safe-marker", wantHeaders)
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
	material := APILogCredentialMaterial{Values: []string{"credential-secret"}}
	patterns := credentialEvidencePatterns(material)
	wantPatterns := []string{"credential-secret"}
	if !reflect.DeepEqual(patterns, wantPatterns) {
		t.Fatalf("fallback credential patterns = %q, want %q", patterns, wantPatterns)
	}
	assertValueCredentialSanitizesExactly(t, material)
}

// TestCovNewAPILogCredentialMaterialEmptyValue covers the value == "" skip
// in NewAPILogCredentialMaterial (line 66).
func TestCovNewAPILogCredentialMaterialEmptyValue(t *testing.T) {
	material := NewAPILogCredentialMaterial(nil, nil, "", "credential-secret", "")
	want := APILogCredentialMaterial{
		Values:      []string{"credential-secret"},
		secretNames: []string{},
		patterns:    []string{"credential-secret"},
	}
	if !reflect.DeepEqual(material, want) {
		t.Fatalf("empty-filtered credential material = %#v, want %#v", material, want)
	}
	assertValueCredentialSanitizesExactly(t, material)
}

func newCredentialMaterialSanitizationRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://provider.test/v1/safe-marker?custom_param=credential-secret&safe_query=safe-marker", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "safe-host.provider.test"
	req.Header = http.Header{
		"X-Custom-Key":          {"opaque-header-value"},
		"X-custom_param-Marker": {"opaque-query-name-value"},
		"X-Credential-Value":    {"prefix credential-secret suffix"},
		"X-Safe":                {"safe-marker"},
	}
	return req
}

func assertCredentialMaterialSanitizesExactly(t *testing.T, material APILogCredentialMaterial) {
	t.Helper()
	req := newCredentialMaterialSanitizationRequest(t)
	endpoint, headers := SanitizeRequestForAPILog(req, material)
	wantHeaders := map[string][]string{
		"Host":   {"safe-host.provider.test"},
		"X-Safe": {"safe-marker"},
	}
	if endpoint != "https://provider.test/v1/safe-marker" || !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("sanitized complete-material request = (%q, %#v), want (%q, %#v)", endpoint, headers, "https://provider.test/v1/safe-marker", wantHeaders)
	}
}

func assertValueCredentialSanitizesExactly(t *testing.T, material APILogCredentialMaterial) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://provider.test/v1/safe-marker?token=credential-secret&safe_query=safe-marker", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "safe-host.provider.test"
	req.Header = http.Header{
		"X-Credential-Value": {"prefix credential-secret suffix"},
		"X-Safe":             {"safe-marker"},
	}
	endpoint, headers := SanitizeRequestForAPILog(req, material)
	wantHeaders := map[string][]string{
		"Host":   {"safe-host.provider.test"},
		"X-Safe": {"safe-marker"},
	}
	if endpoint != "https://provider.test/v1/safe-marker" || !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("sanitized value-material request = (%q, %#v), want (%q, %#v)", endpoint, headers, "https://provider.test/v1/safe-marker", wantHeaders)
	}
}
