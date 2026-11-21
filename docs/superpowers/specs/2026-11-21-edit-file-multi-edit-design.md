# Atomic Multi-Edit for `edit_file`

**Date:** 2026-11-21
**Status:** Approved (ready for implementation planning)

## Intent and Why

On Anthropic and Gemini sessions, Evener exposes `edit_file` for exact-text file mutations (OpenAI/Codex sessions use `apply_patch`). Today `edit_file` accepts only one `{file_path, old_string, new_string, replace_all}` replacement per call, and its description tells the model to "Keep each call small and focused." Changing three disjoint locations in one file takes three tool calls and three separate read/write cycles, with no atomicity if the third edit fails.

An audit of `pi` (`@earendil-works/pi-coding-agent` 0.85.1), Claude Code (`Edit` / former `MultiEdit`), OpenCode (`anomalyco/opencode`), Gemini CLI, Cline, Roo Code, Goose, Sourcegraph Amp, Cursor, and 2025-2026 harness postmortems surfaced five concrete design lessons:

1. **One `edits[]`-only tool beats dual-mode schemas (`pi` `#2639`):** exposing both top-level `old_string`/`new_string` and `edits[]` produces frequent invalid calls, and Anthropic strips root-level `oneOf`/`anyOf`/`allOf`. Making `edits[]` the sole schema makes batching disjoint changes in one file the model's default habit.
2. **RL priors require schema-generic argument healing (Armin Ronacher, July 2026):** frontier models heavily RL-trained on Claude Code's `str_replace` sometimes send top-level `old_string`/`new_string`, stringify `edits` as a JSON string (Opus 4.6, GLM-5.1), wrap a bare edit object, or append invented trailing keys inside `edits[]` (`type`, `unique`, `notes`) even when `old_string`/`new_string` are byte-correct. Keeping Claude/Gemini field names (`file_path`, `old_string`, `new_string`, `replace_all`) and healing those shapes in `agent/internal/tool/repair` eliminates retry loops without loosening execution semantics.
3. **Pi-style CRLF and UTF-8 BOM handling:** LLMs always emit LF (`\n`), while Evener's `read_file` strips `\r` from CRLF lines (`agent/execenv/local_files.go:296`) and keeps any leading UTF-8 BOM on line 1. Strip a leading UTF-8 BOM and detect CRLF from the first line ending before matching in LF space (accepting line 1 with or without the leading BOM), splice once, then restore CRLF and prepend the original BOM once.
4. **Tight fuzzy matching beats deep fallback ladders (OpenCode `#32559`, `#44996`):** deep Levenshtein/block-anchor cascades silently bind to the wrong block in repetitive code and contradict exact-match error copy. Evener's whitespace-normalized fallback stays narrow and safe when it returns exact matched byte offsets, checks uniqueness (including exact-substring ambiguity outside the matched line/window), and reports truthfully what matchers ran.
5. **Errors are recovery prompts, and tool descriptions stay concise (OpenCode `edit.txt`, Amp, `pi`):** collecting all edit misses/ambiguities in one pass, naming 1-based line numbers for duplicate hits and nearest regions, telling the model never to copy `read_file` line-number prefixes (`12\t`), and stating rules once without repetition cuts retry turns and per-turn schema tokens.

This design brings atomic single-file multi-edit, clean repair, CRLF/BOM preservation, single-edit-tool-per-session advertisement, and actionable diagnostics to Evener's `edit_file` (and `memory_edit`, which shares the same schema builder and executor).

## Goals, Non-Goals, and Anti-Goals

### Goals
- Support atomic, all-or-nothing multi-edit on a single file in `edit_file` via an `edits[]`-only schema: every edit validates, matches, overlap-checks, and splices in memory before touching disk.
- Match every edit against the original file text and reject overlapping or nested spans cleanly before writing.
- Preserve per-edit `replace_all` (with explicit span and expanded-output bounds across every match path), Evener's whitespace-normalized fuzzy matching (returning exact byte offsets with uniqueness checking), Pi-style CRLF and UTF-8 BOM preservation (accepting line 1 copied from `read_file` with or without leading BOM), and nearest-region failure diagnostics with 1-based line numbers.
- Expose only one file-editing tool per session (`apply_patch` on `SurfaceOpenAI`, `edit_file` on `SurfaceAnthropic`, `SurfaceGoogle`, and `SurfaceGeneric`, keeping both registered for mid-session model switching and gating system prompt tool guidance on advertised tools).
- Let `memory_edit` inherit atomic multi-edit through the shared builder (`DefMemoryEdit()`) and `execFileEditWith`, while referencing `memory_read`/`memory_write` in its description, preserving the memory guide's LF stamping contract, and updating `docs/tools/memory.md` in the same change.
- Add a focused edit-batch helper in `agent/internal/tool/repair` for legacy top-level `{old_string, new_string}` promotion (rejecting mixed calls carrying both `edits` and top-level fields), stringified JSON arrays/objects, bare-object wrapping, and item-level aliasing, boolean coercion, and unknown-key stripping.
- Tighten the `edit_file` tool description and add a one-line batching nudge in `agent/prompts/system.md.tmpl`.
- Render multi-edit diffs (canonical `edits[]` and historical single-edit `{old_string, new_string}` transcripts) with consistent header-vs-content line classification across `@evener/appwire-client`, web UI, mobile-native, and Go transcript rendering.

### Non-Goals
- Multi-file edits inside `edit_file`. Cross-file patching remains `apply_patch`'s job (keeping `edit_file` single-file preserves sandbox escalation, `memory_edit` reuse, and web "open beside").
- Per-span mixed CRLF/LF offset translation or unconfined temp+rename replacement (unconfined writes stay in-place via `afero.WriteFile`, matching `WriteFile` and `WriteFileRaw`, which preserves inode identity, permissions, symlinks, and OS extended attributes / macOS Finder tags; sandboxed writes continue using `t.sfs.writeFile`).
- Hash-anchored editing (`hashline`) or changing `read_file`'s wire format (noted separately as a potential future direction for non-Claude/non-OpenAI models).
- Positional/occurrence-index matching or deep Levenshtein fuzzy cascades.

### Anti-Goals
- **Partial writes or truncation on failure:**
  - Any validation, match, ambiguity, overlap, resource-bound, or no-op refusal fails before invoking any `finish` (`stamp`) callback and writes nothing.
  - Under sandboxed confinement (`t.sfs != nil`), publication uses `t.sfs.writeFile` (atomic temp+`renameat`), leaving the stored page and session memory index unchanged on write failure.
