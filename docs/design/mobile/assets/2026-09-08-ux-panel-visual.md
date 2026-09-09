Review record: preserve the initial independent assessment and its adjudication appendix. The coordinator synthesis in ../2026-09-08-iphone-ux-panel.md governs accepted priorities.

Method: single reviewer B (visual hierarchy, interaction coherence, source and screenshot evidence)

# iPhone UX review — visual panel

Scope: Operate/Read flows on iPhone only, using the current native source and the six supplied iPhone screenshots. iPad, accessibility-size qualification, physical-device performance, and simulator mutations were out of scope.

## Design specificity verdict

The surface is authored for Evener’s project-first work model. The expandable project hierarchy, quiet session rows, native navigation, native iOS message action sheet, and light/dark semantic palette are product-specific choices rather than a generic web list. The screenshots also show the intended reading-first direction: assistant content is open on the canvas, project/session navigation stays compact, and the composer remains available.

The main hierarchy is coherent in the browser and search states, but the conversation state gives the composer and user-message treatment more visual weight than the work itself. The biggest opportunity is to make the reader’s content the stable visual anchor while keeping consequential actions discoverable.

## Heuristic scores

| # | Heuristic | Score | Key issue |
|---|---|---:|---|
| 1 | Visibility of System Status | 3 | Connection status and loading/retry states are present, but the reader’s active/uncertain state is not visible in the supplied populated reader frame. |
| 2 | Match System / Real World | 3 | “Working”, “Question waiting”, “Needs attention”, and native sheets fit the domain; some fixture-facing labels remain technical or internal-looking. |
| 3 | User Control and Freedom | 3 | Back, Clear, Cancel, retry, and native action sheets are clear; conversation-level controls are concentrated behind overflow. |
| 4 | Consistency and Standards | 3 | Shared tokens and native controls are consistent, but the filled user bubble and open assistant surface create a strong treatment split. |
| 5 | Error Prevention | 3 | Disabled mutation controls and guarded paging are visible in source; consequential outcomes are not represented in these screenshots. |
| 6 | Recognition Rather Than Recall | 3 | Project names and search results identify destinations; the reader’s footer uses compact labels whose full meaning is not obvious from the visual alone. |
| 7 | Flexibility and Efficiency | 3 | Search, paging, preserved expansion, and message actions support repeat use; no evidence here of an accelerated path for frequent reader actions. |
| 8 | Aesthetic and Minimalist Design | 2 | Browser is restrained, but the header chrome and composer occupy substantial vertical area; the reader frame is visually top-heavy. |
| 9 | Error Recovery | 3 | Source includes retry/refresh paths and stale-state handling; screenshots do not show the recovery copy in context. |
| 10 | Help and Documentation | 2 | The primary paths are understandable, but compact composer controls and empty reader state offer little explanation or next-step guidance. |
| **Total** | | **28/40** | Good foundation; reader/composer hierarchy and explanatory states need another pass. |

## Deterministic scan

Command run:

`node /Users/jesse/.codex/plugins/cache/openai-curated-remote/impeccable/4.1.1/skills/impeccable/scripts/detect.mjs --json mobile-native/src`

Observed result: exit 0, JSON `[]`, no rule names, counts, or locations. Raw stdout and stderr are retained privately at `/private/tmp/evener-ux-panel-visual-detector.json` and `/private/tmp/evener-ux-panel-visual-detector.err`.

This detector scans web-style markup patterns and found no deterministic findings in the React Native source. It does not certify native layout, Dynamic Type, VoiceOver order, physical touch geometry, keyboard behavior, or native sheet presentation. No browser overlay was claimed or produced: the target is a native app, no browser page was supplied, and this review was read-only. The manual findings below therefore carry the visual and interaction verdict.

## Overall impression

The project browser now has a calm, legible structure: the project name leads, counts recede, search is easy to find, and expanded sessions read as one hierarchy. The reader is less resolved. The populated screenshot spends a large share of the viewport on the composer, uses a heavy rounded card for the user turn against open assistant content, and presents compact footer choices with little semantic explanation. The single biggest opportunity is to establish a stable reader/composer ratio and let the transcript remain the visual center during ordinary reading.

## What's working

1. The project browser hierarchy is immediately legible. In both browser screenshots, project names are large and aligned, session rows are indented once beneath the expanded project, and the chevron plus separate trailing overflow affordance preserve two distinct actions. This matches `ProjectSessionsList.tsx:329-396` and the two-level indentation rule.

2. Search has a clear, recoverable interaction. The dark search screenshot keeps the query visible, adds an explicit Results line, offers Search and Clear, and reduces project paths to readable labels. Source preserves the full accessible label and uses a separate search result list at `screens.tsx:589-633` and `screens.tsx:679-713`.

3. Message actions use an appropriate iOS convention without stealing transcript width. The screenshot shows the selected message title, Cancel, and Fork from here in a native-looking action sheet. The source keeps the action at a 44×44 target and labels it with the message preview at `TimelineItem.tsx:51-108`. This is a good pattern to reuse for other consequential, message-scoped actions.

## Priority issues

### [P1] Composer dominates the reader

