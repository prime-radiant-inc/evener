package hub

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/identifier"
)

// TestRESTCapabilitiesDoNotAdvertiseForkOrResume pins issue #1162: the REST
// session capability shape carried a fork/resume bit derived from pastExists,
// but no client reads it and no REST endpoint honours it (the mutating REST
// fork route is retired; the TUI and web read the AppWire ForkFromTurn
// capability instead). Advertising it would trap the next client author, so the
// projection must not emit fork/resume in any of its forms.
func TestRESTCapabilitiesDoNotAdvertiseForkOrResume(t *testing.T) {
	past := hubcore.NewPastIndex("")
	now := time.Now()
	pastID := identifier.MustNewSessionID()
	past.SeedForTest([]schema.SessionMeta{{ID: pastID, CreatedAt: now, UpdatedAt: now}})
	web := NewWebServer(hubcore.WebConfig{Past: past})

	projections := map[string]hubapi.SessionCapabilities{
		"api past, not live":   web.apiSessionCapabilities(pastID, false),
		"api past, live":       web.apiSessionCapabilities(pastID, true),
		"api unknown session":  web.apiSessionCapabilities("missing", false),
		"appwire fork-bearing": hubCapabilitiesFromAppwire(appwire.ThreadCapabilities{Send: true, ForkFromTurn: true}),
	}
	for name, caps := range projections {
		data, err := json.Marshal(caps)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		for _, key := range []string{`"fork"`, `"resume"`} {
			if strings.Contains(string(data), key) {
				t.Errorf("%s still advertises %s: %s", name, key, data)
			}
		}
	}
}
