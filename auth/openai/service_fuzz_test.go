package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// authfz_maxFuzzInput bounds the credential-file and refresh-response byte
// slices so a pathological input cannot exhaust memory. Real records and token
// replies are well under a kilobyte; 64 KiB is generous headroom.
const authfz_maxFuzzInput = 1 << 16

// authfz_issuer is a syntactically valid but non-routable issuer base URL. The
// fake RoundTripper intercepts every request, so this is never dialed; it exists
// only so http.NewRequestWithContext can build a request without a real host.
const authfz_issuer = "http://serf-authfz.invalid"

// authfz_instance is the instance name whose credential file the fuzz harness
// seeds and resolves.
const authfz_instance = "openai"

// authfz_fakeRoundTripper is the injection point for adversarial token-refresh
// bytes. It is the code-under-test's only window to the network, so it must obey
// the http.RoundTripper contract exactly: drain and close the request body, and
// return a non-nil response XOR a non-nil error (never both, never neither).
//
// The fuzzed logic being exercised is the REAL RefreshToken -> exchangeTokenRequest
// -> compactTokenError / intoTokenSet decode path; this transport only decides
// which reply bytes and status code that real code sees.
type authfz_fakeRoundTripper struct {
	status int
	body   []byte
	// networkErr, when true, models a transport failure (dial/read error): the
	// RoundTripper returns a non-nil error and a nil response, exercising the
	// client.Do error branch and the transient (non-permanent) refresh failure.
	networkErr bool
}

func (rt authfz_fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Honor the contract: always drain and close the request body regardless of
	// which branch we take, so the caller's body writer never blocks.
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}

	if rt.networkErr {
		return nil, &authfz_transportError{}
	}

	return &http.Response{
		StatusCode: rt.status,
		Status:     http.StatusText(rt.status),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(rt.body))),
		Request:    req,
	}, nil
}

// authfz_transportError is a plain non-permanent error (its message contains
// none of the invalid_grant / invalid_request / unauthorized_client /
// access_denied markers), so isPermanentRefreshError classifies it as transient.
type authfz_transportError struct{}

func (*authfz_transportError) Error() string { return "authfz simulated transport failure" }

// authfz_newResolveService is the reusable mock factory. It builds a real
// Service wired to a fake RoundTripper carrying the fuzzed refresh bytes, and a
// t.TempDir() state directory seeded with the fuzzed credential-file bytes. The
// Service keeps NewService's production refreshToken (the real RefreshToken), so
// resolving through it exercises the genuine HTTP-refresh-and-decode path; only
// the clock (now) is overridden to make expiry decisions reproducible.
//
// The returned stateDir lives under t.TempDir(), so every disk write the code
// performs (SaveAuth's atomic temp-file rename) is contained to the test sandbox.
func authfz_newResolveService(t *testing.T, credFile, refreshBody []byte, status int, networkErr bool, now time.Time) (*Service, string) {
	t.Helper()

	stateDir := t.TempDir()
	authPath := AuthFilePath(stateDir, authfz_instance)
	if err := os.MkdirAll(filepath.Dir(authPath), 0o755); err != nil {
		t.Fatalf("authfz: mkdir auth dir: %v", err)
	}
	if err := os.WriteFile(authPath, credFile, 0o600); err != nil {
		t.Fatalf("authfz: seed credential file: %v", err)
	}

	cfg := DefaultConfig()
	cfg.IssuerBaseURL = authfz_issuer
	client := &http.Client{Transport: authfz_fakeRoundTripper{
		status:     status,
		body:       refreshBody,
		networkErr: networkErr,
	}}

	svc := NewService(cfg, client)
	svc.now = func() time.Time { return now }
	return svc, stateDir
}

// authfz_resolveStatus normalizes the fuzzed status selector into a decision:
// selector 0 means "the transport fails" (network error), otherwise an HTTP
// status in [200, 599] steered to reach both the 2xx decode branch and the
// non-2xx compactTokenError branch.
func authfz_resolveStatus(sel byte) (status int, networkErr bool) {
	if sel == 0 {
		return 0, true
	}
	return 200 + int(sel)%400, false
}

