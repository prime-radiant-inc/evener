package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// FuzzSessionResiduePass5 targets the remaining tree, memoization,
// decision-store, and daemon-status branches with process-local inputs.
func FuzzSessionResiduePass5(f *testing.F) {
	for op := range uint8(4) {
		f.Add(op, "residue")
	}
	f.Fuzz(func(t *testing.T, op uint8, title string) {
		now := time.Now()
		thread := appwire.Thread{
			ID: "thread-5", SessionID: "thread-5", Source: "remote", Name: title,
			CWD: "/work/project", ModelProvider: "provider/model",
			CreatedAt: now.Add(-time.Minute).Unix(), UpdatedAt: now.Unix(),
			Status: appwire.ThreadStatus{Type: "active"},
		}

		switch op % 4 {
		case 0:
			dir := t.TempDir()
			archive := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
			favorite := hubcore.NewFavoriteStore(filepath.Join(dir, "index.db"))
			if err := archive.Set("session", "remote:thread-5", true, now); err != nil {
				t.Fatal(err)
			}
			if err := favorite.Set("session", "remote:thread-5", true, now); err != nil {
				t.Fatal(err)
			}
			web := NewWebServer(hubcore.WebConfig{Archive: archive, Favorite: favorite})
			_ = web.archiveDecisions()
			_, _ = web.favoriteDecisions()
			bad := filepath.Join(dir, "database-is-directory")
			if err := os.Mkdir(bad, 0o700); err != nil {
				t.Fatal(err)
			}
			web.cfg.Archive = hubcore.NewArchiveStore(bad)
			web.cfg.Favorite = hubcore.NewFavoriteStore(bad)
			_ = web.archiveDecisions()
			_, _ = web.favoriteDecisions()
		case 1:
			cache := &hubcore.RemoteThreadCache{}
			cache.Store([]appwire.Thread{thread})
			inputs := &hubcore.InputsVersion{}
			web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache, Inputs: inputs})
			_, _, _ = web.navigationTreeInputs(context.Background())
			_, _ = web.memoTree(context.Background())
			_, _ = web.memoTree(context.Background())
			inputs.Bump()
			_, _ = web.memoTree(context.Background())
		case 2:
			past := hubcore.NewPastIndex("")
			past.SeedForTest([]schema.SessionMeta{{
				ID: "past-5", Name: title, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
				EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/project"},
			}})
			web := NewWebServer(hubcore.WebConfig{Past: past})
			tree, _ := web.memoTree(context.Background())
			if len(tree.Projects) == 0 {
				t.Fatal("expected project")
			}
		case 3:
			// fetchStatus rejects invalid JSON and accepts a valid daemon response.

			for _, payload := range []string{"{", `{"status":"active","turns":2}`} {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(payload)) }))
				addr := strings.TrimPrefix(srv.URL, "http://")
				_ = NewWebServer(hubcore.WebConfig{}).fetchStatus(hubcore.LiveEntry{Address: addr})
				srv.Close()
			}
		}
	})
}