- **Silent discard of mixed calls:** any call carrying both `edits` (present with any value, including `null`) and top-level `old_string`/`new_string` must be rejected before dispatch, never promoted or partially executed after `dropUnknown` strips the top-level keys.
- **Ambiguous dual-mode schemas or dual edit tools:** never expose both top-level `old_string`/`new_string` and `edits[]` in the advertised schema, and never advertise both `apply_patch` and `edit_file` in the same session.
- **Silent non-atomic fallback:** if an `ExecutionEnvironment` does not support batch editing, a multi-edit call (`len(edits) > 1`) must error rather than loop over single-edit calls.
- **Misleading error copy:** never tell the model "must match exact whitespace" when whitespace-normalized fuzzy matching also ran.

## Constraints

- **Provider schema compatibility:**
  - Anthropic strips root-level `oneOf`, `anyOf`, and `allOf` (`llm/providers/anthropic/emit.go`). The schema must be a single flat root object.
  - Gemini's Schema proto strips `additionalProperties` and `$`-prefixed metadata (`llm/providers/google/request.go`), and accepts `minItems` (already used by `DefAskUser`).
  - OpenAI strict mode (`llm/providers/internal/openaichat/strict.go`) falls back cleanly on optional properties (already the case for `DefEditFile` because `replace_all` is optional).
  - Every declared property, including nested `edits.items.properties`, must carry a `description` (`TestAllBuiltinParametersCarryDescriptions`).
- **Single-file invariants across Evener:**
  - `agent/session_escalation.go` (`escalatableTools`) only escalates single-file tools because human approval grants one leaf path.
  - `cmd/evener-hub/frontend/src/panes/session/transcript/tools/editTools.tsx` binds `openBesidePath: filePathArg`, which requires one file path per call.
  - `agent/session_tools_memory.go` writes one memory page per call, stamping `updated:`/`by:` through `frontmatter.SetField` (`agent/internal/frontmatter/frontmatter.go`, which normalizes stamped Markdown pages to LF without a leading BOM per `docs/product/memory.md`).
- **Interface compatibility:** `execenv.ExecutionEnvironment` is implemented by over a dozen test doubles across `agent/`. Extending `execenv` via an optional capability interface (matching `ContextualFileReader`, `GlobBudgeter`, `DirListBudgeter`, `FileMutator`) keeps existing doubles compiling.
- **Testing rules (`docs/developing-evener/testing.md`):** Tests assert behavior, never exact error-message phrasing, tool-description prose, or copy constants.
- **Compatibility rule:** Historical transcript rendering for saved `{file_path, old_string, new_string}` calls (and accepted pre-repair shapes) is preserved so stored transcripts remain readable without mutating the recorded arguments. Runtime argument repair for legacy-only top-level `{old_string, new_string}` is permanent off-distribution model-quirk healing in `agent/internal/tool/repair` (same class as `old_str` -> `old_string`), approved by Jesse as part of Approach A.

## Approaches Considered

| Approach | Summary | Tradeoffs |
| --- | --- | --- |
| **A. Pi-style `edits[]`-only on `edit_file` (Selected)** | Replace top-level `old_string`/`new_string`/`replace_all` in `DefEditFile()` with required `edits: [{old_string, new_string, replace_all?}]`. Heal off-distribution shapes in `agent/internal/tool/repair`. | Cleanest schema across Anthropic/Gemini/OpenAI; eliminates dual-mode schema errors (`pi` `#2639`); nudges models to batch by default; preserves single-file escalation, `memory_edit`, and "open beside". Costs ~10 extra tokens on single-edit calls and updates existing raw `edit_file` test fixtures. |
| **B. Dual-mode `edit_file`** | Keep top-level `old_string`/`new_string` alongside optional `edits[]`, making only `file_path` required in JSON Schema and enforcing mutual exclusivity in the handler. | Zero churn for single-edit calls, at the cost of the invalid-call failures `pi` removed in `#2639` (exacerbated by Anthropic stripping root `oneOf`/`anyOf`) and weaker nudging toward batching. |
| **C. Separate `multi_edit` tool** | Leave `edit_file` unchanged and add a second single-file `multi_edit` tool (Claude Code 1.x model). | Avoids schema churn on `edit_file`, at the cost of extra tool-catalog tokens, models splitting across two edit tools (why Claude Code 2.0 removed it), and wiring a second edit tool through hooks, escalation, memory, and both UIs. |

**Decision (Jesse):** Approach A with original-file, non-overlapping matching.

## Detailed Design

Design items agreed with Jesse are unmarked; design calls filled in by this spec are marked *(proposal)*.

### 1. Tool Schema and Description (`agent/internal/tool/definitions.go`)

Build `DefEditFile()` and `DefMemoryEdit()` from one internal helper `defEditFile(readTool, writeTool string)` so behavioral rules are stated **once**, property descriptions stay single-clause, and `memory_edit` points models to `memory_read` and `memory_write` (never workspace `read_file`/`write_file`):

```go
func defEditFile(readTool, writeTool string) llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "edit_file",
		Description: fmt.Sprintf(
			"Apply one or more exact replacements to an existing file atomically. "+
				"Read the file first with %s and never copy line-number prefixes (like '12\\t') into old_string or new_string. "+
				"Batch disjoint changes in the same file into one call via edits rather than separate calls. "+
				"Every edits[].old_string matches the original file (not after earlier edits) and must not overlap any other edit; merge nearby changes into one entry. "+
				"Keep each old_string as small as possible while unique, and use %s when replacing most or all of a file. "+
				"If any edit fails to match, is ambiguous, or overlaps another edit, nothing is written.",
			readTool, writeTool,
		),
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Path of the existing file to edit.",
				},
				"edits": map[string]any{
					"type":        "array",
					"minItems":    1,
					"description": "Disjoint replacements matched against the original file and applied atomically.",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"old_string": map[string]any{
								"type":        "string",
								"description": "Non-empty exact text from the original file to replace (unique unless replace_all is true).",
							},
							"new_string": map[string]any{
								"type":        "string",
								"description": "Replacement text.",
							},
							"replace_all": map[string]any{
								"type":        "boolean",
								"description": "Replace every occurrence in the original file instead of requiring a unique match (default false).",
							},
						},
						"required": []string{"old_string", "new_string"},
					},
				},
			},
			"required": []string{"file_path", "edits"},
		},
	}
}

func DefEditFile() llm.ToolDefinition {
	return defEditFile("read_file", "write_file")
}

func DefMemoryEdit() llm.ToolDefinition {
	return MemoryDefinition(defEditFile("memory_read", "memory_write"), "memory_edit")
}
```

