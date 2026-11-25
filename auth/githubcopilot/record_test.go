package githubcopilot

import (
	"encoding/json"
	"testing"
	"time"
)

func sampleRecord() AuthRecord {
	return AuthRecord{
		Instance:      "github-copilot",
		Scheme:        AuthScheme,
		Version:       1,
		AccessToken:   "access-token",
		RefreshToken:  "refresh-token",
		TokenType:     "bearer",
		Scope:         "read:user offline_access",
		ObtainedAt:    time.Date(2026, 11, 22, 1, 0, 0, 0, time.UTC),
		AccessExpiry:  time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC),
		RefreshExpiry: time.Date(2027, 5, 21, 1, 0, 0, 0, time.UTC),
		CopilotToken:  "copilot-token",
		CopilotExpiry: time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC),
		AccountID:     "1234567",
		Email:         "user@example.com",
	}
}

func TestAuthSchemeValue(t *testing.T) {
	if AuthScheme != "oauth-github-copilot" {
		t.Fatalf("AuthScheme = %q, want %q", AuthScheme, "oauth-github-copilot")
	}
}

func TestValidateAcceptsCompleteRecord(t *testing.T) {
	if err := sampleRecord().Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestValidateAcceptsOptionalStateUnset(t *testing.T) {
	record := sampleRecord()
	record.Email = ""
	record.RefreshRejected = false
	if err := record.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil with optional state unset", err)
	}
}

func TestValidateAcceptsPersistedRefreshRejection(t *testing.T) {
	record := sampleRecord()
	record.RefreshRejected = true
	if err := record.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil with refresh_rejected set", err)
	}
}

// TestValidateAcceptsNonExpiringGitHubToken pins the Pi-compatible identity's
// current token shape: a GitHub access token with no refresh token and no
// expiry, plus the required Copilot session token and expiry.
func TestValidateAcceptsNonExpiringGitHubToken(t *testing.T) {
	record := sampleRecord()
	record.RefreshToken = ""
	record.AccessExpiry = time.Time{}
	record.RefreshExpiry = time.Time{}
	record.TokenType = ""
	record.Scope = ""
	if err := record.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want a non-expiring GitHub token to be valid", err)
	}
}

// TestNeedsLoginForNonExpiringToken proves a non-expiring GitHub token never
// needs login just because the GitHub refresh fields are empty.
func TestNeedsLoginForNonExpiringToken(t *testing.T) {
	now := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	record := sampleRecord()
	record.RefreshToken = ""
	record.AccessExpiry = time.Time{}
	record.RefreshExpiry = time.Time{}
	if record.NeedsLogin(now) {
		t.Fatal("non-expiring GitHub token reported login required")
	}
	record.RefreshRejected = true
	if !record.NeedsLogin(now) {
		t.Fatal("rejected credential did not require login")
	}
}

func TestValidateRejectsMissingOrWrongRequiredFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*AuthRecord)
	}{
		{"missing instance", func(r *AuthRecord) { r.Instance = "" }},
		{"missing scheme", func(r *AuthRecord) { r.Scheme = "" }},
		{"cross scheme", func(r *AuthRecord) { r.Scheme = "oauth-openai" }},
		{"missing version", func(r *AuthRecord) { r.Version = 0 }},
		{"unsupported version", func(r *AuthRecord) { r.Version = 2 }},
		{"missing access token", func(r *AuthRecord) { r.AccessToken = "" }},
		{"missing copilot token", func(r *AuthRecord) { r.CopilotToken = "" }},
		{"missing copilot expiry", func(r *AuthRecord) { r.CopilotExpiry = time.Time{} }},
		{"missing obtained at", func(r *AuthRecord) { r.ObtainedAt = time.Time{} }},
		{"missing account id", func(r *AuthRecord) { r.AccountID = "" }},
		{"refresh token without expiry", func(r *AuthRecord) { r.RefreshExpiry = time.Time{} }},
		{"refresh expiry without token", func(r *AuthRecord) { r.RefreshToken = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := sampleRecord()
			tc.mutate(&record)
			if err := record.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want a validation error")
			}
		})
	}
}

func TestRecordJSONBindsSchemeAndOmitsSource(t *testing.T) {
	data, err := json.Marshal(sampleRecord())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	for _, key := range []string{
		"instance", "auth_scheme", "version", "access_token", "refresh_token",
		"token_type", "scope", "obtained_at", "access_expiry", "refresh_expiry",
		"copilot_token", "copilot_expiry", "account_id",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("serialized record is missing %q", key)
		}
	}
	if _, ok := raw["source"]; ok {
		t.Error("serialized record carries an OAuth source field, want none")
	}
	if _, ok := raw["refresh_rejected"]; ok {
		t.Error("refresh_rejected serialized while false, want it omitted")
	}
	var scheme string
	if err := json.Unmarshal(raw["auth_scheme"], &scheme); err != nil {
		t.Fatalf("Unmarshal(auth_scheme) error = %v", err)
	}
	if scheme != AuthScheme {
		t.Errorf("auth_scheme = %q, want %q", scheme, AuthScheme)
	}
}
