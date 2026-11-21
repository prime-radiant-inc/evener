# Atomic Multi-Edit for `edit_file` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade `edit_file` (and `memory_edit`) to an atomic, all-or-nothing single-file `edits[]`-only schema with original-file non-overlapping matching, schema-generic 5-phase argument repair, byte-faithful CRLF/BOM handling, atomic unconfined writes, consistent client diff rendering, and a 3-arm `evener-fluency` evaluation suite.

**Architecture:** Stage 1 lands the new `BatchFileEditor` execution engine in `agent/execenv` behind the existing `EditFileWithCtx` entry point and upgrades all Go/TypeScript transcript consumers to decode canonical `edits[]`, pre-repair shapes, and legacy top-level calls while keeping every existing test green. Stage 2 atomically switches `DefEditFile()`/`DefMemoryEdit()`, `system.md.tmpl`, 5-phase `repair.RepairArgs`, `execFileEditWith`/`execMemoryEdit`, test fixtures, and `docs/tools/memory.md`. Stage 3 adds the 7 multi-edit `evener-fluency` probes and Arm B prompt variant for live multi-model evaluation.

**Tech Stack:** Go (`agent/execenv`, `agent/internal/tool`, `agent/internal/tool/repair`, `agent`, `tools/tool-fluency`), TypeScript/React/React Native (`appwire-client/typescript`, `cmd/evener-hub/frontend`, `mobile-native`), Vitest, Biome.

**Spec:** `docs/superpowers/specs/2026-11-21-edit-file-multi-edit-design.md`

## Global Constraints

- Single flat root JSON Schema object (Anthropic strips root `oneOf`/`anyOf`/`allOf`; Gemini strips `additionalProperties` and `$`-meta; every property and nested item property must carry a `description` for `TestAllBuiltinParametersCarryDescriptions`).
- Single-file tool invariant stays intact (`escalatableTools`, web `openBesidePath: filePathArg`, `memory_edit` scope confinement and LF stamping via `frontmatter.SetField`).
- `execenv.ExecutionEnvironment` method signatures stay unchanged; batch support is an optional `BatchFileEditor` capability interface.
- Never assert on error-message phrasing, tool-description prose, or human-readable copy (`docs/developing-evener/testing.md`); assert behavior and structural outcomes instead.
- All frontend/AppWire/mobile changes respect `@evener/appwire-client` package-name imports (`make lint-package-imports`), Biome formatting from `cmd/evener-hub/frontend`, and never run `npm ci` through a symlinked `node_modules`.
- `RepairArgs` must never mutate its caller's input `args` map or any nested slice/map (`FuzzRepairArgs`).
- No Windows-only work, findings, or checks (Linux and Darwin only).

## Review Focus

1. **Multi-byte UTF-8 self-overlapping needles:** `idx + 1` byte advance for `!replace_all` uniqueness must safely detect overlapping UTF-8 occurrences (e.g. `"αβα"` in `"βαβα"`) without false mid-rune matches or panics. Pinned in Task 2.
2. **Malformed elements inside `edits[]`:** `[null]`, `["str"]`, or `[{"old_string": 123, "new_string": "b"}]` must fail cleanly without panicking in `RepairArgs`, `parseFileEdits`, `transcript_render.go`, and `editOperationsOf`. Pinned in Tasks 3, 4, 6, and 7.
3. **Symlink parity vs confinement:** unconfined `writeFileAtomic` on `OsFs` updates a symlink's target while keeping the symlink intact (never touching host filesystem on `MemMapFs`/`BasePathFs`), while confined `sandboxFS.writeFile` continues to refuse symlinks. Pinned in Task 2.
4. **CRLF/LF inside `ed.OldString`/`ed.NewString` on mixed-ending files:** normalizing `\r\n` -> `\n` on input strings while preserving untouched bytes outside matched spans must keep untouched LF and CRLF segments verbatim. Pinned in Task 2.
5. **Malformed vs legacy vs repaired `memory_edit` rendering:** `MemoryEditBody` and mobile `memoryEvidence` must fall back to generic display on `{file_path: "a.md"}` while rendering diffs for canonical `edits[]`, stringified/bare/aliased pre-repair calls, and legacy top-level calls. Pinned in Tasks 4 and 5.

---

## File Map

