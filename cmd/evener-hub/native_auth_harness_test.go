package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/internal/appserver"
)

// TestNativeAuthHarness runs real hub auth handlers with a local scripted OAuth
// boundary for manual native browser round trips. It never contacts a provider.
// Explicit opt-in keeps the default suite deterministic and non-interactive.
func TestNativeAuthHarness(t *testing.T) {
	if os.Getenv("EVENER_NATIVE_AUTH_HARNESS") != "1" {
		t.Skip("manual native auth harness")
	}
	addr, base := os.Getenv("EVENER_NATIVE_AUTH_ADDR"), os.Getenv("EVENER_NATIVE_AUTH_ORIGIN")
	if addr == "" || base == "" {
		t.Fatal("set EVENER_NATIVE_AUTH_ADDR and EVENER_NATIVE_AUTH_ORIGIN")
	}
	oaitest.IsolateOpenAIAuth(t)
	dir, stateDir := t.TempDir(), t.TempDir()
	config := writeProvidersToml(t, dir, codexInstanceToml)
	ctrl := newTestAuthController(t, dir, stateDir, config)
	ctrl.cfg.IssuerBaseURL = base
	var approved, browser, pollFailure atomic.Bool
	var clockOffset atomic.Int64
	ctrl.now = func() time.Time { return time.Now().Add(time.Duration(clockOffset.Load())) }
	ctrl.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
		if browser.Load() {
			return authopenai.DeviceCode{}, authopenai.ErrDeviceCodeNotEnabled
		}
		approved.Store(false)
		return authopenai.DeviceCode{UserCode: "NATIVE-TEST", VerificationURL: base + "/authorize", DeviceAuthID: "native-device", Interval: time.Second}, nil
	}
	ctrl.pollDeviceOnce = func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error) {
		if pollFailure.Load() {
			return authopenai.DeviceCodeSuccess{}, false, errors.New("fixture OAuth poll failed")
		}
		if !approved.Load() {
			return authopenai.DeviceCodeSuccess{}, true, nil
		}
		return authopenai.DeviceCodeSuccess{AuthorizationCode: "fixture-code", CodeVerifier: "fixture-verifier"}, false, nil
	}
	tokens := func() authopenai.TokenSet {
		return authopenai.TokenSet{AccessToken: "native-fixture-access", RefreshToken: "native-fixture-refresh", TokenType: "Bearer", Expiry: ctrl.now().Add(time.Hour)}
	}
	ctrl.exchangeDevice = func(context.Context, *http.Client, authopenai.Config, string, string) (authopenai.TokenSet, error) {
		return tokens(), nil
	}
	ctrl.exchangeCode = func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
		return tokens(), nil
	}
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "Native auth fixture", Version: "native-test", SourceID: "local"})
	registerAuthHandlers(app, ctrl)
	registerInstanceHandlers(app, &hubInstancesController{reg: ctrl.reg, providersConfigPath: config, auth: ctrl})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{Data: []appwire.Thread{}}, nil
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", app.ServeWebSocket)
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<meta name="viewport" content="width=device-width"><h1>Evener native auth fixture</h1><p>Device code: NATIVE-TEST</p><form action="/approve" method="post"><button style="font-size:24px;padding:16px">Approve fixture sign-in</button></form>`)
	})
	mux.HandleFunc("POST /approve", func(w http.ResponseWriter, r *http.Request) {
		approved.Store(true)
		_, _ = fmt.Fprint(w, "Approved. Return to Evener.")
	})
	mux.HandleFunc("GET /oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		redirect := "http://localhost:1455/auth/callback?" + url.Values{"code": {"fixture-code"}, "state": {r.URL.Query().Get("state")}}.Encode()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<meta name="viewport" content="width=device-width"><h1>Fixture browser authorization</h1><p>Copy this full redirect URL into Evener:</p><textarea rows="6" style="width:95%%;font-size:18px">%s</textarea>`, html.EscapeString(redirect))
	})
	mux.HandleFunc("POST /mode/browser", func(w http.ResponseWriter, r *http.Request) { browser.Store(true); w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /mode/device", func(w http.ResponseWriter, r *http.Request) {
		browser.Store(false)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /clock/expire", func(w http.ResponseWriter, r *http.Request) {
		clockOffset.Add(int64(16 * time.Minute))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /fault/poll", func(w http.ResponseWriter, r *http.Request) {
		pollFailure.Store(true)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /fault/clear", func(w http.ResponseWriter, r *http.Request) {
		pollFailure.Store(false)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		status, err := ctrl.Status(appwire.AuthStatusParams{Provider: "work"})
		if err != nil {
			http.Error(w, "status failed", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"signedIn": status.SignedIn, "activeSource": status.ActiveSource})
	})
	stop := make(chan struct{}, 1)
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		select {
		case stop <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	})
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { _ = server.Close() })
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Logf("Native auth fixture ready at %s; isolated state %s", base, stateDir)
	select {
	case <-stop:
	case err := <-done:
		if err != http.ErrServerClosed {
			t.Fatal(err)
		}
	}
}