`registerMemoryTools` (`agent/session_tools_memory.go:26`) switches from `tool.MemoryDefinition(tool.DefEditFile(), "memory_edit")` to `tool.DefMemoryEdit()`, matching `DefMemoryRead()`, `DefMemoryWrite()`, and `DefMemoryDelete()`.

### 2. System Prompt Guidance (`agent/prompts/system.md.tmpl`)

Keep `edit_file` batching guidance in `DefEditFile.Description` alone (never in the frozen launch system prompt `system.md.tmpl`), so mid-session model switches across `edit_file` and `apply_patch` surfaces never leave stale editor instructions in the cached launch prompt. All detailed matching, overlap, batching, and line-prefix rules live once in `DefEditFile.Description`.

### 3. Schema-Generic Argument Repair (`agent/internal/tool/repair/repair.go`)

`prepareToolCall` (`agent/session_tool_repair.go`) runs `repair.RepairArgs(t.Definition.Parameters, args, ...)` whenever `t.Schema.Validate(args)` fails, and records applied `repair.Change` items on `EventToolCallRepaired`. Generalize the schema-driven passes in `repair.go` in a strict 5-phase order so decoded/wrapped arrays always receive item-level aliasing before unknown-key stripping, and mixed calls are never silently stripped or promoted into passing:

1. **Extend `aliasTable` (`repair.go:46`):**
   Add `oldText` -> `old_string`, `old_text` -> `old_string`, `newText` -> `new_string`, `new_text` -> `new_string`, `replaceAll` -> `replace_all` alongside existing `old_str` -> `old_string` and `new_str` -> `new_string`. The safe-apply rule (rename only when canonical is declared on the active object schema, alias is not declared, alias is present, canonical is absent) prevents collisions on unrelated tools.
   - **Deterministic alias conflict rule (shared with transcript readers):** iterate alias keys in sorted order, never raw Go map iteration order. Canonical keys always take precedence over aliases. When the canonical key is absent and **two or more aliases targeting the same canonical key are present with different values** (e.g. both `new_str: "a"` and `newText: "b"` with `new_string` absent), do **not** pick an arbitrary winner; preserve the competing alias keys past `dropUnknown` so schema validation rejects the ambiguous call and leaves the file unchanged. If competing aliases carry identical values, promote once and drop the duplicate. Both Go `transcript_render.go` and TypeScript `editOperationsOf` apply the same canonical-first, reject-conflicting-aliases rule.
2. **Phase 1 (Root aliases):** Run existing top-level `applyAliases` on root `args` against root `params` (e.g. `path` -> `file_path`).
3. **Phase 2 (Single-item array promotion and mixed-form conflict detection):**
   Inspect every required property `K` of root `params` whose schema is `"type": "array"` with an object `items` schema carrying a non-empty `required` list. Identify root keys in `args` that are **not** declared on root `params` and map (directly or via `aliasTable`) to a declared property of `K.items`:
   - Check key presence via `_, hasKey := args[K]` (NOT nil-ness).
   - **If `!hasKey` (the array key is completely absent):** if those matched root keys cover every key in `K.items.required`, move those keys into `args[K] = []any{item}` and delete them from root `args`, recording a `repair.Change` on field `K`.
   - **If `hasKey` (the array key is present with any value, including `null`) and one or more conflicting root keys are present:** do **not** promote AND add those conflicting root keys to a `preserveUnknown` set so Phase 5 (`dropUnknown`) leaves them in place. Because `additionalProperties: false`, `t.Schema.Validate(healed)` then rejects the mixed call before dispatch, leaving the file untouched and preventing both silent promotion of `edits: null` + `old_string` and silent stripping of `edits: [...]` + `old_string`.
4. **Phase 3 (Root coercion, including stringified JSON arrays and bare objects):**
   Run `applyCoercions` on root `args` **before** descending into array items:
   - When a root property has `"type": "array"` and its `items` schema declares `"type": "object"`:
     - If `raw` is a `string`: attempt `json.Unmarshal` (bounded by existing argument size/depth limits). If it decodes to `[]any`, replace with that slice (`ChangeCoerceType`, `"json_string→array"`); if it decodes to `map[string]any`, wrap as `[]any{decoded}` (`ChangeCoerceType`, `"json_string→[object]"`). If it is not valid JSON or decodes to neither array nor object, leave it untouched so schema validation reports the real type error (never wrap a plain string into `[]any{raw}` when `items.type == "object"`).
     - If `raw` is a `map[string]any`: existing scalar-to-array coercion wraps it into `[]any{raw}`.
5. **Phase 4 (Recursive nested object and array-item repair):**
   Now that any promoted, JSON-decoded, or bare-wrapped array is a real `[]any`, walk nested `type: "object"` properties and `type: "array"` elements whose `items` is `type: "object"` (using the same copy-on-write cloning pattern as `dropEmptyOptionalsWithin` at `repair.go:443`), running in order per item:
   - `applyAliases` (heals `old_str`/`oldText`/`old_text`, `new_str`/`newText`/`new_text`, `replaceAll`, prefixing `Change.Field` with `edits[i].`)
   - `applyCoercions` (heals `replace_all: "true"`/`"false"` -> `bool`)
   - `dropEmptyOptionals`
   - `dropUnknown` when `items.additionalProperties == false` (neutralizes the Claude Opus 4.8 / Sonnet 5 failure mode where models emit byte-correct `old_string`/`new_string` plus invented trailing keys like `type`/`unique`/`notes`, while still failing loudly if a typo like `old_sting` leaves required `old_string` missing)
6. **Phase 5 (Root `dropEmptyOptionals` and `dropUnknown`):**
   Run root `dropEmptyOptionals` and root `dropUnknown`, skipping any key in `preserveUnknown` from Phase 2 so mixed-form calls always fail schema validation.

**Non-mutation invariant:** `FuzzRepairArgs` enforces that `RepairArgs` never mutates its input `args` map or nested slices/maps. Every recursive pass clones slices and modified item maps on write.

