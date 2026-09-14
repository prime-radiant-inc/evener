# Skills Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver reliable skill activation, explicit client selection, invocation controls, portable discovery, and agent-selected disk reload across compaction and restart.

**Architecture:** `agent/skill` supplies one full catalog, validated loader, and inert renderer. Sessions own provenance, authorization, complete-content admission, and generation-owned compaction state using existing snapshots and fold publication. AppWire carries canonical request selections; the composer preserves them with the request throughout its lifecycle.

**Tech Stack:** Go 1.27 workspace; existing YAML frontmatter parser, session schema/JSONL persistence, AppWire generators; TypeScript, React, Vitest, Biome, existing real-browser guards. No new runtime dependency.

**Spec:** [Approved skills lifecycle design](../specs/2026-09-10-skills-lifecycle-design.md), approved by Jesse after commit `61934ec1f84c9734509f9c1cb65712e4921128e2`. The specification's status line predates that approval.

## Global Constraints

The following requirements apply to every task:

- "Existing sessions track future ordinary activations only. Do not reconstruct historical activations from old tool calls or transcript prose."
- "Role preloads keep their permanent-prompt and frozen-delegate lifetime."
- "Guaranteed inline invocation comes from explicit selection. Plain typed inline slash mentions keep their current model-interpreted behavior."
- "Keep the recorded source identity: do not silently retarget a skill to a different collision winner. A different source requires fresh activation."
- "Store operation metadata, not skill bodies, and avoid writes on unchanged pressure checks or fold retries."
- "Use the existing generation-checked fold publication transaction."
- "Use TDD with a scripted provider only at the LLM boundary and real Evener behavior below it."
- "Assert structured state, events, wire inputs, request shapes, and opaque fixture-data delivery. Do not pin natural-language prompt or warning prose."
- "Default tests require neither credentials nor network."
- "Do not add automatic `.claude/skills/` discovery, new project-trust UI, executable templates, shell substitution, or implicit permission grants."
- "Commit only scoped paths, push the isolated branch, and open a PR containing the study, approved spec, implementation, tests, documentation, and actual evidence."

Work only in the existing `wip/skills-implementation-study` worktree. Leave `/home/jesse/git/prime-radiant-inc/evener` untouched. No branch merge is requested. Preserve existing unrelated changes; stop if new overlapping changes appear. Stage named paths only. Do not add compatibility emulation, additional stores, or new terminal completion UI.

Read `docs/developing-evener/testing.md` before changing tests and `docs/developing-evener/README.md` before gates. Existing `fakeAdapter` is a scripted **external LLM boundary**, permitted by the approved spec; do not mock loaders, sessions, persistence, filesystem operations, compaction, AppWire, or composer state machines. Use temporary real files, actual saved sessions, provider-request captures, typed events, and deterministic barriers. Never use sleeps as synchronization or change assertion references to make a failure pass.

All new APIs below are **proposed implementation interfaces**, not claims that they already exist. Existing APIs are marked **existing**. Keep the named interfaces consistent between tasks. Source line numbers are orientation at the approved base; use symbols when edits move lines. Add imports with the normal formatter. Red/green commands are instructions for execution, not test results from planning.

## Delivery structure and file ownership

These are four dependent stages inside one PR. Each stage must pass its gate before the next stage begins. Documentation belongs to the task that introduces the behavior. Do not parallelize overlapping session edits; use isolated native worktrees if a writing delegate overlaps another writer.

| Stage | Responsibility | Primary files |
|---|---|---|
| Skill package | Metadata, exact-source loading, rendering, discovery and advertisements | `agent/skill/metadata.go`, `load.go`, `discovery.go`; existing `skills.go`, `builtin_skills.go`; `agent/plugin/plugin.go`, `agent/session_init.go`, `agent/session_prompts.go`, `agent/status.go`, `cmd/evener-hub/app_threadread.go` |
| Session activation | Durable ordinary/preload identity, authorization, route integration, complete admission, dispatch revalidation | New `agent/schema/skill_lifecycle.go`, `agent/session_skill_activation.go`, `agent/session_skill_delivery.go`; existing schema/snapshot, route, model-call and delegate files identified in the tasks |
| Compaction | Selection parsing, durable owning operation, generation-safe fold handoff, reload and reminder | New `agent/skill_reload_selection.go`, `agent/session_skill_compaction.go`; existing compact tool, automatic elicitation, fold publication, snapshot and dispatch files |
| Explicit selection | Wire input and capability, consumption, drafts/recovery, browser proof | `appwire/types.go`, `input.go`, generated API/SDK; server/agent mutation pipeline; composer draft, queue, recovery, submission and completion modules |
| Delivery | Live behavior evaluation, final documentation, gates, independent review and PR | New `agent/skills_live_test.go`; `docs/skills.md`, implementation evidence document and existing study link |

### Existing seams to preserve

- `skill.LoadSkillBody(meta SkillMeta) (string, error)`, `ResolveSkill`, `ResolveSkillContent`, `CatalogEntries`, and `DiscoverSkills` have existing callers/tests. Change their internals where needed and migrate production callers; do not add an alternate permissive parser or legacy-policy fallback.
- `frontmatter.Parse(raw string) (Document, error)` treats an unclosed delimiter as body. Strict skill-file validation belongs in the skill parser; changing all frontmatter consumers is outside this task.
- `restoreFrozenSkillBodies(skillNames, skillBodies []string) ([]string, error)` restores saved bodies without disk reads. Existing descriptors can have only names and bodies. New provenance must remain optional for these descriptors; do not invent their historical source/digest.
- Builtins currently materialize under a process-local temporary directory. Record that actual source. A missing old materialization fails ordinary reload even if discovery creates another copy. Frozen role bodies retain their separate lifetime.
- Cold plugin skill discovery deliberately ignores unrelated malformed plugin components. Share metadata-only manifest/skill selection between live and cold discovery; retain full plugin component loading separately. Do not weaken `TestPastThreadSkill` coverage to obtain parity.
- `make merge-approval-gate` currently runs `make lint`, `make build`, then `ROOT_FULL=1 make test`. Run **`make vet` separately** as well to satisfy the specification's vet requirement. SDK and browser gates are also separate.

## Stage 1: Skill package

### Task 1: Parse invocation controls and expose diagnostics

**Files:** Create `agent/skill/metadata.go`, `agent/skill/metadata_test.go`; modify `agent/skill/skills.go`, `agent/skill/cov_s5_skills_test.go`, `docs/skills.md`.

**Interfaces:** Consumes existing `frontmatter.Parse`, `SkillMeta`, `IsSlashAddressableName`. Produces:

```go
type InvocationControls struct {
    DisableModelInvocation bool `json:"disable_model_invocation"`
    UserInvocable bool `json:"user_invocable"`
}
type Diagnostic struct {
    Category string `json:"category"`
    Name string `json:"name,omitempty"`
    Source string `json:"source"`
    OtherSource string `json:"other_source,omitempty"`
    Field string `json:"field,omitempty"`
    Message string `json:"message"`
}
type Descriptor struct {
    CatalogName string
    Meta SkillMeta
    Controls InvocationControls
    Unavailable bool
}
func Parse(data []byte, skillFile string) (Descriptor, []Diagnostic, error)
```

Use categories `unreadable_source`, `invalid_frontmatter`, `invalid_metadata`, `invalid_control`, `unsupported_control`, `allowed_tools_not_enforced`, and `collision`. `Source` is the winning source for a collision and `OtherSource` the displaced one. Extend `SkillMeta` with `Metadata map[string]any` for descriptive fields; deep-copy mutable metadata when projecting it. `Descriptor.CatalogName` is canonical, while `Meta.Name` remains the declared name. A parse error after a valid name returns that named descriptor with `Unavailable: true` so precedence can reserve it.

- [ ] **Write the red tests.** In package `skill`, use the existing real-file `writeSkill(t, root, dir, content)` helper when a filesystem case is needed. Add this parser test first:

```go
func TestSkillControlsRejectQuotedBoolean(t *testing.T) {
    raw := []byte("---\nname: probe\ndescription: fixture\nuser-invocable: \"false\"\n---\nBODY_941\n")
    d, diagnostics, err := Parse(raw, filepath.Join(t.TempDir(), "SKILL.md"))
    if err == nil || !d.Unavailable || d.CatalogName != "probe" {
        t.Fatalf("descriptor=%+v error=%v", d, err)
    }
    found := false
    for _, diagnostic := range diagnostics {
        found = found || diagnostic.Category == "invalid_control" && diagnostic.Field == "user-invocable"
    }
    if !found { t.Fatalf("diagnostics=%+v", diagnostics) }
}
```

Add table rows for both fields: omitted defaults, true, false, quoted booleans, `yes`, numbers, explicit null, sequence and map. Test missing/blank/non-string required fields, invalid names, closed malformed YAML, unclosed frontmatter, scalar and array `allowed-tools`, mixed-type array rejection, and `context: fork` diagnostic. Assert categories, fields and preserved values rather than message prose.

- [ ] **Run red:** `go test ./agent/skill -run '^TestSkillControls' -count=1`. Initially fails because `Parse` is undefined. After adding the interface, demonstrate the targeted assertion fails before implementing strict parsing.
- [ ] **Implement strict parsing.** Add this internal function and use it for each optional flag; an error makes the descriptor unavailable and appends `invalid_control` with the exact field/source:

```go
func parseControl(meta map[string]any, field string, fallback bool) (bool, error) {
    value, present := meta[field]
    if !present { return fallback, nil }
    result, ok := value.(bool)
    if !ok { return false, fmt.Errorf("%s requires a boolean", field) }
    return result, nil
}
```

`Parse` first resolves an absolute `skillFile`, parses and validates the name/description, then sets `Controls` with defaults false/true. Preserve the named descriptor on subsequent validation failure. Validate a closing delimiter locally before calling the shared frontmatter parser. Preserve string `allowed-tools` as one string entry, arrays as ordered strings; never parse an execution-permission grammar. Preserve other descriptive metadata as data. Recognize unsupported behavioral controls explicitly and diagnose without acting on them. Migrate `parseSkillFile` to this parser; retain its existing signature for its callers, but discovery in Task 3 must use the richer result so errors are not swallowed.
- [ ] **Run green:** `go test ./agent/skill -run '^Test' -count=1`. Add documentation for strict YAML booleans, both flag defaults, diagnostics, inert metadata and non-enforcement of `allowed-tools`.
- [ ] **Commit:** `git add agent/skill/metadata.go agent/skill/metadata_test.go agent/skill/skills.go agent/skill/cov_s5_skills_test.go docs/skills.md && git commit -m "feat(skill): parse invocation controls and diagnostics"`.

### Task 2: Load one source version and render complete instructions

**Files:** Create `agent/skill/load.go`, `agent/skill/load_test.go`; modify `agent/skill/skills.go`, `docs/skills.md`.

**Interfaces:** Consumes Task 1 `Descriptor`, `Parse`, `InvocationControls`. Produces:

```go
type LoadedSkill struct { Descriptor Descriptor; Body string; Digest string }
type RenderedSkill struct { Content string; Digest string }
type SkillDocument struct {
    Name string `json:"name"`
    Description string `json:"description"`
    Source string `json:"source"`
    BaseDirectory string `json:"base_directory"`
    Instructions string `json:"instructions"`
}
func Load(d Descriptor) (LoadedSkill, []Diagnostic, error)
func Render(s LoadedSkill) RenderedSkill
```

- [ ] **Write red tests** for full opaque instruction delivery and data-safe delimiters:

