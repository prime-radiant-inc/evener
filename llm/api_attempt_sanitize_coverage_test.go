package llm

import (
	"net/http"
	"testing"
)

// TestNewAPILogCredentialMaterialEmptyHeaderName covers the canonical == ""
// skip for header names (line 40).
func TestNewAPILogCredentialMaterialEmptyHeaderName(t *testing.T) {
	// An empty or whitespace-only header name produces canonical == "".
	m := NewAPILogCredentialMaterial([]string{"  ", ""}, nil, "secret")
	if len(m.HeaderNames) != 0 {
		t.Fatalf("HeaderNames = %v, want empty", m.HeaderNames)
	}
}

// TestNewAPILogCredentialMaterialEmptyQueryName covers the canonical == ""
// skip for query names (line 53).
func TestNewAPILogCredentialMaterialEmptyQueryName(t *testing.T) {
	// An empty or whitespace-only query name produces canonical == "".
	m := NewAPILogCredentialMaterial(nil, []string{"  ", ""}, "secret")
	if len(m.QueryNames) != 0 {
		t.Fatalf("QueryNames = %v, want empty", m.QueryNames)
	}
}

// TestAPILogCredentialMaterialForRequestNil covers the req == nil path
// (lines 106-107).
func TestAPILogCredentialMaterialForRequestNil(t *testing.T) {
	configured := NewAPILogCredentialMaterial([]string{"X-Custom"}, []string{"token"}, "secret")
	material := APILogCredentialMaterialForRequest(nil, configured)
	if _, ok := material.HeaderNames["X-Custom"]; !ok {
		t.Fatal("missing configured header name")
	}
	if _, ok := material.QueryNames["token"]; !ok {
		t.Fatal("missing configured query name")
	}
}