| File | Responsibility |
| --- | --- |
| `agent/execenv/local_edit_match.go` | Offset-returning `findFuzzyMatchSpans`, `byteSpan`, line-numbered `nearestFileRegion` |
| `agent/execenv/execenv.go` | `FileEdit` struct and optional `BatchFileEditor` interface |
| `agent/execenv/local_files.go` | `MaxEditFileSpans`, `MaxEditFileOutputBytes`, `EditFileBatchCtx`, `EditFileBatchWithCtx`, `EditFileWithCtx` delegation, `writeFileAtomic`, O(1)-extra-memory LF-to-original endpoint translation |
| `agent/execenv/local_edit_batch_test.go` | Unit/fault/symlink/bounds/mixed-ending tests for batch engine and fuzzy spans |
| `agent/execenv/local_fuzz_test.go` | Updated `FuzzEditFile` CRLF-aware oracle and `FuzzEditFileDifferential` success check |
| `agent/transcript_render.go` | Dual-shape (canonical, pre-repair, legacy) `edit_file` summary rendering |
| `appwire-client/typescript/editDiff.ts` | `EditOperation`, `DiffLineKind`, `editOperationsOf`, `editBatchDiffText`, `classifyDiffLineKinds`, header-aware `diffStats`, 1-line `editDiffText` wrapper |
| `appwire-client/typescript/index.ts` | Export new types/helpers from `@evener/appwire-client` |
| `appwire-client/typescript/toolSummaries.ts` | `editFileWords` using `editOperationsOf` + `editBatchDiffText` |
| `appwire-client/typescript/memorySteps.ts` | `memoryEditWords` using `editOperationsOf` + `editBatchDiffText` |
| `cmd/evener-hub/frontend/src/widgets/diffblock/index.tsx` | Reuse `classifyDiffLineKinds` header-vs-content rule in `parseUnifiedDiff` |
| `cmd/evener-hub/frontend/src/panes/session/transcript/tools/bodies.tsx` | `EditFileBody` using `editOperationsOf` + `editBatchDiffText` |
| `cmd/evener-hub/frontend/src/panes/session/transcript/tools/memoryTools.tsx` | `MemoryEditBody` using `editOperationsOf` |
| `mobile-native/src/session/evidence.ts` | `edit_file` and `memory_edit` evidence using `editOperationsOf` + `editBatchDiffText` |
| `mobile-native/src/session/StepEvidence.tsx` | `Diff` styling via shared `classifyDiffLineKinds` |
| `agent/internal/tool/repair/repair.go` | 5-phase schema-generic repair (deterministic aliases + conflict rejection, single-item promotion + mixed-form preservation, JSON-stringified array coercion, recursive nested repair) |
| `agent/internal/tool/definitions.go` | `defEditFile`, `DefEditFile()`, `DefMemoryEdit()` (`edits[]`-only) |
| `agent/prompts/system.md.tmpl` | 1-sentence `edit_file` batching nudge gated on `HasTool "edit_file"` |
| `agent/session_tools_file.go` | `parseFileEdits`, in-place `execFileEditWith`, `BatchFileEditor` dispatch + 1-edit fallback |
| `agent/session_tools_memory.go` | `DefMemoryEdit()` registration + `execMemoryEdit` via updated `execFileEditWith` |
| `docs/tools/memory.md` | Evergreen update to `memory_edit` table row, Focused correction example, original-file matching, advertised vs repair shape |
| `tools/tool-fluency/probes/edit_file_*.yaml` & `variants/edit-file-parallel-calls.md` | 7 multi-edit probes + Arm B parallel-call prompt variant |

---

### Task 1: Offset-Returning Fuzzy Matcher and Line-Numbered Nearest Region

**Files:**
- Modify: `agent/execenv/local_edit_match.go:1-134`
- Create: `agent/execenv/local_edit_match_test.go`

**Interfaces:**
- Consumes: `eachLine` (`agent/execenv/local_files.go:223`), `splitEditLines`
- Produces:
  - `type byteSpan struct { start, end int }` (package `execenv`)
  - `func findFuzzyMatchSpans(content, oldString string, replaceAll bool, maxSpans int) (spans []byteSpan, ambigLines []int, limitExceeded bool)`
  - `func nearestFileRegion(content, oldString string) string` (now prefixed with 1-based line number `L<n>:`)

- [ ] **Step 1: Write failing tests in `agent/execenv/local_edit_match_test.go`**
  Test cases (asserting returned spans, `ambigLines`, `limitExceeded`, and line-numbered nearest region):
  - `TestFindFuzzyMatchSpans_SingleAndMultiLineOffsets`: verifies exact `content[sp.start:sp.end]` for single-line and multi-line sliding-window hits.
  - `TestFindFuzzyMatchSpans_EmptyNormalizedNeedle`: whitespace-only `oldString` (`"\t  "`) against file with empty lines and no tabs returns `(nil, nil, false)` for both `replaceAll=false` and `replaceAll=true`.
  - `TestFindFuzzyMatchSpans_AmbiguousLineNumbers`: 3 whitespace-variant lines (`"  foo bar"`, `"\tfoo\tbar"`, `"foo   bar"`) with `replaceAll=false` returns `spans == nil`, `ambigLines == []int{1, 3, 5}` (1-based, capped at 10).
  - `TestFindFuzzyMatchSpans_ReplaceAllNonOverlapping`: `replaceAll=true` collects all non-overlapping single-line and multi-line hits and advances past matched multi-line windows.
  - `TestFindFuzzyMatchSpans_MaxSpansLimit`: `maxSpans=0` with `replaceAll=false` and `maxSpans=2` against 3 matches with `replaceAll=true` both return `limitExceeded == true`.
  - `TestNearestFileRegion_IncludesOneBasedLineNumber`: confirms returned snippet carries the 1-based line number of the best matching region.

- [ ] **Step 2: Run tests to verify failure**
  Run: `go test ./agent/execenv -run 'TestFindFuzzyMatchSpans|TestNearestFileRegion'`
  Expected: FAIL (compile error: `findFuzzyMatchSpans` undefined).

