//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"primeradiant.com/serf/agent/execenv"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/internal/bundled"
	"primeradiant.com/serf/llm"
)

// FuzzWorkspacePromptProgram covers the offline context assembled before a
// session calls a model: workspace inventory, project instructions, prompt
// sections, runtime paths, and transcript outlines. All command responses are
// supplied by wppEnv, so the target never invokes git, a shell, a provider, or
// the network. Its filesystem fixtures are created under testing.T.TempDir.
//
// Oracles:
//   - repeated scans, prompt renders, and outline projections agree exactly;
//   - workspace and project-instruction bounds are truthful;
//   - scripted git metadata is counted and routed without executing git; and
//   - lifecycle tool results remain paired to the right outline call, including
//     the audit-pivot child reference.
func FuzzWorkspacePromptProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		{1, 2, 3, 4},
		[]byte("workspace prompt fixture"),
		[]byte{0xff, 0x00, 0x7f, 0x41},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, program []byte) {
		token := shortHash(program)
		wppWorkspace(t, token)
		wppProjectDocs(t, token)
		wppGitSnapshot(t, token)
		wppRuntimePaths(t, token)
		wppPromptDataAndRender(t, token)
		wppSectionResolver(t, token)
		wppOutline(t, token)
	})
}

func wppWorkspace(t *testing.T, token string) {
	t.Helper()
	root := t.TempDir()
	wppWrite(t, root, "Makefile", "# comment\nVAR = value\nall clean: deps\n\t@echo ok\n%.o: %.c\n.PHONY: all\n")
	wppWrite(t, root, "package.json", `{"scripts":{"z":"z","build":"go build","lint":"go vet"}}`)
	wppWrite(t, root, "go.mod", "module example.invalid/"+token+"\n")
	wppWrite(t, root, "CMakeLists.txt", "project(fixture)\n")
	wppWrite(t, root, "Cargo.toml", "[package]\nname = \"fixture\"\n")
	wppWrite(t, root, "pyproject.toml", "[project]\nname = \"fixture\"\n")
	wppWrite(t, root, "pytest.ini", "[pytest]\n")
	wppWrite(t, root, "Dockerfile", "FROM scratch\n")
	wppWrite(t, root, "docker-compose.yaml", "services: {}\n")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	wppWrite(t, root, filepath.Join("src", "visible.go"), "package fixture\n// "+token+"\n")
	wppWrite(t, root, filepath.Join("src", "deep", "deeper", "hidden.go"), "package hidden\n")
	if err := os.MkdirAll(filepath.Join(root, "src", "deep", "deeper", "level4"), 0o755); err != nil {
		t.Fatalf("mkdir deep level: %v", err)
	}
	wppWrite(t, root, filepath.Join("node_modules", "pkg", "index.js"), "ignored\n")
	wppWrite(t, root, filepath.Join(".hidden", "secret"), "ignored\n")

	first := ScanWorkspace(root)
	second := ScanWorkspace(root)
	if first != second {
		t.Fatalf("workspace scan changed on replay:\nfirst=%#v\nsecond=%#v", first, second)
	}
	for _, want := range []string{
		"src/", "visible.go", "Makefile targets: all, clean", "package.json scripts: build, lint, z",
		"CMake project", "Go module (go.mod): example.invalid/" + token, "Rust project",
		"Python project", "pytest configured", "Dockerfile present", "docker-compose.yaml present",
	} {
		if !strings.Contains(first.Tree+"\n"+first.BuildInfo, want) {
			t.Fatalf("workspace context missing %q:\n%+v", want, first)
		}
	}
	for _, forbidden := range []string{"node_modules", ".hidden", "hidden.go"} {
		if strings.Contains(first.Tree, forbidden) {
			t.Fatalf("workspace tree included excluded path %q:\n%s", forbidden, first.Tree)
		}
	}

	targets := parseMakefileTargets(filepath.Join(root, "Makefile"))
	if got, want := strings.Join(targets, ","), "all,clean"; got != want {
		t.Fatalf("make targets = %q, want %q", got, want)
	}
	if got := parseMakefileTargets(filepath.Join(root, "missing")); got != nil {
		t.Fatalf("missing makefile targets = %#v, want nil", got)
	}
	scripts := parsePackageJsonScripts(filepath.Join(root, "package.json"))
	if !sort.StringsAreSorted(scripts) || strings.Join(scripts, ",") != "build,lint,z" {
		t.Fatalf("package scripts = %#v", scripts)
	}
	wppWrite(t, root, "bad-package.json", "not json")
	if got := parsePackageJsonScripts(filepath.Join(root, "bad-package.json")); got != nil {
		t.Fatalf("invalid package scripts = %#v, want nil", got)
	}
	if got := parsePackageJsonScripts(filepath.Join(root, "missing-package.json")); got != nil {
		t.Fatalf("missing package scripts = %#v, want nil", got)
	}

	entries, truncated := walkTree(root, 3)
	if !truncated || len(entries) != 3 {
		t.Fatalf("limited walk = entries:%d truncated:%v, want 3/true", len(entries), truncated)
	}
	if tree := formatTree(root, entries, true, 3); !strings.Contains(tree, "truncated, >3 entries") {
		t.Fatalf("truncated tree marker missing: %q", tree)
	}
	if entries, truncated := walkTree(filepath.Join(root, "missing"), 3); entries != nil || truncated {
		t.Fatalf("missing walk = %#v truncated:%v", entries, truncated)
	}
	nonDir := filepath.Join(root, "package.json")
	if entries, truncated := walkTree(nonDir, 3); entries != nil || truncated {
		t.Fatalf("file walk = %#v truncated:%v", entries, truncated)
	}
	if tree := formatTree(root, nil, false, 1); tree != "" {
		t.Fatalf("empty formatted tree = %q", tree)
	}

	fallback := t.TempDir()
	wppWrite(t, fallback, "makefile", "fallback:\n")
	wppWrite(t, fallback, "go.mod", "// no module line\n")
	wppWrite(t, fallback, "setup.py", "setup()\n")
	wppWrite(t, fallback, "docker-compose.yml", "services: {}\n")
	build := detectBuildSystem(fallback)
	for _, want := range []string{"Makefile targets: fallback", "Python project", "docker-compose.yml present"} {
		if !strings.Contains(build, want) {
			t.Fatalf("fallback build info missing %q: %q", want, build)
		}
	}
}

