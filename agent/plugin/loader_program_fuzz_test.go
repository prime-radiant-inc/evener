//go:build serffuzz

package plugin

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// FuzzPluginLoaderProgram builds a bounded set of real plugin trees and drives
// their complete loader path: manifest flavor selection, skill/agent/command
// discovery, inline/file/default hooks, MCP layering, settings, and batch
// loading. Fuzz bytes select equivalent manifest shapes and safe fixture text;
// they never become filesystem paths or executable commands.
//
// The semantic oracle checks namespacing, component ordering, MCP shadowing,
// hook classification/source metadata, fail-soft batch decisions, settings
// behavior, and deterministic normalized loader traces. No command, MCP, or
// provider process is started: this target only reads its per-test temp tree.
func FuzzPluginLoaderProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{0x00},
		{0x01, 0x31},
		{0x02, 0x72, 0x21},
		{0x07, 0xff, 0x00, 0x4a},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		reader := pluginLoaderProgramReader{data: program}
		mode := reader.byte()
		token := reader.token()

		first := runPluginLoaderProgram(t, mode, token)
		second := runPluginLoaderProgram(t, mode, token)
		if !reflect.DeepEqual(first, second) {
			firstJSON, _ := json.Marshal(first)
			secondJSON, _ := json.Marshal(second)
			t.Fatalf("plugin loader program is not deterministic:\nfirst=%s\nsecond=%s", firstJSON, secondJSON)
		}
	})
}

type pluginLoaderProgramReader struct {
	data []byte
	pos  int
}

func (r *pluginLoaderProgramReader) byte() byte {
	if len(r.data) == 0 {
		return 0
	}
	b := r.data[r.pos%len(r.data)]
	r.pos++
	return b
}