- [ ] **Step 3: Implement `byteSpan`, `findFuzzyMatchSpans`, and line-numbered `nearestFileRegion` in `agent/execenv/local_edit_match.go`**
  - Guard `normOld := normalizeWS(oldString); if normOld == "" { return nil, nil, false }`.
  - Stream lines tracking exact `(lineStart, lineEnd, lineNo)` without allocating a whole-file line slice.
  - Require `end > start` on every accepted span; check `len(spans) >= maxSpans` before recording any match (returning `(nil, nil, true)` immediately on overflow).
  - When `!replaceAll`: record the first match's `(span, lineNo)`; if a second match is found, collect up to 10 candidate 1-based line numbers into `ambigLines` and return `(nil, ambigLines, false)`.
  - When `replaceAll`: collect all non-overlapping matches (for multi-line windows, clear/skip the window after a hit so consecutive overlapping windows are not emitted).
  - Keep a thin `findFuzzyMatch(content, oldString string) string` delegating to `findFuzzyMatchSpans` until Task 2 replaces its sole caller in `local_files.go` (so `local_files.go` compiles at this commit).
  - Prefix `nearestFileRegion`'s returned region with its 1-based starting line (`L%d:\n%s` or per-line `L%d: %s`).

- [ ] **Step 4: Run tests to verify pass**
  Run: `go test ./agent/execenv -run 'TestFindFuzzyMatchSpans|TestNearestFileRegion|TestEditFile'`
  Expected: PASS.

- [ ] **Step 5: Commit**
  ```bash
  git add agent/execenv/local_edit_match.go agent/execenv/local_edit_match_test.go
  git commit -m "feat(execenv): return exact byte spans and ambiguity lines from fuzzy matcher"
  ```

---

### Task 2: Atomic Batch Edit Engine, Byte-Faithful CRLF/BOM Splicing, Bounds, and Unconfined Atomic Write

**Files:**
- Modify: `agent/execenv/execenv.go:180-198`
- Modify: `agent/execenv/local_files.go:366-446`
- Modify: `agent/execenv/local_edit_match.go` (remove unused `findFuzzyMatch` wrapper once caller switches)
- Create: `agent/execenv/local_edit_batch_test.go`
- Modify: `agent/execenv/local_fuzz_test.go:44-150`

**Interfaces:**
- Consumes: `byteSpan`, `findFuzzyMatchSpans`, `nearestFileRegion` from Task 1.
- Produces:
  - `type FileEdit struct { OldString string; NewString string; ReplaceAll bool }` (`agent/execenv/execenv.go`)
  - `type BatchFileEditor interface { EditFileBatchCtx(ctx context.Context, path string, edits []FileEdit) (string, error) }` (`agent/execenv/execenv.go`)
  - `var MaxEditFileSpans = 100_000` and `var MaxEditFileOutputBytes int64 = MaxWholeFileReadBytes` (`agent/execenv/local_files.go`)
  - `func (e *LocalExecutionEnvironment) EditFileBatchCtx(ctx context.Context, path string, edits []FileEdit) (string, error)`
  - `func (e *LocalExecutionEnvironment) EditFileBatchWithCtx(ctx context.Context, path string, edits []FileEdit, finish func(edited []byte) []byte) (string, error)`
  - `EditFileWithCtx` delegates to `EditFileBatchWithCtx` with a 1-element slice.

- [ ] **Step 1: Write failing tests in `agent/execenv/local_edit_batch_test.go`**
  Cover every spec and Review Focus behavior (asserting outcomes, file bytes, callback invocation, and index/line-number presence, never exact error phrasing):
  - `TestEditFileBatch_DisjointForwardReverseAdjacent`: forward order, reverse file order, and adjacent touching spans (`[0,3)` and `[3,6)`).
  - `TestEditFileBatch_AtomicityOnMissAndEmpty`: edit 0 matches, edit 1 misses (or empty `OldString`, or empty `edits`) -> error returned, file bytes unchanged, `finish` not called.
  - `TestEditFileBatch_MultiErrorCollectionAndNearestCap`: 4 missing edits report all 4 indices in one error and cap `nearestFileRegion` snippets at 3 with 1-based line numbers.
  - `TestEditFileBatch_OverlapRejection`: partial overlap, nested span, identical duplicate edits, and `replace_all: true` overlapping another edit -> error names both edit indices, file bytes unchanged.
  - `TestEditFileBatch_SelfOverlappingNeedleAndUTF8`: `"ana"` in `"banana"` and `"αβα"` in `"βαβα"` fail as ambiguous when `ReplaceAll=false`, and apply left-to-right non-overlapping replacement (`"bXna"`, `"βX"`) when `ReplaceAll=true`.
  - `TestEditFileBatch_FuzzySubstringGuardAndAmbiguityLines`: line 1 `"prefix  alpha   beta"` + line 2 `"  alpha   beta"` with `"alpha beta"` (`ReplaceAll=false`) fails as ambiguous rather than editing line 1; two whitespace-variant lines fail reporting both 1-based line numbers; whitespace-only fuzzy miss leaves file unchanged; fuzzy `ReplaceAll=true` updates all whitespace-variant lines by offset.
  - `TestEditFileBatch_ExactNoOpAndCancellingEdits`: single no-op AND adjacent cancelling edits (`"abc"` with `"a"->"ab"` + `"bc"->"c"`) return error without calling `finish` or writing; fuzzy whitespace cleanup (`old=="foo"`, `new=="foo"` on `"  foo  "`) succeeds.
  - `TestEditFileBatch_CRLFMixedEndingsAndBOM`: pure CRLF+BOM file preserves BOM and `\r\n`; mixed file `"a\r\nb\nc\r\n"` edited on line 1 preserves untouched `\n` after `b` verbatim; CRLF inside `ed.OldString`/`ed.NewString` handled cleanly.
  - `TestEditFileBatch_ResourceBounds`: save/restore `MaxEditFileSpans` and `MaxEditFileOutputBytes`: batch at exact span limit succeeds; +1 span fails across unique exact, exact `replace_all`, unique fuzzy, and fuzzy `replace_all`; CRLF `replace_all` exceeding `MaxEditFileOutputBytes` fails before writing.
  - `TestEditFileBatch_UnconfinedAtomicWriteAndPermissions`: fault-injected write error (`faultFsEnv`) leaves original bytes intact and cleans up temp file; read-only target (`0o444`) in writable dir is refused; read-only parent dir returns error without modifying target; symlink on `OsFs` updates target while keeping symlink intact; `MemMapFs` and `BasePathFs` stay inside their injected filesystem namespace.
  - `TestEditFileBatch_ConfinedAtomicityAndSymlinkRefusal`: `NewConfinedFileEnvironment` denies read-only/masked paths before matching, writes multi-edit atomically, and continues to refuse symlinks.

