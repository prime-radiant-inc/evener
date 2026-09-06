package tokenauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"testing"

	"golang.org/x/oauth2/google"
	"primeradiant.com/evener/llm/registry"
)

// FuzzCredentialJSON drives the gcp-adc decode+validate boundary
// (registry.CredentialJSONType, isUserCredential, ValidateCredentialJSON) over
// arbitrary bytes. This never calls google.CredentialsFromJSON's network
// path — CredentialsFromJSON only parses its argument and never performs
// I/O — so the target stays offline and deterministic (docs/skills/
// fuzzing-an-api-surface/SKILL.md).
func FuzzCredentialJSON(f *testing.F) {
	f.Add([]byte(`{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}`))
	f.Add(serviceAccountSeed(f))
	f.Add([]byte(`{"type":"external_account","audience":"//iam.googleapis.com/x","subject_token_type":"urn:ietf:params:oauth:token-type:jwt","token_url":"https://sts.googleapis.com/v1/token","credential_source":{"file":"/etc/passwd"}}`))
	f.Add([]byte(`{"type":"service_account"}`))
	f.Add([]byte(`{"type":"authorized_user","client_id":"a"}`))
	// A field the parser decodes but the gate never checks; the oracle
	// below requires the gate to refuse it.
	f.Add([]byte(`{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","delegates":"not-a-list"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"type":1}`))
	f.Add([]byte(`{"type":"authorized_user"`))
	f.Add([]byte(``))
	f.Add([]byte(`not json`))
	f.Add([]byte("\xff"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		err := ValidateCredentialJSON(raw) // oracle: never panics (a fuzz failure here is a crash, not a t.Fatal)
		typ := registry.CredentialJSONType(raw)
		allowed := registry.AllowedCredentialJSONTypes[typ]

		if err == nil && !allowed {
			t.Fatalf("ValidateCredentialJSON(%q) accepted type %q, want it restricted to the allowlist", raw, typ)
		}
		if err == nil && isUserCredential(raw) != (typ == "authorized_user") {
			t.Fatalf("isUserCredential(%q) = %v, want it to agree with registry.CredentialJSONType == authorized_user", raw, isUserCredential(raw))
		}
		if !json.Valid(raw) && err == nil {
			t.Fatalf("ValidateCredentialJSON(%q) accepted invalid JSON", raw)
		}
		if !allowed && err == nil {
			t.Fatalf("ValidateCredentialJSON(%q) accepted an out-of-allowlist type %q", raw, typ)
		}
		// The registry selects a stored credential on CheckCredentialJSON
		// alone, so anything it accepts must be something Google's parser
		// accepts too; otherwise the entry shadows ADC and fails at the
		// first request with no fallback.
		if registry.CheckCredentialJSON(raw) == nil {
			if _, parseErr := google.CredentialsFromJSON(context.Background(), raw, cloudPlatformScope); parseErr != nil { //nolint:staticcheck // the gate mirrors this exact parser
				t.Fatalf("CheckCredentialJSON(%q) accepted a credential the library rejects: %v", raw, parseErr)
			}
		}
	})
}

// serviceAccountSeed is the service_account seed, carrying a freshly
// generated PKCS#8 RSA key so no key material lives in the repository. It
// duplicates testServiceAccountJSON because a seed is built from a
// *testing.F, which that *testing.T helper cannot take.
func serviceAccountSeed(f *testing.F) []byte {
	f.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024) // test-only: small for speed
	if err != nil {
		f.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		f.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"client_email": "sa@example.iam.gserviceaccount.com",
		"token_uri":    "https://oauth2.googleapis.com/token",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	})
	if err != nil {
		f.Fatal(err)
	}
	return raw
}