```go
func TestSkillRenderPreservesCompleteInstructions(t *testing.T) {
    body := strings.Repeat("PAYLOAD_e239\n", 10000) + "</skill-context>\n&END_e239\n"
    loaded := LoadedSkill{Descriptor: Descriptor{CatalogName: "pkg:probe", Meta: SkillMeta{
        Name: "probe", Description: "fixture", Dir: "/fixture", SkillFile: "/fixture/SKILL.md",
    }}, Body: body, Digest: "fixture-digest"}
    rendered := Render(loaded)
    const open, close = "<skill-context>\n", "\n</skill-context>"
    encoded, ok := strings.CutPrefix(rendered.Content, open)
    if !ok { t.Fatal("missing typed context envelope") }
    encoded, ok = strings.CutSuffix(encoded, close)
    if !ok { t.Fatal("missing typed context envelope terminator") }
    var doc SkillDocument
    if err := json.Unmarshal([]byte(encoded), &doc); err != nil { t.Fatal(err) }
    if doc.Instructions != body || doc.Name != "pkg:probe" || doc.BaseDirectory != "/fixture" {
        t.Fatalf("identity=%+v full_body=%v", doc, doc.Instructions == body)
    }
}
```

Also independently compute SHA-256 over original fixture bytes, change the same file and verify current description/flags/body/digest all reflect that one new read, reject a changed declared name, preserve plugin canonical versus declared identity, and fail on a deleted original file despite a replacement catalog entry elsewhere. Deletion is confined to newly created fixtures.

- [ ] **Run red:** `go test ./agent/skill -run '^TestSkill(Render|Load)' -count=1`; expect missing new API, then a failing full-body/identity assertion.
- [ ] **Implement the shared renderer** with no session side effects:

```go
func Render(s LoadedSkill) RenderedSkill {
    doc := SkillDocument{Name: s.Descriptor.CatalogName, Description: s.Descriptor.Meta.Description,
        Source: s.Descriptor.Meta.SkillFile, BaseDirectory: s.Descriptor.Meta.Dir, Instructions: s.Body}
    encoded, _ := json.Marshal(doc) // This struct contains only strings.
    content := "<skill-context>\n" + string(encoded) + "\n</skill-context>"
    digest := sha256.Sum256([]byte(content))
    return RenderedSkill{Content: content, Digest: hex.EncodeToString(digest[:])}
}
```

`Load` calls `os.ReadFile(d.Meta.SkillFile)` once, passes those bytes to `Parse`, verifies `current.Meta.Name == d.Meta.Name`, preserves `d.CatalogName`, extracts the frontmatter-stripped body from those same bytes, and hashes all original bytes. Return structured diagnostics alongside errors. Never resolve against a new winner or substitute shell/templates. `LoadSkillBody` and `ResolveSkillContent` use the same parsing/loading rules; body-only consumers migrate to `Render` in Stage 2. Add directory-guidance semantics for `base_directory` to the skill prompt instructions, without changing tool permissions.
- [ ] **Run green:** `go test ./agent/skill -run '^Test' -count=1`. Document complete delivery, lazy references and non-executable templates.
- [ ] **Commit:** `git add agent/skill/load.go agent/skill/load_test.go agent/skill/skills.go docs/skills.md && git commit -m "feat(skill): share exact-source loading and complete rendering"`.

### Task 3: Unify portable discovery and filtered advertisements

**Files:** Create `agent/skill/discovery.go`, `agent/skill/discovery_test.go`, `agent/plugin/skill_sources.go`, `agent/plugin/skill_sources_test.go`; modify `agent/skill/skills.go`, `agent/skill/builtin_skills.go`, `agent/plugin/plugin.go`, `agent/session_init.go`, `agent/session_prompts.go`, `agent/status.go`, `agent/prompt_data.go`, `cmd/evener-hub/app_threadread.go`, `cmd/evener-hub/app_threadread_test.go`, `agent/session_skills_test.go`, `docs/skills.md`. Client-visible diagnostic wire projection is completed with Task 10's co-generation, not handwritten generated output.

Also modify these existing catalog consumers for the field-type migration only: `agent/session.go`, `agent/session_tool_registry.go`, `agent/session_slash_command.go`, `agent/subagents.go`, `agent/delegate_runtime.go`, `agent/plugin_integration_live_test.go`, `agent/session_slash_command_test.go`, `agent/session_lifecycle_slots_fuzz_test.go`, `agent/status_test.go`, `agent/agent_misc_program_fuzz_test.go`, `agent/session_perf_test.go`, `agent/plugin_agents_integration_test.go`, `agent/subagents_fuzz_test.go`. Change the existing `Session.skills` field from `map[string]skill.SkillMeta` to `skill.Catalog`; update accesses to `Entries` and descriptor metadata without maintaining a parallel legacy map. These are fixture/carrier changes, not permission or activation-lifetime changes. Preserve every existing assertion's reference and meaning.

**Interfaces:** Consumes Task 1 descriptors/diagnostics and Task 2 loader. Produces:

```go
type PluginSource struct { Name, Dir string }
type DiscoverOptions struct {
    HomeDir string
    UserSkillsDir string
    ExtraDirs []string
    Plugins []PluginSource
}
type Catalog struct { Entries map[string]Descriptor; Diagnostics []Diagnostic }
type ResolutionError struct { Kind string; Name string; Candidates []string }
func (e *ResolutionError) Error() string
func Discover(env execenv.ExecutionEnvironment, opts DiscoverOptions) Catalog
func (c Catalog) Resolve(name string) (Descriptor, error)
func (c Catalog) ResolveExact(name string) (Descriptor, error)
func (c Catalog) ModelEntries() []Descriptor
func (c Catalog) UserEntries() []Descriptor
// In package plugin:
func SkillSources(dirs []string) ([]skill.PluginSource, []skill.Diagnostic)
```

`ResolveExact` never suffix-matches; `Resolve` returns exact match first, then one qualified suffix match, or `ResolutionError{Kind:"unknown"|"ambiguous"}` with sorted candidates. Known invalid winners return their descriptor for a visible invalid-metadata refusal. Advertisements omit unavailable entries and filter only their respective flag; the full catalog retains hidden valid entries for runtime authorization checks.

- [ ] **Write red tests** against real directories, starting with:

```go
func TestPortableProjectDirectory(t *testing.T) {
    root := t.TempDir()
    if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil { t.Fatal(err) }
    want := writeSkill(t, filepath.Join(root, ".agents", "skills"), "probe",
        "---\nname: probe\ndescription: fixture\n---\nPORTABLE_31ac\n")
    catalog := Discover(execenv.NewLocalExecutionEnvironment(root), DiscoverOptions{HomeDir: t.TempDir()})
    if got := catalog.Entries["probe"].Meta.SkillFile; got != want { t.Fatalf("source=%q want=%q", got, want) }
}
```

Create same-name fixtures at every precedence level and assert winner paths and both collision sources. A higher-priority quoted control reserves the name as unavailable, including across plugin qualification. Missing optional directories emit no diagnostic; absent/unreadable configured roots do. Isolate HOME and XDG in integration fixtures; do not run home-mutating tests in parallel. Test canonical/suffix/ambiguous names and independent model/user views without prompt-string snapshots. Retain cold metadata-only malformed-plugin tests and first-manifest reservation tests.

- [ ] **Run red:** `go test ./agent/skill -run '^(TestPortable|TestSkillDiscovery|TestSkillCatalog)' -count=1`.
- [ ] **Implement ordered discovery and catalog views.** The view predicate is:

```go
func (c Catalog) ModelEntries() []Descriptor {
    names := make([]string, 0, len(c.Entries))
    for name, d := range c.Entries {
        if !d.Unavailable && !d.Controls.DisableModelInvocation { names = append(names, name) }
    }
    slices.Sort(names)
    result := make([]Descriptor, 0, len(names))
    for _, name := range names { result = append(result, c.Entries[name]) }
    return result
}
```

Implement `UserEntries` with `d.Controls.UserInvocable`. Centralize winner insertion and diagnostic accumulation inside `Discover`. Scan embedded, home `.agents/skills`, Evener user, root-to-cwd `.agents/skills` then `skills`, explicit roots in order, then qualified plugins. Share the manifest-only `SkillSources` selector between startup and cold catalog; `initPlugins` must no longer silently replace that skill catalog with full-component results. Preserve unrelated plugin loading and hooks. Keep configured roots discoverable when a cold entry has no cwd. Startup exposes diagnostics through existing event/inspection channels; status returns full metadata without leaking source paths into the completion catalog. Model fallback advertisements use the same filtered view. Do not make raw file reads tracked activations.
- [ ] **Run the Stage 1 gate:**

```bash
go test ./agent/skill ./agent/plugin -run '^Test' -count=1
go test ./agent -run '^(TestDiscoverSkills_|TestNewSessionAutomaticallyDiscoversUserSkill$|TestConfiguredSkillDirShadowsAutomaticUserSkill$|TestProjectSkillShadowsAutomaticUserSkill$|TestSkillCatalog|TestRestoreFrozenSkillBodies)' -count=1
go test ./cmd/evener-hub -run '^(TestPastThreadSkill|TestPastThreadReadCarriesSkillCatalog$|TestPastThreadReadResponseCarriesSkillCatalog$|TestPastThreadListDoesNotDiscoverSkills$|TestPastThreadTurnsListDoesNotDiscoverSkills$)' -count=1
```

Document exact precedence, trust, collision diagnostics, flag advertisements and the full-catalog/runtime distinction. Confirm every newly named test family actually exists and ran, rather than accepting a no-tests warning.
- [ ] **Commit** only this task's named files with message `feat(skill): unify portable discovery and catalog policy`. Review the Stage 1 diff and passing gate before Stage 2.

## Stage 2: Session activation

### Task 4: Persist activation identity and source-scoped authorization

**Files:** Create `agent/schema/skill_lifecycle.go`, `agent/schema/skill_lifecycle_test.go`, `agent/session_skill_activation.go`, `agent/session_skill_activation_test.go`; modify `agent/schema/snapshot.go`, `agent/schema/turn.go`, `agent/session.go`, `agent/session_state.go`, `agent/session_init.go`, `docs/skills.md`.

**Interfaces:** Consumes Stage 1 types. Add the following schema DTOs, with snake_case JSON tags on all fields; do not omit either boolean flag:

```go
type SkillInvocationControls struct {
    DisableModelInvocation bool `json:"disable_model_invocation"`
    UserInvocable bool `json:"user_invocable"`
}
type SkillContentIdentity struct {
    Name string
    DeclaredName string
    Source string
    FileDigest string
    RenderedDigest string
}
type OrdinarySkillActivation struct {
    Identity SkillContentIdentity
    Description string
    Controls SkillInvocationControls
    Route string
    InvocationID string
    UserAuthorized bool
}
type FrozenSkillPreload struct {
    Name string
    Description string
    Source string // Empty only for legacy unknown provenance.
    FileDigest string
    RenderedDigest string
}
type SkillInventoryEntry struct {
    Ordinary *OrdinarySkillActivation
    Preload *FrozenSkillPreload
}
type SkillDeliveryObligation struct {
    InvocationID string
    ToolCallID string
    ClientMutationID string
    AtomicGroupID string
    Identity SkillContentIdentity
    Route string
}
type SkillLifecycleSnapshot struct {
    Revision uint64
    Inventory map[string]SkillInventoryEntry
    Obligations []SkillDeliveryObligation
    PinnedNoteGen uint64
    NextOperationGen uint64
}
type SkillInputRecord struct {
    OriginalText string
    Arguments string
    Names []string
    AtomicGroupID string
}
type SkillActivationOutcome struct {
    Revision uint64
    SessionID string
    InvocationID string
    ToolCallID string
    ClientMutationID string
    Identity SkillContentIdentity
    Activation *OrdinarySkillActivation
    Status string
    ErrorCode string
    PreviousIdentity *SkillContentIdentity
    PreviousControls *SkillInvocationControls
}
type SkillTurnState struct {
    Input *SkillInputRecord
    Outcomes []SkillActivationOutcome
    Obligations []SkillDeliveryObligation
}
```

