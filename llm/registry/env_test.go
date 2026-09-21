package registry

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/internal/valueexpr"
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
		{"${KEY:-fallback}", "sk-1", nil},
		{"${MISSING:-fallback}", "fallback", nil},
		{"${MISSING:-}", "", nil},
		{"${MISSING:-$NOT_A_REF}", "$NOT_A_REF", nil},
	}
	for _, c := range cases {
		got, missing := expandEnv(c.in, lookup)
		if got != c.want || !reflect.DeepEqual(missing, c.missing) {
			t.Errorf("expandEnv(%q) = %q, %v; want %q, %v", c.in, got, missing, c.want, c.missing)
		}
	}
}

// Command expressions in an api_key-shaped value mint through the shared
// evaluator's cache; a failed one behaves like an unset variable, worded as a
// phrase so a warning can carry the command's own diagnosis.
func TestExpandEnvCommandExpression(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) { runs++; return "minted", nil }
	lookup := func(string) (string, bool) { return "", false }

	got, missing := expandEnv("Bearer $(get-gateway-token)", lookup)
	if got != "Bearer minted" || len(missing) != 0 {
		t.Fatalf("expandEnv command = %q, %v", got, missing)
	}
	if _, missing := expandEnv("Bearer $(get-gateway-token)", lookup); len(missing) != 0 {
		t.Fatal("second expansion re-ran the command")
	}
	if runs != 1 {
		t.Fatalf("executor ran %d times; want 1", runs)
	}

	valueexpr.ResetForTest()
	valueexpr.RunCommand = func(string) (string, error) {
		return "", errors.New("command exited with status 1: session expired")
	}
	got, missing = expandEnv("Bearer $(get-gateway-token)", lookup)
	if got != "Bearer " {
		t.Fatalf("failed command did not substitute empty: %q", got)
	}
	if len(missing) != 1 || !strings.HasPrefix(missing[0], "command expression failed: command exited with status 1: session expired") {
		t.Fatalf("missing = %v; want the failure phrase", missing)
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
		{"${A:-Bearer}", []string{"A"}, ""},
		{"$(cat /tmp/thing)", nil, ""},
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
// a credential header: at least one $VARIABLE reference, and at most one
// literal word, which is the auth scheme and stands ahead of the reference
// (spec §11.2). Any scheme name works, custom ones included.
func TestCheckCredentialHeaderValue(t *testing.T) {
	for _, value := range []string{
		"$PORTKEY_KEY",
		"${PORTKEY_KEY}",
		"Bearer $PORTKEY_KEY",
		"Bearer ${PORTKEY_KEY}",
		"Basic ${A}${B}",
		"Custom $PORTKEY_KEY",
		"${PORTKEY_KEY:-Bearer}",
		"${PORTKEY_KEY:-}",
		"Bearer ${A:-Basic}${B}",
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
		// A secret made of letters alone reads as a second scheme word, so
		// only the count and the order of the literals can refuse it.
		{"an alphabetic key beside the reference", "Bearer supersecret $PORTKEY_KEY", "$VARIABLE", "supersecret"},
		{"a literal behind the reference", "$PORTKEY_KEY Bearer", "$VARIABLE", ""},
		{"two scheme words", "Bearer Basic $PORTKEY_KEY", "$VARIABLE", ""},
		{"a literal that is not a scheme word", "key=$K", "$VARIABLE", "key="},
		{"an unterminated reference", "Bearer ${TOKEN", "unterminated", ""},
		{"an invalid variable name", "Bearer ${1BAD}", "invalid environment variable name", ""},
		// A user who means to type "Bearer $API_KEY" but pastes the key
		// itself inside the braces (issue #718) still gets a malformed
		// name, but the content inside ${...} may be the very secret
		// being protected -- it must never reach the refusal text.
		{"an invalid name that is itself a secret", "Bearer ${sk-test-PLANTEDSECRET1234}", "invalid environment variable name", "sk-test-PLANTEDSECRET1234"},
		// A reference's default is literal text standing in the file, so a
		// key there is a key at rest: only an auth scheme word may stand.
		{"a smuggled key as a default", "Bearer ${A:-sk-live-abc}", "auth scheme word", "sk-live-abc"},
		// Command expressions are authored in providers.toml, never through
		// an authoring surface; the refusal must not echo the command, which
		// may embed a secret path or argument.
		{"a command expression", "Bearer $(cat /tmp/planted-secret)", "command expressions are authored in providers.toml", "/tmp/planted-secret"},
		{"a command expression beside a reference", "$(cat /tmp/planted-secret)${A}", "command expressions are authored in providers.toml", "/tmp/planted-secret"},
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

// CheckCredentialHeaderName is the other half of the NAME=VALUE both
// authoring surfaces parse: a name outside the HTTP field-name grammar is one
// no server would read, and a CR or LF in it would forge a second header.
func TestCheckCredentialHeaderName(t *testing.T) {
	for _, name := range []string{"Authorization", "X-Api-Key", "x_api_key", "X-Api-Key-1"} {
		if err := CheckCredentialHeaderName(name); err != nil {
			t.Errorf("CheckCredentialHeaderName(%q) = %v, want accepted", name, err)
		}
	}
	for _, name := range []string{"", "Bad Name", "X-Api-Key:", "X-Api-Key\n", "X-Api-Key\r\nX-Other: y"} {
		if err := CheckCredentialHeaderName(name); err == nil {
			t.Errorf("CheckCredentialHeaderName(%q) = nil, want refused", name)
		}
	}
}

func TestCheckEnvRefs(t *testing.T) {
	if err := checkEnvRefs("Bearer $KEY and ${OTHER}", "api_key"); err != nil {
		t.Fatal(err)
	}
	if err := checkEnvRefs("Bearer ${KEY:-fallback} and $(cat /tmp/thing)", "api_key"); err != nil {
		t.Fatal(err)
	}
	if err := checkEnvRefs("${UNTERMINATED", "api_key"); err == nil {
		t.Fatal("unterminated ${ must error")
	}
	if err := checkEnvRefs("${9BAD}", "api_key"); err == nil {
		t.Fatal("invalid name must error")
	}
	if err := checkEnvRefs("$(unterminated", "api_key"); err == nil {
		t.Fatal("unterminated $( must error")
	}
	if err := checkEnvRefs("$()", "api_key"); err == nil {
		t.Fatal("empty command must error")
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

// CheckAPIKeyEnvName holds api_key_env to what the field names: an
// environment variable, spelled as a "${NAME}" reference spells it. A value
// outside that grammar is a key standing where its variable's name belonged.
func TestCheckAPIKeyEnvName(t *testing.T) {
	for _, name := range []string{"OPENAI_API_KEY", "_private", "k9", "lowercase_ok"} {
		if err := CheckAPIKeyEnvName(name); err != nil {
			t.Errorf("CheckAPIKeyEnvName(%q) = %v, want accepted", name, err)
		}
	}
	for _, name := range []string{"", "sk-live-abc", "TWO WORDS", "$OPENAI_API_KEY", "9LEADING"} {
		err := CheckAPIKeyEnvName(name)
		if err == nil {
			t.Errorf("CheckAPIKeyEnvName(%q) = nil, want refused", name)
			continue
		}
		// The value may be the key itself, so the refusal must not echo it.
		if name != "" && strings.Contains(err.Error(), name) {
			t.Errorf("the refusal echoed the name: %v", err)
		}
	}
}
