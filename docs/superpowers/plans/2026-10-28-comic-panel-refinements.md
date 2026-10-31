# Comic Panel Refinements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render bounded Comic panels with Markdown, character-free technical inserts and launch-positioned child strips.

**Architecture:** Keep the existing Comic projection, canonical activity owners and single virtual reader. Add Comic-owned Markdown composition and source mapping around the existing Markdown and CodeBlock widgets. Coordinate bounded geometry with the real measurer; change child placement without changing child membership or lifetimes.

**Tech Stack:** Existing TypeScript, React, marked, DOMPurify, Vitest and Chrome browser guard. No new dependency.

**Spec:** `docs/superpowers/specs/2026-10-28-comic-panel-refinements-design.md`, approved by Jesse. Two independent PAR reviewers found no significant specification defects at `0cddaa1a46`; their reports were static reviews, not runtime evidence.

## Global Constraints

- Ordinary panels: preferred maximum width `26rem`. Technical inserts: maximum `40rem`. Both fit available width; retain the one-column boundary at 760 CSS pixels.
- Preserve complete technical blocks, grow vertically and scroll wide content horizontally. No truncation, text shrinking or nested vertical reader.
- Keep raw transcript text, source identities and source-offset anchors unchanged. Prose and tables use Comic Sans; code retains fixed-width lettering.
- No new widget or widget API without separate Jesse approval. Import widgets through the barrel; never style their internals.
- Preserve canonical membership, qualified identities, draft ownership, child read lifetimes and recovery. No protocol changes or deployment.
- Read `AGENTS.md`, `docs/developing-evener/testing.md` and `docs/developing-evener/design-system.md`. Assert behavior, not production copy. Do not mock Evener internals.
- Before running checks, inspect their scripts. Use the worktree's matching dependency install; never run `npm ci` through a shared symlink. Run heavy gates sequentially.

**Selection scope:** Keep rendered text selectable and preserve its reading location through reload; do not add persistent saved text selections.

## Review Focus

1. Reference links defined outside a panel must still resolve without displaying definitions or changing raw source: Task 2.
2. Nested lists/quotes containing code or tables, CRLF, escapes, entities and emoji must retain structure, ordering and accurate source mapping: Task 2.
3. Incomplete streamed fences/tables must settle without duplicate content, empty character panels or moving an away-reader: Tasks 2 and 4.
4. A completion-only notification, remote child or newly discovered older launch must never produce a false launch location or replace a live child reader: Tasks 3 and 4.
5. Font failure, large blocks and phone reflow must preserve readable content and a later-page source anchor through reload/reconnect: Tasks 1, 2 and 4.

## File responsibilities

Existing paths below are relative to `cmd/evener-hub/frontend/` unless stated otherwise.

- `src/comic/comic.module.css`, `measure.ts`: visible/probe geometry and measured formatted content.
- `src/comic/markdown.ts` (new): semantic parts, safe composition boundaries and original-source mapping. It owns no durable state.
- `src/comic/ComicMarkdown.tsx` (new): compose existing widgets for those parts; expose source-point lookup for rendered text.
- `src/comic/layout.ts`, `ComicPanel.tsx`: compose prose and insert panels, retain identity and images.
- `src/comic/ComicTranscript.tsx`: consume formatted measurements and source points, retain existing restoration/lifetime logic.
- `src/comic/delegates.ts`, `comicRows.ts`: launch evidence and chronological strip placement.
- Existing adjacent tests, `scripts/comicguard/run.mjs`, root-relative `cmd/evener-hub/comic_browser_test.go`: prove changed behavior through real code.
- Root-relative `docs/product/comic-chat.md`: current verified behavior.

## Task 1: Bound panels and their measurement

**Files:** Modify `src/comic/comic.module.css`, `src/comic/measure.ts`, `src/styles/tokens.css`; extend `src/comic/layout.test.ts` and create `src/comic/measure.test.ts`. Add focused geometry assertions to `scripts/comicguard/run.mjs`.

