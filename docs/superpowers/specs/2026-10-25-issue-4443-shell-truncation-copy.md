# Issue #4443: one owner for the shell truncation note

**Status:** Approved after two PAR rounds; implemented and verified.

**Base:** `origin/main` at `1e37237d8e64410a73cedba6292681b27bb9fadd`,
which contains the mandated `f6900f5d8ff3e436a474054a7211fff746d1f451`.

## Problem

Two code paths format the same human-readable tail-elision sentence:

- `appwire-client/typescript/toolCallText.ts`'s `tailFold` (function at line
  48, return at line 50) builds
  `earlier output not retained — showing the last ${max.toLocaleString("en-US")} chars`
  before the kept tail slice.
- `cmd/evener-hub/frontend/src/panes/session/transcript/tools/shellTool.tsx`
  (line 93) hand-builds the same sentence for a settled, truncated shell body:
  `earlier output not retained — showing the last ${TOOL_BODY_MAX_CHARS.toLocaleString("en-US")} chars\n${renderedOutput}`.

The strings currently agree byte for byte. They are two owners, so a wording or
number-formatting change can land in one path and miss the other. Issue #4443
asks for one shared `tailFoldNote(max)` owner beside `tailFold`, with
`shellTool` reusing it and a test that fails if the two paths diverge again.

The shell path cannot simply call `tailFold`: by the time it renders, the ANSI
tail has already been sliced and rendered by `AnsiTailBuffer`, and it may carry
a synthesized exit footer. Calling `tailFold(renderedOutput, max)` would
tail-slice already-sliced text and could show escape bytes instead of the
rendered tail. Only the note sentence is shared; the kept tail is composed by
each caller.

## Chosen behavior

1. `appwire-client/typescript/toolCallText.ts` exports
   `tailFoldNote(max: number): string`, the one owner of the sentence:
   `earlier output not retained — showing the last ${max.toLocaleString("en-US")} chars`.
   It returns the note line without a trailing newline, so each caller joins it
   to its own kept tail.
2. `tailFold(text, max)` keeps its current no-op under budget and, over budget,
   returns `${tailFoldNote(max)}\n${tailSlice(text, max)}`.
3. `appwire-client/typescript/index.ts` root-exports `tailFoldNote` from
   `./toolCallText`, beside `tailFold`.
4. `shellTool.tsx` imports `tailFoldNote` from `@evener/appwire-client` and
   replaces its hand-built string with
   `${tailFoldNote(TOOL_BODY_MAX_CHARS)}\n${renderedOutput}`.
5. Nothing else changes: the note still appears only for a settled
   (`!live`) body whose `AnsiTailBuffer` reports `truncated`; `copyText`
   remains the raw retained bytes; ANSI rendering and the synthesized exit
   footer are untouched.

### Byte-identical rendered text

For `TOOL_BODY_MAX_CHARS = 8000`, the rendered note before and after is
exactly:

```text
earlier output not retained — showing the last 8,000 chars
```

followed by the newline and the previously rendered tail. The existing
shell body tests that assert the full rendered string (the ANSI boundary and
OSC payload cases) must pass unchanged, and the inverse-video case keeps
asserting the styled background.

## Tests and divergence evidence

The change is a behavior-preserving refactor: today's two strings already
agree, so no runtime test can fail on the pre-change tree for the behavior the
refactor preserves. This spec does not pretend otherwise. Divergence is guarded
by a cross-path test, and the guard is proven red by mutation.