Add `Skills *SkillLifecycleSnapshot` to `schema.SessionMeta` and `SkillState *SkillTurnState` to `schema.Turn`. Status values are `pending`, `already_present`, `delivered`, `failed`; failure codes distinguish `source_missing`, `invalid_metadata`, `policy_denied`, `output_limit`, `context_budget`, `source_changed`, and `save_failed`. `already_present` is provisional until its obligation is fulfilled. A final unchanged-body satisfaction does not emit a new-body delivery event.

Add runtime `skillLifecycle schema.SkillLifecycleSnapshot` to `Session`, protected by the existing `s.mu`, with a non-nil inventory map. Snapshot/restore copy this field; it is not a second store. The existing catalog field is separate. The existing pinned-note text and `pinnedNoteGen` remain authoritative, with the generation copied into lifecycle metadata at save.

Runtime interfaces:

```go
type skillInvocation struct {
    Name, Route, InvocationID, ToolCallID, ClientMutationID, AtomicGroupID string
    Source *schema.SkillContentIdentity // Non-nil only for continuation of this exact source.
}
type preparedSkillActivation struct {
    Invocation skillInvocation
    Loaded skill.LoadedSkill
    Rendered skill.RenderedSkill
}
type skillActivationBatch struct { Items []preparedSkillActivation }
func skillInvocationAllowed(route string, controls skill.InvocationControls, authorized bool) bool
func (s *Session) prepareSkillActivations(ctx context.Context, invocations []skillInvocation) (*skillActivationBatch, error)
func (s *Session) saveMeta() error
```

Routes are `user_slash`, `user_selection`, `model_tool`, `compaction_reload`, `role_preload`. Only server-created genuine-user routes can authorize a source. Generated model text, role input, copied history and client fields cannot supply that fact.

- [ ] **Write red policy and actual metadata round-trip tests.** Start with the complete flag matrix:

```go
func TestSkillActivation_PolicyMatrix(t *testing.T) {
    for _, disabled := range []bool{false, true} {
        for _, user := range []bool{false, true} {
            controls := skill.InvocationControls{DisableModelInvocation: disabled, UserInvocable: user}
            for _, authorized := range []bool{false, true} {
                for _, route := range []string{"user_slash", "user_selection", "model_tool", "compaction_reload", "role_preload"} {
                    want := route == "role_preload"
                    switch route {
                    case "user_slash", "user_selection": want = user
                    case "model_tool", "compaction_reload": want = authorized || !disabled
                    }
                    if got := skillInvocationAllowed(route, controls, authorized); got != want {
                        t.Fatalf("route=%s disabled=%v user=%v auth=%v got=%v", route, disabled, user, authorized, got)
                    }
                }
            }
        }
    }
}
```

Use `schema.SaveSessionMeta`/`LoadSessionMeta` with real temporary files and `RestoreSessionFromMeta` to prove inventory, controls including false, obligations, and note generation round-trip. Old metadata without `Skills` must restore empty ordinary state. A failed reinvocation preserves the earlier ordinary record. Same-name source replacement revokes prior authorization; frozen provenance remains separate.

- [ ] **Run red:** `go test ./agent -run '^TestSkillActivation_PolicyMatrix$' -count=1` and `go test ./agent/schema -run '^TestSkillLifecycleSnapshot_' -count=1`.
- [ ] **Implement preparation and policy:**

```go
func skillInvocationAllowed(route string, controls skill.InvocationControls, authorized bool) bool {
    switch route {
    case "user_slash", "user_selection": return controls.UserInvocable
    case "model_tool", "compaction_reload": return authorized || !controls.DisableModelInvocation
    case "role_preload": return true
    default: return false
    }
}
```

Preparation resolves fresh names through the full catalog, or reconstructs `skill.Descriptor` from `Source.Name`, `Source.DeclaredName`, `Source.Source` and `filepath.Dir(Source.Source)` for continuation. Call `skill.Load` and `skill.Render`; check current controls against same-name/same-source authorization. Return no inventory changes on failure. Snapshot under `s.mu` and deep-copy maps/slices. Extract existing `maybeAutoSave` internals into error-returning `saveMeta`, preserving lock order `metaSaveMu` before `Meta()` and filesystem I/O outside `s.mu`; leave `maybeAutoSave` as the existing warning wrapper. Lifecycle-sensitive callers return visible save failure instead of claiming durable success. Add new typed records to `recordTurn`; never recover inventory by recognizing body text.
- [ ] **Run green:** the two red commands, then `go test ./agent -run '^(TestSkillActivation_|TestPinnedNote_MetaRoundTrip|TestPinnedNote_SurvivesResume)' -count=1`. Document future-only history, exact-source authorization, and untracked raw file reads.
- [ ] **Commit** the task's named paths with `feat(agent): persist skill activation identity and authorization`.

### Task 5: Activate skills with complete final-dispatch delivery

**Files:** Modify `agent/session_skill_activation.go`, `agent/session_skill_activation_test.go`, `agent/session_tools_communicate.go`, `agent/session_tool_registry.go`, `agent/session_slash_command.go`, `agent/session_lifecycle.go`, `agent/input_message.go`, `agent/subagents.go`, `agent/delegate_runtime.go`, `agent/internal/delegatestore/record.go`, `agent/session_prompts.go`, `agent/prompt_data.go`, `agent/session_skills_test.go`, `agent/subagents_branches_test.go`, `docs/skills.md`.

**Interfaces:** Consumes Task 4 preparation and `SkillInputRecord`. Replace the slash expansion return with an explicit internal result so failures cannot fall through:

```go
type slashInputResult struct {
    Text string
    Handled bool
    Selection *schema.SkillInputRecord
    Activations *skillActivationBatch
    Err error
}
func (s *Session) expandSlashCommand(ctx context.Context, input string) slashInputResult
```

`Text` is original input for skill routes; ordinary command expansion keeps its existing behavior. Leading slash calls `Catalog.Resolve`, structured selection later uses `ResolveExact`, and model tool resolution retains the existing exact/unique-suffix behavior. A hidden authorized name must remain resolvable through the full catalog. Add `FrozenSkillMetadata []schema.FrozenSkillPreload` to the existing descriptor and corresponding spawn configuration, alongside the unchanged names/bodies.

- [ ] **Write failing provider-boundary tests.** Reuse `agenttest.ScriptedAdapter`, `newSession`, `withAdapter`, `withDir`, `withProfile`, `newAnthropicProfile`, `useSkillCall`, `toolCallResponse`, and `communicateCall`. This setup is shared test infrastructure for the integration tests, not a mocked session:

```go
root := t.TempDir()
markGitRoot(t, root)
body := strings.Repeat("BODY_7f2a\n", 64)
writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+body)
calls := 0
adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
    calls++
    if calls == 1 { return toolCallResponse(useSkillCall("skill-1", "opaque")) }
    return toolCallResponse(communicateCall("done-1", "ok"))
}}
s := newSession(t, withAdapter(adapter), withDir(root),
    withProfile(newAnthropicProfile("claude-test")), withoutGitSnapshot())
if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil { t.Fatal(err) }
if len(adapter.Requests()) != 2 { t.Fatalf("requests=%d", len(adapter.Requests())) }
```

In `TestSkillActivation_Routes`, decode the Task 2 JSON envelope from actual provider content for tool/slash/preload and assert complete original `body`, canonical name, source and base directory. Check typed saved input separately for original request and arguments. Delete a discovered fixture before invoking it and assert no success event. Include known denied/unreadable versus unknown slash, command-name collision, unique/ambiguous suffixes, new arguments with duplicate bodies, model-generated slash without authorization, and raw `read_file` full/partial reads without activation state.

- [ ] **Run red:** `go test ./agent -run '^TestSkillActivation_(Routes|Failure|Resolution|RawRead)' -count=1`.
- [ ] **Implement route wiring.** Tool handlers return `tool.StateResult` containing pending identity and causal IDs; remove premature `EventSkillActivated`. For each user route construct the invocation from trusted call-site provenance:

```go
invocation := skillInvocation{
    Name: canonicalName, Route: "user_slash", InvocationID: invocationID,
    AtomicGroupID: invocationID,
}
batch, err := s.prepareSkillActivations(ctx, []skillInvocation{invocation})
result := slashInputResult{Text: input, Handled: true, Activations: batch, Err: err,
    Selection: &schema.SkillInputRecord{OriginalText: input, Arguments: arguments,
        Names: []string{canonicalName}, AtomicGroupID: invocationID}}
```

Here `canonicalName` is the resolved descriptor name, `invocationID` the existing causal turn/input identity, and `arguments` the parsed slash tail. Define them at the existing resolution call site, not with arbitrary random identifiers. Preserve user prose and attachments in the original message; attach rendered instructions through separately identifiable context/typed turns. A known failure produces a visible failed input and dispatches no dependent work.

Fresh role setup uses the shared loader/renderer and seeds preload metadata only after its permanent prompt is admitted. Existing frozen descriptor restoration uses saved names/bodies exactly; optional metadata stays unknown when absent. Never reload legacy frozen bytes from disk or grant ordinary user authorization. New/forked delegates seed only their role preloads; filter lifecycle import by originating session identity even when copied history contains new-format records. Same-delegate restore uses its own saved metadata/receipts.
Continue with complete admission below before the task's green gate or commit. Route integration and delivery publication are one review unit; do not land pending-only activation behavior.

#### Full-body admission and dispatch revalidation (same task)

**Files:** Create `agent/session_skill_delivery.go`, `agent/session_skill_delivery_test.go`; modify `agent/session_skill_activation.go`, `agent/internal/tool/registry.go`, `agent/internal/tool/registry_test.go`, `agent/session_tools.go`, `agent/session_model_call.go`, `agent/session_stream.go`, `agent/session_config.go`, `agent/session_state.go`, `agent/session_init.go`, `docs/skills.md`.

**Interfaces:** Consumes prepared batches, lifecycle revision and typed outcomes. Extend existing `tool.StateResult` with `RequireCompleteOutput bool`; use it only for operations that promise complete content. Add:

```go
type skillDeliveryCommit struct {
    Revision uint64
    Outcomes []schema.SkillActivationOutcome
    SatisfiedInvocationIDs []string
}
func (s *Session) prepareSkillDelivery(ctx context.Context, profile *provider.Profile,
    turns []schema.Turn, req llm.Request) (llm.Request, skillDeliveryCommit, error)
func (s *Session) commitSkillDelivery(ctx context.Context, commit skillDeliveryCommit) (bool, error)
func completeSkillContentPresent(req llm.Request, identity schema.SkillContentIdentity) bool
```

The presence function recognizes complete Task 2 envelopes in eligible request carriers and verifies canonical/source/rendered digest against runtime-owned typed provenance. It must not infer activation intent from arbitrary user text. The commit checks lifecycle revision, admits the whole explicit group, writes causal outcomes through existing transcript machinery, saves obligations and updates inventory; stale revision returns false for revalidation. Persisted delivery represents complete admission, not proof of model obedience or a successful network response.

- [ ] **Write the full-body red first** using Task 5's real provider setup. Compare decoded instruction bytes from the second provider request to the original long fixture, independent of rendering logic. Then add separate cases for configured output limit, mandatory-budget failure, failed reinvocation preserving inventory, and a new activation surviving a real pre-dispatch compaction.
- [ ] **Run red:** `go test ./agent -run '^TestSkillDelivery_CompleteBody$' -count=1`. Expect the old suffix truncation to violate the complete-body assertion, not an unrelated setup failure.
- [ ] **Implement complete-or-fail tool shaping.** In the existing registry truncation branch, enforce this decision before returning output:

```go
if result.RequireCompleteOutput && wouldTruncate {
    execResult.IsError = true
    execResult.Err = fmt.Errorf("complete output exceeds configured limit")
    execResult.Output = execResult.Err.Error()
    execResult.FullOutput = ""
    execResult.RecoverableOutput = ""
    execResult.Truncated = false
}
```

`result` is the decoded `StateResult`; `wouldTruncate` is the existing output policy's actual decision, and `execResult` its existing result value. Produce typed `output_limit` failure state as well. Remove only `use_skill`'s default 32,000-character tail policy; preserve explicit user limits as complete-delivery failures and preserve other tools' truncation. Failed output shaping cannot leave a pending-success inventory record.
- [ ] **Add red dispatch-revalidation tests.** A first complete activation followed by a second `use_skill` produces a provisional already-present result. Use actual summarizer barriers from `session_fold_publication_test.go` to remove its old carrier before the next dispatch. Assert the next real provider request has exactly one complete current body and causal corrected outcome. Repeat for projection, Responses continuation planning, full-history recovery, model fallback with smaller window, transport retry and save/restore. Change/delete the same source between provisional result and dispatch; require changed-content notice or failure, never false delivery. No new arguments may disappear. Add the same-name frozen-plus-ordinary case.
- [ ] **Implement final admission at every dispatch shape.** Store a pending obligation when returning provisional dedupe. Protect not-yet-delivered activation carriers from history masking/folding; normal older-history compaction remains allowed. After projection and continuation construction, call `prepareSkillDelivery` on the actual request. For missing content reload `Source` with the shared loader and route policy, append a typed causal activation notification to real history so continuation re-expansion also sees it, and recompute the request. At `callModel`'s final budget seam immediately before `s.client.Stream`/nonstreaming dispatch, revalidate the resulting shape and call `commitSkillDelivery`. Apply the same guard to full-history and fallback request rebuilding. A final budget change must fail visibly rather than truncate protected bodies or start another compact/reload cycle.

Use existing `budgetModelDispatchRequestWithBudget`, token estimator, model window and output reservation. Reserve mandatory/current-request metadata first. Then admit complete current activation groups, then Task 9 reloads in order. Insufficient explicit-group budget dispatches none of that request; an unrelated in-flight turn excludes all failed steering prose. Commit satisfied unchanged-content obligations without a new-body event; commit reload success only after full admission. Save pending obligations before a retry/restart can lose them. Delivery receipt reconciliation must prevent duplicate success while still proving complete retained content on restored dispatch.
- [ ] **Run the Stage 2 gate:**

```bash
go test ./agent/internal/tool ./agent/schema -run '^Test' -count=1
go test ./agent -run '^(TestSkillActivation_|TestSkillDelivery_|TestUseSkill_|TestStandaloneSkillActivation|TestRestoreFrozenSkillBodies|TestPrepareModelRequest_)' -count=1
go test -race ./agent -run '^(TestSkillDelivery_|TestFoldPublication_)' -count=1
```

Document complete-or-fail limits, current availability versus inventory, provisional dedupe correction, and large-skill reference-file guidance. Check actual events/client grouping through typed projection. Review the stage before compaction work.
- [ ] **Commit** named task files with `feat(agent): enforce complete skill delivery at dispatch`.

## Stage 3: Compaction selection and lifetime

### Task 6: Parse explicit and automatic reload selection

**Files:** Create `agent/skill_reload_selection.go`, `agent/skill_reload_selection_test.go`; modify `agent/schema/skill_lifecycle.go`, `agent/session_tools_compact.go`, `agent/session_tool_registry.go`, `agent/internal/contextmgr/context_manager.go`, `agent/internal/contextmgr/context_manager_test.go`, `agent/session_self_compact.go`, `docs/skills.md`.

**Interfaces:** Add:

```go
type SkillReloadSelection struct {
    State string `json:"state"` // absent, valid, invalid
    Names []string `json:"names"`
    ErrorCode string `json:"error_code,omitempty"`
}
type SkillInventorySummary struct {
    Name string
    Description string
    Availability string
    HasOrdinary bool
    HasPreload bool
}
// In package agent:
func parseSkillReloadSelection(raw json.RawMessage, inventory map[string]schema.SkillInventoryEntry) schema.SkillReloadSelection
func parseSkillReloadElicitation(text string, inventory map[string]schema.SkillInventoryEntry) (string, schema.SkillReloadSelection)
// Changed existing context-manager API:
func (cm *Manager) ElicitNote(ctx context.Context, history []schema.Turn, loaded []schema.SkillInventorySummary) (string, error)
```

- [ ] **Write red parsing tests** with the exact selection table:

```go
func TestSkillReloadSelection_Presence(t *testing.T) {
    inventory := map[string]schema.SkillInventoryEntry{"pkg:probe": {}}
    for _, tc := range []struct{ raw, state string; names []string }{
        {"", "absent", nil}, {"null", "absent", nil}, {"[]", "valid", []string{}},
        {`["pkg:probe","pkg:probe"]`, "valid", []string{"pkg:probe"}},
        {`["missing"]`, "invalid", nil}, {`"pkg:probe"`, "invalid", nil},
    } {
        got := parseSkillReloadSelection(json.RawMessage(tc.raw), inventory)
        if got.State != tc.state || !reflect.DeepEqual(got.Names, tc.names) {
            t.Fatalf("raw=%q result=%+v", tc.raw, got)
        }
    }
}
```

Add single valid block removal from an opaque free-text note, no block, multiple blocks, malformed JSON, unknown names, incidental mentions, null and selection-only `[]`. Invalid parsing preserves the note. Tool JSON schema still requires `note_to_self` and validates array element types; schema-invalid calls schedule nothing. Accepted unknown names yield invalid selection and preserve the valid note.
- [ ] **Run red:** `go test ./agent -run '^TestSkillReload(Selection|Elicitation)_' -count=1`.
- [ ] **Implement explicit presence and ordered deduplication:**

```go
func parseSkillReloadSelection(raw json.RawMessage, inventory map[string]schema.SkillInventoryEntry) schema.SkillReloadSelection {
    if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
        return schema.SkillReloadSelection{State: "absent"}
    }
    var names []string
    if err := json.Unmarshal(raw, &names); err != nil {
        return schema.SkillReloadSelection{State: "invalid", ErrorCode: "invalid_selection"}
    }
    result := schema.SkillReloadSelection{State: "valid", Names: []string{}}
    seen := map[string]bool{}
    for _, name := range names {
        if _, ok := inventory[name]; !ok {
            return schema.SkillReloadSelection{State: "invalid", ErrorCode: "unknown_skill"}
        }
        if !seen[name] { seen[name] = true; result.Names = append(result.Names, name) }
    }
    return result
}
```

The elicitation parser accepts exactly one `<skill-reload-selection>` block whose JSON object has `reload_skills`; use raw JSON to preserve presence. Remove only a valid block. Missing/multiple/malformed block cannot authorize any body load. Extend `ElicitNote` and its `elicitNoteFn` call sites consistently; give it typed loaded metadata from successful inventory. Nudge with `compact_context` asks for selection; no-tool nudge gives awareness and existing note advice only. Preserve best-effort fallback without an extra repair round.
- [ ] **Run green:** red command plus `go test ./agent/internal/contextmgr -run '^Test' -count=1`. Document the tool and automatic selection protocols and all empty/null/invalid distinctions.
- [ ] **Commit** task paths with `feat(agent): parse compaction skill reload selections`.

### Task 7: Own and persist pending compaction operations

**Files:** Create `agent/session_skill_compaction.go`, `agent/session_skill_compaction_test.go`, `agent/session_skill_compaction_restore_test.go`; modify `agent/schema/skill_lifecycle.go`, `agent/schema/skill_lifecycle_test.go`, `agent/session_self_compact.go`, `agent/session_tools_compact.go`, `agent/session_tool_registry.go`, `agent/session_state.go`, `agent/session_init.go`, `agent/session.go`, `docs/skills.md`.

**Interfaces:** Add the owning operation and `PendingCompaction *SkillCompactionOperation` to `SkillLifecycleSnapshot`:

```go
type SkillCompactionOperation struct {
    Generation uint64 `json:"generation"`
    Origin string `json:"origin"` // forced or automatic
    Instructions string `json:"instructions"`
    NoteGeneration uint64 `json:"note_generation"`
    Selection SkillReloadSelection `json:"selection"`
    Phase string `json:"phase"` // pending or published
    PublicationID string `json:"publication_id,omitempty"`
}
// In package agent:
func (s *Session) requestSkillCompaction(ctx context.Context, note, instructions string, selection schema.SkillReloadSelection) (uint64, error)
func (s *Session) acceptAutomaticSkillCompaction(ctx context.Context, capturedNoteGen uint64, note string, selection schema.SkillReloadSelection) (bool, error)
func (s *Session) cancelSkillCompaction(ctx context.Context, generation uint64, reason string) error
func (s *Session) resumeSkillCompaction(ctx context.Context) error
```

Cancellation reasons are `superseded_note`, `forced_not_published`, and `save_failed`. Visible notices identify the operation generation/origin and lost selection, never claim that no other compaction occurred.

- [ ] **Write acceptance durability red tests**, including a skill-only empty selection:

```go
func TestSkillCompaction_AcceptanceSurvivesMetadataRoundTrip(t *testing.T) {
    stateDir := t.TempDir()
    s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
    generation, err := s.requestSkillCompaction(context.Background(), "", "",
        schema.SkillReloadSelection{State: "valid", Names: []string{}})
    if err != nil { t.Fatal(err) }
    loaded, err := schema.LoadSessionMeta(stateDir, s.Meta().ID)
    if err != nil { t.Fatal(err) }
    if loaded.Skills == nil || loaded.Skills.PendingCompaction == nil { t.Fatal("operation not saved") }
    pending := loaded.Skills.PendingCompaction
    if pending.Generation != generation || pending.Origin != "forced" || pending.Phase != "pending" || pending.Selection.State != "valid" {
        t.Fatalf("pending=%+v", pending)
    }
}
```

The existing `SessionConfig.StateDir` enables real incremental persistence; `newSession` passes the supplied config to `NewSession`. Do not accept a round-trip that only serializes a manually constructed copy. Add automatic selection-only `[]` acceptance before publication, second-request rejection with unchanged note/selection, stale captured generation, nonempty-note latch, pending-operation latch, and explicit clear cancellation. Use real filesystem failure conditions for save failure and assert typed outcome, not warning wording.
- [ ] **Run red:** `go test ./agent -run '^TestSkillCompaction_(Acceptance|Latch|Stale|Clear|Save)' -count=1`.
- [ ] **Implement atomic ownership transitions.** Under `s.mu`, check for an existing operation **before changing note**, increment operation and note generations, store note and owning operation, increment lifecycle revision, then save outside `s.mu` through `saveMeta`. Serialize acceptance/save ordering with existing metadata synchronization; do not expose an unsaved success. On save error retain a visible retryable failure and prevent dispatch from treating the operation as durable until reconciled. Replace consume-before-fold `takeForceRequest` behavior with an owner snapshot retained until publication or terminal cancellation.

The automatic acceptance guard is:

```go
if s.pinnedNoteGen != capturedNoteGen || s.skillLifecycle.PendingCompaction != nil {
    return false, nil
}
```

Here `s.skillLifecycle` is the runtime `SkillLifecycleSnapshot` added in Task 4. Capture the note generation before the actual elicitor call. Latch on nonempty note **or** any pending/published undelivered operation. Empty note plus absent selection and no instructions clears without compaction; present valid `[]` requests one. Clearing/replacing a note cancels only its associated automatic operation. Generation-matched publication uses claim semantics and does not call the replacement cancellation path. Persist accepted automatic responses, cancellations and phase transitions once; unchanged pressure checks and losing attempts do not write metadata.
- [ ] **Run green** and restore tests with actual saved metadata/transcripts. Restore an unpublished forced operation before dispatch; automatic state restores its latch and waits for normal pressure. Restart is not cancellation. A published operation must finish delivery only. Cover missing legacy lifecycle metadata without history backfill.
- [ ] **Commit** named files with `feat(agent): persist generation-owned skill compaction intent`.

