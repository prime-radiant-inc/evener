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
		{"missing refresh token", func(r *AuthRecord) { r.RefreshToken = "" }},
		{"missing token type", func(r *AuthRecord) { r.TokenType = "" }},
		{"missing scope", func(r *AuthRecord) { r.Scope = "" }},
		{"missing obtained at", func(r *AuthRecord) { r.ObtainedAt = time.Time{} }},
		{"missing access expiry", func(r *AuthRecord) { r.AccessExpiry = time.Time{} }},
		{"missing refresh expiry", func(r *AuthRecord) { r.RefreshExpiry = time.Time{} }},
		{"missing account id", func(r *AuthRecord) { r.AccountID = "" }},
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
		"account_id",
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
