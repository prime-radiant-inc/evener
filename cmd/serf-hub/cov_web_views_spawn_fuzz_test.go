package main

import (
	"bytes"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
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
		_ = errString(nil)
		_ = errString(errors.New("x"))
		_ = tildeHome(filepath.Join(root, "x"))

		pluginDir := filepath.Join(root, "plugin")
		for _, dir := range []string{"good", "bad", "file"} {
			if err := os.MkdirAll(filepath.Join(pluginDir, "skills", dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(pluginDir, "skills", "good", "SKILL.md"), []byte("---\nname: zed\ndescription: useful\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pluginDir, "skills", "bad", "SKILL.md"), []byte("---\nname: missing-description\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pluginDir, "skills", "file", "SKILL.md"), []byte("not frontmatter"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pluginDir, "skills", "loose"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		p := pluginDisplay{Name: "p", Path: pluginDir}
		_ = collectSkillsForPlugin(p)
		_ = collectSkillsForPlugin(pluginDisplay{Path: filepath.Join(root, "absent")})
		_ = skillsFromPlugins([]pluginDisplay{{Name: "z", Path: pluginDir}, {Name: "a", Path: pluginDir}})
		_, _, _ = readSkillFrontmatter(filepath.Join(root, "absent"))
		_ = countHooks(map[plugin.HookEvent][]plugin.RegisteredHook{"A": {{}, {}}})

		cfg := hubcore.WebConfig{PluginDirs: []string{pluginDir}, MCPConfigPath: filepath.Join(root, "missing-mcp")}
		web := NewWebServer(cfg)
		_ = pluginDirsFromConfig(cfg)
		_, _ = web.discoverPluginsForSettings()
		_, _ = web.discoverMCPsForSettings(t.Context(), "")
		_, _ = web.discoverMCPsForSettings(t.Context(), cfg.MCPConfigPath)

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
		_ = contextMeterHTML(-1, 0, 0, -1)
		_ = contextMeterHTML(2, 10, 5, 2)

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

		broken := template.Must(template.New("root").Parse(`{{define "app"}}{{.Missing.Field}}{{end}}{{define "spawn"}}{{.Missing.Field}}{{end}}{{define "workspace"}}{{.Missing.Field}}{{end}}{{define "thread_document"}}{{.Missing.Field}}{{end}}{{define "project_settings"}}{{.Missing.Field}}{{end}}`))
		web.appTmpl, web.spawnTmpl, web.workspaceTmpl, web.threadTmpl, web.projectSettingsTmpl = broken, broken, broken, broken, broken
		for _, target := range []string{"/settings", "/credentials", "/new?dir=/absent", "/thread/missing", "/_partials/s/missing/workspace", "/_partials/settings/project"} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, target, nil)
			switch {
			case target == "/settings":
				web.handleSettings(rec, req)
			case target == "/credentials":
				web.handleCredentials(rec, req)
			case strings.HasPrefix(target, "/new"):
				web.handleWorkspaceSpawn(rec, req)
			case strings.HasPrefix(target, "/thread"):
				web.handleThreadDocument(rec, req)
			case strings.Contains(target, "workspace"):
				web.renderWorkspacePartial(rec, req, "missing")
			default:
				web.renderProjectSettingsPartial(rec, req)
			}
		}

		var out bytes.Buffer
		renderDetailsRow(&out, detailsRow{Label: "x", Value: "y", DataRow: "d", Mono: true, Wide: true, Copy: true})
	})
}
