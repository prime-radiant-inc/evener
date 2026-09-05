package registry

import (
	"encoding/json"
	"errors"
	"fmt"
)

// AllowedCredentialJSONTypes are the Google credential JSON shapes evener
// accepts for a gcp-adc instance (spec 2026-09-04 google-vertex-express §4):
// a service-account key or an application-default authorized_user file.
// external_account and other types can name local files or executables as
// credential sources and are refused.
var AllowedCredentialJSONTypes = map[string]bool{"service_account": true, "authorized_user": true}

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
// then an allowed "type". The registry runs it when a gcp-adc instance's
// credentials-store entry resolves; the tokenauth authenticator and the
// hub's evener/auth/credentialJson/set run the identical check, so a value
// the registry resolves is one the authenticator will accept.
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
	return nil
}