// FuzzResolveRuntimeCredentials drives the REAL credential-resolution and
// token-refresh logic in Service.ResolveRuntimeCredentials: LoadAuth's
// credential-file parse/validate, the needsRefresh expiry math, the genuine
// RefreshToken HTTP call decoded from fuzzed reply bytes (2xx intoTokenSet or
// non-2xx compactTokenError), refreshedAuthRecord merging, and SaveAuth. The
// fake transport and fuzzed credential file are only injection points; the
// production auth code is what is under test.
//
// Oracles (beyond never-panic):
//   - Error discipline: a non-nil error always comes with the zero-value
//     RuntimeCredentials (no partial credential leak on any failure path).
//   - Well-formed success: a nil error yields Source oauth or env; for an OAuth
//     result the returned BearerToken and Expiry match what was persisted to
//     disk, proving the decoded refresh token is the one actually used.
//   - Determinism: resolving the same inputs against a freshly seeded state
//     directory produces the same error-ness, Source, and BearerToken.
func FuzzResolveRuntimeCredentials(f *testing.F) {
	validRecord := func(expiry time.Time) []byte {
		rec := AuthRecord{
			Version:      1,
			Provider:     "openai",
			Source:       AuthSourceOAuth,
			ObtainedAt:   time.Date(2026, 5, 7, 23, 0, 0, 0, time.UTC),
			TokenType:    "Bearer",
			Scope:        "openid profile email offline_access",
			AccessToken:  "stored-access-token",
			RefreshToken: "stored-refresh-token",
			IDToken:      "id-token",
			Expiry:       expiry,
		}
		data, _ := json.Marshal(rec)
		return data
	}
	okBody := []byte(`{"access_token":"fresh","refresh_token":"rt2","token_type":"Bearer","scope":"openid","expires_in":3600,"id_token":"id2"}`)

	// Expired record + healthy 2xx refresh: the core refresh-and-decode path.
	f.Add(validRecord(time.Date(2026, 5, 7, 23, 59, 0, 0, time.UTC)), okBody, byte(1), int64(0))
	// Fresh record: no refresh, returns the stored token straight through.
	f.Add(validRecord(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)), okBody, byte(1), int64(0))
	// Expired record + non-2xx invalid_grant: permanent-refresh -> login required.
	f.Add(validRecord(time.Date(2026, 5, 7, 23, 59, 0, 0, time.UTC)), []byte(`{"error":"invalid_grant"}`), byte(200), int64(0))
	// Expired record + transport error: transient refresh failure.
	f.Add(validRecord(time.Date(2026, 5, 7, 23, 59, 0, 0, time.UTC)), okBody, byte(0), int64(0))
	// Corrupt / non-JSON credential file.
	f.Add([]byte(`not json`), okBody, byte(1), int64(0))
	f.Add([]byte(``), okBody, byte(1), int64(0))
	f.Add([]byte(`{}`), okBody, byte(1), int64(0))
	// Valid record but no refresh token, already expired -> cannot refresh.
	f.Add([]byte(`{"version":1,"source":"oauth","token_type":"Bearer","access_token":"a","refresh_token":"","expiry":"2026-05-07T23:00:00Z","obtained_at":"2026-05-07T23:00:00Z"}`), okBody, byte(1), int64(0))
	// 2xx refresh with empty body / missing fields.
	f.Add(validRecord(time.Date(2026, 5, 7, 23, 59, 0, 0, time.UTC)), []byte(`{}`), byte(1), int64(3600))
	// Expired record + 2xx refresh whose id_token is a parseable JWT, so the
	// post-refresh applyClaims merge runs on decoded display metadata.
	f.Add(validRecord(time.Date(2026, 5, 7, 23, 59, 0, 0, time.UTC)),
		[]byte(`{"access_token":"fresh","token_type":"Bearer","expires_in":3600,"id_token":"a.eyJlbWFpbCI6ImpAZXhhbXBsZS5jb20ifQ.sig"}`),
		byte(1), int64(0))

	f.Fuzz(func(t *testing.T, credFile, refreshBody []byte, statusSel byte, nowOffset int64) {
		if len(credFile) > authfz_maxFuzzInput || len(refreshBody) > authfz_maxFuzzInput {
			t.Skip()
		}
		// Keep OPENAI_API_KEY out of the picture so the env fallback branch is
		// deterministic across runs and inputs.
		t.Setenv("OPENAI_API_KEY", "")

		status, networkErr := authfz_resolveStatus(statusSel)

		// Bound the clock offset to +/- 100 years around a fixed base so now is a
		// sane, overflow-free instant while still crossing expiry boundaries.
		const century = int64(100 * 365 * 24 * 3600)
		base := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
		now := base.Add(time.Duration(nowOffset%century) * time.Second)

		creds, err := authfz_resolveOnce(t, credFile, refreshBody, status, networkErr, now)

		if err != nil {
			// Error discipline: no partial credential may leak on any failure.
			if creds != (RuntimeCredentials{}) {
				t.Fatalf("ResolveRuntimeCredentials returned creds %+v alongside error %v", creds, err)
			}
		} else if creds.Source != AuthSourceOAuth && creds.Source != AuthSourceEnv {
			// A successful resolve must carry a recognized provenance. (The
			// OAuth success-path disk invariant is asserted in authfz_resolveOnce.)
			t.Fatalf("ResolveRuntimeCredentials success with unexpected Source %q", creds.Source)
		}

		// Determinism: a second resolve against a freshly seeded identical state
		// directory must agree on error-ness, Source, and BearerToken. (Expiry
		// can differ by wall-clock nanoseconds because intoTokenSet stamps it
		// from time.Now(); the stable fields must not.)
		creds2, err2 := authfz_resolveOnce(t, credFile, refreshBody, status, networkErr, now)
		if (err == nil) != (err2 == nil) {
			t.Fatalf("ResolveRuntimeCredentials non-deterministic error: %v vs %v", err, err2)
		}
		if err == nil {
			if creds.Source != creds2.Source || creds.BearerToken != creds2.BearerToken {
				t.Fatalf("ResolveRuntimeCredentials non-deterministic result: %+v vs %+v", creds, creds2)
			}
		}
	})
}