- [ ] **Step 2: Run tests to verify failure**
  Run: `go test ./agent/execenv -run TestEditFileBatch`
  Expected: FAIL (compile error: `FileEdit` / `EditFileBatchCtx` undefined).

- [ ] **Step 3: Implement `FileEdit`, `BatchFileEditor`, `EditFileBatchWithCtx`, and `writeFileAtomic`**
  - Add `FileEdit` and `BatchFileEditor` in `agent/execenv/execenv.go`.
  - In `agent/execenv/local_files.go`:
    - Implement `EditFileBatchCtx` and `EditFileBatchWithCtx` following Spec Section 4's exact pipeline:
      1. `fileTarget` + sandbox `checkWritable` first.
      2. Validate `len(edits) > 0` and non-empty `OldString`s before reading.
      3. Read `origBytes`; strip optional UTF-8 BOM (`bomLen`); build LF view `lfText` (zero-copy when `!hasCRLF`); precompute `oldLF`, `newLF`, `lenLF`, `lenCRLF`, `defaultCRLF`.
      4. Single-scan matching (`strings.Index`, `idx+1` uniqueness check for `!ReplaceAll`, `replace_all` non-overlapping loop, `findFuzzyMatchSpans` with substring ambiguity guard), enforcing `MaxEditFileSpans` on every path and collecting all edit errors.
      5. Sort LF spans, check `prev.lfEnd > curr.lfStart` overlap, translate endpoints to `origStart`/`origEnd` in one O(1)-extra-memory forward pass over `body` when `hasCRLF`, set per-span `useCRLF` from `origBytes[origStart:origEnd]` (falling back to `defaultCRLF`).
      6. Compute scalar `outLen`, enforce `MaxEditFileOutputBytes` before expanding any CRLF string or allocating `edited`, splice in one pass into `make([]byte, 0, outLen)`, check `bytes.Equal(edited, origBytes)` before `finish`.
      7. Run `finish` when non-nil; publish via `t.sfs.writeFile` (sandboxed) or `writeFileAtomic` (unconfined: resolve symlink ONLY when `fsys` is `*afero.OsFs`, probe `fsys.OpenFile(resolved, os.O_WRONLY, 0)` and close immediately, stat mode, `afero.TempFile` in parent dir, write/chmod/close/`fsys.Rename`, remove temp on any error).
      8. Format confirmation: `edited <path>: <n> replacement[s][fuzzyNote]` for 1 edit, `edited <path>: <total> replacement[s] across <len(edits)> edits[fuzzyNote]` for >1 edits.
    - Make `EditFileWithCtx` delegate to `EditFileBatchWithCtx` and remove the obsolete `findFuzzyMatch` wrapper.
  - Update `FuzzEditFile` in `agent/execenv/local_fuzz_test.go` so LF exact matches keep the raw `strings.Replace`/`ReplaceAll` oracle while CRLF/BOM inputs assert CRLF-preserving expected bytes, and add a concrete successful-edit assertion to `FuzzEditFileDifferential`.

- [ ] **Step 4: Run full `agent/execenv` tests**
  Run: `go test ./agent/execenv/...`
  Expected: PASS (all existing + new batch + fuzz seed tests green).

- [ ] **Step 5: Commit**
  ```bash
  git add agent/execenv/execenv.go agent/execenv/local_files.go agent/execenv/local_edit_match.go agent/execenv/local_edit_batch_test.go agent/execenv/local_fuzz_test.go
  git commit -m "feat(execenv): add atomic BatchFileEditor engine with CRLF/BOM fidelity"
  ```

---

### Task 3: Dual-Shape Go Transcript Renderer

**Files:**
- Modify: `agent/transcript_render.go:2275-2285`
- Modify: `agent/transcript_render_test.go`

**Interfaces:**
- Produces: internal helper `extractRenderedEditOps(m map[string]any) []renderedEditOp` in `agent/transcript_render.go` decoding:
  - canonical `edits[]` array,
  - JSON-stringified `edits` (array or single edit object),
  - bare edit object under `edits`,
  - item aliases (`old_str`/`oldText`/`old_text`, `new_str`/`newText`/`new_text`, `replaceAll`) with canonical-first precedence and competing-alias rejection,
  - legacy top-level fields only when `edits` key is absent.

- [ ] **Step 1: Write failing tests in `agent/transcript_render_test.go`**
  Add cases to the `edit_file` summary tests covering:
  - canonical 1-edit `edits[]` (matches existing `path (replace A→B chars[, all])` shape),
  - canonical multi-edit `edits[]` (reports edit count and aggregate char counts),
  - repaired stringified JSON `edits`, bare object `edits`, and nested `old_str`/`newText` aliases,
  - competing aliases with different values (falls back cleanly, never panics or picks nondeterministically),
  - malformed `edits: [null, "bad", {"old_string": 123}]`,
  - historical top-level `{file_path, old_string, new_string}` (preserved).