### 4. Multi-Edit Execution Engine (`agent/execenv`)

#### Types, Bounds, and Capability Interface (`agent/execenv/execenv.go`, `agent/execenv/local_files.go`)

```go
// FileEdit is one exact-text replacement within a single-file batch edit.
type FileEdit struct {
	OldString  string
	NewString  string
	ReplaceAll bool
}

// BatchFileEditor is an optional execution-environment capability for applying
// multiple non-overlapping replacements to one file atomically in a single read
// and write. Separate from ExecutionEnvironment so existing test doubles remain
// unaffected.
type BatchFileEditor interface {
	EditFileBatchCtx(ctx context.Context, path string, edits []FileEdit) (string, error)
}
```

Package-level resource caps (variables like `MaxWholeFileReadBytes` in `agent/execenv/read_capped.go` so tests can exercise both refusals with fixture-sized caps):
- `MaxEditFileSpans = 100_000`: maximum total matched spans across **all** entries and **all** match paths (unique exact, exact `replace_all`, unique fuzzy, and fuzzy `replace_all`) in a single batch.
- `MaxEditFileOutputBytes = MaxWholeFileReadBytes` (128 MiB default): maximum spliced output byte size, verified from scalar lengths **before** allocating any converted replacement buffers or the output buffer.

`LocalExecutionEnvironment` implements:
- `EditFileBatchCtx(ctx context.Context, path string, edits []FileEdit) (string, error)` -> calls `EditFileBatchWithCtx(ctx, path, edits, nil)`
- `EditFileBatchWithCtx(ctx context.Context, path string, edits []FileEdit, finish func(edited []byte) []byte) (string, error)`
- Existing `EditFileWithCtx(ctx, path, oldString, newString, replaceAll, finish)` delegates directly to `EditFileBatchWithCtx(ctx, path, []FileEdit{{OldString: oldString, NewString: newString, ReplaceAll: replaceAll}}, finish)` so there is one engine and zero duplicated matching/writing logic.

#### Atomic Matching, Pi-Style CRLF/BOM Splicing, and Publication (`agent/execenv/local_files.go`)

Walkthrough:
1. **Target & write preflight:** Call `e.fileTarget("edit_file", path, targetWrite)` and defer `t.release()`. Under sandbox (`t.sfs != nil`), run `t.sfs.checkWritable("edit_file", t.abs)` first so a non-writable path always returns a clean sandbox denial (enabling human escalation on containment denials) rather than an input or match error.
2. **Input guard:** Reject `len(edits) == 0`. Reject any entry with `edits[i].OldString == ""`, collecting all empty-`old_string` errors (naming `edits[i]` when `len(edits) > 1`) before doing file I/O.
3. **Single read + Pi-style BOM/LF view:** Read raw bytes `origBytes` once via `t.sfs.readFileCtx` (sandboxed) or `readFileCappedCtx` (unconfined).
   - Detect an optional leading UTF-8 BOM (`\xEF\xBB\xBF`) and strip it into `bom` before matching.
   - Detect `useCRLF` from the first line ending in the body (`\r\n` vs `\n`), and normalize `\r\n` -> `\n` into `lfText`.
   - Normalize each `ed.OldString` and `ed.NewString` `\r\n` -> `\n` (`oldLF`, `newLF`). When `bom != ""` and `oldLF` begins with `bom` (with remaining non-empty text, as when copied verbatim from `read_file` line 1), strip `bom` from `oldLF` and `newLF` so first-line edits match cleanly and emit the BOM exactly once.
4. **Match ALL edits against `lfText` (single-scan per edit, enforce span budget on EVERY path, collect all errors):**
   For each `i, ed := range edits`:
   - Call `idx := strings.Index(lfText, oldLF)` **once**.
   - **Exact-match path (`idx >= 0`):**
     - If `!ed.ReplaceAll`: check for a second occurrence starting at **`idx + 1`** (`second := strings.Index(lfText[idx+1:], oldLF)`). Using `idx + 1` catches self-overlapping needles such as `old_string: "ana"` in `"banana"` as ambiguous rather than silently picking the first occurrence. If `second >= 0`, record an ambiguity error for `edits[i]` listing the 1-based line numbers of occurrences (capped at 10) and continue. Otherwise check `len(spans)+1 > MaxEditFileSpans`; if exceeded, record a recoverable span-limit error and stop collecting further spans (returning the error without writing). Otherwise append span `{start: idx, end: idx + len(oldLF), editIndex: i}`.
     - If `ed.ReplaceAll`: use standard left-to-right non-overlapping matches (matching `strings.ReplaceAll`), advancing `pos` by `len(oldLF)` after each match and checking `len(spans)+1 > MaxEditFileSpans` **before** every append; if exceeded, record a recoverable span-limit error and stop immediately.
   - **Fuzzy fallback (`idx < 0`):**
     - Compute `remaining := MaxEditFileSpans - len(spans)`.
     - Call `fuzzySpans, ambigLines, limitExceeded := findFuzzyMatchSpans(lfText, oldLF, ed.ReplaceAll, remaining)` (which rejects an empty normalized needle `normalizeWS(oldLF) == ""`, enforces `end > start` on every matched span, checks `remaining` before appending any single-line or multi-line hit including `remaining == 0` when `!ed.ReplaceAll`, and returns up to 10 candidate 1-based line numbers in `ambigLines` on ambiguity without rescanning).
     - If `limitExceeded`: record a recoverable span-limit error and stop immediately.
     - If `len(ambigLines) > 0`: record an ambiguity error for `edits[i]` naming those 1-based candidate line numbers (advising more context or `replace_all: true`) and continue.
     - If `len(fuzzySpans) == 0`: record a not-found error for `edits[i]`, truthfully stating that exact and whitespace-normalized matching ran and attaching `nearestFileRegion(lfText, oldLF)` with 1-based line numbers (capping nearest-region snippets to the first 3 missed edits), then continue.
     - **Substring ambiguity guard for `!ed.ReplaceAll`:** for a single fuzzy match `[start, end)`, verify that the verbatim matched text `lfText[start:end]` does not also appear elsewhere in `lfText` as an exact substring (`strings.Index(lfText[:start], matched) >= 0 || strings.Index(lfText[end:], matched) >= 0`). If it does, record an ambiguity error instead of silently editing either location.
     - Mark `usedFuzzy = true` and append each returned fuzzy span directly (using its own `(start, end)` offsets, never diverting to an earlier substring).
   - If any errors were collected across `edits`, return them joined together without writing anything to disk.
