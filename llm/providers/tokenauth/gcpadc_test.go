package tokenauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// The credential is read per request — that is what lets a re-login reach the
// next request — but an unchanged credential keeps the cached source, so the
// token it minted is the one every request carries, and each instance has its
// own source.
func TestGCPADCCachesPerInstanceWhileTheCredentialStands(t *testing.T) {
	calls := 0
	minted := 0
	credential := `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}`
	a := &GCPADC{FindCredentials: func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		calls++
		if len(scopes) != 1 || scopes[0] != cloudPlatformScope {
			t.Fatalf("scopes = %v", scopes)
		}
		minted++
		return &google.Credentials{
			JSON:        []byte(credential),
			TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: fmt.Sprintf("adc-token-%d", minted)}),
		}, nil
	}}
	res := registry.Resolved{Instance: "vertex", Credential: registry.Credential{Source: "adc"}}
	for range 2 {
		req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
		if err := a.Apply(context.Background(), req, res); err != nil {
			t.Fatal(err)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer adc-token-1" {
			t.Fatalf("Authorization = %q, want the cached source's token", got)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	if err := a.Apply(context.Background(), req, registry.Resolved{Instance: "google-vertex", Credential: registry.Credential{Source: "adc"}}); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got == "Bearer adc-token-1" {
		t.Fatalf("Authorization = %q, want the second instance's own source", got)
	}
	if calls != 3 {
		t.Fatalf("FindDefaultCredentials called %d times, want one read per request", calls)
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
	// The remedy names every way the registry can resolve a gcp-adc
	// credential, including a stored credential JSON, not just gcloud.
	if !strings.Contains(err.Error(), "store a credential JSON") {
		t.Fatalf("err = %v, want the remedy to offer a stored credential JSON", err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("no header on failure")
	}
}

// failingTokenSource fails Token() the way a credential refresh does.
type failingTokenSource struct{ err error }

func (f failingTokenSource) Token() (*oauth2.Token, error) { return nil, f.err }

// A refresh the token endpoint refused is a credential problem, not an
// unreachable endpoint: the hub's credential test reports the class, and
// "sign in again" is the remedy it offers for it.
func TestGCPADCReportsARefusedRefreshAsAuthentication(t *testing.T) {
	refused := &oauth2.RetrieveError{ErrorCode: "invalid_grant", ErrorDescription: "Token has been expired or revoked."}
	a := &GCPADC{FindCredentials: func(context.Context, ...string) (*google.Credentials, error) {
		return &google.Credentials{JSON: []byte(storedUserJSON), TokenSource: failingTokenSource{err: refused}}, nil
	}}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	err := a.Apply(context.Background(), req, registry.Resolved{Instance: "vertex", Credential: registry.Credential{Source: "adc"}})
	if err == nil {
		t.Fatal("Apply accepted a refused refresh")
	}
	if llm.Kind(err) != llm.KindAuthentication {
		t.Fatalf("Kind = %v, want authentication: the hub reports any other class as an unreachable endpoint", llm.Kind(err))
	}
	if !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the oauth error in the chain", err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("no header on failure")
	}
}

// A token that could not be minted because the token endpoint was unreachable
// keeps its transport meaning: nothing says the credential is bad.
func TestGCPADCAFailingTransportIsNotAnAuthFailure(t *testing.T) {
	unreachable := errors.New("dial tcp 169.254.169.254:80: connect: no route to host")
	a := &GCPADC{FindCredentials: func(context.Context, ...string) (*google.Credentials, error) {
		return &google.Credentials{TokenSource: failingTokenSource{err: unreachable}}, nil
	}}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	err := a.Apply(context.Background(), req, registry.Resolved{Instance: "vertex", Credential: registry.Credential{Source: "adc"}})
	if err == nil {
		t.Fatal("Apply accepted a failed token")
	}
	if llm.Kind(err) == llm.KindAuthentication {
		t.Fatalf("Kind = authentication for a transport failure: %v", err)
	}
	if !errors.Is(err, unreachable) {
		t.Fatalf("err = %v, want the transport error in the chain", err)
	}
}

// The token endpoint reports its own transient conditions through the same
// RFC 6749 error field (temporarily_unavailable on a 503, server_error): those
// are not verdicts on the credential, and must keep their retryable meaning
// rather than telling the reader to sign in again.
func TestGCPADCATransientTokenFailureKeepsItsRetryableMeaning(t *testing.T) {
	transient := &oauth2.RetrieveError{ErrorCode: "temporarily_unavailable", ErrorDescription: "The authorization server is currently unable to handle the request."}
	a := &GCPADC{FindCredentials: func(context.Context, ...string) (*google.Credentials, error) {
		return &google.Credentials{JSON: []byte(storedUserJSON), TokenSource: failingTokenSource{err: transient}}, nil
	}}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	err := a.Apply(context.Background(), req, registry.Resolved{Instance: "vertex", Credential: registry.Credential{Source: "adc"}})
	if err == nil {
		t.Fatal("Apply accepted a failed token")
	}
	if llm.Kind(err) == llm.KindAuthentication {
		t.Fatalf("Kind = authentication for a transient token failure: %v", err)
	}
	if got := llm.Classify(err); got != llm.ErrorClassRetryable {
		t.Fatalf("Classify = %v, want retryable: a token endpoint that is briefly unavailable is worth retrying", got)
	}
	var cfg *llm.ConfigurationError
	if errors.As(err, &cfg) {
		t.Fatalf("a transient token failure was reported as configuration: %v", err)
	}
}

// A stored credential the token endpoint refuses is reported as a refused
// credential too, with the stored JSON named as the thing to replace.
func TestGCPADCReportsAStoredCredentialRefusal(t *testing.T) {
	refused := &oauth2.RetrieveError{ErrorCode: "invalid_grant", ErrorDescription: "Token has been expired or revoked."}
	a := &GCPADC{
		CredentialsFromJSON: func(context.Context, []byte, ...string) (*google.Credentials, error) {
			return &google.Credentials{JSON: []byte(storedUserJSON), TokenSource: failingTokenSource{err: refused}}, nil
		},
		FindCredentials: func(context.Context, ...string) (*google.Credentials, error) {
			t.Fatal("a stored credential must not fall back to application-default credentials")
			return nil, errors.New("unreachable")
		},
	}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	err := a.Apply(context.Background(), req, storedRes("vertex", storedUserJSON))
	if err == nil {
		t.Fatal("Apply accepted a refused stored credential")
	}
	if llm.Kind(err) != llm.KindAuthentication {
		t.Fatalf("Kind = %v, want authentication", llm.Kind(err))
	}
	if !strings.Contains(err.Error(), "stored credential JSON") {
		t.Fatalf("err = %v, want the stored credential named", err)
	}
	if !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the oauth error in the chain", err)
	}
}

// A credential replaced under a long-running process (a re-login rewrites the
// ADC file) must reach the next request: the cached source is dropped when the
// credential it was built from no longer matches, while an unchanged
// credential keeps it.
func TestGCPADCRebuildsADCSourceWhenTheCredentialFileChanges(t *testing.T) {
	credential := `{"type":"authorized_user","client_id":"c","client_secret":"s","refresh_token":"first"}`
	token := "token-first"
	a := &GCPADC{FindCredentials: func(context.Context, ...string) (*google.Credentials, error) {
		return &google.Credentials{
			JSON:        []byte(credential),
			TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
		}, nil
	}}
	res := registry.Resolved{Instance: "vertex", Credential: registry.Credential{Source: "adc"}}
	apply := func() string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
		if err := a.Apply(context.Background(), req, res); err != nil {
			t.Fatal(err)
		}
		return req.Header.Get("Authorization")
	}
	if got := apply(); got != "Bearer token-first" {
		t.Fatalf("Authorization = %q", got)
	}
	// Re-reading the same file must not disturb the cached source.
	token = "token-reread"
	if got := apply(); got != "Bearer token-first" {
		t.Fatalf("Authorization = %q, want the cached source's token", got)
	}
	// The re-login: a new credential on disk, which the next request must use.
	credential = `{"type":"authorized_user","client_id":"c","client_secret":"s","refresh_token":"second"}`
	token = "token-second"
	if got := apply(); got != "Bearer token-second" {
		t.Fatalf("Authorization = %q, want the replaced credential's token", got)
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
		return &google.Credentials{
			JSON:        []byte(`{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}`),
			TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "adc-token"}),
		}, nil
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

