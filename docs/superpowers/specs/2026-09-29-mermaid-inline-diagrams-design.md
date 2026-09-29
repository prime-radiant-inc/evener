# Mermaid inline diagrams in session messages

Date: 2026-09-29
Status: design approved in chat; revised after two adversarial review rounds, pending re-review

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

Added by review:

4. **Pin the exact mermaid version** in both package.json files and in the
   native asset build. The security posture this spec leans on is
   version-specific behavior (verified identical on 11.17.2 and 12.0.0, but
   exactly the kind of behavior that drifts across minors), and the native
   WebView bakes mermaid into a committed asset where drift is silent. A
   version bump is a security-review event; it also triggers fixture
   regeneration.

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
gallery. The hub CSP allows inline styles, which mermaid's SVG output needs,
and `script-src 'self'`, which covers the Vite lazy chunk.

**Native** (`mobile-native/src/MarkdownResponse.tsx`): messages render
through `react-native-enriched-markdown`, a monolithic native component
(Fabric view over an md4c parser; verified against the packed 1.0.2: props
are the markdown string, styles, and event callbacks, with no JS slot for
custom block rendering). Call sites: `TimelineItem` (transcript),
`TasksSheet`, `ActivityDelegateDetails`. `marked` v18 is already a
dependency (`reader/documentBlocks.ts` uses its lexer). The app has no
WebView or SVG dependency today. `TimelineItem` uses a full-screen `Modal`
pattern (its "Select text" view) and a long-press menu that operates on the
whole markdown string. The transcript list is a virtualized `FlatList`
(`screens.tsx`), so off-window message rows unmount and remount on scroll.
Native has no `closeOpenMarkdown` equivalent: `MarkdownResponse` feeds the
raw string straight to the renderer.

## Core mechanism: segment the token stream, not the parse

Both platforms split a message at top-level fenced code blocks whose
language's first word is `mermaid` (case-insensitive; a capitalized variant
gets a render attempt too, with the same error fallback). Fences nested
inside blockquotes or lists never surface as top-level tokens (verified), so
they keep rendering as literal code.

**Web segments are token slices.** The message is lexed once per render;
the top-level token array is cut at mermaid code tokens; each markdown slice
goes to marked's parser. Link definitions resolve at *lex* time against the
lexer's document-wide registry (verified in marked 18.0.6: the parser never
reads `.links`), so whole-document lexing is what makes slices render
identically to whole-document parsing (12/12 adversarial corpus, zero
divergence, definitions on either side of a diagram, defs nested in
blockquotes and lists). Mermaid segments carry the code token's `text`.

**Native segments are source strings**, because `EnrichedMarkdownText`
takes markdown text and parses each segment standalone. Two consequences,
both verified against the package's own md4c build:

- Reference-style links break when a definition and its use land on
  opposite sides of a diagram (whole parse resolves them; split parses
  leave literal `[text][label]`, both directions). Fix: the splitter
  prepends the whole message's link-definition lines (available from the
  same marked lex) to every markdown segment's source. Definitions render
  as nothing, so prose output is unchanged, and uses resolve. Segment
  sources are then token `raw` concatenation plus those definitions, no
  longer byte-identical to the source; the rendering-equivalence claim is
  what matters.
- `raw` concatenation is byte-exact for LF sources and byte-exact modulo
  marked's line-ending normalization for CRLF sources (marked normalizes
  at lex). The test corpus covers LF, CRLF, tilde fences, indented fences,
  fences in lists and blockquotes, HTML blocks, setext headings, front
  matter, and tables with pipes in code spans.

