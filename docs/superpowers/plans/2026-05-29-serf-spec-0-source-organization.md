# Spec 0 — Source Organization (split `agent/session.go`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split the ~5,200-line `agent/session.go` into focused same-package files, with zero behavior change and zero public-API change.

**Architecture:** This is a pure mechanical refactor. Go does not care which file in a package a declaration lives in, so moving a `func`/`type`/`const`/`var` between files in `package agent` is behavior-preserving by construction. We move coherent groups of declarations out of `session.go` into new `session_*.go` files (and the existing `session_namer.go`), verifying after each move that the package still compiles and the **entire existing `agent` test suite stays green**. No new packages, no signature changes, no logic edits.

**Tech Stack:** Go 1.25. Tools: `gofmt` (always present), `goimports` (install with `go install golang.org/x/tools/cmd/goimports@latest`; on this machine it lands at `$(go env GOPATH)/bin/goimports`). The existing `agent` test suite (`go test ./agent`, ~70s) is the regression harness.

---

## Why there are no new tests in this plan

Spec 0 changes **no behavior**. TDD's "write a failing test first" does not apply to a pure code-move refactor — there is no new behavior to specify. The safety net is the **existing** suite: every task ends by running `go test ./agent` and requiring it to stay green. If a move accidentally changes behavior (e.g., a decl that was shadowing another, or an `init()` ordering assumption), the existing suite catches it. Do **not** add, delete, or weaken any test in this plan. If a move makes a test fail, the move was wrong — revert and investigate; never edit the test to match.

## The mechanical procedure (identical for every move task)

Each task moves a named set of declarations out of `agent/session.go` into a target file. For every task:

1. **Create (or open) the target file.** New files start with exactly:
   ```go
   package agent
   ```
   (No imports yet — `goimports` adds them in step 4.) For the one existing target (`session_namer.go`), append to the end of the file instead of creating it.
2. **Move each listed declaration**, in full, from `session.go` to the target file. "In full" means the entire `func`/`type`/`const`/`var` block **including its immediately preceding doc comment**. Cut from `session.go`, paste into the target. **Do not edit the moved code** — not the body, not the signature, not the names. Locate each declaration by its name (the line numbers below are from the pre-refactor `session.go` at commit `718db409` and drift as you cut; rely on the names).
3. **Format + fix imports** on both files:
   ```bash
   "$(go env GOPATH)/bin/goimports" -w agent/<target>.go agent/session.go
   ```
   If `goimports` is unavailable, run `gofmt -w agent/<target>.go agent/session.go`, then `go build ./agent` and add/remove imports exactly as the compiler reports (`undefined: X` → add the package providing `X`; `imported and not used` → remove it).
4. **Compile:**
   ```bash
   go build ./agent
   ```
   Expected: no output (success).
5. **Run the regression suite:**
   ```bash
   go test ./agent
   ```
   Expected: `ok  	primeradiant.com/serf/agent  <time>s`. Must stay green.
6. **Confirm formatting is clean:**
   ```bash
   gofmt -l agent/<target>.go agent/session.go
   ```
   Expected: no output (both files already formatted).
7. **Commit** (add files by name — never `git add -A`):
   ```bash
   git add agent/<target>.go agent/session.go
   git commit -m "refactor(agent): extract <concern> into agent/<target>.go"
   ```

Tasks are independent (all same package) but **must run sequentially** — each edits `session.go`, so they cannot be parallelized. A reviewer should confirm each task moved exactly its listed decls and nothing else, and that `git show --stat` for the commit touches only the two expected files.

> **Bucketing note for reviewers:** declarations are grouped by concern. A handful sit near a boundary (e.g. `handleCompactionTurn` is grouped with the namer because it drives naming-from-compaction; `describeImage` is grouped with tools). If, while moving, a declaration clearly belongs in a different listed bucket than assigned here, moving it to that other bucket is acceptable **as long as build + the full suite stay green and it lands in one of this plan's target files** — do not invent new files.

---

## File structure (the decomposition)