**Interfaces:** Preserve `createComicMeasurer(container: HTMLElement)` and its `fits`/`dispose` contract. CSS and the probe must use the same width constraints, including paired rows. No consumer API change in this task.

- [ ] Add a failing rendered/probe test for single and paired panels in a wide container, plus phone widths. Use real computed geometry in Chrome for the final assertions; jsdom cannot establish layout. Assert:

```ts
expect(single.width).toBeLessThanOrEqual(26 * rootFontSize + 1);
expect(single.width).toBeLessThan(availableWidth);
expect(probe.width).toBeCloseTo(single.width, 0);
expect(phone.panelRight).toBeLessThanOrEqual(phone.readerRight + 1);
```

- [ ] Run `npx vitest run src/comic/layout.test.ts src/comic/measure.test.ts` from the frontend and the focused geometry assertion through the existing browser guard. Record the actual failing assertion; a launch/setup error is not a red test.
- [ ] Declare `--comic-panel-max-width: 26rem` and `--comic-insert-max-width: 40rem` in the central tokens. Apply the dialogue bound, left-aligned gutters and non-stretching single rows. Make the hidden probe use the same geometry, with its own document/window. Keep the paired and narrow composition rules and image behavior.
- [ ] Rerun the targeted tests and Chrome geometry, with bundled and failed fonts. Verify visible text is contained and panel width stays capped as the container grows.
- [ ] Format touched TypeScript from the frontend directory, inspect the diff, and commit only this task's exact files.

## Task 2: Render Markdown and technical inserts with source anchors

**Files:** Create `src/comic/markdown.ts`, `src/comic/markdown.test.ts`, `src/comic/ComicMarkdown.tsx`, `src/comic/ComicMarkdown.test.tsx`; modify `src/comic/layout.ts`, `measure.ts`, `ComicPanel.tsx`, `ComicTranscript.tsx`, `comic.module.css` and their adjacent tests. Reuse `src/widgets` without modifying its API.

**Interfaces:**

- `projectComicMarkdown(source: string, live: boolean): ComicMarkdownDocument` in `markdown.ts`. The document retains the original source and ordered `ComicMarkdownPart` values. Each part has `kind: "prose" | "code" | "table"`, original UTF-16 `start`/`end`, render input and an explicit visible-text-to-source mapping. Code parts carry their literal code payload and the recorded language, if any. Mapping types remain local to this module.
- `ComicMarkdown({ document, part }: { document: ComicMarkdownDocument; part: ComicMarkdownPart })` in `ComicMarkdown.tsx`, using existing widgets. `comicMarkdownPoint(root: HTMLElement, offset: number): DOMRect | undefined` maps an original source offset to its rendered glyph, or the nearest visible boundary for hidden syntax.
- Extend each `ComicRow.panels` element with its Markdown part while retaining `ComicSlice`'s original `entryId/start/end`. Add an `"insert"` composition. Export `ComicFits = (document: ComicMarkdownDocument, part: ComicMarkdownPart, composition: ComicComposition, kind: ComicEntry["kind"]) => boolean` from `layout.ts`; use it for `composeComicRows`'s third argument and the measurer's `fits`. `comicRows.ts` continues to call the composition API through its existing inferred measurer type.

- [ ] Add failing behavior tests using independently supplied source. Require real `strong`, heading, list, link and table elements; correct link destinations across part boundaries; code whitespace; no stage, portrait or balloon on inserts; no empty prose panel around code-only messages; and tool images exactly once. Pin ordering structurally:

```ts
expect(parts.map(part => part.kind)).toEqual(["prose", "code", "table", "prose"]);
expect(parts.map(part => source.slice(part.start, part.end)).join("")).toBe(source);
expect(insert.querySelectorAll("[data-comic-scene], [data-comic-balloon]")).toHaveLength(0);
expect(renderedCode.textContent).toBe(codeFixture);
```

