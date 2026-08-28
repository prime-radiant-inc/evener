package hub

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm/providercfg"
)

// FuzzCovWebViewsSpawn drives deterministic edge seeds through the web view
// helpers. Its historical name is retained for fuzz corpus stability. The byte
// is deliberately ignored: this target is a coverage seed, while the existing
// handler fuzzers own arbitrary request mutation.
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
		models := []appwire.ModelDescriptor{{Provider: "z", Model: "m-20250101"}, {Provider: "a", Model: "m"}, {Provider: "z", Model: "m"}}
		sortModelDescriptors(models)
		_ = enrichModelDescriptors(nil, nil)
		_ = enrichModelDescriptors([]appwire.ModelDescriptor{{}, {Provider: "openai", Model: "gpt-4o"}}, nil)
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
			entry := appwire.ModelDescriptor{}
			applyInstanceModelOverride(&entry, providerCfg, "custom", model)
		}
		applyInstanceModelOverride(&appwire.ModelDescriptor{}, providerCfg, "absent", "x")
		_ = behaviorTagFor(providerCfg, "plain")
		_ = behaviorTagFor(providerCfg, "custom")
		_ = behaviorTagFor(providerCfg, "absent")
		_ = evenerUsageFromCumulative(schema.CumulativeUsage{})
		_ = evenerUsageFromCumulative(schema.CumulativeUsage{InputTokens: 1})

		sb := newSandbox(t)
		for _, target := range []string{"/settings/launch", "/settings/project?cwd=" + root, "/settings/general"} {
			sb.Web.handleSettings(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
		}
		hx := httptest.NewRequest(http.MethodGet, "/settings", nil)
		hx.Header.Set("HX-Request", "true")
		sb.Web.handleSettings(httptest.NewRecorder(), hx)
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