// authfz_resolveOnce seeds a fresh state directory with the fuzzed inputs and
// runs one ResolveRuntimeCredentials, additionally asserting the OAuth
// success-path disk invariant (returned token/expiry match the persisted record).
func authfz_resolveOnce(t *testing.T, credFile, refreshBody []byte, status int, networkErr bool, now time.Time) (RuntimeCredentials, error) {
	t.Helper()
	svc, stateDir := authfz_newResolveService(t, credFile, refreshBody, status, networkErr, now)
	creds, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, authfz_instance)
	if err == nil && creds.Source == AuthSourceOAuth {
		rec, ok := authfz_rawRecord(stateDir)
		if ok {
			if rec.AccessToken != creds.BearerToken {
				t.Fatalf("OAuth BearerToken %q != persisted AccessToken %q", creds.BearerToken, rec.AccessToken)
			}
			if !rec.Expiry.Equal(creds.Expiry) {
				t.Fatalf("OAuth Expiry %v != persisted Expiry %v", creds.Expiry, rec.Expiry)
			}
		}
	}
	return creds, err
}

// authfz_rawRecord reads the persisted auth file and json-unmarshals it WITHOUT
// running Validate, so the oracle can inspect what was saved even when the saved
// record would fail validation (e.g. a server that 200s with no access_token).
func authfz_rawRecord(stateDir string) (AuthRecord, bool) {
	data, err := os.ReadFile(AuthFilePath(stateDir, authfz_instance))
	if err != nil {
		return AuthRecord{}, false
	}
	var rec AuthRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return AuthRecord{}, false
	}
	return rec, true
}