**Streaming rule.** Web: in live mode the split runs against the
`closeOpenMarkdown` output, and a mermaid fence that only the auto-closer
closed is demoted back to markdown. Identification compares the closed-lex
and real-lex code tokens (the closed lex's mermaid `raw` extends the real
lex's); never a raw-vs-source suffix check, which CRLF normalization breaks
(verified: `source.endsWith(token.raw)` is false for a CRLF fence with
content). Native has no auto-closer: split the real source directly and
apply the same token-comparison demote rule.

**Live-mode performance.** No O(tail) claim survives review: the existing
windowed throttle already falls back to a full parse whenever the tail
window contains a fence marker (verified: FULL_PARSE at every position of a
streaming mermaid block), and the demote rule's second lex adds whole-source
work in exactly that state. This matches today's behavior for any code
fence, so it is not a regression, but the design states it plainly: while a
mermaid block streams, each render pays one full close+lex+segment of the
message. The segmentation result is cached keyed on exact source text;
settled slices memoize and do not re-render. The message-level
link-definition gates of the existing throttle (full-parse fallback when any
`def` token exists) stay at message level, so live cross-segment references
keep resolving exactly as today.

## Web design

- **`widgets/markdown/segments.ts`** (new): the token-slice split above.
  Pure, unit-testable, no renderer changes.
- **`widgets/markdown/index.tsx`**: `Markdown` maps token slices to
  children. Markdown slices render through the current marked → DOMPurify
  → innerHTML path unchanged, one per slice. Mermaid tokens render a new
  `MermaidDiagram` component. All children sit in one wrapper div that
  keeps the existing `ref` contract. Settled markdown slices memoize so
  their DOM (and the portals mounted into it) stays stable during
  streaming.
- **Link walk and entity walk exclusions** (required changes in
  `panes/session/transcript/`): the entity enhancement's skip selector
  gains the `MermaidDiagram` root (today it walks all text under the
  shared root and would rewrite SVG label text nodes containing real
  `dlg_`/`job_`/`watch_` ids, emptying labels and mounting invisible HTML
  spans in SVG). `AgentMarkdown`'s `a[href]` walk gains the same
  exclusion: mermaid at strict DOES emit anchors (authored `<a href>` in
  labels survives, and `click A href "..."` emits `<a xlink:href>`,
  verified in real Chrome), so without the exclusion a model-authored
  relative href would get its href rewritten and an "Open beside" button
  portaled into the diagram.
- **`widgets/mermaid/`** (new): `MermaidDiagram` lazy-loads mermaid with
  `import("mermaid")`, keeping the mermaid payload out of the main bundle
  (the minified closure measures roughly 5.4 MB raw, about 1 MB gz with
  core chunks; per-diagram-type chunks load lazily on top). It renders the
  source to SVG, sanitizes, and sets innerHTML. Theme is built from the
  active color scheme and re-renders on theme change. Render errors show
  the source in the existing CodeBlock styling plus an error note. Click
  opens a fullscreen overlay built on `widgets/dialog/OverlayPanel`
  (verified API fit: FocusScope trap, Escape and scrim close, scrim
  dismiss only on presses that start on the scrim) with pan/zoom (small
  transform handler, no new dependency), a show-source toggle, and a
  copy-source action. The inline SVG is non-interactive
  (`pointer-events: none` on the diagram container), so no in-transcript
  navigation surface exists even if an anchor ever survived
  sanitization.

## Security (web): what mermaid strict actually does, verified in Chrome

Mermaid 11.17.2 and 12.0.0 at `securityLevel: "strict"` *sanitize* labels:
event handlers, `javascript:` URLs, and scripts never reach the raw SVG,
and `securityLevel` is not overridable by `%%{init:}%%` directives
(verified on both versions). But strict does NOT encode or remove benign
authored markup: `<img>`, `<video>`, `<table>`, and `<a href>` in labels,
and ER attributes, reach the SVG as live elements. Two consequences,
verified end to end in real Chrome with the hub CSP replicated:

- A model-authored `<img src="https://evil.example/pixel.png">` label
  makes the browser fetch that URL — once during `mermaid.render` itself
  (its temporary render DOM, before any post-render sanitizer can run) and
  again when the sanitized SVG is inserted. CSP `img-src` allows `https:`,
  so CSP is not the control. The only layer that can stop the render-time
  fetch is mermaid's own internal sanitize, which accepts a
  `dompurifyConfig`. Verified fix:

  ```ts
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: "strict",
    dompurifyConfig: {
      FORBID_TAGS: ["a", "img", "video", "audio", "iframe", "object",
                    "embed", "source", "track", "form", "input"],
    },
  });
  ```

  With this, hostile labels carry no resource-bearing or navigation
  elements in the RAW output, no network requests are issued, and label
  text still renders.
- Post-render, the component sanitizer applies the same FORBID_TAGS on
  top of the SVG config (defense in depth, in case a future mermaid
  version stops honoring `dompurifyConfig`):

  ```ts
  {
    USE_PROFILES: { html: true, svg: true, svgFilters: true },
    ADD_TAGS: ["foreignObject"],
    HTML_INTEGRATION_POINTS: { foreignobject: true },
    ADD_ATTR: ["role"], // keeps role="graphics-document document" for screen readers
    FORBID_TAGS: ["a", "img", "video", "audio", "iframe", "object",
                  "embed", "source", "track", "form", "input"],
  }
  ```

  The html profile plus `foreignObject` integration point are required:
  mermaid emits flowchart, class, state, and ER labels as XHTML inside
  `<foreignObject>`, and an svg-only profile strips every one of them
  (round-1 finding, verified). Sequence/gantt/pie/gitGraph labels are SVG
  `<text>` and survive either way, which would mask the bug in a one-type
  smoke test.

## Native design

- **`markdownSegments.ts`** (new, `mobile-native/src/`): the lexer split
  above producing source-string segments with prepended link definitions,
  pure JS, tested in vitest.
- **`MarkdownResponse`**: maps segments to children. Markdown segments
  render through `EnrichedMarkdownText` as today, and EACH prose segment
  carries the caller's `accessibilityActions`/`onAccessibilityAction`
  (today they attach to the single `EnrichedMarkdownText`; a plain wrapper
  View cannot carry them — RN 0.86.3 defaults `accessible` to false, so
  actions on a wrapper are unreachable on both iOS and Android, and making
  the wrapper accessible would flatten the prose children into one
  element, breaking the documented "markdown view stays VoiceOver's
  element" design). `MermaidDiagram`'s own wrapper gets
  `accessible={true}`, an accessibility label ("Diagram"), and the same
  actions, so a diagram-only message keeps Copy/Quote/Select-text.
  `TimelineItem`'s long-press menu and "Select text" modal operate on the
  full markdown string and are unaffected; "Copy response" still copies
  the whole original message.
- **`MermaidDiagram`** (new): a `react-native-webview` hosting one
  self-contained HTML page with mermaid AND DOMPurify bundled. The page
  initializes mermaid with the same `securityLevel` + `dompurifyConfig`
  as web, sanitizes the SVG with the same config, and posts its rendered
  height over `postMessage`; the component sizes the WebView to fit and
  caches the last posted height per segment source, so a FlatList
  remount shows the right-sized placeholder while the WebView
  re-initializes. Tap opens a fullscreen `Modal` following the
  `TimelineItem` pattern: WebView-native zoom, source toggle, copy via
  the existing `clipboard` helper.
- **Native network lockdown** (the spec's first draft asserted "loads no
  network resources" with no mechanism; react-native-webview 13.16.1
  defaults `originWhitelist` to `["http://*", "https://*"]`): the page
  carries a restrictive CSP meta (`default-src 'none'` with only the
  inline script/style allowances the bundle needs), the WebView sets
  `originWhitelist={["about:blank"]}` and rejects every navigation in
  `onShouldStartLoadWithRequest`, file access stays disabled, and the
  mermaid-source input crosses the bridge as a JSON message, never
  string-interpolated into JS.
- **Long-press dead zone** (documented trade-off): the WebView becomes
  the touch responder for its own region, so the transcript's long-press
  menu does not fire over the diagram itself. The tap-to-fullscreen
  affordance covers the diagram's region; the menu remains reachable over
  the prose and via the fullscreen view's source toggle.
- **WebView page packaging**: one build step produces a single
  self-contained HTML asset (mermaid plus DOMPurify plus a small
  bootstrap: receive source and theme as JSON, render, sanitize, post
  height). The exact packaging (script output committed as an asset,
  loaded via Metro's asset system) is the first native implementation
  task, with a verification step.

## Theming

Mermaid initializes per theme with `themeVariables` derived from each
platform's design tokens, so diagrams match dark and light mode. A color
scheme change re-renders visible diagrams. Verified: hostile
`themeVariables` values are dropped by mermaid, so theme config is not a
CSS-breakout channel.

## Pre-existing bug surfaced by review (fixed as part of this work)

`closeOpenMarkdown`'s boundary predicates reject CR-terminated lines, and
`streaming.test.ts` has no `\r` coverage. The fence regexes
(`streaming.ts:66-67`) are the worst of it (glued backticks on an open CRLF
fence, phantom second fence at EOF), but the same `$`/`[ \t]` anchoring
affects the thematic-break, setext-underline, ATX-heading, and list-marker
predicates too: at CRLF line ends each can kill emphasis closing, leaving a
stray literal `*` glued to the message tail in live preview (all verified
against the repo's code). Fix: extend `\r` tolerance to every end-anchored
line predicate in `closeOpenMarkdown` (fences, thematic breaks, setext
underlines, ATX headings, list markers, and their blockquote-quoted
variants), and add the enumerated CRLF classes to `streaming.test.ts`.
Normalizing line endings at the boundary instead was evaluated and
rejected: it flips the live balance gate (`closeOpenMarkdown(head) ===
head`) to false for every balanced CRLF head, permanently forcing CRLF
streams onto the full-parse path.

## Testing

- Unit (vitest, both platforms): segment splitting across fence forms
  (```` ``` ```` and `~~~`), indented fences, fences nested in
  blockquotes and lists, unterminated fences in live mode, CRLF sources,
  and language-string variants (`mermaid` with trailing whitespace or
  info-string text, case variants, non-matches like `.mermaid` and
  `mermaidjs`). A link definition used on the opposite side of a diagram
  from its definition must resolve, in settled AND live mode. On native
  the vitest target is the splitter (prepended definitions present in each
  markdown segment); md4c resolution itself is covered by the on-device
  runbook, since vitest cannot observe the native parser. CRLF cases for
  `closeOpenMarkdown` alongside its fix, covering every predicate class
  above. The demote rule's CRLF x live x unterminated combination.
- Sanitizer fixtures: committed SVG produced by the real mermaid. A
  committed generator script runs mermaid in jsdom with shims for the six
  missing layout APIs (getBBox, getComputedTextLength, getScreenCTM,
  getPointAtLength, CSSStyleSheet, adoptedStyleSheets; verified sufficient
  for all eight common diagram types on 11.17.2 and 12.0.0 with a direct
  ESM import), one render per process (hostile payloads can hang jsdom
  rendering and poison the process). Regeneration on mermaid version
  bumps. The fixture test asserts label text survives sanitization for
  every fixture type; that assertion would have caught the foreignObject
  bug at implementation time.
- Security tests: a browser-guard case asserts that rendering and
  inserting a diagram with hostile labels (`<img>`, `<a>`,
  `click ... href`) issues zero network requests and produces no anchor or
  resource elements. The native runbook covers the WebView lockdown
  (navigation rejection, CSP meta) on both platforms.
- Web real render: a case in the browser-guard suite
  (`make test-web-browser`) renders a real diagram end to end.
- Native real render: manual runbook on device (no native browser gate
  exists), covering render, theme switch, error fallback, the fullscreen
  viewer, VoiceOver/TalkBack actions on prose-only, mixed, and
  diagram-only messages, and scroll recycling of mounted diagrams.

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
- Each visible diagram is a live WebView on native: the FlatList unmounts
  off-window rows, so scrolling a diagram away and back re-initializes its
  WebView (height-cached placeholder bounds the layout jump; render cost
  is paid again). Several diagrams on screen means several concurrent
  WebViews and one mermaid bundle per page.
- `react-native-webview` is a new native dependency, and the mermaid
  payload is roughly 1 MB gz order of magnitude on web (lazy chunk) and in
  the native asset.

## Slices

0. `closeOpenMarkdown` CRLF predicate fix + tests (pre-existing bug,
   blocks the mermaid live path).
1. Web: segments + inline diagram + error fallback, including the
   `dompurifyConfig` and FORBID_TAGS security config and the walk
   exclusions.
2. Web: fullscreen viewer.
3. Native: WebView page packaging (with lockdown), segments with
   prepended link definitions, inline diagram + error fallback (adds
   `react-native-webview`).
4. Native: fullscreen viewer.

## Non-goals

- Rendering mermaid authored anywhere other than message markdown (doc
  pane and activity rows get it for free through the shared widget, but no
  new call sites are added).
- Diagram editing, live re-layout, or export to PNG/SVG files.
- Server-side rendering.

## Adversarial review provenance

Round 1 (two reviewers, competition rules, every finding verified before
adoption): the foreignObject sanitizer bug, cross-segment link-definition
regression, entity-walk exclusion, CRLF byte-exactness correction,
whole-source split cost, spacing mechanism, native dead zone, VoiceOver
carrier gap, fixture provenance, and the pre-existing closeOpenMarkdown
CRLF fence bug.

Round 2 (two fresh reviewers on the round-1 revision, same rules): mermaid
strict does not encode authored markup and render-time fetches need the
`dompurifyConfig` layer; anchors survive strict and would meet
AgentMarkdown's link walk; the native page needed its own sanitizer and
real network lockdown (the WebView default whitelist allows http/https);
link definitions resolve at lex time and the live tail re-lex loses them;
the CRLF fix must cover every boundary predicate, not just fences; the
demote rule's identification is CRLF-fragile as a suffix check; native
link-definition loss confirmed against the package's own md4c build with
the prepend-definitions fix; wrapper-View accessibilityActions are
unreachable in RN; native live streaming needed its own statement;
WebView lifecycle in the FlatList; version pinning; and the true bundle
size. One round-2 finding was invalidated in adjudication: the jsdom shim
list was reported incomplete (pie needing `structuredClone`), but that
failure was an artifact of an eval-a-bundle harness; direct ESM import
with the six shims renders all eight diagram types on both mermaid
versions.

Verified-as-sound and kept: token-slice settled rendering identical to
whole-doc parsing (12/12 corpus), nested fences never surfacing, the CSP's
sufficiency for the lazy chunk and inline styles, no JS slot in
react-native-enriched-markdown, react-native-webview Expo support at SDK
57, OverlayPanel's fit, strict mode's handler/script stripping and its
robustness to `%%{init}%%` override, themeVariables not being a breakout
channel, and label-survival being assertable in structure for all eight
diagram types.
