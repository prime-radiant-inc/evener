# Issue #4459: parked search compaction

## Scope and authority

The issue and task acceptance criteria authorize this summary-only correction.
A search refused by the repeat-failure breaker must remain a refusal when
the compaction summarizer is asked to summarize it. Real registry refusals
already carry `IsError=true` and observation masking preserves all error
results without invoking this helper. Preserve that behavior: this fixes the
helper's classification, not a demonstrated live masking failure. The affected
model-context code is shared by all clients; client renderers do not change.

Only `agent/internal/contextmgr/context_manager.go`, its tests and test fixtures,
and this specification may change. The isolated lane includes `origin/main`
`48a6703dc790cd665bb574c6a411afe6ea0796d2` before this specification is written.

## Cause and detection

The leading hypothesis is that `searchresult.EntryCount` counts the refusal's
first paragraph as one search entry. `summarizeToolResult` currently calls it
unconditionally for `grep`, `glob` and `memory_search`. A second possibility is
that notes are being counted too, but `searchresult.Split` already separates
notes at the first blank line. The regression test must expose the first cause
in the helper before implementation, separately from the already-correct
observation-masking bypass.

For these three tools only, before counting entries, require both the original
tool result's trusted `IsError` flag and the exact, case-sensitive, byte-zero
prefix, shown as a quoted string:

```go
"evener did not execute this call: "
```

The trailing space is part of the marker. This is `parkPrefix` in
`agent/internal/tool/breaker.go`, documented there as the stable marker for
parked results. Do not trim leading whitespace, search for the marker anywhere
in the body, or match words such as `failed`, `refused` or `not execute` alone.
The marker is intentionally recognized as a contract rather than matching the
rest of the breaker's human-readable sentence. Since the constraint excludes
breaker changes, the summarizer keeps this exact marker locally; regression
inputs from the real registry protect the producer/reader boundary.

Carry `isError` explicitly through `summarizeToolResult` and its injected
callback contract in `maskToolResultContent`. Do not change which results
observation masking admits. In particular, it still returns before invoking
the callback when `IsError` is true.

The prefix is not reserved in successful output: a valid filename can begin
with those exact bytes. Every successful result retains its existing summary,
including such a filename and even text identical to a park sentence. An error
result containing only similar words, a missing trailing-space marker, or the
exact marker inside an entry, on a later line, or in notes also retains its
existing entry count and summary. Generic search errors are not reclassified.

## Summary shape

A parked result uses the same tool name and quoted pattern as an ordinary
search, replacing the count with `refused`:

```text
[grep: "p" → refused]
[glob: "*.go" → refused]
[memory_search: "p" → refused]
```

When the breaker includes its failure evidence after a blank line, keep it in
the existing semicolon-separated note form, without counting any of it:

```text
[grep: "p" → refused; The failures so far:; 1. FAILURE_SENTINEL; 2. FAILURE_SENTINEL]
```

Reuse the existing `searchSummary` note handling, including bracket stripping
and multiline flattening. Preserve all currently retained evidence; do not add
new truncation or special snippet parsing. The breaker's refusal sentence need
not be repeated: `refused` expresses the outcome, and the retained snippets
explain the failures. A refusal must never be described as zero matches, one
match or a file count.

## Preservation and recovery

Ordinary successful search summaries stay byte-for-byte unchanged. The #4223
behavior remains: registry repetition/failure nudges do not become search notes,
and the search's own notes, including multiple note blocks, remain intact.
No mutation of the original result, transcript or breaker ledger is introduced.
The breaker continues to own recovery through changed arguments or approach;
compaction neither executes nor retries the refused call. Original full output
remains available through existing transcript recovery.

## Non-goals

- No breaker output, threshold, dispatch, retry or ledger changes.
- No classification change for other tools or generic search errors.
- No client/UI, search counting, registry truncation or transcript schema change.
- No broad summary format redesign or assertion weakening.
- No full local test suite. CI remains the full-suite source of truth.
- No labels. Unrelated defects become separate GitHub issues.

## Verification

1. Before production changes, add regression cases for `grep`, `glob` and
   `memory_search`, each with and without snippets. Obtain refused output by
   executing two failed calls through a real `tool.Registry`, then a third call
   that is parked. Assert that the executor was called only twice and that the
   third result is an error. For the optional no-snippet shape, use the first
   paragraph of that same producer output.
2. Pin complete rendered summaries with `agenttest.CheckGolden` fixtures, not
   fragment assertions against production copy. Use opaque failure sentinels.
   Establish expected refusal fixtures before running the unchanged summarizer;
   record its wrong count-based output and nonzero exit.
3. Cover ordinary successful searches and near-marker data with whole-output
   fixtures. Include the exact marker embedded in content/notes and a missing
   marker delimiter, so a loose substring test fails. Include successful data
   beginning with the exact marker, including a filename collision and a full
   producer-generated park sentence, so ignoring error status fails.
   Independently pass real parked results, retaining `IsError`, through
   `maskObservations` and prove their content and error status remain unchanged.
4. Run the new regression selector before and after the fix, plus the #4223
   nudge/own-notes tests. Every new parked case must fail before and pass after.
5. Run `go test ./agent/internal/contextmgr ./agent/internal/tool -count=1`.
   These focused packages cover context compaction and breaker/tool registry
   behavior. Run `make build`, which performs frontend preflight, TypeScript
   checking/Vite bundling and the real runtime binary build. Read its scripts
   and report what actually ran. Do not run `go test ./...` or `make test`.
6. Freeze this spec for two independent competing PAR reviewers before code.
   Resolve legitimate findings and re-review affected requirements. Repeat PAR
   over the frozen implementation diff, then run simplify-code.
7. Commit intended paths and run branch RoboRev. Before final push, fetch
   `origin/main` again and merge if it moved, renewing affected checks/reviews.
   Open a main PR closing #4459 and shepherd its current-head CI and RoboRev.
   Admin squash-merge only after every required check is green and every
   RoboRev finding is low or conclusively refuted. Verify GitHub's merged state
   and merge SHA before cleanup/reporting.
