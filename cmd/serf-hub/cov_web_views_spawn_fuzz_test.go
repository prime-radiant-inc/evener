package main

import (
	"errors"
	"html/template"
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
	"primeradiant.com/serf/llm/providercfg"
)

// FuzzCovWebViewsSpawn drives deterministic edge seeds through the web view
// helpers. The byte is deliberately ignored: this target is a coverage seed,
// while the existing handler fuzzers own arbitrary request mutation.
func FuzzCovWebViewsSpawn(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		root := t.TempDir()
		file := filepath.Join(root, "sized")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, size := range []int64{12, 2 << 10, 3 << 20, 1 << 30} {
			if err := os.Truncate(file, size); err != nil {
				t.Fatal(err)
			}
			_ = fileSizeHuman(file)
		}
		for _, age := range []time.Duration{time.Minute, time.Hour, 24 * time.Hour, 72 * time.Hour} {
			when := time.Now().Add(-age)
			if err := os.Chtimes(file, when, when); err != nil {
				t.Fatal(err)
			}
			_ = fileAgeHuman(file)
		}
		_ = fileAgeHuman(filepath.Join(root, "missing"))
		_ = fileSizeHuman(filepath.Join(root, "missing"))
		_ = tildeHome(filepath.Join(root, "x"))

		web := NewWebServer(hubcore.WebConfig{})

		for _, s := range []string{"", "x", "foo-20250101", "a/b-20250101-v2", "-x"} {
			_ = isDatedSnapshotModelID(s)
			_ = prettifyModelDisplayName(s)
		}
		models := []map[string]any{{"provider": "z", "model": "m-20250101"}, {"provider": "a", "model": "m"}, {"provider": "z", "model": "m"}}
		sortModelEntriesDatedLast(models)
		_ = recentModelEntriesFromDescriptors(models, nil)
		_ = recentModelEntriesFromDescriptors(models, []appwire.ModelDescriptor{{Provider: "z", Model: "m"}, {Provider: "none", Model: "none"}})
		_ = modelDescriptorsToAPIModels(nil, nil)
		_ = modelDescriptorsToAPIModels([]appwire.ModelDescriptor{{}, {Provider: "openai", Model: "gpt-4o"}}, nil)
		_ = catalogModelInfo(nil, "", "")
		no, yes := false, true
		providerCfg := &providercfg.Config{Instances: []providercfg.InstanceConfig{
			{Name: "plain", Type: "ollama"},
			{Name: "custom", Type: "openai", APIStyle: providercfg.StyleChatCompletions, Models: map[string]providercfg.ModelConfig{
				"off":    {Reasoning: &no},
				"levels": {ThinkingLevels: map[string]string{"high": "hard", "low": "easy"}},
				"on":     {Reasoning: &yes, ContextWindow: 1234},
			}},
		}}
		for _, model := range []string{"missing", "off", "levels", "on"} {
			entry := map[string]any{}
			applyInstanceModelOverride(entry, providerCfg, "custom", model)
		}
		applyInstanceModelOverride(map[string]any{}, providerCfg, "absent", "x")
		_ = behaviorTagFor(providerCfg, "plain")
		_ = behaviorTagFor(providerCfg, "custom")
		_ = behaviorTagFor(providerCfg, "absent")
		_ = serfUsageFromCumulative(schema.CumulativeUsage{})
		_ = serfUsageFromCumulative(schema.CumulativeUsage{InputTokens: 1})

		for _, tc := range []struct{ method, target, body string }{
			{http.MethodGet, "/api/spawn", ""},
			{http.MethodPost, "/api/spawn", "{"},
			{http.MethodPost, "/api/spawn", `{}`},
		} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
			web.handleApiSpawn(rec, req)
		}
		for _, err := range []error{errors.New("x"), appwire.InvalidParams("bad"), appwire.Unavailable("down")} {
			writeSpawnError(httptest.NewRecorder(), err)
		}
		writeModelsResponse(httptest.NewRecorder(), nil, nil, nil, true)
		writeModelsResponse(httptest.NewRecorder(), models, nil, nil, false)

		sb := newSandbox(t)
		for _, target := range []string{"/settings/launch", "/settings/project?cwd=" + root, "/settings/general"} {
			sb.Web.handleSettings(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
		}
		hx := httptest.NewRequest(http.MethodGet, "/settings", nil)
		hx.Header.Set("HX-Request", "true")
		sb.Web.handleSettings(httptest.NewRecorder(), hx)
		_ = sb.Web.workspaceDataForRender(sandboxSessionID)
		sb.Web.renderSessionTasks(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), sandboxSessionID)
		sb.Web.renderSessionTasks(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "missing")
		sb.Web.fillForkLineage(&WorkspaceData{}, schema.SessionMeta{})
		sb.Web.fillForkLineage(&WorkspaceData{}, schema.SessionMeta{ID: "id", ForkLabel: "fork", DivergenceTurn: 2})
		sb.Web.fillSubagentLineage(&WorkspaceData{}, schema.SessionMeta{})
		sb.Web.fillSubagentLineage(&WorkspaceData{}, schema.SessionMeta{IsSubagent: true, ParentSessionID: "parent"})
		sb.Web.fillObserverLink(&WorkspaceData{}, schema.SessionMeta{})
		observerData := WorkspaceData{}
		sb.Web.fillObserverLink(&observerData, schema.SessionMeta{ObservedBy: []string{"", "observer", "observer"}})
		for _, target := range []string{
			"/s/", "/s/" + sandboxSessionID, "/s/" + sandboxSessionID + "/state",
			"/s/" + sandboxSessionID + "/details", "/s/" + sandboxSessionID + "/tasks",
			"/s/" + sandboxSessionID + "/unknown", "/s/" + sandboxSessionID + "/images/bad",
			"/s/" + sandboxSessionID + "/send", "/s/" + sandboxSessionID + "/fork",
			"/s/" + sandboxSessionID + "/steer", "/s/" + sandboxSessionID + "/queue",
			"/s/" + sandboxSessionID + "/drain-as-steer",
		} {
			sb.Web.handleSession(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
		}
		hxSession := httptest.NewRequest(http.MethodGet, "/s/"+sandboxSessionID, nil)
		hxSession.Header.Set("HX-Request", "true")
		sb.Web.handleSession(httptest.NewRecorder(), hxSession)
		for _, target := range []string{"/thread/", "/thread/a/b"} {
			sb.Web.handleThreadDocument(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
		}
		sb.Web.handleThreadDocument(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/thread/x", nil))

		broken := template.Must(template.New("root").Parse(`{{define "app"}}{{.Missing.Field}}{{end}}{{define "thread_document"}}{{.Missing.Field}}{{end}}`))
		web.appTmpl, web.threadTmpl = broken, broken
		for _, target := range []string{"/settings", "/credentials", "/thread/missing"} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, target, nil)
			switch target {
			case "/settings":
				web.handleSettings(rec, req)
			case "/credentials":
				web.handleCredentials(rec, req)
			default:
				web.handleThreadDocument(rec, req)
			}
		}
	})
}
