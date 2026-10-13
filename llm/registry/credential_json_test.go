package registry

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
)

func TestCheckCredentialJSON(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want error // the refusal reason, or nil for an accepted credential
	}{
		{
			name: "authorized_user",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}`,
			want: nil,
		},
		{
			name: "service_account",
			raw:  testServiceAccountJSON(t),
			want: nil,
		},
		{
			name: "external_account",
			raw:  `{"type":"external_account","audience":"x"}`,
			want: ErrCredentialTypeUnsupported,
		},
		{
			name: "service_account with no fields",
			raw:  `{"type":"service_account"}`,
			want: ErrCredentialFieldsMissing,
		},
		{
			name: "authorized_user with only a client id",
			raw:  `{"type":"authorized_user","client_id":"a"}`,
			want: ErrCredentialFieldsMissing,
		},
		{
			name: "service_account with an empty private key",
			raw:  `{"type":"service_account","client_email":"sa@example.iam.gserviceaccount.com","private_key":""}`,
			want: ErrCredentialFieldsMissing,
		},
		{
			name: "service_account with a non-string private key",
			raw:  `{"type":"service_account","client_email":"sa@example.iam.gserviceaccount.com","private_key":1}`,
			want: ErrCredentialFieldsMissing,
		},
		{
			name: "service_account with a PKCS#1 private key",
			raw:  serviceAccountJSONWithKey(t, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(testRSAKey(t))})),
			want: nil,
		},
		{
			name: "service_account with key material that will not parse",
			raw:  `{"type":"service_account","client_email":"sa@example.iam.gserviceaccount.com","private_key":"not-a-real-key"}`,
			want: ErrCredentialPrivateKeyUnusable,
		},
		{
			name: "service_account with an ECDSA private key",
			raw:  serviceAccountJSONWithKey(t, testECDSAKeyPEM(t)),
			want: ErrCredentialPrivateKeyUnusable,
		},
		{
			name: "authorized_user naming Google's token endpoint",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","token_uri":"https://oauth2.googleapis.com/token"}`,
			want: nil,
		},
		{
			name: "authorized_user naming a foreign token endpoint",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","token_uri":"https://attacker.example/token"}`,
			want: ErrCredentialTokenEndpoint,
		},
		{
			name: "service_account naming an internal token endpoint",
			raw:  `{"token_uri":"http://169.254.169.254/token",` + strings.TrimPrefix(testServiceAccountJSON(t), "{"),
			want: ErrCredentialTokenEndpoint,
		},
		{
			name: "authorized_user with a non-string token_uri",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","token_uri":7}`,
			want: ErrCredentialTokenEndpoint,
		},
		{
			// encoding/json matches the library's json:"token_uri" tag case-
			// insensitively, so the gate must see the field the same way.
			name: "authorized_user naming a foreign token endpoint under a case variant of the key",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","Token_Uri":"https://attacker.example/token"}`,
			want: ErrCredentialTokenEndpoint,
		},
		{
			name: "service_account naming Google's legacy token endpoint",
			raw:  `{"token_uri":"https://accounts.google.com/o/oauth2/token",` + strings.TrimPrefix(testServiceAccountJSON(t), "{"),
			want: nil,
		},
		{
			// Google's parser treats a top-level web/installed block as an
			// OAuth client configuration and returns a credential with no
			// token source; the gate refuses it up front.
			name: "authorized_user carrying an OAuth client configuration block",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","installed":{"redirect_uris":["http://localhost"]}}`,
			want: ErrCredentialClientConfig,
		},
		{
			name: "authorized_user carrying a web client block under a case variant",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","Web":{"redirect_uris":["http://localhost"]}}`,
			want: ErrCredentialClientConfig,
		},
		{
			// The library's decode is case-insensitive and last-wins, so the
			// key it will actually sign with is the later Private_Key.
			name: "service_account whose later case-variant private_key overrides a good one",
			raw:  strings.TrimSuffix(testServiceAccountJSON(t), "}") + `,"Private_Key":"not-a-real-key"}`,
			want: ErrCredentialPrivateKeyUnusable,
		},
		{
			name: "authorized_user whose fields are all case variants",
			raw:  `{"type":"authorized_user","Client_Id":"a","CLIENT_SECRET":"b","Refresh_Token":"c"}`,
			want: nil,
		},
		{
			// Google's parser decodes quota_project_id as a string; a value of
			// another type fails there, so the gate must fail first.
			name: "authorized_user with a non-string quota_project_id",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","quota_project_id":7}`,
			want: ErrCredentialJSONMalformed,
		},
		{
			name: "service_account with a non-string universe_domain",
			raw:  `{"universe_domain":["googleapis.com"],` + strings.TrimPrefix(testServiceAccountJSON(t), "{"),
			want: ErrCredentialJSONMalformed,
		},
		{
			// The parser decodes every field of its credential file before it
			// looks at type, external-account fields included.
			name: "authorized_user with a non-list delegates",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","delegates":"sa@example.iam.gserviceaccount.com"}`,
			want: ErrCredentialJSONMalformed,
		},
		{
			name: "service_account with a malformed credential_source block",
			raw:  `{"credential_source":{"headers":"x"},` + strings.TrimPrefix(testServiceAccountJSON(t), "{"),
			want: ErrCredentialJSONMalformed,
		},
		{
			name: "authorized_user with a string token_lifetime_seconds",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","service_account_impersonation":{"token_lifetime_seconds":"3600"}}`,
			want: ErrCredentialJSONMalformed,
		},
		{
			name: "authorized_user with a malformed source_credentials block",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","source_credentials":{"client_id":5}}`,
			want: ErrCredentialJSONMalformed,
		},
		{
			name: "service_account with an unrelated field Go cannot represent",
			raw:  `{"x":1e999,` + strings.TrimPrefix(testServiceAccountJSON(t), "{"),
			want: nil,
		},
		{
			name: "empty object",
			raw:  `{}`,
			want: ErrCredentialJSONUntyped,
		},
		{
			name: "json array",
			raw:  `[1,2]`,
			want: ErrCredentialJSONUntyped,
		},
		{
			name: "not json",
			raw:  `not json`,
			want: ErrCredentialJSONInvalid,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckCredentialJSON([]byte(tt.raw))
			if tt.want == nil {
				if err != nil {
					t.Fatalf("CheckCredentialJSON(%q) = %v, want nil", tt.raw, err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("CheckCredentialJSON(%q) = %v, want %v", tt.raw, err, tt.want)
			}
		})
	}
}