func wppProjectDocs(t *testing.T, token string) {
	t.Helper()
	if docs, truncated := LoadProjectDocs(nil, "AGENTS.md"); docs != nil || truncated {
		t.Fatalf("nil environment docs = %#v truncated:%v", docs, truncated)
	}
	if docs, truncated := LoadProjectDocs(&wppEnv{}, "AGENTS.md"); docs != nil || truncated {
		t.Fatalf("empty environment docs = %#v truncated:%v", docs, truncated)
	}

	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	wppWrite(t, root, "AGENTS.md", "root "+token+"\n")
	wppWrite(t, nested, "AGENTS.md", "nested "+token+"\n")
	env := &wppEnv{workDir: nested, replies: map[string]wppReply{
		"git rev-parse --show-toplevel": {result: execenv.ExecResult{Stdout: root + "\n"}},
	}}
	docs, truncated := LoadProjectDocs(env, "AGENTS.md", "", "  ", "missing.md")
	if truncated || len(docs) != 2 {
		t.Fatalf("project docs = %#v truncated:%v", docs, truncated)
	}
	if docs[0].Path != "AGENTS.md" || docs[1].Path != filepath.Join("nested", "AGENTS.md") {
		t.Fatalf("project document paths = %#v", docs)
	}
	if !strings.Contains(docs[0].Content, token) || !strings.Contains(docs[1].Content, token) {
		t.Fatalf("project docs lost fixture content: %#v", docs)
	}
	if got := len(env.calls); got != 1 || env.calls[0] != "git rev-parse --show-toplevel" {
		t.Fatalf("project doc git probe calls = %#v", env.calls)
	}

	bigRoot := t.TempDir()
	wppWrite(t, bigRoot, "BIG.md", strings.Repeat("x", projectDocByteBudget+64))
	bigEnv := &wppEnv{workDir: bigRoot, replies: map[string]wppReply{
		"git rev-parse --show-toplevel": {result: execenv.ExecResult{Stdout: bigRoot}},
	}}
	bigDocs, bigTruncated := LoadProjectDocs(bigEnv, "BIG.md")
	if !bigTruncated || len(bigDocs) != 1 || !strings.HasSuffix(bigDocs[0].Content, projectDocTruncMark+"\n") {
		t.Fatalf("bounded project docs = %#v truncated:%v", bigDocs, bigTruncated)
	}
	if len(bigDocs[0].Content) <= projectDocByteBudget {
		t.Fatalf("truncation marker was not retained: len=%d", len(bigDocs[0].Content))
	}
	zeroRoot := t.TempDir()
	wppWrite(t, zeroRoot, "A.md", strings.Repeat("a", projectDocByteBudget))
	wppWrite(t, zeroRoot, "B.md", "b")
	zeroEnv := &wppEnv{workDir: zeroRoot, replies: map[string]wppReply{
		"git rev-parse --show-toplevel": {result: execenv.ExecResult{Stdout: zeroRoot}},
	}}
	zeroDocs, zeroTruncated := LoadProjectDocs(zeroEnv, "A.md", "B.md")
	if !zeroTruncated || len(zeroDocs) != 2 || !strings.HasPrefix(zeroDocs[1].Content, "\n"+projectDocTruncMark) {
		t.Fatalf("zero-remaining project docs = %#v truncated:%v", zeroDocs, zeroTruncated)
	}
}