- [ ] **Step 2: Run tests to verify failure**
  Run: `go test ./agent -run TestTranscriptRender`
  Expected: FAIL on new `edits[]` cases.

- [ ] **Step 3: Implement `extractRenderedEditOps` and update `case "edit_file"` in `agent/transcript_render.go`**
  Use deterministic sorted alias lookup matching Spec Section 3's conflict rule (canonical wins; two competing aliases with differing values reject the item).

- [ ] **Step 4: Run transcript render tests**
  Run: `go test ./agent -run 'TestTranscriptRender|FuzzTranscriptRender'`
  Expected: PASS.

- [ ] **Step 5: Commit**
  ```bash
  git add agent/transcript_render.go agent/transcript_render_test.go
  git commit -m "feat(agent): render multi-edit and repaired edit_file calls in transcripts"
  ```

---

### Task 4: Shared TypeScript Edit/Diff Helpers and Header-Aware Classifier (`@evener/appwire-client`)

**Files:**
- Modify: `appwire-client/typescript/editDiff.ts:1-26`
- Modify: `appwire-client/typescript/index.ts:255-258`
- Modify: `appwire-client/typescript/toolSummaries.ts:196-205`
- Modify: `appwire-client/typescript/memorySteps.ts:66-79`
- Create: `appwire-client/typescript/editDiff.test.ts`
- Modify: `appwire-client/typescript/toolSummaries.test.ts`
- Modify: `appwire-client/typescript/memorySteps.test.ts`

**Interfaces:**
- Produces (exported from `@evener/appwire-client`):
  - `export interface EditOperation { readonly oldString: string; readonly newString: string; readonly replaceAll?: boolean; }`
  - `export type DiffLineKind = "header" | "add" | "del" | "context" | "meta";`
  - `export function editOperationsOf(args: Record<string, unknown>): EditOperation[] | null`
  - `export function editBatchDiffText(path: string, edits: readonly EditOperation[]): string`
  - `export function classifyDiffLineKinds(unified: string): DiffLineKind[]`
  - `export function editDiffText(path: string, oldString: string, newString: string): string` (1-line wrapper around `editBatchDiffText`)
  - `export function diffStats(text: string): { added: number; removed: number }` (built on `classifyDiffLineKinds`)

- [ ] **Step 1: Write failing tests in `appwire-client/typescript/editDiff.test.ts`, `toolSummaries.test.ts`, and `memorySteps.test.ts`**
  Cover:
  - `editOperationsOf`: canonical 1-edit and multi-edit `edits[]`; JSON-stringified array/object `edits`; bare object `edits`; nested aliases (`old_str`/`oldText`/`old_text`, `new_str`/`newText`/`new_text`, `replaceAll`); competing aliases with different values -> rejected (`null`); legacy top-level `{old_string, new_string}` when `edits` absent; mixed `{edits: [...], old_string: "..."}` and malformed `[null]` / `{file_path: "a.md"}` -> `null`.
  - `editBatchDiffText`: 1 edit produces identical output to legacy `editDiffText` (no `@@`); N>1 edits insert `@@ edit 1 @@`, `@@ edit 2 @@`, ...
  - `classifyDiffLineKinds` + `diffStats`: deleting/adding Markdown `---` or `+++` lines counts as `del`/`add` (not `header`) in BOTH flat single-edit diffs (after the leading `---`/`+++` header pair) and multi-edit `@@` diffs; `diff --git` / `index ` resets `inHeader`.
  - `editFileSummary` and `memoryEditWords`: aggregate `+A -R` across multi-edit `edits[]`, stringified pre-repair calls, legacy top-level calls, and omit diff detail on malformed calls.

- [ ] **Step 2: Run tests to verify failure**
  Run: `make test-api-package`
  Expected: FAIL (missing exports / failing assertions).

- [ ] **Step 3: Implement `editDiff.ts`, re-exports in `index.ts`, and wire `toolSummaries.ts` + `memorySteps.ts`**
  Keep `editDiffText` as `return editBatchDiffText(path, [{ oldString, newString }]);` and `diffStats` counting `"add"` and `"del"` from `classifyDiffLineKinds(text)`. Format touched files with Biome from `cmd/evener-hub/frontend`: `cd cmd/evener-hub/frontend && npx biome check --write ../../../appwire-client/typescript/editDiff.ts ../../../appwire-client/typescript/editDiff.test.ts ../../../appwire-client/typescript/index.ts ../../../appwire-client/typescript/toolSummaries.ts ../../../appwire-client/typescript/toolSummaries.test.ts ../../../appwire-client/typescript/memorySteps.ts ../../../appwire-client/typescript/memorySteps.test.ts`.

- [ ] **Step 4: Run `make test-api-package`**
  Run: `make test-api-package`
  Expected: PASS.

- [ ] **Step 5: Commit**
  ```bash
  git add appwire-client/typescript/editDiff.ts appwire-client/typescript/editDiff.test.ts appwire-client/typescript/index.ts appwire-client/typescript/toolSummaries.ts appwire-client/typescript/toolSummaries.test.ts appwire-client/typescript/memorySteps.ts appwire-client/typescript/memorySteps.test.ts
  git commit -m "feat(appwire-client): add multi-edit diff helpers and shared header classifier"
  ```