### Task 8: Claim operations only in winning actual fold publication

**Files:** Create `agent/session_skill_compaction_publication_test.go`; modify `agent/session_compaction.go`, `agent/session_skill_compaction.go`, `agent/session_self_compact.go`, `agent/session_model_call.go`, `agent/schema/skill_lifecycle.go`, `agent/transcript_read.go`, `agent/session_init.go`, `agent/session_state.go`, `docs/skills.md`.

**Interfaces:** Extend `foldCommit` with `actualCompaction bool` and an explicitly captured operation copy. Add a schema receipt and attach it as `SkillTurnState.Compaction`; add `PendingHandoffs []SkillCompactionReceipt` to `SkillLifecycleSnapshot`:

```go
type SkillCompactionReceipt struct {
    Revision uint64
    SessionID string
    Operation SkillCompactionOperation
    Phase string // published, delivered, cancelled
    Reason string
}
```

Existing integration APIs are `publishFoldTransaction(snapLen, snapRevision, snapAppends int, folded []schema.Turn, commit *foldCommit, onPublishLocked func([]schema.Turn)) ([]schema.Turn, bool)` and `stageCompactionEffects`. Preserve lock order `attentionMu` then `s.mu`; no new transaction or independent store.

Pending handoffs are metadata receipts in that same snapshot, distinct from accepted unpublished intent. This is needed when a competing winner has a handoff while a forced operation remains pending. Every actual compaction records a handoff, including an absent-selection reminder when no operation was captured. Do not overwrite the losing forced operation with the winner's receipt. Coalesce checkpoint/summary phases belonging to the same winning publication into its final handoff, using publication identity rather than list position.

- [ ] **Write red race/lifetime cases** using the existing real summarizer fixture and its barriers:

```go
entered, release := make(chan struct{}), make(chan struct{})
s := newScriptedSummaryCompactSession(t, "anthropic", func(llm.Request) llm.Response {
    close(entered)
    <-release
    return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nSUMMARY_f1e9\n[END SUMMARY]")}
}, withoutGitSnapshot())
seedNumberedSessionHistory(t, s, 20)
```

Park the actual fold, clear/replace or accept competing intent, then release it. Assert typed receipt generations, provider counts, saved inventory/obligations and retained steering. Required named cases: `DeferredAutomatic`, `CheckpointOnly`, `UnchangedPublication`, `LosingAttempt`, `CompetingForced`, `ConcurrentSteering`, `FinalSummary`, `RestartBeforePublish`, `RestartAfterPublish`, `StaleSnapshot`. No direct fake publication may stand in for the decisive real-fold cases.
- [ ] **Run red:** `go test ./agent -run '^TestSkillCompaction_(DeferredAutomatic|CheckpointOnly|UnchangedPublication|LosingAttempt|CompetingForced)' -count=1`.
- [ ] **Implement actual-compaction ownership.** Set `actualCompaction` from staged `EventContextCompaction`, not merely successful history publication. Capture forced generation from its requesting caller; unrelated automatic/manual folds never adopt a global pending forced selection. Automatic capture includes note and operation generation even for an empty note. The winning callback uses this predicate:

```go
pending := s.skillLifecycle.PendingCompaction
claim := commit.actualCompaction && pending != nil && captured != nil &&
    pending.Generation == captured.Generation && pending.NoteGeneration == captured.NoteGeneration
if claim {
    pending.Phase = "published"
    pending.PublicationID = publicationID
    s.skillLifecycle.Revision++
}
```

`captured` is the fold's explicit operation snapshot; `publicationID` is the existing winning checkpoint/summary identity. Commit receipt and markers through `commitTranscriptsLocked`, preserve append-tail rewriting, flush events after locks, then save metadata phase. Losing folds do none of this. Checkpoint-only actual compaction counts. Unchanged model-request publication leaves intent untouched. Retry exhaustion cancels only the forced owner's generation and emits its terminal loss notice alongside any winner's handoff. Pressure drop/no-op leaves automatic intent pending.

Reconcile newer typed receipts from **all decoded entries before `ResumeHistory`** against snapshot revisions and originating session ID. Preserve final handoff after the final checkpoint/summary. A stale snapshot cannot repeat a delivered operation or attach cancelled selection to another fold. No old text/tool-call inference. Do not discard concurrent inventory additions absent from the captured selection.
- [ ] **Run green:** red command, remaining named cases, then `go test -race ./agent -run '^(TestSkillCompaction_|TestFoldPublication_)' -count=1`. Document no-op, cancellation, competing winner and restart rules.
- [ ] **Commit** named paths with `feat(agent): bind skill reload intent to fold publication`.

### Task 9: Reload current sources and emit complete fallback inventory

**Files:** Create `agent/session_skill_reload.go`, `agent/session_skill_reload_test.go`; modify `agent/session_skill_delivery.go`, `agent/session_skill_compaction.go`, `agent/session_model_call.go`, `agent/session_stream.go`, `agent/schema/skill_lifecycle.go`, `agent/session_init.go`, `docs/skills.md`.

**Interfaces:** Consumes published handoff receipts, Task 4 preparation and Task 5 admission. Add:

```go
func (s *Session) prepareCompactedSkillReloads(ctx context.Context) (*skillActivationBatch, []schema.SkillActivationOutcome, error)
func (s *Session) skillInventorySummary(ctx context.Context) ([]schema.SkillInventorySummary, []skill.Diagnostic)
```

Summary availability values: `permanent`, `already_present`, `reloadable`, `requires_user_activation`, `unavailable`. Both provenances are represented when a name has preload and ordinary state. Exact reload selection targets the ordinary record if present, otherwise preload-only selection is a no-op.

Read pending handoff receipts even when no accepted operation owns the winning fold. Thus an ordinary automatic compaction with no elicited choice still emits the complete reminder, and a competing winner's handoff can accompany a terminal forced-cancellation notice. Delivered receipts are removed by publication identity after durable admission; that cannot clear an unrelated pending operation.

- [ ] **Write red provider-boundary reload tests.** Activate a real fixture, accept/publish compaction, change its bytes and flags on disk, then assert the next actual provider request contains the new complete opaque instructions. Assert old/new digests and both flag values in typed outcomes. Delete the original source while creating a replacement winner elsewhere; require explicit failure without retargeting. Test absent/invalid reminder with every inventory entry and no bodies, explicit `[]` with no reminder, preload-only no-op, and same-name dual provenance.

The ordered priority test must use a real small-window profile and actual token estimator: a new explicit activation fits, selected reload A fits, reload B exceeds remaining budget. Assert new body and A are complete, B has `context_budget`, and no second compaction/model repair round occurs. Also test mandatory metadata/current input alone exceeding budget causes visible dispatch failure.
- [ ] **Run red:** `go test ./agent -run '^TestSkillReload_(CurrentSource|Reminder|Budget|Preload|Repeated)' -count=1`.
- [ ] **Implement operation-directed selection:**

```go
switch operation.Selection.State {
case "valid":
    for _, name := range operation.Selection.Names {
        entry := inventory[name]
        if entry.Ordinary == nil { continue } // Preload-only needs no disk load.
        invocations = append(invocations, skillInvocation{Name: name, Route: "compaction_reload",
            InvocationID: operation.PublicationID + ":" + name, Source: &entry.Ordinary.Identity})
    }
case "absent", "invalid":
    // Build the complete typed metadata notification; never enqueue bodies here.
    summary, diagnostics = s.skillInventorySummary(ctx)
}
```

The variables are local snapshots: `operation` is the published owner, `inventory` its current session inventory, `invocations` is `[]skillInvocation`, and `summary`/`diagnostics` have the return types above. Prepare each selected reload independently with the shared loader. A failed selection cannot erase prior inventory. Preserve selected order and reserve all success/failure/reminder metadata before body admission. Reuse complete content already present in retained tail or restored by another activation; never duplicate it. Failed reloads remain unavailable for dependent work and are visible individually.

Classify reminder entries from current source metadata and actual retained content: authorized same-source continuation can reload regardless of flags; unauthorized hidden-but-user-invocable requires user activation; both flags blocking ordinary routes is unavailable. Permanently present preload is labeled separately. Without `use_skill`, explain the untracked file-read fallback. Keep the full list and fail visibly on metadata-budget exhaustion rather than trimming older names. After final admission, write delivered receipts and clear only the consumed operation; snapshot save and transcript reconciliation prevent repeat delivery after restart.
- [ ] **Run the Stage 3 gate:**

```bash
go test ./agent/internal/contextmgr ./agent/schema -run '^Test' -count=1
go test ./agent -run '^(TestSkillActivation_|TestSkillDelivery_|TestSkillReload|TestSkillCompaction_|TestPinnedNote_|TestMaybeElicitNoteBeforeCompaction_|TestApplyPendingForceCompact_|TestSessionCompact_|TestFoldPublication_|TestPrepareModelRequest_)' -count=1
go test -race ./agent -run '^(TestSkillCompaction_|TestSkillDelivery_|TestSkillReload_|TestFoldPublication_)' -count=1
```

Review final-fold handoff and budget evidence before enabling client selection.
- [ ] **Commit** named task paths with `feat(agent): reload selected skills after compaction`.

## Stage 4: Explicit client selection

### Task 10: Extend canonical AppWire input, capability and generated SDK

**Files:** Modify `appwire/types.go`, `appwire/input.go`, `appwire/input_test.go`, `appwire/doc.go`, `server/appwire_runtime.go`, `cmd/evener-hub/app_threadread.go`; regenerate `docs/appwire-protocol.md`, `cmd/evener-hub/frontend/src/protocol/types.gen.ts`; modify `docs/skills.md`. Extend `internal/appwirets/emit_test.go` and `internal/appwiredoc/main_test.go`; create `cmd/evener-hub/frontend/src/protocol/skillInput.test.ts` to verify canonical input serialization and the generated capability type.

**Interfaces:** Existing `InputItem.Name` already exists. Add `SkillInput bool` with `json:"skillInput,omitempty"` to `ThreadCapabilities`. Add:

```go
func (i *InputItem) UnmarshalJSON(data []byte) error
func ValidateSkillInputSupport(items []InputItem, supported bool) error
```

Normalize `{Type:"skill", Name:canonical}` alongside existing text/images. Exact catalog identity and invocation policy remain consumption-time checks. Add current flags and availability to `EvenerSkillInfo`, and source/category/field diagnostics to `EvenerDiagnostics` through a new `EvenerSkillDiagnostic` DTO. Keep completion identities/path-free catalog separate from explicit diagnostic source details. Copy values from Stage 1; no duplicated parser or policy in AppWire.

The exact additions to `EvenerSkillInfo` are `DisableModelInvocation bool` (`disableModelInvocation`), `UserInvocable bool` (`userInvocable`), `Available bool` (`available`), and `AllowedTools []string` (`allowedTools,omitempty`). Serialize both boolean values without omission. `EvenerSkillDiagnostic` has string fields `Category`, `Name`, `Source`, `OtherSource`, `Field`, `Message`, using camelCase JSON names. Add `SkillDiagnostics []EvenerSkillDiagnostic` (`skillDiagnostics,omitempty`) to `EvenerDiagnostics`. Composer completion uses `Available && UserInvocable` and still requires actual target `SkillInput` before selection submission.

- [ ] **Write red normalization and raw-JSON tests:**

