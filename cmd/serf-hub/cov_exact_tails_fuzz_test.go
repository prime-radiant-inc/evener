//go:build serffuzz

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/rendezvous"
)

type exactContractLister struct {
	resp appwire.ModelListResponse
}

type exactListSource struct{ *scriptedAppSource }

func (s *exactListSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread, {ID: "id2", Source: "local"}}}, nil
}

func (*exactContractLister) Spawn(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
	return rendezvous.Entry{}, errors.New("unused")
}
func (*exactContractLister) Resume(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
	return rendezvous.Entry{}, errors.New("unused")
}
func (l *exactContractLister) ListLaunchModelContractForWorkingDir(context.Context, string) (appwire.ModelListResponse, error) {
	return l.resp, nil
}

func FuzzExactTails(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		// Model validation accepts a diagnostic-only provider and exercises the
		// highest-priority working-directory contract interface.
		lister := &exactContractLister{resp: appwire.ModelListResponse{Diagnostics: []appwire.ModelListDiagnostic{{Provider: "p", Message: "unavailable"}}}}
		cfg := hubcore.WebConfig{Spawner: lister}
		_ = hasSerfLaunchModelLister(cfg)
		_ = validateSerfLaunchModel(context.Background(), cfg, cmdutil.ModelRef{Provider: "p", Model: "m"}, "/tmp")

		// Transcript projection tails: malformed JSON, usage cost stamping,
		// empty input images, and default output-image media type.
		entry := hubcore.PastEntry{StateDir: t.TempDir(), Meta: schema.SessionMeta{ID: "missing", Model: "gpt-5"}}
		_ = pastEntryTurns(entry)
		state := filepath.Join(t.TempDir(), "state")
		if err := os.MkdirAll(filepath.Join(state, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
		meta := schema.SessionMeta{ID: "past", Name: "past", Model: "gpt-5"}
		if err := schema.SaveSessionMeta(state, meta); err != nil {
			t.Fatal(err)
		}
		transcript := "not-json\n" + `{"kind":"entry","turn":"invalid"}` + "\n" + `{"kind":"entry","turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"image","image":{"data":""}}]},"usage":{"input_tokens":100,"output_tokens":50}}}` + "\n"
		if err := os.WriteFile(filepath.Join(state, "sessions", "past.transcript.jsonl"), []byte(transcript), 0o600); err != nil {
			t.Fatal(err)
		}
		past := hubcore.NewPastIndex(filepath.Join(filepath.Dir(state), "*"))
		if err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		if pe, ok := past.Find("past"); ok {
			_ = pastEntryTurns(pe)
		}
		_ = appItemsFromReplayTurn("s", "t", 0, hubcore.ReplayTurn{Kind: string(schema.TurnUserInput), Message: hubcore.ReplayMessage{Content: []hubcore.ReplayPart{
			{Kind: string(llm.ContentImage), Image: &hubcore.ReplayImage{}},
		}}}, map[string]string{})
		_ = appItemsFromReplayTurn("s", "t", 0, hubcore.ReplayTurn{Kind: string(schema.TurnAssistant), Message: hubcore.ReplayMessage{Content: []hubcore.ReplayPart{
			{Kind: string(llm.ContentToolResult), ToolResult: &hubcore.ReplayToolResult{ImageData: []byte("x")}},
		}}}, map[string]string{})
		_ = projectReplayInputImage(llm.ImageData{}, nil)
		_ = projectReplayInputImage(llm.ImageData{Data: []byte("x")}, map[string]string{})
		_ = projectReplayOutputImages("s", nil)
		_ = projectReplayOutputImages("s", &llm.ToolResultData{ImageData: []byte("x")})

		thread := appwire.Thread{ID: "id", Source: "local", Status: appwire.ThreadStatus{Type: "nonsense"}, Serf: appwire.SerfThread{Ref: "local:id"}}
		_ = workspaceDataFromAppThread(thread)
		web := NewWebServer(hubcore.WebConfig{})
		web.sources = appsource.NewRegistry()
		_ = web.workspaceData("remote:id")
		data := WorkspaceData{}
		web.fillForkLineage(&data, schema.SessionMeta{ID: "id", ForkLabel: "fork"})

		// List truncation and empty transcript references.
		reg := appsource.NewRegistry()
		src := &exactListSource{scriptedAppSource: &scriptedAppSource{id: "local", thread: thread}}
		reg.Add(src)
		_, _ = hubThreadList(context.Background(), hubcore.WebConfig{}, reg, appwire.ThreadListParams{Limit: 1})
		_, _ = hubThreadTranscriptList(context.Background(), hubcore.WebConfig{}, reg, appwire.ThreadTranscriptListParams{})

		// Defensive HTTP branches that require no ambient services.
		web.handleSubagentPreview(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/?ref=remote:missing", nil))
		pastWeb := NewWebServer(hubcore.WebConfig{Past: past})
		pastWeb.handleSubagentPreview(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/?ref=local:past", nil))
		errSource := &pass6TailSource{scriptedAppSource: &scriptedAppSource{id: "local"}, readErr: errors.New("read")}
		pastWeb.sources.Add(errSource)
		pastWeb.handleSubagentPreview(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/?ref=local:past", nil))

		// Missing live sources are a distinct API failure from ended sessions.
		roster := hubcore.NewRosterWithEntries(
			hubcore.LiveEntry{SessionID: "live-a", Entry: rendezvous.Entry{SessionID: "live-a"}},
			hubcore.LiveEntry{SessionID: "live-b", Entry: rendezvous.Entry{SessionID: "live-b"}},
			hubcore.LiveEntry{},
		)
		liveWeb := NewWebServer(hubcore.WebConfig{Roster: roster})
		liveWeb.sources = appsource.NewRegistry()
		liveWeb.handleApiSearch(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/search", nil))
		liveWeb.handleAPIModel(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"model":"p/m"}`)), "live-a")
		liveWeb.handleAPIReasoningEffort(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"reasoning_effort":"high"}`)), "live-a")
		liveWeb.handleAPIRename(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"new"}`)), "live-a")
		writeJSON(httptest.NewRecorder(), make(chan int))
		web.handleInternalPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/_partials/unknown", nil))
	})
}
