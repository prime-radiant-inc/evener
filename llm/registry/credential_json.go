package registry

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// AllowedCredentialJSONTypes are the Google credential JSON shapes evener
// accepts for a gcp-adc instance (spec 2026-09-04 google-vertex-express §4):
// a service-account key or an application-default authorized_user file.
// external_account and other types can name local files or executables as
// credential sources and are refused.
var AllowedCredentialJSONTypes = map[string]bool{"service_account": true, "authorized_user": true}

// requiredCredentialJSONFields lists, per accepted type, the fields a token
// source needs to mint a token. Google's parser accepts their absence and
// fails only at the first request, so the gate checks them here. Presence is
// not the whole story for a service account: checkServiceAccountKey also
// parses its private_key.
var requiredCredentialJSONFields = map[string][]string{
	"service_account": {"client_email", "private_key"},
	"authorized_user": {"client_id", "client_secret", "refresh_token"},
}

// CredentialJSONType returns a Google credential JSON's "type", or "" when
// raw is not a JSON object carrying a string type.
func CredentialJSONType(raw []byte) string {
	var f struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return ""
	}
	return f.Type
}

// CheckCredentialJSON is the pre-parse gate every stored or pasted Google
// credential must pass before anything mints a token from it: valid JSON,
// then an allowed "type", then the fields that type needs to mint a token,
// and for a service account that its private_key is key material the signer
// can use.
// The registry runs it when a gcp-adc instance's credentials-store entry
// resolves; the tokenauth authenticator and the hub's
// evener/auth/credentialJson/set run the identical check, so a value the
// registry resolves is one the authenticator will accept.
func CheckCredentialJSON(raw []byte) error {
	if !json.Valid(raw) {
		return errors.New("not valid JSON")
	}
	t := CredentialJSONType(raw)
	if t == "" {
		return errors.New(`credential JSON has no "type" field`)
	}
	if !AllowedCredentialJSONTypes[t] {
		return fmt.Errorf("credential type %q is not supported: paste a service-account key or an authorized_user file", t)
	}
	// Raw messages, so an unrelated field Go cannot represent (a number past
	// float64) does not decide the verdict; only the required fields are read.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("credential JSON: %w", err)
	}
	var missing []string
	values := map[string]string{}
	for _, name := range requiredCredentialJSONFields[t] {
		var s string
		if json.Unmarshal(fields[name], &s) != nil || s == "" {
			missing = append(missing, name)
			continue
		}
		values[name] = s
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s credential JSON is missing %s", t, strings.Join(missing, ", "))
	}
	if t == "service_account" {
		if err := checkServiceAccountKey(values["private_key"]); err != nil {
			return fmt.Errorf("service_account credential JSON has an unusable private_key: %w", err)
		}
	}
	return nil
}

// checkServiceAccountKey parses private_key the way Google's signer will
// at the first request — a PEM block if present, then PKCS#8, then
// PKCS#1, and the result must be an RSA key — so unusable key material
// is refused here rather than at the first token request.
func checkServiceAccountKey(pemOrDER string) error {
	key := []byte(pemOrDER)
	if block, _ := pem.Decode(key); block != nil {
		key = block.Bytes
	}
	parsed, err := x509.ParsePKCS8PrivateKey(key)
	if err != nil {
		pkcs1, pkcs1Err := x509.ParsePKCS1PrivateKey(key)
		if pkcs1Err != nil {
			return errors.New("private_key is not a PEM or plain PKCS#8/PKCS#1 key")
		}
		parsed = pkcs1
	}
	if _, ok := parsed.(*rsa.PrivateKey); !ok {
		return errors.New("private_key is not an RSA key")
	}
	return nil
}
