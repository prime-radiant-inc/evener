package registry

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
)

func TestCheckCredentialJSON(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
		want string // substring of the error, or "" for nil
	}{
		{
			name: "authorized_user",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}`,
			want: "",
		},
		{
			name: "service_account",
			raw:  testServiceAccountJSON(t),
			want: "",
		},
		{
			name: "external_account",
			raw:  `{"type":"external_account","audience":"x"}`,
			want: "not supported",
		},
		{
			name: "service_account with no fields",
			raw:  `{"type":"service_account"}`,
			want: "service_account credential JSON is missing client_email, private_key",
		},
		{
			name: "authorized_user with only a client id",
			raw:  `{"type":"authorized_user","client_id":"a"}`,
			want: "missing client_secret, refresh_token",
		},
		{
			name: "service_account with an empty private key",
			raw:  `{"type":"service_account","client_email":"sa@example.iam.gserviceaccount.com","private_key":""}`,
			want: "missing private_key",
		},
		{
			name: "service_account with a non-string private key",
			raw:  `{"type":"service_account","client_email":"sa@example.iam.gserviceaccount.com","private_key":1}`,
			want: "missing private_key",
		},
		{
			name: "service_account with a PKCS#1 private key",
			raw:  serviceAccountJSONWithKey(t, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(testRSAKey(t))})),
			want: "",
		},
		{
			name: "service_account with key material that will not parse",
			raw:  `{"type":"service_account","client_email":"sa@example.iam.gserviceaccount.com","private_key":"not-a-real-key"}`,
			want: "unusable private_key",
		},
		{
			name: "service_account with an ECDSA private key",
			raw:  serviceAccountJSONWithKey(t, testECDSAKeyPEM(t)),
			want: "not an RSA key",
		},
		{
			name: "authorized_user naming Google's token endpoint",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","token_uri":"https://oauth2.googleapis.com/token"}`,
			want: "",
		},
		{
			name: "authorized_user naming a foreign token endpoint",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","token_uri":"https://attacker.example/token"}`,
			want: `token_uri "https://attacker.example/token" is not Google's OAuth token endpoint`,
		},
		{
			name: "service_account naming an internal token endpoint",
			raw:  `{"token_uri":"http://169.254.169.254/token",` + strings.TrimPrefix(testServiceAccountJSON(t), "{"),
			want: "is not Google's OAuth token endpoint",
		},
		{
			name: "authorized_user with a non-string token_uri",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","token_uri":7}`,
			want: "is not Google's OAuth token endpoint",
		},
		{
			// encoding/json matches the library's json:"token_uri" tag case-
			// insensitively, so the gate must see the field the same way.
			name: "authorized_user naming a foreign token endpoint under a case variant of the key",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","Token_Uri":"https://attacker.example/token"}`,
			want: "is not Google's OAuth token endpoint",
		},
		{
			name: "service_account naming Google's legacy token endpoint",
			raw:  `{"token_uri":"https://accounts.google.com/o/oauth2/token",` + strings.TrimPrefix(testServiceAccountJSON(t), "{"),
			want: "",
		},
		{
			// Google's parser treats a top-level web/installed block as an
			// OAuth client configuration and returns a credential with no
			// token source; the gate refuses it up front.
			name: "authorized_user carrying an OAuth client configuration block",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","installed":{"redirect_uris":["http://localhost"]}}`,
			want: "OAuth client configuration",
		},
		{
			name: "authorized_user carrying a web client block under a case variant",
			raw:  `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","Web":{"redirect_uris":["http://localhost"]}}`,
			want: "OAuth client configuration",
		},
		{
			name: "service_account with an unrelated field Go cannot represent",
			raw:  `{"x":1e999,` + strings.TrimPrefix(testServiceAccountJSON(t), "{"),
			want: "",
		},
		{
			name: "empty object",
			raw:  `{}`,
			want: `no "type"`,
		},
		{
			name: "json array",
			raw:  `[1,2]`,
			want: `no "type"`,
		},
		{
			name: "not json",
			raw:  `not json`,
			want: "not valid JSON",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckCredentialJSON([]byte(tt.raw))
			if tt.want == "" {
				if err != nil {
					t.Fatalf("CheckCredentialJSON(%q) = %v, want nil", tt.raw, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CheckCredentialJSON(%q) = %v, want an error containing %q", tt.raw, err, tt.want)
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