func wppGitSnapshot(t *testing.T, token string) {
	t.Helper()
	root := t.TempDir()
	nested := filepath.Join(root, "sub", "dir")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir git marker: %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	env := &wppEnv{workDir: nested, replies: map[string]wppReply{
		"git remote get-url origin":             {result: execenv.ExecResult{Stdout: " https://example.invalid/" + token + ".git \n"}},
		"git rev-parse --is-inside-work-tree":   {result: execenv.ExecResult{Stdout: "true\n"}},
		"git rev-parse --abbrev-ref HEAD":       {result: execenv.ExecResult{Stdout: " feature/" + token + " \n"}},
		"git status --porcelain":                {result: execenv.ExecResult{Stdout: " M tracked\r\n?? one\nA  staged\n?? two\n\n"}},
		"git log -n 5 --pretty=format:%h%x20%s": {result: execenv.ExecResult{Stdout: "abc first\r\ndef second\n"}},
	}}
	if gitExecTimeoutMS() <= 0 {
		t.Fatal("git execution timeout must be positive")
	}
	if origin := gitOriginURL(env, ""); origin != "https://example.invalid/"+token+".git" {
		t.Fatalf("origin = %q", origin)
	}
	inRepo, branch, modified, untracked, commits := snapshotGit(env, nested)
	if !inRepo || branch != "feature/"+token || modified != 2 || untracked != 2 || strings.Join(commits, ",") != "abc first,def second" {
		t.Fatalf("snapshot = repo:%v branch:%q modified:%d untracked:%d commits:%#v", inRepo, branch, modified, untracked, commits)
	}
	if got := snapshotGitTrace(env.calls); got != "git remote get-url origin|git rev-parse --is-inside-work-tree|git rev-parse --abbrev-ref HEAD|git status --porcelain|git log -n 5 --pretty=format:%h%x20%s" {
		t.Fatalf("scripted git call order = %q", got)
	}

	if gitOriginURL(nil, nested) != "" {
		t.Fatal("nil origin environment returned a value")
	}
	if snapshot, branch, modified, untracked, commits := snapshotGit(nil, nested); snapshot || branch != "" || modified != 0 || untracked != 0 || commits != nil {
		t.Fatalf("nil snapshot = %v %q %d %d %#v", snapshot, branch, modified, untracked, commits)
	}
	noRepo := t.TempDir()
	noRepoEnv := &wppEnv{workDir: noRepo}
	if inRepo, _, _, _, _ := snapshotGit(noRepoEnv, noRepo); inRepo || len(noRepoEnv.calls) != 0 {
		t.Fatalf("non-repo snapshot invoked scripted command: repo=%v calls=%#v", inRepo, noRepoEnv.calls)
	}
	if !hasGitMetadataAncestor("") || !hasGitMetadataAncestor("relative") || !hasGitMetadataAncestor(nested) || hasGitMetadataAncestor(noRepo) {
		t.Fatal("git metadata structural probe returned an inconsistent result")
	}

	insideFalse := &wppEnv{workDir: nested, replies: map[string]wppReply{
		"git rev-parse --is-inside-work-tree": {result: execenv.ExecResult{Stdout: "false\n"}},
	}}
	if inRepo, _, _, _, _ := snapshotGit(insideFalse, nested); inRepo || len(insideFalse.calls) != 1 {
		t.Fatalf("false inside-work-tree snapshot = repo:%v calls:%#v", inRepo, insideFalse.calls)
	}
	partial := &wppEnv{workDir: nested, replies: map[string]wppReply{
		"git remote get-url origin":             {err: errors.New("scripted origin failure")},
		"git rev-parse --is-inside-work-tree":   {result: execenv.ExecResult{Stdout: "true"}},
		"git rev-parse --abbrev-ref HEAD":       {result: execenv.ExecResult{ExitCode: 1}},
		"git status --porcelain":                {err: errors.New("scripted status failure")},
		"git log -n 5 --pretty=format:%h%x20%s": {result: execenv.ExecResult{ExitCode: 1}},
	}}
	if origin := gitOriginURL(partial, nested); origin != "" {
		t.Fatalf("failed origin = %q", origin)
	}
	if inRepo, branch, modified, untracked, commits := snapshotGit(partial, nested); !inRepo || branch != "" || modified != 0 || untracked != 0 || len(commits) != 0 {
		t.Fatalf("partial snapshot = repo:%v branch:%q modified:%d untracked:%d commits:%#v", inRepo, branch, modified, untracked, commits)
	}
	blankCWD := &wppEnv{workDir: nested, replies: map[string]wppReply{
		"git rev-parse --is-inside-work-tree": {result: execenv.ExecResult{Stdout: "false"}},
	}}
	if inRepo, _, _, _, _ := snapshotGit(blankCWD, ""); inRepo || len(blankCWD.calls) != 1 {
		t.Fatalf("blank cwd snapshot = repo:%v calls:%#v", inRepo, blankCWD.calls)
	}
}

func wppRuntimePaths(t *testing.T, token string) {
	t.Helper()
	root := t.TempDir()
	stateHome := filepath.Join(root, "state")
	cacheHome := filepath.Join(root, "cache")
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	project, got, err := RuntimeDir(root, "")
	if err != nil {
		t.Fatalf("runtime dir: %v", err)
	}
	if want := filepath.Join(stateHome, "serf", "projects", project.ID); got != want {
		t.Fatalf("runtime dir = %q, want %q", got, want)
	}
	explicitProject, got, err := RuntimeDirWithStateHome(root, "", filepath.Join(root, "explicit"))
	if err != nil {
		t.Fatalf("explicit state runtime dir: %v", err)
	}
	if want := filepath.Join(root, "explicit", "serf", "projects", explicitProject.ID); got != want {
		t.Fatalf("explicit state runtime dir = %q, want %q", got, want)
	}
	if overrideProject, got, err := RuntimeDirWithStateHome(filepath.Join(root, "missing"), filepath.Join(root, "override"), stateHome); err != nil || overrideProject.ID != "" || got != filepath.Join(root, "override") {
		t.Fatalf("runtime override = %q", got)
	}
	if got, want := CacheDir(), filepath.Join(cacheHome, "serf"); got != want {
		t.Fatalf("cache dir = %q, want %q", got, want)
	}
	if got := shortHash([]byte(token)); got != nonProjectHash(token) || len(got) != 16 {
		t.Fatalf("short hash = %q", got)
	}

	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", root)
	if got, want := xdgStateHome(), filepath.Join(root, ".local", "state"); got != want {
		t.Fatalf("default state home = %q, want %q", got, want)
	}
	if got, want := xdgCacheHome(), filepath.Join(root, ".cache"); got != want {
		t.Fatalf("default cache home = %q, want %q", got, want)
	}
	t.Setenv("HOME", "")
	if got, want := xdgStateHome(), filepath.Join(os.TempDir(), ".local", "state"); got != want {
		t.Fatalf("fallback state home = %q, want %q", got, want)
	}
	if got, want := xdgCacheHome(), filepath.Join(os.TempDir(), ".cache"); got != want {
		t.Fatalf("fallback cache home = %q, want %q", got, want)
	}
}