- [ ] Run `npx vitest run src/comic/markdown.test.ts src/comic/ComicMarkdown.test.tsx src/comic/ComicPanel.test.tsx src/comic/layout.test.ts`. Require behavioral failures before production changes.
- [ ] Implement semantic composition using the existing `markdownLexer`, recursively respecting list/quote context. Carry document reference definitions into derived prose/table render input, never into stored transcript text. Preserve source spans independently of that preparation. Do not substitute regex-only Markdown parsing or a greedy string search for a source map. Keep formatting constructs intact across continuations; retain grapheme-safe splitting for plain text. Indivisible constructs grow rather than lose formatting or content.
- [ ] Implement the local renderer with `Markdown` for prose/tables and `CodeBlock` with `fold={false}` for code. Code fences, including language-tagged fences, stay code inserts. Use CodeBlock's existing `renderLine` seam where needed for source mapping. Technical content gets an intrinsic-width layout inside the bounded horizontal scroller; prove long lines remain intact, since the existing CodeBlock normally wraps. Add no terminal-window controls or styling of widget descendants. If these existing APIs cannot meet the proof, stop and request the necessary widget change before implementing it.
- [ ] Wire the rendered-content measurer and panel composition together. Measure actual shared-widget markup, not approximate plain text. Use the same prose renderer and width rules for measurement and display. Keep reference context and live state in cache identity. Technical blocks bypass balloon line budgets and use the `40rem` bound. Retain source attribution, thought treatment, image identity and final-slice image ownership.
- [ ] Replace the single-TEXT_NODE assumption in `ComicTranscript.tsx` with the source-point mapper for formatted panels. Keep the literal/plain path working. Use the element's owning document for ranges and selection. Keep existing saved-position format and restoration owners; no compatibility layer or new storage format.
- [ ] Add mapping tests for nested emphasis/links, repeated text, entities, escaped syntax, CRLF, tabs, astral characters, lists/quotes containing technical blocks, hidden Markdown delimiters and reference definitions. Check known original offsets against DOM text nodes in both directions. Test live prefixes of fences/tables through settlement and a document edit that invalidates prior parsing. Verify raw HTML remains escaped, unsafe link destinations are absent and supplied source text stays selectable; no second sanitizer policy.
- [ ] Run `npx vitest run src/comic` and `npm run typecheck`. Preserve the old raw-source reconstruction and grapheme assertions even where visible output now renders formatting. Then check real-browser source geometry in Task 4.
- [ ] Format, inspect and commit only the listed task files, including new files.

## Task 3: Anchor Meanwhile strips to launch events

**Files:** Modify `src/comic/delegates.ts`, `comicRows.ts`, `delegates.test.ts`, `remoteDelegateIdentity.test.ts`, `comicRows.test.ts`; extend `ComicTranscript.test.tsx` and `useComicSubchats.test.tsx` where needed for relocation preservation.

**Interfaces:** Keep `projectComicDelegates(model, entities, ownerRef?)` and `ComicDelegateStrip.source` unchanged. A source denotes verified creation evidence only. Membership still comes from canonical activity; `composeComicFlow` retains existing strip IDs and child paths.

- [ ] Add failing cases with a launch between parent entries, a later completion notification, completion-only loaded history, multiple launches in one turn and remote source-local refs. Assert source item identity and flow order rather than headings:

```ts
expect(strip.source?.itemId).toBe(launchItem.id);
expect(completionOnlyStrip.source).toBeUndefined();
expect(flowIds).toEqual([historyId, beforeId, stripId, afterId]);
expect(relocatedStrip.id).toBe(previousStrip.id);
```

