# SP-B — `manage-plugins` Builtin Skill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a builtin `manage-plugins` skill that teaches the agent how to install, update, remove, and list Claude Code plugins by manipulating the directories SP-A discovers, using only its existing Bash/Read/Write/WebFetch tools.

**Architecture:** Markdown-only sub-project. Drop `agent/skills/manage-plugins/SKILL.md` into the existing `//go:embed all:skills` tree in `agent/builtin_skills.go`. The file is automatically picked up by `extractEmbeddedSkills()` → `scanSkillsDir()` → `DiscoverSkills()`. No new Go code, no new CLI surface. Tests verify the skill is present, parses as YAML-frontmatter + markdown, surfaces via `DiscoverSkills`, and that its description + body sections cover the LLM's invocation surface.

**Tech Stack:** Go 1.22+, `embed.FS`, `primeradiant.com/serf/frontmatter` for parsing, `testing` standard library. No new dependencies.

**Parent specs:**
- `docs/superpowers/specs/2026-05-14-claude-code-compat-design.md`
- `docs/superpowers/specs/2026-05-14-claude-code-compat-spb-manage-plugins-skill-design.md` (source of truth)

**Reference code:**
- `agent/builtin_skills.go` — embed + extraction (no change needed)
- `agent/builtin_skills_test.go` — closest analog; multiple existing tests assert "0 embedded skills" and must be updated
- `agent/skills.go` — `DiscoverSkills`, `SkillMeta`, `scanSkillsDir`, `parseSkillFile`
- `frontmatter/frontmatter.go` — `Parse(raw) (Document, error)`; `Document.Meta` is `map[string]any`, `Document.Body` is string
- `agent/skills_test.go:24` — `writeSkillMD(t, dir, name, content)` helper (project-skill writing; not used here but referenced)
- `agent/project_docs_test.go:67` — `initGitRepo(t, dir)` helper

---

## File Structure

**Create:**
- `agent/skills/manage-plugins/SKILL.md` — the skill content (frontmatter + body, verbatim from sub-spec §2.2)

**Modify:**
- `agent/builtin_skills_test.go` — replace the obsolete "zero embedded skills" assertions with assertions that `manage-plugins` is the (sole, at time of this plan) embedded skill; add new guardrail tests for description completeness and body sections

**Do NOT touch:**
- `agent/builtin_skills.go` — `//go:embed all:skills` already captures any new SKILL.md file dropped into the tree
- `agent/skills.go` — no logic changes
- Any plugin loader, MCP, or hook file

---

## Background Notes for the Engineer

1. **Registration is automatic.** `agent/builtin_skills.go` declares `//go:embed all:skills` and walks the embedded filesystem at runtime. Dropping a new `agent/skills/<name>/SKILL.md` is the entire registration. The sub-spec mentions "one-line registration" as a fallback for hypothetical patterns; the actual codebase pattern is a directory walk, so no Go change is required.

2. **Several existing tests assert "0 embedded skills."** They were written after a prior "ops-task removal" left the embedded tree empty. Adding `manage-plugins` will cause them to fail unless updated. The plan addresses this in Task 4 — do not skip it.

3. **The body content is fully specified.** Use the exact text from sub-spec §2.2 (reproduced in Task 2 below). Do not paraphrase or invent additional sections.

4. **Frontmatter format.** `frontmatter.Parse` expects `---\n<yaml>\n---\n<body>`. `Document.Meta` is `map[string]any`; cast `Meta["name"]` and `Meta["description"]` to `string`.

5. **Skill name is unnamespaced.** Builtin skills register under the bare name from frontmatter (`manage-plugins`), not under `builtin:manage-plugins`. Plugin-supplied skills get namespaced separately by plugin loaders elsewhere; that's not in scope here.

---

## Task 1: Failing registration test

**Files:**
- Modify: `agent/builtin_skills_test.go`