func (r *pluginLoaderProgramReader) token() string {
	buf := make([]byte, int(r.byte()%16)+1)
	for i := range buf {
		buf[i] = r.byte()
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

type pluginLoaderProgramFixture struct {
	root            string
	workDir         string
	alpha           string
	beta            string
	gamma           string
	warn            string
	duplicate       string
	badAgent        string
	badCommand      string
	broken          string
	alphaHooks      []byte
	betaHookPath    string
	gammaHookPath   string
	settingsBody    string
	settingsPlugin  string
	invalidSettings string
	token           string
}

type pluginLoaderProgramTrace struct {
	Instances       []pluginLoaderProgramInstance
	LoadAllNames    []string
	FailSoftNames   []string
	Skipped         []string
	ParserHookCount int
	SettingsBody    string
}

type pluginLoaderProgramInstance struct {
	Name        string
	Flavor      string
	Skills      []string
	Agents      []string
	Commands    []string
	Hooks       []string
	MCP         []string
	Unsupported []string
	Unknown     []string
	Warnings    int
}

func runPluginLoaderProgram(t *testing.T, mode byte, token string) pluginLoaderProgramTrace {
	t.Helper()
	fx := buildPluginLoaderProgramFixture(t, mode, token)

	alpha, err := Load(fx.alpha)
	if err != nil {
		t.Fatalf("Load(alpha): %v", err)
	}
	assertLoaderProgramAlpha(t, alpha, fx)

	// Re-loading the same on-disk plugin must produce the same semantic view.
	alphaAgain, err := Load(fx.alpha)
	if err != nil {
		t.Fatalf("Load(alpha again): %v", err)
	}
	if got, want := summarizePluginLoaderProgramInstance(alphaAgain), summarizePluginLoaderProgramInstance(alpha); !reflect.DeepEqual(got, want) {
		t.Fatalf("Load(alpha) changed semantic output:\ngot=%#v\nwant=%#v", got, want)
	}

	beta, err := Load(fx.beta)
	if err != nil {
		t.Fatalf("Load(beta): %v", err)
	}
	assertLoaderProgramBeta(t, beta, fx)

	gamma, err := Load(fx.gamma)
	if err != nil {
		t.Fatalf("Load(gamma): %v", err)
	}
	assertLoaderProgramGamma(t, gamma, fx)

	warn, err := Load(fx.warn)
	if err != nil {
		t.Fatalf("Load(warn): MCP failures must be non-fatal: %v", err)
	}
	if len(warn.MCPConfigs) != 0 || len(warn.MCPConfigWarnings) != 2 {
		t.Fatalf("warn MCP result = configs:%#v warnings:%#v, want no configs and two warnings", warn.MCPConfigs, warn.MCPConfigWarnings)
	}
	for _, warning := range warn.MCPConfigWarnings {
		if !strings.Contains(warning, "warn") {
			t.Fatalf("MCP warning %q does not identify its plugin", warning)
		}
	}

	assertLoaderProgramFailure(t, fx.badAgent, "bad agent")
	assertLoaderProgramFailure(t, fx.badCommand, "bad command")

	// Exercise the compatibility wrappers as well as Load's diagnostic form.
	parsedHooks, err := parsePluginHooks(fx.alphaHooks, alpha.Dir, alpha.Manifest.Name)
	if err != nil || len(parsedHooks[HookPreToolUse]) != 2 {
		t.Fatalf("parsePluginHooks(alpha inline): hooks=%#v err=%v", parsedHooks, err)
	}
	parsedDiag, unsupported, unknown, err := parsePluginHooksDiag(fx.alphaHooks, alpha.Dir, alpha.Manifest.Name)
	if err != nil || len(parsedDiag[HookPreToolUse]) != 2 || !unsupported[HookEvent("WorktreeCreate")] || !unknown["FuturePluginEvent"] {
		t.Fatalf("parsePluginHooksDiag(alpha inline): hooks=%#v unsupported=%#v unknown=%#v err=%v", parsedDiag, unsupported, unknown, err)
	}
	fileHooks, err := discoverPluginHooks(fx.beta, json.RawMessage(`"hooks/custom.json"`), "beta")
	if err != nil || len(fileHooks[HookPostToolUse]) != 1 {
		t.Fatalf("discoverPluginHooks(file): hooks=%#v err=%v", fileHooks, err)
	}

	// Qualified and unique bare lookups have deterministic meaning; an absent
	// command must not invent a zero-value hit.
	if command, ok := ResolveCommand(alpha.Commands, "alpha:hello"); !ok || command.Name != "hello" {
		t.Fatalf("qualified ResolveCommand = %#v, %v", command, ok)
	}
	if command, ok := ResolveCommand(alpha.Commands, "review"); !ok || command.Name != "review" {
		t.Fatalf("unique bare ResolveCommand = %#v, %v", command, ok)
	}
	if _, ok := ResolveCommand(alpha.Commands, "not-present"); ok {
		t.Fatal("missing command resolved unexpectedly")
	}
	if got := EventTier(HookPreToolUse); got != "claude-compatible-subset" {
		t.Fatalf("EventTier(PreToolUse) = %q", got)
	}
	if got := EventTier(HookEvent("WorktreeCreate")); got != "reserved-placeholder" {
		t.Fatalf("EventTier(WorktreeCreate) = %q", got)
	}
	if got := EventTier(HookEvent("FuturePluginEvent")); got != "" {
		t.Fatalf("EventTier(unknown) = %q", got)
	}

	loaded, err := LoadAll([]string{fx.alpha, fx.beta, fx.gamma, fx.warn})
	if err != nil {
		t.Fatalf("LoadAll(healthy): %v", err)
	}
	loadAllNames := instanceNames(loaded)
	if want := []string{"alpha", "beta", "gamma", "warn"}; !reflect.DeepEqual(loadAllNames, want) {
		t.Fatalf("LoadAll ordering = %v, want %v", loadAllNames, want)
	}
	if dup, err := LoadAll([]string{fx.alpha, fx.duplicate}); err == nil || dup != nil || !strings.Contains(err.Error(), "duplicate plugin name") {
		t.Fatalf("LoadAll duplicate = plugins:%#v err:%v, want nil duplicate result and duplicate error", dup, err)
	}

	failSoft, skipped := LoadAllFailSoft([]string{fx.broken, fx.alpha, fx.duplicate, fx.beta})
	failSoftNames := instanceNames(failSoft)
	if want := []string{"alpha", "beta"}; !reflect.DeepEqual(failSoftNames, want) {
		t.Fatalf("LoadAllFailSoft ordering = %v, want %v", failSoftNames, want)
	}
	if len(skipped) != 2 || skipped[0].Name != "" || skipped[1].Name != "alpha" ||
		!strings.Contains(skipped[0].Reason, "broken") || !strings.Contains(skipped[1].Reason, "duplicate") {
		t.Fatalf("LoadAllFailSoft skipped = %#v", skipped)
	}

	settings, err := LoadSettings(fx.workDir, fx.settingsPlugin)
	if err != nil || settings == nil || settings.Body != fx.settingsBody || settings.Frontmatter["enabled"] != true {
		t.Fatalf("LoadSettings(valid) = %#v, %v", settings, err)
	}
	missing, err := LoadSettings(fx.workDir, "missing")
	if err != nil || missing != nil {
		t.Fatalf("LoadSettings(missing) = %#v, %v", missing, err)
	}
	if _, err := LoadSettings(fx.workDir, fx.invalidSettings); err == nil {
		t.Fatal("LoadSettings(malformed) unexpectedly succeeded")
	}
	assertPluginLoaderProgramErrorPaths(t, fx)

	return pluginLoaderProgramTrace{
		Instances: []pluginLoaderProgramInstance{
			summarizePluginLoaderProgramInstance(alpha),
			summarizePluginLoaderProgramInstance(beta),
			summarizePluginLoaderProgramInstance(gamma),
			summarizePluginLoaderProgramInstance(warn),
		},
		LoadAllNames:    loadAllNames,
		FailSoftNames:   failSoftNames,
		Skipped:         []string{"broken", "duplicate:alpha"},
		ParserHookCount: len(parsedHooks[HookPreToolUse]),
		SettingsBody:    settings.Body,
	}
}

func buildPluginLoaderProgramFixture(t *testing.T, mode byte, token string) pluginLoaderProgramFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(temp root): %v", err)
	}
	fx := pluginLoaderProgramFixture{
		root:            root,
		workDir:         filepath.Join(root, "work"),
		alpha:           filepath.Join(root, "alpha"),
		beta:            filepath.Join(root, "beta"),
		gamma:           filepath.Join(root, "gamma"),
		warn:            filepath.Join(root, "warn"),
		duplicate:       filepath.Join(root, "duplicate"),
		badAgent:        filepath.Join(root, "bad-agent"),
		badCommand:      filepath.Join(root, "bad-command"),
		broken:          filepath.Join(root, "broken"),
		betaHookPath:    filepath.Join(root, "beta", "hooks", "custom.json"),
		gammaHookPath:   filepath.Join(root, "gamma", "hooks", "hooks.json"),
		settingsBody:    "project settings " + token + "\n",
		settingsPlugin:  "alpha",
		invalidSettings: "invalid",
		token:           token,
	}

	var agentsOverride any = []string{"extra-agents"}
	if mode&1 == 0 {
		agentsOverride = "extra-agents"
	}
	var commandsOverride any = "extra-commands"
	if mode&2 != 0 {
		commandsOverride = []string{"extra-commands"}
	}

	alphaEvents := map[string]any{
		"description": "hook config " + token,
		"$schema":     "https://example.invalid/hooks.schema.json",
		"PreToolUse": []any{map[string]any{
			"matcher": "Bash|Read",
			"hooks": []any{
				map[string]any{
					"type":          "prompt",
					"prompt":        "review ${CLAUDE_PLUGIN_ROOT}/" + token,
					"args":          []string{"${PLUGIN_ROOT}/arg", "literal"},
					"shell":         "bash",
					"if":            "Bash(*)",
					"async":         true,
					"asyncRewake":   true,
					"statusMessage": "checking",
					"futureField":   token,
				},
				map[string]any{
					"type":    "command",
					"command": "never-run " + token,
				},
			},
		}},
		"SessionStart": []any{map[string]any{
			"matcher": "startup|resume",
			"hooks":   []any{map[string]any{"type": "prompt", "prompt": "start ${PLUGIN_ROOT}/" + token}},
		}},
		"WorktreeCreate":    []any{},
		"FuturePluginEvent": []any{},
	}
	var alphaHooks any = alphaEvents
	if mode&4 != 0 {
		alphaHooks = map[string]any{"description": "wrapper", "hooks": alphaEvents}
	}
	fx.alphaHooks = mustMarshalPluginLoaderProgram(t, alphaHooks)

	alphaManifest := map[string]any{
		"name":        "alpha",
		"description": "alpha " + token,
		"agents":      agentsOverride,
		"commands":    commandsOverride,
		"hooks":       alphaHooks,
		"mcpServers": map[string]any{
			"shared": map[string]any{"command": "${PLUGIN_ROOT}/inline-server", "args": []string{"--inline", token}},
			"inline": map[string]any{"command": "inline-server", "args": []string{"--token", token}},
		},
	}
	writePluginLoaderProgramManifest(t, fx.alpha, ".claude-plugin", alphaManifest)
	writePluginLoaderProgramManifest(t, fx.alpha, ".codex-plugin", map[string]any{"name": "ignored-codex"})
	writePluginLoaderProgramFile(t, fx.alpha, filepath.Join("skills", "inspect", "SKILL.md"), "---\nname: inspect\ndescription: inspect "+token+"\nallowed-tools:\n  - Read\n---\nskill body "+token+"\n")
	writePluginLoaderProgramFile(t, fx.alpha, filepath.Join("agents", "helper.md"), "---\nname: helper\ndescription: helper "+token+"\ntools:\n  - Read\n  - Bash\nskills:\n  - inspect\ntasks:\n  - title: Audit\n    prompt: audit "+token+"\n---\nhelper body\n")
	writePluginLoaderProgramFile(t, fx.alpha, filepath.Join("extra-agents", "reviewer.md"), "---\nname: reviewer\ndescription: reviewer "+token+"\ntools: all\n---\nreviewer body\n")
	writePluginLoaderProgramFile(t, fx.alpha, filepath.Join("commands", "hello.md"), "---\ndescription: hello "+token+"\nargument-hint: <arg>\nallowed-tools:\n  - Read\nmodel: opus\n---\nhello $ARGUMENTS\n")
	writePluginLoaderProgramFile(t, fx.alpha, filepath.Join("extra-commands", "review.md"), "review "+token+"\n")
	writePluginLoaderProgramFile(t, fx.alpha, ".mcp.json", `{"mcpServers":{"shared":{"command":"${CLAUDE_PLUGIN_ROOT}/file-server","args":["--file"]},"file":{"command":"file-server"}}}`)

	writePluginLoaderProgramManifest(t, fx.beta, ".codex-plugin", map[string]any{"name": "beta", "hooks": "hooks/custom.json"})
	writePluginLoaderProgramFile(t, fx.beta, filepath.Join("hooks", "custom.json"), `{"hooks":{"PostToolUse":[{"matcher":"Read","hooks":[{"type":"prompt","prompt":"from file"}]}]}}`)

	writePluginLoaderProgramManifest(t, fx.gamma, ".claude-plugin", map[string]any{"name": "gamma"})
	writePluginLoaderProgramFile(t, fx.gamma, filepath.Join("hooks", "hooks.json"), `{"Notification":[{"matcher":"*","hooks":[{"type":"prompt","prompt":"default hook"}]}]}`)

	writePluginLoaderProgramManifest(t, fx.warn, ".claude-plugin", map[string]any{"name": "warn", "mcpServers": "not-an-object"})
	writePluginLoaderProgramFile(t, fx.warn, ".mcp.json", `{"mcpServers":`)

	writePluginLoaderProgramManifest(t, fx.duplicate, ".claude-plugin", map[string]any{"name": "alpha"})
	writePluginLoaderProgramManifest(t, fx.badAgent, ".claude-plugin", map[string]any{"name": "bad-agent"})
	writePluginLoaderProgramFile(t, fx.badAgent, filepath.Join("agents", "broken.md"), "---\nname: broken\n---\nmissing description\n")
	writePluginLoaderProgramManifest(t, fx.badCommand, ".claude-plugin", map[string]any{"name": "bad-command"})
	writePluginLoaderProgramFile(t, fx.badCommand, filepath.Join("commands", "broken.md"), "---\nnot yaml: [}\n---\n")

	writePluginLoaderProgramFile(t, fx.workDir, filepath.Join(".claude", "alpha.local.md"), "---\nenabled: true\nmode: "+token+"\n---\n"+fx.settingsBody)
	writePluginLoaderProgramFile(t, fx.workDir, filepath.Join(".claude", "invalid.local.md"), "---\nkey: \"unterminated\n---\n")
	if err := os.MkdirAll(fx.broken, 0o700); err != nil {
		t.Fatalf("mkdir broken plugin: %v", err)
	}

	return fx
}

