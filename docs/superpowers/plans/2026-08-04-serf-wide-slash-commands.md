# Serf-Wide Slash Commands Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add user-defined slash commands discovered from `.serf/commands/` (project, git-root→cwd) and `~/.config/serf/commands/` (user-global) that expand inert, shadow plugin commands by bare name, and become invocable from the web palette.

**Architecture:** Reuse the plugin command pipeline (`plugin.ParseCommand`, `plugin.ResolveCommand`, `Session.expandSlashCommand`). Serf-wide commands enter `Session.pluginCommands` under bare-name keys (plugin commands keep `plugin:name` keys), so the existing exact-match-first resolution gives precedence project > user > plugin for free. Expansion branches on a new `Command.Source`: plugin commands keep full `command.Expand` (shell execution), serf-wide commands use a new inert `command.ExpandArgs`. The hub catalog and web palette gain source-labeled entries, and the palette gains an exact-match fallthrough that forwards unmatched `/name args` to the session.

**Tech Stack:** Go (agent, appwire, cmd/serf-hub), React/TypeScript + zustand (cmd/serf-hub/frontend), make generate/lint-generated for AppWire codegen.

**Spec:** `docs/superpowers/specs/2026-08-04-serf-wide-slash-commands-design.md` (normative — read it first).

## Global Constraints

- `docs/testing.md`: default tests are deterministic — scripted provider (`fakeAdapter`) at the LLM boundary, no live requests, no reliance on ambient machine state.
- Any test touching user-global discovery MUST isolate XDG: `t.Setenv("XDG_CONFIG_HOME", t.TempDir())` so the real `~/.config/serf/commands` is never read.
- Frontend: run `npx biome check --write` on touched files before gates; `make test-web` is the canonical gate; avoid `noNonNullAssertion` and array-index-key violations.
- `make generate` after changing `appwire/types.go`; `make lint-generated` must pass.
- Local `make` invocations may need `SERF_DISK_MIN_FREE_GB=4` while the disk is at ~98% (the go build cache lives on another volume).
- Plugin command behavior is unchanged: `command.Expand` semantics, `plugin:name` namespacing, fail-hard plugin file reads.

---

### Task 1: `Command` gains `Source` and `File`

**Files:**
- Modify: `agent/plugin/commands.go:12-24` (struct + doc comment), `:66-74` (ParseCommand return), `:95-108` (discoverPluginCommands loop)
- Test: `agent/plugin/commands_test.go`

**Interfaces:**
- Produces: `plugin.Command{Source string, File string}`; `Source` values `"plugin"`, `"project"`, `"user"`. Later tasks rely on `discoverPluginCommands` setting `Source: "plugin"` and `File` to the absolute file path.

- [ ] **Step 1: Write the failing test**

Add to `agent/plugin/commands_test.go`:

```go
func TestDiscoverPluginCommands_SetsSourceAndFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	commandsDir := filepath.Join(dir, "commands")
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(commandsDir, "hello.md")
	if err := os.WriteFile(file, []byte("body"), 0644); err != nil {
		t.Fatal(err)
	}
	commands, err := discoverPluginCommands(dir, nil, "p")
	if err != nil {
		t.Fatalf("discoverPluginCommands: %v", err)
	}
	cmd := commands["p:hello"]
	if cmd.Source != "plugin" {
		t.Errorf("Source = %q, want %q", cmd.Source, "plugin")
	}
	if cmd.File != file {
		t.Errorf("File = %q, want %q", cmd.File, file)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./plugin/ -run TestDiscoverPluginCommands_SetsSourceAndFile -v`
Expected: FAIL — `Source` and `File` are unset (empty).

- [ ] **Step 3: Add the fields**

In `agent/plugin/commands.go`, update the doc comment (currently "defined by a plugin") and struct:

```go
// Command represents a slash command. Plugin commands come from a plugin's
// commands/ directory; serf-wide commands come from .serf/commands/ project
// directories or the user-global config dir (see serfwide.go). Invoking a
// plugin command expands Body with command.Expand (shell execution);
// serf-wide commands expand inert with command.ExpandArgs.
type Command struct {
	Name         string   // command name, derived from the command's .md filename (not frontmatter — see ParseCommand)
	Description  string   // shown in command catalogs/autocomplete
	ArgumentHint string   // display hint for the arguments the command expects
	Model        string   // requested per-turn model override (parsed; not yet enforced — see design §14)
	AllowedTools []string // requested per-turn tool restriction, verbatim as declared (parsed; not yet enforced — see design §14)
	Body         string   // markdown template body
	PluginName   string   // owning plugin; empty for serf-wide commands
	Source       string   // "plugin", "project", or "user"
	File         string   // absolute path of the defining .md
}
```

In the `discoverPluginCommands` loop, set both fields after `ParseCommand`:

```go
		command, err := ParseCommand(data, name, pluginName)
		if err != nil {
			return nil, fmt.Errorf("parsing command file %q: %w", filepath.Base(file), err)
		}
		command.Source = "plugin"
		command.File = file
```

- [ ] **Step 4: Run tests**

Run: `cd agent && go test ./plugin/`
Expected: PASS (new test + all existing).

- [ ] **Step 5: Commit**

```bash
git add agent/plugin/commands.go agent/plugin/commands_test.go
git commit -m "agent/plugin: add Source and File to Command"
```

---

### Task 2: `command.ExpandArgs` (inert expansion)

**Files:**
- Modify: `agent/command/expand.go`
- Test: `agent/command/expand_test.go`

**Interfaces:**
- Consumes: existing `substituteArguments(body, args string) string` (expand.go:141).
- Produces: `func ExpandArgs(body, args string) string` — substitutes `$ARGUMENTS`/`$1..$9` over the whole body; `!`cmd`` and `@file` spans remain text (substitution still applies inside them as inert text). Used by Task 7.

- [ ] **Step 1: Write the failing test**

Add to `agent/command/expand_test.go`:

```go
func TestExpandArgs(t *testing.T) {
	t.Parallel()
	body := "Review $1 against @doc.md and run !`git diff` for $ARGUMENTS"
	got := ExpandArgs(body, "main --stat")
	want := "Review main against @doc.md and run !`git diff` for main --stat"
	if got != want {
		t.Errorf("ExpandArgs = %q, want %q", got, want)
	}
}

func TestExpandArgs_SubstitutesInsideSpansAsInertText(t *testing.T) {
	t.Parallel()
	// $1 inside a !` span is replaced, but the span itself is never
	// executed — it stays text.
	got := ExpandArgs("run !`echo $1` now", "foo")
	want := "run !`echo foo` now"
	if got != want {
		t.Errorf("ExpandArgs = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./command/ -run TestExpandArgs -v`
Expected: FAIL — `ExpandArgs` undefined.

- [ ] **Step 3: Implement**

Add to `agent/command/expand.go` after `Expand`:

```go
// ExpandArgs renders a serf-wide slash command's body: $ARGUMENTS and
// $1..$9 substitute as inert text over the whole body, and nothing else
// happens. !`cmd` spans and @file references are never executed or read —
// they remain text (substitution still applies inside them, as inert
// text). This is the serf-wide command posture: auto-discovered templates
// are data, never code (docs/skills.md).
func ExpandArgs(body, args string) string {
	return substituteArguments(body, args)
}
```

- [ ] **Step 4: Run tests**

Run: `cd agent && go test ./command/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/command/expand.go agent/command/expand_test.go
git commit -m "agent/command: add inert ExpandArgs for serf-wide commands"
```

---

### Task 3: Serf-wide discovery core

**Files:**
- Create: `agent/plugin/serfwide.go`
- Test: `agent/plugin/serfwide_test.go`

**Interfaces:**
- Consumes: `execenv.ExecutionEnvironment` (`WorkingDirectory()`), `execenv.GitRootOrEmpty`, `execenv.DirsFromRootToCwd`, `ParseCommand`, `envvars.XDGConfigHome`, `events.WarningData`.
- Produces: `func DiscoverSerfWideCommands(env execenv.ExecutionEnvironment) (map[string]Command, []events.WarningData)` — user-global dir scanned first, then `<dir>/.serf/commands` walking git-root→cwd; later scans shadow earlier by bare-name key. A nil env or empty cwd skips the project walk but still scans user-global. Tasks 4–6 and 9 rely on this exact signature.

- [ ] **Step 1: Write the failing tests**

Create `agent/plugin/serfwide_test.go`:

```go
package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/execenv"
)

// writeSerfwideCommand writes dir/<name>.md with content and returns dir.
func writeSerfwideCommand(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverSerfWideCommands_UserGlobalOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	global := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "serf", "commands")
	writeSerfwideCommand(t, global, "review", "global body")

	got, warnings := DiscoverSerfWideCommands(nil) // nil env: no project walk
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	cmd, ok := got["review"]
	if !ok {
		t.Fatalf("no command %q; got keys %v", "review", maps.Keys(got))
	}
	if cmd.Source != "user" || cmd.Body != "global body" {
		t.Errorf("got %+v, want Source=user Body=%q", cmd, "global body")
	}
}

func TestDiscoverSerfWideCommands_ProjectShadowsUser(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeSerfwideCommand(t, filepath.Join(xdg, "serf", "commands"), "review", "global body")

	workDir := t.TempDir() // not a git repo: root == cwd
	writeSerfwideCommand(t, filepath.Join(workDir, ".serf", "commands"), "review", "project body")

	env := execenv.NewLocalExecutionEnvironment(workDir)
	got, _ := DiscoverSerfWideCommands(env)
	cmd := got["review"]
	if cmd.Source != "project" || cmd.Body != "project body" {
		t.Errorf("got %+v, want project command shadowing user-global", cmd)
	}
}

func TestDiscoverSerfWideCommands_IgnoresNonMarkdown(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "serf", "commands")
	writeSerfwideCommand(t, dir, "review", "body")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got, warnings := DiscoverSerfWideCommands(nil)
	if len(got) != 1 || len(warnings) != 0 {
		t.Errorf("got %d commands, %d warnings; want 1, 0", len(got), len(warnings))
	}
}
```

(Add `"maps"` to imports.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd agent && go test ./plugin/ -run TestDiscoverSerfWideCommands -v`
Expected: FAIL — `DiscoverSerfWideCommands` undefined.

- [ ] **Step 3: Implement the core**

Create `agent/plugin/serfwide.go`:

```go
package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/envvars"
)

var serfwideUserHomeDir = os.UserHomeDir

// globalCommandsDir resolves the user-global commands directory:
// $XDG_CONFIG_HOME/serf/commands, or ~/.config/serf/commands. Mirrors
// promptpath.globalPromptsDir. Returns "" when no home is resolvable.
func globalCommandsDir() string {
	dir := envvars.XDGConfigHome.Getenv()
	if dir == "" {
		home, err := serfwideUserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "commands")
}

// DiscoverSerfWideCommands scans the user-global commands dir, then walks
// git-root→cwd scanning <dir>/.serf/commands, returning commands keyed by
// bare name. Later scans shadow earlier ones, so the deepest project dir
// wins and every project command shadows the user-global one. A nil env or
// empty cwd skips the project walk but still scans the user-global dir
// (the hub catalog relies on this).
//
// Discovery is fail-soft: a missing dir is silent, and per-file problems
// (unreadable dir/file, bad name, malformed frontmatter) skip the file
// with a warning rather than failing the scan.
func DiscoverSerfWideCommands(env execenv.ExecutionEnvironment) (map[string]Command, []events.WarningData) {
	out := map[string]Command{}
	var warnings []events.WarningData

	if dir := globalCommandsDir(); dir != "" {
		scanSerfwideDir(dir, "user", out, &warnings)
	}

	if env != nil {
		cwd := strings.TrimSpace(env.WorkingDirectory())
		if cwd != "" {
			if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
				cwd = resolved
			}
			root := cwd
			if gr := execenv.GitRootOrEmpty(env, cwd); gr != "" {
				root = gr
			}
			for _, dir := range execenv.DirsFromRootToCwd(root, cwd) {
				scanSerfwideDir(filepath.Join(dir, ".serf", "commands"), "project", out, &warnings)
			}
		}
	}

	return out, warnings
}

// scanSerfwideDir parses every immediate .md file of dir into out, keyed by
// bare filename. Guards and warnings land in Task 4; this step covers
// scanning, parsing, source/file labeling, and shadowing.
func scanSerfwideDir(dir, source string, out map[string]Command, warnings *[]events.WarningData) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			*warnings = append(*warnings, serfwideWarning("unreadable commands directory",
				fmt.Sprintf("skipping commands directory %s: %v", dir, err)))
		}
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		file := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(file)
		if err != nil {
			*warnings = append(*warnings, serfwideWarning("unreadable command file",
				fmt.Sprintf("skipping command file %s: %v", file, err)))
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		command, err := ParseCommand(data, name, "")
		if err != nil {
			*warnings = append(*warnings, serfwideWarning("malformed command file",
				fmt.Sprintf("skipping command file %s: %v", file, err)))
			continue
		}
		command.Source = source
		command.File = file
		out[name] = command
	}
}