func wppPromptDataAndRender(t *testing.T, token string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	appendPath := filepath.Join(root, "append.md")
	wppWrite(t, root, "append.md", "append "+token+"\n")
	env := &wppEnv{workDir: root}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		MaxSubagentDepth:   1,
		NoProjectPrompts:   true,
		StateDir:           root,
		SystemPromptAppend: []string{appendPath, filepath.Join(root, "missing.md")},
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
		},
	})
	if err != nil {
		t.Fatalf("new prompt session: %v", err)
	}
	defer sess.Close()

	if got := sess.cachedSystemPrompt; got != "test system prompt" {
		t.Fatalf("minimal cached prompt = %q", got)
	}
	sess.RegisterTool("wpp_custom", "custom fixture", map[string]any{"type": "object"}, func(context.Context, any) (any, error) {
		return "unused", nil
	})
	sess.mcpTools = []llm.ToolDefinition{{Name: "wpp_mcp", Description: "mcp fixture"}}
	sess.systemPromptOverride = " override " + token + " "
	sess.cfg.SystemPromptFile = "override.md"
	sess.delegationAllowance = 1
	data := sess.buildPromptData(env)
	if len(data.CLIAppends) != 1 || !strings.Contains(data.CLIAppends[0], token) {
		t.Fatalf("CLI appends = %#v", data.CLIAppends)
	}
	if !wppHasTool(data.MCPTools, "wpp_mcp") || !wppHasTool(data.CustomTools, "wpp_custom") {
		t.Fatalf("prompt tool tiers = mcp:%#v custom:%#v", data.MCPTools, data.CustomTools)
	}
	if !data.CanDelegate || !sess.canPromptDelegation() {
		t.Fatal("root prompt should expose the registered delegation surface")
	}
	if (&Session{delegationAllowance: 0, reg: sess.reg}).canPromptDelegation() || (&Session{delegationAllowance: 1}).canPromptDelegation() {
		t.Fatal("delegation guard accepted a missing allowance or registry")
	}
	if (&Session{delegationAllowance: 1, reg: tooldefs.NewRegistry()}).canPromptDelegation() {
		t.Fatal("delegation guard accepted a registry without the full prompt surface")
	}

	sess.cfg.testOnly.minimalSystemPrompt = false
	sess.refreshSystemPromptCache(env)
	prompt, warning := sess.renderSystemPrompt(env)
	if warning != "" {
		t.Fatalf("unexpected render warning: %q", warning)
	}
	again, _ := sess.renderSystemPrompt(env)
	if prompt != again || prompt != sess.cachedSystemPrompt {
		t.Fatal("system prompt cache/render was not deterministic")
	}
	for _, want := range []string{token, "append " + token} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("system prompt missing %q", want)
		}
	}
	if !wppHasSource(sess.promptSourceLog, "cli:override.md") || !wppHasSourcePrefix(sess.promptSourceLog, "append:") {
		t.Fatalf("prompt source log = %#v", sess.promptSourceLog)
	}
	user := llm.User("user text")
	if got := prependSystemPromptToUserMessage("system", user); len(got.Content) != len(user.Content)+1 || got.Content[0].Text != "system\n\n" {
		t.Fatalf("system prepend = %#v", got)
	}
	if got := prependSystemPromptToUserMessage("  ", user); len(got.Content) != len(user.Content) {
		t.Fatalf("blank system prepend changed user: %#v", got)
	}
	if promptSectionDirExists("") || promptSectionDirExists(appendPath) || promptSectionDirExists(filepath.Join(root, "missing")) || !promptSectionDirExists(root) {
		t.Fatal("prompt section directory guard returned an inconsistent result")
	}
	if sandboxPromptLine(env) != "" || sandboxPromptLine(nil) != "" {
		t.Fatal("non-local environment unexpectedly emitted sandbox prompt text")
	}
	local := execenv.NewLocalExecutionEnvironment(root)
	if sandboxPromptLine(local) != "" {
		t.Fatal("unsandboxed local environment emitted sandbox prompt text")
	}
	local.Sandbox = &sandbox.ResolvedPolicy{Mode: sandbox.ModeRestricted, Network: false}
	if got := sandboxPromptLine(local); got != "restricted (network off) — fixed for this session" {
		t.Fatalf("sandbox prompt = %q", got)
	}
	local.Sandbox.Network = true
	if got := sandboxPromptLine(local); got != "restricted (network on) — fixed for this session" {
		t.Fatalf("sandbox prompt network-on = %q", got)
	}

	defs := []llm.ToolDefinition{{Name: "a", Description: "  alpha  "}, {Name: "", Description: ""}, {Name: "a"}, {Name: "b"}}
	entries := toolEntriesFromDefinitions(defs)
	if entries[0].Description != "alpha" || entries[1].Description != "(no description)" {
		t.Fatalf("tool entries = %#v", entries)
	}
	if got := strings.Join(toolNamesFromDefinitions(defs), ","); got != "a,b" {
		t.Fatalf("tool names = %q", got)
	}
	if names := toolNameSetFromDefinitions(defs); len(names) != 2 || !names["a"] || !names["b"] {
		t.Fatalf("tool name set = %#v", names)
	}
	if got := strings.Join(unavailableToolNames(defs, []llm.ToolDefinition{{Name: "a"}}), ","); got != "b" {
		t.Fatalf("unavailable names = %q", got)
	}
	if got := (skillEntry{Name: "name", CatalogName: " catalog "}).CatalogNameOrName(); got != " catalog " {
		t.Fatalf("catalog name = %q", got)
	}
	if got := (skillEntry{Name: "name"}).CatalogNameOrName(); got != "name" {
		t.Fatalf("fallback skill name = %q", got)
	}
	for input, want := range map[string]string{
		"":                       "(no description)",
		"plain words":            "plain words",
		"  first sentence. rest": "first sentence.",
		strings.Repeat("x", 121): strings.Repeat("x", 117) + "...",
	} {
		if got := summarizeTaskPrompt(input); got != want {
			t.Fatalf("task summary(%q) = %q, want %q", input, got, want)
		}
	}
	tasks := agentTaskEntries([]task.TaskTemplate{{Title: "a", Prompt: "do it! later", Insert: "parent_tasks"}, {Title: "b"}})
	if len(tasks) != 2 || !tasks[0].ReplacedByParentTasks || tasks[0].Description != "do it!" || tasks[1].Description != "(no description)" {
		t.Fatalf("task entries = %#v", tasks)
	}
	if got := formatToolNamesForPrompt(nil); got != "none" {
		t.Fatalf("empty tool prompt = %q", got)
	}
	if got := formatToolNamesForPrompt([]string{"a", "b"}); got != "`a`, `b`" {
		t.Fatalf("tool prompt names = %q", got)
	}
}