func writePluginLoaderProgramManifest(t *testing.T, root, flavor string, manifest any) {
	t.Helper()
	writePluginLoaderProgramFile(t, root, filepath.Join(flavor, "plugin.json"), string(mustMarshalPluginLoaderProgram(t, manifest)))
}

func writePluginLoaderProgramFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func mustMarshalPluginLoaderProgram(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return b
}

func assertLoaderProgramAlpha(t *testing.T, alpha Instance, fx pluginLoaderProgramFixture) {
	t.Helper()
	if alpha.ManifestFlavor != "claude" || alpha.ManifestPath != filepath.Join(alpha.Dir, ".claude-plugin", "plugin.json") {
		t.Fatalf("alpha manifest selection = flavor:%q path:%q", alpha.ManifestFlavor, alpha.ManifestPath)
	}
	for _, key := range []string{"alpha:inspect"} {
		if _, ok := alpha.Skills[key]; !ok {
			t.Fatalf("alpha missing skill %q: %v", key, sortedPluginLoaderProgramKeys(alpha.Skills))
		}
	}
	for _, key := range []string{"alpha:helper", "alpha:reviewer"} {
		if _, ok := alpha.Agents[key]; !ok {
			t.Fatalf("alpha missing agent %q: %v", key, sortedPluginLoaderProgramKeys(alpha.Agents))
		}
	}
	for _, key := range []string{"alpha:hello", "alpha:review"} {
		if _, ok := alpha.Commands[key]; !ok {
			t.Fatalf("alpha missing command %q: %v", key, sortedPluginLoaderProgramKeys(alpha.Commands))
		}
	}
	pre := alpha.Hooks[HookPreToolUse]
	if len(pre) != 2 || pre[0].SourcePath != "" || pre[0].PluginDir != alpha.Dir || pre[0].Timeout != 30 || pre[1].Timeout != 60 {
		t.Fatalf("alpha PreToolUse = %#v", pre)
	}
	if !alpha.UnsupportedHooks[HookEvent("WorktreeCreate")] || !alpha.UnknownHooks["FuturePluginEvent"] {
		t.Fatalf("alpha hook diagnostics unsupported=%#v unknown=%#v", alpha.UnsupportedHooks, alpha.UnknownHooks)
	}
	for event, hooks := range alpha.Hooks {
		for _, hook := range hooks {
			if hook.Event != event || strings.Contains(hook.Command, "${") || strings.Contains(hook.Prompt, "${") {
				t.Fatalf("alpha hook metadata/expansion error: %#v", hook)
			}
			for _, arg := range hook.Args {
				if strings.Contains(arg, "${") {
					t.Fatalf("alpha hook arg did not expand: %#v", hook)
				}
			}
		}
	}
	configs := map[string]string{}
	for _, config := range alpha.MCPConfigs {
		configs[config.Name] = config.Command + "|" + strings.Join(config.Args, ",")
	}
	if got := configs["plugin_alpha_shared"]; got != filepath.Join(alpha.Dir, "inline-server")+"|--inline,"+fx.token {
		// The exact assertion below is intentionally decomposed after the informative
		// summary so a fuzz failure names the layer that drifted.
		t.Fatalf("inline MCP did not shadow file config: %#v", configs)
	}
	if _, ok := configs["plugin_alpha_file"]; !ok {
		t.Fatalf("file MCP config missing: %#v", configs)
	}
	if _, ok := configs["plugin_alpha_inline"]; !ok {
		t.Fatalf("inline MCP config missing: %#v", configs)
	}
}

