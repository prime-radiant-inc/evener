package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func TestPrivateBrokerRouteRequiresDirectCurrentDaemonCredentials(t *testing.T) {
	called := make(chan struct{}, 1)
	var srv *Server
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	host := strings.TrimPrefix(httpServer.URL, "http://")
	srv = NewServer(ServerConfig{
		HubToken: "current-token", AllowedHost: host,
		PrivateBrokerHandler: func(_ context.Context, transport appwire.Transport) {
			called <- struct{}{}
			_ = transport.Close()
		},
	})

	tests := []struct {
		name, target, host, origin, token string
		wantStatus                        int
	}{
		{name: "missing token", target: appwire.PrivateBrokerPath, host: host, wantStatus: http.StatusUnauthorized},
		{name: "wrong token", target: appwire.PrivateBrokerPath, host: host, token: "wrong", wantStatus: http.StatusUnauthorized},
		{name: "wrong host", target: appwire.PrivateBrokerPath, host: "elsewhere.invalid", token: "current-token", wantStatus: http.StatusForbidden},
		{name: "same origin is still browser traffic", target: appwire.PrivateBrokerPath, host: host, origin: "http://" + host, token: "current-token", wantStatus: http.StatusForbidden},
		{name: "escaped path", target: "/internal/artifacts/%62roker", host: host, token: "current-token", wantStatus: http.StatusNotFound},
		{name: "cleaned path", target: "/internal//artifacts/../artifacts/broker", host: host, token: "current-token", wantStatus: http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			select {
			case <-called:
				t.Fatal("private handler ran for rejected request")
			default:
			}
		})
	}

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + appwire.PrivateBrokerPath
	header := http.Header{"Authorization": []string{"Bearer current-token"}}
	transport, err := appwire.DialPrivateWebSocketWithHeaders(t.Context(), wsURL, http.DefaultClient, header)
	if err != nil {
		t.Fatalf("dial private route: %v", err)
	}
	defer transport.Close() //nolint:errcheck
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("private handler was not reached")
	}
}

func TestPrivateBrokerPathClassificationRejectsEncodedAndCleanedVariants(t *testing.T) {
	tests := []struct {
		path, raw string
		exact     bool
	}{
		{path: appwire.PrivateBrokerPath, exact: true},
		{path: appwire.PrivateBrokerPath, raw: "/internal/artifacts/%62roker"},
		{path: "/internal//artifacts/broker"},
		{path: "/internal/artifacts/../artifacts/broker"},
	}
	for _, tc := range tests {
		if !appwire.IsPrivateBrokerPath(tc.path, tc.raw) {
			t.Errorf("path=%q raw=%q was not classified private", tc.path, tc.raw)
		}
		if got := appwire.IsExactPrivateBrokerPath(tc.path, tc.raw); got != tc.exact {
			t.Errorf("exact path=%q raw=%q = %v, want %v", tc.path, tc.raw, got, tc.exact)
		}
	}
	if appwire.IsPrivateBrokerPath("/rpc", url.PathEscape("/rpc")) {
		t.Fatal("ordinary RPC classified as private")
	}
}
