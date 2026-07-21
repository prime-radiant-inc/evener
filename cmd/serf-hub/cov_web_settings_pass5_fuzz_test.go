//go:build serffuzz

package main

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// FuzzWebSettingsPass5 covers settings' filesystem discovery, formatting, and
// fail-soft rendering with deterministic, process-local fixtures.
func FuzzWebSettingsPass5(f *testing.F) {
	for mode := uint8(0); mode < 8; mode++ {
		f.Add(mode)
	}
	f.Fuzz(func(t *testing.T, mode uint8) {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		xdg := filepath.Join(root, "xdg")
		t.Setenv("HOME", home)
		if mode&1 == 0 {
			t.Setenv("XDG_CONFIG_HOME", xdg)
		} else {
			t.Setenv("XDG_CONFIG_HOME", "")
		}

		_ = defaultPluginsRoot()
		_ = defaultMCPConfigPath()
		_ = tildeHome(home)
		_ = tildeHome(filepath.Join(home, "child"))
		_ = tildeHome(filepath.Join(root, "outside"))
		_ = errString(nil)
		_ = errString(errors.New("fixture"))
		t.Setenv("HOME", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		_ = defaultPluginsRoot()
		_ = defaultMCPConfigPath()
		_ = tildeHome("unchanged")
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", xdg)

		file := filepath.Join(root, "data")
		if err := os.WriteFile(file, []byte("abc"), 0o600); err != nil {
			t.Fatal(err)
		}
		sizes := []int64{3, 2 << 10, 2 << 20, 2 << 30}
		if err := os.Truncate(file, sizes[int(mode)%len(sizes)]); err != nil {
			t.Fatal(err)
		}
		ages := []time.Duration{time.Second, 10 * time.Minute, 3 * time.Hour, 72 * time.Hour}
		when := time.Now().Add(-ages[int(mode)%len(ages)])
		if err := os.Chtimes(file, when, when); err != nil {
			t.Fatal(err)
		}
		_ = fileSizeHuman(file)
		_ = fileSizeHuman(filepath.Join(root, "missing"))
		_ = fileAgeHuman(file)
		_ = fileAgeHuman(filepath.Join(root, "missing"))

		pluginsRoot := defaultPluginsRoot()
		valid := filepath.Join(pluginsRoot, "z-valid")
		invalid := filepath.Join(pluginsRoot, "a-invalid")
		second := filepath.Join(pluginsRoot, "b-valid")
		noManifest := filepath.Join(pluginsRoot, "no-manifest")
		plain := filepath.Join(pluginsRoot, "plain-file")
		for _, dir := range []string{valid, invalid, second} {
			if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(valid, ".claude-plugin", "plugin.json"), []byte(`{"name":"zeta","version":"1"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(invalid, ".claude-plugin", "plugin.json"), []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(second, ".claude-plugin", "plugin.json"), []byte(`{"name":"beta","version":"2"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(noManifest, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = pluginDirsFromConfig(hubcore.WebConfig{PluginDirs: []string{"explicit"}})
		_ = pluginDirsFromConfig(hubcore.WebConfig{})
		_ = pluginDirsFromConfig(hubcore.WebConfig{PluginDirs: []string{}})
		t.Setenv("HOME", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		_ = pluginDirsFromConfig(hubcore.WebConfig{})
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", xdg)

		skillsRoot := filepath.Join(valid, "skills")
		fixtures := map[string]string{
			"good":    "---\nname: beta\ndescription: useful\n---\n",
			"alpha":   "---\nname: alpha\ndescription: first\n---\n",
			"invalid": "not frontmatter",
			"missing": "---\nname: \ndescription: absent\n---\n",
		}
		for dir, body := range fixtures {
			path := filepath.Join(skillsRoot, dir)
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(skillsRoot, "not-a-dir"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		p := pluginDisplay{Name: "zeta", Path: valid}
		_ = collectSkillsForPlugin(p)
		_ = collectSkillsForPlugin(pluginDisplay{Path: filepath.Join(root, "absent")})
		_ = skillsFromPlugins([]pluginDisplay{{Name: "zeta", Path: valid}, {Name: "alpha", Path: valid}})
		_, _, _ = readSkillFrontmatter(filepath.Join(skillsRoot, "good", "SKILL.md"))
		_, _, _ = readSkillFrontmatter(filepath.Join(skillsRoot, "invalid", "SKILL.md"))
		_, _, _ = readSkillFrontmatter(filepath.Join(root, "absent"))
		_ = countHooks(map[plugin.HookEvent][]plugin.RegisteredHook{"PreToolUse": {{}, {}}})

		projectA := filepath.Join(root, "projects", "one")
		projectB := filepath.Join(root, "projects", "two")
		for i, project := range []string{projectA, projectB, projectB, projectB} {
			workingDir := filepath.Join(root, "work", filepath.Base(project))
			if i == 1 {
				workingDir = ""
			} else if i == 2 {
				workingDir = filepath.Join(root, "work", "one")
			} else if i == 3 {
				workingDir = filepath.Join(root, "work", "zero")
			}
			if err := schema.SaveSessionMeta(project, schema.SessionMeta{ID: string(rune('A' + i)), UpdatedAt: time.Now(), EnvInfo: schema.EnvironmentInfo{WorkingDir: workingDir}}); err != nil {
				t.Fatal(err)
			}
		}
		past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
		if _, err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		web := NewWebServer(hubcore.WebConfig{PluginDirs: []string{valid, invalid, second}, Past: past, PastIndexPath: file, HubStateRoot: root})
		_, _ = web.discoverPluginsForSettings()
		emptyWeb := NewWebServer(hubcore.WebConfig{PluginDirs: []string{filepath.Join(root, "absent")}})
		_, _ = emptyWeb.discoverPluginsForSettings()
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "no-config"))
		_, _ = (&WebServer{}).discoverPluginsForSettings()
		t.Setenv("XDG_CONFIG_HOME", xdg)

		mcp := filepath.Join(root, "mcp.json")
		_ = (&WebServer{cfg: hubcore.WebConfig{MCPConfigPath: mcp}}).mcpConfigPathForSettings()
		if err := os.WriteFile(mcp, []byte(`{"mcpServers":{"z":{"command":"definitely-not-a-real-command"},"a":{"type":"http","url":"://bad"}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = web.discoverMCPsForSettings(context.Background(), "")
		_, _ = web.discoverMCPsForSettings(context.Background(), filepath.Join(root, "absent.json"))
		_, _ = web.discoverMCPsForSettings(context.Background(), mcp)
		if err := os.WriteFile(mcp, []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = web.discoverMCPsForSettings(context.Background(), mcp)

		reqs := []*http.Request{
			httptest.NewRequest(http.MethodGet, "/settings", nil),
			httptest.NewRequest(http.MethodGet, "/settings/launch", nil),
			httptest.NewRequest(http.MethodGet, "/settings/project?cwd=%00", nil),
		}
		hx := httptest.NewRequest(http.MethodGet, "/settings", nil)
		hx.Header.Set("HX-Request", "true")
		reqs = append(reqs, hx)
		for _, req := range reqs {
			web.handleSettings(httptest.NewRecorder(), req)
		}
		web.renderSettingsPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "launch")
		web.renderSettingsPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "unknown")
		web.renderSettingsPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "general")
		web.renderSettingsPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "project")
		web.renderProjectSettingsPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/?cwd=%00", nil))
		web.renderProjectSettingsPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/?cwd="+root, nil))
		web.renderProjectSettingsPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

		oldList, oldAbs := settingsLaunchModelList, settingsAbsPath
		t.Cleanup(func() { settingsLaunchModelList, settingsAbsPath = oldList, oldAbs })
		settingsLaunchModelList = func(context.Context, hubcore.WebConfig, string) (appwire.ModelListResponse, error) {
			return appwire.ModelListResponse{Data: []appwire.ModelDescriptor{
				{Provider: "z-provider", Model: "one"},
				{Provider: "z-provider", Model: "two"},
				{Provider: "a-provider", Model: "three"},
			}}, nil
		}
		web.renderSettingsPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "providers")
		settingsLaunchModelList = func(context.Context, hubcore.WebConfig, string) (appwire.ModelListResponse, error) {
			return appwire.ModelListResponse{}, errors.New("model fixture")
		}
		web.renderSettingsPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "providers")
		settingsAbsPath = func(string) (string, error) { return "", errors.New("abs fixture") }
		web.renderProjectSettingsPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/?cwd=relative", nil))

		badApp := template.Must(template.New("root").Parse(`{{define "app"}}{{call .WorkspaceURL}}{{end}}`))
		badSettings := template.Must(template.New("root").Parse(`{{define "settings"}}{{.NoSuchField}}{{end}}{{define "settings-content"}}{{.NoSuchField}}{{end}}{{define "project_settings"}}{{.NoSuchField}}{{end}}`))
		broken := &WebServer{cfg: hubcore.WebConfig{}, appTmpl: badApp, projectSettingsTmpl: badSettings, settingsTmpls: map[string]*template.Template{"general": badSettings, "credentials": badSettings}}
		broken.handleSettings(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/settings", nil))
		broken.renderSettingsPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "general")
		contentReq := httptest.NewRequest(http.MethodGet, "/", nil)
		contentReq.Header.Set("HX-Target", "settings-content")
		broken.renderSettingsPartial(httptest.NewRecorder(), contentReq, "general")
		broken.renderProjectSettingsPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		broken.handleCredentials(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/credentials", nil))
		broken.handleCredentialsPartial(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	})
}
