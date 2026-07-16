//go:build serffuzz

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
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/launchconfig"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/rendezvous"
)

type exactContractLister struct {
	resp appwire.ModelListResponse
}

type exactListSource struct{ *scriptedAppSource }

type exactNameSource struct{ *scriptedAppSource }

func (*exactNameSource) SetThreadName(context.Context, appwire.ThreadNameSetParams) error { return nil }

func (s *exactListSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: []appwire.Thread{
		s.thread,
		{ID: "id2", Source: "local"},
		{Serf: appwire.SerfThread{Kind: "subagent", ParentRef: "local:root"}},
	}}, nil
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
		_, _ = ResumeDaemon(context.Background(), "", t.TempDir(), hubcore.ResumeRequest{}, time.Nanosecond)

		oldContract := listSerfLaunchModelContractFn
		listSerfLaunchModelContractFn = func(context.Context, string, []string) (appwire.ModelListResponse, error) {
			return appwire.ModelListResponse{}, errors.New("contract")
		}
		_, _ = (&HubSpawner{}).ListLaunchModels(context.Background())
		listSerfLaunchModelContractFn = oldContract

		inline := launchconfig.Resolved{}
		inline.Effective.SystemPromptMode = "inline"
		inline.Effective.SystemPromptText = "system"
		oldMkdirAll, oldMkdirTemp := spawnMkdirAll, spawnMkdirTemp
		oldWriteFile, oldRemoveAll := spawnWriteFile, spawnRemoveAll
		spawnMkdirAll = func(string, os.FileMode) error { return errors.New("mkdir") }
		_, _, _ = prepareResolvedForSpawn(t.TempDir(), inline)
		spawnMkdirAll = oldMkdirAll
		spawnMkdirTemp = func(string, string) (string, error) { return "", errors.New("temp") }
		_, _, _ = prepareResolvedForSpawn(t.TempDir(), inline)
		spawnMkdirTemp = oldMkdirTemp
		spawnWriteFile = func(string, []byte, os.FileMode) error { return errors.New("write") }
		spawnRemoveAll = func(string) error { return nil }
		_, _, _ = prepareResolvedForSpawn(t.TempDir(), inline)
		appendInline := launchconfig.Resolved{}
		appendInline.Effective.SystemPromptAppendMode = "inline"
		appendInline.Effective.SystemPromptAppendText = "append"
		_, _, _ = prepareResolvedForSpawn(t.TempDir(), appendInline)
		spawnWriteFile, spawnRemoveAll = oldWriteFile, oldRemoveAll

		store, err := credentials.LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
		if err != nil {
			t.Fatal(err)
		}
		oldOAuth := openAIStoredOAuthUsableForLaunch
		openAIStoredOAuthUsableForLaunch = func([]string) bool { return true }
		_ = validateProviderCredentials("openai", store, nil, "")
		openAIStoredOAuthUsableForLaunch = oldOAuth

		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		_ = validateSerfLaunchContract(canceled, "/bin/true", "", nil)
		_, _ = listSerfLaunchModelContract(canceled, "/bin/true", nil)

		oldListRendezvous := listRendezvousForWait
		startedAfter := time.Now().UTC()
		listRendezvousForWait = func(string) ([]rendezvous.Entry, error) {
			return []rendezvous.Entry{{PID: 42, StartedAt: startedAfter.Add(-time.Second)}}, nil
		}
		_, _ = waitForRendezvousOrExit(canceled, t.TempDir(), 42, nil, WithStartedAfter(startedAfter))
		listRendezvousForWait = oldListRendezvous

		// Transcript projection tails: malformed JSON, usage cost stamping,
		// empty input images, and default output-image media type.
		entry := hubcore.PastEntry{StateDir: t.TempDir(), Meta: schema.SessionMeta{ID: "missing", Model: "gpt-5"}}
		_, _ = pastEntryTurns(entry)
		state := filepath.Join(t.TempDir(), "state")
		if err := os.MkdirAll(filepath.Join(state, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
		meta := schema.SessionMeta{ID: "past", Name: "past", Model: "gpt-5"}
		meta.EnvInfo.WorkingDir = "/work/delete"
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
			_, _ = pastEntryTurns(pe)
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
		_ = NewWebServer(hubcore.WebConfig{CodexLaunches: []codexlaunch.CodexLaunchConfig{{ID: "codex"}}})
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
		pastWeb.sources = appsource.NewRegistry()
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

		// Model a rename race: the first liveness check is stale, while the
		// pre-write roster recheck observes the resumed session.
		oldRenameLive := isLiveForRename
		isLiveForRename = func(*WebServer, string) bool { return false }
		liveWeb.handleAPIRename(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"new"}`)), "live-a")
		liveWeb.sources.Add(&scriptedAppSource{id: "local"})
		liveWeb.handleAPIRename(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"new"}`)), "live-a")
		liveWeb.sources = appsource.NewRegistry()
		liveWeb.sources.Add(&exactNameSource{scriptedAppSource: &scriptedAppSource{id: "local"}})
		liveWeb.cfg.Past = past
		liveWeb.handleAPIRename(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"new"}`)), "live-a")
		isLiveForRename = oldRenameLive

		oldManagedList := ensureManagedCodexSourcesForList
		ensureManagedCodexSourcesForList = func(context.Context, hubcore.WebConfig, *appsource.Registry, appwire.ThreadListParams) error {
			return errors.New("managed source")
		}
		_, _ = hubThreadList(context.Background(), hubcore.WebConfig{}, appsource.NewRegistry(), appwire.ThreadListParams{})
		ensureManagedCodexSourcesForList = oldManagedList

		// Project deletion's second liveness check can race with a resume.
		deleteWeb := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})
		tree, _ := deleteWeb.memoTree(context.Background())
		projects := append(append([]hubcore.TreeProject(nil), tree.Projects...), tree.ArchivedProjects...)
		if len(projects) > 0 {
			body, _ := json.Marshal(map[string]string{"key": projects[0].Key, "working_dir": projects[0].WorkingDir})
			checks := 0
			oldProjectLive := projectSessionLive
			projectSessionLive = func(*hubcore.Roster, string) bool {
				checks++
				return checks > 1
			}
			deleteWeb.handleAPIProjectDelete(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body))))
			projectSessionLive = oldProjectLive
		}

		lineagePast := hubcore.NewPastIndex("")
		lineagePast.SeedForTest([]schema.SessionMeta{{ID: "", ParentSessionID: "parent"}})
		lineageWeb := NewWebServer(hubcore.WebConfig{Past: lineagePast})
		lineageWeb.fillForkLineage(&data, schema.SessionMeta{ID: "parent", ForkLabel: "fork"})
		lineageWeb.handleSession(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/s/missing/fork", nil))

		oldTranscriptRoot := hubTranscriptRootForList
		hubTranscriptRootForList = func(context.Context, hubcore.WebConfig, *appsource.Registry, string) (appwire.Thread, error) {
			return appwire.Thread{}, nil
		}
		_, _ = hubThreadTranscriptList(context.Background(), hubcore.WebConfig{}, appsource.NewRegistry(), appwire.ThreadTranscriptListParams{Ref: "fallback"})
		_, _ = hubThreadTranscriptList(context.Background(), hubcore.WebConfig{}, appsource.NewRegistry(), appwire.ThreadTranscriptListParams{})
		duplicateRegistry := appsource.NewRegistry()
		duplicateRegistry.Add(&exactListSource{scriptedAppSource: &scriptedAppSource{
			id: "local",
			thread: appwire.Thread{ID: "root", Source: "local", Serf: appwire.SerfThread{
				Ref: "local:root", Kind: "subagent", ParentRef: "local:root",
			}},
		}})
		hubTranscriptRootForList = func(context.Context, hubcore.WebConfig, *appsource.Registry, string) (appwire.Thread, error) {
			return appwire.Thread{ID: "root", Source: "local", Serf: appwire.SerfThread{Ref: "local:root"}}, nil
		}
		_, _ = hubThreadTranscriptList(context.Background(), hubcore.WebConfig{}, duplicateRegistry, appwire.ThreadTranscriptListParams{Ref: "local:root"})
		hubTranscriptRootForList = oldTranscriptRoot

		oldEnsureAction := ensureAPIActionAvailable
		ensureAPIActionAvailable = func(*WebServer, string, string) error { return nil }
		liveWeb.sources = appsource.NewRegistry()
		liveWeb.handleAPIClear(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil), "live-a")
		liveWeb.handleAPIModel(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"model":"p/m"}`)), "live-a")
		liveWeb.sources.Add(&exactNameSource{scriptedAppSource: &scriptedAppSource{id: "local"}})
		ensureAPIActionAvailable = func(*WebServer, string, string) error { return errors.New("denied") }
		liveWeb.handleAPIModel(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"model":"p/m"}`)), "live-a")
		ensureAPIActionAvailable = oldEnsureAction
		_ = appendProjectDeleteLiveSkip(nil, "id")
		sortLiveForSearch([]hubcore.LiveEntry{{SessionID: "b"}, {SessionID: "a"}}, nil)
		t.Setenv(envvars.Home.Name, "")
		liveWeb.handleApiDirs(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/dirs", nil))
		writeJSON(httptest.NewRecorder(), make(chan int))
		(&WebServer{}).handleManifest(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))
		partialReq := httptest.NewRequest(http.MethodGet, "/_partials/workspace/spawn", nil)
		partialReq.Header.Set("HX-Request", "true")
		web.handleInternalPartial(httptest.NewRecorder(), partialReq)
		web.handleInternalPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/_partials/unknown", nil))
	})
}
