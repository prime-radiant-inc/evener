package hub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// FuzzSessionTreePass3 drives the presentation and HTTP boundary branches that
// are otherwise difficult for the broad route fuzzers to reach. All sources
// and filesystem roots are process-local and deterministic.
func FuzzSessionTreePass3(f *testing.F) {
	for _, op := range []uint8{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15} {
		f.Add(op, "alpha\r\nbeta", int64(90_000))
	}
	f.Fuzz(func(t *testing.T, op uint8, text string, number int64) {
		now := time.Now().UnixMilli()
		started := now - 90000
		usage := &appwire.EvenerUsage{InputTokens: 11, OutputTokens: 7, CacheReadTokens: 3, TotalTokens: 18}
		thread := appwire.Thread{
			ID: "thread-1", SessionID: "session-1", Source: "remote", Name: text,
			Preview: "preview", CWD: "/work/project", ModelProvider: "openai/gpt",
			CreatedAt: now - 100, UpdatedAt: now, Status: appwire.ThreadStatus{Type: "active"},
			Turns:  []appwire.Turn{{ID: "done", Status: appwire.TurnStatusCompleted}, {ID: "run", Status: appwire.TurnStatusInProgress, StartedAt: &started}},
			Evener: appwire.EvenerThread{Ref: "remote:thread-1", ActiveTurnID: "run-explicit", ContextUsed: 10, ContextWindow: 20, ContextRemaining: 10, WorkMillis: number, Usage: usage, Capabilities: appwire.ThreadCapabilities{Send: true, Steer: true, Queue: true}},
		}

		switch op % 16 {
		case 4:
			for _, p := range []string{"", " first\r\nsecond ", strings.Repeat("x", 90)} {
				_ = compactSessionPromptTitle(p)
			}
			for _, m := range []schema.SessionMeta{{Name: " named "}, {OriginalPrompt: "prompt"}, {ID: "0123456789abcdef"}} {
				_ = sessionTitleFromMeta(m)
			}
		case 6:
			_, _, _ = appThreadTreeEntries(thread)
			thread.Evener.Ref = "bad"
			thread.Source = ""
			_, _, _ = appThreadTreeEntries(thread)
			thread.Source = "remote"
			thread.ID = ""
			_, _, _ = appThreadTreeEntries(thread)
			for _, status := range []string{appwire.ThreadStatusClosed, appwire.ThreadStatusNotLoaded, "active"} {
				thread.Status.Type = status
				_ = appThreadTreeLive(thread)
			}
		case 7:
			_ = hubCapabilitiesFromAppwire(thread.Evener.Capabilities)
			_ = hubRefFromTreeNodeID("bad")
		case 8:
			web := NewWebServer(hubcore.WebConfig{})
			l := &stubThreadLister{id: "remote", resp: appwire.ThreadListResponse{Data: []appwire.Thread{thread}}}
			_ = web.listThreadsWithFallback(context.Background(), l)
			l.err = errors.New("offline")
			_ = web.listThreadsWithFallback(context.Background(), l)
		case 9:
			web := NewWebServer(hubcore.WebConfig{})
			_ = web

		case 10:
			roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: rendezvous.Entry{SessionID: "live", Model: "m"}, SessionID: "live", Status: "active"})
			web := NewWebServer(hubcore.WebConfig{Roster: roster})
			_ = web.isLive("live")
			_ = web.isLive("missing")
			_, _ = web.liveEntry("live")
			p := hubcore.TreeProject{Key: "key", Current: []hubcore.TreeNode{{ID: "live", State: "active"}}, Recent: []hubcore.TreeNode{{ID: "recent", State: "ended"}}, Archived: []hubcore.TreeNode{{ID: "old", State: "closed"}}}
			_ = p
			_ = web.rowRenameable("live")
			_ = hubAttentionSummaryFromCore(appwire.AttentionSummary{NeedsYou: 1})
			_ = web.apiTreeSources()
		case 15:
			dir := t.TempDir()
			meta := schema.SessionMeta{ID: "0123456789abcdef", Name: text, OriginalPrompt: "prompt"}
			_ = schema.SaveSessionMeta(dir, meta)
			pe := hubcore.PastEntry{StateDir: dir, Meta: meta}
			_ = pastTitle(pe)
			_ = searchPastTitle(pe)
			_ = liveTitle(meta.ID, hubcore.LiveEntry{}, nil)
		}
	})
}
