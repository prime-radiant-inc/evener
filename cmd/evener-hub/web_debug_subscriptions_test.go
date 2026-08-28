package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

func TestWebDebugSubscriptionsRequiresAuthentication(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{AuthToken: "debug-secret"})
	req := httptest.NewRequest(http.MethodGet, "/api/debug/subscriptions", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", rec.Code)
	}
}

func TestWebDebugSubscriptionsReturnsLiveRegistrySnapshot(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{AuthToken: "debug-secret"})
	conn := web.appRPC.NewConnection("conn-http-debug")
	conn.Subscribe("local:thread-http")

	req := httptest.NewRequest(http.MethodGet, "/api/debug/subscriptions", nil)
	req.Header.Set("Authorization", "Bearer debug-secret")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %q, want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var got appserver.SubscriptionDebugSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode snapshot: %v (body %q)", err, rec.Body.String())
	}
	if len(got.Subscriptions) != 1 || got.Subscriptions[0].ConnectionID != "conn-http-debug" || got.Subscriptions[0].ThreadID != "local:thread-http" || got.Subscriptions[0].Buffering {
		t.Fatalf("subscriptions = %+v", got.Subscriptions)
	}
}