1. **Cross-path divergence test (new, in `shellTool.test.tsx`).** Render a
   settled over-budget shell body whose output is plain text and whose
   `exitCode` is absent, so shell's rendered tail is exactly
   `tailSlice(output, TOOL_BODY_MAX_CHARS)` with no synthesized footer. Use the
   one-line fixture shape the existing shell tests use
   (`"x".repeat(TOOL_BODY_MAX_CHARS + 100)`), because `CodeBlock` folds past 100
   lines and would hide the note from the rendered `<code>`. Assert the output
   `<code>` text equals
   `tailFold(output, TOOL_BODY_MAX_CHARS)` imported from
   `@evener/appwire-client`. That compares two production paths with no mock
   and no hardcoded copy, the same parity style as the
   `formatToolDuration`/`formatDurationMs` test (#1228). It fails when one
   side's note wording or number formatting drifts, so a re-duplicated literal
   in either path turns it red.
2. **Mutation evidence, before the production change.** Temporarily change the
   hand-built literal in `shellTool.tsx` (`chars` to `characters`), run the new
   test, and record that it fails. Restore the literal. This shows the test
   detects a divergent format string in the current code.
3. **Mutation evidence, after the shared owner lands.** Temporarily change the
   literal in `tailFoldNote` (`chars` to `characters`); the cross-path test
   stays green because both callers move together, while the existing
   exact-string shell tests fail. Restore. The green cross-path test under a
   shared-owner mutation, and its failure under a one-sided mutation, is the
   proof that the two paths have one owner.
4. **Byte-identical guard.** The existing shell body tests keep asserting the
   exact rendered text, including `8,000`, across plain text, ANSI state
   restoration, and an OSC payload (`shellTool.test.tsx`'s ANSI-restoration,
   ESC-intermediate, and OSC-payload tests).
5. **Focused checks, no full suite:**

   ```sh
   cd cmd/evener-hub/frontend
   npx vitest run src/panes/session/transcript/tools/shellTool.test.tsx \
     ../../../appwire-client/typescript/toolCallText.test.ts
   npm run typecheck
   npx biome check --write \
     src/panes/session/transcript/tools/shellTool.tsx \
     src/panes/session/transcript/tools/shellTool.test.tsx \
     ../../../appwire-client/typescript/toolCallText.ts \
     ../../../appwire-client/typescript/toolCallText.test.ts \
     ../../../appwire-client/typescript/index.ts
   cd ../../.. && make build
   ```

   CI owns the full suite. The AppWire package tests run through the
   frontend's Vitest config, whose include glob is
   `../../../appwire-client/typescript/**/*.{test,spec}.?(c|m)[jt]s?(x)`
   (`vite.config.ts:179-182`).

## Non-goals

- No change to the sentence, its punctuation, its number formatting, or the
  8,000-character budget.
- No change to `AnsiTailBuffer`, `CodeBlock`, the shell descriptor's summary,
  exit-code handling, or `copyText`.
- No change to other tool bodies (`bodies.tsx` already calls `tailFold`) or to
  any other user-visible copy.
- No mock of `@evener/appwire-client` or any other first-party package, and no
  derived-oracle assertion that restates the implementation.
- No new AppWire subpath, package manifest entry, or qualification-runner
  assertion.
- No GitHub labels, and no unrelated cleanup.
- No full local suite; CI is the source of truth.
- No merge: the parent owns the CI wait and merge.

## Review and delivery

1. Two independent competing PAR reviewers review this frozen spec against the
   same issue, source files, and base commit; legitimate findings are resolved
   before implementation. This revision resolves round 1's two blocking
   findings and is re-reviewed.
2. A second PAR round reviews the frozen implementation diff.
3. `/simplify-code` runs on the touched files, then the focused checks above.
4. One commit, `roborev` branch review, push, and a PR against `main`. The
   parent owns the settle command, CI wait, and merge.

## Revision notes

Round 1's two independent reviewers agreed on two findings, both accepted:

- **Dropped the proposed mocked seam test.** Replacing `tailFoldNote` with a
  sentinel through `vi.mock("@evener/appwire-client")` would mock a first-party
  package to prove a consumer goes through it. `docs/developing-evener/testing.md`
  forbids mocking Evener internals and assigns "every consumer actually goes
  through the library" to a static audit, not consumer sabotage. The
  cross-path parity test replaced it.
- **Dropped the derived-oracle composition assertion.** Asserting
  `tailFold(...)` equals `` `${tailFoldNote(max)}\n${tailSlice(...)}` `` restated
  the implementation and passed with an identical inline note. The cross-path
  test plus mutation evidence replaced it.

Minor corrections: the Biome command now names the five touched paths; the
`tailFold` line reference now names the function at line 48 (the sentence is
built at line 50); the `vi.hoisted` detail is moot because the mock is gone.

Round 2: both reviewers approved the revision (`d682ed81…60bf2`) with no
blocking or significant findings. Three editorial nits were accepted: the
parity fixture is pinned to a single line because `CodeBlock` folds past 100;
the drift sentence says one-sided divergence; the Vitest include glob is
quoted exactly. The inverse-video test is described as a styling assertion,
not a full-string one.

The implementation PAR round also approved with no blocking or significant
findings. Its nits were accepted where they were precision, not scope: the
parity test's comment now says an identical re-inline cannot be detected by a
runtime comparison, and this spec names the guard tests by title instead of
line numbers that shift when a test is inserted.