func TestGCPADCOmitsQuotaProjectForServiceAccounts(t *testing.T) {
	a := &GCPADC{FindCredentials: func(context.Context, ...string) (*google.Credentials, error) {
		return &google.Credentials{
			JSON:        []byte(`{"type":"service_account","client_email":"sa@example.iam.gserviceaccount.com"}`),
			TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "sa-token"}),
		}, nil
	}}
	req, _ := http.NewRequest(http.MethodGet, "https://x", nil)
	res := registry.Resolved{Instance: "vertex", Credential: registry.Credential{Source: "adc"},
		Transport: registry.Transport{Vars: map[string]string{"GOOGLE_VERTEX_PROJECT": "my-project"}}}
	if err := a.Apply(context.Background(), req, res); err != nil {
		t.Fatal(err)
	}
	if _, set := req.Header["X-Goog-User-Project"]; set {
		t.Fatalf("header set for a service account: %v", req.Header)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sa-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestGCPADCOmitsQuotaProjectWithoutCredentialJSON(t *testing.T) {
	a := &GCPADC{FindCredentials: func(context.Context, ...string) (*google.Credentials, error) {
		return &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "metadata-token"})}, nil
	}}
	req, _ := http.NewRequest(http.MethodGet, "https://x", nil)
	res := registry.Resolved{Instance: "vertex", Credential: registry.Credential{Source: "adc"},
		Transport: registry.Transport{Vars: map[string]string{"GOOGLE_VERTEX_PROJECT": "my-project"}}}
	if err := a.Apply(context.Background(), req, res); err != nil {
		t.Fatal(err)
	}
	if _, set := req.Header["X-Goog-User-Project"]; set {
		t.Fatalf("header set without credential JSON: %v", req.Header)
	}
}

