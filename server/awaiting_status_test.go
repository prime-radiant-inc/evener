package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestStatusReportsAwaitingAndSendCapability(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetState("awaiting")
	srv.SetProcessing(false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	srv.handleStatus(rec, req)
	var got StatusInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "awaiting" {
		t.Fatalf("State = %q, want awaiting", got.State)
	}
	if !got.Capabilities.Send {
		t.Fatal("Send capability must be true for an awaiting session")
	}
	if s := appStatus(got.State, false); s != appwire.ThreadStatusAwaiting {
		t.Fatalf("appStatus(awaiting,false) = %q", s)
	}
}
