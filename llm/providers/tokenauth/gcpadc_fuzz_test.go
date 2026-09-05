package tokenauth

import (
	"encoding/json"
	"testing"
)

// FuzzCredentialJSON drives the gcp-adc decode+validate boundary
// (credentialJSONType, isUserCredential, ValidateCredentialJSON) over
// arbitrary bytes. This never calls google.CredentialsFromJSON's network
// path — CredentialsFromJSON only parses its argument and never performs
// I/O — so the target stays offline and deterministic (docs/skills/
// fuzzing-an-api-surface/SKILL.md).
func FuzzCredentialJSON(f *testing.F) {
	f.Add([]byte(`{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}`))
	f.Add([]byte(`{"type":"service_account","private_key":"not-a-real-key","client_email":"sa@example.iam.gserviceaccount.com","token_uri":"https://oauth2.googleapis.com/token"}`))
	f.Add([]byte(`{"type":"external_account","audience":"//iam.googleapis.com/x","subject_token_type":"urn:ietf:params:oauth:token-type:jwt","token_url":"https://sts.googleapis.com/v1/token","credential_source":{"file":"/etc/passwd"}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"type":1}`))
	f.Add([]byte(`{"type":"authorized_user"`))
	f.Add([]byte(``))
	f.Add([]byte(`not json`))
	f.Add([]byte("\xff"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		err := ValidateCredentialJSON(raw) // oracle: never panics (a fuzz failure here is a crash, not a t.Fatal)
		typ := credentialJSONType(raw)
		allowed := typ == "service_account" || typ == "authorized_user"

		if err == nil && !allowed {
			t.Fatalf("ValidateCredentialJSON(%q) accepted type %q, want it restricted to the allowlist", raw, typ)
		}
		if err == nil && isUserCredential(raw) != (typ == "authorized_user") {
			t.Fatalf("isUserCredential(%q) = %v, want it to agree with credentialJSONType == authorized_user", raw, isUserCredential(raw))
		}
		if !json.Valid(raw) && err == nil {
			t.Fatalf("ValidateCredentialJSON(%q) accepted invalid JSON", raw)
		}
		if !allowed && err == nil {
			t.Fatalf("ValidateCredentialJSON(%q) accepted an out-of-allowlist type %q", raw, typ)
		}
	})
}
