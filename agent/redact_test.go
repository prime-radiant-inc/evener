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

// TestRedactStandard_MasksEnvVarNames covers the canonical leak vector the old
// `\b`-anchored rule missed: real credential env-var names with a prefix and/or
// suffix around the sensitive segment (OPENAI_API_KEY, AWS_ACCESS_KEY_ID, …). Each
// name is checked in KEY=VALUE and KEY: VALUE form; the secret value must vanish and
// the NAME must stay legible.
func TestRedactStandard_MasksEnvVarNames(t *testing.T) {
	const secret = "S3cr3tValue0123456789" // 21 chars, no rule-prefix of its own
	names := []string{
		// Prefixed: the sensitive segment is not at the start of the key.
		"OPENAI_API_KEY", "GITHUB_TOKEN", "GH_TOKEN", "DB_PASSWORD",
		"STRIPE_SECRET_KEY", "AWS_SECRET_ACCESS_KEY",
		// Suffixed: the sensitive segment is not at the end of the key.
		"AWS_ACCESS_KEY_ID",
		// Standalone / both: the original plan classes.
		"SECRET_KEY", "REFRESH_TOKEN", "CLIENT_SECRET", "PRIVATE_KEY",
		"password", "api_key",
	}
	for _, name := range names {
		for _, sep := range []string{"=", ": "} {
			in := name + sep + secret
			t.Run(name+strings.TrimSpace(sep), func(t *testing.T) {
				out := redact(in, redactStandard)
				if strings.Contains(out, secret) {
					t.Fatalf("leaked secret for %q\n in:  %s\n out: %s", name, in, out)
				}
				if !strings.Contains(out, redactMarker) {
					t.Fatalf("no redaction marker for %q\n in:  %s\n out: %s", name, in, out)
				}
				if !strings.Contains(out, name) {
					t.Fatalf("redaction destroyed legible name %q\n in:  %s\n out: %s", name, in, out)
				}
			})
		}
	}
}

// TestRedactStandard_MasksJSONCredentialForms covers JSON object credential forms,
// which the colon-adjacent assignment rule could not reach because the surrounding
// quotes broke its anchors.
func TestRedactStandard_MasksJSONCredentialForms(t *testing.T) {
	cases := []struct {
		in     string
		leaked string
	}{
		{`{"authorization":"Bearer XYZsecret123token"}`, "XYZsecret123token"},
		{`{"api_key":"sekritapikeyvalue"}`, "sekritapikeyvalue"},
		{`{"x-api-key":"headerstylesecret99"}`, "headerstylesecret99"},
		{`{"password":"hunter2isweak"}`, "hunter2isweak"},
		{`{"token": "spaced-token-value"}`, "spaced-token-value"},
		{`{"client_secret":"clientsecretblob"}`, "clientsecretblob"},
	}
	for _, tc := range cases {
		t.Run(tc.leaked, func(t *testing.T) {
			out := redact(tc.in, redactStandard)
			if strings.Contains(out, tc.leaked) {
				t.Fatalf("leaked %q\n in:  %s\n out: %s", tc.leaked, tc.in, out)
			}
			if !strings.Contains(out, redactMarker) {
				t.Fatalf("no redaction marker\n in:  %s\n out: %s", tc.in, out)
			}
		})
	}
}

// TestRedactStandard_MasksURLPassword masks just the password in a userinfo URL,
// keeping the scheme, user, and host legible.
func TestRedactStandard_MasksURLPassword(t *testing.T) {
	in := "DATABASE_URL=postgres://dbuser:s3cretPw@db.host:5432/app"
	out := redact(in, redactStandard)
	if strings.Contains(out, "s3cretPw") {
		t.Fatalf("URL password leaked\n in:  %s\n out: %s", in, out)
	}
	// Scheme, user, and host stay legible so the URL remains diagnosable.
	for _, want := range []string{"postgres://", "dbuser", "db.host", "5432"} {
		if !strings.Contains(out, want) {
			t.Fatalf("URL redaction destroyed legible %q\n in:  %s\n out: %s", want, in, out)
		}
	}
}

// TestRedactStandard_MasksCommonTokenPrefixes masks anchorless bare tokens by their
// well-known prefixes (the same independent-of-anchor style as the sk- rule).
func TestRedactStandard_MasksCommonTokenPrefixes(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		leaked string // a distinctive secret-body fragment that must not survive
	}{
		{"github_ghp", "token=ghp_ABCDEFGHIJKLMNOPQRST0123", "ABCDEFGHIJKLMNOPQRST0123"},
		{"github_pat", "GH=github_pat_11ABCDE0aaaaBBBBccccDD", "11ABCDE0aaaaBBBBccccDD"},
		{"slack_xoxb", "slack xoxb-111-222-abcdefghij", "abcdefghij"},
		{"aws_akia", "id AKIAIOSFODNN7EXAMPLE here", "AKIAIOSFODNN7EXAMPLE"},
		{"google_aiza", "key AIzaSyA1234567890abcdefghij1234567890abc end", "SyA1234567890abcdefghij1234567890abc"},
		{"jwt", "bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NX0.sigPART123abc done", "eyJzdWIiOiIxMjM0NX0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := redact(tc.in, redactStandard)
			if strings.Contains(out, tc.leaked) {
				t.Fatalf("leaked %q\n in:  %s\n out: %s", tc.leaked, tc.in, out)
			}
			if !strings.Contains(out, redactMarker) {
				t.Fatalf("no redaction marker\n in:  %s\n out: %s", tc.in, out)
			}
		})
	}
}

// TestRedactStandard_DoesNotOverMaskConfig is the legibility guard: relaxing the key
// match must NOT swallow innocent identifiers that merely CONTAIN a sensitive word as
// a substring (not a full segment), nor prose that names "token"/"key" with no value.
// Each input must be returned UNCHANGED under standard.
func TestRedactStandard_DoesNotOverMaskConfig(t *testing.T) {
	unchanged := []string{
		// Substring-but-not-segment: the sensitive word is glued to more letters.
		"tokenizer=gpt2",
		"keyboard=querty",
		"keyword=foo",
		"secretary=alice",
		"tokens=5",
		"keys=abc",
		// Plain config / prose: no credential-looking key, no userinfo URL.
		"content-type: application/json",
		"Accept: */*",
		"This sentence mentions a token and a key but has no value.",
		"https://example.com/path?ok=1",
	}
	for _, in := range unchanged {
		t.Run(in, func(t *testing.T) {
			out := redact(in, redactStandard)
			if out != in {
				t.Fatalf("over-masked legible config\n in:  %s\n out: %s", in, out)
			}
			if strings.Contains(out, redactMarker) {
				t.Fatalf("unexpected redaction marker in legible config\n in:  %s\n out: %s", in, out)
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
	in := "token=sk-SECRET123 Authorization: Bearer leakme\n" +
		"OPENAI_API_KEY=envsecretvalue123\n" +
		`{"api_key":"jsonsecretvalue123"}` + "\n" +
		"DB=postgres://u:urlsecretpw@h/db\n" +
		"gh=ghp_ABCDEFGHIJKLMNOP0123\n" +
		"opaque " + blob + " trailing"

	std := redact(in, redactStandard)
	strict := redact(in, redactStrict)

	// 1. Every secret standard masks must ALSO be masked by strict (superset). This
	// re-asserts the property across the new env/JSON/URL/token-prefix rules.
	for _, secret := range []string{
		"sk-SECRET123", "leakme", "envsecretvalue123",
		"jsonsecretvalue123", "urlsecretpw", "ABCDEFGHIJKLMNOP0123",
	} {
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
