# WebUI Transcript Typography and Rhythm Cleanup — Design

**Date:** 2026-07-09
**Status:** Approved for implementation

## Purpose

Tighten Serf Hub’s WebUI transcript reader and transcript-related settings panel by improving typography and spacing, and by limiting preformatted/monospace presentation to genuinely machine-oriented content.

The work is intentionally a presentation pass. It must not alter transcript transport, lazy loading, tool behavior, disclosure behavior, status semantics, or preference persistence.

## Goals

- Make human-authored transcript content easier to scan and read.
- Establish a consistent vertical rhythm across messages, tool rows, and transcript settings.
- Use the sans family for prose and UI chrome.
- Reserve monospace and preserved whitespace for code, commands, file paths, identifiers, raw tool arguments, raw tool output, JSON, diffs, and other machine text.
- Preserve current dark/light themes, font-size preferences, accessibility, and all existing transcript interactions.

## Non-goals

- Changing transcript data structures or AppWire payloads.
- Changing lazy transcript loading, streaming, tool grouping, completion states, or error handling.
- Adding settings or changing defaults.
- Hiding existing content behind new disclosures.
- Redesigning desktop Hub layout, sidebar behavior, or composer behavior outside short landscape viewports.

## Existing implementation boundaries

The change remains within the current Hub rendering and styling boundaries:

- `cmd/serf-hub/assets/style.css` supplies shared tokens and transcript/message styles.
- `cmd/serf-hub/assets/renderer.js` renders conversation entries and applies their semantic classes.
- `cmd/serf-hub/assets/renderer-tools.js` renders tool summaries, structured arguments, expandable output, diffs, and source-truncation/binary notices.
- `cmd/serf-hub/templates/partials/settings/transcript.html` provides transcript-preference markup.
- `cmd/serf-hub/assets/settings-transcript.js` owns localStorage persistence, checkbox state restoration, and change events.

No server, AppWire, transcript-projection, or preference-key contract changes are required.

## Design

### 1. Transcript reader hierarchy

The reader distinguishes human communication from machine detail visually without changing the underlying entry types.

#### Human-authored content

User messages, assistant prose, steering, notification prose, labels, and disclosure copy use `--font-sans`.

- Assistant prose is the primary reading layer: use the established medium text token and relaxed readable leading.
- Paragraphs, lists, and adjacent blocks receive consistent internal spacing rather than inheriting preformatted behavior.
- User and steering content remain visually quieter than assistant prose, but are still readable proportional text.
- Role labels become subdued sans UI text. The transcript must not depend on all-caps, heavy tracking, or monospace chrome to establish hierarchy.

#### Tool entries

A tool card’s purpose, title, result summary, status copy, and disclosure labels are human-facing and therefore use `--font-sans`.

Machine detail is isolated to the elements that actually contain it:

- commands and command arguments;
- file paths and identifiers;
- formatted JSON arguments;
- raw standard output and standard error;
- diff content;
- fenced and inline code.

These machine values retain `--font-mono` and whitespace-preserving behavior. The current full-card `.msg.tool` monospace rule is removed or narrowed so it does not affect normal tool labels and summaries.

### 2. Spacing and containment

Apply the established spacing scale consistently rather than introducing ad hoc pixel values.

- Top-level transcript entries have a clear, repeatable inter-entry gap.
- Each entry uses smaller internal gaps between label, summary, body, and machine detail.
- Padding and backgrounds do not compound unnecessarily: visual emphasis remains reserved for stateful entries such as errors, active work, or needs-attention content.
- The conversation column remains constrained to the existing readable workspace width.
- Side-pane compact mode uses the same hierarchy with a reduced but token-based rhythm; it must not reintroduce monospace chrome or make tool headers unreadably wrap.

### 3. Preformatted content rules

Whitespace preservation is not a default message style.

- Ordinary transcript prose wraps normally and uses the sans family.
- Existing `<pre>` blocks continue to preserve whitespace, allow horizontal scrolling for long unbroken values, and use the mono family.
- Inline code remains mono without acquiring block-level `pre` spacing or wrapping behavior.
- Tool renderer output previews, expandable output tails, formatted arguments, and diffs retain their current preformatted DOM paths and current expand/collapse behavior.

### 4. Transcript settings panel

The existing four preferences and their localStorage behavior remain unchanged.

The settings partial receives the same visual language:

- normal-weight sans heading and labels;
- a subdued supporting sentence below the section heading;
- aligned label/toggle rows with consistent row spacing;
- help text as readable secondary prose with normal line-height;
- no code-like or overly dense label treatment.

The explanation that **Hook exits (all)** includes normal exits remains visible and plain-language. No preference is merged, removed, or made dependent on another as part of this work.

### 5. State, accessibility, and responsive behavior

