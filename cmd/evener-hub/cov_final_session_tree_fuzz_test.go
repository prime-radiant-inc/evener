package hub

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func FuzzFinalSessionTree(f *testing.F) {
	for op := range uint8(2) {
		f.Add(op)
	}
	f.Fuzz(func(_ *testing.T, op uint8) {
		now := time.Now().UTC()
		switch op % 2 {
		case 0:
			cache := &hubcore.RemoteThreadCache{}
			cache.Store([]appwire.Thread{{ID: "r", Source: "remote", Status: appwire.ThreadStatus{Type: "closed"}}})
			past := hubcore.NewPastIndex("")
			past.SeedForTest([]schema.SessionMeta{{ID: "p", CreatedAt: now, UpdatedAt: now}})
			web := NewWebServer(hubcore.WebConfig{Past: past, RemoteThreadCache: cache})
			_, _, _ = web.navigationTreeInputs(context.Background())
		case 1:
			web := NewWebServer(hubcore.WebConfig{})
			web.sources = appsource.NewRegistry()
			_ = web.refreshRemoteThreads(context.Background())
			_ = web.apiTreeSources()
			_, _, _ = appThreadTreeEntries(appwire.Thread{ID: "x", Source: "remote", GitInfo: &appwire.GitInfo{Branch: "b", OriginURL: "u"}})
		}
	})
}
