package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// FuzzParseIDTokenClaims drives the real ParseIDTokenClaims seam, which decodes
// the base64url JWT payload of an OAuth ID token (network-shaped untrusted input)
// for display metadata. Oracles beyond no-panic:
//
//   - Error discipline: any non-nil error must wrap ErrInvalidIDToken, and a
//     non-nil error must come with the zero TokenClaims (no partial leak).
//   - Determinism: the same token must parse identically every time.
func FuzzParseIDTokenClaims(f *testing.F) {
	seeds := []string{
		"",
		"   ",
		"header.payload",
		"a.b.c",
		"a.eyJlbWFpbCI6ImpAZXhhbXBsZS5jb20ifQ",
		"a.eyJlbWFpbCI6ImpAZXhhbXBsZS5jb20ifQ.sig",
		"a.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoiYWNjXzEifX0.x",
		"a.bm90LWpzb24.x",
		"a..c",
		"onlyonepart",
		"a.eyJlbWFpbCI6eyJpZCI6ImpAeC5jb20ifX0.x",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, idToken string) {
		claims, err := ParseIDTokenClaims(idToken)

		again, errAgain := ParseIDTokenClaims(idToken)
		if (err == nil) != (errAgain == nil) || claims != again {
			t.Fatalf("ParseIDTokenClaims not deterministic for %q", idToken)
		}

		if err != nil {
			if !errors.Is(err, ErrInvalidIDToken) {
				t.Fatalf("ParseIDTokenClaims error %v does not wrap ErrInvalidIDToken (%q)", err, idToken)
			}
			if claims != (TokenClaims{}) {
				t.Fatalf("ParseIDTokenClaims returned claims %+v alongside error %v (%q)", claims, err, idToken)
			}
		}
	})
}

// FuzzTokenEndpointResponse drives the real token-endpoint decode seam: the fuzz
// bytes are replayed as the /oauth/token reply and ExchangeCode runs them through
// json.NewDecoder via exchangeTokenRequest -> intoTokenSet (2xx) or
// compactTokenError (non-2xx). The status code is steered by the first byte to
// reach both branches.
//
// Oracle: no panic over arbitrary bodies; a non-2xx reply always yields a
// non-nil error; a 2xx reply that decodes yields no error and a non-negative,
// monotonic expiry.
func FuzzTokenEndpointResponse(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"access_token":"at","refresh_token":"rt","id_token":"id","token_type":"Bearer","scope":"openid","expires_in":3600}`),
		[]byte(`{"access_token":"at"}`),
		[]byte(`{"expires_in":-5}`),
		[]byte(`{"expires_in":"nope"}`),
		[]byte(`{"error":"invalid_grant","error_description":"expired"}`),
		[]byte(`{}`),
		[]byte(``),
		[]byte(`not json`),
		[]byte(`{"access_token":123}`),
	}
	for _, s := range seeds {
		f.Add(byte(200), s)
	}
	f.Add(byte(0), []byte(`{"error":"invalid_grant"}`))
	f.Add(byte(50), []byte(`garbage`))

	f.Fuzz(func(t *testing.T, statusSel byte, body []byte) {
		status := http.StatusOK
		if statusSel != 0 {
			status = 200 + int(statusSel)%300 // 200..499
		}

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(body)
		}))
		t.Cleanup(srv.Close)

		cfg := DefaultConfig()
		cfg.IssuerBaseURL = srv.URL

		before := time.Now()
		tokens, err := ExchangeCode(context.Background(), srv.Client(), cfg, TokenExchangeRequest{
			Code:         "code",
			RedirectURI:  "http://localhost:1455/auth/callback",
			CodeVerifier: "verifier",
		})

		if status < 200 || status >= 300 {
			if err == nil {
				t.Fatalf("ExchangeCode: nil error for HTTP status %d (body %q)", status, body)
			}
			return
		}
		if err != nil {
			return // a malformed 2xx body that fails to decode is a valid error path.
		}
		if !tokens.Expiry.IsZero() && tokens.Expiry.Before(before) {
			t.Fatalf("ExchangeCode produced expiry %v before request time %v (body %q)", tokens.Expiry, before, body)
		}
	})
}