func TestCredentialJSONType(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "authorized_user", raw: `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}`, want: "authorized_user"},
		{name: "service_account", raw: testServiceAccountJSON(t), want: "service_account"},
		{name: "external_account", raw: `{"type":"external_account","audience":"x"}`, want: "external_account"},
		{name: "empty object", raw: `{}`, want: ""},
		{name: "json array", raw: `[1,2]`, want: ""},
		{name: "not json", raw: `not json`, want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := CredentialJSONType([]byte(tt.raw)); got != tt.want {
				t.Fatalf("CredentialJSONType(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestCheckCredentialJSONBoundsWhatItEchoes: two refusals name the value
// they refused, and callers render those messages — the TUI keeps one on
// screen after its prompt closes. A document pasted by mistake can carry
// anything in those fields, so the message shows enough of the value to
// identify it and no more.
func TestCheckCredentialJSONBoundsWhatItEchoes(t *testing.T) {
	long := strings.Repeat("S", 400)
	for _, tt := range []struct {
		name, raw string
		want      error
	}{
		{
			name: "unsupported type",
			raw:  `{"type":"` + long + `"}`,
			want: ErrCredentialTypeUnsupported,
		},
		{
			name: "foreign token_uri",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","token_uri":"https://attacker.example/` + long + `"}`,
			want: ErrCredentialTokenEndpoint,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckCredentialJSON([]byte(tt.raw))
			if err == nil {
				t.Fatal("want a refusal")
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %q, want %v", err, tt.want)
			}
			if strings.Contains(err.Error(), long) {
				t.Fatalf("err repeats the whole value: %q", err)
			}
			if len(err.Error()) > 200 {
				t.Fatalf("err is %d bytes, want a bounded message: %q", len(err.Error()), err)
			}
		})
	}
}

// TestCheckCredentialJSONEchoesNothingATerminalWouldObey: the values these
// refusals quote are rendered — the TUI puts one on its error line — and JSON
// may carry control characters raw, C1 among them, which a terminal reads as
// commands. None survives into the message.
func TestCheckCredentialJSONEchoesNothingATerminalWouldObey(t *testing.T) {
	// U+009B is CSI: a terminal treats it as the start of a control sequence.
	// Raw in a JSON string it is legal, since JSON only requires escaping
	// below U+0020.
	for _, tt := range []struct{ name, raw string }{
		{name: "type", raw: "{\"type\":\"bad\u009b2Jstill-bad\"}"},
		{name: "token_uri", raw: "{\"type\":\"authorized_user\",\"client_id\":\"a\",\"client_secret\":\"b\",\"refresh_token\":\"c\",\"token_uri\":\"https://x.example/\u009b2J\u0085\"}"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckCredentialJSON([]byte(tt.raw))
			if err == nil {
				t.Fatal("want a refusal")
			}
			for _, r := range err.Error() {
				if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
					t.Fatalf("err carries control character %U: %q", r, err)
				}
			}
		})
	}
}

// testServiceAccountJSON returns a service_account credential JSON whose
// private_key is a freshly generated PKCS#8 RSA key, so no key material
// lives in the repository. llm/registry and llm/providers/tokenauth share no
// test helper package, so each keeps its own copy.
func testServiceAccountJSON(t *testing.T) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(testRSAKey(t))
	if err != nil {
		t.Fatal(err)
	}
	return serviceAccountJSONWithKey(t, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// serviceAccountJSONWithKey wraps PEM key material in a service_account
// credential JSON; json.Marshal escapes the PEM's newlines.
func serviceAccountJSONWithKey(t *testing.T, keyPEM []byte) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"client_email": "sa@example.iam.gserviceaccount.com",
		"private_key":  string(keyPEM),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// testRSAKey generates a test-only RSA key; 1024 bits is too small to sign
// with in production and is chosen here only for speed.
func testRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// testECDSAKeyPEM returns a PKCS#8 PEM of a freshly generated P-256 key:
// material x509 parses but Google's RSA signer cannot use.
func testECDSAKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}