- [ ] Run `npx vitest run src/comic/delegates.test.ts src/comic/remoteDelegateIdentity.test.ts src/comic/comicRows.test.ts`; observe the chronology failures.
- [ ] Remove completion notifications as launch evidence. Check the actual recorded delegate-result forms used by producers, including bounded/retained output references; use existing typed helpers when they prove creation. Do not invent identity or chronology when evidence is unavailable. Preserve authoritative remote qualification and entity-generation precedence.
- [ ] Move the unlocated group to the older-history boundary, before chronological message rows. Preserve strip keys, expansion paths and child lifetimes when a launch is later loaded. Keep itemless errors separate. Do not add an eager history scan or change shared paging ownership.
- [ ] Update existing completion-placement tests to require an unlocated source while retaining their remote identity assertions. Add a real-store relocation test proving the same expanded child reader survives late launch discovery, collapse still releases only its own branch, and pending reading intent is retained.
- [ ] Rerun the named tests plus `npx vitest run src/comic/ComicTranscript.test.tsx src/comic/useComicSubchats.test.tsx` and `npm run typecheck`. Format, inspect and commit the exact task files.

## Task 4: Prove the whole reading journey and deliver

**Files:** Extend `scripts/comicguard/run.mjs`, `scripts/comicguard/README.md`, root-relative `cmd/evener-hub/comic_browser_test.go` and `docs/product/comic-chat.md`. Update only harness assumptions directly changed by rendered Markdown and relocated strips.

**Interfaces:** Reuse the existing real authenticated hub/daemon fixture and scripted external provider. Extend fixture data with independent Markdown/code/table inputs and real launch/continuation messages. No mocked stores, invented child refs or product debug API.

- [ ] Add a producer-backed journey covering formatted prose → code → table → prose, streamed block completion, a real child launch between parent messages and a later completion. Assert bounded geometry, semantic elements, source-order identity, no character stage in inserts, full code whitespace, safe links and readable horizontal overflow at 1360, 760, 390 and 320 CSS pixels.
- [ ] Extend source-point observations to formatted DOM without calling the production mapping helper as the oracle. Pin original offsets from fixture source, independently locate the corresponding DOM glyph and measure its rectangle. Check an away position inside formatted prose and inside a technical block through resize, font readiness/failure, Standard/Comic switching, multiple history pages, reload and reconnect. Preserve the existing no-input recovery and no-read-causes-turn/start checks.
- [ ] Run the changed journey against the pre-feature state where practical to show each new behavior assertion fires. Then run `make test-web` from the repository root, after touched-file Biome autofix from the frontend. A successful targeted Vitest run does not establish typecheck or full frontend-gate success.
- [ ] Inspect the build and browser scripts, then run `make build-web` and `COMICGUARD_EVIDENCE_DIR="$EVENER_SCRATCH_DIR/comic-refinements-bundled" go test -tags browserguard ./cmd/evener-hub -run '^TestComicChatBrowser$' -count=1 -v`. Repeat with `EVENER_COMIC_BLOCK_FONTS=1` and `comic-refinements-fallback`. Require completed runs, actual browser assertions and clean owned-helper shutdown. Do not disturb the main hub or existing preview.
- [ ] Update the product guide's literal-Markdown and unlocated-strip descriptions to verified behavior. State bounded sizes, insert rendering and source-position preservation. Do not describe proposals as shipped behavior.
- [ ] Run independent implementation PAR on the immutable full diff and evidence, resolve legitimate findings red-first, then invoke `simplify-code:simplify-code` and rerun affected checks. Stage exact files and inspect the staged diff before commits.
- [ ] Invoke `primeradiant-ops:shepherd-pr`. Push one completed round, wait for exact-head CI and read combined plus own per-commit RoboRev findings. Follow the repository's normal merge queue, which overrides the feature skill's general admin-merge instruction. Verify GitHub MERGED and intended content before cleanup. No deployment or hub restart.
- [ ] Inspect owned scratch and finished reviewer scratch; remove only disposable owned files. Preserve report-linked browser evidence and shared dependency installs. Report the merged PR, changed behavior, checks, retained evidence and anything unverified.

## Execution

The plan-scoped SDD ledger records execution progress; the session task list tracks only phase status. Task 1 establishes the bounded reader first. Task 2 is one coherent rendering/source-map unit. Task 3 is a separately reviewable chronology change. Task 4 verifies their integration. Jesse confirmed subagent-driven execution; keep writers isolated when their paths overlap.
