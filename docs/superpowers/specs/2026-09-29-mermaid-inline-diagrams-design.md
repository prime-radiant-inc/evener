# Mermaid inline diagrams in session messages

Date: 2026-09-29
Status: design approved in chat; revised after adversarial review, pending re-review

## Goal

Render ```` ```mermaid ```` fenced blocks in session messages as diagrams,
inline in the transcript, on the web hub and in the native mobile app. The
diagram appears where the code block sits in the message. A tap or click opens
a fullscreen viewer with pan/zoom, a show-source toggle, and a copy-source
action.

## Decisions (made with Jesse, 2026-09-28)

1. **One renderer everywhere: mermaid.js.** On native it runs inside a
   WebView (`react-native-webview`; Expo SDK 57 pins 13.16.1 in
   `bundledNativeModules.json`). MermaidKit was considered and set aside:
   8 stars, no React Native binding, an Xcode 26/Swift 6.2 toolchain
   requirement on iOS, and a reimplementation whose quirks would diverge from
   mermaid.js across platforms.
2. **Interaction scope: inline render plus fullscreen viewer.** Parse errors
   fall back to the styled code block with a visible error note. No inline
   pan/zoom inside the transcript.
3. **Streaming: a diagram renders only once its fence closes in the real
   source.** While the stream is in flight and the fence is still open, the
   block shows as an ordinary code block. No parse-error flicker, no
   per-token mermaid re-render churn.

## Context: what exists today

**Web** (`cmd/evener-hub/frontend/src/widgets/markdown/`): the `Markdown`
widget renders a message through marked v18 (custom renderer overrides) into
one HTML string, sanitizes it with DOMPurify against a fixed allowlist (no
SVG today), and sets it with `dangerouslySetInnerHTML`. A `live` prop treats
the source as an in-flight stream: `closeOpenMarkdown` closes unterminated
constructs (including code fences) before parsing, and a windowed throttle
caches the settled head past 2000 characters so per-render work is O(tail).
Consumers: `AgentMarkdown` (transcript; walks the rendered DOM and portals
"Open beside" buttons and entity enhancements into it), `ActivityRowDetail`,
`DocPane`, and the dev gallery. The hub CSP allows inline styles, which
mermaid's SVG output needs, and `script-src 'self'`, which covers the Vite
lazy chunk.

**Native** (`mobile-native/src/MarkdownResponse.tsx`): messages render
through `react-native-enriched-markdown`, a monolithic native component
(Fabric view over an md4c C++ parser; verified against the packed 1.0.2:
props are the markdown string, styles, and event callbacks, with no JS slot
for custom block rendering). Call sites: `TimelineItem` (transcript),
`TasksSheet`, `ActivityDelegateDetails`. `marked` v18 is already a
dependency (`reader/documentBlocks.ts` uses its lexer). The app has no
WebView or SVG dependency today. `TimelineItem` already uses a full-screen
`Modal` pattern (its "Select text" view) and a long-press menu that operates
on the whole markdown string.

## Core mechanism: segment the token stream, not the parse

Both platforms split a message into segments before rendering:

```ts
type Segment =
  | { kind: "markdown"; ... }
  | { kind: "mermaid"; source: string }; // code token text, fence stripped
