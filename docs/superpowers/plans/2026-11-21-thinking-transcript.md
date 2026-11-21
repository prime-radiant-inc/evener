# Thinking Transcript Rows Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Hide reasoning at Chat and make compact thinking rows consistently disclosable and visually aligned with intent rows at Intent and above.

**Architecture:** The shared TypeScript projector owns web visibility; native's Intent-projected Chat filter owns its additional conversation-only filtering. The existing web `ThinkBlock`, `Disclosure`, `Loader`, `ToolIcon`, and design tokens own compact presentation, with no new preference or widget API.

**Tech Stack:** TypeScript, React, React Native, Vitest, CSS Modules, Biome, browser layout guards.

**Spec:** `docs/superpowers/specs/2026-11-21-thinking-transcript-design.md`

## Global Constraints

- Chat hides live, settled, and redacted reasoning; Intent and above retain disclosable thinking.
- Genuine non-reasoning failure presentation remains visible at Chat.
- Full's richer reasoning presentation and named Custom content behavior remain unchanged.
- Use existing type and spacing tokens; do not add a widget, widget prop, or persisted preference.
- Keep the coarse-pointer tap target intact while auditing padding.
- Tests assert behavior and structure, not human-readable copy.
- Keep production changes below the default couple-hundred-line bugfix budget; stop and report if the minimal fix exceeds it.

## Review Focus

- A terminal turn containing only reasoning must not resurrect that reasoning through the fallback at Chat; Task 1 adds this case.
- Native Chat projects through Intent, so shared Chat filtering alone cannot implement the rule; Task 1 tests that exact path.
- A reader's disclosure choice must survive streaming and settlement; Task 2 retains and runs the existing identity and rerender tests.
- Padding changes must not shrink phone/coarse-pointer targets or push the chevron off-screen; Task 2 adds geometry checks and browser inspection.
- Full and Custom must not inherit Chat's preset-only carve-out; Task 1 tests both.

---

### Task 1: Hide reasoning at Chat on shared and native paths

**Files:**
- Modify: `appwire-client/typescript/transcriptDisplayConfig.ts`
- Modify: `appwire-client/typescript/transcriptProjector.ts`
- Test: `appwire-client/typescript/transcriptProjector.test.ts`
- Modify: `mobile-native/src/transcriptPresentation.ts`
- Test: `mobile-native/src/transcriptPresentation.test.ts`
- Test: `mobile-native/src/conversation/state/conversation.displayAndRebind.test.ts` only if its existing Chat journey pins the superseded behavior

**Interfaces:**
- Consumes: `hidesThinkingTraces(content: ContentSelection): boolean`, `decisionFor`, `terminalFallbackEntry`, and `projectNativeTranscript(conversation, config, {justTheConversation})`.
- Produces: Chat projections with no reasoning entries; Intent/Tools/Activity compact `thinking` entries and Full ordinary reasoning `item` entries remain unchanged.

- [ ] **Step 1: Establish the native baseline**

Reuse a lockfile-identical native install for focused Vitest only. Run `sh ../scripts/native/run-tests.sh src/transcriptPresentation.test.ts src/conversation/state/conversation.displayAndRebind.test.ts` from `mobile-native` and record a clean baseline. The frontend/shared baseline already passed: 3 files, 226 tests.

- [ ] **Step 2: Add failing shared-projector cases**

Extend the existing reasoning projection tests with a running Chat thought, a failed Chat thought, and a terminal turn whose only item is reasoning. Assert Chat emits no reasoning entry and the terminal fallback does not reintroduce one. Retain assertions that Intent and more detailed reasoning-off presets emit `kind: "thinking"`, Full emits `kind: "item"`, and Custom follows its vector rather than the Chat carve-out.

- [ ] **Step 3: Add the failing native Chat case**

Extend the existing `justTheConversation` coverage with running and settled `family: "reasoning"` activities and a thought-specific failure row. Assert Chat removes all three while retaining an ordinary failure and the conversation; assert the same thinking rows survive without `justTheConversation`.

- [ ] **Step 4: Run the new tests and confirm the old behavior fails them**

Run the shared projector test through frontend `npm test -- ../../../appwire-client/typescript/transcriptProjector.test.ts` and the focused native command above. Expected: the new Chat live/failure/fallback and native running/failure cases fail for the stated visibility reason.

- [ ] **Step 5: Implement visibility at the source**

In `decisionFor`, hide reasoning at Chat before live or critical classification when `vector.reasoning` is false. In `terminalFallbackEntry`, suppress a reasoning fallback that Chat hid. Update `hidesThinkingTraces` comments to describe all reasoning, not only settled traces.

In native `conversationOnly`, replace the settled-only thought predicate with one that covers every reasoning-family activity and thought-specific failure. Keep unrelated failures and steps governed by their existing rules.

- [ ] **Step 6: Run focused tests and commit**

