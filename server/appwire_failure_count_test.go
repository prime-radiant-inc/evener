package server

import "testing"

// A running session is exactly the reader the failure count was built for — the
// person watching a long run, who cannot tell whether anything has gone wrong
// without scrolling all of it. Before this, the figure only arrived once the
// session was cold and the hub could scan the finished transcript (kata 12rq).
func TestAppThreadCarriesTheLiveFailureCount(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "active"})
	srv.SetFailedToolCallsFunc(func() (int, bool) { return 6, true })

	thread := srv.appThread()
	if thread.Serf.FailedToolCalls == nil {
		t.Fatal("appThread().Serf.FailedToolCalls = nil, want the running session's count")
	}
	if got := *thread.Serf.FailedToolCalls; got != 6 {
		t.Fatalf("appThread().Serf.FailedToolCalls = %d, want 6", got)
	}
}

func TestAppThreadCarriesAMeasuredZero(t *testing.T) {
	// Zero renders nothing, but it is a claim the daemon is entitled to make:
	// the session was counted and nothing failed. It has to reach the wire as 0
	// rather than as absent, so the two cases stay distinguishable downstream.
	srv := NewServer(ServerConfig{})
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "active"})
	srv.SetFailedToolCallsFunc(func() (int, bool) { return 0, true })

	thread := srv.appThread()
	if thread.Serf.FailedToolCalls == nil {
		t.Fatal("appThread().Serf.FailedToolCalls = nil, want a measured 0")
	}
	if got := *thread.Serf.FailedToolCalls; got != 0 {
		t.Fatalf("appThread().Serf.FailedToolCalls = %d, want 0", got)
	}
}

func TestAppThreadOmitsAnUnmeasuredFailureCount(t *testing.T) {
	// The load-bearing case. A session nobody counted must arrive absent, not
	// as a comforting zero: a partial or fabricated figure wears session-level
	// authority while under-reporting, which is worse than saying nothing.
	srv := NewServer(ServerConfig{})
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "active"})
	srv.SetFailedToolCallsFunc(func() (int, bool) { return 0, false })

	thread := srv.appThread()
	if thread.Serf.FailedToolCalls != nil {
		t.Fatalf("appThread().Serf.FailedToolCalls = %d, want absent when nobody counted", *thread.Serf.FailedToolCalls)
	}
}

func TestAppThreadOmitsTheFailureCountOnADaemonThatNeverWiredIt(t *testing.T) {
	// An old daemon (or a Codex-sourced thread) never installs the callback.
	// Absence is the honest report; the client renders nothing.
	srv := NewServer(ServerConfig{})
	srv.SetStatus(StatusInfo{SessionID: "s1", State: "active"})

	thread := srv.appThread()
	if thread.Serf.FailedToolCalls != nil {
		t.Fatalf("appThread().Serf.FailedToolCalls = %d, want absent with no callback", *thread.Serf.FailedToolCalls)
	}
}