func assertLoaderProgramBeta(t *testing.T, beta Instance, fx pluginLoaderProgramFixture) {
	t.Helper()
	if beta.ManifestFlavor != "codex" || beta.ManifestPath != filepath.Join(beta.Dir, ".codex-plugin", "plugin.json") {
		t.Fatalf("beta manifest selection = flavor:%q path:%q", beta.ManifestFlavor, beta.ManifestPath)
	}
	hooks := beta.Hooks[HookPostToolUse]
	if len(hooks) != 1 || hooks[0].SourcePath != fx.betaHookPath || hooks[0].PluginDir != beta.Dir {
		t.Fatalf("beta file hooks = %#v, want source %q", hooks, fx.betaHookPath)
	}
}

func assertLoaderProgramGamma(t *testing.T, gamma Instance, fx pluginLoaderProgramFixture) {
	t.Helper()
	hooks := gamma.Hooks[HookNotification]
	if gamma.ManifestFlavor != "claude" || len(hooks) != 1 || hooks[0].SourcePath != fx.gammaHookPath {
		t.Fatalf("gamma default hooks = flavor:%q hooks:%#v want source %q", gamma.ManifestFlavor, hooks, fx.gammaHookPath)
	}
}

func assertLoaderProgramFailure(t *testing.T, dir, label string) {
	t.Helper()
	inst, err := Load(dir)
	if err == nil || !pluginLoaderProgramInstanceIsZero(inst) {
		t.Fatalf("Load(%s) = %#v, %v; want error and zero Instance", label, inst, err)
	}
}

