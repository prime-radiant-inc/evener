package registry

import (
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
			raw:  `{"type":"service_account","client_email":"sa@example.iam.gserviceaccount.com","private_key":"not-a-real-key"}`,
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
			name: "service_account with an unrelated field Go cannot represent",
			raw:  `{"type":"service_account","client_email":"sa@example.iam.gserviceaccount.com","private_key":"not-a-real-key","x":1e999}`,
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
		{name: "service_account", raw: `{"type":"service_account","client_email":"sa@example.iam.gserviceaccount.com","private_key":"not-a-real-key"}`, want: "service_account"},
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
