# Mermaid inline diagrams in session messages

Date: 2026-09-29
Status: design approved in chat, pending written-spec review

## Goal

Render ```` ```mermaid ```` fenced blocks in session messages as diagrams,
inline in the transcript, on the web hub and in the native mobile app. The
diagram appears where the code block sits in the message. A tap or click opens
a fullscreen viewer with pan/zoom, a show-source toggle, and a copy-source
action.

## Decisions (made with Jesse, 2026-09-28)

1. **One renderer everywhere: mermaid.js.** On native it runs inside a
   WebView (`react-native-webview`). MermaidKit was considered and set aside:
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
caches the settled head past 2000 characters. Consumers: `AgentMarkdown`
(transcript; walks the rendered DOM and portals "Open beside" buttons and
entity enhancements into it), `ActivityRowDetail`, `DocPane`, and the dev
gallery. The hub CSP allows inline styles, which mermaid's SVG output needs.

**Native** (`mobile-native/src/MarkdownResponse.tsx`): messages render
through `react-native-enriched-markdown`, a monolithic native component with
no JS-level slot for custom block rendering. Call sites: `TimelineItem`
(transcript), `TasksSheet`, `ActivityDelegateDetails`. `marked` v18 is
already a dependency (`reader/documentBlocks.ts` uses its lexer). The app
has no WebView or SVG dependency today. `TimelineItem` already uses a
full-screen `Modal` pattern (its "Select text" view) and a long-press menu
that operates on the whole markdown string.

## Core mechanism (shared by both platforms)

Split message markdown into segments before rendering, using the marked v18
lexer:

```ts
type Segment =
  | { kind: "markdown"; source: string }
  | { kind: "mermaid"; source: string }; // source is the diagram text, fence stripped
```

A top-level fenced code block whose language's first word is `mermaid`
(case-insensitive; a capitalized variant gets a render attempt too, with the
same error fallback) becomes a mermaid segment carrying the code token's
`text` (fence stripped). Everything else stays markdown, each markdown
segment's source the concatenation of its tokens' `raw` fields, verified
byte-exact against the original source on a corpus covering `~~~` fences,
indented fences, tables, and link definitions. Fences nested inside
blockquotes or lists never surface as top-level tokens, so they keep
rendering as literal code.

**Streaming rule.** In live mode the split runs against the
`closeOpenMarkdown` output, but a mermaid fence that only the auto-closer
closed (the fence is open in the real source) is demoted back to markdown,
so it renders as a code block until the real closing fence arrives.

## Web design

- **`widgets/markdown/segments.ts`** (new): the split above. Pure,
  unit-testable, no renderer changes.
- **`widgets/markdown/index.tsx`**: `Markdown` maps segments to children.
  Markdown segments render through the current marked → DOMPurify →
  innerHTML path unchanged, one per segment, with the windowed live
  throttle applied per segment. Mermaid segments render a new
  `MermaidDiagram` component. All segments sit in one wrapper div that
  keeps the existing `ref` contract: `AgentMarkdown` and the entity
  enhancement walk that root with `querySelectorAll` and keep working.
  Settled markdown segments memoize on their exact source text, so their
  DOM (and the portals mounted into it) stays stable during streaming.
- **`widgets/mermaid/`** (new): `MermaidDiagram` lazy-loads mermaid with
  `import("mermaid")`, keeping roughly 500 KB gz out of the main bundle.
  It renders the source to SVG, sanitizes the SVG with a separate
  DOMPurify profile (`USE_PROFILES: { svg: true, svgFilters: true }`), and
  sets innerHTML. Mermaid config: `securityLevel: "strict"`,
  `startOnLoad: false`, theme built from the active color scheme and
  re-rendered on theme change. Render errors show the source in the
  existing CodeBlock styling plus an error note. Click opens a fullscreen
  overlay built on `widgets/dialog/OverlayPanel` with pan/zoom (small
  transform handler, no new dependency), a show-source toggle, and a
  copy-source action.

## Native design

- **`markdownSegments.ts`** (new, `mobile-native/src/`): the same lexer
  split, pure JS, tested in vitest.
- **`MarkdownResponse`**: maps segments to children. Markdown segments
  render through `EnrichedMarkdownText` as today. Mermaid segments render
  a new `MermaidDiagram`. `TimelineItem`'s long-press menu and "Select
  text" modal already operate on the full markdown string and are
  unaffected; "Copy response" still copies the whole original message.
- **`MermaidDiagram`** (new): a `react-native-webview` (new dependency,
  Expo-supported) hosting one self-contained HTML page with mermaid
  bundled. The page renders the diagram and posts its height back over
  `postMessage`; the component sizes the WebView to fit. The mermaid
  source crosses the bridge as a JSON message, never string-interpolated
  into JS. Tap opens a fullscreen `Modal` following the `TimelineItem`
  pattern: WebView-native zoom, source toggle, copy via the existing
  `clipboard` helper.
- **WebView page packaging**: one build step produces a single
  self-contained HTML asset (mermaid plus a small bootstrap: receive
  source and theme, render, post height). The exact packaging (script
  output committed as an asset, loaded via Metro's asset system) is the
  first native implementation task, with a verification step.

## Security

Model-authored mermaid is untrusted input.

- Web: two independent layers, matching the widget's existing philosophy.
  Mermaid runs at `securityLevel: "strict"` (no click interactions, no raw
  HTML in labels); DOMPurify then sanitizes the SVG output against the SVG
  profile. CSP already permits mermaid's inline styles.
- Native: the WebView loads no network resources and no file URLs;
  JavaScript is enabled (required for mermaid) but the only inbound
  channel is the JSON bootstrap message; `onMessage` validates the message
  shape before using it.

## Theming

Mermaid initializes per theme with `themeVariables` derived from each
platform's design tokens, so diagrams match dark and light mode. A color
scheme change re-renders visible diagrams.

## Testing

- Unit (vitest, both platforms): segment splitting across fence forms
  (```` ``` ```` and `~~~`), indented fences, fences nested in
  blockquotes, unterminated fences in live mode, and language-string
  variants (`mermaid` with trailing whitespace or info-string text).
  Sanitizer profile tested against committed SVG fixtures produced by the
  real mermaid. Error-fallback rendering states.
- Web real render: a case in the browser-guard suite
  (`make test-web-browser`) renders a real diagram end to end; jsdom
  cannot perform mermaid's SVG layout.
- Native real render: manual runbook on device (no native browser gate
  exists), covering render, theme switch, error fallback, and the
  fullscreen viewer.

## Known trade-offs

- Prose on either side of a diagram renders as separate innerHTML blocks,
  so CSS margin collapsing across the boundary is lost. Slice 1 includes a
  visual check of transcript spacing.
- Native text selection no longer spans across a diagram (each prose
  segment is its own native text view). Whole-message selection remains
  available in the "Select text" modal.
- `react-native-webview` is a new native dependency and the mermaid bundle
  adds roughly 500 KB gz inside the app.

## Slices

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