```go
func TestNormalizeMutationInputSkillSelection(t *testing.T) {
    want := []InputItem{{Type: "text", Text: "REQUEST_55a"}, {Type: "skill", Name: "pkg:probe"},
        {Type: "image", MediaType: "image/png", Data: []byte{1, 2}}}
    got, err := NormalizeMutationInput(want)
    if err != nil { t.Fatal(err) }
    if !got.HasContent() || !reflect.DeepEqual(got.Items, want) { t.Fatalf("input=%#v", got.Items) }
    only, err := NormalizeMutationInput(want[1:2])
    if err != nil || !only.HasContent() { t.Fatalf("skill-only=%#v error=%v", only, err) }
}
func TestSkillInputRejectsRawPathAndBody(t *testing.T) {
    for _, raw := range []string{
        `{"type":"skill","name":"pkg:probe","path":""}`,
        `{"type":"skill","name":"pkg:probe","body":"BODY_55a"}`,
        `{"type":"skill","name":"pkg:probe","text":""}`,
    } {
        var item InputItem
        if err := json.Unmarshal([]byte(raw), &item); err == nil { t.Fatalf("accepted=%s", raw) }
    }
}
```

Test all input-bearing RPC parameter decoding, skill-only input, whitespace/empty name, duplicate selections, and non-skill decoding unchanged. Unknown `body` currently disappears through ordinary `json.Unmarshal`, so checking only the Go struct is insufficient.
- [ ] **Run red:** `go test ./appwire -run '^(TestNormalizeMutationInputSkill|TestSkillInput)' -count=1`.
- [ ] **Implement skill-specific raw-key validation:**

```go
func (i *InputItem) UnmarshalJSON(data []byte) error {
    type plain InputItem
    var decoded plain
    if err := json.Unmarshal(data, &decoded); err != nil { return err }
    if decoded.Type == "skill" {
        var fields map[string]json.RawMessage
        if err := json.Unmarshal(data, &fields); err != nil { return err }
        for field := range fields {
            if field != "type" && field != "name" { return fmt.Errorf("skill input rejects field %q", field) }
        }
    }
    *i = InputItem(decoded)
    return nil
}
```

Normalization also rejects populated forbidden struct fields for Go callers, trims/validates nonempty names, and retains canonical items with no body/path. Keep existing text/image semantics. Implement support validation as a pure check: any skill item requires `supported == true`; otherwise return an unsupported-input error. Keep the new capability false until Task 11 wires runtime consumption; that task enables it only for implemented runtime endpoints. Do not invent support for discovered/cold/older endpoints.
- [ ] **Regenerate and run green:**

```bash
go generate ./appwire/...
go test ./appwire/... ./internal/appwirets/... ./internal/appwiredoc/...
make test-api-package
```

Install existing locked protocol/frontend dependencies first if absent. Inspect generated diff and SDK tests for `InputItem`, `skillInput`, diagnostics and serialized selection. Add API documentation example with original text plus canonical skill input, and forbidden body/path examples. Generated outputs and wire change belong in the same commit/stage.
- [ ] **Commit** the named wire/runtime/generated/doc paths and any explicitly added contract test paths with `feat(appwire): add canonical skill input and capability`.

### Task 11: Preserve selections through durable mutation and atomic consumption

**Files:** Modify `agent/session_queue.go`, `agent/session_client_mutation.go`, `agent/session_client_mutation_queue.go`, `agent/session_lifecycle.go`, `agent/input_message.go`, `agent/session_skill_activation.go`, `agent/session_client_mutation_queue_test.go`, `server/appwire_runtime.go`, `server/appwire_mutation_recovery_test.go`, `docs/skills.md`.

**Interfaces:** Add `SkillNames []string` to existing `queuedInput` and `steeringMessage`; these are canonical identities, never bodies. Extend the existing conversion functions with the new field:

```go
func clientMutationInput(text string, images []ImageAttachment, skillNames []string) []appwire.InputItem
```

`queuedInputFromClientMutation`, `combineClientMutationInputs`, `clientSteeringFromSnapshot`, queue-return and drain conversion must preserve this field. Use Task 4 `SkillInputRecord` in persisted failed input before journal payload cleanup. Add an internal consumption entry point:

```go
func (s *Session) prepareSelectedInput(ctx context.Context, input queuedInput, route string) (*skillActivationBatch, error)
```

It calls `Catalog.ResolveExact` and Task 4 preparation with one atomic group tied to the actual input/mutation identity. Route is supplied by the trusted session caller.

- [ ] **Write red real-conversion and mutation tests.** Extend `TestClientMutation_InputShapesMatchDaemonBoundary` with selection-only and mixed text/image/skill input. Drive real start/queue/steer/drain/return and save/restore, asserting original typed inputs, payload hash and causal mutation identity. Same mutation ID with altered selections conflicts; response-loss retry executes once. Queue a selection, change its disk file before consumption, and require new bytes. A missing second selection causes zero new activation successes and zero dependent provider requests; retained failed input keeps both names and prose.

Add a direct loss-boundary assertion:

```go
func TestClientMutation_SkillInputRoundTrip(t *testing.T) {
    items := []appwire.InputItem{{Type: "text", Text: "REQUEST_612"}, {Type: "skill", Name: "pkg:probe"}}
    normalized, err := appwire.NormalizeMutationInput(items)
    if err != nil { t.Fatal(err) }
    input := queuedInputFromClientMutation(clientMutationQueueEntry{Input: normalized.Items})
    got := clientMutationInput(input.Text, input.Images, input.SkillNames)
    if !reflect.DeepEqual(got, items) { t.Fatalf("round-trip=%#v", got) }
}
```

The existing conversion accepts `clientMutationQueueEntry` and returns `queuedInput` with `Text`/`Images`; `SkillNames` is the new field. The semantic assertion is exact input round-trip, not a rendered-text comparison.
- [ ] **Run red:** `go test ./agent -run '^TestClientMutation_.*Skill' -count=1`.
- [ ] **Implement consumption-time selection.** Preserve selections in journal `Payload`, queue `Input` and `PendingExecutions`; existing normalized JSON hashing naturally includes them. Extend emptiness checks so selection-only is content. At actual consumption build one invocation group:

```go
invocations := make([]skillInvocation, 0, len(input.SkillNames))
for _, name := range input.SkillNames {
    invocations = append(invocations, skillInvocation{Name: name, Route: route,
        InvocationID: inputID + ":" + name, ClientMutationID: mutationID, AtomicGroupID: inputID})
}
return s.prepareSkillActivations(ctx, invocations)
```

`inputID` and `mutationID` come from the existing queue/steering ownership record. Preparation, policy and combined final admission are all-or-nothing; roll back no successful inventory because none has been published yet. Failed steering leaves the unrelated in-flight turn running with none of that steering input, including its prose. Persist a prominent failed-steering record before clearing pending execution. It remains available for correction and explicit new-intent retry, never automatically downgraded to text. Preserve attachments and user-facing original input projection through transcript replay.
- [ ] **Run green:** `go test ./agent -run '^TestClientMutation_' -count=1` and `go test ./server -run '^TestAppWireMutation' -count=1`, then Stage 2 provider-boundary activation/delivery families. Include restart before and after mutation claim, queue edit/drain, and response-loss recovery.
- [ ] **Commit** the task's named paths with `feat(agent): preserve selected skills through input consumption`.

### Task 12: Enforce target capability before forwarding selected input

**Files:** Modify `cmd/evener-hub/appwire_validation.go`, `cmd/evener-hub/app_rpc.go`, `cmd/evener-hub/app_relay.go`, `cmd/evener-hub/app_threadlifecycle.go`, `cmd/evener-hub/app_threadread.go`, `cmd/evener-hub/internal/appsource/local_daemon.go`; add `cmd/evener-hub/skill_input_test.go`, `cmd/evener-hub/internal/appsource/skill_input_test.go`; modify `docs/skills.md`.

**Interfaces:** Consumes `ValidateSkillInputSupport`. Existing capability reads are `LocalDaemonSource.ReadThread`, `ReadThreadAtEntry`, `readSpawnedLocalThread`, and `appwire.Client.ThreadRead`. No new capability lookup service.

- [ ] **Write red fail-closed tests** for false/absent/true capability, initial input, resumed start, ordinary start, queue, steer and drain. Assert unknown/unreachable endpoint cannot authorize forwarding. Test actual validator with typed capability data:

```go
func TestSkillInputSupportIsFailClosed(t *testing.T) {
    items := []appwire.InputItem{{Type: "skill", Name: "pkg:probe"}}
    for _, supported := range []bool{false, true} {
        err := appwire.ValidateSkillInputSupport(items, supported)
        if (err == nil) != supported { t.Fatalf("supported=%v error=%v", supported, err) }
    }
    if err := appwire.ValidateSkillInputSupport([]appwire.InputItem{{Type: "text", Text: "REQUEST_712"}}, false); err != nil {
        t.Fatal(err)
    }
}
```

Use real endpoint/connection integration tests for forwarding and runtime requests. Do not add a canned AppWire server as final behavior evidence or stamp an older source true just to satisfy a test. Final positive forwarding is also proved by Task 14's real stack.
- [ ] **Run red:** `go test ./cmd/evener-hub/... -run '^TestSkillInput' -count=1`.
- [ ] **Implement same-connection checks.** Within each existing `withMutationClient` callback, if input contains a skill, call `client.ThreadRead` with `Subscribe:false` and `IncludeTurns:false`, inspect `response.Thread.Evener.Capabilities.SkillInput`, then call the shared validator before forwarding. Keep endpoint/expected-instance ownership checks. Read failure is a rejection, not permission.

```go
if err := appwire.ValidateSkillInputSupport(input, response.Thread.Evener.Capabilities.SkillInput); err != nil {
    return err
}
```

For cold/resumed initial input, reuse the actual `readSpawnedLocalThread` result before `StartTurnAtEntry`, and recheck after replacement/resume. Synthesized roster/cold capabilities remain false unless actual support is known. Apply checks to relay/source paths as well as direct hub handlers. A rejection preserves the selected input and never emits operative slash prose.
- [ ] **Run green:** red command plus `go test ./server/... ./cmd/evener-hub/...`. Document mixed-version fail-closed behavior and recovery.
- [ ] **Commit** named task paths with `feat(hub): gate selected input on target capability`. Record this commit as `PRE_COMPOSER_REF` in the execution evidence for Task 14's browser baseline.

### Task 13: Preserve canonical selections in composer drafts and recovery

**Files:** Modify files under `cmd/evener-hub/frontend/src/`: `panes/session/composer/slashCompletion.ts`, `panes/session/composer/draft.ts`, `panes/session/composer/Composer.tsx`, `panes/session/composer/queue/pendingTurnsStore.ts`, `panes/session/composer/queue/QueueStrip.tsx`, `panes/session/composer/recovery/recoveryDraft.ts`, `stores/composerInput.ts`, `stores/threads.ts`, `stores/mutationOutbox.ts`; modify `panes/session/composer/draft.test.ts`, `panes/session/composer/slashCompletion.test.ts`, `panes/session/composer/recovery/recoveryDraft.test.ts`, `panes/session/composer/queue/pendingTurnsStore.test.ts`; create `panes/session/composer/skillSelections.ts`, `panes/session/composer/skillSelections.test.ts`. Modify `docs/skills.md`.

**Interfaces:** Define and consistently thread these types:

```ts
export interface ComposerDraft { text: string; skillNames: string[] }
export function readComposerDraft(ref: string): ComposerDraft;
export function writeComposerDraft(ref: string, value: ComposerDraft): void;
export function buildInput(text: string, attachments?: readonly InputAttachment[], skillNames?: readonly string[]): InputItem[];
export function buildComposerInput(text: string, attachments?: readonly InputAttachment[], skillNames?: readonly string[]): InputItem[];
```