Why it matters: In `2026-09-08-project-reader-after.png`, the composer occupies roughly the bottom fifth of the iPhone frame even with a short draft, while the footer’s model/reasoning/vision choices and Send button have comparable visual prominence. This reduces the amount of transcript visible during reading and makes the writing surface feel like the primary product. It conflicts with the philosophy’s “make the work the visual center” rule.

Evidence: screenshot `2026-09-08-project-reader-after.png`; composer is rendered in the conversation screen around `screens.tsx:1500+` (current source should be checked at implementation time), and the shared primary Action styling is `ui.tsx:25-66`.

Fix: Keep the draft full-width, but reduce idle composer chrome and reserve the strongest fill for Send only when the draft is actionable. Keep model/reasoning as compact secondary controls and let the composer grow only with text or active decisions. Verify keyboard-open and large-text variants with the same transcript fixture.

Suggested command: `$impeccable layout`.

### [P1] Reader hierarchy is split by a heavy user-message card

Why it matters: The reader screenshot uses a large rounded filled bubble for the user turn while assistant prose sits directly on the background. The treatment is recognizable, but the card’s dark surface, radius, padding, and trailing action become a visual interruption between two assistant blocks. On long transcripts, repeated bubbles will create a field of competing containers, directly contrary to the philosophy and `DESIGN.md` open-reading-surface guidance.

Evidence: `TimelineItem.tsx:213-235` applies `backgroundColor: colors.surface`, `borderRadius: 18`, `padding: 14`, and `marginLeft: 24` to every user item; the same treatment is visible in `2026-09-08-project-reader-after.png`.

Fix: Retain speaker distinction through alignment, attribution, spacing, and a restrained surface treatment. Reduce radius/padding and use the surface only as a subtle cue for user turns; keep the message action mounted in the same 44-point slot. Recheck long user messages and a sequence of adjacent turns.

Suggested command: `$impeccable quieter`.

### [P2] Header and connection chrome compete with the first task

Why it matters: The browser screenshots devote the first viewport to a large Hubs control, centered Sessions title, New/menu control group, connection line, and search row before the first project. Each element is individually understandable, but together they delay the project hierarchy and make the first task begin below a tall stack of controls. The connection line is useful when state changes, but quiet connected status should have less visual pull.

Evidence: `2026-09-08-project-browser-dark.png` and light counterpart; `screens.tsx:147-161` always renders connection status, while `screens.tsx:487-547` configures multiple header action representations and `screens.tsx:549-645` adds the in-list header/search.

Fix: Keep native navigation and hub identity, but reduce connected-state prominence and consolidate the visual header path. Reserve extra status height for disconnected, stale, or uncertain states. Compare the first project’s top position before and after on the same iPhone.

Suggested command: `$impeccable distill`.

### [P2] Empty reader state does not explain the next useful action

Why it matters: `2026-09-08-project-empty.png` shows “No messages yet.” beneath “iPhone joined journey · Connected” with a large unused reading area before the composer. The person can infer that typing is possible, but the state does not establish whether the session is newly created, still loading, or waiting for the first message. This is especially ambiguous after opening a session from search.

Evidence: screenshot `2026-09-08-project-empty.png`; source status and empty transcript rendering are in `screens.tsx:147-161` and the conversation body around the current `ConversationScreen` render.

Fix: Add a concise, human next step tied to the state, such as “Start the conversation below” for a confirmed empty session, and distinguish loading/reconnecting/unknown from a confirmed empty transcript. Keep it short enough to preserve the reading surface.

Suggested command: `$impeccable clarify`.

### [P2] Compact composer labels need stronger recognition cues

Why it matters: In the populated reader screenshot, “fixture-model”, “(default)”, and “Vision” appear as a row of compact text controls with chevrons. Their role is discoverable only after tapping or remembering the composition contract. “Vision” can read as a capability/status label rather than a selectable setting, and “(default)” is not clearly paired with the control it qualifies.

Evidence: `2026-09-08-project-reader-after.png`; `screens.tsx` composer rendering and `ComposerSettings.tsx`/ `ComposerSettingsSheet.tsx` provide the control path; shared `Action` renders quiet controls at `ui.tsx:25-66`.

Fix: Give each setting a compact visible label/value pair with a consistent separator and accessible full name; keep model and reasoning separately tappable. Use a dedicated secondary row only at large text sizes as the style guide requires, and ensure the current effective value is visually attached to its label.

Suggested command: `$impeccable clarify`.

## Persona red flags

**Alex (Power User):** The project browser supports repeat use well, but the reader’s high-frequency actions are split between the overflow menu, message-specific dots, footer selectors, and the large Send control. Alex must visually parse several low-label affordances before acting. The stable message action slot and preserved browser state are positives.

**Jordan (First-Timer):** “Hubs”, “Sessions”, “New session”, and “Vision” are individually plausible, but the empty reader state does not tell Jordan whether to wait or write. The composer’s compact footer does not explain which control changes the model versus reasoning, and the connection line reads as system status without a next step.

