package registry

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestExpandEnv(t *testing.T) {
	env := map[string]string{"KEY": "sk-1", "ORG": "org-9"}
	lookup := func(n string) (string, bool) { v, ok := env[n]; return v, ok }
	cases := []struct {
		in, want string
		missing  []string
	}{
		{"plain", "plain", nil},
		{"$KEY", "sk-1", nil},
		{"${KEY}", "sk-1", nil},
		{"Bearer $KEY", "Bearer sk-1", nil},
		{"a$$b", "a$b", nil},
		{"$", "$", nil},
		{"$1", "$1", nil},
		{"$MISSING", "", []string{"MISSING"}},
		{"x-$MISSING-$KEY", "x--sk-1", []string{"MISSING"}},
	}
	for _, c := range cases {
		got, missing := expandEnv(c.in, lookup)
		if got != c.want || !reflect.DeepEqual(missing, c.missing) {
			t.Errorf("expandEnv(%q) = %q, %v; want %q, %v", c.in, got, missing, c.want, c.missing)
		}
	}
}

// ScanConfigValue splits a value into the two halves a caller needs to tell a
// reference from a literal: everything a secret could hide in is in `literal`.
func TestScanConfigValue(t *testing.T) {
	for _, tt := range []struct {
		value   string
		refs    []string
		literal string
	}{
		{"$PORTKEY_KEY", []string{"PORTKEY_KEY"}, ""},
		{"Bearer $KEY", []string{"KEY"}, "Bearer "},
		{"sk-live-abc$X", []string{"X"}, "sk-live-abc"},
		{"${A}${B}", []string{"A", "B"}, ""},
		{"literal only", nil, "literal only"},
		{"a$$b", nil, "a$b"},
	} {
		refs, literal, err := ScanConfigValue(tt.value)
		if err != nil {
			t.Fatalf("ScanConfigValue(%q): %v", tt.value, err)
		}
		if !slices.Equal(refs, tt.refs) || literal != tt.literal {
			t.Errorf("ScanConfigValue(%q) = %v/%q, want %v/%q", tt.value, refs, literal, tt.refs, tt.literal)
		}
	}
	for _, bad := range []string{"${UNTERMINATED", "${9BAD}"} {
		if _, _, err := ScanConfigValue(bad); err == nil {
			t.Errorf("ScanConfigValue(%q) must report the syntax error", bad)
		}
	}
}

// CheckCredentialHeaderValue is the one rule both authoring surfaces apply to
// a credential header: at least one $VARIABLE reference, and no literal text
// beside it but an auth scheme word (spec §11.2).
func TestCheckCredentialHeaderValue(t *testing.T) {
	for _, value := range []string{
		"$PORTKEY_KEY",
		"${PORTKEY_KEY}",
		"Bearer $PORTKEY_KEY",
		"Bearer ${PORTKEY_KEY}",
		"Basic ${A}${B}",
	} {
		if err := CheckCredentialHeaderValue(value); err != nil {
			t.Errorf("CheckCredentialHeaderValue(%q) = %v, want accepted", value, err)
		}
	}
	for _, tt := range []struct {
		name   string
		value  string
		want   string
		secret string
	}{
		{"no reference at all", "Bearer literal-secret", "$VARIABLE", "literal-secret"},
		{"nothing at all", "", "$VARIABLE", ""},
		{"a key glued to a reference", "Bearer sk-live-abc$X", "$VARIABLE", "sk-live-abc"},
		{"a key as its own word", "Bearer sk-live-abc $X", "$VARIABLE", "sk-live-abc"},
		{"a literal that is not a scheme word", "key=$K", "$VARIABLE", "key="},
		{"an unterminated reference", "Bearer ${TOKEN", "unterminated", ""},
		{"an invalid variable name", "Bearer ${1BAD}", "invalid environment variable name", ""},
		// A user who means to type "Bearer $API_KEY" but pastes the key
		// itself inside the braces (issue #718) still gets a malformed
		// name, but the content inside ${...} may be the very secret
		// being protected -- it must never reach the refusal text.
		{"an invalid name that is itself a secret", "Bearer ${sk-test-PLANTEDSECRET1234}", "invalid environment variable name", "sk-test-PLANTEDSECRET1234"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckCredentialHeaderValue(tt.value)
			if err == nil {
				t.Fatalf("CheckCredentialHeaderValue(%q) = nil, want refused", tt.value)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CheckCredentialHeaderValue(%q) = %v, want an error mentioning %q", tt.value, err, tt.want)
			}
			// The value may hold a secret, so no refusal may echo it.
			if tt.secret != "" && strings.Contains(err.Error(), tt.secret) {
				t.Fatalf("the refusal echoed the value: %v", err)
			}
		})
	}
}

func TestCheckEnvRefs(t *testing.T) {
	if err := checkEnvRefs("Bearer $KEY and ${OTHER}", "api_key"); err != nil {
		t.Fatal(err)
	}
	if err := checkEnvRefs("${UNTERMINATED", "api_key"); err == nil {
		t.Fatal("unterminated ${ must error")
	}
	if err := checkEnvRefs("${9BAD}", "api_key"); err == nil {
		t.Fatal("invalid name must error")
	}
	// checkEnvRefs is what config load time uses to tell the user which
	// field is broken (issue #718). The braces may hold a pasted secret
	// instead of a variable name: the refusal must still name the field
	// so the user can find it, but never echo what they pasted.
	err := checkEnvRefs("${sk-test-PLANTEDSECRET1234}", "providers.foo.api_key")
	if err == nil {
		t.Fatal("invalid name must error")
	}
	if !strings.Contains(err.Error(), "providers.foo.api_key") {
		t.Fatalf("checkEnvRefs error must name the field: %v", err)
	}
	if strings.Contains(err.Error(), "sk-test-PLANTEDSECRET1234") {
		t.Fatalf("checkEnvRefs error echoed the value: %v", err)
	}
}
