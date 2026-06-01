# Automatic Session Naming Implementation Plan

Date: 2026-05-20
Branch: `feature/session-auto-naming`
Spec: `docs/specs/2026-05-20-session-auto-naming.md`

## Working rules

- Red-green TDD: every production change starts with the smallest failing test that proves the behavior.
- YAGNI: implement only `fast_cheap_model`, `--fast-cheap-model`, metadata display names, single-call naming, and advisory logging. No manual rename UI, no migration command, no multi-provider cheap client abstraction unless tests force it.
- Small commits: each task below should be independently testable and committable.
- Keep fallback behavior intact: if naming is absent or fails, sessions still display/search by `OriginalPrompt`.
- Do not create an agent/subagent/session for naming. The namer is one cheap LLM call.

## Task 1: Add `fast_cheap_model` to launch config core

**Files:**
- Test: `internal/launchconfig/types_test.go`
- Test: `internal/launchconfig/merge_test.go`
- Test: `internal/launchconfig/args_test.go`
- Modify: `internal/launchconfig/types.go`
- Modify: `internal/launchconfig/merge.go`
- Modify: `internal/launchconfig/args.go`

- [ ] **Red:** add TOML round-trip test for `fast_cheap_model`.
  - Save/load or marshal/unmarshal a `Layer{FastCheapModel: "openai/gpt-5-mini"}`.
  - Expected failure: field does not exist or does not round-trip.

- [ ] **Green:** add field:
  ```go
  FastCheapModel string `toml:"fast_cheap_model,omitempty"`
  ```

- [ ] **Red:** add merge precedence test.
  - global: `openai/gpt-5-mini`
  - repo/project/launch override: `openai/gpt-5-nano`
  - assert effective value and provenance key `fast_cheap_model`.

- [ ] **Green:** merge scalar string in `mergeLayers` exactly like `Model`.

- [ ] **Red:** add `ToArgs` test expecting:
  ```sh
  --fast-cheap-model openai/gpt-5-mini
  ```

- [ ] **Green:** emit `--fast-cheap-model` from `internal/launchconfig/args.go` when set.

- [ ] Run:
  ```sh
  go test ./internal/launchconfig -run 'FastCheapModel|LayerTOML|Merge|ToArgs' -count=1
  ```

## Task 2: Wire `fast_cheap_model` through appwire and hub launch settings

**Files:**
- Test: `internal/launchconfig/wire_test.go`
- Test: `cmd/serf-tui/launch_settings_panel_test.go`
- Test: `cmd/serf-hub/app_launch_test.go`
- Modify: `internal/appwire/types.go`
- Modify: `internal/launchconfig/wire.go`
- Modify: `cmd/serf-tui/launch_settings_panel.go`

- [ ] **Red:** add wire round-trip test.
  - `launchconfig.Layer{FastCheapModel: "openai/gpt-5-mini"}` converts to `appwire.LaunchConfigLayer{FastCheapModel: ...}` and back.

- [ ] **Green:** add `FastCheapModel string 'json:"fastCheapModel,omitempty"'` to appwire layer and update `ToWire`/`FromWire`.

- [ ] **Red:** add TUI layer row test.
  - `layerRows(appwire.LaunchConfigLayer{FastCheapModel: "openai/gpt-5-mini"})` includes field `fast_cheap_model` with the value.

- [ ] **Green:** add a row immediately after `model`:
  ```go
  {"fast_cheap_model", "fast_cheap_model", l.FastCheapModel}
  ```

- [ ] **Red:** add TUI edit test.
  - `applyEdit(layer, "fast_cheap_model", " openai/gpt-5-mini ")` sets trimmed value.

- [ ] **Green:** handle `fast_cheap_model` in `applyEdit`.

- [ ] **Red:** extend hub launch set/get round-trip test to include `FastCheapModel`.

- [ ] **Green:** appwire/wire changes should make this pass; avoid extra hub logic unless required.

- [ ] Run:
  ```sh
  go test ./internal/launchconfig ./cmd/serf-tui ./cmd/serf-hub -run 'FastCheapModel|LaunchSettings|SetLayer' -count=1
  ```

## Task 3: Pass `--fast-cheap-model` through hub daemon spawn

**Files:**
- Test: `cmd/serf-hub/spawn_test.go`
- Modify: likely only covered by `internal/launchconfig/args.go` unless spawn has filtering/validation logic.

- [ ] **Red:** add/extend spawn arg test with resolved effective launch layer:
  ```go
  launchconfig.Layer{
      Model: "openai/gpt-5.5",
      FastCheapModel: "openai/gpt-5-mini",
  }
  ```
  Assert spawned args contain:
  ```sh
  --model openai/gpt-5.5 --fast-cheap-model openai/gpt-5-mini
  ```

