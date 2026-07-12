package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/hubapi"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/rendezvous"
)

// FuzzWebWorkspacePass5 exercises workspace composition and model rendering
// against in-memory sources and indexes. It never consults a live provider.
func FuzzWebWorkspacePass5(f *testing.F) {
	for mode := uint8(0); mode < 12; mode++ {
		f.Add(mode, "alpha\r\nbeta")
	}
	f.Fuzz(func(t *testing.T, mode uint8, text string) {
		started := time.Now().Add(-90 * time.Second).Unix()
		thread := appwire.Thread{
			ID: "thread", SessionID: "thread", Source: "remote", Name: text,
			Preview: "preview", CWD: "/tmp/work", ModelProvider: "openai/gpt-4o",
			Status: appwire.ThreadStatus{Type: "active"},
			Turns:  []appwire.Turn{{ID: "done", Status: appwire.TurnStatusCompleted}, {ID: "active", Status: appwire.TurnStatusInProgress, StartedAt: &started}},
			Serf: appwire.SerfThread{Ref: "remote:thread", ActiveTurnID: "active", ContextUsed: 12, ContextWindow: 100,
				ContextRemaining: 88, WorkMillis: 61_000, Usage: &appwire.SerfUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
				Capabilities: appwire.ThreadCapabilities{Send: true, Steer: true, Interrupt: true, Compact: true, Queue: true}},
		}
		source := &scriptedAppSource{id: "remote", thread: thread}
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
		web := NewWebServer(hubcore.WebConfig{Past: past, Roster: roster, LiveModels: func(context.Context) []map[string]any {
			return []map[string]any{{"provider": "fixture", "model": "model"}}
		}})
		web.sources.Add(source)

		switch mode % 12 {
		case 0:
			_ = workspaceDataFromAppThread(thread)
			thread.Name, thread.Preview, thread.SessionID, thread.Serf.Ref, thread.Status.Type = "", "", "", "", ""
			_ = workspaceDataFromAppThread(thread)
		case 1:
			for _, d := range []time.Duration{-time.Second, 0, 30 * time.Second, 2 * time.Minute, 2*time.Hour + 4*time.Minute} {
				_ = compactDuration(d)
			}
			_ = activeTurnRunningFor(thread)
			thread.Turns[1].StartedAt = nil
			_ = activeTurnRunningFor(thread)
			_ = activeTurnIDFromAppwireThread(thread)
			thread.Serf.ActiveTurnID = ""
			_ = activeTurnIDFromAppwireThread(thread)
			thread.Turns = nil
			_ = activeTurnIDFromAppwireThread(thread)
		case 2:
			for _, v := range []string{"bad", "local:one", "remote:one", "", ".", "/", "/a/tree"} {
				_ = sourceLabelFromRefText(v)
				_ = worktreeLabel(v)
			}
			for _, n := range []int{-1, 0, 999, 1000, 1500} {
				_ = formatTokenCount(n)
			}
			_ = formatContextNumbers(1, 0, -1)
			_ = formatContextNumbers(1, 2, -1)
			_ = formatCompactContextNumbers(1, 0)
		case 3:
			for _, m := range []schema.SessionMeta{{Name: " name "}, {OriginalPrompt: text}, {ID: "0123456789abcdef"}} {
				_ = sessionTitleFromMeta(m)
			}
			for _, p := range []string{"", "first\r\nsecond", strings.Repeat("x", 90)} {
				_ = compactSessionPromptTitle(p)
			}
			_ = searchPastTitle(hubcore.PastEntry{Meta: child})
			_ = stateLabel("active", true)
		case 4:
			_ = web.workspaceData("remote:thread")
			_ = web.workspaceData("remote:missing")
			_ = web.liveWorkspaceCapabilities("remote:thread", hubapi.SessionCapabilities{Resume: true})
			_, _ = web.liveWorkspaceSnapshot("missing:thread", hubapi.SessionCapabilities{Resume: true})
		case 5:
			_ = web.workspaceData("child")
			_ = web.workspaceData("parent")
			_ = web.workspaceData("missing")
			data := WorkspaceData{}
			web.fillForkLineage(&data, child)
			web.fillSubagentLineage(&data, child)
			web.fillObserverLink(&data, child)
		case 6:
			for _, target := range []string{"/s/", "/s/remote:thread", "/s/remote:thread/state", "/s/remote:thread/details", "/s/remote:thread/tasks", "/s/remote:thread/nope"} {
				web.handleSession(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
			}
		case 7:
			web.renderWorkspacePartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "remote:thread")
			web.renderWorkspacePartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "missing")
			for _, target := range []string{"/thread/remote:thread", "/thread/remote:missing", "/thread/", "/thread/a/b"} {
				web.handleThreadDocument(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
			}
		case 8:
			web.renderDetailsPanel(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "child")
			_ = web.detailsSections("missing")
			var b bytes.Buffer
			renderDetailsRow(&b, detailsRow{Label: text, Value: text, HTML: "<b>x</b>", Copy: true, Mono: true, Wide: true, DataRow: text})
			_ = tokensAndCostRows(text, nil)
			_ = tokensAndCostRows("openai/gpt-4o", thread.Serf.Usage)
			_ = contextMeterHTML(-1, 2, 1, -1)
			_ = contextMeterHTML(2, 2, 1, 0)
			_ = appwireUsageFromHub(nil)
		case 9:
			web.renderSessionTasks(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "remote:thread")
			web.renderSessionTasks(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "missing")
			web.renderInputStrip(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/?thread_document=1", nil), "remote:thread")
		case 10:
			web.handleWorkspaceSpawn(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/new?dir="+t.TempDir()+"&prompt=x", nil))
			_ = safeSpawnEnv()
			_ = launchHarnessIDs(web.cfg)
			for _, body := range []string{"{", `{}`, `{"prompt":"x"}`} {
				web.handleApiSpawn(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/spawn", strings.NewReader(body)))
			}
		case 11:
			for _, target := range []string{"/api/models", "/api/models?diagnostics=1", "/api/models?harness=unknown"} {
				web.handleApiModels(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
			}
			models := modelDescriptorsToAPIModels([]appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-4o"}, {}, {Provider: "z", Model: "m-20251101"}}, nil)
			_ = recentModelEntriesFromDescriptors(models, []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-4o"}, {Provider: "x", Model: "y"}})
			_ = web.overlayLiveEntries(models)
			_ = catalogModelInfo(llm.EmbeddedModelCatalog(), "", text)
			_ = behaviorTagFor(nil, text)
			_ = launchModelListErrorDiagnostic(errors.New(text))
			_ = prettifyModelDisplayName(text)
			_ = isDatedSnapshotModelID(text)
			_ = isDatedSnapshotModelID("provider/model-20251101-v1")
		}
	})
}