5. **Sort spans and reject overlaps:**
   - Sort `spans` by `start` ascending, then `end` ascending, then `editIndex` ascending.
   - For `k := 1; k < len(spans); k++`:
     - If `spans[k-1].end > spans[k].start`: fail the whole call naming `edits[spans[k-1].editIndex]` and `edits[spans[k].editIndex]` and instructing the model to merge overlapping or nearby edits into one entry.
     - Adjacent touching spans (`spans[k-1].end == spans[k].start`) are disjoint and allowed.
6. **Output cap check, single-pass splice, CRLF+BOM restore, and exact no-op guard:**
   - Check the pre-expansion LF output length against `MaxEditFileOutputBytes`, splice into a pre-sized `strings.Builder` (stripping any redundant leading `bom` at `sp.start == 0`), check the post-CRLF final byte length against `MaxEditFileOutputBytes`, restore `\r\n` when `useCRLF`, and prepend `bom`.
   - Check `bytes.Equal(edited, origBytes)` **before** calling `finish` or writing. If equal, return a no-op error without calling `finish` or writing to disk.
7. **Finish hook (`memory_edit` formatting contract) and write:**
   - **Formatting contract for `finish != nil` (`memory_edit`):** `finish` (`stamp` in `agent/session_tools_memory.go`) computes stamped bytes in memory from `edited` right before writing.
   - **Write path:**
     - Sandboxed (`t.sfs != nil`): `t.sfs.writeFile("edit_file", t.abs, edited, 0o644)` publishes atomically via temp file + `renameat` under the directory fd (preserving regular-file permissions and refusing symlinks).
     - Unconfined (`t.sfs == nil`): `afero.WriteFile(e.filesystem(), t.abs, edited, 0o644)` writes in place, matching `WriteFile`, `WriteFileRaw`, and prior `EditFile` — preserving inode identity, file mode, symlink targets, OS extended attributes / macOS Finder tags, and read-only (`0o444`) target refusals.
8. **Confirmation summary:**
   - Single edit (`len(edits) == 1`): `edited <path>: <n> replacement[s][fuzzyNote]`.
   - Multiple edits (`len(edits) > 1`): `edited <path>: <totalSpans> replacement[s] across <len(edits)> edits[fuzzyNote]`.

#### Offset-Returning Fuzzy Matcher (`agent/execenv/local_edit_match.go`)

Replace string-returning `findFuzzyMatch` with an offset-returning matcher `findFuzzyMatchSpans(content, oldString string, replaceAll bool, maxSpans int) (spans []byteSpan, ambigLines []int, limitExceeded bool)`:
- Reject an empty normalized needle immediately (`if normalizeWS(oldString) == "" { return nil, nil, false }`), carrying forward the existing guard so whitespace-only `old_string` values never match empty lines on the fuzzy path. Require every accepted span to satisfy `end > start`.
- Walks `content` with byte offsets tracked across `\n`-delimited lines (streaming, no whole-file line-slice allocation per issue `#4556`).
- Single-line `oldString`: checks `normalizeWS(line) == normOld` and records exact `(lineStart, lineEnd)`.
- Multi-line `oldString`: tracks a sliding window of `(lineStart, lineEnd)` pairs of size `wSize` and records exact `(window[0].start, window[wSize-1].end)`.
- Before recording any match: if `len(spans) >= maxSpans`, stop immediately and return `(nil, nil, true)` (covers `maxSpans <= 0` for `!replaceAll` too!).
- When `!replaceAll`: records the first match's 1-based line number; if a second matching line/window is found, collects up to 10 candidate 1-based line numbers in `ambigLines` and returns `(nil, ambigLines, false)` without rescanning the file; otherwise returns the single `[]byteSpan{{start, end}}`.
- When `replaceAll`: collects all non-overlapping matching line/window spans (advancing past the matched window on multi-line hits so sliding windows do not overlap themselves).
- Update `nearestFileRegion` to include the 1-based starting line number (`L<n>:`) so missed edits tell the model where in the file the nearest region sits.

### 5. Session File and Memory Executors (`agent/session_tools_file.go`, `agent/session_tools_memory.go`)

- **Decode `edits` (`parseFileEdits` in `agent/session_tools_file.go`):**
  - Extract `args["edits"]` as `[]any`, decoding each `map[string]any` into `execenv.FileEdit{OldString, NewString, ReplaceAll}`. Return a validation error if `edits` is missing, empty, or contains a malformed element.
- **Update `execFileEditWith` in place (`agent/session_tools_file.go`):**
  - `execFileEditWith` is unexported and has only two callers (`execFileEdit` and `execMemoryEdit`). Update its signature in place to take `edit func(path string, edits []execenv.FileEdit) (string, error)` instead of adding a parallel `execFileEditBatchWith`.
  - Check `readGuard.ReadBeforeWriteWarning(path)` once for `file_path`, and prepend `warn` when `err == nil && warn != ""`.
- **`execFileEdit` fallback (`agent/session_tools_file.go`):**
  - If `env` implements `execenv.BatchFileEditor`, call `be.EditFileBatchCtx(ctx, path, edits)`.
  - Otherwise (minimal test double implementing only `ExecutionEnvironment` / `ContextualFileReader`):
    - If `len(edits) == 1`: call `ContextualFileReader.EditFileCtx` (or `env.EditFile`) with `edits[0]`.
    - If `len(edits) > 1`: return an error (`"edit_file: execution environment does not support multi-edit"`).
- **`execMemoryEdit` (`agent/session_tools_memory.go`):**
  - `execOwnMemoryWrite` already passes `*execenv.LocalExecutionEnvironment` and `stamp`. Call updated `execFileEditWith` delegating to `env.EditFileBatchWithCtx(ctx, path, edits, stamp)`.
  - One read, one atomic multi-edit, one frontmatter `updated:`/`by:` stamp, one atomic write, one memory index update.
- **Hooks and sandbox escalation:**
  - `PreToolUse` / `PostToolUse` hooks receive the canonical post-repair JSON `{ "file_path": "...", "edits": [...] }` unchanged.
  - `session_escalation.go` keeps `"edit_file": true` in `escalatableTools` because `edit_file` remains strictly single-file.

