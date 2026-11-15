# Tools and execution environment

## Verdict

The clearest recurring waste is `grep`: a normal ripgrep search launches two additional full-tree file listings to count oversized files. `read_file` also reads and transforms a whole file before returning a requested line window, while the breaker hashes each complete result body once. Tool lookup, schema validation, the Git-root cache, and alias tables did not show comparable avoidable per-call work.

## Findings, ranked

Rank reflects estimated CPU saved times frequency divided by fix risk; costs are code-traced unless labeled executed.

### 1. **High: `grep` launches two extra ripgrep walks on ordinary searches**

**Path and call path:** `agent/session_tools_shell.go:287-291,941-943` registers `execFileGrep`; a model `grep` call reaches `LocalExecutionEnvironment.GrepSkipping` (`agent/execenv/local_search.go:329`), which runs the main ripgrep at `:357-383`. On successful searches, `:424` unconditionally calls `grepRgOversizeCount`; `:493-511` runs `grepRgListFiles` twice, and `:522-546` starts one `rg --files` process per listing. The zero-match path also performs a native exclusion walk at `:448-471,558-588`.

**Mechanism:** A successful search therefore starts three ripgrep processes and traverses the candidate tree for the search plus two listings. A zero-match search adds another native tree traversal. The additional listings exist to report the oversized-file omission count, but they are paid even when the model only needs the matches.

**Evidence, read:** the call path and unconditional success-path invocation are at the cited lines. No syscall/process counter was run for `Grep` itself.

**Scale:** Per `grep` tool call, normally 3 child processes and up to 3 ripgrep tree passes; a no-match call adds one native walk. Work grows with candidate files and directories, not with the number of returned matches.

**Fix design:** Keep the omission note but replace the pair of `grepRgListFiles` subprocesses with one `rg --files --null` listing and count over-cap files with `os.Stat` on the emitted paths. This removes one process and one full listing pass without changing which matches are returned. Check handling of deleted/unreadable files and exit-1 empty listings. Add command-factory call-count tests for success, no-match, and over-cap-file cases, and compare a large-tree benchmark before and after.

**How to verify the win:** Benchmark `LocalExecutionEnvironment.Grep` on a fixed generated tree; instrument the command factory to assert process count and compare elapsed CPU, allocations, and directory/file visits. The normal successful path should fall from three subprocesses to two, with identical output and oversized-file notes.

### 2. **High: `read_file` reads whole files for a line window**

**Path and call path:** `agent/session_tools_file.go:18-23,49-57,61-80` registers and executes `read_file`; `LocalExecutionEnvironment.ReadFileCtx` reaches `ReadFileAndBytesCtx` (`agent/execenv/local_files.go:90-115`), which calls `readFileCappedCtx` for the entire file. `NumberLines` then normalizes and scans that buffer (`local_files.go:176-204`). The whole-file cap is 128 MiB (`agent/execenv/read_capped.go:12-21`).

**Mechanism:** `offset` and `limit` bound the returned text but do not bound bytes opened, allocated, or examined. The path loads the full byte slice, converts it to a string, normalizes CRLF, checks for NUL, then scans for the selected lines. Repeated reads of a small window in a large file repeat this work.

**Evidence, read:** the cited path passes offsets only to `NumberLines`, after the whole-file read. No file-size benchmark was run.

**Scale:** Per text-file read, O(file bytes) I/O and copying, even for a 2,000-line page near the beginning; worst-case input is 128 MiB per call. Cost repeats per call.

**Fix design:** Add a contextual line-window reader in `execenv` that streams to the requested line range and emits only that range. Keep full reads for image/PDF handling. Preserve the whole-file size bound. Explicitly decide how to retain the existing whole-file NUL rejection when NUL occurs outside the requested window; a chunked NUL check retains behavior but still scans the file, while stopping at the window changes that behavior. Tests should cover CRLF, trailing newlines, out-of-range offsets, oversized files, binary data before and after the window, and image/PDF reads.

**How to verify the win:** Compare `ReadFileCtx` on a fixed large text file at `offset=1, limit=20` and near EOF. Record bytes read, allocations, and CPU; confirm returned numbering and newline behavior match the existing implementation for supported text cases.

### 3. **Medium: breaker repetition hashing traverses every result body**

**Path and call path:** model tool call → `Registry.ExecuteCall` → `executeCall` (`agent/internal/tool/registry.go:806-818,930-944`) → `failureLedger.record` (`agent/internal/tool/breaker.go:413-452`) → `shortHash([]byte(output))` at `breaker.go:427`. `record` runs for judged executed calls, including successes.

