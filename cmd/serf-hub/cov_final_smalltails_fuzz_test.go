package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/launchconfig"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/rendezvous"
)

type finalSmalltailLister struct{ mode int }

func (l *finalSmalltailLister) Spawn(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
	return rendezvous.Entry{}, errors.New("unused")
}
func (l *finalSmalltailLister) Resume(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
	return rendezvous.Entry{}, errors.New("unused")
}
func (l *finalSmalltailLister) ListLaunchModels(context.Context) ([]appwire.ModelDescriptor, error) {
	if l.mode != 0 {
		return nil, errors.New("models")
	}
	return []appwire.ModelDescriptor{{Provider: "p", Model: "m"}}, nil
}

func FuzzFinalSmalltails(f *testing.F) {
	for i := uint8(0); i < 4; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, variant uint8) {
		root := t.TempDir()
		state := filepath.Join(root, "state")
		if err := os.MkdirAll(filepath.Join(state, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
		meta := schema.SessionMeta{ID: "live", Name: "Live", Model: "meta-model", ProfileID: "profile", TurnCount: 3, WorkMillis: 1234, LastInputTokens: 7,
			CumulativeUsage: schema.CumulativeUsage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}
		meta.EnvInfo.WorkingDir = "/meta/work"
		if err := schema.SaveSessionMeta(state, meta); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "sessions", "live.transcript.jsonl"), []byte("not-json\n{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "sessions", "live.api.jsonl"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		past := hubcore.NewPastIndex(filepath.Join(root, "*"))
		if _, err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		status := daemonStatus{Model: "status-model", State: "active", Turns: 4, WorkingDir: "/status/work", ContextPressure: .5,
			ContextUsed: 5, ContextWindow: 10, ContextRemaining: 5, WorkMillis: 2222,
			Usage: &appwire.SerfUsage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6}}
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(status) }))
		defer ts.Close()
		roster := hubcore.NewRosterWithEntries(
			hubcore.LiveEntry{SessionID: "live", Status: "idle", Entry: rendezvous.Entry{SessionID: "live", Address: strings.TrimPrefix(ts.URL, "http://"), PID: 42, Model: "roster-model"}},
			hubcore.LiveEntry{SessionID: "status-only", Status: "idle", Entry: rendezvous.Entry{SessionID: "status-only", Address: strings.TrimPrefix(ts.URL, "http://"), PID: 43}},
		)
		web := NewWebServer(hubcore.WebConfig{Past: past, Roster: roster, StateDir: state, PokeAttention: func() {}})

		_ = web.workspaceData("live")
		_ = web.workspaceData("status-only")
		status.Model, status.WorkingDir, status.State, status.Usage, status.WorkMillis, status.ContextWindow = "", "", "", nil, 0, 0
		_ = web.workspaceData("live")
		_ = workspaceDataFromAppThread(appwire.Thread{Status: appwire.ThreadStatus{}, Serf: appwire.SerfThread{Goal: &appwire.GoalState{Status: "active", Iterations: 2}}})

		bad := filepath.Join(root, "bad-state")
		if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		resolved := launchconfig.Resolved{}
		resolved.Effective.SystemPromptMode = "inline"
		resolved.Effective.SystemPromptText = "x"
		_, _, _ = prepareResolvedForSpawn(bad, resolved)
		_, _, _ = prepareResolvedForSpawn("", resolved)
		_, _ = SpawnDaemon(context.Background(), "", root, hubcore.SpawnRequest{}, time.Millisecond)
		_, _ = ResumeDaemon(context.Background(), "", root, hubcore.ResumeRequest{}, time.Millisecond)
		_ = validateSerfLaunchContract(context.Background(), "", "", nil)
		_, _ = listSerfLaunchModelContract(context.Background(), "", nil)
		_ = openAIStoredOAuthUsable(nil)

		for _, mode := range []int{0, 1} {
			cfg := hubcore.WebConfig{Spawner: &finalSmalltailLister{mode: mode}}
			_, _ = serfLaunchModelList(context.Background(), cfg, "")
			_ = hasSerfLaunchModelLister(cfg)
			_ = validateSerfLaunchModel(context.Background(), cfg, cmdutil.ModelRef{Provider: "missing", Model: "m"}, "")
		}

		thread := appwire.Thread{ID: "child", Source: "remote", Serf: appwire.SerfThread{Kind: "subagent", ParentRef: "remote:root"}, Turns: []appwire.Turn{{Items: []appwire.ThreadItem{{Type: "agentMessage", Text: "x"}}}}}
		_ = subagentPreviewFromThread(thread, "", 1)
		_, _ = hubThreadTranscriptList(context.Background(), hubcore.WebConfig{}, web.sources, appwire.ThreadTranscriptListParams{})
		_ = threadRef(appwire.Thread{})
		_ = transcriptTargetSource("bad", "fallback")
		_, _ = pastEntryTurns(hubcore.PastEntry{Meta: meta, StateDir: state})
		_ = appItemsFromReplayTurn("s", "t", 0, hubcore.ReplayTurn{}, map[string]string{})
		_ = variant
	})
}
