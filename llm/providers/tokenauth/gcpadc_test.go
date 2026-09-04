package tokenauth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestGCPADCAppliesTokenOncePerInstance(t *testing.T) {
	calls := 0
	a := &GCPADC{FindCredentials: func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		calls++
		if len(scopes) != 1 || scopes[0] != cloudPlatformScope {
			t.Fatalf("scopes = %v", scopes)
		}
		return &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "adc-token"})}, nil
	}}
	res := registry.Resolved{Instance: "vertex", Credential: registry.Credential{Source: "adc"}}
	for range 2 {
		req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
		if err := a.Apply(context.Background(), req, res); err != nil {
			t.Fatal(err)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer adc-token" {
			t.Fatalf("Authorization = %q", got)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	_ = a.Apply(context.Background(), req, registry.Resolved{Instance: "google-vertex", Credential: registry.Credential{Source: "adc"}})
	if calls != 2 {
		t.Fatalf("FindDefaultCredentials called %d times, want once per instance", calls)
	}
}

func TestGCPADCReportsMissingCredentials(t *testing.T) {
	a := &GCPADC{FindCredentials: func(context.Context, ...string) (*google.Credentials, error) {
		return nil, errors.New("could not find default credentials")
	}}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	err := a.Apply(context.Background(), req, registry.Resolved{Instance: "vertex"})
	var cfg *llm.ConfigurationError
	if !errors.As(err, &cfg) || !strings.Contains(err.Error(), "vertex") || !strings.Contains(err.Error(), "default credentials") {
		t.Fatalf("err = %v", err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("no header on failure")
	}
}

func TestDefaultsAreRegistered(t *testing.T) {
	if a, ok := llm.AuthenticatorFor(registry.AuthGCPADC); !ok || a != llm.Authenticator(DefaultGCPADC) {
		t.Fatal("gcp-adc not registered as DefaultGCPADC")
	}
	if p, ok := llm.RequestPreparerFor(registry.AuthOAuthOpenAICodex); !ok || !p.RequiresStreamingComplete() {
		t.Fatal("oauth-openai-codex not registered as a streaming-complete preparer")
	}
}

func staticADC() *GCPADC {
	return &GCPADC{FindCredentials: func(context.Context, ...string) (*google.Credentials, error) {
		return &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "adc-token"})}, nil
	}}
}

func TestGCPADCSetsQuotaProjectFromTransportVars(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://x", nil)
	res := registry.Resolved{Instance: "vertex", Credential: registry.Credential{Source: "adc"},
		Transport: registry.Transport{Vars: map[string]string{"GOOGLE_VERTEX_PROJECT": "my-project", "GOOGLE_VERTEX_LOCATION": "global"}}}
	if err := staticADC().Apply(context.Background(), req, res); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("x-goog-user-project"); got != "my-project" {
		t.Fatalf("x-goog-user-project = %q, want my-project (the listing 403s without it)", got)
	}
}

func TestGCPADCOmitsQuotaProjectWithoutTheVariable(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://x", nil)
	if err := staticADC().Apply(context.Background(), req, registry.Resolved{Instance: "vertex", Credential: registry.Credential{Source: "adc"}}); err != nil {
		t.Fatal(err)
	}
	if _, set := req.Header["X-Goog-User-Project"]; set {
		t.Fatalf("header set without a project: %v", req.Header)
	}
}

const storedUserJSON = `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}`

func storedRes(instance, value string) registry.Resolved {
	return registry.Resolved{Instance: instance, Credential: registry.Credential{Value: value, Source: "store"}}
}

func TestGCPADCUsesStoredJSONAndCachesByValue(t *testing.T) {
	var fromJSON [][]byte
	finds := 0
	a := &GCPADC{
		FindCredentials: func(context.Context, ...string) (*google.Credentials, error) {
			finds++
			return nil, errors.New("must not be consulted for a stored credential")
		},
		CredentialsFromJSON: func(_ context.Context, data []byte, scopes ...string) (*google.Credentials, error) {
			if len(scopes) != 1 || scopes[0] != cloudPlatformScope {
				t.Fatalf("scopes = %v", scopes)
			}
			fromJSON = append(fromJSON, data)
			return &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "json-token"})}, nil
		},
	}
	for range 2 {
		req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
		if err := a.Apply(context.Background(), req, storedRes("vertex", storedUserJSON)); err != nil {
			t.Fatal(err)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer json-token" {
			t.Fatalf("Authorization = %q", got)
		}
	}
	if finds != 0 || len(fromJSON) != 1 || string(fromJSON[0]) != storedUserJSON {
		t.Fatalf("finds=%d fromJSON=%q; want the JSON parsed once and ADC never consulted", finds, fromJSON)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	if err := a.Apply(context.Background(), req, storedRes("vertex", `{"type":"authorized_user","client_id":"other"}`)); err != nil {
		t.Fatal(err)
	}
	if len(fromJSON) != 2 {
		t.Fatalf("a changed stored value must rebuild the token source; parses = %d", len(fromJSON))
	}
}

func TestGCPADCReportsMalformedStoredJSON(t *testing.T) {
	a := &GCPADC{CredentialsFromJSON: func(context.Context, []byte, ...string) (*google.Credentials, error) {
		return nil, errors.New("invalid character")
	}}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	err := a.Apply(context.Background(), req, storedRes("vertex", "{"))
	var cfg *llm.ConfigurationError
	if !errors.As(err, &cfg) || !strings.Contains(err.Error(), "vertex") || !strings.Contains(err.Error(), "stored credential") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateCredentialJSON(t *testing.T) {
	if err := ValidateCredentialJSON([]byte(storedUserJSON)); err != nil {
		t.Fatalf("authorized_user JSON rejected: %v", err)
	}
	if err := ValidateCredentialJSON([]byte(`{"type":"authorized_user"`)); err == nil {
		t.Fatal("truncated JSON accepted")
	}
	if err := ValidateCredentialJSON([]byte(`{"hello":"world"}`)); err == nil {
		t.Fatal("JSON with no credential type accepted")
	}
}