func serfwideWarning(title, message string) events.WarningData {
	return events.WarningData{Source: "commands", Title: title, Message: message}
}
```

(`events.WarningData` has `Source`, `Title`, `Message` fields — same shape as the hook warnings in `agent/session_init.go`.)

- [ ] **Step 4: Run tests**

Run: `cd agent && go test ./plugin/ -run TestDiscoverSerfWideCommands -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin/serfwide.go agent/plugin/serfwide_test.go
git commit -m "agent/plugin: serf-wide command discovery core"
```

---

### Task 4: Discovery guards and advisory warnings

**Files:**
- Modify: `agent/plugin/serfwide.go`
- Test: `agent/plugin/serfwide_test.go`

**Interfaces:**
- Consumes: `scanSerfwideDir` from Task 3.
- Produces: rejection rules (colon, whitespace, empty name) and advisories (`!`` spans, unenforced `model`/`allowed-tools`) as `events.WarningData` entries naming the file.

- [ ] **Step 1: Write the failing tests**

Add to `agent/plugin/serfwide_test.go`:

```go
func TestDiscoverSerfWideCommands_RejectsBadNames(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "serf", "commands")
	writeSerfwideCommand(t, dir, "ok", "body")
	writeSerfwideCommand(t, dir, "p:forge", "body")   // colon: namespace forgery
	writeSerfwideCommand(t, dir, "my command", "body") // whitespace: uninvokable
	writeSerfwideCommand(t, dir, "", "body")           // file named exactly ".md"

	got, warnings := DiscoverSerfWideCommands(nil)
	if len(got) != 1 {
		t.Errorf("got keys %v, want only [ok]", maps.Keys(got))
	}
	if len(warnings) != 3 {
		t.Errorf("got %d warnings, want 3 (colon, whitespace, empty): %v", len(warnings), warnings)
	}
}

func TestDiscoverSerfWideCommands_MalformedFrontmatterSkipped(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "serf", "commands")
	writeSerfwideCommand(t, dir, "broken", "---\n[unclosed\n---\nbody")
	got, warnings := DiscoverSerfWideCommands(nil)
	if len(got) != 0 || len(warnings) != 1 {
		t.Errorf("got %d commands, %d warnings; want 0, 1", len(got), len(warnings))
	}
}

func TestDiscoverSerfWideCommands_Advisories(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "serf", "commands")
	writeSerfwideCommand(t, dir, "exec", "run !`git status` here")
	writeSerfwideCommand(t, dir, "front", "---\nmodel: gpt-5.2\nallowed-tools:\n  - shell\n---\nbody")
	_, warnings := DiscoverSerfWideCommands(nil)
	if len(warnings) != 2 {
		t.Fatalf("got %d warnings, want 2: %v", len(warnings), warnings)
	}
	for _, w := range warnings {
		if !strings.Contains(w.Message, dir) {
			t.Errorf("warning %q does not name the file", w.Message)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd agent && go test ./plugin/ -run 'TestDiscoverSerfWideCommands_(RejectsBadNames|Malformed|Advisories)' -v`
Expected: FAIL — bad names load (3 keys), no advisory warnings.

- [ ] **Step 3: Implement the guards**

In `agent/plugin/serfwide.go`, replace the per-file loop body in `scanSerfwideDir` (between the `.md` filter and the parse) and add advisories after a successful parse:

```go
		name := strings.TrimSuffix(entry.Name(), ".md")
		if name == "" {
			*warnings = append(*warnings, serfwideWarning("empty command name",
				fmt.Sprintf("skipping command file %s: a file named exactly .md has no command name", file)))
			continue
		}
		if strings.Contains(name, ":") {
			*warnings = append(*warnings, serfwideWarning("colon in command name",
				fmt.Sprintf("skipping command file %s: ':' is the plugin namespace separator", file)))
			continue
		}
		if strings.IndexFunc(name, unicode.IsSpace) >= 0 {
			*warnings = append(*warnings, serfwideWarning("whitespace in command name",
				fmt.Sprintf("skipping command file %s: names with whitespace can never be invoked", file)))
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			*warnings = append(*warnings, serfwideWarning("unreadable command file",
				fmt.Sprintf("skipping command file %s: %v", file, err)))
			continue
		}
		command, err := ParseCommand(data, name, "")
		if err != nil {
			*warnings = append(*warnings, serfwideWarning("malformed command file",
				fmt.Sprintf("skipping command file %s: %v", file, err)))
			continue
		}
		command.Source = source
		command.File = file
		if execSpanPattern.MatchString(command.Body) {
			*warnings = append(*warnings, serfwideWarning("inert execution directive",
				fmt.Sprintf("command file %s contains !` spans: execution directives are inert in serf-wide commands; use a plugin command for executable templates", file)))
		}
		if command.Model != "" || len(command.AllowedTools) > 0 {
			*warnings = append(*warnings, serfwideWarning("unenforced command frontmatter",
				fmt.Sprintf("command file %s declares model/allowed-tools, which serf does not enforce yet", file)))
		}
		out[name] = command