Add `canonicalName?: string` to `SlashMenuItem`; only skill rows set it. `RecoveredComposerDraft` gains `skillNames: string[]`. Append `currentSkillNames: readonly string[] = []` to `mergeRecoveryComposerDraft`. Append the optional `skillNames` parameter through existing send/queue/steer/drain and update/resend APIs, `SubmitWithPendingTrackingOptions` and submitted/drain snapshots. Canonical selections live in existing mutation `payload.input`; retain `composerText` for attachment anchors. No second outbox/recovery store.

**Existing-draft transition:** Preserve current plain-text drafts when moving the draft value to `{text, skillNames}`; existing text becomes `skillNames: []`, never an inferred slash selection. This is a narrow existing-data transition requiring Jesse's explicit approval with the plan handoff before implementation. Do not add an older-server downgrade or a parallel legacy submission path.

- [ ] **Write red pure-state/serialization tests** using actual functions, including:

```ts
test("recovers canonical selections without converting them to prose", () => {
  const draft = recoveryComposerDraft(recoveryRecord([
    { type: "text", text: "WIRE_818" },
    { type: "skill", name: "pkg:probe" },
    { type: "image", mediaType: "image/png", data: "AQID", name: "proof.png" },
  ], {
    composerText: "REQUEST_818 [image 3]",
    attachments: [durableAttachment(3, "proof.png")],
  }));
  expect(draft.text).toBe("REQUEST_818 [image 3]");
  expect(draft.skillNames).toEqual(["pkg:probe"]);
  expect(draft.attachments).toEqual([{ marker: 3, name: "proof.png", mediaType: "image/png", data: "AQID", pending: false }]);
});
```

`recoveryRecord` and `durableAttachment` already exist in `recovery/recoveryDraft.test.ts`. Test deduplication, union with recovered names, skill-only draft, text edits retaining chips, removing a chip without text insertion, command/skill collision identity, inline pasted tokens staying text, and draft revision changing on chip edits. Use real browser storage for the durable acceptance in Task 14; do not replace the final proof with FakeClient or fake IndexedDB.
- [ ] **Run red:** from `cmd/evener-hub/frontend`, `npx vitest run src/panes/session/composer/skillSelections.test.ts src/panes/session/composer/recovery/recoveryDraft.test.ts`.
- [ ] **Implement canonical selection updates and serialization:**

```ts
export function addSkillSelection(names: readonly string[], canonicalName: string): string[] {
  return names.includes(canonicalName) ? [...names] : [...names, canonicalName];
}
export function removeSkillSelection(names: readonly string[], canonicalName: string): string[] {
  return names.filter((name) => name !== canonicalName);
}
```

Put these functions in the new `panes/session/composer/skillSelections.ts`. Selecting a skill removes only the active completion token and adds its canonical chip. Commands retain existing insertion/execution behavior. Use canonical names as React keys. Accessible remove labels explain that the skill applies to the request independently of later prose edits. Preserve surrounding text and attachment anchors. Show diagnostics in skill details and distinguish command/skill rows.

`buildInput`/`buildComposerInput` append canonical skill items after ordinary text/attachment conversion. Persist structured draft atomically in one localStorage value; after approved transition, decode existing text values to an empty selection list. Keep draft revision ownership and increment on chip changes. Snapshot text, attachments, skills and both existing revisions before send. Successful submission clears only unchanged submitted ownership; later edits survive delayed commit. Wire names into queue editing/return/drain, recovery edit/resend, thread switching and remount. Gate both UI and store submission/resend with `capabilities.skillInput === true`; false/absent capability retains draft and shows a useful unsupported-target failure.
- [ ] **Run green and format:** targeted Vitest, then `npx biome check --write` on each touched `src/` path, followed by `make test-web` from repository root. Never run Biome on out-of-scope guard harness HTML. Verify typecheck sees regenerated protocol types from Task 10.
- [ ] **Commit** this task's explicit touched frontend/test/doc paths with `feat(web): preserve canonical skill selections in composer input`.

### Task 14: Prove production composer behavior through the real hub

**Files:** Create `cmd/evener/skill_browser_helper_test.go`, `cmd/evener-hub/skill_composer_browser_test.go`, `cmd/evener-hub/frontend/scripts/skillguard/run.mjs`; modify `scripts/web/test-web-browser.sh`, `docs/developing-evener/testing.md`. All daemon/helper plumbing is test-only. Production `serve.go`, hub routes and embedded UI need no testing endpoints or alternate clients.

**Interfaces:** New Go files use `//go:build browserguard`; define `TestSkillBrowserDaemonHelper` and `TestSkillComposerBrowser`. New helper-only test flag `-skill-browser-config` names a private JSON file containing `workDir`, `stateDir`, `runDir`, `requestLog`, and `controlPath`. The helper writes actual provider-request/outcome JSONL to its owned log. Control JSONL commands `hold`, `release`, and `shutdown` synchronize adapter calls and cleanup; they are fixture IPC, never production RPC. The Node driver accepts `--url`, `--artifact-dir`, `--control-path` and `--milestone-path`, documents these in `--help`, and records browser milestones for the Go owner to verify against actual requests/transcripts.

Existing seams: `defaultServeDeps`, `runServeWithDeps`, `waitForServeTestRendezvous`, `shutdownServeTestDaemon`; `NewWebServer(hubcore.WebConfig)`, `web.Handler`, `hubcore.NewRoster(runDir, &hubcore.StatusProber{})`, `roster.RefreshAndWait`, `hubcore.NewPastIndex`, `PastIndex.Rebuild`. Browser helpers are existing `findChrome`, profile isolation/lifecycle functions, `connectPage`, `navigateTo`, and `evaluate` in frontend scripts. Reuse them; `startBrowserGuard` always starts Vite and is unsuitable for the real hub URL.

- [ ] **Write the red real-stack test.** Compile the helper binary with `go test -tags browserguard -c` into `t.TempDir`, start two helper daemons with isolated HOME/XDG/config/state/work/run roots, and use their real rendezvous registration. Start an actual hub with `httptest.NewServer(web.Handler())`, real roster prober and past index. Build/embed production frontend first. Navigate Chrome through existing `/auth/<token>` and then production AppShell/Session/Composer. Use native CDP keyboard/click events, real AppWire, real stores and native IndexedDB.

Initial failing scenario: type surrounding opaque prose plus a completion token, select `pkg:probe`, remove/reselect it, submit, and assert the actual helper's provider request has complete fixture body plus unchanged prose with no operative completion token. Check the canonical selection in the durable mutation and transcript, and inspect accessible chip/remove labels.
- [ ] **Verify the browser baseline and current behavior:**

```bash
make build-web
go test -tags browserguard ./cmd/evener-hub -run '^TestSkillComposerBrowser$' -count=1
```

This task adds final integration evidence after Task 13's TDD implementation, so current behavior should pass. To establish the guard detects the missing feature, use a native isolated worktree at the recorded `PRE_COMPOSER_REF`, copy only these new test/driver files, install its dependencies, build its frontend, and run the same command. Expect the canonical-chip behavior assertion to fail there. Do not alter current source, weaken an assertion, or count a missing browser/daemon as that baseline failure. Dispose the baseline worktree after collecting its result. A launch failure leaves verification incomplete.
- [ ] **Implement only test fixture wiring**, injecting solely the external provider adapter:

```go
deps := defaultServeDeps()
deps.newClient = func(string, io.Writer) (*llm.Client, func() error, error) {
    client := llm.NewClient()
    client.Register(adapter)
    return client, func() error { return nil }, nil
}
err := runServeWithDeps([]string{"--model", "openai/gpt-test", "--addr", "127.0.0.1:0",
    "--dir", workDir, "--state-dir", stateDir, "--run-dir", runDir}, deps)
```

`adapter` implements the real `llm.ProviderAdapter` interface and records every actual request; only it returns scripted model output. Keep default Session creation, server, registration, bridge, routing and persistence. Use the existing serve fixture's shutdown/barrier pattern. The helper must never run without its explicit test flag. Bound every wait with context cancellation and clean up all child processes, Chrome profiles, temporary files and HTTP servers. Keep full failing logs and screenshots in the owned artifact directory.

Add browser scenarios for draft thread-switch/remount, queue edit/return/drain, attachment preservation, failed activation recovery and explicit retry, selected steering, capability loss, and delayed accepted-send versus a newer chip edit. Hold/release the actual adapter through fixture IPC; do not add a fake outbox storage implementation. Fail an activation by removing a test-owned skill source before consumption and prove no dependent request was dispatched. For transport-loss recovery, use browser network controls against the actual hub, restore connectivity, and verify persisted input and same-mutation semantics.

Register this Go browser test in `scripts/web/test-web-browser.sh` after the frontend build/dependency prerequisites so `make test-web-browser` actually runs it. The new build tag keeps Chrome out of default tests. Retain existing guards unchanged; no hand-authored imitation of Composer is acceptance evidence.
- [ ] **Run the Stage 4 gate:**

```bash
go test ./appwire/... ./server/... ./cmd/evener-hub/...
go test ./agent -run '^(TestClientMutation_|TestSkillActivation_|TestSkillDelivery_)' -count=1
make test-api-package
make test-web
make test-web-browser
```

Confirm the browser report names every scenario, has actual provider/mutation evidence, and contains no silent skip. If Chrome is unavailable, this required gate remains incomplete.
- [ ] **Commit** named test/script/doc paths with `test(web): verify skill selection through the real hub`.

## Cross-stage evaluation and delivery

### Task 15: Evaluate live model selection and reload behavior

**Files:** Create `agent/skills_live_test.go`, `docs/research/2026-09-11-skills-lifecycle-evidence.md`; modify `docs/skills.md`, `docs/research/2026-09-10-skills-implementation-study.md` only to link the delivered contract and evidence. Keep the study's historical findings dated and intact.

**Interfaces:** New test uses `//go:build eval`, existing `EVENER_LIVE_TESTS=1` opt-in, and new test flag:

```go
var skillsEvalModel = flag.String("skills-eval-model", "", "registry model selector for explicitly opted-in skill lifecycle evaluations")
```

`TestSkillsLive` runs a known-good smoke on the same selected model before behavior cases. Resolve configuration with existing registry loading and `provider.Resolve`; use actual `llm.Client` providers and `NewSession`. Do not use the hardcoded mini-model `integrationSession` helper. Record structured outcomes, selected model/configuration identifiers without credentials, corpus cases, failure categories and counts.

- [ ] **Add the live corpus and explicit opt-in guard:**

