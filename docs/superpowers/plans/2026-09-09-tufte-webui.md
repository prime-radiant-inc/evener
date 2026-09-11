# Editorial Instrument Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Apply the approved Tufte-inspired editorial system across Evener's web UI, verify it through actual use, and open an unmerged PR.

**Architecture:** Retain React, CSS Modules, shared widgets, protocol models and workspace state. Land shared tokens/type/primitives first; then revise transcript evidence/delegates and the surrounding application in independent lanes. Integrate before comprehensive browser verification and an independent usability panel.

**Tech Stack:** React 19, TypeScript, Vite, Vitest, Biome, existing Chrome geometry guards; Go hub for an isolated live preview where supported.

**Spec:** `docs/superpowers/specs/2026-09-09-tufte-webui-design.md`

## Global constraints

- All colors and type sizes belong in tokens.
- Preserve the existing mobile breakpoint at 899px.
- Preserve `system`, `light`, and `dark` preferences and the existing default behavior.
- Introduce a shared `--font-prose` and a body-scaled prose size. Target 18px reading text at the default scale.
- Editable phone controls remain at least 16px at the normal scale.
- Keep widget public APIs, stores, RPC schema, routing and persistence compatible.
- Preserve semantic hue meanings, honest liveness, reduced motion, contrast floors, and disclosure/scroll contracts.
- Read `docs/developing-evener/testing.md` before changing tests.
- Biome applies to touched `src/` files; browser harness HTML outside `src/` is not part of its enforced scope.
- Never stage unrelated files or use blanket staging commands. Do not merge or alter the user's live service.
- User authorized autonomous decisions, a hands-on subagent panel with fixes/retests, feature-branch push and PR creation.

## Task 1: Shared editorial foundations and canonical guide

**Files:**
- Modify `cmd/evener-hub/frontend/src/styles/tokens.css`, `global.css`, `faces.test.ts`, `measure.test.ts`, `token-contract.test.ts` only where approved type-contract additions require it.
- Modify `cmd/evener-hub/frontend/package.json` and `package-lock.json` for the self-hosted reading face and explicitly tested Vitest security update if applicable.
- Modify existing CSS modules under `src/widgets/` for shared enclosure, controls, typography and tables; no public API changes.
- Modify `src/dev/TypeSpecimen.tsx` and its stylesheet to demonstrate the third face.
- Modify `docs/web-ui/design-system.md`, `decisions.md`, and `README.md` to document the superseding system and retain interaction law.
- Update PWA theme color consumers only if actual token changes require it, using the existing `pwa-manifest-colors.test.ts` as the independent consistency gate.

**Interfaces:** consumes current token names and widget APIs; produces `--font-prose` and `--font-size-prose` while retaining all existing tokens. Transcript and surface tasks must use these shared properties, not local fonts or pixel sizes.

- [ ] Read current tokens, global fonts, token/measure tests and component styles. Record existing geometry contracts before altering visual values.
- [ ] Add a focused typography contract to `faces.test.ts` using its existing CSS reader pattern. The key independent requirements are a distinct serif prose face, the existing mono face for code, and body-scaled prose size. Extend the type specimen test to expose the new face. Run the focused tests and confirm the new requirement fails before implementation.
- [ ] Install a licensed self-hosted Source Serif 4 font package. Use its real package CSS/font entry and inspect the license; do not assume a filename. Retain Inter and JetBrains Mono. Define the shared properties in tokens, with the size on body alongside the current scaled ramp:

```css
--font-prose: "Source Serif 4 Variable", Georgia, "Times New Roman", serif;
/* In the body-scaled ramp, not the root-only palette block: */
--font-size-prose: calc(18px * var(--font-scale));
```

- [ ] Re-tune neutral palettes toward paper/ink using the study as direction, not unverified hex values. Use the existing contrast computations to verify every neutral/semantic/diff pairing. Keep named semantic colors and ANSI roles. Set structural card shadow to none and reduce radii without removing overlay boundaries or field affordances.
- [ ] Apply the prose face to prose Markdown while explicitly preserving code/pre font rules. Set editorial page headings without turning every button, table cell or machine result into book text. Flatten shared Card/InspectorCard/PaneScaffold where structurally appropriate; retain distinctions between field, overlay, and page. Use fine table rules and sentence-case column labels.
- [ ] Update the canonical style guide before wider surface edits. Clearly mark new values as implemented only after they land; reconcile conflicting old prose rather than placing a new mandate above contradictions. Preserve widget API inventory and interaction sections.
- [ ] Run touched-file Biome, then:

```bash
cd cmd/evener-hub/frontend
npx vitest run src/styles src/widgets src/dev/TypeSpecimen.test.tsx --maxWorkers=4
cd ../../..
make test-web
```

