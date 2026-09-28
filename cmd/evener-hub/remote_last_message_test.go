package hub

import (
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// A controller reads a remote host's last message off its list row, so the
// remote row carries it like a local one: an online host's live row from the
// row as its live entry, and an offline host's last-known row, which is not
// live, from the row as its meta (S1d).
func TestNavigationRemoteRowsCarryTheirLastMessage(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{
		{ID: "online-thread", Source: "remote-online", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}, Evener: appwire.EvenerThread{LastMessage: "The online host's words."}},
		{ID: "offline-thread", Source: "remote-offline", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}, Evener: appwire.EvenerThread{LastMessage: "The offline host's words."}},
	})
	registry := appsource.NewRegistry()
	registry.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "remote-online"}, online: true})
	registry.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "remote-offline"}, online: false})
	web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})
	web.sources = registry

	captured, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	projection, err := buildNavigationProjection(captured.Inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}
	for ref, want := range map[string]string{
		"remote-online:online-thread":   "The online host's words.",
		"remote-offline:offline-thread": "The offline host's words.",
	} {
		if row := navigationProjectedSummary(t, projection, ref); row.LastMessage != want {
			t.Errorf("%s last message = %q (live %v), want %q", ref, row.LastMessage, row.Live, want)
		}
	}
}