```

A top-level fenced code block whose language's first word is `mermaid`
(case-insensitive; a capitalized variant gets a render attempt too, with the
same error fallback) becomes a mermaid segment. Everything else stays
markdown. Fences nested inside blockquotes or lists never surface as
top-level tokens (verified), so they keep rendering as literal code.

**Web segments are token slices, not source strings.** The message is
lexed once; the top-level token array is cut at mermaid code tokens; each
markdown slice is rendered by marked's parser with the slice's `.links`
pointing at the whole array's link-definition registry. This matters
because link definitions register document-wide: a `[label]: url`
definition and its use on opposite sides of a diagram must still resolve
(verified broken under naive per-segment source parsing, both directions).
Mermaid segments carry the code token's `text`.

**Native segments are source strings**, because `EnrichedMarkdownText`
takes markdown text. Each markdown segment's source is the concatenation of
its tokens' `raw` fields: byte-exact for LF sources across a 32-case corpus
(tilde fences, indented fences, fences in lists and blockquotes, HTML
blocks, setext headings, front matter, tables with pipes in code spans).
Marked normalizes line endings before tokenizing, so for CRLF sources the
reconstruction is byte-exact only modulo that normalization; it remains
rendering-equivalent. The corpus includes CRLF cases.

**Streaming rule.** In live mode the split runs against the
`closeOpenMarkdown` output, but a mermaid fence that only the auto-closer
closed (the fence is open in the real source) is demoted back to markdown,
so it renders as a code block until the real closing fence arrives. An
unterminated mermaid fence lexes as a top-level code token in the real
source (verified), so a second lex of the real source identifies it.

**The split must meet the throttle's complexity target.** The windowed
live throttle exists because whole-source work per streamed token is O(n²)
over a stream. The split therefore follows the same head/tail caching
discipline as the existing gates: segmentation of the settled head is
cached per exact head text, and only the tail window is re-closed and
re-lexed per render. Per-render cost stays O(tail).

## Web design

- **`widgets/markdown/segments.ts`** (new): the token-slice split above.
  Pure, unit-testable, no renderer changes.
- **`widgets/markdown/index.tsx`**: `Markdown` maps token slices to
  children. Markdown slices render through the current marked → DOMPurify
  → innerHTML path unchanged, one per slice, with the windowed live
  throttle and head cache applied per slice. Mermaid tokens render a new
  `MermaidDiagram` component. All children sit in one wrapper div that
  keeps the existing `ref` contract: `AgentMarkdown`'s link walk keeps
  working (mermaid at `securityLevel: "strict"` emits no `<a>` elements,
  verified, so the walk never touches diagram SVG). Settled markdown
  slices memoize on their exact token-slice identity, so their DOM (and
  the portals mounted into it) stays stable during streaming.
- **Entity-enhancement exclusion** (required change to
  `panes/session/transcript/EntityText.tsx`): the entity walk TreeWalks
  all text under the shared root and today skips only
  `code, pre, script, style, a, [data-entity-host]`. Without an exclusion
  it would walk into diagram SVG and rewrite label text nodes that contain
  real `dlg_`/`job_`/`watch_` ids, emptying the label and mounting an
  invisible HTML span inside SVG. Add the `MermaidDiagram` root (an `svg`
  selector or a data attribute on the component root) to
  `ENTITY_SKIP_SELECTOR`.
- **`widgets/mermaid/`** (new): `MermaidDiagram` lazy-loads mermaid with
  `import("mermaid")`, keeping roughly 500 KB gz out of the main bundle.
  It renders the source to SVG, sanitizes, and sets innerHTML. Mermaid
  config: `securityLevel: "strict"`, `startOnLoad: false`, theme built
  from the active color scheme and re-rendered on theme change. Render
  errors show the source in the existing CodeBlock styling plus an error
  note. Click opens a fullscreen overlay built on
  `widgets/dialog/OverlayPanel` (verified API fit: FocusScope trap, Escape
  and scrim close, scrim dismiss only on presses that start on the scrim)
  with pan/zoom (small transform handler, no new dependency), a
  show-source toggle, and a copy-source action.
- **Sanitizer config** (verified by experiment against mermaid 11.17.2
  and 12.0.0 output; the spec's first draft got this wrong): mermaid emits
  flowchart, class, state, and ER labels as XHTML inside `<foreignObject>`,
  and an svg-only DOMPurify profile strips every one of them. The config
  is:

  ```ts
  {
    USE_PROFILES: { html: true, svg: true, svgFilters: true },
    ADD_TAGS: ["foreignObject"],
    HTML_INTEGRATION_POINTS: { foreignobject: true },
    ADD_ATTR: ["role"], // keeps role="graphics-document document" for screen readers
  }
  ```

  Verified: labels survive on all eight common diagram types, event
  handlers and `javascript:` hrefs are still stripped, `htmlLabels: false`
  does not rescue the svg-only profile on mermaid 11 (node labels stay
  foreignObject), and sequence/gantt/pie/gitGraph use SVG `<text>` and
  survive either way, which would mask the bug in a one-type smoke test.

## Native design

- **`markdownSegments.ts`** (new, `mobile-native/src/`): the same lexer
  split producing source-string segments, pure JS, tested in vitest.
- **`MarkdownResponse`**: maps segments to children. Markdown segments
  render through `EnrichedMarkdownText` as today. Mermaid segments render
  a new `MermaidDiagram`. All children sit in a wrapper `View` that
  carries the `accessibilityActions`/`onAccessibilityAction` props, so a
  message that is only a diagram still exposes the Copy/Quote/Select-text
  VoiceOver actions (today they attach to the single
  `EnrichedMarkdownText`). `TimelineItem`'s long-press menu and "Select
  text" modal operate on the full markdown string via the wrapper
  `Pressable` and are unaffected; "Copy response" still copies the whole
  original message.
- **`MermaidDiagram`** (new): a `react-native-webview` hosting one
  self-contained HTML page with mermaid bundled. The page renders the
  diagram and posts its height back over `postMessage`; the component
  sizes the WebView to fit. The mermaid source crosses the bridge as a
  JSON message, never string-interpolated into JS. Tap opens a fullscreen
  `Modal` following the `TimelineItem` pattern: WebView-native zoom,
  source toggle, copy via the existing `clipboard` helper.
- **Long-press dead zone** (documented trade-off): the WebView becomes
  the touch responder for its own region, so the transcript's long-press
  menu does not fire over the diagram itself. The tap-to-fullscreen
  affordance covers the diagram's region; the menu remains reachable over
  the prose and via the fullscreen view's source toggle.
- **WebView page packaging**: one build step produces a single
  self-contained HTML asset (mermaid plus a small bootstrap: receive
  source and theme, render, post height). The exact packaging (script
  output committed as an asset, loaded via Metro's asset system) is the
  first native implementation task, with a verification step.

## Security

Model-authored mermaid is untrusted input.

- Web: two layers with distinct jobs, now stated precisely because the
  widened sanitizer makes the first load-bearing. Mermaid at
  `securityLevel: "strict"` encodes authored label text (an `<img
  onerror>` label arrives as escaped text, verified) and disables click
  interactions; that encoding is what keeps label *content* safe now that
  `foreignObject` XHTML is allowed through. DOMPurify then sanitizes the
  SVG *structure*, and is verified to strip event handlers and
  `javascript:` hrefs inside foreignObject content. Residual note: CSP
  `img-src` allows `https:`, so label markup that survives both layers as
  an `<img>` could fetch remotely; the strict-mode encoding is the control
  for that, not CSP.
- Native: the WebView loads no network resources and no file URLs;
  JavaScript is enabled (required for mermaid) but the only inbound
  channel is the JSON bootstrap message; `onMessage` validates the message
  shape before using it.

## Theming

Mermaid initializes per theme with `themeVariables` derived from each
platform's design tokens, so diagrams match dark and light mode. A color
scheme change re-renders visible diagrams.

## Pre-existing bug surfaced by review (fixed as part of this work)

`closeOpenMarkdown`'s fence regexes (`streaming.ts:66-67`) reject
CR-terminated lines (`.` excludes `\r`, `$` anchors string end), and
`streaming.test.ts` has no `\r` coverage. Verified consequences for CRLF
sources in live mode today: an open CRLF fence gets the auto-closer's
backticks glued onto its last content line, and a CRLF closing fence at
end of input goes unrecognized so a second, phantom fence is appended.
The mermaid demote rule inherits all of this. Fix: make the fence regexes
`\r`-tolerant (or normalize line endings at the `closeOpenMarkdown`
boundary) and add CRLF cases to `streaming.test.ts`.

## Testing

- Unit (vitest, both platforms): segment splitting across fence forms
  (```` ``` ```` and `~~~`), indented fences, fences nested in
  blockquotes and lists, unterminated fences in live mode, CRLF sources,
  and language-string variants (`mermaid` with trailing whitespace or
  info-string text, case variants, non-matches like `.mermaid` and
  `mermaidjs`). A link definition used on the opposite side of a diagram
  from its definition must resolve. CRLF cases for `closeOpenMarkdown`
  alongside its fix.
