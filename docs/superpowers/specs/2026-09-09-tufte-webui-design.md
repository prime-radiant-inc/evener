# Evener web UI: the editorial instrument

Date: 2026-09-09. Status: approved for autonomous implementation.

The user approved an Edward Tufte-inspired, comprehensive web UI makeover in a new worktree, selected **Editorial instrument**, reviewed two browser studies, requested deeper tool and subagent treatment, then approved proceeding without further design checkpoints. Work stays on `tufte-webui`, unmerged, with a phone-accessible preview linked through `http://m5`.

## Purpose and scope

Make the conversation the primary reading surface and the work underneath it legible evidence. This is not a sepia theme, a reproduction of a book, or a rewrite of the application. Apply one coherent system to the shell, navigation, welcome and spawn flows, live and historical transcripts, tool output, delegates, composer, questions, queues, supervisory panels, settings, documents, dialogs, menus, notifications, and mobile layouts.

The shared style guide at `docs/web-ui/design-system.md` remains canonical. This design supersedes the Beautiful UI aesthetic mandate for palette, type, enclosure, and elevation. Its interaction contracts, semantic color meanings, accessibility requirements, and licensing attribution remain intact. Update the guide and `decisions.md` to record that distinction rather than erasing the history.

## Visual grammar

- Warm neutral paper and ink in light mode; near-neutral, slightly warm dark surfaces in dark mode. Preserve `system`, `light`, and `dark` preferences and the existing default behavior.
- Inter for controls and compact operational information; a self-hosted, licensed reading serif for user/assistant prose and editorial headings; JetBrains Mono for code, commands, paths, and machine identifiers. Do not make tool and navigation labels monospace. Use tabular figures for comparable quantities.
- Introduce a shared `--font-prose` and a body-scaled prose size. Target 18px reading text at the default scale, preserving the user's small/medium/large/extra-large preference. Editable phone controls remain at least 16px at the normal scale.
- Keep the existing reading/wide measure preference, shared content alignment, transcript timestamp rail, and responsive fallback. Do not add a second permanent sidebar merely to emulate book sidenotes. Secondary timestamps and existing optional metadata may occupy available margins; on narrow panes they return to the flow without overlap.
- Favor whitespace, alignment, fine rules, and typographic hierarchy over nested cards, pills, tinted bands, and shadows. Structural panes and cards are flat; menus and dialogs retain enough boundary and depth to read as interactive layers. Controls remain unmistakably controls.
- Preserve all four semantic hue meanings: attention means human/actionable attention, alive means active work, danger means failure/destruction, accent means focus/selection/links. No decorative status hues or color-only distinctions. Preserve measured cadence traces and reduced-motion behavior; never animate activity during silence.
- Do not add new dashboards, speculative sparklines, progress percentages, fonts loaded from third-party CDNs, or replacement component frameworks.

## Tools: intent, action, evidence

Use the existing shared ToolRow and descriptor system, not a separate mockup component. The agent's stated intent leads. The exact action or target is quieter beneath it; absent intent produces no invented rationale. Preserve the deliberate two-line grammar and the independent open-out control. Avoid duplicating a shell command when its expanded body already shows it.

Tool bodies retain their native evidence: source excerpts and line numbers, structured search results, additions/deletions, ANSI shell output, image/PDF previews, links, and raw unknown-tool fallback. Give them a quiet, consistent evidence surface with visible boundaries only where useful. Commands, long paths, tables, and diffs must not widen the document or hide adjacent actions. A reader can still reach full retained output and see honest truncation notices.

Routine success recedes; failure or active execution remains legible without expanding a row. Applying an edit does not imply that a subsequent test passed. Do not infer a successful check from successful tool invocation or from an exit status absent from the response.

Retain settled-turn folding exactly: at least three consecutive eligible, successful tool entries; summaries name the last consequential operation. Never fold across prose, reasoning, failures, in-flight work, unknown tools, questions, tasks, or delegates. Do not collapse live work or impose collapse-on-scroll churn. User disclosure choices survive reprojection, virtualization, and theme changes.

## Delegates: durable collaborators, not tool receipts

A compact inline delegate entry presents, in order:

1. Its mandate/assignment, without repeating the same intent in two headers.
2. A truthful current lifecycle with a distinct, visible attention cue when supported by data.
3. The child's latest authored activity, report excerpt, or failure reason.
4. Independent access to the real child transcript, and a disclosure for recent activity and details.

Use restrained typography and rules, not boxed miniature chat windows. Keep each entry at its launch location in transcript chronology. Adjacent delegates can read as a comparable group through alignment, but do not collect or reorder delegates across intervening entries. A separate Activity view remains useful for large fan-out; it is not required to understand a child's contribution to the current exchange.

The launch call and child lifecycle are different facts. A returned `delegate` call does not mean the child finished. Stable delegate identity comes from `delegate_id`, never `job_id`. Current lifecycle comes from the owning stable projection; child snapshots supply authored content. Missing snapshots omit usage, counts, and clocks rather than manufacture zero or perpetual running state.

Keep running, idle/reported, failed, stopped/cancelled, exhausted, unknown, and attention distinguishable using the available projection. A report is not synonymous with a permanently finished delegate, nor with parent acceptance. Resumed work uses the same identity with current-run timing; historical reports and launch receipts remain in the transcript. If the current projection cannot prove a state, say unavailable rather than “working” or “needs you.” A generic attention bit is not permission to invent a human question.

