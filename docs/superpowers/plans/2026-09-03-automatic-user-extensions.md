# Automatic User Extensions and Shared Config Paths Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Automatically discover the user's config-root skills and commands while consolidating Evener's repeated XDG config-path resolution into one low-level helper.

**Architecture:** Add a dependency-light `internal/userdirs` package that owns the `$XDG_CONFIG_HOME` → `~/.config` resolution and derives the Evener config root and extension paths. Existing higher-level packages keep their public APIs and test seams, delegating path construction to this helper. Session initialization adds the default user skills directory as a lower-priority automatic source, while the already-automatic user-global command discovery remains enabled and gains regression coverage.

**Tech Stack:** Go, `os.UserHomeDir`, `envvars.XDGConfigHome`, `filepath`, standard `go test`.

**Spec:** `docs/skills.md` (skill and slash-command discovery/activation contract), plus the approved design in the conversation: automatic user skills, automatic user-global commands, and one shared config-path resolver.

## Global Constraints

- Preserve `$XDG_CONFIG_HOME` precedence and the `~/.config` fallback.
- Preserve existing shadowing order: embedded skills, filesystem project skills, then configured extra skill directories; the automatic user skills directory must not override an explicitly configured directory.
- Preserve existing no-home error behavior in strict low-level consumers (commands, prompts, MCP, and plugin store); preserve `cmdutil.DefaultConfigRoot`'s existing `.` fallback.
- Keep project and automatically discovered user-wide command content inert: no shell execution or file inclusion.
- Default tests must remain deterministic and must not use provider credentials, network access, or ambient developer state.
- Do not refactor unrelated state-home, cache-home, Google ADC, or sandbox security paths.
- Run `gofmt` on touched Go files and `npx biome check --write` only if frontend files are touched; no frontend files are expected.

---

### Task 1: Create the shared config-path resolver

**Files:**
- Create: `internal/userdirs/userdirs.go`
- Create: `internal/userdirs/userdirs_test.go`
- Modify: `cmdutil/userdirs.go`
- Test: `cmdutil/userdirs_test.go`

**Interfaces:**
- Consumes: `envvars.XDGConfigHome` and a caller-supplied `func() (string, error)` home-directory lookup.
- Produces: `userdirs.ConfigRoot(xdgConfigHome string, userHomeDir func() (string, error)) string`, returning an empty string when neither XDG_CONFIG_HOME nor the home lookup can provide a base; `userdirs.Subdir(root, name string) string`, returning an empty string for an empty root; and `userdirs.DefaultConfigRoot() string`, using the process environment and `os.UserHomeDir`.

- [ ] **Step 1: Write the failing shared-helper tests**

Add table-driven tests in `internal/userdirs/userdirs_test.go` covering:

```go
func TestConfigRoot(t *testing.T) {
    tests := []struct {
        name string
        xdg  string
        home string
        err  error
        want string
    }{
        {name: "xdg wins", xdg: "/xdg", home: "/home", want: "/xdg/evener"},
        {name: "home fallback", home: "/home", want: "/home/.config/evener"},
        {name: "home lookup failure", err: os.ErrNotExist, want: ""},
    }
    // Call ConfigRoot(tc.xdg, func() (string, error) { return tc.home, tc.err })
    // and assert tc.want for every row.
}

func TestSubdir(t *testing.T) {
    if got := Subdir("/cfg/evener", "skills"); got != "/cfg/evener/skills" { t.Fatal(got) }
    if got := Subdir("", "skills"); got != "" { t.Fatal(got) }
}
```

Also cover `DefaultConfigRoot` under a temporary `XDG_CONFIG_HOME` using `t.Setenv`.

- [ ] **Step 2: Run the focused tests and verify they fail**

Run:

```bash
go test ./internal/userdirs ./cmdutil -run 'Test(ConfigRoot|Subdir|UserConfigDirs)' -count=1
```

Expected: FAIL because `internal/userdirs` and its functions do not exist yet.

- [ ] **Step 3: Implement the minimal shared helper and delegate cmdutil**

Implement the exact interfaces above. `ConfigRoot` must use the supplied XDG value when non-empty, otherwise call the supplied home function and append `.config`; on home lookup error it returns `""`. `Subdir` must guard the empty-root case. `DefaultConfigRoot` calls `ConfigRoot(envvars.XDGConfigHome.Getenv(), os.UserHomeDir)`.

Update `cmdutil.DefaultConfigRoot` to use `userdirs.DefaultConfigRoot`, but retain its current fallback when that returns empty:

```go
if root := userdirs.DefaultConfigRoot(); root != "" {
    return root
}
return filepath.Join(".", ".config", "evener")
```

Keep `DefaultSkillsDir` and `DefaultPluginsRoot` derived from `DefaultConfigRoot` so their existing `.` fallback remains unchanged.

- [ ] **Step 4: Run the focused tests and verify they pass**

Run:

```bash
go test ./internal/userdirs ./cmdutil -run 'Test(ConfigRoot|Subdir|UserConfigDirs)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the shared helper**

```bash
git add internal/userdirs/userdirs.go internal/userdirs/userdirs_test.go cmdutil/userdirs.go cmdutil/userdirs_test.go
git commit -m "refactor: centralize config root path resolution"
```

---

### Task 2: Migrate config-owned consumers and cover XDG behavior

**Files:**
- Modify: `agent/plugin/evenerwide.go`
- Modify: `agent/internal/promptpath/prompt_paths.go`
- Modify: `agent/mcpconfig/config.go`
- Modify: `cmd/evener-hub/web_settings.go`
- Modify: `internal/plugins/paths.go`
- Modify: `llm/registry/load.go`
- Test: `agent/plugin/evenerwide_test.go`
- Create: `agent/internal/promptpath/prompt_paths_test.go`
- Test: `agent/mcpconfig/config_test.go`
- Test: `internal/plugins/paths_test.go`
- Test: `llm/registry/*_test.go` where existing path tests live

**Interfaces:**
- Consumes: `userdirs.ConfigRoot` and `userdirs.Subdir` from Task 1; each existing package's injected home function remains the function passed to the shared resolver.
- Produces: identical paths and error/fallback behavior at all existing call sites, with no second implementation of the XDG/home config-base dance.

- [ ] **Step 1: Add or strengthen failing consumer tests for XDG and fallback paths**

For each existing package test, assert both `$XDG_CONFIG_HOME/evener/<child>` and the home fallback where that package already has a path helper. In particular, add a user-global command fixture under a temporary XDG root and assert `DiscoverEvenerWideCommands(nil)` returns its filename-derived command; keep a home-lookup-error case for strict consumers returning an empty path. Do not use the real home directory.

- [ ] **Step 2: Run the consumer tests before migration**

Run:

```bash
go test ./agent/plugin ./agent/internal/promptpath ./agent/mcpconfig ./internal/plugins ./llm/registry -count=1
```

Expected: PASS on the current behavior; these tests establish the pre-refactor contract and the command-directory automatic behavior.

- [ ] **Step 3: Replace duplicated path construction with the shared helper**

Use `userdirs.ConfigRoot(envvars.XDGConfigHome.Getenv(), existingInjectedHomeFunc)` and `userdirs.Subdir` for commands, prompts, MCP, and plugin paths. The command resolver must still return an empty path when the shared root is empty. The hub's `defaultMCPConfigPath` must derive the same `mcp.json` path instead of maintaining a second implementation. In `llm/registry`, replace only `defaultConfigRoot`; leave its provider-config environment tri-state and state-root logic unchanged.

- [ ] **Step 4: Run consumer tests and verify behavior is unchanged**

Run:

```bash
gofmt -w agent/plugin/evenerwide.go agent/internal/promptpath/prompt_paths.go agent/mcpconfig/config.go cmd/evener-hub/web_settings.go internal/plugins/paths.go llm/registry/load.go
go test ./agent/plugin ./agent/internal/promptpath ./agent/mcpconfig ./internal/plugins ./llm/registry -count=1
```

Expected: PASS, including XDG and fallback/error cases.

- [ ] **Step 5: Commit the consumer migration**

```bash
git add agent/plugin/evenerwide.go agent/plugin/evenerwide_test.go agent/internal/promptpath/prompt_paths.go agent/internal/promptpath/prompt_paths_test.go agent/mcpconfig/config.go agent/mcpconfig/config_test.go cmd/evener-hub/web_settings.go internal/plugins/paths.go internal/plugins/paths_test.go llm/registry/load.go llm/registry/*_test.go
git commit -m "refactor: share config paths across consumers"
```

---

### Task 3: Automatically enable user skills and document extension discovery

**Files:**
- Modify: `agent/session_init.go`
- Modify: `agent/skill/skills.go`
- Test: `agent/session_skills_test.go`
- Test: `agent/skill/skills_test.go`
- Modify: `README.md`
- Modify: `docs/skills.md`

**Interfaces:**
- Consumes: `userdirs.DefaultConfigRoot`/`userdirs.Subdir` from Task 1 and existing `SessionConfig.SkillsDirs`.
- Produces: every new session scans the default user skills directory automatically; explicitly configured `SkillsDirs` remain additional directories with their existing precedence; exact `/skill-name` activation continues through `expandSlashCommand`.

- [ ] **Step 1: Write failing automatic-discovery and slash-activation tests**

Add a deterministic session-level test that sets `XDG_CONFIG_HOME` to `t.TempDir()`, writes `<xdg>/evener/skills/auto/SKILL.md` with valid `name`/`description` frontmatter and a sentinel body, creates a session in an unrelated temporary working directory, and proves the session discovers the skill and expands `/auto` to the sentinel body. Use the existing scripted adapter/session helpers; do not contact a provider.

Add a precedence assertion showing an explicitly configured `SkillsDirs` entry shadows the automatic user directory when both define the same bare skill name.

- [ ] **Step 2: Run the new focused tests and verify they fail**

Run:

```bash
go test ./agent ./agent/skill -run 'Test(.*Automatic.*Skill|.*User.*Skill|.*Slash.*Skill)' -count=1
```

Expected: FAIL because the default user skills directory is not currently included in `NewSession` discovery.

- [ ] **Step 3: Add the automatic user skills directory to session discovery**

In the skill-loading block of `agent/session_init.go`, construct the extra directory list with the automatic user skills directory first and `s.cfg.SkillsDirs` after it. This preserves the existing rule that configured extra directories shadow earlier entries, while project skills continue to shadow embedded skills but remain below configured extras. Skip an empty default path so a failed home lookup cannot accidentally scan a relative `skills` directory.

Update `DiscoverSkills` to ignore empty extra-directory entries, making this invariant explicit and safe for all callers.

Do not change `expandSlashCommand`: once the skill is in `s.skills`, its existing exact `/name` resolution and activation event are the required behavior.

- [ ] **Step 4: Run focused tests and verify they pass**

Run:

```bash
gofmt -w agent/session_init.go agent/skill/skills.go agent/session_skills_test.go agent/skill/skills_test.go
go test ./agent ./agent/skill -run 'Test(.*Automatic.*Skill|.*User.*Skill|.*Slash.*Skill)' -count=1
```

Expected: PASS, including automatic discovery, explicit-directory precedence, and slash activation.

- [ ] **Step 5: Update user-facing documentation**

Change `README.md` so the standard user skills and commands directories are described as automatically discovered, while `skills_dirs` remains available for additional skill roots. Update `docs/skills.md` to state:

- skills are discovered from the bundled layer, project `skills/`, the automatic user skills directory, configured `skills_dirs`, and plugins;
- user-global commands in `$XDG_CONFIG_HOME/evener/commands` (or `~/.config/evener/commands`) are automatically discovered and cataloged;
- standalone skills are listed in the model skill catalog and exact `/skill-name` input activates them, but they are not command-file entries in the command catalog.

- [ ] **Step 6: Commit the automatic extension behavior**

```bash
git add agent/session_init.go agent/skill/skills.go agent/session_skills_test.go agent/skill/skills_test.go README.md docs/skills.md
git commit -m "feat: automatically load user skills"
```

---

### Task 4: Review the full diff and run verification gates

**Files:**
- Review all files changed by Tasks 1–3; do not add unrelated changes.

**Interfaces:**
- Consumes: the committed implementation from Tasks 1–3.
- Produces: verified automatic user skills, automatic XDG-aware user-global commands, and a clean isolated worktree with no scratch artifacts.

- [ ] **Step 1: Review the diff and repository status**

Run:

```bash
git diff main...HEAD --stat
git diff main...HEAD --check
git status --short
```

Expected: only the planned shared-helper, consumer, session, test, and documentation files are changed; `git diff --check` is clean; no scratch files are present.

- [ ] **Step 2: Run focused package tests**

Run:

```bash
go test ./internal/userdirs ./cmdutil ./agent ./agent/skill ./agent/plugin ./agent/internal/promptpath ./agent/mcpconfig ./internal/plugins ./llm/registry ./cmd/evener-hub -count=1
```

Expected: PASS.

- [ ] **Step 3: Run repository verification gates**

Run:

```bash
make vet
make test
```

Expected: both commands exit 0. If an environment prerequisite prevents a gate from running, report the exact command and failure rather than treating it as passed.

- [ ] **Step 4: Run lint and report final worktree state**

Run:

```bash
make lint
git status --short
```

Expected: `make lint` exits 0 and the worktree contains only the committed plan/implementation history with no untracked artifacts.