```

Add near the top of the file:

```go
// execSpanPattern matches a !`cmd` execution span in a command body. The
// same shape as command.cmdOrFilePattern's first alternative.
var execSpanPattern = regexp.MustCompile("!`[^`]*`")
```

and `"regexp"` to imports.

- [ ] **Step 4: Run tests**

Run: `cd agent && go test ./plugin/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin/serfwide.go agent/plugin/serfwide_test.go
git commit -m "agent/plugin: serf-wide discovery guards and advisory warnings"
```

---

### Task 5: `MergeCommands` shared loader

**Files:**
- Modify: `agent/plugin/plugin.go` (after `LoadAllFailSoft`, ~line 330)
- Test: `agent/plugin/commands_test.go`

**Interfaces:**
- Consumes: `Instance.Commands` (map keyed `plugin:name`).
- Produces: `func MergeCommands(instances []Instance, serfwide map[string]Command) map[string]Command` — plugin commands first (namespaced keys), serf-wide overlaid (bare keys). Tasks 6 and 9 consume this.

- [ ] **Step 1: Write the failing test**

Add to `agent/plugin/commands_test.go`:

```go
func TestMergeCommands(t *testing.T) {
	t.Parallel()
	instances := []Instance{
		{Commands: map[string]Command{
			"p:review": {Name: "review", PluginName: "p", Source: "plugin"},
		}},
	}
	serfwide := map[string]Command{
		"review": {Name: "review", Source: "user"},
	}
	got := MergeCommands(instances, serfwide)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %v", len(got), maps.Keys(got))
	}
	if got["review"].Source != "user" || got["p:review"].Source != "plugin" {
		t.Errorf("got %+v, want bare key = user command, namespaced key = plugin command", got)
	}
	// Nil inputs are safe.
	if merged := MergeCommands(nil, nil); len(merged) != 0 {
		t.Errorf("MergeCommands(nil, nil) = %v, want empty", merged)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./plugin/ -run TestMergeCommands -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

Add to `agent/plugin/plugin.go`:

```go
// MergeCommands flattens plugin instances' commands (namespaced keys) and
// overlays serf-wide commands (bare keys), returning the unified command
// map a session or the hub catalog resolves against. Bare keys can never
// collide with "plugin:name" keys (serf-wide discovery rejects colons), so
// the overlay cannot shadow a plugin's qualified key; precedence between a
// bare serf-wide command and a plugin's bare-name fallback is decided by
// ResolveCommand's exact-match-first rule.
func MergeCommands(instances []Instance, serfwide map[string]Command) map[string]Command {
	out := make(map[string]Command, len(serfwide))
	for _, inst := range instances {
		maps.Copy(out, inst.Commands)
	}
	maps.Copy(out, serfwide)
	return out
}
```

(`maps` is already imported in plugin.go.)

- [ ] **Step 4: Run tests**

Run: `cd agent && go test ./plugin/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin/plugin.go agent/plugin/commands_test.go
git commit -m "agent/plugin: add MergeCommands shared command assembly"
```

---

### Task 6: Session init integration

**Files:**
- Modify: `agent/session_init.go:1139` (remove per-plugin merge), `:907-911` (add discovery+merge after initPlugins)
- Modify: `agent/session.go:457-459` (field comment)
- Test: `agent/session_slash_command_test.go`

**Interfaces:**
- Consumes: `plugin.DiscoverSerfWideCommands`, `plugin.MergeCommands`, `s.plugins` (set by `initPlugins`; nil when no plugin dirs — safe for `MergeCommands`).
- Produces: `s.pluginCommands` assembled in one place for every session, including `PluginDirs == nil`.

- [ ] **Step 1: Write the failing tests**

Add to `agent/session_slash_command_test.go`:

```go
// writeSerfwideCommandFile creates <workDir>/.serf/commands/<name>.md.
func writeSerfwideCommandFile(t *testing.T, workDir, name, content string) {
	t.Helper()
	dir := filepath.Join(workDir, ".serf", "commands")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSerfwideCommand_LoadsWithNoPluginDirs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	client := llm.NewClient()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}}
	client.Register(adapter)
	workDir := t.TempDir()
	writeSerfwideCommandFile(t, workDir, "review", "Review $ARGUMENTS")
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	cmd, ok := plugin.ResolveCommand(sess.pluginCommands, "review")
	if !ok || cmd.Source != "project" {
		t.Fatalf("pluginCommands = %v, want project command %q", sess.pluginCommands, "review")
	}
}

func TestSerfwideCommand_ShadowsPluginBareName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	workDir := t.TempDir()
	writeSerfwideCommandFile(t, workDir, "greet", "serf-wide body")
	pluginDir := writePluginCommand(t, "greeter", "greet", "plugin body")
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), SessionConfig{PluginDirs: []string{pluginDir}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	bare, ok := plugin.ResolveCommand(sess.pluginCommands, "greet")
	if !ok || bare.Source == "plugin" {
		t.Errorf("bare /greet resolved to %+v, want the serf-wide command", bare)
	}
	qualified, ok := plugin.ResolveCommand(sess.pluginCommands, "greeter:greet")
	if !ok || qualified.Source != "plugin" {
		t.Errorf("/greeter:greet resolved to %+v, want the plugin command", qualified)
	}
}

func TestSerfwideCommand_DiscoveryWarningsQueued(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	workDir := t.TempDir()
	writeSerfwideCommandFile(t, workDir, "bad name", "body") // whitespace guard fires
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	if len(sess.pluginCommands) != 0 {
		t.Errorf("pluginCommands = %v, want the guarded file skipped", sess.pluginCommands)
	}
	if len(sess.pendingHookWarnings) == 0 {
		t.Error("no discovery warnings queued; want the whitespace-guard warning")
	}
}
```

Add `"primeradiant.com/serf/agent/plugin"` to the test file imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd agent && go test . -run 'TestSerfwideCommand_' -v`
Expected: FAIL — no serf-wide commands in `pluginCommands`.

- [ ] **Step 3: Implement**

In `agent/session_init.go`, remove the per-plugin merge inside `initPlugins`:

```go
-		maps.Copy(s.pluginCommands, p.Commands)
```

and add discovery+assembly immediately after the `initPlugins` call site (~line 908-911), matching the unwrapped skill merge above it:

```go
	// Serf-wide commands (project .serf/commands + user-global config dir)
	// merge with plugin commands in one place. This runs for every session,
	// including PluginDirs == nil, so it must not live inside initPlugins
	// (which early-returns on empty PluginDirs). Discovery is fail-soft;
	// warnings join the same session-start queue as command frontmatter
	// warnings.
	serfwide, cmdWarnings := plugin.DiscoverSerfWideCommands(s.currentEnv())
	s.pluginCommands = plugin.MergeCommands(s.plugins, serfwide)
	s.pendingHookWarnings = append(s.pendingHookWarnings, cmdWarnings...)
```

In `agent/session.go`, update the field comment:

```go
	// pluginCommands is the union of every loaded plugin's slash commands
	// (namespaced "plugin-name:command-name") and all serf-wide commands
	// (bare-name keys from .serf/commands and the user-global config dir).
	// Looked up by expandSlashCommand via plugin.ResolveCommand.
	pluginCommands map[string]plugin.Command
```

- [ ] **Step 4: Run tests**

Run: `cd agent && go test . -run 'TestSerfwideCommand_|TestExpandSlashCommand_|TestSessionInitPlugins' `
Expected: PASS. Then `cd agent && go test . -run 'Plugin'` to catch regressions from removing the per-plugin merge.

- [ ] **Step 5: Commit**

```bash
git add agent/session_init.go agent/session.go agent/session_slash_command_test.go
git commit -m "agent: discover and merge serf-wide commands at session init"
```

---

### Task 7: `expandSlashCommand` branches on `Source`

**Files:**
- Modify: `agent/session_slash_command.go:34-47`
- Test: `agent/session_slash_command_test.go`

**Interfaces:**
- Consumes: `command.ExpandArgs` (Task 2), `Command.Source` (Task 1).
- Produces: plugin commands (`Source == "plugin"`) expand via `command.Expand`; everything else expands inert via `command.ExpandArgs`.

- [ ] **Step 1: Write the failing test**

Add to `agent/session_slash_command_test.go`:

```go
// execRecordingEnv wraps a local execution environment and records
// ExecCommand calls, so tests can assert a !` span never executed.
type execRecordingEnv struct {
	execenv.ExecutionEnvironment
	calls int
}

func (e *execRecordingEnv) ExecCommand(ctx context.Context, command string, timeoutMs int, dir string, env map[string]string) execenv.CommandResult {
	e.calls++
	return e.ExecutionEnvironment.ExecCommand(ctx, command, timeoutMs, dir, env)
}

func TestExpandSlashCommand_SerfwideDoesNotExecute(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}})
	workDir := t.TempDir()
	writeSerfwideCommandFile(t, workDir, "deploy", "Deploying !`touch SHOULD_NOT_EXIST` for $1")
	env := &execRecordingEnv{ExecutionEnvironment: execenv.NewLocalExecutionEnvironment(workDir)}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)

	got, ok := sess.expandSlashCommand(context.Background(), "/deploy v2")
	if !ok {
		t.Fatal("expected ok=true for a serf-wide command")
	}
	if env.calls != 0 {
		t.Errorf("ExecCommand called %d times; serf-wide expansion must never execute", env.calls)
	}
	if !strings.Contains(got, "!`touch SHOULD_NOT_EXIST`") || !strings.Contains(got, "for v2") {
		t.Errorf("expanded %q, want the !` span kept as text with $1 substituted", got)
	}
	if _, statErr := os.Stat(filepath.Join(workDir, "SHOULD_NOT_EXIST")); !os.IsNotExist(statErr) {
		t.Error("the !` span executed: SHOULD_NOT_EXIST exists")
	}
}
```

(Verify the `execenv.CommandResult`/`ExecCommand` signature against `agent/execenv/execenv.go` and `runInlineCommand`'s call shape before writing; adjust the wrapper's signature to match exactly.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test . -run TestExpandSlashCommand_SerfwideDoesNotExecute -v`
Expected: FAIL — the `!` span executes (`calls` == 1, file exists).