Add a new test that asserts the embedded skills tree contains `manage-plugins` and that it's discoverable via the same path production code uses (`extractEmbeddedSkills` → `DiscoverSkills` with the extracted dir as an `extraDirs` argument). This will fail because `agent/skills/manage-plugins/SKILL.md` does not exist yet.

- [ ] **Step 1: Write the failing test**

Append to `agent/builtin_skills_test.go`:

```go
func TestManagePluginsSkill_Registered(t *testing.T) {
	dir, err := extractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	skills := make(map[string]SkillMeta)
	scanSkillsDir(dir, skills)

	meta, ok := skills["manage-plugins"]
	if !ok {
		t.Fatalf("manage-plugins skill not registered; found: %v", builtinSkillNames(skills))
	}
	if strings.TrimSpace(meta.Description) == "" {
		t.Error("manage-plugins description is empty")
	}
}

func TestManagePluginsSkill_DiscoverableViaDiscoverSkills(t *testing.T) {
	dir, err := extractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	root := t.TempDir()
	initGitRepo(t, root)

	env := NewLocalExecutionEnvironment(root)
	skills := DiscoverSkills(env, dir)

	if _, ok := skills["manage-plugins"]; !ok {
		t.Fatalf("DiscoverSkills missing manage-plugins; got: %v", builtinSkillNames(skills))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/ -run TestManagePluginsSkill -v`

Expected: both tests FAIL. `TestManagePluginsSkill_Registered` fails with `manage-plugins skill not registered; found: []`. `TestManagePluginsSkill_DiscoverableViaDiscoverSkills` fails with `DiscoverSkills missing manage-plugins; got: []`.

- [ ] **Step 3: Commit the failing test**

```bash
git add agent/builtin_skills_test.go
git commit -m "test: failing registration test for manage-plugins skill"
```

---

## Task 2: Add the SKILL.md file (make Task 1 pass)

**Files:**
- Create: `agent/skills/manage-plugins/SKILL.md`

Write the SKILL.md file with content drawn verbatim from sub-spec §2.1 (frontmatter) and §2.2 (body).

- [ ] **Step 1: Create the directory and the SKILL.md file**

Create `agent/skills/manage-plugins/SKILL.md` with this exact content:

```markdown
---
name: manage-plugins
description: Use when the user asks to install, update, remove, list, or inspect Claude Code plugins for serf. Handles marketplaces (cloning marketplace.json catalogs), plugin sources (github/url/git-subdir/directory/npm), and stages plugin directories into ~/.config/serf/plugins/ (user) or <project>/.serf/plugins/ (project).
---

## What this skill does

Stage plugin directories under serf's known plugin paths so SP-A's filesystem
discovery picks them up at next session start. There is no registry; presence
in the right directory IS the install state.

## Plugin storage layout

User-scope (default):  ~/.config/serf/plugins/<plugin-name>/
Project-scope:         <project-root>/.serf/plugins/<plugin-name>/

Each <plugin-name>/ must contain .claude-plugin/plugin.json. Match the plugin's
declared `name` in plugin.json to the directory basename for clarity, though
SP-A resolves by manifest `name` regardless.

## When you should invoke this skill

The user asks any of:
  install plugin X
  install <github-shorthand>            (e.g. anthropics/skills)
  install from marketplace Y
  remove plugin X
  uninstall plugin X
  update plugin X
  update all plugins
  list installed plugins
  show plugin sources

## Step-by-step: install a plugin from a marketplace

1. Resolve the marketplace source. Ask the user for one of:
   - A marketplace name they've already cloned (find it under ~/.config/serf/plugins/marketplaces/)
   - A git URL
   - A `owner/repo` GitHub shorthand
   - A path to a local marketplace.json
2. Fetch marketplace.json:
   - For a git URL or GitHub shorthand: run `git clone --depth=1 <url> /tmp/serf-marketplace-XXXX` then read .claude-plugin/marketplace.json
   - For a direct URL to marketplace.json: WebFetch it
   - For a local path: Read it
3. Parse marketplace.json. Find the plugin entry whose `name` matches what the user asked for. Show the user the entry's `description`, `source`, and confirm.
4. Resolve the plugin source per its `source` type:
   - "directory": `cp -R <marketplace-root>/<relative-path> <target>`
   - "github": `git clone https://github.com/<repo>.git --depth=1 <target>` (then `git checkout <ref/sha>` if pinned)
   - "url": `git clone <url> <target>` (with `--depth=1` if no `sha` pin)
   - "git-subdir": git clone --depth=1, then `cp -R <subdir> <target>`, then rm clone
   - "npm": `npm pack <package>` then unpack; or `npm install <package>` and find the install path. Less common; ask if the user wants to proceed.