- Current color/status semantics and visual distinction for active, awaiting, warning, completed, and error states remain intact.
- Toggle inputs, disclosure summaries, and existing keyboard/focus interactions remain functional and retain visible focus treatment.
- Semantic native elements (`details`, `summary`, `pre`, labels, inputs) are preserved.
- Human prose continues to wrap at narrow widths; only machine payloads may scroll horizontally.
- Font-size settings and light/dark theme tokens apply to all adjusted styles without per-component overrides.

### 6. Short landscape workspace mode

The screenshot review identified a distinct short-landscape problem: a permanently visible desktop-width sidebar and tall composer/status chrome leave too little vertical and horizontal room for transcript reading.

Apply a CSS-only workspace mode at `@media (max-width: 900px) and (max-height: 560px)`. Desktop and portrait layouts remain unchanged.

- The sidebar is off-canvas/default hidden in this mode so `#workspace` receives the full shell width. Its resizer, side panes, and pane splitter are hidden.
- The existing app navigation control remains the entry point for opening the sidebar; no new viewport JavaScript, persisted layout state, or navigation behavior is introduced.
- The workspace header, composer, task status, queue preview, and input-status strip use reduced token-based spacing. The textarea is constrained to a compact composing height while retaining attachment, model, stop, steer, and send controls.
- The input-status strip keeps source, state, and turn count visible. CWD, branch, context, work time, tokens, liveness, cost, and goal are visually demoted in this mode so they cannot consume multiple tall rows. Full session details remain available through the existing Details surface.
- `.conversation` continues to flex and scroll within `#workspace`, taking the reclaimed short-viewport height. Transcript data, loading, streaming, and interactions do not change.

## Data flow

No new data flow is introduced:

1. Existing AppWire and transcript events reach the Hub renderer.
2. `renderer.js` and `renderer-tools.js` build the same semantic DOM structures.
3. CSS applies prose versus machine-text typography based on existing message classes and elements/classes emitted by the renderer.
4. The settings partial renders the same inputs.
5. `settings-transcript.js` continues to read/write `serf-hub.transcript.systemStatus`, synchronize ON/OFF labels, emit the existing change event, and show the existing saved toast.

## Edge cases

- **Long output:** retain five-line preview, expandable remainder, source-truncation notice, and binary-output notice. Changes are typographic only.
- **Tool errors:** preserve automatic expansion and error-state treatment; readable error summary text is sans, while raw stderr stays preformatted mono.
- **Diffs:** preserve current diff line rendering and monospace line layout.
- **Markdown:** ordinary paragraphs and lists are sans; inline/fenced code remains mono.
- **Long machine tokens:** prevent layout overflow through current `pre` scrolling behavior rather than forcing human prose into `white-space: pre`.
- **Compact panes:** preserve a legible summary-first order without changing side-pane data loading or navigation.
- **Short landscape:** the sidebar must not remain a desktop-width column; long operational status fields must not displace the conversation into a shallow reading area; primary composer controls and source/state/turn information remain accessible.

## Acceptance criteria

1. Human-authored reader content and UI labels display in the sans family and wrap normally.
2. Monospace/preformatted styles are limited to code, commands, paths, identifiers, JSON/arguments, raw tool output, and diffs.
3. Tool titles, intent text, summaries, status copy, and disclosure labels are no longer made monospace merely because they are inside a tool entry.
4. Transcript message spacing and settings-row spacing follow the shared spacing/type tokens consistently.
5. Transcript settings retain all four controls, their accessible label associations, preference key, persistence, event emission, and saved-toast behavior.
6. Existing lazy loading, output expansion, error auto-expansion, binary/truncation notices, theme selection, font-size selection, and keyboard/focus behavior are unchanged.
7. Default tests remain deterministic and do not require provider credentials, network access, or a live browser.
8. At `max-width: 900px` and `max-height: 560px`, the sidebar and pane chrome are not persistently visible; the conversation gets full workspace width and retains the available reading height after compact workspace chrome.
9. In that short-landscape mode, source, state, turn count, attachment, model, stop, steer, and send remain accessible; long operational status fields are demoted without changing the underlying status payload or Details access.

## Verification plan

1. Update deterministic Hub JavaScript tests around renderer/tool DOM output to verify prose/summary versus machine-output class/element boundaries and preserve tool disclosure behavior.
2. Retain and extend the Go settings-render test only if markup hooks/classes change; assert all four controls and their accessible labels remain present.
3. Run the relevant Hub Go tests and the affected `cmd/serf-hub/jstest` tests.
4. Add deterministic CSS/markup contract coverage for `@media (max-width: 900px) and (max-height: 560px)`, verifying the off-canvas sidebar/pane rules, compact workspace chrome, primary control retention, and status-field demotion without requiring a live viewport.
5. Inspect the static WebUI golden and hard-case examples against the shared typography, spacing, and short-landscape selectors as a manual UI review; this is a design check, not a network-dependent CI test.
6. Verify the working tree and staged diff contain only focused transcript/settings/short-landscape styling, rendering, and corresponding deterministic tests when implementation begins.