`session.go` is reduced to: the `Session` struct itself plus small cross-cutting accessors/controls that do not form an extractable concern (`ID`, `SetModel`, `SetReasoningEffort`, `SetTimeout`, `resolveProfileForRef`, `reapplyProviderSpecificTools`, `applyModelRequestMetadata`, `openAIModelSupports24hPromptCache`, `openAIModelFamilyMatch`, `Communicated`, `CommunicateOutput`, `extractOriginalPrompt`, `appendTurn`, `appendAssistantTurn`, `maybeAutoSave`, `applyThresholdScale`, `TranscriptPath`). Everything else moves to:

| File | Concern |
| --- | --- |
| `session_config.go` | `SessionConfig` and its defaults |
| `session_state.go` | session state enum, closing/abort guards, snapshot/meta |
| `session_events.go` | event emission + hook plumbing |
| `session_prompts.go` | system-prompt building and caching |
| `session_namer.go` *(exists)* | session auto-naming |
| `session_queue.go` | input queue, steering, follow-ups, drain |
| `session_init.go` | construction + startup init |
| `session_tools.go` | tool registry, naming, execution, core-tool registration |
| `session_lifecycle.go` | the turn run-loop, model call/stream, compaction, close |

---

## Task 1: `session_config.go`

**Files:**
- Create: `agent/session_config.go`
- Modify: `agent/session.go`

Move these declarations (by name) from `session.go`:
- `type SessionConfig struct` (~line 73)
- `func (c *SessionConfig) applyDefaults()` (~line 210)

- [ ] **Step 1:** Create `agent/session_config.go` with `package agent`.
- [ ] **Step 2:** Move the two declarations above (with doc comments) from `session.go` into it. Do not edit them.
- [ ] **Step 3:** `"$(go env GOPATH)/bin/goimports" -w agent/session_config.go agent/session.go`
- [ ] **Step 4:** `go build ./agent` → expect success (no output).
- [ ] **Step 5:** `go test ./agent` → expect `ok  primeradiant.com/serf/agent`.
- [ ] **Step 6:** `gofmt -l agent/session_config.go agent/session.go` → expect no output.
- [ ] **Step 7:** Commit:
  ```bash
  git add agent/session_config.go agent/session.go
  git commit -m "refactor(agent): extract SessionConfig into agent/session_config.go"
  ```

## Task 2: `session_state.go`

**Files:**
- Create: `agent/session_state.go`
- Modify: `agent/session.go`

Move (by name):
- `type SessionState string` and its `const (...)` value block — `SessionIdle`/`SessionProcessing`/`SessionAwaitingInput`/`SessionClosed` (~lines 31–38)
- `func (s *Session) State()` (~834)
- `func (s *Session) Snapshot()` (~841)
- `func (s *Session) Meta()` (~860)
- `func (s *Session) ContextPressure()` (~1205)
- `func (s *Session) closingOrClosedLocked()` (~1215)
- `func (s *Session) setStateIfOpenLocked(state SessionState)` (~1219)
- `func (s *Session) abortIfClosing(ctx context.Context)` (~1226)
- `func (s *Session) errIfClosing()` (~1239)
- `func (s *Session) abortResponseProcessing(ctx context.Context)` (~1249)
- `func (s *Session) withResponseSideEffects(ctx context.Context, fn func())` (~1263)
- `func (s *Session) isClosingOrClosed()` (~1273)

- [ ] **Step 1:** Create `agent/session_state.go` with `package agent`.
- [ ] **Step 2:** Move the declarations above (with doc comments). Do not edit them.
- [ ] **Step 3:** `"$(go env GOPATH)/bin/goimports" -w agent/session_state.go agent/session.go`
- [ ] **Step 4:** `go build ./agent` → expect success.
- [ ] **Step 5:** `go test ./agent` → expect `ok`.
- [ ] **Step 6:** `gofmt -l agent/session_state.go agent/session.go` → expect no output.
- [ ] **Step 7:** Commit:
  ```bash
  git add agent/session_state.go agent/session.go
  git commit -m "refactor(agent): extract session state/snapshot into agent/session_state.go"
  ```

## Task 3: `session_events.go`

**Files:**
- Create: `agent/session_events.go`
- Modify: `agent/session.go`

Move (by name):
- `func (s *Session) Events()` (~812)
- `func (s *Session) emitSessionStartEnvelope(...)` (~814)
- `func (s *Session) emit(kind EventKind, data any)` (~2430)
- `func warningHookMessage(data any)` (~2454)
- `func (s *Session) runNotificationHook(ctx context.Context, message string)` (~2470)
- `func (s *Session) hookInput(event HookEvent)` (~2484)

