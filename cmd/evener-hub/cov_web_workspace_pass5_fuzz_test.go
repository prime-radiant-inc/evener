package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// FuzzWebWorkspacePass5 exercises session titles, HTTP routes, and model
// presentation against in-memory indexes. It never consults a live provider.
func FuzzWebWorkspacePass5(f *testing.F) {
	for _, mode := range []uint8{3, 6, 7, 8, 9, 10, 11} {
		f.Add(mode, "alpha\r\nbeta")
	}
	f.Fuzz(func(t *testing.T, mode uint8, text string) {
		past := hubcore.NewPastIndex("")
		parent := schema.SessionMeta{ID: "parent", Name: "Parent"}
		child := schema.SessionMeta{ID: "child", Name: "Child", Model: "openai/gpt-4o", OriginalPrompt: text,
			ParentSessionID: "parent", IsSubagent: true, ForkLabel: "original", DivergenceTurn: 2, ObservedBy: []string{"observer", "observer"},
			TurnCount: 3, WorkMillis: 61_000, CumulativeUsage: schema.CumulativeUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}}
		child.EnvInfo.WorkingDir = filepath.Join(t.TempDir(), "work")
		child.EnvInfo.GitBranch = "main"
		child.WorktreePath = filepath.Join(t.TempDir(), "tree")
		past.SeedForTest([]schema.SessionMeta{parent, child, {ID: "fork", Name: "Fork", ParentSessionID: "child"}})
		roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "child", Model: "openai/gpt-4o", WorkingDir: child.EnvInfo.WorkingDir}, SessionID: "child", Status: "ended"})
		web := NewWebServer(hubcore.WebConfig{Past: past, Roster: roster, LiveModels: func(context.Context) []appwire.ModelDescriptor {
			return []appwire.ModelDescriptor{{Provider: "fixture", Model: "model"}}
		}})

		switch mode % 12 {
		case 3:
			for _, m := range []schema.SessionMeta{{Name: " name "}, {OriginalPrompt: text}, {ID: "0123456789abcdef"}} {
				_ = sessionTitleFromMeta(m)
			}
			for _, p := range []string{"", "first\r\nsecond", strings.Repeat("x", 90)} {
				_ = compactSessionPromptTitle(p)
			}
			_ = searchPastTitle(hubcore.PastEntry{Meta: child})
		case 6:
			for _, target := range []string{"/s/", "/s/remote:thread", "/s/remote:thread/state", "/s/remote:thread/details", "/s/remote:thread/tasks", "/s/remote:thread/nope"} {
				web.handleSession(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
			}
		case 7:
			for _, target := range []string{"/thread/remote:thread", "/thread/remote:missing", "/thread/", "/thread/a/b"} {
				web.handleThreadDocument(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
			}
		case 9:
		case 11:
			_, _ = hubModelList(context.Background(), web.cfg, web.sources, appwire.ModelListParams{Harness: "unknown"})
			models := withDisplayNames([]appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-4o"}, {}, {Provider: "z", Model: "m-20251101"}})
			_ = attachRecentModels(web.cfg, appwire.ModelListResponse{Data: models})
			_ = prettifyModelDisplayName(text)
			_ = isDatedSnapshotModelID(text)
			_ = isDatedSnapshotModelID("provider/model-20251101-v1")
		}
	})
}
