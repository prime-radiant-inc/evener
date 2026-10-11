package hub

import (
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// The hub's own list rows name a root's current model and never lend it to
// the root's in-process subagent aliases.
func TestLocalDaemonEntriesFromRosterNameTheCurrentModelOnlyOnTheRoot(t *testing.T) {
	entries := localDaemonEntriesFromRoster([]hubcore.LiveEntry{{
		Entry:     rendezvous.Entry{ThreadID: "sess_root", SessionID: "sess_root", Model: "kimi-k3"},
		SessionID: "sess_root", Status: appwire.ThreadStatusIdle,
		RunningSubagentIDs: []string{"sess_child"}, CurrentModel: "gpt-5.6",
	}})
	if len(entries) != 2 || entries[0].CurrentModel != "gpt-5.6" || entries[1].CurrentModel != "" {
		t.Fatalf("entries = %+v, want the root's current model and none on its alias", entries)
	}
}

// A controller reads a remote host's model off its list row, so the remote row
// names it like a local one: an online host's live row from the row as its
// live entry, and an offline host's last-known row, which is not live, from
// the row as its meta (S17).
func TestNavigationRemoteRowsNameTheirModel(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{
		{ID: "online-thread", Source: "remote-online", ModelProvider: "gpt-5.6", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}},
		{ID: "offline-thread", Source: "remote-offline", ModelProvider: "claude-opus-4-7", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}},
	})
	registry := appsource.NewRegistry()
	registry.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "remote-online"}, online: true})
	registry.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "remote-offline"}, online: false})
	web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})
	web.sources = registry

	captured, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	projection, err := buildNavigationProjection(captured.Inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}
	for ref, want := range map[string]string{
		"remote-online:online-thread":   "Gpt 5.6",
		"remote-offline:offline-thread": "Claude Opus 4 7",
	} {
		if row := navigationProjectedSummary(t, projection, ref); row.ModelName != want {
			t.Errorf("%s model name = %q (live %v), want %q", ref, row.ModelName, row.Live, want)
		}
	}
}