- [ ] **Step 1:** Create `agent/session_events.go` with `package agent`.
- [ ] **Step 2:** Move the declarations above (with doc comments). Do not edit them.
- [ ] **Step 3:** `"$(go env GOPATH)/bin/goimports" -w agent/session_events.go agent/session.go`
- [ ] **Step 4:** `go build ./agent` → expect success.
- [ ] **Step 5:** `go test ./agent` → expect `ok`.
- [ ] **Step 6:** `gofmt -l agent/session_events.go agent/session.go` → expect no output.
- [ ] **Step 7:** Commit:
  ```bash
  git add agent/session_events.go agent/session.go
  git commit -m "refactor(agent): extract event emission into agent/session_events.go"
  ```

## Task 4: `session_prompts.go`

**Files:**
- Create: `agent/session_prompts.go`
- Modify: `agent/session.go`

Move (by name):
- `func prependSystemPromptToUserMessage(systemPrompt string, user llm.Message)` (~2492)
- `func (s *Session) refreshSystemPromptCache()` (~3932)
- `func (s *Session) buildPromptData()` (~3937)
- `func (s *Session) renderSystemPrompt()` (~4027)

- [ ] **Step 1:** Create `agent/session_prompts.go` with `package agent`.
- [ ] **Step 2:** Move the declarations above (with doc comments). Do not edit them.
- [ ] **Step 3:** `"$(go env GOPATH)/bin/goimports" -w agent/session_prompts.go agent/session.go`
- [ ] **Step 4:** `go build ./agent` → expect success.
- [ ] **Step 5:** `go test ./agent` → expect `ok`.
- [ ] **Step 6:** `gofmt -l agent/session_prompts.go agent/session.go` → expect no output.
- [ ] **Step 7:** Commit:
  ```bash
  git add agent/session_prompts.go agent/session.go
  git commit -m "refactor(agent): extract system-prompt building into agent/session_prompts.go"
  ```

## Task 5: `session_namer.go` (append to existing file)

**Files:**
- Modify: `agent/session_namer.go` (exists — append)
- Modify: `agent/session.go`

Move (by name) the session auto-naming cluster:
- `func (s *Session) launchInitialPromptNamer(...)` (~895)
- `func (s *Session) clearPromptNamePendingAfterAttempt(err error)` (~922)
- `func (s *Session) launchCompactionNamer(...)` (~930)
- `func (s *Session) nameSessionFromCompactionTurn(...)` (~952)
- `func isSessionNameCompactionTurn(turn Turn)` (~966)
- `func (s *Session) handleCompactionTurn(t Turn)` (~970)
- `func (s *Session) shouldNameFromCompaction()` (~992)
- `func (s *Session) nameSessionFromText(...)` (~1002)
- `func (s *Session) shouldApplySessionName(source string)` (~1048)
- `func (s *Session) shouldApplySessionNameLocked(source string)` (~1054)
- `func (s *Session) appendSessionNamerLog(entry SessionLogEntry)` (~1070)
- `func sessionNameSourceLabel(source string)` (~1087)

- [ ] **Step 1:** Open `agent/session_namer.go` (do not recreate the `package agent` line; append after the existing content).
- [ ] **Step 2:** Move the declarations above (with doc comments) from `session.go` to the end of `session_namer.go`. Do not edit them.
- [ ] **Step 3:** `"$(go env GOPATH)/bin/goimports" -w agent/session_namer.go agent/session.go`
- [ ] **Step 4:** `go build ./agent` → expect success.
- [ ] **Step 5:** `go test ./agent` → expect `ok`.
- [ ] **Step 6:** `gofmt -l agent/session_namer.go agent/session.go` → expect no output.
- [ ] **Step 7:** Commit:
  ```bash
  git add agent/session_namer.go agent/session.go
  git commit -m "refactor(agent): consolidate session auto-naming into agent/session_namer.go"
  ```

## Task 6: `session_queue.go`

**Files:**
- Create: `agent/session_queue.go`
- Modify: `agent/session.go`

