package openai

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/llm"
)

// FuzzNewForInstanceOAuth drives NewForInstance's OAuth branch end to end through
// the AuthHTTPClient seam: a fuzzed per-instance credential file on disk plus a
// fuzzed token-refresh reply delivered by a fake transport exercise the real
// resolve -> (needs-refresh) RefreshToken/decode -> Status -> LoadAuth +
// ParseIDTokenClaims -> authScopeForOAuth -> Adapter construction path. This is
// the branch the auth-package fuzzer could not reach, because NewForInstance used
// to build its auth client internally with no injection point; the AuthHTTPClient
// field (nil in production, byte-identical) is what makes it fuzzable without a
// real network call.
//
// Oracles (beyond never-panic):
//   - Error discipline: a non-nil error yields a nil Adapter (no partial adapter
//     on any failure path).
//   - A successful OAuth build carries a non-nil Client and the ChatGPT base URL.
//   - Determinism: the same inputs against a freshly seeded state dir agree on
//     error-ness and the built adapter's key/base.
//
// SAFETY: the fake transport intercepts every request (no real network, even
// though DefaultConfig's production issuer URL is used); all disk writes are
// contained to t.TempDir.
func FuzzNewForInstanceOAuth(f *testing.F) {
	const maxInput = 1 << 16
	rec := func(expiry time.Time, idToken string) []byte {
		data, _ := json.Marshal(openai.AuthRecord{
			Version:      1,
			Provider:     "openai",
			Source:       openai.AuthSourceOAuth,
			ObtainedAt:   time.Date(2026, 5, 7, 23, 0, 0, 0, time.UTC),
			TokenType:    "Bearer",
			Scope:        "openid profile email offline_access",
			AccessToken:  "stored-access-token",
			RefreshToken: "stored-refresh-token",
			IDToken:      idToken,
			Expiry:       expiry,
		})
		return data
	}
	okBody := []byte(`{"access_token":"fresh","refresh_token":"rt2","token_type":"Bearer","scope":"openid","expires_in":3600,"id_token":"id2"}`)
	future := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC) // fresh: no refresh
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)   // expired: triggers refresh
	// id_token JWT carrying account/workspace claims, so the claims-merge branch runs.
	jwtID := "a.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoiYWNjdC0xIn19.sig"

	f.Add(rec(future, "id-token"), okBody, byte(1), byte(1))                              // fresh OAuth, straight through
	f.Add(rec(past, "id-token"), okBody, byte(1), byte(1))                                // expired -> 2xx refresh
	f.Add(rec(past, "id-token"), []byte(`{"error":"invalid_grant"}`), byte(200), byte(0)) // permanent refresh error
	f.Add(rec(future, jwtID), okBody, byte(1), byte(1))                                   // claims-merge branch
	f.Add([]byte(`not json`), okBody, byte(1), byte(0))                                   // corrupt cred -> not signed in
	f.Add([]byte(``), okBody, byte(1), byte(0))
	f.Add(rec(past, "id-token"), okBody, byte(0), byte(1)) // transport error on refresh

	f.Fuzz(func(t *testing.T, credFile, refreshBody []byte, statusSel, hasherSel byte) {
		if len(credFile) > maxInput || len(refreshBody) > maxInput {
			t.Skip()
		}
		adapter, err := oaifz_buildOnce(t, credFile, refreshBody, statusSel, hasherSel)

		if err != nil {
			if adapter != nil {
				t.Fatalf("NewForInstance returned an adapter %+v alongside error %v", adapter, err)
			}
		} else if adapter == nil {
			t.Fatal("NewForInstance returned nil adapter and nil error")
		} else if adapter.Client == nil {
			t.Fatal("NewForInstance built an adapter with a nil Client")
		}

		// Determinism against a freshly seeded identical state dir.
		adapter2, err2 := oaifz_buildOnce(t, credFile, refreshBody, statusSel, hasherSel)
		if (err == nil) != (err2 == nil) {
			t.Fatalf("NewForInstance non-deterministic error: %v vs %v", err, err2)
		}
		if err == nil && (adapter.APIKey != adapter2.APIKey || adapter.BaseURL != adapter2.BaseURL) {
			t.Fatalf("NewForInstance non-deterministic build: {%q,%q} vs {%q,%q}",
				adapter.APIKey, adapter.BaseURL, adapter2.APIKey, adapter2.BaseURL)
		}
	})
}

// oaifz_buildOnce seeds a fresh state home with the fuzzed credential file, wires
// the fuzzed refresh reply into an injected fake transport, and runs one
// NewForInstance. The instance name "openai" matches the seeded credential path.
func oaifz_buildOnce(t *testing.T, credFile, refreshBody []byte, statusSel, hasherSel byte) (*Adapter, error) {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "") // keep the key fallback deterministic

	stateHome := t.TempDir()
	authPath := openai.AuthFilePath(openai.DefaultStateDirWithStateHome(stateHome), "openai")
	if err := os.MkdirAll(filepath.Dir(authPath), 0o755); err != nil {
		t.Fatalf("oaifz: mkdir auth dir: %v", err)
	}
	if err := os.WriteFile(authPath, credFile, 0o600); err != nil {
		t.Fatalf("oaifz: seed credential file: %v", err)
	}

	status, networkErr := oaifz_status(statusSel)
	client := &http.Client{Transport: oaifz_fakeRoundTripper{status: status, body: refreshBody, networkErr: networkErr}}

	var hasher *llm.ContinuationHasher
	if hasherSel%2 == 1 {
		hasher = llm.NewContinuationHasher([]byte("oaifz-secret"))
	}

	return NewForInstance(OpenAIInstanceParams{
		Name:               "openai",
		StateHome:          stateHome,
		AuthHTTPClient:     client,
		ContinuationHasher: hasher,
	})
}

func oaifz_status(sel byte) (status int, networkErr bool) {
	if sel == 0 {
		return 0, true
	}
	return 200 + int(sel)%400, false
}

// oaifz_fakeRoundTripper is the injection point for adversarial refresh bytes. It
// honors the RoundTripper contract: drain+close the body, return a non-nil
// response XOR a non-nil error.
type oaifz_fakeRoundTripper struct {
	status     int
	body       []byte
	networkErr bool
}

func (rt oaifz_fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	if rt.networkErr {
		return nil, &oaifz_transportError{}
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

type oaifz_transportError struct{}

func (*oaifz_transportError) Error() string { return "oaifz simulated transport failure" }
