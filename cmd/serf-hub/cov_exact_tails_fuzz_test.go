//go:build serffuzz

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
		_ = appItemsFromReplayTurn("s", "t", 0, hubcore.ReplayTurn{Kind: string(schema.TurnUserInput), Message: hubcore.ReplayMessage{Content: []hubcore.ReplayPart{
			{Kind: string(llm.ContentImage), Image: &hubcore.ReplayImage{}},
		}}}, map[string]string{})
		_ = appItemsFromReplayTurn("s", "t", 0, hubcore.ReplayTurn{Kind: string(schema.TurnAssistant), Message: hubcore.ReplayMessage{Content: []hubcore.ReplayPart{
			{Kind: string(llm.ContentToolResult), ToolResult: &hubcore.ReplayToolResult{ImageData: []byte("x")}},
		}}}, map[string]string{})

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
		writeJSON(httptest.NewRecorder(), make(chan int))
		web.handleInternalPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/_partials/unknown", nil))
	})
}