Move (by name) the input-queue / steering / follow-up / drain cluster:
- `type queuedInputDrainContextKey struct{}` (~357), `type queuedInputDrainConfig struct` (~359), `func WithQueuedInputDrainOnInterrupt(...)` (~366), `func WithQueuedInputDrainOnInterruptHandler(...)` (~373)
- `type steeringMessage struct` (~1408), `func steeringInjectedDataFromMessage(...)` (~1413), `func (s *Session) Steer(msg string)` (~1422), `func (s *Session) SteerWithImages(...)` (~1430)
- `func (s *Session) FollowUp(msg string)` (~1570)
- `type queuedInput struct` (~1584), `func (s *Session) Enqueue(...)` (~1592), `func (s *Session) EnqueueWithImages(...)` (~1604), `func (s *Session) DrainAsSteer(...)` (~1635), `func (s *Session) DrainAsSteerWithInput(...)` (~1643), `func (s *Session) QueueDepth()` (~1692), `func (s *Session) QueuePreview()` (~1705), `func firstQueueLine(msg string)` (~1721), `func queuedEntryPreviewLine(entry queuedInput)` (~1732), `func (s *Session) popQueueHead()` (~1747), `func (s *Session) pushQueueHead(entry queuedInput)` (~1763), `func (s *Session) queueChangedDataLocked()` (~1778)
- `func (s *Session) drainSteering()` (~3699), `func (s *Session) prependSteering(...)` (~3710), `type SteeringEntry struct` (~3724), `func (s *Session) SteeringQueueSnapshot()` (~3732), `func steeringMessageToLLM(entry steeringMessage)` (~3750), `func (s *Session) popFollowUp()` (~3757)
- `func queuedInputDrainContext(ctx context.Context, err error)` (~3441)

- [ ] **Step 1:** Create `agent/session_queue.go` with `package agent`.
- [ ] **Step 2:** Move the declarations above (with doc comments). Do not edit them.
- [ ] **Step 3:** `"$(go env GOPATH)/bin/goimports" -w agent/session_queue.go agent/session.go`
- [ ] **Step 4:** `go build ./agent` → expect success.
- [ ] **Step 5:** `go test ./agent` → expect `ok`.
- [ ] **Step 6:** `gofmt -l agent/session_queue.go agent/session.go` → expect no output.
- [ ] **Step 7:** Commit:
  ```bash
  git add agent/session_queue.go agent/session.go
  git commit -m "refactor(agent): extract input queue/steering into agent/session_queue.go"
  ```

## Task 7: `session_init.go`

**Files:**
- Create: `agent/session_init.go`
- Modify: `agent/session.go`

Move (by name) construction + startup init:
- `func selectStrategy(cfg SessionConfig, cm *ContextManager, sess *Session)` (~384)
- `func NewSession(...)` (~410)
- `func RestoreSession(...)` (~545)
- `func RestoreSessionFromMeta(...)` (~677)
- `func (s *Session) initSessionState(sessionStartKind SessionStartKind)` (~4098)
- `func (s *Session) validateModelFallbacks()` (~4209)
- `func modelFallbackEligible(err error)` (~4243)
- `func (s *Session) applyAgentRolePromptOverride()` (~4252)
- `func (s *Session) initPlugins(sessionStartKind SessionStartKind)` (~4271)
- `func (s *Session) initMCP()` (~4328)

- [ ] **Step 1:** Create `agent/session_init.go` with `package agent`.
- [ ] **Step 2:** Move the declarations above (with doc comments). Do not edit them.
- [ ] **Step 3:** `"$(go env GOPATH)/bin/goimports" -w agent/session_init.go agent/session.go`
- [ ] **Step 4:** `go build ./agent` → expect success.
- [ ] **Step 5:** `go test ./agent` → expect `ok`.
- [ ] **Step 6:** `gofmt -l agent/session_init.go agent/session.go` → expect no output.
- [ ] **Step 7:** Commit:
  ```bash
  git add agent/session_init.go agent/session.go
  git commit -m "refactor(agent): extract construction/init into agent/session_init.go"
  ```

## Task 8: `session_tools.go`

**Files:**
- Create: `agent/session_tools.go`
- Modify: `agent/session.go`