**Mechanism:** The ledger computes a SHA-256-derived 64-bit body fingerprint over the full untruncated result to detect repeated identical outputs. This is an additional linear pass over output bytes and may be significant for large `FullOutput` values. The failure-class hash is separate and hashes at most the normalized first nonblank line, truncated to 200 runes (`breaker.go:535-546`).

**Evidence, read:** code trace above. `agent/internal/tool/breaker_bench_test.go:14-24` benchmarks repeated-failure ledger recording on a roughly 3 KiB body, but that benchmark was not run in this audit. No standalone SHA/body-size benchmark was run; there is no measured SHA-256 cost to report.

**Scale:** One complete result-body hash per judged executed tool call, O(result bytes); independent of session history. Large grep/read/shell outputs cost more than short status messages.

**Fix design:** Consider replacing only the repetition body fingerprint with a fast, process-keyed 64-bit hash such as `hash/maphash`, leaving SHA-256 semantic dispatch fingerprints unchanged. The current fingerprint is already truncated to 64 bits, but changing the hash still changes collision behavior; test identical and distinct large outputs, and adversarial collision cases. Keep the full-body equality contract and verify nudges still occur on the same repeated outputs.

**How to verify the win:** Run `go test ./agent/internal/tool -run '^$' -bench 'FailureLedger_RecordRepeatFailure' -benchmem -benchtime=1x`, then add a benchmark case for multi-megabyte successful output. Compare `ns/op` and allocations and run focused breaker dispatch tests.

### 4. **Medium: brace-expanded glob alternatives can repeat overlapping walks**

**Path and call path:** model `glob` call → registration in `agent/session_tools_shell.go:295-317` → `GlobWithBudget` (`agent/execenv/local_search.go:143-205`). It expands alternatives at `:147`, then loops each expanded pattern at `:177-199`, invoking `globMatches` separately at `:178`; deduplication happens after each traversal.

**Mechanism:** Alternatives that share a recursive prefix or overlap (for example, brace alternatives) can enumerate the same directories and candidate paths multiple times before the `seen` map removes duplicate results. Default ignore discovery is also constructed for each call at `:165-172`; it is not a persistent matcher cache.

**Evidence, read:** code trace above. No glob walk-count benchmark was run. `agent/execenv/gitignore.go:374-385` shows `loadIgnoreSet` builds its `ignoreSet` and `loadedRules` map per invocation, so `.gitignore` files are reopened/recompiled on subsequent calls; within one invocation, duplicate reads are avoided.

**Scale:** Per glob call, at least one matcher-discovery walk when exclusions apply; the matching walk can repeat for each expanded alternative, O(alternatives × overlapping tree). `.gitignore` reads and rule compilation recur across calls.

**Fix design:** Let the glob walker evaluate a set of expanded patterns during one traversal, then keep the existing path deduplication and mtime ordering. This avoids a persistent ignore cache and its invalidation/staleness risk. Add tests for brace alternatives, overlapping recursive patterns, exclusions, and result order. Cache ignore matchers only if profiling shows remaining cost warrants the complexity; key them to file identity/metadata and account for live `.gitignore` edits.

**How to verify the win:** Add a fixed-tree glob benchmark with overlapping alternatives and an instrumented filesystem counting directory opens and `.gitignore` reads. Compare visits, allocations, and CPU; results and ordering must match.

### 5. **Low: `apply_patch` rebuilds a complete file for every update operation**

**Path and call path:** `agent/session_tools_shell.go:352-362` registers `apply_patch`; `ApplyPatchCtx` parses the patch and applies each operation (`agent/internal/tool/apply_patch.go:24-40`). `updateFileOp.apply` reads all bytes, creates normalized text and a full line slice, searches each hunk, then joins and writes all lines (`apply_patch.go:91-152`).

**Mechanism:** A small patch to a large file still materializes every source line and a second complete output string. Each hunk searches forward from the current position; a failed hint search can fall back and search again (`:111-123`). The normal case is linear, but repeated failed/narrowed searches can revisit portions of the file.

**Evidence, read:** code trace above. No apply-patch benchmark or adversarial hunk count was run.

**Scale:** Per updated file, O(file bytes + line count) allocations/copies; hunk matching is typically linear over disjoint forward ranges, but fallback searches can add repeated scans. Work repeats per file in a patch.

**Fix design:** First measure this path before changing it. If material, retain the current whole-file read/write safety contract but index candidate anchor lines once and use that index for hunk placement; avoid fallback rescans where the hint already failed. A streaming rewrite is riskier because it must preserve mismatch diagnostics, fuzzy match semantics, final-newline handling, and atomic mutation behavior. Test repeated anchors, hints, missing hunks, CRLF, and final newline.