The type specimen test exists at the named path. Run the existing style and widget suites alongside it; never accept “no tests found” as a pass.
- [ ] Inspect real `/dev/widgets`, `/dev/type`, and `/dev/surfaces` in both themes. Commit named changed paths with intent `feat(web): establish editorial typography and flat visual foundations`.

## Task 2: Conversation, tool evidence, and durable collaborators

**Files:**
- `src/panes/session/transcript/ToolCallItem.tsx`, `ToolRow.tsx`, `toolcallitem.module.css`, `toolrungroup.module.css`, `turnblock.module.css` and relevant tests.
- `src/panes/session/transcript/messages/*.module.css`, plus component files only if hierarchy needs a semantic label.
- `src/panes/session/transcript/tools/subagentModule.tsx`, `subagentModuleStore.ts`, `subagentmodule.module.css`, their tests and any focused lifecycle helper extracted beside them.
- Other tool-renderer CSS in `src/panes/session/transcript/tools/`; preserve native renderer bodies and registry semantics.
- Relevant `src/dev/surface-sections/transcript*` fixtures and browser guard cases specific to transcript geometry.

**Interfaces:** consumes Task 1's prose tokens and existing ItemModel/ThreadModel/EvenerDelegateInfo. Produces no protocol changes; existing ToolRow/descriptor props and transcript anchors remain compatible.

- [ ] Read current two-line ToolRow grammar, source fixture tests, all delegate state tests, and `toolRuns.ts`. Add missing behavior tests before a correction. For missing/unrecognized lifecycle input, the direct classifier expectation is:

```ts
expect(classifyJobStatus(undefined)).toBe("unknown");
expect(classifyJobStatus("unrecognized-status")).toBe("unknown");
```

Also preserve an independent component test proving a genuinely in-flight delegate launch still renders running, and an owner-projection test proving a completed launch can have a running child. Do not change the fallback until these isolate the boundary.
- [ ] Add an attention test that checks the indicator in both collapsed and expanded renderings using the real stable projection fixture. Test unknown state is not labeled as a human question. Preserve stopped, exhausted, resumed and stable-ID migration tests.
- [ ] Apply serif reading type to user/agent message bodies and child-authored quotes; keep intent/operation rows compact sans. Preserve speaker/content alignment and the existing timestamp rail at wide/narrow measures. Remove ornamental enclosing fills around tool evidence while preserving readable raw output and line numbering.
- [ ] Present delegate lifecycle visibly, not only to screen readers. Keep the mandate in the existing intent header; do not repeat it inside the body. Put current child words/report or failure reason before secondary stats, with stable transcript opening independent of expansion. Label missing data truthfully; do not derive child runtime from the launch call's duration. Preserve real recent activity and immutable transcript evidence.
- [ ] Keep folding logic unchanged unless a new test isolates a violation of the approved contract. Style successful runs quietly; failure and attention must remain apparent. Keep file/transcript OpenButton controls outside disclosure buttons and reachable at phone widths.
- [ ] Run focused tests and touched-file Biome, then `make test-web`. Add or extend actual-component browser cases for tool/delegate geometry without weakening existing bounds.
- [ ] Commit named paths with intent `feat(web): present tool evidence and delegate contributions inline`.

## Task 3: Shell, forms, panels, and mobile cohesion

**Files:**
- Existing CSS modules in `src/shell/`, including `AppShell.module.css`, `DockHost.module.css`, `PaneTab.module.css`, `dockview-theme.css`, `rail/`, `mobile/`, `palette/`, and overlay-related subdirectories.
- Existing CSS modules in `src/panes/welcome/`, `spawn/`, `settings/`, `doc/`, `transcript/`, and `sessionPanels/`.
- Existing CSS modules in `src/panes/session/chrome/`, `composer/`, `queue/`, and `askDock/`.
- Relevant component/behavior tests and real surface-gallery fixtures; no stores, routing, auth, protocol or persistence refactoring.

**Interfaces:** consumes shared widget/token changes from Task 1. Preserves component public props, preferences, app routing, and shell workspace state. Does not modify Task 2's transcript-owned paths.

- [ ] Inventory every listed surface, distinguishing direct changes from inherited widget/token changes. Make a checked coverage matrix in the implementation report.
- [ ] Flatten structural shell surfaces and tabs; keep clear selected/focused state and readable navigation. Respect existing mobile viewport ownership and keyboard inset handling.
- [ ] Restyle welcome/spawn/settings as editorial headings and aligned fields, not stacks of cards. Keep controls sans, shared directory confirmation, provider sheet editing, and inline validation. Do not replace real controls with text merely to resemble the study.
- [ ] Restyle composer, queued turns, AskDock and attachments as a coherent input area with clear action/attention. Keep safe-area spacing, iOS font threshold and responsive layout constraints.
- [ ] Simplify Details/Activity/Tasks hierarchy and accounting alignment. Do not introduce charts or data not present in the live projection. Keep unknown/error/empty/loading states distinct.
- [ ] Reuse current focused interaction tests. If a structural component change is needed, first add the relevant assertion for maintained focus/label/disclosure behavior, run red, then implement. For CSS-only changes, existing browser guard assertions are the geometry reference.
- [ ] Run touched-file Biome, `make test-web`, then browser guards relevant to shell/spawn/overflow. Commit named paths with intent `feat(web): carry the editorial system across the workspace`.