- [ ] **Step 3: Implement the branch**

In `agent/session_slash_command.go`, after `ResolveCommand` succeeds:

```go
	if cmd.Source != "plugin" {
		// Serf-wide commands expand inert: arguments substitute as text,
		// nothing executes or reads (docs/skills.md).
		return command.ExpandArgs(cmd.Body, strings.TrimSpace(args)), true
	}
	expanded, err := command.Expand(ctx, cmd.Body, strings.TrimSpace(args), s.currentEnv())
```

(The existing error handling for `Expand` stays as-is under the plugin branch.)

- [ ] **Step 4: Run tests**

Run: `cd agent && go test . -run 'TestExpandSlashCommand_'`
Expected: PASS — new test plus all existing plugin-command expansion tests.

- [ ] **Step 5: Commit**

```bash
git add agent/session_slash_command.go agent/session_slash_command_test.go
git commit -m "agent: expand serf-wide commands inert via ExpandArgs"
```

---

### Task 8: AppWire `source` field + codegen

**Files:**
- Modify: `appwire/types.go:1844-1852` (CommandDescriptor + doc comment)
- Modify: `appwire/protocol.go:158` (method description)
- Modify: `docs/appwire-protocol.md` (`serf/command/list` row)
- Generated: `cmd/serf-hub/frontend/src/protocol/types.gen.ts` (via make generate)

**Interfaces:**
- Produces: `CommandDescriptor.Source string \`json:"source,omitempty"\`` — `"plugin"` or `"user"` in this implementation; `"project"` reserved. The frontend (Tasks 10–11) reads it from `types.gen.ts`.

- [ ] **Step 1: Update the type and description**

In `appwire/types.go`:

```go
// CommandDescriptor describes one slash command — plugin-provided or
// serf-wide — for catalog/autocomplete display. Name is unqualified;
// PluginName disambiguates when more than one plugin defines the same
// command name.
type CommandDescriptor struct {
	Name         string `json:"name"`
	PluginName   string `json:"pluginName,omitempty"`
	Description  string `json:"description,omitempty"`
	ArgumentHint string `json:"argumentHint,omitempty"`
	// Source is "plugin" or "user"; "project" is reserved for a future
	// project-scoped catalog (project commands are cwd-dependent and never
	// appear in the hub-wide catalog).
	Source string `json:"source,omitempty"`
}
```