func assertPluginLoaderProgramErrorPaths(t *testing.T, fx pluginLoaderProgramFixture) {
	t.Helper()

	// Parser errors stay no-partial even when they arise below Load's manifest
	// layer. These cover the rejected agent schema shapes plugin authors commonly
	// produce while keeping all file effects inside the fixture root.
	for _, data := range []string{
		"---\nname: \ndescription: d\n---\n",
		"---\nname: a\ndescription: d\ntools: \"*\"\n---\n",
		"---\nname: a\ndescription: d\ntools: unknown\n---\n",
		"---\nname: a\ndescription: d\ntools:\n  - Read\n  - 7\n---\n",
		"---\nname: a\ndescription: d\ntools: 7\n---\n",
		"---\nname: a\ndescription: d\nskills: one\n---\n",
		"---\nname: a\ndescription: d\nskills:\n  - 7\n---\n",
		"---\nname: a\ndescription: d\ntasks: one\n---\n",
		"---\nname: a\ndescription: d\ntasks:\n  - one\n---\n",
	} {
		if agent, err := ParseAgent([]byte(data), "p"); err == nil || !reflect.DeepEqual(agent, Agent{}) {
			t.Fatalf("invalid agent parse = %#v, %v for %q", agent, err, data)
		}
	}
	detailed, err := ParseAgent([]byte("---\nname: detailed\ndescription: d\ntasks:\n  - title: t\n    prompt: p\n    reasoning_effort: high\n    type: research\n    insert: append\n---\nbody\n"), "p")
	if err != nil || len(detailed.Tasks) != 1 || detailed.Tasks[0].ReasoningEffort != "high" || detailed.Tasks[0].Insert != "append" {
		t.Fatalf("detailed agent parse = %#v, %v", detailed, err)
	}

	for _, check := range []struct {
		name string
		fn   func() error
	}{
		{"agents malformed override", func() error { _, err := discoverPluginAgents(fx.alpha, json.RawMessage(`{`), "alpha"); return err }},
		{"commands malformed override", func() error { _, err := discoverPluginCommands(fx.alpha, json.RawMessage(`{`), "alpha"); return err }},
		{"agents file as directory", func() error {
			_, err := discoverPluginAgents(fx.alpha, json.RawMessage(`"agents/helper.md"`), "alpha")
			return err
		}},
		{"commands file as directory", func() error {
			_, err := discoverPluginCommands(fx.alpha, json.RawMessage(`"commands/hello.md"`), "alpha")
			return err
		}},
		{"missing specified hooks file", func() error {
			_, _, _, err := discoverPluginHooksDiag(fx.beta, json.RawMessage(`"hooks/missing.json"`), "beta")
			return err
		}},
	} {
		if err := check.fn(); err == nil {
			t.Fatalf("%s unexpectedly succeeded", check.name)
		}
	}
	if agents, err := discoverPluginAgents(fx.alpha, json.RawMessage(`"missing-agents"`), "alpha"); err != nil || len(agents) != 1 {
		t.Fatalf("agents missing override = %#v, %v", agents, err)
	}
	if commands, err := discoverPluginCommands(fx.alpha, json.RawMessage(`"missing-commands"`), "alpha"); err != nil || len(commands) != 1 {
		t.Fatalf("commands missing override = %#v, %v", commands, err)
	}

	for _, data := range [][]byte{
		[]byte(`{"PreToolUse":{}}`),
		[]byte(`{"PreToolUse":[{"hooks":[1]}]}`),
	} {
		hooks, unsupported, unknown, err := parsePluginHooksDiag(data, fx.alpha, "alpha")
		if err == nil || hooks != nil || unsupported != nil || unknown != nil {
			t.Fatalf("malformed hooks result = hooks:%#v unsupported:%#v unknown:%#v err:%v", hooks, unsupported, unknown, err)
		}
	}
	if unknown := captureUnknownFields(json.RawMessage(`{`)); unknown != nil {
		t.Fatalf("malformed raw handler retained unknown fields: %#v", unknown)
	}
	hookPathAsDir := filepath.Join(fx.root, "hook-path-as-dir", "hooks", "hooks.json")
	if err := os.MkdirAll(hookPathAsDir, 0o700); err != nil {
		t.Fatalf("mkdir hook path-as-dir: %v", err)
	}
	if _, _, _, err := discoverPluginHooksDiag(filepath.Join(fx.root, "hook-path-as-dir"), nil, "bad-hooks"); err == nil {
		t.Fatal("default hooks directory path unexpectedly loaded")
	}

	configs, warnings, err := discoverPluginMCPConfigs(fx.alpha, json.RawMessage(`{"":{"command":"invalid"}}`), "alpha")
	if err != nil || len(configs) != 2 || len(warnings) != 1 || !strings.Contains(warnings[0], "alpha") {
		t.Fatalf("invalid inline MCP layer = configs:%#v warnings:%#v err:%v", configs, warnings, err)
	}

	assertLoaderProgramFailure(t, filepath.Join(fx.root, "does-not-exist"), "missing root")
	notDir := filepath.Join(fx.root, "not-a-plugin-dir")
	if err := os.WriteFile(notDir, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write not-a-plugin-dir: %v", err)
	}
	assertLoaderProgramFailure(t, notDir, "file root")
	if loaded, err := LoadAll([]string{fx.broken}); err == nil || loaded != nil {
		t.Fatalf("LoadAll(broken) = %#v, %v", loaded, err)
	}

	settingsDir := filepath.Join(fx.workDir, ".claude", "directory.local.md")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatalf("mkdir settings path: %v", err)
	}
	if _, err := LoadSettings(fx.workDir, "directory"); err == nil {
		t.Fatal("LoadSettings(directory path) unexpectedly succeeded")
	}
}