Move (by name) the tool registry / naming / execution / core-tool registration cluster:
- `type ctxKey string` (~25), `const ctxToolCallID ctxKey = "toolCallID"` (~28)
- `const defaultAgentName = "default"` (the `const (...)` block at ~40)
- `func (s *Session) resultToolName()` (~826)
- `func (s *Session) RegisterTool(...)` (~1386)
- `func (s *Session) describeImage(ctx context.Context, r ToolExecResult)` (~1484)
- `func (s *Session) canonicalToolName(name string)` (~1805), `func (s *Session) canonicalizeToolNames(names []string)` (~1818), `func (s *Session) providerToolName(name string)` (~1832), `func (s *Session) providerVisibleToolNames(names []string)` (~1843)
- `func (s *Session) execTool(ctx context.Context, call llm.ToolCallData)` (~2060), `func applyUpdatedToolInput(...)` (~2226), `func skippedToolResult(...)` (~2247), `func (s *Session) appendCanceledToolResults(...)` (~2261), `func (s *Session) appendToolResults(...)` (~2300)
- `func (s *Session) customToolDescriptions()` (~3806), `func (s *Session) allToolDefinitions(_ int)` (~3829), `func (s *Session) defaultToolSummaryForAgent(agent PluginAgent)` (~3833), `func (s *Session) availableAgentEntries()` (~3847), `func (s *Session) rebuildToolDefsCache()` (~3873)
- `func registerCoreTools(reg *ToolRegistry, s *Session)` (~4361) — large (~640 lines); move the whole function.
- `type nodeOutput struct` (~5002), `func normalizeNodeOutput(raw any)` (~5009), `func hasMeaningfulNodeOutput(out nodeOutput)` (~5057), `func canonicalNodeOutputText(raw any)` (~5064)
- `func (s *Session) trackReadFile(path string)` (~5074), `func (s *Session) readBeforeWriteWarning(path string)` (~5082), `func (s *Session) resolveFilePath(path string)` (~5097)
- `func (s *Session) getOrCreateTaskStore()` (~5125), `func (s *Session) maybeInjectTaskReminder()` (~5143), `func optionalIntArg(args map[string]any, key string)` (~5176), `func (s *Session) Tasks()` (~5189)

- [ ] **Step 1:** Create `agent/session_tools.go` with `package agent`.
- [ ] **Step 2:** Move the declarations above (with doc comments). Do not edit them. (`registerCoreTools` is large — move the entire function body intact.)
- [ ] **Step 3:** `"$(go env GOPATH)/bin/goimports" -w agent/session_tools.go agent/session.go`
- [ ] **Step 4:** `go build ./agent` → expect success.
- [ ] **Step 5:** `go test ./agent` → expect `ok`.
- [ ] **Step 6:** `gofmt -l agent/session_tools.go agent/session.go` → expect no output.
- [ ] **Step 7:** Commit:
  ```bash
  git add agent/session_tools.go agent/session.go
  git commit -m "refactor(agent): extract tool registry/exec into agent/session_tools.go"
  ```

## Task 9: `session_lifecycle.go`

**Files:**
- Create: `agent/session_lifecycle.go`
- Modify: `agent/session.go`

Move (by name) the turn run-loop / model call / compaction / close cluster:
- Run-loop error values + types: `var errBareTextWithoutResultTool` (~44), `var errEmptyResponseExhausted` (~45), `var errStreamUnavailable` (~46), `type emptyResponseExhaustedError struct` + its `Error()`/`Is()` (~48–58), `type bareTextWithoutResultToolError struct` + its `Error()`/`Is()` (~60–70)
- `func (s *Session) Compact(ctx context.Context)` (~1115), `type steeringTurnRecord struct` (~1138), `func (s *Session) compactionEmitFunc(...)` (~1143), `func (s *Session) runPreCompactHook(...)` (~1159), `func appendSteeringMessagesToHistory(...)` (~1167), `func (s *Session) flushSteeringTurnRecords(...)` (~1180), `func (s *Session) buildCompactionMeta()` (~1192)
- `func (s *Session) Close()` (~1858)
- `func (s *Session) ProcessInput(ctx context.Context, input string, images []ImageAttachment)` (~1949)
- `func (s *Session) maybeWarnContextUsage(msgs []llm.Message)` (~2358), `func messageCharCount(m llm.Message)` (~2388)
- `func (s *Session) processOneInput(...)` (~2503) — large (~900 lines); move the whole function.
- `type sessionModelResponse struct` (~3399), `func (s *Session) callModel(...)` (~3404), `func isTurnCancellation(...)` (~3433), `func streamUnavailable(err error)` (~3464), `func (s *Session) consumeModelStream(...)` (~3474), `func partialJSONStringField(...)` (~3592), `func unquoteJSONUnicodeEscape(...)` (~3644)
- `func (s *Session) stuckEscalation(count int)` (~3674), `func looksLikeQuestion(text string)` (~3770), `func detectLoop(signatures []string, windowSize int)` (~3780)