### 6. Go Transcript Rendering and Checkpointing

- **`agent/transcript_render.go` (`case "edit_file"`):**
  - Live events and stored transcripts preserve the model's original raw arguments (including repaired calls where `edits` arrived as a JSON string, a bare edit object, legacy top-level fields, or with item aliases). Parse edit operations with a small helper that handles:
    - `edits` as a JSON array, a JSON-encoded string decoding to an array or single object, or a bare edit object;
    - item fields accepting canonical `old_string`/`new_string`/`replace_all` and the repair aliases (`old_str`/`oldText`/`old_text`, `new_str`/`newText`/`new_text`, `replaceAll`);
    - legacy top-level fields only when `edits` is absent.
  - Summary format:
    - 1 edit: preserve compact summary `path (replace <oldLen>→<newLen> chars[, all])`.
    - N > 1 edits: summarize edit count and aggregate char delta, e.g. `path (<N> edits, replace <sumOld>→<sumNew> chars)`.
- **`agent/internal/contextmgr/checkpoint.go`:**
  - Already reads only `args["file_path"]` for `case "edit_file"`. No change needed.

### 7. Shared TypeScript Client, Web UI, and Mobile (`appwire-client`, `cmd/evener-hub/frontend`, `mobile-native`)

#### Shared Helpers (`appwire-client/typescript/editDiff.ts`)

Add one shared edit-extraction, diff-synthesis, and diff-line-classification path exported from `@evener/appwire-client`:

```ts
export interface EditOperation {
  readonly oldString: string;
  readonly newString: string;
  readonly replaceAll?: boolean;
}

// Extracts edit operations from edit_file / memory_edit arguments while
// preserving original raw arguments in stored transcripts. Accepts:
// - canonical edits[] array,
// - repaired-at-dispatch shapes (JSON-stringified edits array/object, bare
//   edit object, and item aliases old_str/oldText/old_text,
//   new_str/newText/new_text, replaceAll),
// - historical top-level old_string / new_string (or aliases) only when the
//   edits key is absent.
// Returns null when no valid edit operations are present (malformed call).
export function editOperationsOf(args: Record<string, unknown>): EditOperation[] | null;

// Single diff-synthesis implementation for one or more edits to a single file.
// For a single edit, output is identical to editDiffText (no @@ header).
// For multiple edits, each edit is separated by an "@@ edit N @@" header so
// both web DiffBlock and mobile Diff render clear visual boundaries.
export function editBatchDiffText(path: string, edits: readonly EditOperation[]): string;

export type DiffLineKind = "header" | "add" | "del" | "context" | "meta";

// Single header-vs-content line classifier shared by diffStats, web DiffBlock,
// and mobile StepEvidence Diff.
export function classifyDiffLineKinds(unified: string): DiffLineKind[];
```

- Reduce `editDiffText(path, oldString, newString)` to a one-line wrapper: `return editBatchDiffText(path, [{ oldString, newString }]);`.
- **Single header-vs-content rule across `diffStats`, web `DiffBlock`, and mobile `Diff`:**
  - `inHeader` starts `true` (and resets `true` on `diff --git` or `index `).
  - `inHeader` turns `false` on `@@`.
  - On flat single-edit diffs with no `@@`, `inHeader` also turns `false` immediately after the leading `+++ ` file-header line that follows a `--- ` file-header line (so a subsequent deleted `-- heading` / `---` line or added `+++` line is classified as `"del"` / `"add"` in `diffStats`, web `DiffBlock`, AND mobile `Diff`, never muted as a second file header!).
  - `diffStats`, web `DiffBlock` (`parseUnifiedDiff`), and mobile `StepEvidence.tsx` (`Diff`) all share this exact rule, so summary `+A -R` counts and rendered styling always agree on both flat legacy single-edit diffs and multi-edit `@@` diffs.

#### Summary and Body Consumers

All four consumers switch to `editOperationsOf(args)` + `editBatchDiffText(path, ops)`:

1. **`appwire-client/typescript/toolSummaries.ts` (`editFileWords`):**
   - Sums `+added -removed` across all edits via `diffResultText(editBatchDiffText(path, ops))`, rendering `Edited <path> · +A -R` (consistent with `applyPatchWords`). Omits diff detail when `ops === null`.
2. **`appwire-client/typescript/memorySteps.ts` (`memoryEditWords`):**
   - Uses `editOperationsOf(args)` + `editBatchDiffText(path, ops)`. When `ops === null`, omits diff detail (matching existing malformed-call guard).
3. **`cmd/evener-hub/frontend/src/panes/session/transcript/tools/bodies.tsx` (`EditFileBody`) and `memoryTools.tsx` (`MemoryEditBody`):**
   - `EditFileBody` extracts `ops = editOperationsOf(args)`. When `ops === null`, returns `null` (matching `ApplyPatchBody`); otherwise renders `<DiffBlock unified={editBatchDiffText(path, ops)} />`.
   - `MemoryEditBody` checks `editOperationsOf(args) === null` before falling back to `defaultToolBody(props)`.
   - `openBesidePath: filePathArg` on `edit_file` stays unchanged.
4. **`mobile-native/src/session/evidence.ts` and `StepEvidence.tsx`:**
   - `evidence.ts` uses `editOperationsOf(args)` + `editBatchDiffText(path ?? "", ops)` for both `edit_file` and `memory_edit`.
   - `StepEvidence.tsx` (`Diff`) uses the shared header-vs-content line classification so `@@ edit N @@` and file headers style muted (`palette.inkLow`), while header-like content lines (`---` / `+++` after the header pair or `@@`) render with `diffDel` / `diffAdd` background styling.

### 8. Implementation Order and Verification Plan

#### Staged Implementation Order (Keep Every Intermediate Commit Working)

