package server

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestAppThreadReportsAwaitingAndSendCapability(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetState("awaiting")
	srv.SetProcessing(false)
	got := srv.appThread()
	if got.Status.Type != appwire.ThreadStatusAwaiting {
		t.Fatalf("Status = %q, want awaiting", got.Status.Type)
	}
	if !got.Evener.Capabilities.Send {
		t.Fatal("Send capability must be true for an awaiting session")
	}
	if s := appStatus(got.Status.Type, false, false); s != appwire.ThreadStatusAwaiting {
		t.Fatalf("appStatus(awaiting,false) = %q", s)
	}
}

func TestAppThreadPendingAskTrueFalseTrueAfterRestart(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "awaiting"})

	asked := true
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.askPending = asked })
	if got := appThreadPendingAsk(srv); !got {
		t.Fatal("expected pending_ask=true while the question is unanswered")
	}

	asked = false
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) { e.askPending = asked })
	if got := appThreadPendingAsk(srv); got {
		t.Fatal("expected pending_ask=false once answered")
	}

	// Simulate a daemon restart: a fresh Server, a fresh pendingAskFn backed
	// by a new session whose restore rebuilt HasPendingAsk()=true (the
	// already-shipped §2 restore fix) — the wire must reflect it immediately,
	// with no post-restart grace period where it reads stale-false.
	restarted := NewServer(ServerConfig{})
	restarted.SetStatus(StatusInfo{SessionID: "s1", State: "awaiting"})
	setEnvelope(restarted, func(e *stubThreadEnvelopeSource) { e.askPending = true })
	if got := appThreadPendingAsk(restarted); !got {
		t.Fatal("expected pending_ask=true immediately after restart, mirroring HasPendingAsk()'s restore rebuild")
	}
}

func appThreadPendingAsk(srv *Server) bool {
	return srv.appThread().Evener.AskPending
}