- [ ] **Step 1:** Create `agent/session_lifecycle.go` with `package agent`.
- [ ] **Step 2:** Move the declarations above (with doc comments). Do not edit them. (`processOneInput` is large — move the entire function body intact.)
- [ ] **Step 3:** `"$(go env GOPATH)/bin/goimports" -w agent/session_lifecycle.go agent/session.go`
- [ ] **Step 4:** `go build ./agent` → expect success.
- [ ] **Step 5:** `go test ./agent` → expect `ok`.
- [ ] **Step 6:** `gofmt -l agent/session_lifecycle.go agent/session.go` → expect no output.
- [ ] **Step 7:** Commit:
  ```bash
  git add agent/session_lifecycle.go agent/session.go
  git commit -m "refactor(agent): extract turn run-loop into agent/session_lifecycle.go"
  ```

## Task 10: Final verification

**Files:**
- Inspect only: all `agent/session*.go`

- [ ] **Step 1:** Confirm `session.go` shrank substantially and holds only the core `Session` type + small accessors:
  ```bash
  wc -l agent/session.go
  ```
  Expected: well under 1,500 lines (down from ~5,200).
- [ ] **Step 2:** Confirm no declaration was lost or duplicated — the package must build and there must be no redeclaration:
  ```bash
  go build ./agent
  ```
  Expected: success (a duplicated decl would fail with "redeclared in this block"; a lost one with "undefined").
- [ ] **Step 3:** Full suite, with the race detector, to catch any accidental behavior/ordering change:
  ```bash
  go test ./agent
  go test -race -run TestSession ./agent
  ```
  Expected: `ok` for both.
- [ ] **Step 4:** Whole-repo build (nothing outside `agent` referenced a moved-but-unexported decl — they can't, but verify the module still builds):
  ```bash
  go build ./...
  ```
  Expected: success.
- [ ] **Step 5:** Formatting clean across the new files:
  ```bash
  gofmt -l agent/session*.go
  ```
  Expected: no output.
- [ ] **Step 6:** Commit any final formatting nits if `gofmt -l` reported files (otherwise skip). There is no code change to commit here if all prior tasks were clean.

---

## Self-Review

**1. Spec coverage.** Spec 0's "Proposed behavior" lists eight target files (`session_config.go`, `session_state.go`, `session_events.go`, `session_queue.go`, `session_lifecycle.go`, `session_init.go`, `session_tools.go`, `session_prompts.go`) — all eight are created by Tasks 1–9 (plus consolidation into the existing `session_namer.go`). Spec 0 step 1 ("split large files inside their existing packages with no behavior changes") and step 2 ("add compile/test checks after each mechanical split") are realized by the per-task `go build`/`go test` gates. Spec 0 steps 3–4 (extract leaf/architectural packages) are **explicitly out of scope** here per the spec's "only promote a seam into a subpackage when imports point one way" and PRI-1938's scope — they are deferred until interfaces are proven by ≥2 callers, and belong to later specs.

**2. Placeholder scan.** No "TBD"/"handle edge cases"/"similar to Task N". Every move task lists its declarations by name; every verification step gives the exact command and expected output. The procedure is stated once and each task repeats its concrete commands (they differ only by filename).

**3. Consistency.** All target filenames match between the file-structure table, the per-task headers, and the commit commands. Every declaration named in a task's move-list comes from the verified `session.go` decl map at commit `718db409`; the "stays in session.go" set is the complement and is enumerated in the file-structure section so nothing is double-assigned. Each named decl appears in exactly one task.