const storedUserJSON = `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}`

func storedRes(instance, value string) registry.Resolved {
	return registry.Resolved{Instance: instance, Credential: registry.Credential{Value: value, Source: "store"},
		Transport: registry.Transport{Vars: map[string]string{"GOOGLE_VERTEX_PROJECT": "my-project"}}}
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
			return &google.Credentials{JSON: data, TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "json-token"})}, nil
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
		if got := req.Header.Get("x-goog-user-project"); got != "my-project" {
			t.Fatalf("x-goog-user-project = %q, want my-project for a stored authorized_user credential", got)
		}
	}
	if finds != 0 || len(fromJSON) != 1 || string(fromJSON[0]) != storedUserJSON {
		t.Fatalf("finds=%d fromJSON=%q; want the JSON parsed once and ADC never consulted", finds, fromJSON)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	if err := a.Apply(context.Background(), req, storedRes("vertex", `{"type":"authorized_user","client_id":"other","client_secret":"b","refresh_token":"c"}`)); err != nil {
		t.Fatal(err)
	}
	if len(fromJSON) != 2 {
		t.Fatalf("a changed stored value must rebuild the token source; parses = %d", len(fromJSON))
	}
}

func TestGCPADCRebuildsWhenTheCredentialSourceChanges(t *testing.T) {
	// The registry re-resolves an instance with no credential once its ADC
	// file is gone; the cached ADC source must not keep answering for it.
	adcGone := false
	a := &GCPADC{
		FindCredentials: func(context.Context, ...string) (*google.Credentials, error) {
			if adcGone {
				return nil, errors.New("could not find default credentials")
			}
			return &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "adc-token"})}, nil
		},
	}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	if err := a.Apply(context.Background(), req, registry.Resolved{Instance: "vertex", Credential: registry.Credential{Source: "adc"}}); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer adc-token" {
		t.Fatalf("Authorization = %q", got)
	}
	adcGone = true
	req2, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	err := a.Apply(context.Background(), req2, registry.Resolved{Instance: "vertex", Credential: registry.Credential{Source: "none"}})
	if err == nil {
		t.Fatalf("Apply reused the cached ADC source for a resolution with no credential; Authorization = %q", req2.Header.Get("Authorization"))
	}
	if got := req2.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want none once the credential is gone", got)
	}
}