---

### Task 5: Web UI and Mobile-Native Diff Consumers

**Files:**
- Modify: `cmd/evener-hub/frontend/src/widgets/diffblock/index.tsx:54-95`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/bodies.tsx:54-64`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/memoryTools.tsx:34-45`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/editTools.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/memoryTools.test.tsx`
- Modify: `mobile-native/src/session/evidence.ts:255-298`
- Modify: `mobile-native/src/session/StepEvidence.tsx:139-173`
- Modify: `mobile-native/src/session/evidence.test.ts`
- Modify: `mobile-native/src/session/StepEvidence.test.tsx`

**Interfaces:**
- Consumes: `editOperationsOf`, `editBatchDiffText`, `classifyDiffLineKinds`, `DiffLineKind` from `@evener/appwire-client` (Task 4).

- [ ] **Step 1: Write failing tests in web and mobile test files**
  - Web `editTools.test.tsx` & `memoryTools.test.tsx`: multi-edit `edits[]` renders all hunks and `@@ edit 1 @@`/`@@ edit 2 @@` headers; stringified pre-repair `edits` renders its diff; malformed `edit_file` renders `null` and malformed `memory_edit` falls back to `defaultToolBody`; removing a Markdown `---` line renders with `"del"` class in both flat single-edit and multi-edit diffs.
  - Mobile `evidence.test.ts` & `StepEvidence.test.tsx`: `edit_file` and `memory_edit` emit multi-edit diff evidence for canonical `edits[]`, stringified pre-repair `edits`, and legacy top-level args; malformed `memory_edit` falls back to JSON evidence; `StepEvidence` styles `@@ edit 1 @@` muted (`palette.inkLow`) and styles a deleted `---` content line with `diffDel` background in both flat and multi-edit diffs.

- [ ] **Step 2: Run targeted web and mobile tests to verify failure**
  Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/transcript/tools/editTools.test.tsx src/panes/session/transcript/tools/memoryTools.test.tsx` and `cd mobile-native && npx vitest run src/session/evidence.test.ts src/session/StepEvidence.test.tsx`
  Expected: FAIL on new multi-edit and header-like content line cases.

- [ ] **Step 3: Update web `DiffBlock`, `EditFileBody`, `MemoryEditBody`, and mobile `evidence.ts`, `StepEvidence.tsx`**
  - Import `classifyDiffLineKinds`, `editBatchDiffText`, `editOperationsOf` from `@evener/appwire-client` (never relative paths).
  - Have `DiffBlock`'s `parseUnifiedDiff` and mobile `StepEvidence`'s `Diff` consume `classifyDiffLineKinds` so both renderers agree 100% with `diffStats`.
  - Run Biome check/write on touched web files from `cmd/evener-hub/frontend` and on touched mobile files from `mobile-native`.

- [ ] **Step 4: Run web and mobile gates**
  Run: `make test-web` then `make test-native` (one at a time per AGENTS.md) and `make lint-package-imports`.
  Expected: PASS.

- [ ] **Step 5: Commit**
  ```bash
  git add cmd/evener-hub/frontend/src/widgets/diffblock/index.tsx cmd/evener-hub/frontend/src/panes/session/transcript/tools/bodies.tsx cmd/evener-hub/frontend/src/panes/session/transcript/tools/memoryTools.tsx cmd/evener-hub/frontend/src/panes/session/transcript/tools/editTools.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/tools/memoryTools.test.tsx mobile-native/src/session/evidence.ts mobile-native/src/session/StepEvidence.tsx mobile-native/src/session/evidence.test.ts mobile-native/src/session/StepEvidence.test.tsx
  git commit -m "feat(ui): render multi-edit diffs and unify header classification across web and mobile"
  ```

---

### Task 6: 5-Phase Schema-Generic Argument Repair

**Files:**
- Modify: `agent/internal/tool/repair/repair.go:40-495`
- Modify: `agent/internal/tool/repair/repair_test.go`
- Modify: `agent/internal/tool/repair/repair_fuzz_test.go`

**Interfaces:**
- Produces: updated `RepairArgs(params, args map[string]any, keep ...string) (map[string]any, []Change)` with deterministic 5-phase order (Spec Section 3):
  1. Root aliases (sorted alias keys; competing aliases with differing values preserved past `dropUnknown`).
  2. Single-item array promotion when required array-of-objects key `K` is absent (`!hasKey`) and unknown root keys cover `K.items.required`; mixed-form detection when `hasKey` (including `args[K] == nil`) marks conflicting root keys in `preserveUnknown`.
  3. Root coercions (including decoding JSON-stringified arrays/objects for `items.type == "object"` without wrapping plain non-JSON strings, and wrapping bare objects into `[]any{raw}`).
  4. Recursive nested repair on nested objects and object-array elements: `applyAliases` -> `applyCoercions` -> `dropEmptyOptionals` -> `dropUnknown` (prefixing `Change.Field` with `edits[i].`, copy-on-write cloning).
  5. Root `dropEmptyOptionals` and `dropUnknown` (skipping `preserveUnknown` keys).