Run both commands from Step 4 plus the existing `ThinkBlock.test.tsx` and `TurnBlock.test.tsx`; expected PASS with the renderer-level Chat placeholder still covered until Task 2 removes that unreachable path. Stage only Task 1's named files and commit `fix(transcript): keep thinking out of chat`.

### Task 2: Align compact thinking type, icon seating, and padding

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/ThinkBlock.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/thinkblock.module.css`
- Test: `cmd/evener-hub/frontend/src/panes/session/transcript/messages/ThinkBlock.test.tsx`
- Test: `cmd/evener-hub/frontend/src/panes/session/transcript/TurnBlock.test.tsx`
- Modify if its copied geometry is affected: `cmd/evener-hub/frontend/scripts/layoutguard/cases/transcript-rail-icon-seats/harness.html` and its assertion file

**Interfaces:**
- Consumes: existing `Disclosure`, `Loader`, `ToolIcon`, `ThoughtBody`, `LiveThoughtBody`, disclosure keys, and CSS tokens.
- Produces: one compact live/settled summary grammar with UI-size sans labels, railed leading glyphs, a visible chevron, and deliberate token-based vertical spacing. Full rendering keeps its existing components and styles.

- [ ] **Step 1: Add failing structural and style cases**

Extend existing `ThinkBlock` tests so a settled compact row has the decorative thought glyph in the rail grammar and its label uses the same UI type treatment as the live compact Loader. Add a padding assertion for the chosen combined block/summary inset, not a screenshot or copy oracle. Add a TurnBlock-level Chat absence check if Task 1 did not already place it there.

- [ ] **Step 2: Run the focused web tests and verify RED**

From `cmd/evener-hub/frontend`, run `npm test -- src/panes/session/transcript/messages/ThinkBlock.test.tsx src/panes/session/transcript/TurnBlock.test.tsx`. Expected: the new compact settled icon/type/padding cases fail; existing live and settled click tests remain green.

- [ ] **Step 3: Implement the smallest presentation change**

Remove the unreachable plain Chat `ContentFreeThinkBlock` path. Keep the live compact Loader disclosure. Render the settled compact summary with the existing thought icon and shared disclosure, using local CSS classes and existing tokens for UI type, rail seating, ellipsis, and chevron clearance.

Audit `.block`, quiet Disclosure summary padding, body margin, and Markdown spacing together. Remove only redundant compact-row inset; do not change Full or the shared Disclosure widget. Preserve the coarse-pointer minimum target.

- [ ] **Step 4: Verify interactions, geometry, and actual appearance**

Rerun Step 2; expected PASS, including opening and closing live and settled compact disclosures and preserving explicit reader choices. Run the rail layout guard when its harness changed. Use the real dev surface or transcript fixture to inspect both themes at desktop and phone widths, collapsed and expanded; record computed font size/line height, summary padding, icon/text/chevron alignment, and tap-target geometry.

- [ ] **Step 5: Format, typecheck, and commit**

Run Biome on each touched frontend file, `npm run typecheck`, and the focused tests again. Stage only Task 2's named files and commit `fix(web): align compact thinking rows with intents`.

### Task 3: Update durable contracts and verify the branch

**Files:**
- Modify: `docs/web-ui/README.md`
- Modify: `docs/web-ui/design-system.md` where the continuous-motion rule currently describes the content-free placeholder
- Modify: `docs/product/subsystems.md` S02/S03 transcript responsibilities where this client boundary belongs
- Delete before completion: `docs/superpowers/plans/2026-11-21-thinking-transcript.md`
- Retain: `docs/superpowers/specs/2026-11-21-thinking-transcript-design.md`

**Interfaces:**
- Consumes: the verified Task 1 and Task 2 behavior.
- Produces: evergreen documentation stating that Chat omits reasoning and Intent+ presents compact thinking as aligned disclosures.

- [ ] **Step 1: Update documentation without changing protocol history rules**

Replace the old “every level” wording with Intent-and-up behavior, retain the Reasoning and Thinking-open switch distinction, and scope the pulsing-grid rule to levels where compact thinking exists. Update S02/S03 ownership text without duplicating implementation detail. Delete this implementation plan because the product guide requires completed plans to leave the tree.

- [ ] **Step 2: Run the focused and canonical checks**

Run shared/web/native focused tests, frontend typecheck and Biome, relevant layout guards, and `make test-web` once. Run `make lint` once before pushing. Do not run heavy gates in parallel. Report any unrun native or browser gate explicitly rather than implying coverage.

- [ ] **Step 3: Review the complete diff**

Run `/simplify-code` on the finished change, apply only in-scope quality improvements, and run `par` once before the PR. Confirm the diff stays within budget, contains no unrelated main-checkout changes, and retains strict behavior coverage.

- [ ] **Step 4: Commit and open the PR**

Stage only the remaining named documentation and cleanup files and commit `docs(transcript): record thinking visibility and presentation`. Push `thinking-transcript`, open a PR with the root cause, behavior changes, measured type/padding evidence, test commands, and any unrun checks, then shepherd the existing review and merge-queue workflow.
