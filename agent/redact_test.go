package agent

import (
	"strings"
	"testing"
)

func TestRedactStandard_MasksCredentials(t *testing.T) {
	in := "token=sk-ABC123 Authorization: Bearer xyz\nAWS_SECRET_ACCESS_KEY=deadbeef"
	out := redact(in, redactStandard)
	for _, secret := range []string{"sk-ABC123", "xyz", "deadbeef"} {
		if strings.Contains(out, secret) {
			t.Fatalf("leaked %q: %s", secret, out)
		}
	}
}

// TestRedactStandard_MasksClassesKeepsKeysLegible covers each documented class and
// asserts the KEY / header name survives (legibility) while the value is masked.
func TestRedactStandard_MasksClassesKeepsKeysLegible(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		leaked  string   // must NOT appear in output
		legible []string // must STILL appear (key/header name preserved)
	}{
		{
			name:    "authorization bearer",
			in:      "Authorization: Bearer abcdef1234567890token",
			leaked:  "abcdef1234567890token",
			legible: []string{"Authorization"},
		},
		{
			name:    "authorization raw",
			in:      "Authorization: abcdef1234567890token",
			leaked:  "abcdef1234567890token",
			legible: []string{"Authorization"},
		},
		{
			name:    "cookie header",
			in:      "Cookie: session=supersecretcookievalue123",
			leaked:  "supersecretcookievalue123",
			legible: []string{"Cookie"},
		},
		{
			name:    "set-cookie header",
			in:      "Set-Cookie: sid=anothersecretcookie456; Path=/",
			leaked:  "anothersecretcookie456",
			legible: []string{"Set-Cookie"},
		},
		{
			name:    "x-api-key header",
			in:      "X-Api-Key: my-very-secret-api-key-value",
			leaked:  "my-very-secret-api-key-value",
			legible: []string{"X-Api-Key"},
		},
		{
			name:    "generic key header",
			in:      "X-Foo-Key: another-secret-header-value-99",
			leaked:  "another-secret-header-value-99",
			legible: []string{"X-Foo-Key"},
		},
		{
			name:    "password assignment",
			in:      "password=hunter2isnotsecure",
			leaked:  "hunter2isnotsecure",
			legible: []string{"password"},
		},
		{
			name:    "api_key assignment",
			in:      "api_key=ABCDEF0123456789abcdef",
			leaked:  "ABCDEF0123456789abcdef",
			legible: []string{"api_key"},
		},
		{
			name:    "client_secret assignment",
			in:      "CLIENT_SECRET=topsecretclientvalue",
			leaked:  "topsecretclientvalue",
			legible: []string{"CLIENT_SECRET"},
		},
		{
			name:    "sk- key inside prose",
			in:      "The provider returned an error with key sk-ABCDEF0123456789abcdefXYZ in the body.",
			leaked:  "sk-ABCDEF0123456789abcdefXYZ",
			legible: []string{"The provider returned an error"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := redact(tc.in, redactStandard)
			if strings.Contains(out, tc.leaked) {
				t.Fatalf("leaked %q\n in:  %s\n out: %s", tc.leaked, tc.in, out)
			}
			for _, want := range tc.legible {
				if !strings.Contains(out, want) {
					t.Fatalf("redaction destroyed legible key %q\n in:  %s\n out: %s", want, tc.in, out)
				}
			}
		})
	}
}

func TestRedactNone_Unchanged(t *testing.T) {
	in := "token=sk-ABC123 Authorization: Bearer xyz password=hunter2"
	if out := redact(in, redactNone); out != in {
		t.Fatalf("redactNone must return input unchanged\n in:  %s\n out: %s", in, out)
	}
}

// TestRedactStrict_SupersetOfStandard proves strict is a strict superset: it masks
// everything standard masks AND additionally redacts a long opaque/high-entropy blob
// that standard leaves untouched.
func TestRedactStrict_SupersetOfStandard(t *testing.T) {
	// A long opaque base64-ish blob with no KEY= or header anchor: standard has no
	// rule for it, strict must redact it.
	blob := "Q1dije8fJ20alkfjAQ0918zKLMnopQRStuvWXyz0123456789abcdEFGHijklMNOPqrstUVWX"
	in := "token=sk-SECRET123 Authorization: Bearer leakme\nopaque " + blob + " trailing"

	std := redact(in, redactStandard)
	strict := redact(in, redactStrict)

	// 1. Every secret standard masks must ALSO be masked by strict (superset).
	for _, secret := range []string{"sk-SECRET123", "leakme"} {
		if strings.Contains(std, secret) {
			t.Fatalf("standard unexpectedly leaked %q: %s", secret, std)
		}
		if strings.Contains(strict, secret) {
			t.Fatalf("strict leaked a secret standard masks %q: %s", secret, strict)
		}
	}

	// 2. The opaque blob is left by standard but redacted by strict (the "additional").
	if !strings.Contains(std, blob) {
		t.Fatalf("test premise broken: standard should leave the opaque blob; out: %s", std)
	}
	if strings.Contains(strict, blob) {
		t.Fatalf("strict must redact the long opaque blob standard leaves: %s", strict)
	}
}