- [ ] **Green:** if spawn uses `launchconfig.ToArgs`, Task 1 may already pass. Otherwise wire the new field through the narrowest spawn arg path.

- [ ] Run:
  ```sh
  go test ./cmd/serf-hub -run 'Spawn|FastCheapModel' -count=1
  ```

## Task 4: Add serve flag and same-provider cheap model override

**Files:**
- Test: `agent/profile_test.go`
- Test: `cmd/serf/serve_test.go` or narrow cmdutil/serve helper test if one exists
- Modify: `agent/profile.go`
- Modify: `cmd/serf/serve.go`

Smallest YAGNI decision: support same-provider overrides first. If `--fast-cheap-model` names a different provider than `--model`, return a clear error. Do not build a cross-provider cheap client yet.

- [ ] **Red:** add profile test for a cheap model override helper.
  - Preferred minimal API:
    ```go
    profile = agent.WithFastCheapModel(profile, "gpt-5-mini")
    if profile.CheapModel() != "gpt-5-mini" { ... }
    ```
  - If an existing profile option pattern is better, use that instead.

- [ ] **Green:** add an optional `cheapModelOverride` to `baseProfile` and preserve it across `WithModel` rebuilds.

- [ ] **Red:** add serve flag parsing/resolution test.
  - `--model openai/gpt-5.5 --fast-cheap-model openai/gpt-5-mini` resolves to active profile ID `openai` and cheap model `gpt-5-mini`.
  - Use the smallest testable helper. If `runServe` is too integration-heavy, extract a tiny helper such as `resolveFastCheapModelOverride(activeProvider, flag string) (string, error)`.

- [ ] **Green:** add `fs.String("fast-cheap-model", "", ...)` in `cmd/serf/serve.go`, resolve it, and apply the override before session creation/restore.

- [ ] **Red:** add mismatch test.
  - `--model openai/gpt-5.5 --fast-cheap-model anthropic/claude-haiku...` returns a clear error.

- [ ] **Green:** validate provider match for now. Document in error that cross-provider fast cheap model calls are not supported yet.

- [ ] Run:
  ```sh
  go test ./agent ./cmd/serf -run 'CheapModel|FastCheapModel|Serve' -count=1
  ```

## Task 5: Add session metadata name fields and display helper

**Files:**
- Test: `agent/snapshot_test.go`
- Modify: `agent/snapshot.go`

- [ ] **Red:** add metadata JSON round-trip test for:
  - `Name`
  - `NameSource`
  - `NameUpdatedAt`

- [ ] **Green:** add fields to `SessionMeta`.

- [ ] **Red:** add display helper test:
  1. name wins over original prompt
  2. original prompt wins over ID
  3. ID used when both are blank
  4. whitespace is trimmed

- [ ] **Green:** add `SessionDisplayName(meta SessionMeta) string`.

- [ ] Run:
  ```sh
  go test ./agent -run 'SessionMeta|SessionDisplayName' -count=1
  ```

## Task 6: Update hub display/search to prefer generated names

**Files:**
- Test: `cmd/serf-hub/past_test.go`
- Test: tree/thread title test if present for `cmd/serf-hub/tree.go`
- Modify: `cmd/serf-hub/past.go`
- Modify: `cmd/serf-hub/tree.go`

Smallest step: use generated name for display and in-memory search first. Only touch FTS schema if an existing test proves FTS must include it in this increment.

- [ ] **Red:** add display/title test showing `Name` is preferred over `OriginalPrompt`.

- [ ] **Green:** use `agent.SessionDisplayName(meta)` in title/rendering code.

- [ ] **Red:** add past search test:
  - meta name: `Launch Config Cheap Model`
  - original prompt: unrelated
  - search `cheap model` finds it.

- [ ] **Green:** include `meta.Name` in search matching. If search is FTS-only, add `name` to the FTS table with the smallest compatible rebuild path.

- [ ] Run:
  ```sh
  go test ./cmd/serf-hub -run 'Past|Tree|Search|DisplayName' -count=1
  ```

## Task 7: Add advisory session-log entries

**Files:**
- Test: `agent/session_log_test.go`
- Modify: `agent/session_log.go`

- [ ] **Red:** add JSON round-trip test for `SessionLogEntry{Kind: "advisory"}`.

- [ ] **Green:** add:
  ```go
  Kind string `json:"kind,omitempty"`
  ```

- [ ] **Red:** add `SessionLog.String()` behavior test.
  - Decide one of two minimal options:
    - skip advisory entries from context string, or
    - render them as `[session_namer advisory]`.
  - Recommended for YAGNI/privacy: skip advisory entries from `String()` because persisted observability is enough.

- [ ] **Green:** skip `Kind == "advisory"` in `String()`.

- [ ] Run:
  ```sh
  go test ./agent -run 'SessionLog.*Advisory|SessionLog' -count=1
  ```

## Task 8: Implement pure session name generation helper