func wppSectionResolver(t *testing.T, token string) {
	t.Helper()
	dir := t.TempDir()
	wppWrite(t, dir, "tools.md", "base\n")
	wppWrite(t, dir, "tools.provider-openai_prepend.md", "provider before\n")
	wppWrite(t, dir, "tools.provider-openai_append.md", "provider after\n")
	wppWrite(t, dir, "tools.agent-reviewer_prepend.md", "agent before\n")
	wppWrite(t, dir, "tools.agent-reviewer.md", "agent body "+token+"\n")
	wppWrite(t, dir, "tools.agent-reviewer_append.md", "agent after\n")
	wppWrite(t, dir, "identity.md.tmpl", "identity {{ .Provider }}\n")
	wppWrite(t, dir, "bad.md.tmpl", "{{")
	wppWrite(t, dir, "role.agent-reviewer.md", "disk role\n")
	wppWrite(t, dir, "page.md.tmpl", "{{ section \"identity\" }}\n\n\n{{ section \"tools\" }}\n")

	r := &sectionResolver{provider: "openai", agent: "reviewer", sources: []sectionSource{diskSource{dir: dir}}}
	if got := r.Section("tools", promptData{}); got != "agent before\n\nagent body "+token+"\n\nagent after" {
		t.Fatalf("agent section = %q", got)
	}
	if got := r.Section("identity", promptData{Provider: "openai"}); got != "identity openai" {
		t.Fatalf("templated section = %q", got)
	}
	if got := r.Section("bad", promptData{}); got != "" || !wppHasSourcePrefix(r.Sources(), "ERROR:") {
		t.Fatalf("bad section = %q sources:%#v", got, r.Sources())
	}
	if got := r.Section("role", promptData{}); got != "disk role" {
		t.Fatalf("disk role = %q", got)
	}
	if out, sources, err := r.Render(dir, "page", promptData{Provider: "openai"}); err != nil || !strings.Contains(out, "identity openai") || !strings.Contains(out, token) || len(sources) == 0 {
		t.Fatalf("disk render = %q sources:%#v err:%v", out, sources, err)
	}
	if _, _, err := r.Render(dir, "missing", promptData{}); err == nil {
		t.Fatal("missing disk template unexpectedly rendered")
	}
	if _, _, err := r.renderFromContent("bad", []byte("{{ .MissingField }}"), promptData{}); err == nil {
		t.Fatal("missing template field unexpectedly executed")
	}
	if _, err := r.renderTemplate("bad", "{{", promptData{}); err == nil {
		t.Fatal("bad section template unexpectedly parsed")
	}
	if _, err := r.renderTemplate("missing", "{{ .MissingField }}", promptData{}); err == nil {
		t.Fatal("missing section template field unexpectedly executed")
	}
	if _, _, err := r.renderFromContent("parse", []byte("{{"), promptData{}); err == nil {
		t.Fatal("bad top-level template unexpectedly parsed")
	}
	if got := collapseBlankLines("a\n\n\n\n\nb"); got != "a\n\nb" {
		t.Fatalf("collapsed blank lines = %q", got)
	}

	providerOnly := &sectionResolver{provider: "openai", sources: []sectionSource{diskSource{dir: dir}}}
	if got := providerOnly.Section("tools", promptData{}); got != "provider before\n\nbase\n\nprovider after" {
		t.Fatalf("provider layering = %q", got)
	}
	if got := (&sectionResolver{}).Section("role", promptData{}); got != "" {
		t.Fatalf("empty role = %q", got)
	}
	override := &sectionResolver{agent: "reviewer"}
	if got := override.Section("role", promptData{RolePromptOverride: " override role "}); got != "override role" || !wppHasSource(override.Sources(), "config:role_prompt_override") {
		t.Fatalf("role override = %q sources:%#v", got, override.Sources())
	}
	fromAgentFS := &sectionResolver{
		agent:   "worker",
		agentFS: fstest.MapFS{"worker.md": &fstest.MapFile{Data: []byte("---\nname: worker\n---\nworker role\n")}},
	}
	if got := fromAgentFS.Section("role", promptData{}); got != "worker role" || !wppHasSource(fromAgentFS.Sources(), "agent:worker") {
		t.Fatalf("agent filesystem role = %q sources:%#v", got, fromAgentFS.Sources())
	}
	badAgentFS := &sectionResolver{
		agent:   "bad",
		agentFS: fstest.MapFS{"bad.md": &fstest.MapFile{Data: []byte("---\n: bad: yaml: [unclosed\n---\nbody\n")}},
	}
	if got := badAgentFS.Section("role", promptData{}); got != "" {
		t.Fatalf("invalid frontmatter role = %q", got)
	}
	if got := (&sectionResolver{agent: "missing", agentFS: fstest.MapFS{}}).Section("role", promptData{}); got != "" {
		t.Fatalf("missing agent filesystem role = %q", got)
	}

	embedded := embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}
	if data, ok := embedded.ReadFile("identity.md"); !ok || len(data) == 0 {
		t.Fatal("embedded source did not read identity")
	}
	if data, ok := embedded.ReadFile("missing.md"); ok || data != nil {
		t.Fatalf("missing embedded source = %#v ok:%v", data, ok)
	}
	if data, ok := (diskSource{}).ReadFile("anything"); ok || data != nil {
		t.Fatalf("empty disk source = %#v ok:%v", data, ok)
	}
	if got := r.sourceLabel(diskSource{dir: dir}, "a.md"); !strings.HasPrefix(got, "disk:") {
		t.Fatalf("disk source label = %q", got)
	}
	if got := r.sourceLabel(embedded, "a.md"); got != "embedded:prompts/sections/a.md" {
		t.Fatalf("embedded source label = %q", got)
	}
	memory := wppMemorySource{files: map[string][]byte{"x.md": []byte("memory")}}
	if got := r.sourceLabel(memory, "x.md"); got != "unknown:x.md" {
		t.Fatalf("unknown source label = %q", got)
	}
	memResolver := &sectionResolver{sources: []sectionSource{memory}}
	if content := memResolver.readAndRender("x", promptData{}); content != "memory" {
		t.Fatalf("memory source read = %q", content)
	}
	if len(memResolver.tracked) != 1 || memResolver.tracked[0].Label != "unknown:x.md" {
		t.Fatalf("memory source tracking = %#v", memResolver.tracked)
	}
	embeddedResolver := &sectionResolver{provider: "openai", agent: defaultAgentName, agentFS: bundled.Agents(), sources: []sectionSource{embedded}}
	if out, sources, err := embeddedResolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "system", promptData{Provider: "openai", Agent: defaultAgentName}); err != nil || out == "" || len(sources) == 0 {
		t.Fatalf("embedded render = %q sources:%#v err:%v", out, sources, err)
	}
	if _, _, err := embeddedResolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "missing", promptData{}); err == nil {
		t.Fatal("missing embedded template unexpectedly rendered")
	}
}