- [ ] **Step 1: Write failing tests in `agent/internal/tool/repair/repair_test.go`**
  Test cases against a required `edits[]` schema (`additionalProperties: false` on root and items):
  - Legacy-only top-level `{file_path, old_string, new_string, replace_all}` and `{path, old_str, new_str}` promote to `edits: [{...}]`.
  - Mixed `{file_path, edits: [...], old_string: "a", new_string: "b"}` AND `{file_path, edits: null, old_string: "a", new_string: "b"}` preserve conflicting root keys past `dropUnknown` (so schema validation rejects them).
  - Combined quirk: stringified JSON array (and stringified single object, and bare object) containing `old_str`, `newText`, `replaceAll: "true"`, and invented trailing key `notes: "x"` decodes, aliases, coerces, and drops `notes`, leaving canonical `edits[0]`.
  - Competing aliases `{old_string: "a", new_str: "x", newText: "y"}` (canonical absent, differing values) preserve competing keys past `dropUnknown` deterministically; identical values promote once; canonical key wins when present.
  - Plain non-JSON string `edits: "not json"` is NOT wrapped into `[]any{"not json"}`; malformed array items `[null, "str"]` do not panic; typo `old_sting` leaves `old_string` missing; original `args` map and nested slices/maps are never mutated.

- [ ] **Step 2: Run tests to verify failure**
  Run: `go test ./agent/internal/tool/repair/...`
  Expected: FAIL on new 5-phase cases.

- [ ] **Step 3: Implement the 5-phase schema-generic pipeline in `agent/internal/tool/repair/repair.go`**
  - Extend `aliasTable` with `oldText`, `old_text`, `newText`, `new_text`, `replaceAll`.
  - Sort alias keys (`slices.Sorted(maps.Keys(aliasTable))` or pre-sorted slice) so alias resolution is 100% deterministic.
  - Bound `json.Unmarshal` on stringified array values by `MaxToolArgumentBytes`/depth guard; ensure copy-on-write cloning everywhere so `FuzzRepairArgs` non-mutation invariant holds.
  - Extend `FuzzRepairArgs` seeds in `repair_fuzz_test.go` with nested `edits[]`, stringified JSON, and mixed-form inputs.

- [ ] **Step 4: Run repair package tests and fuzz seeds**
  Run: `go test -race ./agent/internal/tool/repair/...`
  Expected: PASS.

- [ ] **Step 5: Commit**
  ```bash
  git add agent/internal/tool/repair/repair.go agent/internal/tool/repair/repair_test.go agent/internal/tool/repair/repair_fuzz_test.go
  git commit -m "feat(repair): generalize 5-phase nested repair and single-item array promotion"
  ```

---

### Task 7: Schema Cutover, Prompt, File/Memory Dispatch, Fixtures, and Evergreen Docs

**Files:**
- Modify: `agent/internal/tool/definitions.go:148-165` (and add `DefMemoryEdit()` beside `DefMemoryRead()`/`DefMemoryWrite()`/`DefMemoryDelete()`)
- Modify: `agent/prompts/system.md.tmpl` (1-sentence batching nudge gated on `{{ if .HasTool "edit_file" }}`)
- Modify: `agent/session_tools_file.go:97-121`
- Modify: `agent/session_tools_memory.go:23-30, 369-375`
- Modify: `docs/tools/memory.md:32-45, 67-83`
- Modify: `agent/internal/tool/definitions_test.go`, `agent/session_tools_file_ctx_test.go`, `agent/session_tool_repair_test.go`, `agent/session_memory_test.go`, `agent/session_memory_writes_test.go`, `agent/session_parity_test.go`, and other raw-dispatch `agent/*_test.go` fixtures calling `edit_file`/`memory_edit` directly.

**Interfaces:**
- Consumes: `FileEdit`, `BatchFileEditor`, `EditFileBatchCtx`, `EditFileBatchWithCtx` (Task 2); 5-phase `RepairArgs` (Task 6).
- Produces:
  - `func DefEditFile() llm.ToolDefinition` (`edits[]`-only, referencing `read_file`/`write_file`)
  - `func DefMemoryEdit() llm.ToolDefinition` (`edits[]`-only + `scope`, referencing `memory_read`/`memory_write`)
  - `func execFileEditWith(args map[string]any, guard readGuard, edit func(path string, edits []execenv.FileEdit) (string, error)) (any, error)`

- [ ] **Step 1: Write failing dispatch, repair-integration, and memory tests**
  Add behavior tests in `agent/` (without asserting prose/error wording):
  - `edit_file` multi-edit end-to-end through `Session` / `prepareToolCall` (disjoint edits, read-before-write warning once, atomicity on failure).
  - `edit_file` against a minimal `ExecutionEnvironment` fake without `BatchFileEditor`: 1-edit call falls back to `EditFileCtx`/`EditFile`; >1-edit call returns an error without calling `EditFile`.
  - `edit_file` with mixed `edits` (array or `null`) + top-level `old_string`/`new_string`, and with competing aliases (`new_str` != `newText`), is rejected before dispatch and leaves file unchanged.
  - `edit_file` with stringified JSON `edits` + nested aliases + `"replaceAll": "true"` + trailing `"notes"` executes cleanly AND renders its edits in both live events and reopened transcript projection (`agent/transcript_render.go` + AppWire projection).
  - `memory_edit` multi-edit on LF and CRLF+BOM pages verifies scope confinement, atomic mutation, stamped LF output, and refreshed index; mid-write failure leaves stored page and index unchanged.
  - `TestAllBuiltinParametersCarryDescriptions` includes `DefMemoryEdit()`, and provider schema emission tests (Anthropic, Google, OpenAI) pass on `DefEditFile()`.