In `appwire/protocol.go:158`, update the description string to: `"Lists loaded slash commands (name, plugin, description, source) for catalog/autocomplete display."`

In `docs/appwire-protocol.md`, update the `serf/command/list` row to mention the `source` field (`plugin`/`user`).

- [ ] **Step 2: Regenerate**

Run: `make generate && make lint-generated`
Expected: `types.gen.ts` gains `source?: string` on `CommandDescriptor`; lint passes. (Prefix `SERF_DISK_MIN_FREE_GB=4` if the preflight trips.)

- [ ] **Step 3: Run AppWire tests**

Run: `go test ./appwire/`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add appwire/types.go appwire/protocol.go docs/appwire-protocol.md cmd/serf-hub/frontend/src/protocol/types.gen.ts
git commit -m "appwire: add source to CommandDescriptor"
```

---

### Task 9: Hub catalog via `MergeCommands`

**Files:**
- Modify: `cmd/serf-hub/app_rpc.go:815-839` (`hubCommandList`)
- Test: `cmd/serf-hub/app_command_list_test.go`

**Interfaces:**
- Consumes: `plugin.MergeCommands`, `plugin.DiscoverSerfWideCommands(nil)` (nil env — the hub is multi-project and must never see project commands).
- Produces: catalog entries carry `Source`; user-global commands appear even with zero plugin dirs.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/serf-hub/app_command_list_test.go`:

```go
func TestHubCommandList_UserGlobalWithoutPlugins(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	commandsDir := filepath.Join(xdg, "serf", "commands")
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "standup.md"), []byte("standup body"), 0644); err != nil {
		t.Fatal(err)
	}
	resp, err := hubCommandList(hubcore.WebConfig{PluginRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("hubCommandList: %v", err)
	}
	if len(resp.Commands) != 1 {
		t.Fatalf("got %d commands, want 1: %+v", len(resp.Commands), resp.Commands)
	}
	if resp.Commands[0].Name != "standup" || resp.Commands[0].Source != "user" {
		t.Errorf("got %+v, want Name=standup Source=user", resp.Commands[0])
	}
}

func TestHubCommandList_ShadowedPluginListsBoth(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	commandsDir := filepath.Join(xdg, "serf", "commands")
	if err := os.MkdirAll(commandsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// A user-global "greet" shadows the plugin's "greet" (writeCommandListTestPlugin
	// always writes greet.md).
	if err := os.WriteFile(filepath.Join(commandsDir, "greet.md"), []byte("user body"), 0644); err != nil {
		t.Fatal(err)
	}
	pluginDir := t.TempDir()
	writeCommandListTestPlugin(t, pluginDir, "greeter")
	resp, err := hubCommandList(hubcore.WebConfig{PluginRoot: t.TempDir(), PluginDirs: []string{pluginDir}})
	if err != nil {
		t.Fatalf("hubCommandList: %v", err)
	}
	if len(resp.Commands) != 2 {
		t.Fatalf("got %d commands, want 2 (shadowed plugin + user shadow): %+v", len(resp.Commands), resp.Commands)
	}
	var sources []string
	for _, c := range resp.Commands {
		if c.Source == "project" {
			t.Errorf("hub catalog contains a project-sourced entry %+v; the hub passes a nil env and must never see project commands", c)
		}
		sources = append(sources, c.Source)
	}
	sort.Strings(sources)
	if sources[0] != "plugin" || sources[1] != "user" {
		t.Errorf("sources = %v, want [plugin user]", sources)
	}
}
```