**Morgan (Interrupted / returning user):** The source has strong continuity intent and the supplied browser evidence shows expansion/position preservation. The main remaining risk is visual: a returning user lands on a large composer and may lose the transcript context that tells them what is currently happening. Active, stale, or uncertain states need to be visually distinct from the quiet connected state.

## Minor observations

- Project browser rows use 0.5-point dividers and generous blank space; this is calm, though the first project starts low enough that fewer projects are visible in the initial viewport.
- The empty reader title “session Rmtlg1” looks like a fixture or internal identifier. If this can appear in production, prefer a human title or an explicit untitled-session label.
- The search result metadata is clearer than the browser’s project count because it includes the project label; reuse that association in any future cross-hub result.
- The action-sheet screenshot dims the underlying reader effectively, and the selected message remains identifiable through the sheet title.
- The source uses literal colors in `useColors`, but they are centralized semantic theme values and match DESIGN.md; the detector’s empty result is therefore not evidence of broad native theming correctness.

## Questions to consider

- Can the composer become visually quiet until the person focuses it or has a draft?
- Can user turns remain distinct with less container weight so the transcript reads as one continuous document?
- When a session is empty, what exact state is confirmed, and what is the one next action the person should understand?
- Which connected-state information belongs in the quiet header, and which should expand only when recovery is needed?

## Run notes

- Target: `mobile-native/src`; worktree and HEAD were supplied as `d683abe21`; no source edits or simulator mutations were made.
- Required source/design references and supplied screenshots were read.
- Detector: real CLI attempt completed, exit 0, empty JSON result; raw output retained privately.
- Browser/overlay: skipped because this is a native React Native target and no mutable browser surface was in scope; no overlay is claimed.
- Detector limitation: native layout, VoiceOver, Dynamic Type, keyboard/inset behavior, and physical-device geometry require native runtime review.
- Temp/report path: this report is written at `/private/tmp/evener-ux-panel-visual.md`.

## Source-verified adjudication appendix

This appendix narrows the original visual review where source contracts change the interpretation. It does not replace the independent observations above.

### Header chrome is native and should be preserved

The browser header is supplied through React Navigation’s native iOS header APIs: `screens.tsx:487-536` configures `unstable_headerLeftItems` and `unstable_headerRightItems` with SF Symbols, while `screens.tsx:537-547` supplies the compatibility header components. The supplied screenshots therefore show OS-controlled native navigation layout. The earlier suggestion to “consolidate” or shrink the header was a visual preference without sufficient source evidence and should not be treated as a requested change. Keep Hubs, New session, and Hub actions in the native header; review only content below it if a measured first-project reachability problem appears.

### User-message surface is an accepted speaker distinction

The style guide explicitly permits a restrained user-message surface, and `TimelineItem.tsx:213-235` implements that contract. The concern is only a bounded readability risk in long or repeated user turns: the surface adds `marginLeft: 24`, `padding: 14`, and a trailing 44-point action slot, reducing available text width and potentially causing earlier wrapping. No supplied screenshot proves a task failure or unacceptable wrapping. Downgrade this from a P1 conclusion to a P2 validation item: run the long-message and adjacent-turn fixtures, measure line count/scroll distance, and retain the surface if reading remains comfortable. The action target and source placement should remain stable.

### Composer sizing is a validation opportunity, not a dominance defect

The supplied reader screenshot’s composer is approximately 12–13% of the full iPhone frame, rather than one fifth. Source confirms bounded behavior: `screens.tsx:2205-2221` uses `flexShrink: 1` and `maxHeight: "80%"`; the multiline input is full-width with a 44-point iOS minimum and a 160-point default maximum at `screens.tsx:2329-2355`; model, reasoning, and vision remain separately tappable in `ComposerSettings.tsx:35-150`. The earlier P1 wording overstates the evidence. Treat this as P2 visual tuning/measurement: preserve the full-width input, separate model/reasoning targets, and native 44-point controls while checking keyboard-open, long-draft, and large-text cases. Do not reduce the composer solely because the static screenshot feels tall.

### Empty state logic is already differentiated in source

The source distinguishes opening/loading, disconnected, read error, and confirmed empty states at `screens.tsx:2131-2140`: “Loading conversation…”, “Reconnect to load the conversation. Your draft is kept.”, “Pull down to retry.”, and “No messages yet.” The screenshot is therefore evidence of the confirmed empty branch, not an ambiguity bug. Replace the original P2 diagnosis with a P3 copy refinement: if product wants a stronger first action, append a short “Start the conversation below” cue only to the confirmed empty branch, preserving the existing recovery messages.

### Bounded recommended actions

1. **P2 validation — `$impeccable critique`**: test long/repeated user turns and confirm whether the accepted surface causes materially worse wrapping or scroll cost; preserve the 44-point action slot.
2. **P2 validation — `$impeccable layout`**: inspect composer height in keyboard-open and long-draft fixtures; preserve full-width input, separately tappable model/reasoning, and native minimum targets.
3. **P3 copy refinement — `$impeccable clarify`**: optionally add one short instruction to the confirmed empty branch only; retain loading, disconnected, and retry copy.
4. **No header action**: retain the native iOS OS-controlled header as implemented by `screens.tsx:487-547`.

