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

func TestHandleStatus_PendingAskTrueFalseTrueAfterRestart(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "awaiting"})

	asked := true
	srv.SetPendingAskFunc(func() bool { return asked })
	if got := statusPendingAsk(t, srv); !got {
		t.Fatal("expected pending_ask=true while the question is unanswered")
	}

	asked = false
	if got := statusPendingAsk(t, srv); got {
		t.Fatal("expected pending_ask=false once answered")
	}

	// Simulate a daemon restart: a fresh Server, a fresh pendingAskFn backed
	// by a new session whose restore rebuilt HasPendingAsk()=true (the
	// already-shipped §2 restore fix) — the wire must reflect it immediately,
	// with no post-restart grace period where it reads stale-false.
	restarted := NewServer(ServerConfig{})
	restarted.SetStatus(StatusInfo{SessionID: "s1", State: "awaiting"})
	restarted.SetPendingAskFunc(func() bool { return true })
	if got := statusPendingAsk(t, restarted); !got {
		t.Fatal("expected pending_ask=true immediately after restart, mirroring HasPendingAsk()'s restore rebuild")
	}
}

func statusPendingAsk(t *testing.T, srv *Server) bool {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	srv.handleStatus(rec, req)
	var got StatusInfo
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got.PendingAsk
}