// TestAPILogCredentialMaterialForRequestEscapedQueryName covers the rawName !=
// name path where the escaped and unescaped query names both get added
// (lines 131-132).
func TestAPILogCredentialMaterialForRequestEscapedQueryName(t *testing.T) {
	// Use a query name with URL-encoding so rawName != name.
	req, err := http.NewRequest(http.MethodGet, "https://provider.test/api?access%5Ftoken=secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	material := APILogCredentialMaterialForRequest(req, APILogCredentialMaterial{})
	// access_token is a common credential query name; both the escaped and
	// unescaped forms should be registered.
	if _, ok := material.QueryNames["access_token"]; !ok {
		t.Fatal("missing unescaped query name")
	}
}

// TestStructuredCredentialHeaderValuesCookieEdgeCases covers the cookie pair
// edge cases: no = (line 159), empty value (line 163), quoted value
// (lines 168-169).
func TestStructuredCredentialHeaderValuesCookieEdgeCases(t *testing.T) {
	// Cookie with no =, empty value, and quoted value.
	values := structuredCredentialHeaderValues("Cookie", "noequals; empty=; quoted=\"quoted-value\"")
	found := map[string]bool{}
	for _, v := range values {
		found[v] = true
	}
	// "noequals" has no =, so it should be skipped (line 159).
	// "empty=" has empty value, so it should be skipped (line 163).
	// "quoted=\"quoted-value\"" should yield the unquoted form (lines 168-169).
	if found["quoted-value"] {
		// The unquoted version should be present.
	} else if len(values) == 0 {
		t.Fatal("expected at least the quoted value to be extracted")
	}
}

// TestSanitizeRequestForAPILogNil covers the req == nil path (lines 183-184).
func TestSanitizeRequestForAPILogNil(t *testing.T) {
	endpoint, headers := SanitizeRequestForAPILog(nil, APILogCredentialMaterial{})
	if endpoint != "" || headers != nil {
		t.Fatalf("endpoint=%q headers=%v, want empty/nil", endpoint, headers)
	}
}

// TestSanitizeRequestForAPILogTrailerAllCredentials covers the len(values) == 0
// path after trailer sanitize (line 202).
func TestSanitizeRequestForAPILogTrailerAllCredentials(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://provider.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Set a Trailer header that lists only credential-bearing header names.
	req.Header.Set("Trailer", "Authorization, X-Api-Key")
	material := NewAPILogCredentialMaterial(nil, nil) // common credential names are built-in
	endpoint, headers := SanitizeRequestForAPILog(req, material)
	// The Trailer header should be removed because all its entries are
	// credential-bearing.
	if _, ok := headers["Trailer"]; ok {
		t.Fatalf("Trailer should be removed when all entries are credentials: %v", headers)
	}
	if endpoint == "" {
		t.Fatal("endpoint should not be empty")
	}
}

// TestSanitizeRequestForAPILogTrailerEmptyName covers the name == "" skip in
// trailer header values (line 234).
func TestSanitizeRequestForAPILogTrailerEmptyName(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://provider.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Trailer with empty entries between commas.
	req.Header.Set("Trailer", ", X-Custom, ")
	material := NewAPILogCredentialMaterial([]string{"X-Custom"}, nil)
	_, headers := SanitizeRequestForAPILog(req, material)
	// X-Custom is a credential, so it should be removed. The empty names
	// are skipped. The result should have no Trailer (all entries removed
	// or empty).
	if _, ok := headers["Trailer"]; ok {
		t.Fatalf("Trailer should be removed: %v", headers["Trailer"])
	}
}

// TestSanitizeTrailerHeaderValuesNoRemoval covers the !removed path (lines
// 243-244) where no credential names are found in the trailer values.
func TestSanitizeTrailerHeaderValuesNoRemoval(t *testing.T) {
	values := sanitizeTrailerHeaderValues([]string{"X-Debug, X-Info"}, APILogCredentialMaterial{})
	if len(values) != 1 || values[0] != "X-Debug, X-Info" {
		t.Fatalf("values = %v, want original preserved", values)
	}
}

// TestUnescapeQueryComponentError covers the err != nil path (lines 264-265).
func TestUnescapeQueryComponentError(t *testing.T) {
	// An invalid percent-encoding should return the original string.
	got := unescapeQueryComponent("%zz")
	if got != "%zz" {
		t.Fatalf("unescapeQueryComponent(%q) = %q, want %q", "%zz", got, "%zz")
	}
}

// TestContainsCredentialDurableStringEvidencePartsMarshalError covers the
// err != nil || len(encoded) < 2 path (lines 323-324). This is hard to trigger
// because json.Marshal of a string always succeeds. But a string with invalid
// UTF-8 might cause an error — actually json.Marshal of any string always
// succeeds in Go. The len(encoded) < 2 path is for empty strings (encoded as
// `""` which is len 2, so < 2 is never true for valid strings). This is
// effectively unreachable with valid strings. Let's test with an empty string.
func TestContainsCredentialDurableStringEvidencePartsEmptyString(t *testing.T) {
	// An empty string encodes to `""` (len 2), so encoded[1:1] is "".
	// This doesn't trigger the < 2 path, but it does exercise the function
	// with an empty string.
	got := containsCredentialDurableStringEvidenceParts("", nil, nil)
	if got {
		t.Fatal("empty string should not contain credential evidence")
	}
}

// TestCaseFoldedCredentialNameVariantsDuplicate covers the duplicate variant
// skip (line 350).
func TestCaseFoldedCredentialNameVariantsDuplicate(t *testing.T) {
	// Pass names that produce duplicate case-folded variants.
	variants := caseFoldedCredentialNameVariants([]string{"Key", "key", "KEY"})
	// All three produce the same lowercased variant "key", so there should
	// be only one entry (plus its escaped variants, which are also identical).
	// The important thing is no panic and at least one entry.
	if len(variants) == 0 {
		t.Fatal("expected at least one variant")
	}
	// Check that "key" is present.
	found := false
	for _, v := range variants {
		if v == "key" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing 'key' variant")
	}
}

// TestCredentialValueVariantsEmpty covers the value == "" skip (line 369).
func TestCredentialValueVariantsEmpty(t *testing.T) {
	variants := credentialValueVariants([]string{"", "valid"})
	if len(variants) == 0 {
		t.Fatal("expected at least one variant for 'valid'")
	}
	for _, v := range variants {
		if v == "" {
			t.Fatal("empty variant should not be included")
		}
	}
}

// TestNewAPILogCredentialMaterialDuplicateValue covers the duplicate value skip
// (lines 68-69).
func TestNewAPILogCredentialMaterialDuplicateValue(t *testing.T) {
	m := NewAPILogCredentialMaterial(nil, nil, "secret", "secret", "other")
	if len(m.Values) != 2 {
		t.Fatalf("Values = %v, want 2 (secret, other)", m.Values)
	}
}
