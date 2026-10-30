# Issue #4206: scope-root-relative memory search paths

Status: frozen for review, 2026-10-23. The issue's explicit acceptance criteria authorize this bounded behavior correction.

## Contract

Every `memory_search` result names a file relative to the selected personal or project scope root, in the same form accepted by `memory_read.file_path`. Searching `path: "sub"` for a match in `sub/page.md` returns `sub/page.md`, not `page.md`. Blank paths and `.` search the root. Accepted leading `./` segments are normalized away. Explicit file targets name that file, including a file directly at the root, rather than `.` or an unnamed content/count row.

This applies to `content`, `files_with_matches`, and `count`, including context lines. File names retain the existing `OneLinePath` escaping. Matched and context page text remains byte-for-byte unchanged. Result counts, truncation notes, group separators, limits, regex/glob matching, ignore rules, confinement, cancellation, explicit large-file streaming, and exclusion of the root generated `MEMORY.md` retain their current behavior.

The `path` argument still selects exactly the existing search base. A subdirectory search must not scan the scope root and filter afterward: that changes ignore discovery, glob matching, work bounds, and access behavior. Sibling and root pages outside that search base remain excluded even when they match the same pattern.

## Ownership and seam

`memoryFileArgs` continues validating scope-relative input and forwarding the absolute, confined search target. `execMemorySearch` selects a new confined-memory search entry point in `agent/execenv`, alongside its existing confined file operations. That entry point invokes the same native sandbox walk with an explicit presentation root equal to the environment's working directory.

The walk keeps its search base, selection paths, skip predicate paths, and file I/O unchanged. Only the accumulator's presentation path is rebased from search-base-relative to scope-root-relative before `OneLinePath` and result formatting. Explicit-file streaming uses the same presentation path. Existing generic `Grep`/`GrepSkipping` calls retain their original entry point and default presentation behavior. No rendered-result parsing or whole-text substitution is used, because file names and page text may contain grep separators or scope host paths.

This extends #4201's naming contract from memory summaries/errors to search result path fields. #4201's successful reads/searches must still preserve page text. Its assertions remain intact. The old searched-path-relative description in `docs/tools/memory.md` is superseded by this contract; the user's explicit file restriction keeps that document outside this change.

The #4137 named-file case in `TestMemorySearchSkipsTheLegacyRootIndex` proves that a confined search actually searches the named page, not that memory must retain generic grep's `.` presentation. The parent explicitly approved correcting that one superseded expected value to `p.md`, preserving the test, named-page search, and all root-index exclusion assertions. A red/green run must show `.` before and `p.md` after. Ordinary `Grep` retains its own pinned `.` and unnamed-single-file presentation.

## Scope and non-goals

Allowed changes are the new spec, memory search rendering/rebase code (including the confined renderer entry point and native accumulator presentation seam), and its tests. No generic grep/glob behavior change, storage/migration change, client rendering change, new matching policy, labels, or unrelated cleanup. There is no persistent format change and rollback requires no data repair. The fix is read-only and leaves memory pages untouched.

## Verification

1. Add a regression through registered `memory_search` using `memoryExec`, not a fabricated renderer result. Seed `sub/page.md`, a conflicting root `page.md`, and another matching sibling. Search `sub`; assert the actual returned filename is `sub/page.md`, then pass it unchanged to registered `memory_read` and verify the nested page's opaque body, not the root decoy.
2. Cover both scopes, blank/root search, explicit root file, explicit nested file, `./sub`, and `./sub/page.md`. Cover all output modes, content context lines, grouped matches and truncation, glob filtering relative to the original search base, and JSON-quoted unusual paths. Verify actual result fields and opaque page bytes, not human-readable copy.
   Also search an explicit nested memory file larger than the directory read cap, forcing the named-file streaming branch. Assert its scope-root-relative name in all three output modes and in content context lines. Existing ordinary grep single-file and streaming assertions must remain intact.
3. Run the new regression before production changes and retain the exact red result. Rerun after the fix, preserving all prior assertions from #4201.
4. Run focused memory tests in `./agent` and grep/path tests in `./agent/execenv`, including ordinary grep's single-file behavior. Run `make build` after inspecting its target. Do not run the full test suite locally; CI remains the source of truth.
5. Freeze and review this spec with two independent competing PAR reviewers. Review the immutable implementation diff with another two-reviewer PAR round, run simplify-code, commit and branch RoboRev, then push and shepherd a PR. Fetch current main before final push and merge it if it moved. Admin squash-merge only with all required current-head checks green and every RoboRev finding low or conclusively refuted.