func TestGCPADCClassifiesAStoredCredentialFromTheStoredJSON(t *testing.T) {
	// The parser seam returns no JSON echo; the stored value itself must
	// decide that this is a user credential and so needs the quota header.
	a := &GCPADC{
		CredentialsFromJSON: func(context.Context, []byte, ...string) (*google.Credentials, error) {
			return &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "json-token"})}, nil
		},
	}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	if err := a.Apply(context.Background(), req, storedRes("vertex", storedUserJSON)); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("x-goog-user-project"); got != "my-project" {
		t.Fatalf("x-goog-user-project = %q, want my-project: a stored authorized_user credential is classified from the stored JSON, not from what the parser echoes", got)
	}
}

func TestGCPADCReplacesStoredSourceOnRotation(t *testing.T) {
	var fromJSON [][]byte
	a := &GCPADC{
		CredentialsFromJSON: func(_ context.Context, data []byte, scopes ...string) (*google.Credentials, error) {
			if len(scopes) != 1 || scopes[0] != cloudPlatformScope {
				t.Fatalf("scopes = %v", scopes)
			}
			fromJSON = append(fromJSON, data)
			return &google.Credentials{JSON: data, TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "json-token"})}, nil
		},
	}
	const valueB = `{"type":"authorized_user","client_id":"other","client_secret":"b","refresh_token":"c"}`
	checkOneEntry := func(value string) {
		t.Helper()
		a.mu.Lock()
		defer a.mu.Unlock()
		if len(a.sources) != 1 {
			t.Fatalf("len(a.sources) = %d, want 1", len(a.sources))
		}
		if got, want := a.sources["vertex"].identity, credentialIdentity("store", []byte(value)); got != want {
			t.Fatalf("identity = %q, want %q", got, want)
		}
	}
	for _, value := range []string{storedUserJSON, valueB, storedUserJSON} {
		req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
		if err := a.Apply(context.Background(), req, storedRes("vertex", value)); err != nil {
			t.Fatal(err)
		}
		checkOneEntry(value)
	}
	if len(fromJSON) != 3 {
		t.Fatalf("parses = %d, want 3 (A dropped when B replaced it, so it is parsed again)", len(fromJSON))
	}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	if err := a.Apply(context.Background(), req, storedRes("vertex-2", storedUserJSON)); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sources) != 2 {
		t.Fatalf("len(a.sources) = %d, want 2", len(a.sources))
	}
}

func TestGCPADCReplacesADCSourceWhenAStoredCredentialArrives(t *testing.T) {
	a := &GCPADC{
		FindCredentials: func(context.Context, ...string) (*google.Credentials, error) {
			return &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "adc-token"})}, nil
		},
		CredentialsFromJSON: func(_ context.Context, data []byte, _ ...string) (*google.Credentials, error) {
			return &google.Credentials{JSON: data, TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "json-token"})}, nil
		},
	}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	if err := a.Apply(context.Background(), req, registry.Resolved{Instance: "vertex", Credential: registry.Credential{Source: "adc"}}); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer adc-token" {
		t.Fatalf("Authorization = %q", got)
	}
	req2, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	if err := a.Apply(context.Background(), req2, storedRes("vertex", storedUserJSON)); err != nil {
		t.Fatal(err)
	}
	if got := req2.Header.Get("Authorization"); got != "Bearer json-token" {
		t.Fatalf("Authorization = %q, want the stored source's token", got)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sources) != 1 {
		t.Fatalf("len(a.sources) = %d, want 1", len(a.sources))
	}
	if !strings.HasPrefix(a.sources["vertex"].identity, "store") {
		t.Fatalf("identity = %q after a stored credential replaced the ADC source, want the store's", a.sources["vertex"].identity)
	}
}

func TestGCPADCReportsMalformedStoredJSON(t *testing.T) {
	a := &GCPADC{CredentialsFromJSON: func(context.Context, []byte, ...string) (*google.Credentials, error) {
		t.Fatal("non-JSON must be rejected before the CredentialsFromJSON seam is called")
		return nil, nil
	}}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	err := a.Apply(context.Background(), req, storedRes("vertex", "{"))
	var cfg *llm.ConfigurationError
	if !errors.As(err, &cfg) || !strings.Contains(err.Error(), "vertex") || !strings.Contains(err.Error(), "stored credential") || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err = %v", err)
	}
}

const externalAccountJSON = `{"type":"external_account","audience":"//iam.googleapis.com/x","subject_token_type":"urn:ietf:params:oauth:token-type:jwt","token_url":"https://sts.googleapis.com/v1/token","credential_source":{"file":"/etc/passwd"}}`