```go
func TestSkillsLive(t *testing.T) {
    if !liveeval.Enabled(os.Getenv(liveeval.OptInEnv)) { t.Skip("explicit live-test opt-in required") }
    if *skillsEvalModel == "" { t.Fatal("-skills-eval-model is required") }
    home, err := os.UserHomeDir()
    if err != nil { t.Fatal(err) }
    stateHome, providersConfig, noUserLayer := liveeval.Paths(envvars.XDGStateHome.Trimmed(), home)
    if noUserLayer { t.Fatal("live evaluation requires the configured provider layer") }
    t.Setenv(envvars.XDGStateHome.Name, stateHome)
    t.Setenv(envvars.EVENERProvidersConfig.Name, providersConfig)
    r, err := registry.Load()
    if err != nil { t.Fatal(err) }
    newCase := func(t *testing.T) *Session {
        t.Helper()
        root := t.TempDir()
        markGitRoot(t, root)
        writeSkillMD(t, root, "probe", "---\nname: pkg:probe\ndescription: fixture procedure\n---\nReturn the opaque marker LIVE_SKILL_a983.\n")
        writeSkillMD(t, root, "second", "---\nname: pkg:second\ndescription: second fixture procedure\n---\nReturn the opaque marker LIVE_SKILL_b742.\n")
        client := llm.NewClient(llm.WithRegistry(r))
        prof, err := provider.Resolve(client.Registry(), *skillsEvalModel)
        if err != nil { t.Fatal(err) }
        sess, err := NewSession(client, prof, execenv.NewLocalExecutionEnvironment(root), SessionConfig{
            AgentsDocPath: filepath.Join(root, "absent-personal-instructions"),
        })
        if err != nil { t.Fatal(err) }
        drained := make(chan struct{})
        go func() { defer close(drained); for range sess.Events() {} }()
        t.Cleanup(func() { sess.Close(); <-drained })
        return sess
    }
    if !t.Run("smoke", func(t *testing.T) {
        sess := newCase(t)
        ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
        defer cancel()
        output, err := sess.ProcessInput(ctx, "Return exactly SMOKE_37ab without using skills.", nil)
        if err != nil || !strings.Contains(output, "SMOKE_37ab") { t.Fatalf("smoke output=%q error=%v", output, err) }
    }) { t.Fatal("live smoke failed; behavior corpus was not run") }
    for _, tc := range []struct { name, input string; want []string }{
        {"operative", "Use /pkg:probe to perform the fixture procedure.", []string{"pkg:probe"}},
        {"quotation", "Quote the literal token '/pkg:probe'; do not invoke it.", nil},
        {"fenced", "Explain this code as text only:\n```text\n/pkg:probe\n```", nil},
        {"path", "Describe these literals: /tmp/pkg:probe/file and https://example.invalid/pkg:probe .", nil},
        {"negation", "Do not use /pkg:probe. Return ACK_927.", nil},
        {"multiple", "Use /pkg:probe and /pkg:second to perform both fixture procedures.", []string{"pkg:probe", "pkg:second"}},
        {"near_miss", "Use /pkg:probex if that exact skill exists. Do not substitute another skill.", nil},
    } {
        t.Run(tc.name, func(t *testing.T) {
            sess := newCase(t)
            ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
            defer cancel()
            if _, err := sess.ProcessInput(ctx, tc.input, nil); err != nil { t.Fatal(err) }
            var got []string
            if state := sess.Meta().Skills; state != nil {
                for name, entry := range state.Inventory {
                    if entry.Ordinary != nil { got = append(got, name) }
                }
            }
            slices.Sort(got)
            want := append([]string(nil), tc.want...)
            slices.Sort(want)
            if !slices.Equal(got, want) { t.Fatalf("activations=%v want=%v", got, want) }
        })
    }
}
```

Import the standard packages used above plus existing `agent/execenv`, `agent/internal/liveeval`, `agent/provider`, `envvars`, `llm`, `llm/registry`, and the blank provider registration import `llm/providers/all`. No scripted responder participates. Extend this runner with the structured-input and two compaction cases below, capturing typed compaction/delivery events instead of discarding them in those cases. After successful smoke, use fresh sessions per case and real temporary skill files whose opaque response marker is known only from that skill body. Assert actual activation/selection/delivery records as the primary oracle; marker output is supplementary proof of use. Never infer success from fluent natural-language answers alone.

Corpus inputs:

| Case | Input shape | Expected tracked behavior |
|---|---|---|
| Operative | `Use /pkg:probe to perform the fixture procedure.` | Model invokes canonical skill |
| Quotation | `Quote the literal token '/pkg:probe'; do not invoke it.` | No activation |
| Fenced code | Code block containing `/pkg:probe`, with a request to explain the text | No activation |
| Path/URL | A path and URL containing `/pkg:probe` | No activation from these literals |
| Negation | `Do not use /pkg:probe.` | No activation |
| Multiple | Operative request for two named skills | Both canonical activations |
| Near miss | Operative request containing a nonexistent similar name | No false activation of the real name |
| Structured inline | Ordinary prose plus actual canonical input selection | Runtime activation independent of text position |
| Reload selection | Two loaded skills, task continues only one after actual compaction | Valid relevant selection; needed body delivered |
| Fallback reminder | Actual compaction with absent selection, next task needs one previously loaded skill | Model invokes relevant reloadable skill |

For useful reload choice, define expected relevant set before running the model and report extra/missing names rather than moving the oracle after seeing output. Record every run, including refusals, malformed selections and model failures. Do not silently rerun until green. Evaluate opaque fixture requests, not ambient project secrets.

- [ ] **Verify deterministic exclusion first:** `go test -tags eval ./agent -run '^TestSkillsLive$' -count=1` with `EVENER_LIVE_TESTS` unset must report an explicit skip and no provider request. Compile all eval-tagged code with the repository lint-eval gate. This skip proves isolation, not live behavior.
- [ ] **Run live smoke then corpus:**

```bash
: "${SKILLS_EVAL_MODEL:?Set SKILLS_EVAL_MODEL to a verified configured registry model selector}"
EVENER_LIVE_TESTS=1 go test -tags eval ./agent -run '^TestSkillsLive$' -count=1 -args -skills-eval-model="$SKILLS_EVAL_MODEL"
```

`SKILLS_EVAL_MODEL` is a shell variable used by this command, not a new production environment setting. Obtain the selector from the actual model registry/catalog and verify it before the run. Credentials stay in the provider's normal resolution path. If comparing models, run the smoke on each before interpreting differences. Configuration/quota/network failures, skips and unrun cases are reported separately from behavior failures. Do not fabricate a passing evaluation when blocked.
- [ ] **Complete authoring/user documentation.** Include shared loading, explicit versus typed-inline behavior, original request preservation, atomic failure/retry, invocation matrix, same-source authorization, generic reads, frozen roles, future-only old sessions, source-change notices, complete-body budgets, selection protocol, restart/publication rules, diagnostics, precedence and trust. Correct the inaccurate Codex comparison using the pinned study sources. Link the original study to `docs/skills.md` and the actual evidence without rewriting historical observations.
- [ ] **Commit** named files with `test(agent): evaluate live skill invocation and reload behavior`.

### Task 16: Run final gates, independent review, and open the PR

**Files:** Production/test fixes only where supported by a reproduced failure or review finding; update `docs/research/2026-09-11-skills-lifecycle-evidence.md` with actual commands, exit codes, model/browser results and review resolution. Do not stage unrelated work.

**Interfaces:** No new runtime API. Deliver the single requested PR containing the study, approved spec, this plan, implementation, tests, documentation and evidence.

- [ ] **Prepare and verify the checkout.** Check branch/ref/status, install pinned tools with `make tools` if absent, install locked dependencies using documented frontend/SDK setup. Run Biome on touched `src/` paths only. Confirm no background test/server process is left running. Keep gates serial where the Makefile already orchestrates parallel work; 16 CPUs/62,066 MB are caps, not a reason to run several full suites concurrently.
- [ ] **Run required gates and capture complete evidence:**

```bash
make test-api-package
make vet
make merge-approval-gate
make test-web-browser
```

Run affected race tests from Stages 2/3 as well. Each command must actually exit zero; timeout, denial, launch failure or required skip is incomplete. Preserve stdout/stderr, inspect warnings, and fix the root cause of failures. Expected-error tests must validate their structured error outputs. Re-run the decisive failing case and affected gate after a fix. Do not weaken assertions, filters, pairing or tolerances.
- [ ] **Obtain independent implementation review.** Use `superpowers:requesting-code-review` with the approved spec, plan, exact changed range and evidence. Require review of source identity/policy, final dispatch completeness, generation ownership, stale-snapshot reconciliation, atomic steering, client recovery and real browser coverage. Validate findings against source/tests; fix supported defects with TDD and rerun affected gates. After two unsuccessful fix/review cycles, checkpoint and reslice rather than cycling indefinitely.
- [ ] **Verify every acceptance row below against real evidence.** Record test names/results and live/model/browser limitations. Check generated freshness, clean diff, scoped commits, no scratch artifacts, and that final docs describe implemented behavior rather than promises.
- [ ] **Push and create PR only after the delivery requirements are met.** Verify `git branch --show-current` is `wip/skills-implementation-study`, verify `origin` is `https://github.com/prime-radiant-inc/evener.git`, and determine the repository's actual default base with `gh repo view --json defaultBranchRef --jq '.defaultBranchRef.name'`. Then:

```bash
git push -u origin wip/skills-implementation-study
gh pr create --head wip/skills-implementation-study --base "$PR_BASE" --title "Implement skills lifecycle and explicit selection" --body-file "$PR_BODY"
```

Set `PR_BASE` to that verified default-branch result and write `PR_BODY` in owned scratch from the actual evidence. These are explicit shell variables, not guessed branch names or filesystem paths. The PR body names study/spec/plan, four staged changes, actual deterministic/live/browser checks, review resolution, and any remaining limitation. Verify the resulting URL/head/base with `gh pr view`; do not merge. Remove only scratch files created for this task.
- [ ] **Report** PR URL, branch/commit, gates, live evaluation status and any remaining limitation. If a required gate or live evaluation is blocked, report its exact condition and leave completion unclaimed.

## Acceptance coverage and self-review

This table maps every approved deterministic acceptance area to its implementing task and independent evidence boundary. During execution replace each planned evidence description with the actual test/result reference in the evidence document.

| Spec acceptance area | Tasks | Required evidence |
|---|---|---|
| Shared loading | 2, 5, 11 | Same canonical/source/complete original fixture body at actual provider boundary for all routes |
| Input | 5, 11, 13, 14 | Typed original input, attachments, selection, atomic failure and retry/restart |
| Resolution | 3, 5, 10 | Exact/unique/ambiguous suffixes, collisions, stale identity |
| Completeness | 2, 5 | Full independent fixture bytes or explicit failure, including pre-dispatch fold |
| Deduplication | 5, 9 | Actual outgoing content after projection/fold/fallback; causal corrections; retry/restart |
| Failure | 1, 4, 5, 9, 11 | No false success on read/metadata/policy/budget failure |
| Compaction choice | 6, 7 | Presence table and actual elicitation/nudge inventory inputs |
| Automatic lifetime | 7, 8 | Deferred/no-op/checkpoint-only, note-generation cancellation, no forced restore |
| Elicitation latch | 7 | Selection-only and stale-concurrent-response real barriers |
| Tool availability | 6, 9 | No-tool nudge and automatic choice; untracked read fallback |
| Reload | 2, 9 | Current original bytes, flag/digest changes, no source retargeting |
| Repeated lifetime | 7, 8, 9 | Multiple actual folds and restart, full inventory, consumed receipts |
| Publication | 8 | Winning versus losing receipts, tail/steering preservation, final handoff |
| Invocation policy | 1, 3, 4, 5 | Full matrix, filtered advertisement versus authorized full-catalog lookup |
| Discovery | 3 | Every precedence level, invalid shadow, plugin metadata parity and diagnostics |
| Browser | 13, 14 | Production Composer/native IndexedDB/real hub and request evidence |
| Role lifetime | 4, 5, 9 | Legacy frozen bytes and optional unknown provenance; dual-name selection target |
| Raw file reads | 5 | Full/partial read results create no activation or authorization |
| Budget priority | 5, 9 | New group retained; excess reloads fail individually; no additional fold |
| Pending operation | 7, 8 | On-disk acceptance/phase, visible save failure, no unchanged writes, reconcile receipts |
| Note semantics | 6, 7 | Required note, empty clears, present selection requests compaction |
| Historical sessions | 4, 5, 8 | No backfill, future verified records only |
| Delegates | 5, 8 | New/fork child isolation, own-state resume |
| Protocol support | 10, 12, 13, 14 | Raw forbidden fields; false/absent fail closed; selected drafts retained |
| Live behavior | 15 | Per-model smoke, explicit corpus, actual outcomes and honest failures/skips |
| Documentation/review/PR | 1–16 | Updated contract/generated SDK, complete evidence, independent review, verified PR |

Self-review before handoff: compare all spec sections to this table; check every proposed type/signature against its consumers; verify every modified path exists or is explicitly new; scan for unfinished instructions and undefined helpers; confirm no source/test implementation has been made during planning. Source mapping and baseline tests are evidence for the plan only, not completion of the implementation gates.