func pluginLoaderProgramInstanceIsZero(inst Instance) bool {
	return inst.Manifest.Name == "" && inst.Dir == "" && inst.ManifestFlavor == "" && inst.ManifestPath == "" &&
		inst.Skills == nil && inst.Agents == nil && inst.Commands == nil && inst.Hooks == nil && inst.MCPConfigs == nil &&
		inst.MCPConfigWarnings == nil && inst.UnsupportedHooks == nil && inst.UnknownHooks == nil
}

func instanceNames(instances []Instance) []string {
	names := make([]string, len(instances))
	for i, instance := range instances {
		names[i] = instance.Manifest.Name
	}
	return names
}

func summarizePluginLoaderProgramInstance(inst Instance) pluginLoaderProgramInstance {
	summary := pluginLoaderProgramInstance{
		Name:        inst.Manifest.Name,
		Flavor:      inst.ManifestFlavor,
		Skills:      sortedPluginLoaderProgramKeys(inst.Skills),
		Agents:      sortedPluginLoaderProgramKeys(inst.Agents),
		Commands:    sortedPluginLoaderProgramKeys(inst.Commands),
		Unsupported: sortedPluginLoaderProgramHookEvents(inst.UnsupportedHooks),
		Unknown:     sortedPluginLoaderProgramKeys(inst.UnknownHooks),
		Warnings:    len(inst.MCPConfigWarnings),
	}
	for event, hooks := range inst.Hooks {
		summary.Hooks = append(summary.Hooks, string(event)+"="+strconv.Itoa(len(hooks)))
	}
	sort.Strings(summary.Hooks)
	for _, config := range inst.MCPConfigs {
		command := strings.ReplaceAll(config.Command, inst.Dir, "$ROOT")
		args := make([]string, len(config.Args))
		for i, arg := range config.Args {
			args[i] = strings.ReplaceAll(arg, inst.Dir, "$ROOT")
		}
		summary.MCP = append(summary.MCP, config.Name+"|"+config.Type+"|"+command+"|"+strings.Join(args, ","))
	}
	sort.Strings(summary.MCP)
	return summary
}

func sortedPluginLoaderProgramHookEvents(values map[HookEvent]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, string(key))
	}
	sort.Strings(keys)
	return keys
}

func sortedPluginLoaderProgramKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