5. Pick the target directory:
   - Default: ~/.config/serf/plugins/<plugin-name>/
   - If the user said "for this project" or you detect the user is in a project: <project>/.serf/plugins/<plugin-name>/
6. Verify <target>/.claude-plugin/plugin.json exists. If not, abort with a clear error.
7. Tell the user: "Installed <name> at <target>. Restart your serf session for the plugin to load."

## Step-by-step: update a plugin

1. Find the plugin's directory under ~/.config/serf/plugins/ or .serf/plugins/.
2. If it is a git checkout, run `git pull --ff-only` in that directory. Report the result.
3. If it is not a git checkout, ask the user where to re-fetch from (probably the original marketplace), then do the install flow into the same target.
4. Verify the manifest still parses. Tell the user to restart serf.

## Step-by-step: remove a plugin

1. Confirm the user wants to remove plugin <X>.
2. Find its directory under known plugin paths.
3. `rm -rf <dir>` (or move to a backup with `mv` if the user prefers).
4. Tell the user to restart serf.

## Step-by-step: list installed plugins

1. Read ~/.config/serf/plugins/*/ and <project>/.serf/plugins/*/.
2. For each, read its .claude-plugin/plugin.json. Print name, version, description.
3. Note which scope each is in (user / project).
4. Surface any directories that lack a manifest as "broken (no manifest)".

## Safety reminders

- Never `git clone` from a URL the user hasn't seen or approved.
- Marketplaces are arbitrary code repositories. The user should treat installing a plugin like installing any third-party package — read the source if it's untrusted.
- When updating with `git pull`, prefer `--ff-only` so you don't accidentally pull divergent history.
- When removing, double-check the directory path before `rm -rf`.

## What this skill does NOT do