- [ ] **Step 2: Run targeted tests to verify failure**
  Run: `go test ./agent/internal/tool/... ./agent -run 'TestAllBuiltinParametersCarryDescriptions|TestEditFile|TestMemoryEdit'`
  Expected: FAIL.

- [ ] **Step 3: Switch schemas, prompt, executors, fixtures, and `docs/tools/memory.md` together**
  - Update `DefEditFile()` and add `DefMemoryEdit()` via shared `defEditFile(readTool, writeTool)` in `agent/internal/tool/definitions.go` (exact schema from Spec Section 1).
  - Add the 1-sentence `{{ if .HasTool "edit_file" }}` batching nudge in `agent/prompts/system.md.tmpl` under `## Tool usage`.
  - Implement `parseFileEdits(args map[string]any) ([]execenv.FileEdit, error)`, update `execFileEditWith` in place, wire `execFileEdit` (`BatchFileEditor` + 1-edit fallback) in `agent/session_tools_file.go`, and wire `DefMemoryEdit()` + `execMemoryEdit` in `agent/session_tools_memory.go`.
  - Update raw-dispatch test fixtures in `agent/*_test.go` that bypass `prepareToolCall` (e.g. `cov_s3_filetools_test.go`, `session_parity_test.go`, `session_memory*_test.go`, `session_tooldeps_test.go`, `session_tools_file_ctx_test.go`, `session_tools_core_exact_fuzz_test.go`, `tool_exec_fuzz_test.go`, `session_escalation_e2e_test.go`, `session_file_scratch_test.go`, `session_goal_progress_test.go`).
  - Update `docs/tools/memory.md` (`memory_edit` row in Arguments and effects, Focused correction example, original-file matching, and advertised `edits[]` vs legacy repair). Check `docs/product/subsystems.md` and `docs/product/memory.md` for any stale single-edit claims and update if needed.

- [ ] **Step 4: Run Go test suites across touched packages**
  Run: `go test ./agent/internal/tool/... ./agent/execenv/... ./agent ./llm/providers/...`
  Expected: PASS.

- [ ] **Step 5: Commit**
  ```bash
  git add agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go agent/prompts/system.md.tmpl agent/session_tools_file.go agent/session_tools_memory.go docs/tools/memory.md agent/*_test.go
  git commit -m "feat(agent): switch edit_file and memory_edit to atomic edits[] schema"
  ```

---

### Task 8: Multi-Edit Fluency Probes and Arm B Prompt Variant (`tools/tool-fluency`)

**Files:**
- Keep: `tools/tool-fluency/probes/edit_file.yaml` (`edit_file.happy_path` serves as the single-edit regression probe)
- Create:
  - `tools/tool-fluency/probes/edit_file_disjoint_multi_edit.yaml`
  - `tools/tool-fluency/probes/edit_file_atomicity_recovery.yaml`
  - `tools/tool-fluency/probes/edit_file_rename_plus_targeted.yaml`
  - `tools/tool-fluency/probes/edit_file_ambiguous_context_recovery.yaml`
  - `tools/tool-fluency/probes/edit_file_crlf_bom_multi_edit.yaml`
  - `tools/tool-fluency/probes/edit_file_large_file_no_padding.yaml`
  - `tools/tool-fluency/variants/edit-file-parallel-calls.md` (Arm B `--system-prompt-append` prompt variant)

**Interfaces:**
- Consumes: `evener-fluency` single-probe-per-file YAML schema (`schema: 1`, `id`, `tool`, `prompt`, `fixture`, `expect`).

- [ ] **Step 1: Write the 6 new probe YAML files and the Arm B prompt variant**
  - Each probe uses `schema: 1`, `tool: edit_file`, `forbidden_calls: [shell]`, deterministic fixture files, and exact `artifacts` content checks matching Spec Section 11 (`edit_file.disjoint_multi_edit`, `edit_file.atomicity_recovery`, `edit_file.rename_plus_targeted`, `edit_file.ambiguous_context_recovery`, `edit_file.crlf_bom_multi_edit`, `edit_file.large_file_no_padding`).
  - Write `tools/tool-fluency/variants/edit-file-parallel-calls.md` instructing the model to emit multiple `edit_file` calls in a single response whenever making disjoint edits to one file.

- [ ] **Step 2: Run offline fluency runner tests to validate all probe manifests**
  Run: `go test ./tools/tool-fluency/...`
  Expected: PASS (every probe YAML parses strictly and validates against the runner's manifest checks).

- [ ] **Step 3: Commit**
  ```bash
  git add tools/tool-fluency/probes/edit_file*.yaml tools/tool-fluency/variants/edit-file-parallel-calls.md
  git commit -m "test(fluency): add multi-edit probe suite and parallel-call prompt variant"
  ```

---

## Final Verification Gate (Before PR / Live Eval)

Run the fast targeted gates sequentially (never in parallel, per AGENTS.md):
1. `go test ./agent/execenv/... ./agent/internal/tool/... ./agent ./llm/providers/... ./tools/tool-fluency/...`
2. `make test-api-package`
3. `make test-web`
4. `make test-native`
5. `make lint-package-imports`

Then run the 3-arm live multi-model evaluation (`evener-fluency catalog` -> `evener-fluency run` / `matrix` across Arms A, B, and C on available Google, Anthropic, and open-weight models) per Spec Section 11 and report the comparison table to Jesse before deciding whether to ship Arm C globally or adjust per surface.
