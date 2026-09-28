package hubcore

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

// fuzzScenarioStatusProber_KeepsTheRootsProfile: the probe keeps the provider
// instance the root's current model runs on, which a sign-in notice counts its
// sessions by (S11). The rendezvous entry's Provider is the one the session
// started on and goes stale after a model switch.
func fuzzScenarioStatusProber_KeepsTheRootsProfile(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_profile",
		state:     appwire.ThreadStatusIdle,
		setup: func(srv *server.Server) {
			srv.UpdateSessionInfo("th_profile", "gpt-5.6", "codex-jesse-fsck.com")
		},
	})
	entry.Provider = "lunaroute"
	got := prober.Probe(entry)
	if !got.OK || got.Profile != "codex-jesse-fsck.com" {
		t.Fatalf("probe = %+v, want the profile codex-jesse-fsck.com", got)
	}
	if live := liveEntryFromProbe(entry, got); live.Profile != "codex-jesse-fsck.com" || live.Provider != "lunaroute" {
		t.Fatalf("live entry profile %q provider %q, want the probe's profile beside the entry's own provider", live.Profile, live.Provider)
	}
}

// A resumed session's first publication carries its profile too, so a sign-in
// notice counts it before the next scan.
func TestRosterReadSpawnedThreadPublishesTheProfile(t *testing.T) {
	r := NewRoster(t.TempDir(), nil)
	entry := rendezvous.Entry{
		PID: 1001, SourceID: "local", Protocol: appwire.ProtocolVersion,
		Endpoint: "ws://127.0.0.1:50001/rpc", ThreadID: "01SPAWNED", SessionID: "01SPAWNED",
	}
	if _, err := r.ReadSpawnedThread(t.Context(), entry, func(context.Context) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: "01SPAWNED", SessionID: "01SPAWNED",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener: appwire.EvenerThread{Profile: "codex-jesse-fsck.com"},
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	live, ok := r.Find("01SPAWNED")
	if !ok || live.Profile != "codex-jesse-fsck.com" {
		t.Fatalf("published entry = %+v, want the profile the read carried", live)
	}
}