- Maintain a registry of installed plugins (no installed_plugins.json).
- Resolve version constraints from `dependencies` (the field is warn-on-unsupported).
- Persist marketplace state in a known_marketplaces.json (you re-clone if needed).
- Auto-update on a schedule (the user invokes you).
- Enforce permission patterns (deferred).
```

- [ ] **Step 2: Run the Task 1 tests to confirm they pass**

Run: `go test ./agent/ -run TestManagePluginsSkill -v`

Expected: both `TestManagePluginsSkill_Registered` and `TestManagePluginsSkill_DiscoverableViaDiscoverSkills` PASS.

- [ ] **Step 3: Commit the skill file**

```bash
git add agent/skills/manage-plugins/SKILL.md
git commit -m "feat(skills): add manage-plugins builtin skill"
```

---

## Task 3: Update legacy "zero embedded skills" tests

**Files:**
- Modify: `agent/builtin_skills_test.go`

After Task 2 the embedded skill catalog is no longer empty. Four existing tests assert `len(skills) == 0` and will now fail. Update each to reflect the new reality: the catalog contains exactly `manage-plugins`.

- [ ] **Step 1: Run the full suite to see the failures**

Run: `go test ./agent/ -run "TestExtractEmbeddedSkills|TestEmbeddedSkills_InSystemPrompt" -v`

Expected FAILS (these are the legacy tests that asserted zero embedded skills):
- `TestExtractEmbeddedSkills_EmptyAfterOpsTaskRemoval` — `expected 0 embedded skills, got 1`
- `TestExtractEmbeddedSkills_DiscoverableByDiscoverSkills` — `expected 0 skills from empty embedded dir, got 1: [manage-plugins]`
- `TestEmbeddedSkills_AllSkillsLoadable` — `expected 0 embedded skills, got 1`
- `TestEmbeddedSkills_InSystemPrompt` — `system prompt should not contain <skill-catalog> when no skills exist` (now it should contain it)

- [ ] **Step 2: Update `TestExtractEmbeddedSkills_EmptyAfterOpsTaskRemoval`**

Rename to reflect new intent and update expectations. Replace the existing function body with:

```go
func TestExtractEmbeddedSkills_ContainsManagePlugins(t *testing.T) {
	dir, err := extractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	skills := make(map[string]SkillMeta)
	scanSkillsDir(dir, skills)
	if _, ok := skills["manage-plugins"]; !ok {
		t.Fatalf("expected manage-plugins in embedded skills, got: %v", builtinSkillNames(skills))
	}
}
```

- [ ] **Step 3: Update `TestExtractEmbeddedSkills_DiscoverableByDiscoverSkills`**

Replace its body with:

```go
func TestExtractEmbeddedSkills_DiscoverableByDiscoverSkills(t *testing.T) {
	dir, err := extractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	root := t.TempDir()
	initGitRepo(t, root)

	env := NewLocalExecutionEnvironment(root)
	skills := DiscoverSkills(env, dir)

	if _, ok := skills["manage-plugins"]; !ok {
		t.Fatalf("expected DiscoverSkills to surface manage-plugins; got: %v", builtinSkillNames(skills))
	}
}
```

- [ ] **Step 4: Update `TestEmbeddedSkills_AllSkillsLoadable`**

Replace its body with one that verifies every embedded skill loads its body successfully (currently `manage-plugins`, future-proof for additions):

```go
func TestEmbeddedSkills_AllSkillsLoadable(t *testing.T) {
	dir, err := extractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	skills := make(map[string]SkillMeta)
	scanSkillsDir(dir, skills)

	if len(skills) == 0 {
		t.Fatal("expected at least one embedded skill, got 0")
	}
	for name, meta := range skills {
		body, err := LoadSkillBody(meta)
		if err != nil {
			t.Errorf("LoadSkillBody(%s): %v", name, err)
			continue
		}
		if strings.TrimSpace(body) == "" {
			t.Errorf("skill %s has empty body", name)
		}
	}
}
```

- [ ] **Step 5: Update `TestEmbeddedSkills_InSystemPrompt`**

This test previously asserted the system prompt does NOT contain `<skill-catalog>` when no skills exist. With `manage-plugins` embedded, it will. Replace its body with:

```go
func TestEmbeddedSkills_InSystemPrompt(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	c := llm.NewClient()
	comm := communicateCall("c1", "done")

	var capturedSystem string
	f := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if len(req.Messages) > 0 && req.Messages[0].Role == llm.RoleSystem {
					capturedSystem = req.Messages[0].Text()
				}
				return toolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewAnthropicProfile("claude-test"), NewLocalExecutionEnvironment(root), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = sess.ProcessInput(ctx, "hi", nil)
	sess.Close()

	// The embedded manage-plugins skill should now appear in the catalog.
	if !strings.Contains(capturedSystem, "manage-plugins") {
		t.Error("system prompt should reference manage-plugins skill")
	}
}
```

- [ ] **Step 6: Run all updated tests to verify they pass**

Run: `go test ./agent/ -run "TestExtractEmbeddedSkills|TestEmbeddedSkills" -v`

Expected: all tests PASS, including `TestExtractEmbeddedSkills_ContainsManagePlugins`, `TestExtractEmbeddedSkills_DiscoverableByDiscoverSkills`, `TestEmbeddedSkills_AllSkillsLoadable`, `TestEmbeddedSkills_InSystemPrompt`, and the unchanged `TestExtractEmbeddedSkills_CreatesDir`, `TestExtractEmbeddedSkills_FilesystemShadowsEmbedded`, `TestEmbeddedSkills_ProjectShadowsEmbedded`, `TestEmbeddedSkills_UseSkillWithProjectSkill`, `TestEmbeddedSkills_UseSkillUnknownReturnsError`.

- [ ] **Step 7: Commit**

```bash
git add agent/builtin_skills_test.go
git commit -m "test: update embedded-skill tests for manage-plugins"
```

---

## Task 4: Guardrail tests — description completeness and body sections

**Files:**
- Modify: `agent/builtin_skills_test.go`

The sub-spec §7 specifies two guardrails (tests #4 and #5) that protect the LLM's invocation surface from silent regressions during future edits to SKILL.md. Add them as standalone tests.

- [ ] **Step 1: Write the failing guardrail tests**

Append to `agent/builtin_skills_test.go`:

```go
func TestManagePluginsSkill_DescriptionCoversTriggeringVerbs(t *testing.T) {
	dir, err := extractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	skills := make(map[string]SkillMeta)
	scanSkillsDir(dir, skills)
	meta, ok := skills["manage-plugins"]
	if !ok {
		t.Fatalf("manage-plugins not present")
	}
	desc := strings.ToLower(meta.Description)
	// Sub-spec §7 test #4: every triggering verb/noun must appear so the LLM
	// matches the skill against common user phrasing.
	for _, want := range []string{"install", "update", "remove", "marketplace", "list", "plugin"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing %q (LLM match surface); description=%q", want, desc)
		}
	}
}