(The `Source == "project"` assertion pins §Catalog boundary: project commands can only leak in if someone passes a real env to discovery — Task 3's nil-env test covers the mechanism, this covers the call site.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub && go test . -run 'TestHubCommandList_UserGlobalWithoutPlugins|TestHubCommandList_ShadowedPluginListsBoth' -v`
Expected: FAIL — empty-dirs early return yields 0 commands; no `Source`.

- [ ] **Step 3: Implement**

Replace `hubCommandList` (keep its doc comment, updated):

```go
func hubCommandList(cfg hubcore.WebConfig) (appwire.CommandListResponse, error) {
	dirs := plugins.NewManager(cfg.PluginRoot).EnabledPluginDirs(cfg.PluginDirs)
	loaded, _ := plugin.LoadAllFailSoft(dirs)
	// Nil env: the hub is multi-project, so discovery scans the user-global
	// dir only — project commands are per-session and never appear here.
	serfwide, _ := plugin.DiscoverSerfWideCommands(nil)
	merged := plugin.MergeCommands(loaded, serfwide)
	var commands []appwire.CommandDescriptor
	for _, cmd := range merged {
		commands = append(commands, appwire.CommandDescriptor{
			Name:         cmd.Name,
			PluginName:   cmd.PluginName,
			Description:  cmd.Description,
			ArgumentHint: cmd.ArgumentHint,
			Source:       cmd.Source,
		})
	}
	sort.Slice(commands, func(i, j int) bool {
		if commands[i].Name != commands[j].Name {
			return commands[i].Name < commands[j].Name
		}
		if commands[i].PluginName != commands[j].PluginName {
			return commands[i].PluginName < commands[j].PluginName
		}
		return commands[i].Source < commands[j].Source
	})
	return appwire.CommandListResponse{Commands: commands}, nil
}
```

(The `len(dirs) == 0` early return is gone — `LoadAllFailSoft(nil)` returns nothing and `MergeCommands` still overlays user-global commands.)

- [ ] **Step 4: Run tests**

Run: `cd cmd/serf-hub && go test . -run 'CommandList'`
Expected: PASS, including the existing catalog tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/app_rpc.go cmd/serf-hub/app_command_list_test.go
git commit -m "serf-hub: serve serf-wide commands in serf/command/list"
```

---

### Task 10: Web palette lists catalog commands

**Files:**
- Create: `cmd/serf-hub/frontend/src/stores/commandCatalog.ts` (+ test)
- Modify: `cmd/serf-hub/frontend/src/shell/palette/commands.ts` (`filterCommands`/`commandsInScope`)
- Test: `cmd/serf-hub/frontend/src/shell/palette/commands.test.ts`

**Interfaces:**
- Consumes: `connectionStore.getState().client` with `client.request("serf/command/list", {})` (pattern: `commands.ts:155-158`), `CommandDescriptor` from `../protocol/types.gen` (has `source` after Task 8).
- Produces: `useCommandCatalog` zustand store `{ commands: CommandDescriptor[], loaded: boolean, refresh(): Promise<void> }`; palette items for catalog entries carry `source`/`pluginName` so Task 11 can submit the right invocation.

- [ ] **Step 1: Write the failing store test**

Create `commandCatalog.test.ts` next to the store, using the fake client from `src/protocol/testing/fakeClient.ts`:

```ts
import { expect, test } from "vitest";
import { useCommandCatalog } from "./commandCatalog";
import { connectionStore } from "./connection";
import { FakeClient } from "../protocol/testing/fakeClient";

test("refresh populates catalog entries and tolerates failure", async () => {
  const fake = new FakeClient();
  fake.on("serf/command/list", () => ({
    commands: [
      { name: "review", pluginName: "p", description: "plugin cmd", source: "plugin" },
      { name: "standup", description: "user cmd", source: "user" },
    ],
  }));
  connectionStore.setState({ client: fake as never });
  await useCommandCatalog.getState().refresh();
  expect(useCommandCatalog.getState().commands).toHaveLength(2);
  expect(useCommandCatalog.getState().loaded).toBe(true);

  const failing = new FakeClient();
  failing.on("serf/command/list", () => Promise.reject(new Error("down")));
  connectionStore.setState({ client: failing as never });
  await useCommandCatalog.getState().refresh();
  expect(useCommandCatalog.getState().commands).toEqual([]);
  expect(useCommandCatalog.getState().loaded).toBe(true);
});
```

(Adjust the fake-client API to match `src/protocol/testing/fakeClient.ts`'s actual surface — mirror an existing store test such as `stores/threads.test.ts`'s setup.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/stores/commandCatalog.test.ts`
Expected: FAIL — module does not exist.

- [ ] **Step 3: Implement the store**

Create `src/stores/commandCatalog.ts`:

```ts
import { create } from "zustand";
import { connectionStore } from "./connection";
import type { CommandDescriptor, CommandListResponse } from "../protocol/types.gen";

interface CommandCatalogState {
  commands: CommandDescriptor[];
  loaded: boolean;
  refresh: () => Promise<void>;
}

// Catalog of plugin + user-global slash commands from serf/command/list,
// fetched lazily by the palette. Fetch failure degrades to an empty list —
// the palette's built-ins and the slash fallthrough still work.
export const useCommandCatalog = create<CommandCatalogState>((set) => ({
  commands: [],
  loaded: false,
  refresh: async () => {
    const client = connectionStore.getState().client;
    if (!client) return;
    try {
      const resp = (await client.request("serf/command/list", {})) as CommandListResponse;
      set({ commands: resp.commands ?? [], loaded: true });
    } catch {
      set({ commands: [], loaded: true });
    }
  },
}));
```

- [ ] **Step 4: Wire catalog entries into the palette list**

In `commands.ts`, map catalog entries to palette commands inside `commandsInScope` (or the `filterCommands` assembly): each entry becomes a command whose `id` is the bare `name`, whose title shows a source badge (`[plugin]`/`[user]`), and whose action submits the invocation — **`/pluginName:name` for `source === "plugin"`, `/name` for `source === "user"`** — via the session send path (`threadsStore.getState().send(ctx.sessionRef, text)`, threads.ts:131). Only include entries when `ctx.sessionRef !== null`. Sketch:

```ts
function catalogCommands(ctx: PaletteContext): ScopedCommand[] {
  if (ctx.sessionRef === null) return [];
  return useCommandCatalog.getState().commands.map((cmd) => ({
    id: cmd.name,
    title: `${cmd.name} [${cmd.source ?? "plugin"}]`,
    description: cmd.description ?? "",
    scope: "session" as const,
    slashCommandInvocation:
      cmd.source === "user" ? `/${cmd.name}` : `/${cmd.pluginName}:${cmd.name}`,
  }));
}
```

Add `slashCommandInvocation?: string` to the `Command` type; Task 11's Enter handling consumes it. Call `useCommandCatalog.getState().refresh()` when the palette opens on a session page (the palette-open path in `paletteController`).

- [ ] **Step 5: Test the mapping**

Extend `commands.test.ts`: with a catalog store containing one plugin and one user entry, `filterCommands(ctx, "/rev")` includes the entry titled `review [plugin]` with `slashCommandInvocation === "/p:review"`; with `ctx.sessionRef === null`, catalog entries are absent.

Run: `cd cmd/serf-hub/frontend && npx vitest run src/shell/palette/commands.test.ts src/stores/commandCatalog.test.ts`
Expected: PASS.

- [ ] **Step 6: Biome, gates, commit**

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/stores/commandCatalog.ts src/stores/commandCatalog.test.ts src/shell/palette/commands.ts src/shell/palette/commands.test.ts
cd cmd/serf-hub && make test-web
git add cmd/serf-hub/frontend/src/stores/commandCatalog.ts cmd/serf-hub/frontend/src/stores/commandCatalog.test.ts cmd/serf-hub/frontend/src/shell/palette/
git commit -m "web: list plugin and user slash commands in the palette"
```

---

### Task 11: Web palette slash fallthrough

**Files:**
- Modify: `cmd/serf-hub/frontend/src/shell/palette/CommandPalette.tsx:403-413` (`enterPressed`)
- Modify: `cmd/serf-hub/frontend/src/shell/palette/commands.ts` (Task 10's `slashCommandInvocation`)
- Test: `cmd/serf-hub/frontend/src/shell/palette/CommandPalette.test.tsx` (or the existing palette test file)

**Interfaces:**
- Consumes: `slashCommandInvocation` (Task 10), `threadsStore.getState().send(ref, text)` (threads.ts:131/1868), `ctx.sessionRef`.
- Produces: Enter in command-filter mode — arrow-navigated selection activates the highlighted command; otherwise an exact first-token id match activates that command; otherwise the raw query is sent to the session.

- [ ] **Step 1: Write the failing tests**

```ts
test("Enter on an exact built-in name runs the built-in", () => {
  // query "/status" with built-in "status" present -> activates status, sends nothing
});

test("Enter on a fuzzy near-miss falls through to the session", () => {
  // query "/stat" (no exact id "stat") -> threadsStore.send called with "/stat"
});

test("Enter on an unknown slash command sends the raw query", () => {
  // query "/review main" -> send called with "/review main", palette closes
});

test("selecting a plugin catalog entry submits the qualified form", () => {
  // catalog entry {name: "review", pluginName: "p", source: "plugin"} -> send "/p:review"
});
```

(Use the existing palette test harness; stub `threadsStore.getState().send` and assert calls.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/shell/palette/CommandPalette.test.tsx`
Expected: FAIL — Enter on unmatched query currently no-ops (CommandPalette.tsx:409-411).

- [ ] **Step 3: Implement**

In `enterPressed` (`CommandPalette.tsx`), replace the command-filter branch:

```ts
if (mode === "command-filter") {
  const active = view.items[activeIndex];
  // An explicitly arrow-navigated selection wins.
  if (activeIndex !== 0 && active?.kind === "command") {
    activateCommand(active.command);
    return;
  }
  const firstToken = query.replace(/^\//, "").trim().split(/\s+/)[0] ?? "";
  const exact = view.items.find((it) => it.kind === "command" && it.command.id === firstToken);
  if (exact?.kind === "command") {
    activateCommand(exact.command);
    return;
  }
  // Slash fallthrough: forward the raw text to the session (TUI parity).
  if (firstToken !== "" && sessionRef) {
    void threadsStore.getState().send(sessionRef, query);
    closePalette();
    return;
  }
  return;
}
```

And in the command-activation path, a command carrying `slashCommandInvocation` (Task 10) sends instead of running an action:

```ts
if (command.slashCommandInvocation) {
  const args = query.replace(/^\//, "").trim().slice(command.id.length).trim();
  const text = args ? `${command.slashCommandInvocation} ${args}` : command.slashCommandInvocation;
  void threadsStore.getState().send(sessionRef, text);
  closePalette();
  return;
}
```

(Verify the palette's actual close and activation helpers — `closePalette`, `activateCommand`, `sessionRef` in scope — and adapt names; the test harness pins the behavior.)

- [ ] **Step 4: Run tests**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/shell/palette/`
Expected: PASS, including the pre-existing palette suites.

- [ ] **Step 5: Biome, gates, commit**

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/shell/palette/
cd cmd/serf-hub && make test-web && make test-web-browser   # browser gate on Chrome-capable hosts
git add cmd/serf-hub/frontend/src/shell/palette/
git commit -m "web: forward unmatched slash commands from the palette to the session"
```

---

### Task 12: Discovery fuzz target

**Files:**
- Create: `agent/plugin/serfwide_fuzz_test.go`
- Pattern: `agent/plugin/loader_program_fuzz_test.go` (repo fuzz conventions; register per `make fuzz-registry-check`)

**Interfaces:**
- Consumes: `DiscoverSerfWideCommands` (Task 3-4).

- [ ] **Step 1: Write the fuzz target**

```go
package plugin

import "testing"

// FuzzDiscoverSerfwideFrontmatter fuzzes command-file content and filenames
// through serf-wide discovery: no panics, bare keys only, every rejected
// file produces a warning.
func FuzzDiscoverSerfwideFrontmatter(f *testing.F) {
	f.Add("review", "body")
	f.Add("a b", "---\nmodel: x\n---\n!`ls`")
	f.Add("p:q", "---\n[bad\n---\nx")
	f.Add("", "body")
	f.Fuzz(func(t *testing.T, name, content string) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "serf", "commands")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0644); err != nil {
			t.Skip() // filename not representable on disk
		}
		got, warnings := DiscoverSerfWideCommands(nil)
		for key := range got {
			if strings.ContainsAny(key, ": \t\n") || key == "" {
				t.Fatalf("bad key %q escaped the guards", key)
			}
		}
		if len(got) == 0 && len(warnings) == 0 && name != "" && filepath.Base(name) == name {
			t.Fatalf("valid-looking file %q produced neither command nor warning", name)
		}
	})
}
```

- [ ] **Step 2: Run it briefly**

Run: `cd agent && go test ./plugin/ -run FuzzDiscoverSerfwideFrontmatter -fuzz FuzzDiscoverSerfwideFrontmatter -fuzztime 30s`
Expected: no failures. Register the target in the fuzz registry (`make fuzz-registry-check` tells you where).

- [ ] **Step 3: Commit**

```bash
git add agent/plugin/serfwide_fuzz_test.go
git commit -m "agent/plugin: fuzz serf-wide command discovery"
```

---

### Task 13: Docs and spec status

**Files:**
- Modify: `docs/skills.md:54-58` (availability note)
- Modify: `docs/superpowers/specs/2026-08-04-serf-wide-slash-commands-design.md:4` (status)

- [ ] **Step 1: Update**

In `docs/skills.md`, remove the availability blockquote (the feature now exists) and the "land with the same implementation" clause in Client caveats.

In the spec, change `Status: Approved design, pre-plan` to `Status: Implemented`.

- [ ] **Step 2: Gate and commit**

```bash
make lint-docs
git add docs/skills.md docs/superpowers/specs/2026-08-04-serf-wide-slash-commands-design.md
git commit -m "docs: mark serf-wide slash commands implemented"
```

---

## Final verification

- [ ] `cd agent && go test ./...`
- [ ] `go test ./appwire/ ./cmd/serf-hub/`
- [ ] `make lint-generated && make lint-docs`
- [ ] `cd cmd/serf-hub && make test-web` (plus `make test-web-browser` on Chrome-capable hosts)
- [ ] Manual smoke: `mkdir -p /tmp/x/.serf/commands && printf 'hello $ARGUMENTS' > /tmp/x/.serf/commands/hi.md`, start a session in `/tmp/x`, type `/hi world` → expanded text, no execution; `/hi world` in the web UI via palette fallthrough.