1. **Stage 1: Backend batch engine + dual-shape transcript consumers (no schema switch yet).**
   - Add `FileEdit`, `BatchFileEditor`, `EditFileBatchCtx`, `EditFileBatchWithCtx` (with `EditFileWithCtx` delegating to it), offset-returning `findFuzzyMatchSpans`, mixed-ending/BOM byte-faithful splicing, `writeFileAtomic` for unconfined writes (staying inside `fsys`'s namespace and preserving target write admission), and `MaxEditFileSpans`/`MaxEditFileOutputBytes` bounds across every match path.
  - Update `FuzzEditFile`'s byte-level oracle in the same stage: retain the raw `strings.Replace` / `strings.ReplaceAll` oracle for LF inputs, and assert CRLF-preserving expected bytes for CRLF / BOM / mixed-ending inputs.
   - Update `agent/transcript_render.go`, `@evener/appwire-client` (`editOperationsOf`, `editBatchDiffText`, shared header-aware diff line classification + `diffStats`, `toolSummaries.ts`, `memorySteps.ts`), web `DiffBlock`/`bodies.tsx`/`memoryTools.tsx`, and mobile `evidence.ts`/`StepEvidence.tsx` to render canonical `edits[]`, accepted pre-repair shapes, and legacy top-level calls.
   - Existing single-edit calls and every existing test remain green at this boundary.
2. **Stage 2: Schema, prompt, 5-phase repair, dispatch, fixtures, and evergreen docs together.**
   - Update `DefEditFile()` / `DefMemoryEdit()`, `agent/prompts/system.md.tmpl`, 5-phase `repair.RepairArgs`, `execFileEditWith` / `execFileEdit` / `execMemoryEdit`, raw-dispatch test fixtures, and evergreen documentation (`docs/tools/memory.md` updating the `memory_edit` argument table, Focused correction JSON example, original-file matching, and advertised batch shape vs legacy repair; plus any product guide row referencing `edit_file`) in one atomic change, verified end-to-end across Go and TypeScript gates.

#### Verification Cases

Follow `docs/developing-evener/testing.md` (behavior assertions only, never phrasing assertions on error copy or tool descriptions):

- **Go `execenv` tests (`agent/execenv`):**
  - Multi-edit disjoint replacements in one call (forward order, reverse file order, adjacent touching spans).
  - Atomicity on match/validation failure: edit 0 matches, edit 1 misses (or is ambiguous, or empty `old_string`) -> error returned naming every failing edit, file bytes unchanged, `finish` callback not called.
  - Atomicity and permissions on unconfined writes:
    - Fault-injected write failure in `writeFileAtomic` removes temp file and leaves original file bytes intact.
    - Read-only target file (`0o444`) in a writable directory is refused without overwriting it.
    - Non-writable parent directory returns an error without modifying the target.
    - Editing through a symlink on `OsFs` updates the symlink target while keeping the symlink intact; `MemMapFs` and `BasePathFs` edits stay strictly inside their injected filesystem namespace.
  - Multi-error collection: two missing edits in one batch report both failures in one error; `nearestFileRegion` snippets cap at 3 and carry 1-based line numbers.
  - Overlap rejection: partial overlap, nested span, identical duplicate edits, and `replace_all: true` overlapping another edit -> error returned naming both edit indices, file bytes unchanged.
  - Resource bounds across every path:
    - `MaxEditFileSpans`: a batch at the exact limit succeeds; one additional span via unique exact, exact `replace_all`, unique fuzzy, or fuzzy `replace_all` fails cleanly before splicing or writing.
    - `MaxEditFileOutputBytes`: a CRLF `replace_all` batch whose expanded size exceeds the cap fails before allocating the expansion or writing.
  - Byte-faithful line endings & BOM: pure CRLF + BOM file preserves CRLF and BOM; mixed-ending file (`a\r\nb\nc\r\n`) edited on line 1 preserves untouched LF after `b` byte-for-byte.
  - Self-overlapping needle: `old_string: "ana"` in `"banana"` fails as ambiguous when `replace_all: false`, while `replace_all: true` applies left-to-right non-overlapping replacement (`"bXna"`).
  - Offset-returning fuzzy matching: single fuzzy match on line 2 (`  alpha   beta`) when line 1 has `prefix  alpha   beta` rejects the substring ambiguity rather than silently editing line 1; unique fuzzy match succeeds; fuzzy ambiguity across two whitespace variants fails when `!replace_all` and reports candidate 1-based line numbers with file bytes unchanged; whitespace-only fuzzy miss (e.g. tab-only `old_string` with `replace_all: true` on a file with empty lines and no tabs) fails cleanly without modifying the file; fuzzy `replace_all: true` replaces all whitespace-variant lines directly by offset.
  - Exact no-op guard: single no-op AND adjacent cancelling edits (`abc` with `a`->`ab` and `bc`->`c`) return an error without calling `finish` or writing; fuzzy whitespace-cleanup edit (`old_string == new_string` matching differently indented bytes) succeeds.
  - Sandboxed (`NewConfinedFileEnvironment` / read-only / masked path) write-denial precedes read/match on multi-edit, and confined atomic write preserves all edits.
  - Update / extend `FuzzEditFile` and `FuzzEditFileDifferential` to verify atomicity and prove a real successful edit produces identical expected bytes in both `MemMapFs` and `BasePathFs-over-OsFs` lanes.
- **Go repair & session tests (`agent/internal/tool`, `agent/internal/tool/repair`, `agent/`):**
  - `TestAllBuiltinParametersCarryDescriptions` passes on `DefEditFile()` and `DefMemoryEdit()` (companion-tool wording for `memory_edit` vs `edit_file` is verified by code review, not prose substring tests).
  - Provider schema emission tests (Anthropic, Google, OpenAI) confirm `DefEditFile()` emits cleanly on every provider.
  - `repair.RepairArgs` tests:
    - Legacy-only top-level `{old_string, new_string, replace_all}` and `{old_str, new_str}` (with `edits` key absent) promote to `edits[0]`.
    - Mixed calls carrying `edits` alongside top-level `old_string`/`new_string` (both `edits: [...]` AND explicit `edits: null`) are **rejected before dispatch**, leaving the file unchanged.
    - Competing aliases for the same field with different values (`new_str: "a"`, `newText: "b"`, `new_string` absent) are deterministically rejected before dispatch, leaving the file unchanged; canonical key always takes precedence when present.
    - Combined quirk test: stringified JSON array (or bare object) `edits` containing nested `old_str`/`new_str`/`replaceAll: "true"` plus invented trailing key `"notes"` decodes, aliases, coerces, drops `"notes"`, and reaches executor with canonical fields.
    - Non-JSON string `edits` is NOT wrapped into `[string]`; typo `old_sting` leaving `old_string` missing fails validation; non-mutation of input map/slices verified by `FuzzRepairArgs`.
  - `agent` session tests: `edit_file` multi-edit end-to-end, `memory_edit` multi-edit on LF and CRLF/BOM pages (verifying scope confinement, stamped LF output, and refreshed index; plus write failure leaving stored page and index unchanged), single-edit fallback on minimal `ExecutionEnvironment` fake, explicit error on multi-edit against a fake without `BatchFileEditor`, `readGuard` warning emitted once.
  - Live and reopened transcript test: dispatch a stringified `edits` batch, confirm execution succeeds, and verify its edits render in both live events, reopened AppWire transcripts, and Go `transcript_render.go`.
- **TypeScript tests (`appwire-client/typescript`, `cmd/evener-hub/frontend`, `mobile-native`):**
  - `editOperationsOf`, `editBatchDiffText`, and shared diff line classification/`diffStats` unit tests covering single `edits[]`, multi `edits[]`, stringified/bare/aliased pre-repair shapes, legacy top-level `{old_string, new_string}`, malformed inputs, and edits removing/adding Markdown `---`/`+++` lines (confirming `diffStats`, web `DiffBlock`, and mobile `Diff` agree on both flat single-edit and multi-edit diffs).
  - `editFileSummary`, `memoryEditWords`, `EditFileBody`, `MemoryEditBody`, `evidence.ts`, and `StepEvidence.tsx` tests covering canonical, repaired, and historical transcripts.
  - Gates: `make test-api-package`, `make test-web`, `make test-native`.

### 9. Left to the Builder

- Exact wording of error messages (must identify every failing `edits[i]` index, both indices on overlap, include 1-based line numbers on duplicates and `nearestFileRegion` snippets, and truthfully state exact + whitespace-normalized matching ran; tests assert behavior, not phrasing).
- Exact `repair.ChangeKind` value for generic single-item array promotion.
- Helper decomposition inside `agent/execenv/local_files.go` and `agent/internal/tool/repair/repair.go`.
- Updating existing Go/TS tests whose hardcoded `edit_file` arguments bypass `prepareToolCall` (direct `Registry.ExecuteCall` callers).

### 10. Related Future Directions (Out of Scope Here)

- **Strict grammar-constrained tool decoding for `edit_file`:** Armin Ronacher's July 2026 findings showed enabling strict tool schemas eliminated ~20% `edits[]` schema errors on Opus 4.8 / Sonnet 5. Enabling strict mode on `DefEditFile()` (making `replace_all` nullable/required or opting into Anthropic's strict tool mode) is a clean follow-up orthogonal to this spec.
- **Hash-anchored edits (`hashline`):** Benchmarks in 2026 (Can Bölük, Lamberti `arXiv:2607.12713`, Oh My Pi) showed strong gains on non-Claude/non-OpenAI models by annotating `read_file` lines with short hashes/tags, at the cost of ~6% read token overhead and cross-cutting `read_file` wire changes. Worth evaluating separately if non-Claude/non-OpenAI edit reliability becomes a bottleneck.