**Files:**
- Test: `agent/session_namer_test.go`
- Add: `agent/session_namer.go`

Keep this pure and synchronous first. Do not hook it into `Session` yet.

- [ ] **Red:** test sanitization:
  - trims whitespace
  - strips wrapping quotes/backticks
  - takes first non-empty line
  - rejects empty/generic/too-long output

- [ ] **Green:** implement `sanitizeSessionName(raw string) (string, bool)`.

- [ ] **Red:** test prompt request shape with fake LLM client if existing tests can fake `llm.Client.Complete`.
  - If `llm.Client` is hard to fake, keep generation helper thin and test the request-building function separately:
    ```go
    buildSessionNameRequest(profile, source, input) llm.Request
    ```

- [ ] **Green:** implement the smallest helper that builds one cheap-model request with no tools and small output budget.

- [ ] Run:
  ```sh
  go test ./agent -run 'SessionName|Namer|Sanitize' -count=1
  ```

## Task 9: Hook initial prompt naming, synchronously-testable core first

**Files:**
- Test: `agent/session_namer_test.go` or `agent/session_test.go`
- Modify: `agent/session.go`
- Modify: `agent/session_namer.go`

YAGNI approach: add a small method that can be called synchronously in tests, then wrap it in an async launcher in production.

- [ ] **Red:** test applying a prompt-derived name updates metadata fields only when current name is empty.
  - No LLM call needed for this test; test `applyGeneratedSessionName(name, "prompt")`.

- [ ] **Green:** implement source priority and metadata update under session lock.

- [ ] **Red:** test advisory log success entry from name application.

- [ ] **Green:** append advisory `session_namer` entry after successful update.

- [ ] **Red:** test first user prompt hook calls the namer launcher once.
  - Use an injectable function field on `Session` only if necessary for testing.
  - Keep it private and nil by default.

- [ ] **Green:** after the first real user input is appended, launch prompt naming with a short timeout.

- [ ] Run:
  ```sh
  go test ./agent -run 'SessionName|InitialPrompt|Advisory' -count=1
  ```

## Task 10: Hook compaction-derived renaming

**Files:**
- Test: `agent/session_namer_test.go` or `agent/context_manager_test.go`
- Modify: `agent/session.go`
- Modify: `agent/session_namer.go`

- [ ] **Red:** test source priority:
  - prompt name exists
  - summary/checkpoint name overwrites it
  - manual name is not overwritten

- [ ] **Green:** implement priority comparison:
  ```text
  manual > summary/checkpoint > prompt > empty
  ```

- [ ] **Red:** test `OnCompactionTurn` path invokes compaction naming for `TurnSummary` and/or `TurnCheckpoint`.

- [ ] **Green:** extend existing `contextMgr.OnCompactionTurn` callback to trigger naming after existing transcript/reminder behavior.

- [ ] Run:
  ```sh
  go test ./agent -run 'Compaction.*Name|SessionName.*Priority|OnCompactionTurn' -count=1
  ```

## Task 11: End-to-end narrow integration test

**Files:**
- Test: choose the narrowest package that can cover config-to-session naming without a live provider, probably `agent` plus launch/serve unit coverage already added.

- [ ] **Red:** add one integration-style fake-client test:
  - create session with state dir
  - process initial prompt or directly exercise the hook
  - fake cheap response returns `Launch Config Cheap Model`
  - assert meta file has `name`
  - assert session log has advisory entry

- [ ] **Green:** fix only what this test exposes.

- [ ] Run focused tests:
  ```sh
  go test ./internal/launchconfig ./internal/appwire ./cmd/serf-tui ./cmd/serf-hub ./cmd/serf ./agent -run 'FastCheapModel|SessionName|Advisory|DisplayName' -count=1
  ```

## Task 12: Final verification

- [ ] Run package tests for touched areas:
  ```sh
  go test ./internal/launchconfig ./internal/appwire ./cmd/serf-tui ./cmd/serf-hub ./cmd/serf ./agent -count=1
  ```

- [ ] Run formatting:
  ```sh
  gofmt -w <changed .go files>
  ```

- [ ] Check workspace:
  ```sh
  git status --short
  ```

- [ ] Confirm no unrelated dirty files were modified.

- [ ] Commit in small logical commits, for example:
  1. `launchconfig: add fast cheap model setting`
  2. `serve: wire fast cheap model override`
  3. `agent: add session display names`
  4. `agent: log advisory session naming`
  5. `agent: generate session names from prompt and compaction`
  6. `hub: display and search generated session names`

## Deferred work

Explicitly do not implement in this feature unless later requested:

- cross-provider cheap side-call client if same-provider validation is acceptable
- manual rename UI/API
- bulk backfill of old session names
- user-configurable naming prompt
- naming-specific model separate from `fast_cheap_model`
- surfacing naming progress in live UI
