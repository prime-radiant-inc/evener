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

// googleTokenEndpoints are the only token_uri values a credential JSON may
// name: the endpoint Google's library uses by default for both credential
// types, and the legacy one older key files carry.
var googleTokenEndpoints = map[string]bool{
	"https://oauth2.googleapis.com/token":        true,
	"https://accounts.google.com/o/oauth2/token": true,
}

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
	// Every field the gate reads is decoded through a tagged field, the way
	// Google's library reads it: key names match case-insensitively and the
	// last occurrence wins, so the value checked here is the value the
	// library will use. Raw messages, so an unrelated field Go cannot
	// represent (a number past float64) does not decide the verdict.
	var cred struct {
		ClientEmail  json.RawMessage `json:"client_email"`
		PrivateKey   json.RawMessage `json:"private_key"`
		ClientID     json.RawMessage `json:"client_id"`
		ClientSecret json.RawMessage `json:"client_secret"`
		RefreshToken json.RawMessage `json:"refresh_token"`
		TokenURL     json.RawMessage `json:"token_uri"`
		Installed    json.RawMessage `json:"installed"`
		Web          json.RawMessage `json:"web"`
	}
	if err := json.Unmarshal(raw, &cred); err != nil {
		return fmt.Errorf("credential JSON: %w", err)
	}
	fields := map[string]json.RawMessage{
		"client_email": cred.ClientEmail, "private_key": cred.PrivateKey,
		"client_id": cred.ClientID, "client_secret": cred.ClientSecret, "refresh_token": cred.RefreshToken,
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
	// A top-level installed or web block makes the library return an OAuth
	// client configuration with no token source instead of a credential;
	// token_uri is where it sends the refresh token or the signed assertion,
	// so only Google's own endpoints are accepted (absent or null is fine,
	// the library defaults to the first).
	if isJSONValue(cred.Installed) || isJSONValue(cred.Web) {
		return errors.New("credential JSON carries an OAuth client configuration (installed/web), not a credential")
	}
	if isJSONValue(cred.TokenURL) {
		var s string
		if json.Unmarshal(cred.TokenURL, &s) != nil || !googleTokenEndpoints[s] {
			return fmt.Errorf("token_uri %s is not Google's OAuth token endpoint", string(cred.TokenURL))
		}
	}
	// Google's parser decodes its whole credential file, every type's fields
	// included, before it looks at type, and fails on any value of the wrong
	// shape — after the gate had already chosen the credential over ADC. The
	// same decode runs here so the mismatch is refused up front. It comes
	// after the checks above so a field they cover gets their message.
	var typed credentialFileShape
	if err := json.Unmarshal(raw, &typed); err != nil {
		return fmt.Errorf("credential JSON: %w", err)
	}
	if t == "service_account" {
		if err := checkServiceAccountKey(values["private_key"]); err != nil {
			return fmt.Errorf("service_account credential JSON has an unusable private_key: %w", err)
		}
	}
	return nil
}

// credentialFileShape mirrors the struct Google's library decodes a
// credential file into (golang.org/x/oauth2 v0.35.0, google.credentialsFile
// with externalaccount.CredentialSource), field types included, so a value
// the library cannot decode is refused by the gate. Field names are the
// library's; the tokenauth fuzz target pins that the gate never accepts a
// credential the library rejects, which is what catches this drifting.
type credentialFileShape struct {
	Type                           string                  `json:"type"`
	ClientEmail                    string                  `json:"client_email"`
	PrivateKeyID                   string                  `json:"private_key_id"`
	PrivateKey                     string                  `json:"private_key"`
	AuthURL                        string                  `json:"auth_uri"`
	TokenURL                       string                  `json:"token_uri"`
	ProjectID                      string                  `json:"project_id"`
	UniverseDomain                 string                  `json:"universe_domain"`
	ClientSecret                   string                  `json:"client_secret"`
	ClientID                       string                  `json:"client_id"`
	RefreshToken                   string                  `json:"refresh_token"`
	Audience                       string                  `json:"audience"`
	SubjectTokenType               string                  `json:"subject_token_type"`
	TokenURLExternal               string                  `json:"token_url"`
	TokenInfoURL                   string                  `json:"token_info_url"`
	ServiceAccountImpersonationURL string                  `json:"service_account_impersonation_url"`
	ServiceAccountImpersonation    credentialImpersonation `json:"service_account_impersonation"`
	Delegates                      []string                `json:"delegates"`
	CredentialSource               credentialSourceShape   `json:"credential_source"`
	QuotaProjectID                 string                  `json:"quota_project_id"`
	WorkforcePoolUserProject       string                  `json:"workforce_pool_user_project"`
	RevokeURL                      string                  `json:"revoke_url"`
	SourceCredentials              *credentialFileShape    `json:"source_credentials"`
}

type credentialImpersonation struct {
	TokenLifetimeSeconds int `json:"token_lifetime_seconds"`
}

type credentialSourceShape struct {
	File                        string                 `json:"file"`
	URL                         string                 `json:"url"`
	Headers                     map[string]string      `json:"headers"`
	Executable                  *credentialExecutable  `json:"executable"`
	EnvironmentID               string                 `json:"environment_id"`
	RegionURL                   string                 `json:"region_url"`
	RegionalCredVerificationURL string                 `json:"regional_cred_verification_url"`
	IMDSv2SessionTokenURL       string                 `json:"imdsv2_session_token_url"`
	Format                      credentialSourceFormat `json:"format"`
}

type credentialExecutable struct {
	Command       string `json:"command"`
	TimeoutMillis *int   `json:"timeout_millis"`
	OutputFile    string `json:"output_file"`
}

type credentialSourceFormat struct {
	Type                  string `json:"type"`
	SubjectTokenFieldName string `json:"subject_token_field_name"`
}

// isJSONValue reports whether a raw field was present with a value other
// than null, which is what the library's own decoding treats as set.
func isJSONValue(m json.RawMessage) bool {
	return len(m) > 0 && string(m) != "null"
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