### 11. Live Multi-Model Evaluation Plan (`tools/tool-fluency`)

After implementation, evaluate the change empirically across available models using Evener's existing `tools/tool-fluency/cmd/evener-fluency` harness (`matrix` + `--system-prompt-append`) before deciding whether to ship `edits[]` globally or adjust per surface.

#### Three-Arm Comparison (3 Repetitions per Cell)

| Arm | Binary / Prompt | Hypothesis Tested |
| --- | --- | --- |
| **Arm A (Baseline)** | `main` binary (single-edit `edit_file`, existing prompt) | Current behavior |
| **Arm B (Prompted parallel calls)** | `main` binary + `--system-prompt-append` instructing the model to emit multiple `edit_file` calls in one response for disjoint edits in one file | Whether parallel single-edit calls match or beat a schema change without risking RL drift |
| **Arm C (Atomic `edits[]`)** | `edit-file-multi-edit` binary (`edits[]`-only schema, 5-phase repair, CRLF/BOM fidelity, multi-error diagnostics) | Whether atomic single-call batching reduces turns/tokens and improves reliability |

#### Probe Suite (Extending `tools/tool-fluency/probes/edit_file.yaml`)

1. `edit_file.single_edit`: 1-hunk edit regression check.
2. `edit_file.disjoint_multi_edit`: 4 disjoint edits across a ~120-line file (primary batching efficiency test).
3. `edit_file.atomicity_recovery`: 3 edits where one target is stale/mismatched (measures partial-mutation risk in Arm B vs atomic all-or-nothing recovery in Arm C).
4. `edit_file.rename_plus_targeted`: `replace_all: true` rename plus a separate import/comment edit in one file.
5. `edit_file.ambiguous_context_recovery`: 3 similar blocks requiring disambiguation (tests line-numbered ambiguity diagnostics).
6. `edit_file.crlf_bom_multi_edit`: 2 edits on a CRLF + UTF-8 BOM file (tests byte-faithful preservation).
7. `edit_file.large_file_no_padding`: 2 distant edits in a ~300-line file (checks token waste from context padding and `read_file` line-prefix leakage).

#### Model Panel and Metrics

Run `evener-fluency catalog --model <id>` first to confirm each model exposes `edit_file` (Anthropic/Google surfaces; OpenAI/Codex surfaces use `apply_patch`). Target a diverse panel across available providers:
- **Google surface:** `vertex-arc/gemini-4-neon`, `vertex-arc/gemini-3.8-flash`, `vertex-arc/gemini-4-argon` (or `shisa/gemini-2.5-pro`).
- **Anthropic surface:** `kimi/kimi-for-coding`, `kimi/k3.1-pro`, plus any active Claude model.
- **Open-weight / stress-test models (where `edit_file` is exposed):** `lunaroute/deepseek-4.1-flash`, `lunaroute/glm-5.3`, `shisa/qwen3.8-max`, `ollama/gemma4:latest`.

Extract metrics directly from `results.jsonl` and `evener doctor`: pass rate (exact file bytes, zero shell escapes), turns to completion, total `edit_file` calls, tool error/retry rate, partial-mutation incidents, `EventToolCallRepaired` rate broken down by repair kind, and output/total tokens.

**Decision rule:** Ship Arm C if it matches or beats Arm A and Arm B on pass rate while reducing turns/tokens across the panel. If Arm B outperforms Arm C on key models, keep Stage 1's engine/diagnostic/CRLF/atomic-write improvements and evaluate keeping single-edit + parallel-call prompting on affected surfaces.