func wppOutline(t *testing.T, token string) {
	t.Helper()
	validJob := `{"job_id":"job-` + token + `","status":"completed","transcript_ref":"local:` + token + `"}`
	if result, ok := decodeJobResult(validJob); !ok || result.effectiveJobID() != "job-"+token {
		t.Fatalf("job decode = %#v ok:%v", result, ok)
	}
	for _, body := range []string{"", "not json", `{"status":"no ids"}`, validJob + " {}"} {
		if _, ok := decodeJobResult(body); ok {
			t.Fatalf("invalid job body accepted: %q", body)
		}
	}
	if info, ok := extractJobResult(validJob); !ok || info.status != "completed" || info.transcriptRef != "local:"+token {
		t.Fatalf("job extract = %#v ok:%v", info, ok)
	}
	if _, ok := extractJobResult("not json"); ok {
		t.Fatal("invalid job extract was accepted")
	}
	if err := decodeSingleJSON(json.NewDecoder(strings.NewReader("{} trailing")), &map[string]any{}); err == nil {
		t.Fatal("decodeSingleJSON accepted trailing data")
	}

	// decodeSingleJSON requires a json.Decoder; exercise both its malformed and
	// trailing-data branches through decodeJobResult above, then use a structured
	// transcript for the paired outline projection.
	calls := func(text string, cs ...*llm.ToolCallData) llm.Message {
		msg := llm.Assistant(text)
		for _, call := range cs {
			msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: call})
		}
		return msg
	}
	resultMessage := func(results ...*llm.ToolResultData) llm.Message {
		msg := llm.Message{Role: llm.RoleTool}
		for _, result := range results {
			msg.Content = append(msg.Content, llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: result})
		}
		return msg
	}
	entries := []transcript.Entry{
		{Turn: schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("inspect " + token)}},
		{Turn: schema.Turn{Kind: schema.TurnAssistant, Message: calls("read", &llm.ToolCallData{ID: "ok", Name: "read_file"})}},
		{Turn: schema.Turn{Kind: schema.TurnToolResults, Message: resultMessage(&llm.ToolResultData{ToolCallID: "ok", Name: "read_file", Content: "line one\nline two"})}},
		{Turn: schema.Turn{Kind: schema.TurnAssistant, Message: calls("fails", &llm.ToolCallData{ID: "err", Name: "shell"})}},
		{Turn: schema.Turn{Kind: schema.TurnToolResults, Message: resultMessage(&llm.ToolResultData{ToolCallID: "err", Name: "shell", Content: "boom", IsError: true})}},
		{Turn: schema.Turn{Kind: schema.TurnAssistant, Message: calls("wait", &llm.ToolCallData{ID: "pending", Name: "grep"})}},
		{Turn: schema.Turn{Kind: schema.TurnAssistant, Message: calls("delegate", &llm.ToolCallData{ID: "job", Name: "delegate"})}},
		{Turn: schema.Turn{Kind: schema.TurnToolResults, Message: resultMessage(&llm.ToolResultData{ToolCallID: "job", Name: "delegate", Content: "fallback", ToolState: []byte(validJob)})}},
		{Turn: schema.Turn{Kind: schema.TurnAssistant, Message: calls("legacy", &llm.ToolCallData{ID: "job-none", Name: "job_send_message"})}},
		{Turn: schema.Turn{Kind: schema.TurnToolResults, Message: resultMessage(&llm.ToolResultData{ToolCallID: "job-none", Name: "job_send_message", Content: `{"current_job_id":"current","status":"running"}`})}},
		{Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("thinking " + token)}},
		{Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "reasoning " + token}}}}}},
		{Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("")}},
	}
	content, truncated, elided := renderOutline(entries, 0, len(entries)-1)
	if truncated || elided != 0 {
		t.Fatalf("small outline unexpectedly truncated: %v %d\n%s", truncated, elided, content)
	}
	for _, want := range []string{"inspect " + token, "read_file", "error", "pending", "delegate[status=completed child=local:" + token + "]", "job_send_message[status=running child=(none)]", "thinking " + token, "reasoning " + token} {
		if !strings.Contains(content, want) {
			t.Fatalf("outline missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "\n2 · ToolResults") {
		t.Fatalf("tool results rendered as an independent outline line:\n%s", content)
	}
	if replay, replayTruncated, replayElided := renderOutline(entries, 0, len(entries)-1); replay != content || replayTruncated != truncated || replayElided != elided {
		t.Fatal("outline changed on replay")
	}
	if partial, _, _ := renderOutline(entries, 1, 1); strings.Contains(partial, "inspect "+token) || !strings.Contains(partial, "read_file") {
		t.Fatalf("outline range projection = %q", partial)
	}
	if empty, wasTruncated, count := renderOutline(entries, len(entries), len(entries)); empty != "" || wasTruncated || count != 0 {
		t.Fatalf("empty outline range = %q %v %d", empty, wasTruncated, count)
	}

	idx := buildResultIndex(entries, 0)
	if got := toolResultStateOrContent(nil); got != "" {
		t.Fatalf("nil result content = %q", got)
	}
	if got := toolResultStateOrContent(&llm.ToolResultData{Content: 42}); got != "42" {
		t.Fatalf("numeric result content = %q", got)
	}
	if got := toolResultStateOrContent(idx.byCallID["job"].result); got != validJob {
		t.Fatalf("state result content = %q", got)
	}
	if brackets := jobLifecycleBrackets([]*llm.ToolCallData{{ID: "missing", Name: "delegate"}}, &idx); len(brackets) != 0 {
		t.Fatalf("unpaired lifecycle bracket = %#v", brackets)
	}
	badLifecycle := &resultIndex{byCallID: map[string]pairedResult{
		"bad": {result: &llm.ToolResultData{ToolCallID: "bad", ToolState: []byte("not json")}},
	}}
	if brackets := jobLifecycleBrackets([]*llm.ToolCallData{{ID: "bad", Name: "delegate_send"}}, badLifecycle); len(brackets) != 0 {
		t.Fatalf("invalid lifecycle bracket = %#v", brackets)
	}
	if got := callStatus(nil, &idx); got != "ok" {
		t.Fatalf("empty call status = %q", got)
	}
	if got := callStatus([]*llm.ToolCallData{{ID: "pending"}}, &idx); got != "pending" {
		t.Fatalf("pending call status = %q", got)
	}
	if got := callStatus([]*llm.ToolCallData{{ID: "err"}}, &idx); got != "error" {
		t.Fatalf("error call status = %q", got)
	}
	if got := resultSizeNote([]*llm.ToolCallData{{ID: "pending"}}, &idx); got != "" {
		t.Fatalf("pending result size = %q", got)
	}
	long := &llm.ToolResultData{ToolCallID: "long", Content: strings.Repeat("x", resultLineMaxRunes+1)}
	longIdx := &resultIndex{byCallID: map[string]pairedResult{"long": {result: long}}}
	if got := resultSizeNote([]*llm.ToolCallData{{ID: "long"}}, longIdx); !strings.HasSuffix(got, "[truncated]") {
		t.Fatalf("wide result size = %q", got)
	}
	if anyLineWiderThan("short\n", resultLineMaxRunes) || !anyLineWiderThan(long.Content.(string), resultLineMaxRunes) {
		t.Fatal("line width predicate disagreed with its fixtures")
	}
	if got := turnPlainText(schema.Turn{Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "a"}, {Kind: llm.ContentText}, {Kind: llm.ContentThinking}, {Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "b"}}}}}); got != "a b" {
		t.Fatalf("turn plain text = %q", got)
	}
	if got := strings.Join(joinNonEmpty([]string{"", "a", "", "b"}), ","); got != "a,b" {
		t.Fatalf("join nonempty = %q", got)
	}
	for kind, want := range map[schema.TurnKind]string{
		schema.TurnUserInput: "User", schema.TurnAssistant: "Assistant", schema.TurnSteering: "Steering",
		schema.TurnSummary: "Summary", schema.TurnCheckpoint: "Checkpoint", schema.TurnSystem: "System",
		schema.TurnToolResults: "ToolResults", schema.TurnTool: "ToolResults", schema.TurnKind("OTHER"): "OTHER",
	} {
		if got := outlineRoleLabel(kind); got != want {
			t.Fatalf("role %q = %q, want %q", kind, got, want)
		}
	}
	bigLines := []string{strings.Repeat("x", convBudgetChars+100)}
	if bounded, wasTruncated, count := boundOutline(bigLines); !wasTruncated || count != 1 || len([]rune(bounded)) > convBudgetChars {
		t.Fatalf("bounded giant outline = len:%d truncated:%v count:%d", len([]rune(bounded)), wasTruncated, count)
	}
	retainedLines := make([]string, 48)
	for i := range retainedLines {
		retainedLines[i] = fmt.Sprintf("%02d %s", i, strings.Repeat("x", 700))
	}
	retained, retainedTruncated, retainedCount := boundOutline(retainedLines)
	if !retainedTruncated || retainedCount <= 0 || !strings.Contains(retained, retainedLines[0]) || !strings.Contains(retained, retainedLines[len(retainedLines)-1]) {
		t.Fatalf("head/tail outline retention = truncated:%v count:%d\n%s", retainedTruncated, retainedCount, retained)
	}
}

func wppWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func wppHasTool(entries []toolEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name == name {
			return true
		}
	}
	return false
}

func wppHasSource(sources []promptSource, label string) bool {
	for _, source := range sources {
		if source.Label == label {
			return true
		}
	}
	return false
}

func wppHasSourcePrefix(sources []promptSource, prefix string) bool {
	for _, source := range sources {
		if strings.HasPrefix(source.Label, prefix) {
			return true
		}
	}
	return false
}

func snapshotGitTrace(calls []string) string { return strings.Join(calls, "|") }

type wppReply struct {
	result execenv.ExecResult
	err    error
}

// wppEnv is a fully scripted ExecutionEnvironment. In particular, ExecCommand
// only records a requested command and returns a fixture reply; it cannot spawn
// a shell or git process.
type wppEnv struct {
	workDir string
	replies map[string]wppReply
	calls   []string
}

func (e *wppEnv) Initialize() error        { return nil }
func (e *wppEnv) Cleanup()                 {}
func (e *wppEnv) WorkingDirectory() string { return e.workDir }
func (e *wppEnv) Platform() string         { return "linux" }
func (e *wppEnv) OSVersion() string        { return "fixture" }
func (e *wppEnv) ReadFile(string, *int, *int) (string, error) {
	return "", errors.New("wppEnv ReadFile is not a filesystem")
}
func (e *wppEnv) WriteFile(string, string) (string, error) {
	return "", errors.New("wppEnv WriteFile is not a filesystem")
}
func (e *wppEnv) EditFile(string, string, string, bool) (string, error) {
	return "", errors.New("wppEnv EditFile is not a filesystem")
}
func (e *wppEnv) FileExists(string) bool { return false }
func (e *wppEnv) Glob(string, string, ...bool) ([]string, error) {
	return nil, errors.New("wppEnv Glob is not a filesystem")
}
func (e *wppEnv) Grep(string, string, string, bool, int, string) (string, error) {
	return "", errors.New("wppEnv Grep is not a filesystem")
}
func (e *wppEnv) ListDirectory(string, int) ([]execenv.DirEntry, error) {
	return nil, errors.New("wppEnv ListDirectory is not a filesystem")
}
func (e *wppEnv) ExecCommand(_ context.Context, command string, _ int, _ string, _ map[string]string) (execenv.ExecResult, error) {
	e.calls = append(e.calls, command)
	if reply, ok := e.replies[command]; ok {
		return reply.result, reply.err
	}
	return execenv.ExecResult{ExitCode: 1}, nil
}

var _ execenv.ExecutionEnvironment = (*wppEnv)(nil)

type wppMemorySource struct{ files map[string][]byte }

func (s wppMemorySource) ReadFile(name string) ([]byte, bool) {
	data, ok := s.files[name]
	return data, ok
}

var _ sectionSource = wppMemorySource{}