**How to verify the win:** Add focused benchmarks for one small hunk in a large file and many hunks in one file; compare CPU and allocations. Keep `apply_patch_test.go` and sandbox mutation tests passing.

### 6. **Low: each subprocess rebuilds the launch environment and PATH overlays**

**Path and call path:** `ExecCommand`/`ExecArgv` → `execPreparedCommandStdout` (`agent/execenv/local_exec.go:29-39,69-97`) → `launchEnv` (`local_exec.go:386-390`) → `commandEnvironment` (`agent/execenv/local.go:264-269`) and virtualenv PATH injection. `findExecutable` performs a fresh `execLookPath` call (`agent/execenv/local.go:324-329`).

**Mechanism:** Every subprocess call rebuilds the inherited environment, filters and overlays it, probes virtualenv paths, and resolves executable names. This is normal command setup, but the extra ripgrep processes in finding 1 multiply it for one `grep` tool call.

**Evidence, read:** code trace above. No environment-construction or PATH-lookup benchmark was run.

**Scale:** Once per child process, O(environment entries plus PATH directories and virtualenv roots); up to three times for a normal current ripgrep search. No cost when no subprocess is launched.

**Fix design:** Profile first. If measurable, cache only immutable base environment entries and the stable `rg` path per `LocalExecutionEnvironment`, while rebuilding caller overlays and virtualenv PATH additions each invocation. PATH and environment changes, and linked worktree `.venv` changes, could make cached values stale; test those transitions before adopting a cache.

**How to verify the win:** Benchmark `launchEnv` and repeated `findExecutable("rg")` with the same environment, plus the real grep benchmark. Compare CPU and allocations and confirm PATH/venv changes remain visible.

## Clean

- `Registry.executeCall` resolves the registered tool with one map lookup under `RLock` (`agent/internal/tool/registry.go:837-840`); dispatch uses the already-registered compiled schema. There is no per-call schema clone or schema compilation on this path.
- Schema compilation is cached across registries by marshaled parameter schema (`agent/internal/tool/registry.go:1290-1355`). A cache hit still marshals the schema at registration, not on every tool call.
- Tool-name alias maps are built once in `agent/internal/toolname/toolname.go:10-34`; `ClaudeToEvener` and `EvenerToClaude` are map lookups (`:36-52`).
- Local Git-root lookups use per-environment caches (`agent/execenv/gitpath.go:55-62,164-169`; `agent/execenv/local.go:200-233`). Normal worktrees are structurally resolved before the `rev-parse` fallback (`gitpath.go:104-119`). `RunGit` uses direct `ExecArgv` for local environments and avoids a shell process (`agent/execenv/rungit.go:18-40`).
- Grep's ripgrep output capture is bounded at the pipe and parsed from retained data (`agent/execenv/local_search.go:378-400`); it does not retain unbounded stdout.
- `list_dir` uses a budgeted walk and stops once the page is answered (`agent/session_tools_shell.go:244-269`), rather than enumerating the full subtree for ordinary pages.

**Executed benchmark:** `GOCACHE="$EVENER_SCRATCH_DIR/go-cache" go test ./agent/execenv -run '^$' -bench 'Git' -benchmem -benchtime=1x` exited 0 on darwin/arm64, Apple M5 Max:

```text
BenchmarkGitStatusShellWrapper-18        1  2542017958 ns/op  102352 B/op  245 allocs/op
BenchmarkGitStatusDirectExec-18          1  3526909125 ns/op  115960 B/op  397 allocs/op
PASS
ok   primeradiant.com/evener/agent/execenv  12.015s
```

The host was heavily contended by other Go builds; these single-iteration timings are noisy and do not establish that direct execution is slower. The benchmark contrasts one shell-wrapper invocation with one direct invocation; each benchmark operation still launches Git. No Git process counter or `dtruss` trace was run. The local Git-root cache was verified by code trace, not measured here.

## Open questions

- Should `read_file` preserve its current whole-file binary/NUL refusal when a NUL byte lies beyond the requested line window? A truly early-stopping window read changes that behavior; a full chunked NUL scan preserves it but still reads every byte.
- Is the oversized-file count note worth the extra grep scan? The one-listing-plus-stat design retains the note with fewer process launches, but should be measured against a design that omits the note unless an oversized match is encountered.
- Is a non-cryptographic body fingerprint acceptable for breaker repetition detection? It would likely be cheaper, but correctness depends on tolerating the existing 64-bit collision risk.
- No tool-package benchmark was run. In particular, this audit has no measured allocation or timing result for `Registry.ExecuteCall`, `failureLedger.record`, or the call-path observations above; the only executed benchmark was the Git pair reported in Clean.
