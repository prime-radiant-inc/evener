# Kata Transcript Turn Design Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete katas `kgp2` and `07ry` on `wip/kata-transcript-turn-design` by making thinking disclosures honest and useful, and by pinning the approved You/Agent conversation grammar without disturbing transcript transport, virtualization, or tool clustering.

**Architecture:** Keep the existing React transcript registry, `TurnBlock`, `VirtualList`, `Markdown`, `StreamingText`, and session-keyed disclosure store. Add pure reasoning-format helpers for timestamp selection, duration formatting, and the final nonblank context line. Keep live reasoning on the append-only `StreamingText` path; only settled reasoning gets the native one-level `<details>` disclosure. The current UserMessage/AgentMessage/TurnBlock geometry is already the React implementation of the approved conversation grammar, so 07ry work will pin its contracts and correct only a proven drift rather than redesigning it.

**Tech Stack:** React 19, TypeScript, CSS Modules, Vitest + Testing Library, Vite, headless Chrome layoutguard/overflowguard.

## Global Constraints

- Read and follow `AGENTS.md` and `docs/testing.md`; default tests remain deterministic and provider-free.
- Use only existing design tokens/components. No new dependency, visual language, card treatment, border system, or disclosure widget.
- Preserve `TurnBlock` item order, `VirtualList` row boundaries, `ToolCallCluster`, tool renderer behavior, and session-scoped disclosure keys.
- Do not touch the rail, Steering, `NotificationCard`, or unrelated transcript tool renderers.
- Do not alter transport, reducer routing, lazy loading, or live model behavior. Existing reducer observation timestamps are the only live fallback; do not read `Date.now()` in rendering.
- Test behavior and layout contracts, not snapshots or large generated markup. Strip CSS comments before stylesheet-source assertions.
- Commit logical slices and run the focused gate before each implementation commit.

## Approved References and Current Audit

The exact approved references are:

- `docs/web-ui/design-system.md`, §1–§3: current React tokens, IBM Plex Sans for UI/prose, IBM Plex Mono for machine text, 12/13/14/16/20px type scale, 4px spacing, no prose cards, hairline edges, and honest-liveness rules.
- `docs/web-ui/history/examples/01-golden-live-session.html`, `.feed`/`.turn`/`.you`/`.agent`/`.think` rules at lines 101–122 and the representative transcript at lines 257–287: quiet left `You` identity, full-width unlabelled agent prose, 16px agent hero, one quiet thinking line with duration/context, and no prose container.
- `docs/web-ui/history/examples/02-hard-cases.html`, `.feed`/`.turn`/`.you`/`.agent`/`.think` rules at lines 95–120 and its transcript cases: the same turn rhythm, no nested prose cards, wrapping human text, and horizontal scrolling reserved for machine payloads.
- `docs/web-ui/history/mockups/03-user-message-steering.html`, Alt A at lines 44–55: `You` is a fixed-width, baseline-aligned left column beside the prompt; no right bubble.
- `docs/web-ui/history/mockups/04-assistant-hero.html`, shared fix at lines 57–69 and Alt A at lines 93–101: agent prose wins by size/space/contrast and inline code is a quiet underline, never a chip.
- `docs/web-ui/history/mockups/05-thinking-block.html`, shared collapsed vocabulary at lines 69–77 and reserved live slot at lines 87–105: one quiet disclosure line, duration plus useful gist/context, one disclosure level, and stable streaming geometry.
- `docs/web-ui/decisions.md`, transcript grammar topics 03–05: current React geometry is a flex gutter; visible `Agent` is not part of the approved screen; agent prose is the hero; thinking state is session-scoped and streamed through the existing append-only path.
- `docs/superpowers/plans/2026-07-20-webui-rewrite-wave4-transcript.md`, T1/T2: `VirtualList` over turns, registered renderers, live `StreamingText`, settled Markdown, and one collapsible reasoning block.

Current 07ry audit, verified against the real harness at 390px and 1400px:

| Surface | Current implementation | Approved contract | Action |
| --- | --- | --- | --- |
| `UserMessageItem` | Flex row; fixed `You` gutter; shrinking body; `ImageGallery` remains in the body; no bubble/right alignment | Mockup 03 Alt A and golden `.you` row | Pin DOM/CSS contract; preserve the current React decision that the tag uses `--ink-hi` for separation while text uses `--ink-mid`. |
| `AgentMessageItem` | Same wrapper for live/final; live `StreamingText`; final `Markdown`; visible label is absent and an `srOnly` Agent name remains | Mockup 04/golden assistant hero | Pin live/final, accessibility, 16px prose hook, and inline-code ownership; preserve behavior. |
| `TurnBlock` | One virtualized turn root; wire-order registry dispatch; tool clusters are collapsed into `ToolCallCluster`; failure/separator endcaps remain outside item mapping | Wave4 T1/T3 and golden turn grouping | Pin the hierarchy and clustering boundary; do not move or duplicate tool renderers. |
| `ThinkBlock` | Live open streaming path; settled native details; session-keyed state; current summary is only `Thought`/duration | Mockup 05 and kgp2 | Implement actual duration and final nonblank context; retain one details level and store scope. |
| Desktop/390 layout | At 1400px the current React-specific `76rem` measure centers the transcript; at 390px it fills the pane without horizontal scrolling | Current React decision `c26abd329` plus current design tokens | Preserve `76rem`; do not substitute the older legacy `720px` mockup width. |

### Historical conflict resolutions

1. The older golden and legacy layout specs use a 720px prose column, while Jesse's later React-specific commit `c26abd329` explicitly caps the React transcript at `76rem`, aligns it with the composer, and verifies 1900px/800px behavior. The current React design-system document does not define a competing measure. The later, implementation-specific decision controls this work; the 720px value remains a visual reference for hierarchy and line density, not a reason to undo the React width decision.
2. Commit `44d04271f` removed the thinking preview to avoid duplicated opening text and literal Markdown in a plain `<summary>`. The newer written `kgp2` record explicitly requires at least the last meaningful nonblank thought line/context. That requirement supersedes the narrow no-preview outcome: the new preview will be a bounded, plain-text final nonblank line, with an ellipsis only when the helper actually truncates it; the expanded Markdown body remains complete.
3. The old `docs/web-ui/decisions.md` table says the agent size/space and inline-code choices were absent, but later React commits `cd4663a99`/`bf1c7f318` shipped them. The implementation and tests follow the shipped current React components and update the stale decision entry after behavior is verified. The visible-label choice remains the written golden decision: `Agent` stays screen-reader-only.

## Implementation Tasks

### Task 1: Replace whole-second reasoning timing with honest duration/context helpers

**Files:**

- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/reasoningFormat.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/ThinkBlock.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/thinkblock.module.css` only if the bounded context line needs an explicit flex/min-width contract

**Contract:**

- Prefer the complete `startedAt`/`completedAt` pair; otherwise use the complete reducer-observed pair.
- Parse only finite ISO timestamps and reject incomplete, invalid, or backwards pairs. Never measure an in-progress item or use a client clock.
- Format actual durations as whole milliseconds below one second, one decimal second below ten seconds, and whole seconds at ten seconds or more. Preserve `0ms` when two authoritative timestamps are exactly equal rather than changing it to a fabricated positive duration.
- Derive the collapsed context from the last nonblank line across the joined reasoning paragraphs. Trim it and append `…` only when the bounded preview is actually clipped. Empty reasoning remains absent.
- Keep `Thought for <duration>` first in the accessible native summary. When timing is absent, use `Thought` with context if context exists; when live/in progress, render `Thinking…` and stream the body without a duration summary.
- Keep the expanded body as the complete joined Markdown document and keep exactly one native `<details>` level.

### Task 2: Add red behavior tests before production changes (kgp2)

**Files:**

- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/reasoningFormat.test.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/ThinkBlock.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/format.test.ts` only if a shared formatter contract is intentionally reused

**Tests to add or replace:**