## Task 4: Integrate, exercise the actual app, and close coverage gaps

**Files:** existing galleries/harnesses under `src/dev/`; browser guards under `scripts/`; documentation coverage notes. Production files only for findings isolated by tests or browser evidence.

**Interfaces:** consumes the integrated branches and real components. Produces a working `m5` preview, explicit surface coverage evidence, and gate outputs.

- [ ] Recheck target branch/ref immediately before every local merge; preserve unrelated files, use explicit no-fast-forward merges and retire integrated lanes.
- [ ] Discover the existing dev harness and isolated hub launch instructions. Use real production components with deterministic external-boundary data where live provider work is not needed. Do not point testing at the user's production hub or use credentials without explicit test opt-in.
- [ ] Start a detached preview bound to `0.0.0.0`, confirm listener and exact `http://m5:<port>` URL. Use a fixture harness for controlled hard states and a real app route for navigation/preferences; clearly distinguish them to reviewers. Keep the approved design companion until an actual UI preview replaces it.
- [ ] Exercise both themes at 360px, 390px, narrow desktop panes and wide desktop; inspect larger text. Verify no document overflow, clipped controls, lost focus, broken composer geometry or drifting timestamp rail. Test native long evidence, failure rows, resumed/missing delegate states and independent child navigation.
- [ ] Run:

```bash
make test-web
make test-web-browser
cd cmd/evener-hub/frontend && npm run build
```

Run broader lint/vet/test gates required by the final diff and PR policy. A timeout, denied launch or missing Chrome leaves a gate incomplete; report it and rerun in the capable parent environment.
- [ ] Request independent specification and correctness review. Fix actionable findings with focused regression tests and rerun decisive gates.

## Task 5: Hands-on subagent panel and revision loop

**Files:** reviewer evidence artifacts only; targeted fixes stay in the relevant owning source paths and receive tests. No fabricated endorsements.

**Interfaces:** each reviewer gets the actual preview URLs, task script, fixture/live distinction, allowed actions and a clear acceptance rubric. Browser reviewers run sequentially if they share browser state; use independent tabs and restore preference changes.

- [ ] Assign a phone-workflow reviewer: navigate sessions, read/expand tools, use composer/AskDock, change font size/theme and inspect keyboard/touch behavior.
- [ ] Assign a tool/delegate reviewer: identify active versus completed tools, inspect failures and full evidence, determine whether a launch or child has finished, open and return from a child, inspect attention and unknown/resumed states.
- [ ] Assign a workspace/accessibility reviewer: use settings/provider forms without submitting secrets, open menus/dialogs by keyboard, inspect focus restoration, select panes and assess information hierarchy in both themes.
- [ ] Require each reviewer to report completed tasks, concrete usability findings with reproductions, blocked tasks, and an explicit endorsement or rejection. A code-only review does not count.
- [ ] Fix findings, then have the affected reviewers repeat the exact task on the revised build. Continue until reviewers endorse and no actionable blocking findings remain. After two incomplete cycles on the same finding, isolate/reslice the issue rather than repeating an ineffective fix.
- [ ] Re-run all final gates after changes and record panel outcomes in the PR evidence.

## Task 6: Commit, push, and open an unmerged PR

- [ ] Load verification-before-completion and finishing-branch skills; inspect fresh repository status, branch, diff and gate results.
- [ ] Confirm intended remote and base with git/GitHub metadata. Do not assume the feature branch should target a different base than `main`; stop only if evidence conflicts.
- [ ] Stage named changed files only, commit remaining tested changes, and push `tufte-webui` to the verified repository remote. Do not amend or merge.
- [ ] Open a PR describing the editorial system, tool/delegate semantics, surface coverage, browser panel outcomes, verification and known limitations. Include actual screenshots where feasible without embedding private session content.
- [ ] Verify the PR URL, head/base, commit and final working-tree state. Report the PR, phone-accessible preview, panel results and gates. Leave the preview detached and available for morning review; stop obsolete preview processes and remove temporary scratch artifacts.

## Plan self-review

Every spec section maps to Tasks 1–3; cross-surface coverage, responsive/accessibility checks and real data boundaries map to Task 4; user-requested independent use and iteration map to Task 5; authorized PR delivery maps to Task 6. No backend/schema changes or fictional UI data are part of implementation. Task 2 and Task 3 may run concurrently only after Task 1 lands, each in its own managed worktree with disjoint source ownership.