Preserve the existing five-item recent chronological activity window and full-transcript destination. Keep exhaustion reason/budget and resumability when supplied. Do not add progress bars from elapsed time, copy counts to the headline, or fabricate state transitions.

Source inspection identified two boundaries to verify with focused tests: `classifyJobStatus` falls back to running for missing/unrecognized states, and collapsed unknown delegate state maps to `needs-you`. Correct only evidence-backed misclassification; preserve genuine launch-in-flight behavior and all other lifecycle semantics.

## Other surfaces

- **Shell and rail:** a quiet index of work. Preserve routing, Dockview integration, single-pane mode, navigation tree, selection, recency hierarchy, keyboard shortcuts, drag behavior, and mobile drawer. Flatten unnecessary pane enclosure without hiding focus or selection.
- **Welcome and spawn:** editorial headings, aligned settings, a clear primary start action. Preserve model/provider selection, advanced options, attachments, shared directory confirmation and cancel behavior, and mobile setting rows.
- **Composer, queue and AskDock:** retain strong input affordance, visible pending questions and send/steer behavior, attachment handling, and phone keyboard/safe-area geometry. Aesthetic minimalism must not shrink touch targets or conceal validation.
- **Activity, tasks and details:** fine-rule lists, directly labeled data, aligned tabular figures, and restrained empty/error states. Preserve optional accounting preferences and actual data provenance.
- **Settings:** calm section hierarchy, aligned labels and controls, fewer ornamental cards. Preserve every section, provider credentials/editor behavior, preference encodings, validation, destructive confirmation, and accessibility.
- **Documents and read-only transcripts:** use the shared reading/evidence grammar. Keep code and file content separate from human prose.
- **Overlays and notifications:** quieter shape/elevation, unchanged focus trapping/restoration, Escape handling, actionable notices, connection state, and protected force-stop workflow.

## Implementation boundaries

Stay within the frontend and its web UI documentation unless verification isolates an independent required fix. Keep widget public APIs, stores, RPC schema, routing and persistence compatible. Prefer CSS/token changes where sufficient; use small component changes for genuine information hierarchy or truthfulness improvements. Do not implement fictional mockup data, study navigation tabs, or hardcoded reports in production.

All colors and type sizes belong in tokens. Use the current CSS-module patterns and centralized widgets. Preserve the existing mobile breakpoint at 899px. Do not weaken tests, contrast floors, geometry bounds, assertion pairing, or the existing font-size/measure preferences to accommodate the redesign. Historical aesthetic assertions may change only with explicit reference to this approved design, while independent accessibility and behavior checks remain intact.

## Verification and acceptance

1. Every listed UI surface receives either a direct revision or a documented shared-component/token update, with browser inspection of representative real components—not only screenshots of the study.
2. The living widget, surface and typography galleries demonstrate the implemented system in both themes, including live/settled tools, failures, long output, delegates with attention/missing data, and readable prose/code separation.
3. Contrast contracts pass unchanged in strength; no document overflow at phone widths, no clipped sibling controls, and no overlap in the timestamp rail or composer. Test 390px and 360px phone widths, narrow desktop panes, and a wide desktop viewport; include normal and larger text preferences.
4. Tool folding, independent file/transcript opening, disclosure persistence, virtual scroll anchors, question answering, preference persistence, and delegate lifecycle accuracy retain behavioral coverage.
5. Run Biome on touched `src/` files, `make test-web`, `make test-web-browser`, and the production frontend build. Inspect every nonzero result and fix its cause. Run additional repository gates as appropriate to the final diff; report incomplete or environmental gates precisely.
6. Independent review checks both specification coverage and correctness. Re-run decisive checks after fixes.
7. After implementation, a panel of subagents must use the actual revised UI in the browser, not merely review code or the study. Cover the phone workflow, tool evidence and failure handling, delegate lifecycle and child navigation, and settings/keyboard/accessibility. Collect concrete observations and task outcomes. Fix usability findings, then have the affected reviewers repeat their tasks until no actionable blocking findings remain and each reviewer endorses the result. Distinguish a tested workflow from one blocked by environment or missing live data.
8. Deliver the actual implementation on `tufte-webui`, with commits, a clean tracked worktree, an `m5` preview bound to all interfaces, and a concise report naming verification, remaining limits, and review access. After the usability panel approves, push the named feature branch and open a PR against the verified intended base. Do not merge or alter the user's live service.

## Baseline and known external issue

At base `dd01ae3be`, `make test-web` passed typecheck, frontend tests, and Biome after dependency setup. The original `main` checkout's two untracked mobile outcome files were not touched. Dependency audit reported three moderate development-dependency entries from Vitest advisory `GHSA-82fw-gwwq-j7x9`, fixed in Vitest 4.1.11 or newer. Keep any necessary dependency remediation explicit and tested, not hidden in an aesthetic change.

## Design evidence

The approved local studies are retained, ignored by git, under `.superpowers/brainstorm/34548-1788938254/content/`: `editorial-instrument.html` and `tools-and-collaborators.html`. They illustrate direction, not production contracts. Their sample results and transcripts must never enter production data paths.