- Completed replay item: authoritative wire timestamps render `Thought for 4s`.
- Completed sub-second item: authoritative timestamps render e.g. `Thought for 250ms`, not `1s` or `0s`.
- Missing/incomplete/invalid timestamps: no numeric duration.
- In-progress/live item, including streaming updates and a partial timestamp pair: no `Thought for` summary and no fabricated elapsed time.
- Live observed frame pair after reducer accumulation: duration is taken from the observed pair only when the wire pair is absent.
- When both pairs exist, wire item timing wins; this pins replay/live parity and authority order.
- Last nonblank line is used across multiple summary indexes, blank tails are ignored, long lines get honest ellipsis, and the expanded body still contains all source content.
- Markdown punctuation may not become a second rendered body; the preview is bounded plain text and the body remains the sole full Markdown rendering.
- Disclosure state remains session-scoped and survives remount; streaming-to-settled removes `StreamingText` and leaves one disclosure.

Run these tests now and record the expected red failures before implementing Task 1.

### Task 3: Add red component/layout contracts for 07ry without brittle snapshots

**Files:**

- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/UserMessageItem.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/AgentMessageItem.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/TurnBlock.test.tsx`
- Add or modify: the smallest existing transcript stylesheet-contract test seam, using comment-stripped CSS

**Contracts:**

- `You` and prompt are sibling nodes in a baseline flex row; the tag is a fixed nonshrinking gutter, body is the shrinkable content column, and attachments/text stay in that column.
- Agent live and final states use one wrapper, retain hidden semantic `Agent`, use the shared prose-size hook, and route final content through Markdown while live content uses StreamingText.
- The turn root remains the VirtualList unit and renderer order; clustered tool items produce one cluster and are not changed by message styling.
- CSS asserts token-based spacing/typography, no visible Agent label, no message background/border/radius card, no nested disclosure, `.details[open]` rather than unscoped flex on closed details, and 100%/max-width/min-width behavior that can shrink at 390px.
- Do not add a new layoutguard fixture unless a component contract cannot be proven by the existing real overflow harness; the existing `overflowguard` already exercises the real Session pane at 390/700/1024/1400.

Run the focused component suite and confirm the new changed contracts are red before production changes. If an already-shipped bf1 contract is green, keep it as a regression contract and do not manufacture a source change merely to create a diff.

### Task 4: Implement the smallest production changes

- Implement Task 1's pure helpers and ThinkBlock summary/context rendering.
- Preserve all current UserMessageItem, AgentMessageItem, TurnBlock, tool cluster, attachment, code, streaming, final, and disclosure-store paths unless a failing contract identifies a concrete regression.
- Update stale transcript decision prose in `docs/web-ui/decisions.md` after tests pass: mark agent size/space and inline-code underline live, and record kgp2 as the newer resolution of the removed-preview conflict.

### Task 5: Verify in proportion to the UI risk

From `cmd/serf-hub/frontend`:

1. Focused transcript tests, including `reasoningFormat`, `ThinkBlock`, `UserMessageItem`, `AgentMessageItem`, and `TurnBlock`.
2. Full frontend Vitest suite.
3. `npm run typecheck`.
4. `npm run lint`.
5. `npm run build`.
6. `npm run layoutguard`.
7. `npm run overflowguard` at the required 390/700/1024/1400 widths.
8. Use the working Vite + headless Chrome harness for a real 390px and desktop visual check; inspect screenshots for the left gutter, agent hierarchy, collapsed/expanded thought, bounded context, attachment/code behavior, and absence of horizontal scroll. This visual check is evidence, not a reason to block otherwise deterministic gates.

Before the final handoff, run `git diff --check`, `git status --short --branch`, and verify the worktree is clean after committing. Report `HEAD`, commit subjects, exact test/guard results, and any visual-harness limitation. Do not close or merge either kata.

## Commit Slices

- [ ] Commit the plan before implementation: `docs: plan transcript turn design katas`.
- [ ] Commit red kgp2 tests and the pure timing/context implementation: `webui: show honest thought duration and context`.
- [ ] Commit 07ry regression contracts and the authoritative decision-record update: `webui: lock approved conversation hierarchy` (or report no production 07ry change if the current React geometry remains green).
- [ ] Commit only after all focused/full tests and browser guards pass; leave no untracked generated artifacts.