- Sanitizer fixtures: committed SVG produced by the real mermaid. A
  committed generator script runs mermaid in jsdom with a small shim for
  the six missing layout APIs (getBBox, getComputedTextLength,
  getScreenCTM, getPointAtLength, CSSStyleSheet, adoptedStyleSheets;
  verified feasible, structure is real though geometry is not), and the
  browser-guard case renders live for real geometry. Regeneration is
  triggered on mermaid version bumps. The fixture test asserts label text
  survives sanitization, with at least one flowchart fixture; that
  assertion would have caught the foreignObject bug at implementation
  time.
- Web real render: a case in the browser-guard suite
  (`make test-web-browser`) renders a real diagram end to end.
- Native real render: manual runbook on device (no native browser gate
  exists), covering render, theme switch, error fallback, and the
  fullscreen viewer.

## Known trade-offs

- Segment boundaries change spacing: `:first-child`/`:last-child` rules
  in `markdown.module.css` (heading top margin, paragraph bottom margin)
  now evaluate per markdown slice rather than per message, so prose can
  sit flush against a diagram. Slice 1 includes a visual check of
  transcript spacing aimed at exactly this.
- Native text selection no longer spans across a diagram (each prose
  segment is its own native text view). Whole-message selection remains
  available in the "Select text" modal.
- The transcript long-press menu does not fire over the diagram region on
  native (see the dead-zone note above).
- `react-native-webview` is a new native dependency and the mermaid
  bundle adds roughly 500 KB gz inside the app.

## Slices

0. `closeOpenMarkdown` CRLF fence fix + tests (pre-existing bug, blocks
   the mermaid live path).
1. Web: segments + inline diagram + error fallback.
2. Web: fullscreen viewer.
3. Native: WebView page packaging, segments + inline diagram + error
   fallback (adds `react-native-webview`).
4. Native: fullscreen viewer.

## Non-goals

- Rendering mermaid authored anywhere other than message markdown (doc
  pane and activity rows get it for free through the shared widget, but no
  new call sites are added).
- Diagram editing, live re-layout, or export to PNG/SVG files.
- Server-side rendering.

## Adversarial review provenance

Two independent reviewers attacked the first draft of this spec under
competition rules; every finding above marked "verified" was confirmed by
re-running the reviewer's experiment or by direct code reading before
adoption. The foreignObject sanitizer bug, the cross-segment link
definition regression, the entity-walk exclusion, the CRLF invariant
correction, the O(n²) split concern, the spacing mechanism, the native
dead zone, the VoiceOver carrier, and fixture provenance came from that
review. Verified-as-sound and kept: the CSP's sufficiency, the lack of a
JS slot in react-native-enriched-markdown, `react-native-webview` Expo
support at SDK 57, OverlayPanel's fit, strict mode emitting no anchors,
the demote rule's implementability, and byte-exact raw reconstruction for
LF sources.
