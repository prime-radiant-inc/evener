package githubcopilot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// FuzzGitHubResponseDecoding drives every response-decoding seam in the package
// with the fuzz bytes replayed as the HTTP reply body: RefreshToken and
// RequestDeviceCode through postFormJSON, and ExchangeCopilotToken and
// FetchAccount through their own json.NewDecoder calls. The seam selector picks
// the call and the status selector picks the reply status, so decode, field
// validation, and status classification all run on arbitrary bytes.
//
// Oracles beyond no-panic:
//   - A failure reply never decodes as success: a non-2xx status always errors,
//     and a refresh reply whose JSON "error" member is set never returns nil.
//   - A successful decode satisfies the call's documented contract: a refresh
//     pair carries an access token and complete-or-absent refresh fields, a
//     device code carries its display fields and a future expiry, a Copilot
//     session carries a token and a positive expiry, and an account carries its
//     stable id.
//   - On a 2xx reply, the two seams whose only failure in that range is a decode
//     or field-validation failure wrap ErrMalformedResponse.
//   - A computed expiry never lands before the request time.
func FuzzGitHubResponseDecoding(f *testing.F) {
	seeds := []struct {
		seam, status byte
		body         []byte
	}{
		{0, 0, []byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600,"refresh_token_expires_in":7200}`)},
		{0, 0, []byte(`{"access_token":"at"}`)},
		{0, 0, []byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600}`)},
		{0, 0, []byte(`{"error":"bad_refresh_token"}`)},
		{0, 0, []byte(`{"expires_in":"nope"}`)},
		{0, 0, []byte(`{`)},
		{0, 200, []byte(`{"access_token":"at"}`)},
		{1, 0, []byte(`{"device_code":"dc","user_code":"uc","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`)},
		{1, 0, []byte(`{"device_code":"dc"}`)},
		{1, 0, []byte(`{`)},
		{2, 0, []byte(`{"token":"copilot-session","expires_at":1764000000}`)},
		{2, 0, []byte(`{"token":"copilot-session"}`)},
		{2, 0, []byte(`{"expires_at":-1}`)},
		{2, 0, []byte(`{`)},
		{2, 201, []byte(`{}`)}, // 200+201%300 = 401, the permanent-rejection branch
		{2, 202, []byte(`{}`)}, // 200+202%300 = 402, the generic status branch
		{3, 0, []byte(`{"id":4242,"login":"octocat","email":null}`)},
		{3, 0, []byte(`{"id":null}`)},
		{3, 0, []byte(`{"id":"abc"}`)},
		{3, 0, []byte(`{"id":4242,"login":3}`)},
		{3, 0, []byte(`{`)},
		{3, 150, []byte(`{}`)},
	}
	for _, s := range seeds {
		f.Add(s.seam, s.status, s.body)
	}

	f.Fuzz(func(t *testing.T, seamSel, statusSel byte, body []byte) {
		status := http.StatusOK
		if statusSel != 0 {
			status = 200 + int(statusSel)%300 // 200..499
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(body)
		}))
		t.Cleanup(server.Close)

		cfg := newTestConfig(server)
		client := server.Client()
		ctx := context.Background()
		ok := status >= 200 && status < 300
		before := nowFunc()

		switch seamSel % 4 {
		case 0: // RefreshToken -> postFormJSON -> tokenResponsePayload
			tokens, err := RefreshToken(ctx, client, cfg, "refresh-old")
			if !ok && err == nil {
				t.Fatalf("RefreshToken: nil error for HTTP status %d", status)
			}
			if field := jsonErrorField(body); field != "" && err == nil {
				t.Fatalf("RefreshToken: nil error for a reply carrying JSON error %q", field)
			}
			if err != nil {
				return
			}
			if strings.TrimSpace(tokens.AccessToken) == "" {
				t.Fatalf("RefreshToken: success without an access token")
			}
			if (strings.TrimSpace(tokens.RefreshToken) == "") != tokens.RefreshExpiry.IsZero() {
				t.Fatalf("RefreshToken: refresh token presence and refresh expiry disagree")
			}
			if expiredBefore(tokens.AccessExpiry, before) || expiredBefore(tokens.RefreshExpiry, before) {
				t.Fatalf("RefreshToken: expiry lands before the request time")
			}
		case 1: // RequestDeviceCode -> postFormJSON -> deviceCodePayload
			code, err := RequestDeviceCode(ctx, client, cfg)
			if !ok && err == nil {
				t.Fatalf("RequestDeviceCode: nil error for HTTP status %d", status)
			}
			if err != nil {
				return
			}
			if code.DeviceCode == "" || code.UserCode == "" || code.VerificationURL == "" || code.Interval <= 0 || !code.Expiry.After(before) {
				t.Fatalf("RequestDeviceCode: success with an incomplete or expired device code")
			}
		case 2: // ExchangeCopilotToken -> inline session-token payload decode
			session, err := ExchangeCopilotToken(ctx, client, cfg, "github-access")
			switch {
			case status == http.StatusUnauthorized || status == http.StatusForbidden:
				if !errors.Is(err, ErrCopilotTokenRejected) {
					t.Fatalf("ExchangeCopilotToken: HTTP %d error = %v, want ErrCopilotTokenRejected", status, err)
				}
			case !ok:
				if err == nil {
					t.Fatalf("ExchangeCopilotToken: nil error for HTTP status %d", status)
				}
			case err != nil:
				if !errors.Is(err, ErrMalformedResponse) {
					t.Fatalf("ExchangeCopilotToken: 2xx failure %v does not wrap ErrMalformedResponse", err)
				}
			case session.Token == "" || session.Expiry.Unix() <= 0:
				t.Fatalf("ExchangeCopilotToken: success without a session token and positive expiry")
			}
		case 3: // FetchAccount -> inline account payload decode
			account, err := FetchAccount(ctx, client, cfg, "access-1")
			if !ok && err == nil {
				t.Fatalf("FetchAccount: nil error for HTTP status %d", status)
			}
			if err != nil {
				if ok && !errors.Is(err, ErrMalformedResponse) {
					t.Fatalf("FetchAccount: 2xx failure %v does not wrap ErrMalformedResponse", err)
				}
				return
			}
			if account.ID == "" {
				t.Fatalf("FetchAccount: success without the stable account id")
			}
		}
	})
}

// jsonErrorField reports the JSON "error" member GitHub sets on a failed token
// reply. A body that is not a JSON object has no error member.
func jsonErrorField(body []byte) string {
	var probe struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return ""
	}
	return probe.Error
}

// expiredBefore reports whether a computed expiry is set yet lands before the
// request that produced it.
func expiredBefore(expiry, before time.Time) bool {
	return !expiry.IsZero() && expiry.Before(before)
}