func TestValidateCredentialJSON(t *testing.T) {
	if err := ValidateCredentialJSON([]byte(storedUserJSON)); err != nil {
		t.Fatalf("authorized_user JSON rejected: %v", err)
	}
	if err := ValidateCredentialJSON([]byte(`{"type":"authorized_user"`)); err == nil {
		t.Fatal("truncated JSON accepted")
	}
	if err := ValidateCredentialJSON([]byte(`{"hello":"world"}`)); err == nil {
		t.Fatal("JSON with no credential type accepted")
	} else if !strings.Contains(err.Error(), `no "type"`) {
		t.Fatalf("err = %v, want it to name the missing \"type\" field", err)
	}
	if err := ValidateCredentialJSON([]byte(externalAccountJSON)); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("err = %v, want an external_account document rejected as not supported", err)
	}
	if err := ValidateCredentialJSON([]byte(`{"type":"service_account"}`)); err == nil || !strings.Contains(err.Error(), "missing client_email") {
		t.Fatalf("err = %v, want a service_account with no key material refused", err)
	}
	if err := ValidateCredentialJSON([]byte(testServiceAccountJSON(t))); err != nil {
		t.Fatalf("service_account JSON rejected: %v", err)
	}
	// Google's signer reads the key only when it mints the first token, so
	// the gate parses it here instead.
	const unusableKeyJSON = `{"type":"service_account","private_key":"not-a-real-key","client_email":"sa@example.iam.gserviceaccount.com","token_uri":"https://oauth2.googleapis.com/token"}`
	if err := ValidateCredentialJSON([]byte(unusableKeyJSON)); err == nil || !strings.Contains(err.Error(), "unusable private_key") {
		t.Fatalf("err = %v, want a service_account whose key will not parse refused", err)
	}
	// The token endpoint is where the refresh token or the signed assertion
	// goes; a credential naming any other must be refused.
	const foreignEndpointJSON = `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","token_uri":"https://attacker.example/token"}`
	if err := ValidateCredentialJSON([]byte(foreignEndpointJSON)); err == nil || !strings.Contains(err.Error(), "token_uri") {
		t.Fatalf("err = %v, want a credential naming a foreign token endpoint refused", err)
	}
	// The library matches its json tags case-insensitively, so a case
	// variant of the key must be refused the same way.
	const caseVariantJSON = `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","Token_Uri":"https://attacker.example/token"}`
	if err := ValidateCredentialJSON([]byte(caseVariantJSON)); err == nil || !strings.Contains(err.Error(), "token_uri") {
		t.Fatalf("err = %v, want a foreign token endpoint under a case variant of the key refused", err)
	}
	// A top-level installed/web block makes the library build an OAuth
	// client configuration with no token source instead of a credential.
	const clientConfigJSON = `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c","installed":{"redirect_uris":["http://localhost"]}}`
	if err := ValidateCredentialJSON([]byte(clientConfigJSON)); err == nil || !strings.Contains(err.Error(), "OAuth client configuration") {
		t.Fatalf("err = %v, want an OAuth client configuration refused", err)
	}
}

// testServiceAccountJSON returns a service_account credential JSON whose
// private_key is a freshly generated PKCS#8 RSA key, so no key material
// lives in the repository. llm/providers/tokenauth and llm/registry share no
// test helper package, so each keeps its own copy.
func testServiceAccountJSON(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024) // test-only: small for speed
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"client_email": "sa@example.iam.gserviceaccount.com",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestGCPADCRejectsUnsupportedStoredCredentialType(t *testing.T) {
	a := &GCPADC{CredentialsFromJSON: func(context.Context, []byte, ...string) (*google.Credentials, error) {
		t.Fatal("the allowlist must reject external_account before the CredentialsFromJSON seam is called")
		return nil, nil
	}}
	req, _ := http.NewRequest(http.MethodPost, "https://x", nil)
	err := a.Apply(context.Background(), req, storedRes("vertex", externalAccountJSON))
	var cfg *llm.ConfigurationError
	if !errors.As(err, &cfg) || !strings.Contains(err.Error(), "vertex") || !strings.Contains(err.Error(), "stored credential") || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("err = %v", err)
	}
}