func TestManagePluginsSkill_BodyHasStepByStepSections(t *testing.T) {
	dir, err := extractEmbeddedSkills()
	if err != nil {
		t.Fatalf("extractEmbeddedSkills: %v", err)
	}
	defer os.RemoveAll(dir)

	skills := make(map[string]SkillMeta)
	scanSkillsDir(dir, skills)
	meta, ok := skills["manage-plugins"]
	if !ok {
		t.Fatalf("manage-plugins not present")
	}
	body, err := LoadSkillBody(meta)
	if err != nil {
		t.Fatalf("LoadSkillBody: %v", err)
	}
	// Sub-spec §7 test #5: each "install/update/remove/list" intent must
	// have a corresponding "Step-by-step:" section so the agent can find
	// the procedure.
	for _, want := range []string{
		"Step-by-step: install a plugin",
		"Step-by-step: update a plugin",
		"Step-by-step: remove a plugin",
		"Step-by-step: list installed plugins",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing section heading %q", want)
		}
	}
}
```

- [ ] **Step 2: Run the new tests**

Run: `go test ./agent/ -run "TestManagePluginsSkill_DescriptionCoversTriggeringVerbs|TestManagePluginsSkill_BodyHasStepByStepSections" -v`

Expected: both PASS. The frontmatter description from Task 2 contains "install, update, remove, list, marketplace, plugin" (all lowercase substrings of the spec text), and the body contains all four `Step-by-step:` headings verbatim.

- [ ] **Step 3: Commit**

```bash
git add agent/builtin_skills_test.go
git commit -m "test: guardrail tests for manage-plugins description and sections"
```

---

## Task 5: Full-suite verification

**Files:**
- None (verification only)

Confirm nothing else broke. The `agent` package owns the embed and discovery paths; a full package test catches any test elsewhere that incidentally counted embedded skills (none expected, but verify).

- [ ] **Step 1: Run the full `agent` package test suite**

Run: `go test ./agent/ -count=1`

Expected: PASS (`ok  primeradiant.com/serf/agent  …`). No failures, no skipped tests, no panics.

- [ ] **Step 2: Run the full repo test suite as a sanity check**

Run: `go test ./...`

Expected: all packages PASS. If a downstream package test happened to count embedded skills, fix it the same way Task 3 fixed the agent-package tests (replace the count with a presence check). The sub-spec scoped SP-B to `agent/`, so failures here would indicate an undocumented dependency — flag it to Jesse rather than silently changing other tests.

- [ ] **Step 3: Verify the file is in git and the embed actually picked it up**

Run: `git ls-files agent/skills/manage-plugins/` and `go run ./cmd/serf -h 2>&1 | head -5` (the second command just ensures the binary still builds with the embed change).

Expected: `agent/skills/manage-plugins/SKILL.md` appears in `git ls-files` output; serf binary builds and prints its help banner.

- [ ] **Step 4: Final commit if anything was needed**

If steps 1–3 required no code changes (the expected outcome), there is nothing to commit. If step 2 surfaced a downstream test, commit its fix with a message describing the package and the change.

---

## Self-Review Notes

Reviewed against `2026-05-14-claude-code-compat-spb-manage-plugins-skill-design.md`:

- §1 (Goal): covered by Tasks 1–2 (skill file + auto-registration via embed).
- §2.1 (Frontmatter): verbatim in Task 2.
- §2.2 (Body outline): verbatim in Task 2.
- §3 (Registration): no Go change needed because `//go:embed all:skills` already covers the new file; documented in "Background Notes."
- §4 (Validation): `frontmatter.Parse` + non-empty `name`/`description` enforced by `parseSkillFile`, exercised by Task 1's `TestManagePluginsSkill_Registered` (asserts non-empty description) and Task 3's updated `TestEmbeddedSkills_AllSkillsLoadable` (asserts every embedded skill's body loads).
- §5 (Error contracts): purely content; covered by the SKILL.md body text in Task 2 (the "Safety reminders" and "Step-by-step" sections specify the contracts).
- §6 (Package/file layout): Task 2 creates the SKILL.md; Task 3 updates `builtin_skills_test.go` per §6's "extend to verify `manage-plugins` is registered." No `agent/builtin_skills.go` change required because the codebase already uses the embed-walk pattern hinted at in §3.
- §7 testing strategy table:
  - #1 registration → Task 1 `TestManagePluginsSkill_Registered`
  - #2 frontmatter parses → exercised implicitly by `scanSkillsDir`/`parseSkillFile` in Task 1; reinforced by `LoadSkillBody` call in updated `TestEmbeddedSkills_AllSkillsLoadable` (Task 3)
  - #3 shadowing → already covered by existing `TestEmbeddedSkills_ProjectShadowsEmbedded` (untouched); no plan task needed
  - #4 description completeness → Task 4 `TestManagePluginsSkill_DescriptionCoversTriggeringVerbs`
  - #5 body procedure links → Task 4 `TestManagePluginsSkill_BodyHasStepByStepSections`
- §8 (Open questions): all four are recommendations to defer or hand off to SP6/SP7; no implementation impact in SP-B.

No placeholders. No invented identifiers (`extractEmbeddedSkills`, `scanSkillsDir`, `DiscoverSkills`, `LoadSkillBody`, `SkillMeta`, `parseSkillFile`, `builtinSkillNames`, `initGitRepo`, `NewLocalExecutionEnvironment`, `NewAnthropicProfile`, `NewSession`, `SessionConfig`, `communicateCall`, `toolCallResponse`, `fakeAdapter`, `llm.NewClient`, `llm.Request`, `llm.Response`, `llm.RoleSystem` — all verified to exist in the current codebase via direct read of `agent/builtin_skills.go`, `agent/builtin_skills_test.go`, `agent/skills.go`, `agent/project_docs_test.go`, and `agent/skills_test.go`).

Type consistency: `SkillMeta` fields (`Name`, `Description`, `Dir`, `SkillFile`, `AllowedTools`) referenced consistently. `scanSkillsDir(dir string, out map[string]SkillMeta)` and `DiscoverSkills(env ExecutionEnvironment, extraDirs ...string) map[string]SkillMeta` signatures match `agent/skills.go`. Test helper `builtinSkillNames(skills map[string]SkillMeta) []string` is defined at the bottom of the existing `builtin_skills_test.go` and used as-is.
