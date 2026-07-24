# M8 Periphery — Behavior-Parity Checklist

Scope: doc/image viewer, standalone `/thread/{ref}` document view, `panes.js`'s multi-pane UX, PWA
manifest handling, and the `/auth` cookie flow — the four areas named in
`docs/superpowers/specs/2026-07-20-webui-workspace-shell-rewrite-design.md` §13's milestone line:
**"M8 periphery: doc viewer, `/thread/{ref}` single-pane mode, PWA, popout windows."** ("Popout
windows" is called out separately below — it is a *new* dockview capability with no legacy
behavior to port; see the note at the end of §3.)

That design doc's §5 "Global" feature-parity paragraph is the authoritative spec these items are
checked against: *"PWA manifest/install; `/auth?token=` cookie flow; doc/image viewer (`/doc/file`,
`/doc/image`) scoped to a session cwd; standalone `/thread/{ref}` document view."* Its §6.7 confirms
the route contract survives verbatim (`/thread/{ref}`, `/doc/file`, `/doc/image`, `/auth` are all
listed as preserved routes) but changes *shape*: **"`/thread/{ref}` renders the app in single-pane
mode (also the share-link target)"** — i.e. the standalone chrome-less HTML document
(`templates/thread.html`) that §2 below describes is replaced by the same React app rendering in a
restricted layout mode, not by a second document. §6.4 lists **"doc viewer"** as a first-class
dockview pane *type*, and §7's server-side change-list keeps `/doc` and image routes reachable
through the Vite dev proxy — read together, this strongly suggests `doc_serve.go`'s route/guard
contract (§1.1–1.2 below) survives as the new pane's data layer while its HTML-presentation
functions (`writeDocPage`/`writeDocMarkdownPage`, §1.3–1.5) are what actually gets replaced; this
checklist keeps those two kinds of behavior in separate subsections so that split is easy to act on.
§6.8 says the PWA manifest mechanism is **"kept"** (assets re-generated to new brand tokens) and §10
confirms `templates/` (all, including `thread.html`) and `assets/*.js` (all, including `panes.js`)
are deleted in the final M10 wave — so everything this document describes is, by design, load-bearing
only until M8/M9 land its replacement and M10 removes the original.

Check an item once the new dockview/React implementation reproduces it (or the team has made a
deliberate, documented decision not to).

- **Source:** `cmd/serf-hub` in this worktree, branch `worktree-webui-workspace-shell`, commit
  `8974a0a679d2cc8d6883650d17ee4d15186f79d4` (2026-07-20) — the 5 primary files below were last
  touched at `9bae74070cd10979c32a5545fc0426642c305f1b` (2026-07-20), an ancestor of the cited HEAD.
  File:line references are exact as of this commit; re-verify after further edits before relying on
  them for automation.
- **Format:** one checkbox per discrete, independently-verifiable behavior, `file:line` pointing at
  the CURRENT (legacy) implementation. Paths are relative to the repo root unless noted.

## Files read

Primary (as requested), read in full:
- `cmd/serf-hub/doc_serve.go` (241 lines)
- `cmd/serf-hub/web_workspace.go` (746 lines — focused on `handleThreadDocument`/
  `renderThreadDocument` and the data-flow feeding them; the send/fork/aside/session-action POST
  handlers in the same file are named for context but not itemized here, they're M5/M10 territory)
- `cmd/serf-hub/templates/thread.html` (55 lines)
- `cmd/serf-hub/assets/panes.js` (461 lines)
- `cmd/serf-hub/web.go` (445 lines)
- `cmd/serf-hub/internal/hubedge/auth_token.go` (221 lines)
- `cmd/serf-hub/assets/manifest.webmanifest` (17 lines)

Pulled in because they're load-bearing for the requested behaviors (not optional reading — the
primary files call straight into them, or the design doc's M8 framing depends on them):
- `cmd/serf-hub/internal/fspaths/paths.go` (151 lines, read in full) — `ResolveInRoot`'s exact
  two-layer containment algorithm backing `doc_serve.go`'s path guards.
- `cmd/serf-hub/output_images.go` (partial: `supportedOutputImageMedia`, `readOutputImageFile`,
  `outputImageSHA`, `outputImageMaxBytes`, the `resolveOutputImageFile`→`/doc/image` URL builder) —
  the media-type/size gate `handleDocImage` delegates to.
- `cmd/serf-hub/internal/httpsec/httpsec.go` (38 lines, read in full) — the CSP wrapping every route
  in scope, including the `frame-ancestors` rule `panes.js`'s iframes depend on.
- `cmd/serf-hub/webnext.go` (58 lines, read in full) and `cmd/serf-hub/embed.go` (77 lines, read in
  full) — `newWebEnabled()`/`serveSPAIndex` (the SPA cutover switch each route either checks or
  conspicuously doesn't) and the asset/template embed plumbing.
- `cmd/serf-hub/templates/app.html` (127 lines, read in full) — the host shell `panes.js` and the
  full (non-thread-document) workspace render into; needed to diff `thread.html`'s script subset and
  to confirm `#side-panes`/`#pane-splitter` sit outside `#workspace`.
- `cmd/serf-hub/templates/partials/workspace.html` (116 lines, read in full) and
  `cmd/serf-hub/templates/partials/input_strip.html` (16 lines, read in full) — the shared partial
  both `/s/{id}` and `/thread/{ref}` render, and its `ThreadDocumentMode` conditionals.
- `cmd/serf-hub/web_types.go` (`WorkspaceData` struct, lines ~161-223) and
  `cmd/serf-hub/web_api_tree.go` (`apiSessionState`, lines 845-903) — the data feeding the
  thread-document fallback and its recurring status poll.
- `cmd/serf-hub/assets/renderer.js` (targeted reads: `isInPane`/`openBeside`/`bindPaneParentLinks`/
  `autoOpenObservers` ~120-405; `fileOpenBesideSpec`/`attachFileOpenBeside`/`attachImageOpenBeside`
  ~2160-2292; `makeOpenBesideButton`/`applyJobRefTarget` ~3980-4032; `bindSubagentEscapeToParent`
  ~6922-6946 — NOT read in full at 7090 lines) and `cmd/serf-hub/assets/sidebar.js` (targeted:
  the "Open beside" context-menu item and subagent-row `open-beside-btn` handler, ~276, ~1136-1160 —
  NOT read in full at 1390 lines) — every producer that calls into the `panes.js`/`openBeside`
  contract.
- `cmd/serf-hub/assets/theme.js` (12 lines, read in full) — to check for (and rule out) any
  cross-frame theme-sync mechanism the iframe-pane model might depend on.
- `cmd/serf-hub/jstest/test-thread-document-bridge.js` (96 lines, read in full) — the executable spec
  for the postMessage bridge; corroborates §3.5 line-for-line.
- `cmd/serf-hub/web_test.go` (targeted: every `TestWeb_ThreadDocument_*` function, lines 2461-2760)
  and `cmd/serf-hub/doc_serve_test.go` (targeted: every `TestDoc*` function) — corroborating evidence
  for §1 and §2; cited inline as "verified by `Test...`" where a specific test pins the behavior.
- `cmd/serf-hub/internal/hubedge/auth_token_test.go` — not read in full, but its 28 `check*`
  function names were enumerated and cross-checked against every §5 claim (all matched).
- `docs/superpowers/specs/2026-07-20-webui-workspace-shell-rewrite-design.md` (324 lines, read in
  full) — the M8 mandate and target-architecture context quoted above and in §3/§4's framing notes.

Not read (out of scope for this pass — flag before assuming): `cmd/serf-hub/frontend/src/**` (the
in-progress new SPA itself — this checklist deliberately only describes the OLD implementation so it
can be diffed against whatever the new pass produces); `cmd/serf-hub/assets/style.css` beyond two
targeted greps (`--side-panes-w`/`--sidebar-w` defaults, `.subagent-parent-up` rules);
`cmd/serf-hub/assets/renderer-tools.js` / `renderer-panels.js` (grepped for `doc/file`/`doc/image`
hits, found none — the affordances live in `renderer.js` itself); `cmd/serf-hub/main.go` (where
`LoadOrCreateAuthToken`/`AuthURLFor` actually get called and printed); `output_images.go`'s
`handleSessionImage` (`/s/{id}/images/{sha}` handler — named only because `panes.js`'s safe-href
check interacts with its URL shape, §3.8).

## Cross-cutting findings

The highest-signal, most easily-missed findings from this pass. Each also appears as a checkbox in
its proper section below — this is a highlight reel, not a separate obligation.

1. **`/doc/file` and `/doc/image` are the only two of these five areas NOT gated behind
   `SERF_HUB_WEB=new`.** Every other page route (`/`, `/s/{id}` bare, `/thread/{ref}`) already forks
   to `serveSPAIndex` when the new SPA is enabled; `doc_serve.go` has no `newWebEnabled()` call at
   all. If M8's dockview doc-viewer pane is meant to be live under the flag today, the routing fork
   still needs to be added. See §1.6.
2. **`/thread/{ref}` never 404s for a syntactically valid ref, known or not** — an unresolvable
   session renders a synthesized idle placeholder instead (`web_workspace.go:175-186`, verified by
   `TestWeb_ThreadDocument_RouteEncoding`). But the recurring `/state` poll behind that same
   placeholder discards the resolved/not-resolved flag and unconditionally re-swaps the header title
   from an empty `SessionDetail{}` — so the placeholder's title visibly blanks within moments of
   load. Nothing currently tests this combination; see §2.2.
3. **The subagent parent-breadcrumb's postMessage bridge (`bindPaneParentLinks`,
   `[data-open-parent-beside]`) is unit-tested in isolation but has no reachable trigger in
   production markup** — the one place the breadcrumb renders never emits that attribute, and the
   block is unconditionally suppressed in exactly the mode (`ThreadDocumentMode`) where the bridge
   would matter. See §3.8.
4. **"Open beside" on an image can silently fail depending on where it's clicked from.** A
   sha-addressed `/s/{id}/images/{sha}` href opens fine from the un-framed top-level workspace
   (`SerfPanes.open()` applies no href allowlist there), but the SAME control clicked from inside an
   already-open pane is dropped silently by the postMessage bridge, whose `isPaneSafeHref` only
   allows `/thread/`/`/doc/` prefixes. See §3.8.
5. **Panes are iframes with no live theme sync.** No `storage` event listener exists anywhere in the
   codebase; a theme change after a pane is already open does not reach that pane until it reloads.
   Irrelevant once panes stop being separate documents — worth confirming the new implementation
   doesn't reintroduce an equivalent boundary. See §3.8.
6. **`ShowSidebarToggle` is a dead field** — set to `true`/`false` on `WorkspaceData` by the two
   render paths but read by no template or script anywhere in the repo. See §2.2.
7. **The 401 auth wall and its self-heal redirect carry no CSP header**, because `AuthGuard` sits
   outside `CSPMiddleware` in the handler-wrapping order and returns before calling `next` on both
   of those paths; every response that reaches the app (including a wrong-token hit on `/auth`
   itself, whose 401 comes from inside `next`) does carry it. Derived directly from the composition
   in `web.go:216-222`; no test in this repo exercises the CSP-on-401 case specifically. See §5.3.
8. **The manifest is technically double-served.** `assetsRoot()` embeds the whole `assets/` tree
   including `manifest.webmanifest`, so `/assets/manifest.webmanifest` is also reachable (auth-gated
   like any other asset) and returns the raw, un-rewritten file (`start_url` still `"/"`, no token) —
   a second, divergent copy alongside the real `/manifest.webmanifest`. See §4.1.
9. **Text files over 512 KiB truncate silently** — no "truncated" notice is ever shown, unlike the
   binary path's explicit "not shown" notice. See §1.3.
10. **`popout windows`, named in the M8 milestone line, has no legacy equivalent to check parity
    against** — the only existing `window.open()` calls in the periphery scope are `search.js`'s
    plain `_blank` new-tab open of a session and two unrelated OAuth-flow popups in
    `credentials.html`; neither is a dockable "popout" in the sense §6.4 of the design doc means.
    Nothing to port here; flagging so it isn't mistaken for an oversight.

---

## Summary table

| # | Area | Routes | Primary file(s) | Gated by `SERF_HUB_WEB=new`? |
|---|---|---|---|---|
| 1 | Doc/image viewer | `GET /doc/file`, `GET /doc/image` | `doc_serve.go` | **No** — always legacy |
| 2 | Standalone thread doc | `GET /thread/{ref}` | `web_workspace.go`, `templates/thread.html` | Yes (`web_workspace.go:158`) |
| 3 | Multi-pane UX | (client-side only; panes load `/thread/*`, `/doc/*`) | `assets/panes.js` | N/A — only runs inside the legacy shell (`templates/app.html:89`) |
| 4 | PWA manifest | `GET /manifest.webmanifest` | `web.go` (`handleManifest`), `assets/manifest.webmanifest` | No — always this handler |
| 5 | Auth cookie flow | `GET /auth`, guard on everything else | `internal/hubedge/auth_token.go` | No — cross-cutting infra, forks nothing |

---

## 1. Doc/image viewer (`doc_serve.go`)

### 1.1 Routes & request contract

- [ ] `GET /doc/file` and `GET /doc/image` are registered directly on the top-level mux, not under
      `/_partials/` — they are standalone document routes an iframe navigates to directly, so they
      cannot require `HX-Request`/htmx-only headers — `web.go:187-188`, doc comment
      `doc_serve.go:23-27`
- [ ] Both handlers require `GET`; any other method is `405 GET required` —
      `doc_serve.go:34-37` (file), `doc_serve.go:87-90` (image)
- [ ] Both require non-empty `session` and `path` query params; either missing yields `404`, not
      `400` — `doc_serve.go:38-43` (file), `doc_serve.go:91-96` (image)
- [ ] `session` is normalized through `canonicalRouteID` before lookup (strips a `local:` source
      prefix so both bare IDs and `local:`-prefixed refs resolve the same session) —
      `doc_serve.go:38`, `doc_serve.go:91`, `web.go:369-375`
- [ ] Only LOCAL sessions are servable — a remote/codex ref (`isLocalRouteID` false) always `404`s;
      there is no path guard to bypass, the session simply never resolves to a cwd —
      `doc_serve.go:131-136`; verified by `TestDocFile_NonLocalSession404`
      (`doc_serve_test.go:281-289`)
- [ ] cwd resolution checks the ended/past-index entry FIRST, live roster second — an ended
      session's doc pane still works (reading from its last-known working dir) as long as the files
      still exist on disk — `doc_serve.go:138-155`
- [ ] Unknown session id (neither past nor live roster has it) → `404` —
      `doc_serve.go:45-49` (file), `doc_serve.go:98-102` (image); verified by
      `TestDocFile_UnknownSession404` (`doc_serve_test.go:273-280`)
- [ ] Every response (success or otherwise) flows through the same global middleware chain as any
      other route — CSP + AuthGuard + optional request recorder — nothing in `doc_serve.go` special-
      cases auth; a same-origin iframe navigation carries the cookie automatically —
      `web.go:216-222`

### 1.2 Path & session containment guards

- [ ] Containment is delegated to `fspaths.ResolveInRoot(cwd, rel)`, called identically from both
      handlers — `doc_serve.go:51`, `doc_serve.go:104`
- [ ] `ResolveInRoot` is a two-layer, independently-sufficient defense: (1) lexical — the cleaned
      joined path must have the cleaned root as a prefix, catching `../` traversal and absolute-path
      escapes; (2) symlink-resolved — the target is `EvalSymlinks`-resolved and re-checked against
      the symlink-resolved root, catching an in-root symlink that points outside —
      `fspaths/paths.go:66-112`
- [ ] An absolute `rel` is only accepted if it is already lexically inside the (cleaned) root; it is
      never treated as "escape the root on purpose" — `fspaths/paths.go:91-96`; verified by
      `TestDocFile_RejectsAbsolutePathEscape` (`doc_serve_test.go:211-224`)
- [ ] A path-escape (either layer) → `403 Forbidden`; any OTHER resolve failure (nonexistent path,
      broken symlink) → `404 Not Found` — a deliberate distinction per the handler's own comment
      ("403 for an escape attempt; 404 for a missing file") — `doc_serve.go:53-58` (file),
      `doc_serve.go:105-109` (image); verified by `TestDocFile_RejectsTraversalDotDot`
      (`doc_serve_test.go:192-209`), `TestDocFile_RejectsSymlinkEscape`
      (`doc_serve_test.go:226-246`), `TestDocImageRejectsTraversalAndSVG`'s traversal case
      (`doc_serve_test.go:127-135`, expects exactly `403`)
- [ ] Non-regular files (directories, devices, etc.) are refused at the read step
      (`info.Mode().IsRegular()` check) and surface as a generic `404`, not a distinct error —
      `doc_serve.go:166-168`

### 1.3 Content-mode selection & rendering

- [ ] `/doc/file` responses are always `Content-Type: text/html; charset=utf-8` regardless of the
      underlying file's real type — `doc_serve.go:70`
- [ ] Binary detection: a NUL byte anywhere in the first 8 KiB of content marks the file binary —
      `doc_serve.go:182-191`
- [ ] Binary detection is checked BEFORE the markdown-extension check — a `.md` file containing a
      NUL byte in its first 8 KiB renders the binary notice, never markdown —
      `doc_serve.go:71-76` (ordering)
- [ ] Binary notice literal text is exactly `binary file — {name} ('{size}') not shown`, including
      the stray single quotes around `{size}` (e.g. `binary file — blob.bin ('2 KiB') not shown`) —
      `doc_serve.go:72`; verified loosely by `TestDocFile_BinaryNotice` (`doc_serve_test.go:290-303`,
      only checks the substring "binary" case-insensitively, not the exact string)
- [ ] `formatDocBytes` renders sizes as integer-truncated (not rounded) `N MiB`/`N KiB`/`N B` — e.g.
      a 1.99 MiB file displays as `1 MiB` — `doc_serve.go:193-202`
- [ ] Text/markdown content is capped at 512 KiB (`docFileMaxBytes`), read via a single `Read()` call
      into a fixed buffer (not `io.ReadFull`/streaming) — `doc_serve.go:18-21`, `doc_serve.go:174-179`
- [ ] A file over 512 KiB is silently truncated to the cap with NO truncation notice shown to the
      client — contrast the binary path's explicit notice; nothing in the current UI tells the user
      they're seeing a partial file — `doc_serve.go:18-21`, `doc_serve.go:80` (render call has no
      truncated-flag)
- [ ] Markdown mode is selected purely by case-insensitive `.md`/`.markdown` file extension — no
      content sniffing, no MIME detection — `doc_serve.go:76`
- [ ] Every other extension (including none) renders inside `<pre class="doc-pre">`, fully
      HTML-escaped via `htmlEscape` — `doc_serve.go:80`; verified by
      `TestDocFile_ServesTextFileEscaped` (`doc_serve_test.go:144-164`, confirms a `<script>` payload
      round-trips as `&lt;script&gt;`, never raw)

### 1.4 Markdown mode

- [ ] `marked.min.js` is loaded ONLY on the markdown-rendering path — the plain-text and binary
      pages load no markdown library at all — `doc_serve.go:230` vs. `doc_serve.go:207-216`
- [ ] The raw markdown source is server-escaped (`htmlEscape`) and embedded as the text content of a
      hidden `<div id="doc-src">` — never as a `<script>` block, per the handler's own comment: a
      `<script>` body is raw text the browser never entity-decodes, so an escaped sequence would
      survive literally and corrupt the markdown — `doc_serve.go:218-224`, `doc_serve.go:234`
- [ ] Client-side, a small inline IIFE reads `#doc-src`'s `.textContent` (decoding the HTML entities
      back to raw markdown) and calls `window.marked.parse(raw)`, assigning the result directly to
      `.doc-markdown`'s `innerHTML` — `doc_serve.go:235-238`
- [ ] No client-side sanitization pass exists after `marked.parse()` — its output goes straight into
      `innerHTML` with nothing else in between; a markdown file containing raw HTML is rendered
      as-is (subject to whatever `marked`'s own default HTML-passthrough behavior is) —
      `doc_serve.go:236-238` (the two lines are the entire rendering path — no sanitizer call exists
      anywhere in this file)
- [ ] Graceful degrade: if `window.marked`/`.parse` is unavailable, falls back to a plain `<pre>` of
      the raw markdown text instead of parsed HTML — `doc_serve.go:236-238`
- [ ] Markdown page requires the embedded source to actually be present (not just the `marked`/
      `doc-markdown` scaffolding) — verified by `TestDocFile_RendersMarkdown`
      (`doc_serve_test.go:166-190`, explicitly checks for the literal `# Title` heading text in the
      response body, not just supporting markup)

### 1.5 Image viewer (`/doc/image`) specifics

- [ ] Allowed media types are exactly `image/png`, `image/jpeg`, `image/gif`, `image/webp` via
      `http.DetectContentType`, plus a RIFF/WEBP byte-signature fallback for small WebP samples
      `DetectContentType` misclassifies as octet-stream — `output_images.go:158-173`
- [ ] SVG is deliberately excluded (an XSS vector via embedded `<script>`/event handlers, not an
      oversight) — a `.svg` file with a valid SVG body still `404`s, distinct from the containment
      `403` — `output_images.go:158-173`; verified by `TestDocImageRejectsTraversalAndSVG`'s SVG
      case (`doc_serve_test.go:136-141`, expects exactly `404`)
- [ ] Size cap is 8 MiB (`outputImageMaxBytes`), 16x the text-file cap — an oversized image `404`s
      rather than truncating/streaming — `output_images.go:28`, `output_images.go:208-218`
- [ ] Success headers: `Content-Type` = the sniffed media type, `Cache-Control: private, max-age=60`,
      `ETag` = double-quoted lowercase-hex SHA-256 of the raw bytes — `doc_serve.go:125-128`,
      `output_images.go:220-223`
- [ ] `/doc/image` shares `doc_serve.go`'s containment guard verbatim (same `ResolveInRoot` call,
      same `session`/`path` param contract, same local-only/unknown-session/escape-vs-missing status
      codes) — the only divergence from `/doc/file` is the media-type/size gate replacing the
      binary/markdown/plain-text content-mode selection — `doc_serve.go:83-129`
- [ ] Server-side, `appwire.OutputImage.URL` for a tool-generated image is built as
      `/doc/image?session={id}&path={rel}` with both components query-escaped — a second, distinct
      producer of `/doc/image` hrefs beyond the client-side ones panes.js/renderer.js construct —
      `output_images.go:202`

### 1.6 Page chrome & SPA-cutover status

- [ ] Doc pages carry NO favicon `<link>` at all — differs from both `thread.html` (inline SVG
      data-URI favicon) and `app.html` (PNG touch icon); the browser shows its default icon —
      `doc_serve.go:207-216`, `doc_serve.go:225-240` (neither emits a `<link rel="icon">`)
- [ ] Doc pages load NO Google Fonts preconnect/stylesheet — visually inconsistent with the rest of
      the app, which uses Hanken Grotesk/JetBrains Mono — same two functions, no
      `fonts.googleapis.com` reference anywhere in their output
- [ ] Doc pages' theme-boot IIFE differs from `app.html`'s/`thread.html`'s: wrapped in `try/catch`
      and applies ANY truthy `localStorage` value unchecked, vs. the other two's uncaught,
      enum-validated (`"light"`/`"dark"` only) form — `doc_serve.go:211`, `doc_serve.go:229` vs.
      `templates/thread.html:9-16`, `templates/app.html:17-24`
- [ ] Doc pages load the stylesheet `<link>` BEFORE the theme-boot `<script>` — the opposite order
      from `app.html`/`thread.html` (theme script first, stylesheet second); both still resolve
      before first paint since a same-document `<script>` after a `<link rel=stylesheet>` blocks on
      it, so this is a documented ordering difference, not a confirmed visible flash —
      `doc_serve.go:208-211`
- [ ] Doc pages load ZERO application JavaScript beyond the theme IIFE and (conditionally)
      `marked.min.js` — no htmx, no `renderer.js`, no `panes.js` — a doc pane cannot itself request
      further nested panes; "open beside" only ever originates from a `/thread/*` pane or the main
      workspace — `doc_serve.go:207-216`, `doc_serve.go:225-240` (complete script inventory)
- [ ] `/doc/file` and `/doc/image` are NOT gated behind `SERF_HUB_WEB=new` — neither handler calls
      `newWebEnabled()`; both always serve this legacy HTML regardless of the flag, unlike `/`,
      bare `/s/{id}`, and `/thread/{ref}` which all fork to `serveSPAIndex` —
      no `newWebEnabled()` call anywhere in `doc_serve.go`; contrast `web.go:226`,
      `web_workspace.go:45`, `web_workspace.go:158` (the complete set of call sites, confirmed by
      repo-wide grep)

---

## 2. Standalone `/thread/{ref}` document view

Per the design doc §6.7, this route's *contract* is preserved but its *shape* changes: the new SPA
renders `/thread/{ref}` as itself in "single-pane mode," not as a second HTML document. Everything
below that describes chrome SUPPRESSED in `ThreadDocumentMode` is the concrete list of what that
single-pane mode needs to replicate through its own layout state.

### 2.1 Route & handler contract

- [ ] `GET /thread/{ref}` is registered at `/thread/` (prefix match) → `handleThreadDocument` —
      `web.go:182`
- [ ] When `SERF_HUB_WEB=new`, the route defers entirely to `serveSPAIndex` before any of the logic
      below runs — `web_workspace.go:158-161`
- [ ] Requires `GET`; any other method is `405 GET required` — `web_workspace.go:162-165`
- [ ] `id` must be non-empty AND contain no further `/` — unlike `/s/{id}/{sub}`, this route has no
      sub-path concept at all; `/thread/{ref}/anything` is `404` — `web_workspace.go:166-169`
- [ ] `id` is passed through `canonicalRouteID` before lookup, exactly like the doc-pane routes —
      `web_workspace.go:171`
- [ ] `%3A`-encoded refs (e.g. `/thread/local%3A{id}`) decode correctly before the `local:` prefix
      strip, resolving the same session a bare-ID request would — verified by
      `TestWeb_ThreadDocument_RouteEncoding/encoded-local-ref` (`web_test.go:2528-2541`)
- [ ] The global CSP header (`frame-ancestors 'self'` etc.) applies identically to `/thread/`
      responses as to every other route — verified by `TestWeb_ThreadDocument_SecurityHeaders`
      (`web_test.go:2567-2583`)

### 2.2 Not-found / fallback synthesis

- [ ] `/thread/{ref}` ALWAYS returns `200` for any syntactically valid ref, local or remote, known or
      unresolvable — this is a deliberate divergence from `renderWorkspacePartial`
      (`/_partials/s/{id}/workspace`), which `404`s on the identical not-found condition —
      `web_workspace.go:175-186` vs. `web_workspace.go:130-135`; verified by
      `TestWeb_ThreadDocument_RouteEncoding/encoded-remote-ref` and `/bare-session`
      (`web_test.go:2543-2564`, both expect `200` with no source configured/session registered)
- [ ] The synthetic fallback `WorkspaceData` sets `Title` = the raw route id verbatim, `State` =
      `"idle"`, `SourceLabel` derived from the ref text, `Capabilities` from
      `apiSessionCapabilities(id, false)` — `web_workspace.go:178-185`
- [ ] `ShowSidebarToggle=false` and `ThreadDocumentMode=true` are set unconditionally for this route
      (contrast `renderWorkspacePartial`'s `true`/`false`) — `web_workspace.go:187-188` vs.
      `web_workspace.go:136-137`
- [ ] `ShowSidebarToggle` is currently a DEAD field: set by both render paths but read by no template
      or script in the repo (grep-verified across `templates/` and `assets/*.js`) — the field exists
      but has zero effect on rendered output today — `web_types.go:192` (declaration),
      `web_workspace.go:136`, `web_workspace.go:187` (only two writers)
- [ ] The fallback's recurring status poll (`renderInputStrip`, fired by
      `hx-trigger="load, ..., every 30s"` almost immediately after initial paint) discards the
      found/not-found boolean from `apiSessionState` and unconditionally sets `OOBTitle=true` — for
      an unresolved ref, `apiSessionState` returns a zero-value `hubapi.SessionDetail{}` (empty
      `Title`), so the header title visibly blanks shortly after the initial fallback-titled render —
      `web_workspace.go:710`, `web_workspace.go:713-715`, `web_api_tree.go:846-849`,
      `templates/partials/input_strip.html:2` (the unconditional OOB swap target); NOT covered by
      any existing test — verified by direct code tracing across these 4 sites in this pass

### 2.3 Document chrome

- [ ] Full standalone `<!DOCTYPE html>` document with its own `<head>`/`<body>` — not an htmx
      fragment — `templates/thread.html:1-55`; required markup verified by
      `TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument`
      (`web_test.go:2477-2488`: must contain `<!DOCTYPE html>`, `body.thread-document`,
      `#conversation`, `data-input-form`, `renderer.js`, `appwire.js`)
- [ ] Title format is `"{Title} · serf thread"` when a title is set, else bare `"serf thread"` —
      differs from `app.html`'s fixed `"serf hub"` — `templates/thread.html:7`
- [ ] Own inline SVG data-URI favicon (a filled grey circle) — not the PNG touch-icon `app.html`
      uses — `templates/thread.html:8`
- [ ] Own theme-boot IIFE — same enum-validated (`light`/`dark` only), uncaught form as `app.html`'s
      (differs from the doc-pane pages' try/caught, unchecked form — see §1.6) —
      `templates/thread.html:9-16`
- [ ] Loads Google Fonts (Hanken Grotesk/JetBrains Mono) identically to `app.html` — visually
      consistent with the main app, unlike doc panes — `templates/thread.html:17-19`
- [ ] `<body class="thread-document" data-thread-document="true">` — a dedicated marker
      class+attribute not present on `app.html`'s plain `<body class="app">` —
      `templates/thread.html:22`
- [ ] `#workspace` wraps the exact same `{{template "workspace" .}}` partial the regular session
      route renders — content structure is fully shared; only surrounding chrome differs —
      `templates/thread.html:23-25`
- [ ] Own `#toast-region` — `SerfToast` is self-contained per document, so a pane's toasts never
      appear in the parent shell — `templates/thread.html:26`
- [ ] Forbidden markup (tested): `#sidebar`, `#search-dialog`, `[data-sidebar-toggle]`,
      `.settings-link` must NOT appear anywhere in the rendered document — verified by
      `TestWeb_ThreadDocument_DirectGet_ServesChromeLessThreadDocument`
      (`web_test.go:2489-2498`)

### 2.4 Script subset (vs. `app.html`)

- [ ] Loads 16 scripts total vs. `app.html`'s 33 — `templates/thread.html:27-42` vs.
      `templates/app.html:79-111`
- [ ] Present in both: `htmx.min.js`, `toast.js`, `thread-state.js`, `skeleton.js`, `appwire.js`,
      `focus-trap.js`, `icons.js`, `theme.js`, `composer-attachments.js`, `marked.min.js`,
      `diagnostics.js`, `pending.js`, `renderer-format.js`, `renderer-tools.js`,
      `renderer-panels.js`, `renderer.js` — same diff as above
- [ ] Absent from `thread.html` (present only in `app.html`): `launchconfig.js`, `plugins.js`,
      `sidebar.js`, `panes.js`, `notifications.js`, `search.js`, all 5 `settings-*.js` files,
      `model-display.js`, `model-switch.js`, `drafts.js`, `dir-picker.js`, `spawn.js`,
      `settings-pickers.js` — none of search/palette, settings, spawn, model-switch, drafts, or OS
      notification UI is available inside a thread document — same diff as above
- [ ] `panes.js` specifically is never loaded inside `thread.html` — it only runs in the host shell
      (`templates/app.html:89`), consistent with `panes.js`'s own file-header description of itself
      as "host-side" — `panes.js:1-2`
- [ ] The inline `htmx:responseError`/`htmx:sendError` → toast handlers are duplicated verbatim
      between the two templates rather than factored into a shared script —
      `templates/thread.html:43-52` vs. `templates/app.html:112-124`

### 2.5 `ThreadDocumentMode`-suppressed content

- [ ] The subagent parent-breadcrumb banner (`.subagent-parent-banner`, incl. the "Esc to parent"
      hint) is suppressed entirely in `ThreadDocumentMode` — deliberate and tested, not merely a
      side-effect of the fallback path: it's suppressed even when `ParentRouteID`/`ParentTitle` ARE
      populated — `templates/partials/workspace.html:6`; verified by
      `TestWeb_ThreadDocument_CompactsSubagentChromeAndFooter` (`web_test.go:2607-2609`, forbids
      `.subagent-parent-banner` and `subagent-parent-esc` even with `ParentRouteID: "local:parent"`
      set in the test fixture)
- [ ] Location telemetry (branch/worktree/cwd chips in the status rail) is suppressed in
      `ThreadDocumentMode` — `templates/partials/input_strip.html:5-11`; verified by the same test
      (`web_test.go:2610-2612`) and by `TestWeb_ThreadDocument_StateRefreshPreservesCompactLocationMode`
      (`web_test.go:2666-2669`)
- [ ] A `"turns"` status item (`.status-item.turns`) is also asserted absent in `ThreadDocumentMode`
      by the same test (`web_test.go:2613`) — no corresponding render site was found in
      `input_strip.html` during this pass; treat this as a contract to preserve regardless of exactly
      where it's ordinarily produced (flagged, not fully traced — see Open Questions)
- [ ] Required-present markup in `ThreadDocumentMode`: `.workspace-title-row`, `.message-input`,
      `[data-task-status-text]`, `.status-badge`, `.input-telemetry[data-input-telemetry]` — the
      composer and live status badge remain fully functional; only lineage/location chrome is
      stripped — `web_test.go:2619-2624`
- [ ] Send/steer/interrupt controls remain live and capability-gated identically to the full
      workspace — same shared `workspace` template, same `Capabilities`-driven `disabled` attributes,
      no `ThreadDocumentMode`-specific composer restriction —
      `templates/partials/workspace.html:86-93`; verified by
      `TestWeb_ThreadDocument_ComposerControlsLiveInsideInputCard` (`web_test.go:2704-2760`)

### 2.6 Compact-mode persistence across polls

- [ ] The `?thread_document=1` query flag is appended to the recurring `/_partials/s/{id}/state`
      poll URL only when `.ThreadDocumentMode` is true at render time —
      `templates/partials/workspace.html:102`; the poll itself fires on `load` (immediately) AND
      every 30s — `templates/partials/workspace.html:103`
- [ ] The compact/full choice is entirely REQUEST-PARAM-DRIVEN, not session-sticky: hitting
      `/_partials/s/{id}/state?thread_document=1` directly for a given session returns compact
      telemetry, while hitting the bare `/_partials/s/{id}/state` (no query) for THE SAME session in
      the same test returns full location telemetry — `web_workspace.go:713`
      (`r.URL.Query().Get("thread_document") == "1"`); verified end-to-end by
      `TestWeb_ThreadDocument_StateRefreshPreservesCompactLocationMode`
      (`web_test.go:2635-2702`, three sequential requests: initial thread-doc render, explicit
      `?thread_document=1` refresh, and a bare refresh for the same id — only the last shows
      branch/worktree/cwd)
- [ ] Every `/state` response — from the very first `hx-trigger="load"` fire through every 30s poll
      thereafter — re-emits `hx-get="/_partials/s/{id}/state?thread_document=1"` on itself, so the
      compact flag is self-perpetuating across the polling lifetime, not just the initial paint —
      verified by `TestWeb_ThreadDocument_CompactsSubagentChromeAndFooter`
      (`web_test.go:2630-2632`)

### 2.7 DOM structural invariants

- [ ] Composer nesting is a pinned contract: `[data-composer-surface]` > `.input-card` >
      (`.message-input`, `.input-controls` > `.controls-left`); `[data-composer-surface]` >
      `.input-status-rail` > (`[data-tasks-trigger]`, `#input-status`) — verified by
      `TestWeb_ThreadDocument_ComposerControlsLiveInsideInputCard` (`web_test.go:2704-2760`)
- [ ] The task-list trigger (`[data-tasks-trigger]`) is required to live OUTSIDE `#input-status` (the
      htmx innerHTML swap target) specifically so its JS-managed badge/listeners survive the
      periodic swap — same test, explicit negative assertions
      (`web_test.go:2755-2757`); documented rationale in `templates/partials/workspace.html:96-98`

---

## 3. `panes.js` — what UX must survive into dockview

Per the design doc, `panes.js` itself is deleted wholesale at M10 (§10) — its entire iframe +
postMessage + localStorage mechanism is replaced by dockview's native pane/tab/split/popout system
(§6.4). Nothing below is "port this file"; it is the list of user-observable guarantees the current
mechanism provides that dockview's built-in pane management needs to match (or a documented decision
to drop).

### 3.1 Host-page contract & lifecycle scope

- [ ] Depends on exactly 4 host-page elements by id: `#side-panes`, `#pane-splitter`, `#sidebar`,
      `#sidebar-resizer` — `panes.js:11-14`; present in `templates/app.html:36,45,57-58`
- [ ] `#side-panes`/`#pane-splitter` are SIBLINGS of `#workspace`, not descendants — htmx's
      `innerHTML` swap of `#workspace` on in-app session navigation never touches an open pane —
      `templates/app.html:51-58`
- [ ] `restore()`/`bindSplitter()`/`bindSidebarResizer()`/`restoreWidth()` run exactly once per full
      document load (`DOMContentLoaded`, or immediately if already past `readyState:"loading"`), NOT
      on htmx partial swaps — open panes persist across in-app session navigation —
      `panes.js:457-461`

### 3.2 Pane open/close mechanics

- [ ] Hard cap of 3 concurrent side panes (`MAX_SIDE_PANES`); a 4th `open()` silently no-ops
      (returns `null`, no toast/error shown) — `panes.js:5`, `panes.js:181`
- [ ] 300px minimum width per pane (`PANE_MIN`), enforced by growing the region after every
      open/close so `paneCount × 300` never exceeds the region width — `panes.js:9`,
      `panes.js:295-309`
- [ ] Default side-region width is 420px, matching the CSS `--side-panes-w` default exactly —
      `panes.js:307`; `assets/style.css:534`
- [ ] `open(href)` dedups by normalized href — re-opening an already-open href just focuses the
      existing pane's iframe rather than opening a duplicate — `panes.js:178-179`
- [ ] `openAfter(href, title, afterHref)` fails outright (`null`, no fallback) if `afterHref` is not
      itself a currently-open pane — it never falls back to append-at-end — `panes.js:180`,
      `panes.js:215-217`
- [ ] Explicitly opening a previously-dismissed href clears its suppression entry — a deliberate
      open always wins over a prior close — `panes.js:177`
- [ ] `close(href)` removes the pane, hides the region+splitter once the last pane closes, and
      records the href as suppressed — `panes.js:233-246`
- [ ] Each newly-opened pane shows a loading state with a 15-second watchdog; on timeout it
      auto-invokes the same error path as a real load failure
      (`"Pane did not finish loading"`) — `panes.js:64-71`
- [ ] The error state renders inline retry (`[data-pane-retry]`) and close
      (`[data-pane-error-close]`) buttons; retry re-sets the iframe `src` and re-arms the loading
      watchdog rather than tearing down and recreating the pane — `panes.js:73-110`
- [ ] The iframe's native `load` event clears the watchdog and flips the pane to `"ready"` UNLESS
      it's already in the error state (a late `load` after a watchdog timeout doesn't un-error it);
      the native `error` event routes to the same `markError` path as the watchdog —
      `panes.js:202-208`

### 3.3 Persistence keys

- [ ] `serf-hub.panes` (localStorage) — JSON array of `{href, title}` for every currently-open pane,
      rewritten on every open/close — `panes.js:248`, `panes.js:410-420`
- [ ] `serf-hub.panes.width` (localStorage) — last side-region width in px —
      `panes.js:249`, `panes.js:291`
- [ ] `serf-hub.panes.closed` (localStorage) — JSON array of suppressed hrefs (see §3.4) —
      `panes.js:250`, `panes.js:255-264`
- [ ] Restore precedence: URL `?pane=` query params, when present (even just one), COMPLETELY
      override localStorage for that load — the stored layout is not merged with URL panes, it's
      ignored outright — `panes.js:422-442`
- [ ] URL-specified panes bypass suppression on restore — an explicit share link is treated as a
      deliberate request that overrides any local dismissal — `panes.js:432-438`
- [ ] `refreshURL()` encodes every currently-open pane href as repeated `?pane=` params via
      `history.replaceState` (no navigation, no new history entry), preserving any other existing
      query params — `panes.js:396-408`
- [ ] `refreshURL()` always reads `window.location.pathname` fresh at call time specifically so it
      survives the renderer's own unrelated `history.replaceState` calls for the primary route —
      `panes.js:394-395`

### 3.4 Suppression set

- [ ] Suppression is the mechanism that keeps AUTO-OPENED panes (observers, see §3.7) from
      reappearing after a user explicitly dismisses them — a closed pane's href is added to
      `serf-hub.panes.closed` and checked by `renderer.js`'s auto-open path before it calls
      `SerfPanes.open()` — `panes.js:271-284` (`suppress`/`unsuppress`/`isSuppressed`),
      `renderer.js:403` (`isSuppressed` check gating `autoOpenObservers`)
- [ ] Suppression persists across reload/re-init (it's the whole point) — it survives independently
      of the `serf-hub.panes` open-set key, so a dismissed pane stays dismissed even though it's no
      longer listed in the "currently open" set — `panes.js:255-264` vs. `panes.js:248`
- [ ] A MANUAL `open()`/`openAfter()` call (user explicitly requesting the href again — e.g.
      re-clicking "open beside") clears suppression for that href FIRST, before checking whether
      it's already open — suppression only blocks AUTOMATIC re-opens, never a repeated explicit
      request — `panes.js:177`

### 3.5 Open-beside postMessage protocol

- [ ] Message shape: `{type: "serf:open-beside", href, title, afterHref?}`, posted to
      `window.location.origin` (never `"*"`) — producer side `renderer.js:379`
- [ ] The host's `onMessage` listener rejects any event whose `origin` isn't EXACTLY
      `window.location.origin` before inspecting the payload at all — `panes.js:150-151`
- [ ] The host only accepts a bridge request from a window that is the `contentWindow` of an
      ALREADY-OPEN pane's iframe (`isKnownPaneSource`) — an arbitrary/unrelated window object is
      rejected — `panes.js:120-128`, `panes.js:145`
- [ ] The host additionally requires the requested href to be same-origin AND path-prefixed
      `/thread/` or `/doc/` (`isPaneSafeHref`) — a cross-origin href, or a same-origin href with any
      other prefix, is rejected even from a known child — `panes.js:130-140`, `panes.js:146`
- [ ] `afterHref`/explicit-ordering is threaded through the bridge only when the posted message
      object literally has an `afterHref` own-property (`hasOwnProperty` check, not just
      truthiness) — distinguishes "insert after this specific href" from "no ordering preference" —
      `panes.js:154`, `panes.js:142-144`
- [ ] Producer-side selection logic (`renderer.js`'s `openBeside(spec)`): try `window.SerfPanes.open()`
      directly first (only ever true in the un-framed host document, since `panes.js` is never
      loaded inside a pane — see §2.4); fall back to `window.parent.postMessage(...)` only when
      `isInPane()` is true — `renderer.js:372-383`
- [ ] `isInPane()` is a same-origin `window.self !== window.top` check, wrapped in try/catch so a
      cross-origin parent (shouldn't happen here, but defensively) doesn't throw — treats any thrown
      access as "yes, framed" — `renderer.js:132-134`
- [ ] `makeOpenBesideButton()` (the shared ⇲ control builder used by every "open beside" affordance)
      refuses to render the control at all when NEITHER `window.SerfPanes` NOR `isInPane()` is
      available — i.e. the affordance is hidden entirely in a context that could neither open
      locally nor bridge — `renderer.js:3986-3987`
- [ ] End-to-end protocol behavior is independently verified by a dedicated jsdom test harness:
      known-child open succeeds and the href appears in `openHrefs()`; a cross-origin href is
      rejected; an unknown source window is rejected; a framed `SerfPanes.open()` call (no
      `#side-panes` in that document) posts the bridge message instead of opening a local pane and
      itself returns `null` — `jstest/test-thread-document-bridge.js:48-72`

### 3.6 Splitters

- [ ] `panes.js` owns TWO distinct drag-resize splitters, not one: `#pane-splitter` (side-panes
      region width) and `#sidebar-resizer` (left sidebar width) — both implemented in this same
      file — `panes.js:322-349` and `panes.js:351-389`
- [ ] `#pane-splitter` drag formula: width = `window.innerWidth - event.clientX` (panes grow as the
      pointer moves left), clamped to `[280, min(1200, innerWidth - 360)]`, persisted to
      `serf-hub.panes.width` on every move — `panes.js:337-342`, `panes.js:286-293`
- [ ] `#sidebar-resizer` drag formula: width = `event.clientX` directly, clamped to
      `[180, min(480, innerWidth * 0.45)]`, sets the `--sidebar-w` CSS custom property directly with
      NO localStorage persistence (unlike the pane-width splitter) — `panes.js:360-369`
- [ ] Sidebar-resize drag is a no-op when the sidebar is in "rail" (collapsed) mode
      (`document.body.dataset.sidebarRail !== undefined`) — dragging a collapsed icon-only rail to
      resize makes no sense — `panes.js:356`; rail mode toggled by `sidebar.js:1287-1289`
- [ ] Both drag handlers share the same defensive stop condition — `mousemove` checks
      `event.buttons === 0` (button released outside the window) in addition to listening for
      `mouseup`/`window blur` — guarantees a drag can't get stuck active if the mouseup is missed —
      `panes.js:328-347`, `panes.js:370-387`

### 3.7 Producers of "open beside" (who triggers it, from where)

- [ ] File-referencing tool cards (`read_file`/`edit_file`/`write_file` only — multi-target tools
      like `apply_patch` and directory/pattern tools like `grep`/`ls` are explicitly excluded) build
      an `/doc/file?session={id}&path={rel}` href, where `{rel}` is the tool's file arg made relative
      to the session cwd (out-of-cwd paths get no affordance at all) —
      `renderer.js:2171-2204`
- [ ] Image cards (single and multi-image "contact sheet") build hrefs from a sha-addressed
      `/s/{id}/images/{sha}` path; `data:` URLs are explicitly skipped (no stable URL, and a `data:`
      iframe `src` is blocked by the same-origin CSP anyway, so the pane would render blank) —
      `renderer.js:2238-2259`
- [ ] Subagent job rows (`applyJobRefTarget`) build hrefs via `renderer.threadHref(ref)` —
      `renderer.js:4009-4032`
- [ ] All three producers above converge on the same shared `makeOpenBesideButton()` control builder
      and the same `openBeside()`/postMessage-bridge decision logic — `renderer.js:3980-4007`
- [ ] Sidebar's context-menu "Open beside" item and its subagent-row-wrap ⇲ button call
      `window.SerfPanes.open()` DIRECTLY, with no `isInPane`/postMessage fallback at all — correct
      only because `sidebar.js` never itself runs inside a pane iframe (it's absent from
      `thread.html`'s script list, §2.4) — `sidebar.js:276`, `sidebar.js:1136-1153`
- [ ] Auto-open: a worker session's LIVE observer subagents (`data-observers` on `#conversation`,
      sourced from `WorkspaceData.ObserverRouteIDs`) are opened automatically beside the worker on
      render, skipping any href already in the suppression set — `renderer.js:398-405`,
      `web_types.go:215-222` (`ObserverRouteIDs` field doc), `web_workspace.go:681-699`
      (`fillObserverLink`, unions live `SessionMeta.ObservedBy` with the durable
      `PastIndex.ObserversOf` grant history so the relationship surfaces even for sessions already
      on disk)
- [ ] A separate, non-bridge accelerator exists for subagent parent navigation: `Escape` navigates
      to `.subagent-parent-up[href]` via `renderer.navigateTo()` (an in-place route change, not
      postMessage) whenever no overlay/dialog/text-entry currently owns the key — a DIFFERENT
      mechanism from the open-beside bridge, bound once per process — `renderer.js:6922-6946`

### 3.8 Verified gaps needing a conscious decision

- [ ] The parent-breadcrumb postMessage delegate (`bindPaneParentLinks()`, listening for clicks on
      `[data-open-parent-beside]`) is real, working code, independently unit-tested — but has NO
      reachable trigger in current production markup: the only template that renders the crumb
      (`templates/partials/workspace.html:8`) never emits a `data-open-parent-beside` attribute (it
      emits a plain `<a href="/s/...">`), and that whole block is gated `{{if and .ParentRouteID
      (not .ThreadDocumentMode)}}` — unconditionally suppressed in exactly the mode
      (`ThreadDocumentMode`) where the bridge would be needed (§2.5). Decide whether to wire the
      attribute through in the new implementation (giving subagent panes a working parent-nav
      affordance) or drop the dead delegate — `renderer.js:385-396` (mechanism),
      `jstest/test-thread-document-bridge.js:74-91` (isolated test using hand-built fixture markup,
      not the real template), `templates/partials/workspace.html:6-8` (actual gating)
- [ ] Image "open beside" hrefs (`/s/{id}/images/{sha}`) are NOT on `isPaneSafeHref`'s allowlist
      (only `/thread/`/`/doc/` prefixes pass) — opening such an image works fine when the control is
      clicked from the un-framed top-level workspace (direct `SerfPanes.open()` call applies no
      allowlist), but the SAME control, clicked from inside an already-open pane, is silently
      dropped by the postMessage bridge. Decide whether image-open-beside should work from inside a
      nested pane (extend the allowlist, or route these through `/doc/image` instead) —
      `panes.js:130-140` (`isPaneSafeHref`), `panes.js:21-44` (`normalizePaneHref`, confirmed NOT to
      rewrite `/s/{id}/images/{sha}` into a `/thread/`-prefixed form), `renderer.js:2249-2251`
      (the href's origin)
- [ ] No cross-frame theme sync exists anywhere — `theme.js` only ever touches its OWN document's
      `data-theme` attribute + localStorage; no `storage` event listener exists in `theme.js`,
      `thread.html`, or `doc_serve.go`'s inline boot scripts. A theme change made after a pane is
      already open does not reach that pane's `data-theme` until the pane reloads (close+reopen, or
      full page refresh) — irrelevant once panes are same-document dockview panels rather than
      cross-document iframes, but worth confirming the replacement doesn't reintroduce an equivalent
      staleness through some other isolation boundary — `assets/theme.js:1-12`
- [ ] CSP context: `frame-ancestors 'self'` permits the same-origin iframes `panes.js` relies on; no
      distinct `frame-src` directive exists (falls back to `default-src 'self'`), consistent with
      `isPaneSafeHref`'s same-origin-only rule — becomes moot once panes stop being iframes, but the
      design doc's §4 tightens CSP further (`script-src 'self'` with no `'unsafe-inline'`) as part of
      this same migration, so it's a live area regardless — `httpsec.go:26-35`

Not applicable / no legacy behavior to port: **popout windows** (design doc §13's M8 line lists this
alongside doc viewer/thread single-pane/PWA, but it is a new dockview-native capability — §6.4 —
with no existing mechanism in this codebase; the only `window.open()` calls in scope are
`search.js:1071`'s plain `_blank` new-tab session open and two unrelated OAuth popups in
`credentials.html:64,413`).

---

## 4. PWA manifest handling

### 4.1 Route & serving mechanics

- [ ] `GET /manifest.webmanifest` → `handleManifest`, registered directly (not under
      `/_partials/` or gated behind auth-exemption) — `web.go:174`
- [ ] NOT gated behind `SERF_HUB_WEB=new` — no `newWebEnabled()` call anywhere in `handleManifest`;
      served identically regardless of the SPA flag — `web.go:262-289` (complete function body)
- [ ] Source of truth is `assets/manifest.webmanifest` (17 lines) read through `s.manifestFS`, which
      falls back to `assetsRoot()` when nil — `web.go:263-267`
- [ ] Missing file → `500 manifest unavailable`; malformed JSON → `500 manifest malformed` —
      `web.go:267-276`
- [ ] The manifest is ALSO reachable, unmodified, via the generic `/assets/` static handler at
      `/assets/manifest.webmanifest` (since `assetsRoot()` embeds the whole `assets/` tree) — this
      path is auth-gated like any other asset (not a bypass), but for an already-authenticated
      client it's a second, divergent copy with `start_url` still `"/"` (no token ever injected) —
      `web.go:156-162`, `embed.go:49-50,71-77` (embed/mount mechanism); `isAuthExempt` does not list
      this specific asset path — `hubedge/auth_token.go:109-117`

### 4.2 `start_url` token injection

- [ ] The server rewrites ONLY `start_url`, and only when `s.cfg.AuthToken` is non-empty: becomes
      `/auth?token={t}&next=%2F` — every other field (name, icons, colors, display, scope,
      orientation) passes through unmodified from disk — `web.go:277-279`
- [ ] When no auth token is configured (guard disabled), `start_url` is left exactly as the disk
      value, `"/"` — no rewrite occurs — `web.go:277` (conditional), `manifest.webmanifest:9`
- [ ] The round-trip through `map[string]any` + `json.Marshal` alphabetically sorts the served
      top-level object keys, differing byte-for-byte from the source file's declared order (name,
      short_name, description, display, ... vs. alphabetical) — semantically inert (JSON is
      order-independent) but a real difference from a naive static passthrough; the `icons` ARRAY
      itself preserves source order, but each individual icon OBJECT's own keys are likewise
      resorted — `web.go:272-280` (mechanism), `manifest.webmanifest:2-15` (source order)

### 4.3 Headers & caching

- [ ] Response `Content-Type` is always `application/manifest+json; charset=utf-8` —
      `web.go:285`
- [ ] Response is always `Cache-Control: no-store` — the token is per-browser-jar and must never be
      cached or shared — `web.go:287`
- [ ] Manifest icon URLs (`manifest.webmanifest:12-15`) carry NO cache-busting `?v=` suffix, unlike
      the `<link rel="apple-touch-icon">`/`<link rel="icon">` tags in `app.html`, which DO get
      `{{assetv}}` appended (a build-mtime token) — an icon change across a hub upgrade may not bust
      an already-installed PWA's cached icon — `manifest.webmanifest:12-15` vs.
      `templates/app.html:15-16`, `embed.go:27-44` (`assetVersionQuery`)

### 4.4 Icon consistency

- [ ] Manifest `icons` is exactly 4 entries: `icon-192.png` (purpose `any`), `icon-512.png` (`any`),
      `icon-maskable-512.png` (`maskable`), `icon.svg` (`any`) — `manifest.webmanifest:12-15`
- [ ] Those exact 4 paths (and no others) are auth-EXEMPT — fetchable without a cookie/bearer token,
      unlike the manifest route itself which stays gated (documented rationale: "a non-sensitive
      logo the OS may fetch without credentials when installing... the manifest stays gated — it
      carries the capability token") — `hubedge/auth_token.go:104-117`; cross-checked in this pass
      against the manifest's own icon list — consistent, no drift found
- [ ] `background_color`/`theme_color` are both `#0a0a0e` in the manifest
      (`manifest.webmanifest:7-8`), matching `app.html`'s `<meta name="theme-color"
      content="#0a0a0e">` (`templates/app.html:13`) — kept in sync by hand across two files, not
      derived from one source; the design doc's §6.8 note that "assets [are] re-generated to the new
      brand tokens" means this pair needs to be re-synced together, not independently, when the
      palette changes

### 4.5 Why this exists (install/relaunch mechanism)

- [ ] The whole feature exists so a home-screen/standalone PWA launch can self-authenticate: the OS
      captures the manifest's `start_url` (including the embedded token) AT INSTALL time; a later
      standalone launch navigates directly to that captured `/auth?token=...&next=/`, which sets a
      fresh cookie in what may be a separate cookie jar (iOS gives standalone launches their own
      jar) — documented in both the manifest handler's comment and the cookie's `SameSite` choice —
      `web.go:169-173` (comment), `hubedge/auth_token.go:177-181` (comment; see §5.5)
- [ ] Consumer: `app.html`'s `<link rel="manifest" href="/manifest.webmanifest"
      crossorigin="use-credentials">` — the `crossorigin="use-credentials"` attribute is REQUIRED for
      the manifest fetch to carry the auth cookie at all (a plain `<link rel="manifest">` without it
      is typically credentialless in browsers) — `templates/app.html:14`
- [ ] `display: "standalone"`, `orientation: "portrait-primary"`, `scope: "/"` are static, currently
      unconfigurable server-side — `manifest.webmanifest:5-6,10`

---

## 5. `/auth` cookie flow — client perspective

What the SPA (or any browser client) must handle, derived entirely from `auth_token.go`'s
server-side contract; cross-checked against all 28 `check*` test functions in
`internal/hubedge/auth_token_test.go` (names only enumerated, not bodies read — every claim below
matched a correspondingly-named test).

### 5.1 Credential transports

- [ ] The client may present the token exactly two ways: the per-hub `serf_hub_auth_*` cookie
      (name suffixed by a hash of the hub's token so co-located hubs don't collide, `cookieName`),
      or an `Authorization: Bearer {token}` header — no other transport is recognized —
      `hubedge/auth_token.go`
- [ ] The token itself lives at `$hub_state_root/auth-token` (mode 0600), created on first hub run if
      absent (256-bit random, base64 raw-URL-encoded); the SPA never generates or reads this file
      directly — a human operator obtains it from the printed startup URL or the file itself —
      `hubedge/auth_token.go:44-82`
- [ ] `AuthURLFor(base, token)` builds the operator-facing URL printed at hub startup
      (`{base}/auth?token={t}`) — this is how a human, not the SPA, first learns the token; the SPA
      has no discovery mechanism of its own — `hubedge/auth_token.go:215-221`

### 5.2 The `/auth` endpoint

- [ ] `GET /auth?token={t}[&next={path}]` is the ONLY route that accepts the token via query string
      as a first-class credential outside the self-heal path (§5.4); on match it sets the cookie and
      `302`-redirects to `next` (default `/`); on mismatch it's `401 invalid token` (plain text) —
      `hubedge/auth_token.go:199-213`
- [ ] `next` is validated: must start with `/` and NOT start with `//`, else silently forced to `/` —
      an open-redirect guard; the client should not rely on this param carrying an arbitrary
      external target — `hubedge/auth_token.go:207-210`

### 5.3 Guard scope — what's exempt, what isn't

- [ ] Every route except an exact-match allowlist of 6 paths (`/auth`, `/api/health`,
      `/assets/icon.svg`, `/assets/icon-192.png`, `/assets/icon-512.png`,
      `/assets/icon-maskable-512.png`) requires a valid cookie or bearer — this includes the SPA's
      OWN entry HTML (`/`, `/s/{id}`, `/thread/{ref}`) and its OWN static bundle (`/webassets/`) — no
      client JS runs at all until the FIRST successful auth — `hubedge/auth_token.go:109-134`;
      `web.go:162-188,216-222`
- [ ] The allowlist is checked by EXACT path string match (`switch path { case ... }`), never a
      prefix/wildcard — any future exempt route must be added verbatim; no sub-path is
      auto-exempted — `hubedge/auth_token.go:109-117`
- [ ] `/rpc`'s WebSocket handshake is itself an HTTP `GET` and is subject to this exact same guard —
      an unauthenticated handshake fails with HTTP `401` at the upgrade, which the browser's
      WebSocket API surfaces only as a generic connection error/close with no status code exposed to
      JS. The SPA has no direct in-band signal distinguishing "auth failed" from any other handshake
      failure over the `WebSocket` object itself — a platform limitation, not something the server
      can change, but the client's reconnect/error UI needs a plan for it —
      `web.go:177,216-222`
- [ ] Middleware composition is `record(auth(httpsec.CSPMiddleware(mux)))` — `AuthGuard` sits
      OUTSIDE `CSPMiddleware`, and returns directly (never calling `next`) on both `401` branches AND
      the self-heal `302` redirect. Consequence: the CSP header is present on every response that
      falls through to `next` — exempt-path routes (including a WRONG-token hit on `/auth` itself,
      whose `401` comes from `HandleAuth` inside `next`, not from `AuthGuard`), the guard-disabled
      bypass, and successfully-authenticated requests — but ABSENT from `AuthGuard`'s own `401`s and
      from the self-heal `302`. Derived directly from the composition order; not the subject of a
      dedicated test in this repo — `web.go:216-222` (composition),
      `hubedge/auth_token.go:136-172` (the three early-return paths that skip `next`)

### 5.4 Self-heal recovery

- [ ] ANY `GET` request (not only `/auth`) that fails the cookie/bearer check but carries a matching
      `?token={t}` query param gets the cookie set and is `302`-redirected to the SAME URL with only
      the `token` param stripped (every other query param preserved) — the SPA can deep-link
      `?token=` onto any page path, not only `/auth` — `hubedge/auth_token.go:136-150`
- [ ] Self-heal is `GET`-only — a `POST`/etc. with a valid `?token=` but no cookie is NOT recovered;
      it `401`s directly. The client must ensure the cookie already exists (via a prior `GET`/
      navigation) before issuing any mutating request or opening the `/rpc` WebSocket —
      `hubedge/auth_token.go:142`

### 5.5 Cookie lifecycle

- [ ] Cookie attributes the client must tolerate: `HttpOnly` (JS cannot read it — don't attempt to),
      `SameSite=Lax` — deliberately NOT `Strict`, because iOS treats a standalone-launch top-level
      navigation as externally initiated and omits `Strict` cookies on it — `Secure=false` (works
      over plain HTTP on loopback/Tailscale; set `Secure=true` via a fronting reverse proxy if
      needed), `Path=/`, `MaxAge` = 1 year — `hubedge/auth_token.go:177-195`
- [ ] Cookie slide-forward: every request authenticated via a valid cookie gets a fresh `Set-Cookie`
      (same value, renewed `MaxAge`) on the response, so an installed PWA in daily use never ages
      out; bearer-authenticated (scripted) requests get NO cookie set — `hubedge/auth_token.go:166-172`
- [ ] Guard-disabled escape hatch: an empty configured token bypasses the guard for EVERY route
      unconditionally — testing-only; a live hub always has a non-empty token —
      `hubedge/auth_token.go:125-131`

### 5.6 Error-response contract

- [ ] `401` responses are always `Cache-Control: no-store` — a client retry after re-auth is never
      served a stale cached `401` — `hubedge/auth_token.go:152`
- [ ] `401` body branches on whether the REQUEST's `Accept` header contains the substring
      `"text/html"`: a long human-readable plain-text instructional block (still
      `Content-Type: text/plain` despite the branch condition's name, since `http.Error` always sets
      that) vs. a bare `"unauthorized"` string for API-style clients — the SPA's `fetch()`/XHR calls
      (typically `Accept: application/json`) land in the terse branch —
      `hubedge/auth_token.go:153-164`

### 5.7 Practical implications for the SPA shell

- [ ] A first-time visit to `/` with NO `?token=` in the URL never reaches the SPA shell at all — it
      renders the server's plain-text `401` wall directly. There is currently no client-side
      "please authenticate" UI; the entire pre-auth experience is server-rendered text —
      `hubedge/auth_token.go:153-162`
- [ ] Because `/webassets/` (the new SPA's hashed bundle) is behind the same guard and is NOT in the
      exemption list, the SPA's own JS/CSS cannot load at all until a cookie already exists — the
      auth handshake is unavoidably a pre-SPA, server-only step in every cold-start scenario, not
      something the React app can smooth over from the inside — `web.go:162-167` (`/webassets/`
      registration), `hubedge/auth_token.go:109-117` (exemption list, `/webassets/` absent)

---

## Open questions for the M8 implementer (not behaviors to replicate, but decisions to make explicitly)

- [ ] `doc_serve.go`'s HTML-presentation functions (`writeDocPage`/`writeDocMarkdownPage`) are
      obvious rewrite targets for the new "doc viewer" pane type (§6.4 of the design doc), but its
      route/guard layer (`handleDocFile`/`handleDocImage`, `ResolveInRoot`, the status-code contract
      in §1.1-1.2) is exactly the kind of thing the design doc's §7 dev-proxy note implies survives
      as a data API. Decide explicitly: does the new doc-viewer pane call through the EXISTING
      `/doc/file`/`/doc/image` routes (keeping their guard/status-code contract as a stable API), or
      does it get its own JSON endpoint and these two routes eventually disappear too (outside the
      `web_*.go`/`templates`/`assets/*.js` glob that §10's deletion wave explicitly names, so they
      wouldn't be auto-deleted by that pass)?
- [ ] The parent-breadcrumb postMessage delegate (§3.8, item 1) — wire the
      `data-open-parent-beside` attribute through so subagent panes get working parent-nav, or drop
      the unreachable delegate code as dead weight? Either is legitimate; silence is not.
- [ ] Image "open beside" from inside a nested pane (§3.8, item 2) — extend `isPaneSafeHref` to
      allow `/s/{id}/images/{sha}`, or route those hrefs through `/doc/image` instead so the existing
      allowlist just works? The dockview rewrite may make this moot (no more nested-iframe
      distinction), but if any interim shell still uses framed panes the gap should be closed
      consciously, not left as an unexplained "sometimes it doesn't work."
- [ ] The `.status-item.turns` element asserted absent in `TestWeb_ThreadDocument_CompactsSubagentChromeAndFooter`
      (§2.5) has no traceable render site in `templates/partials/input_strip.html` as read for this
      pass — confirm where (if anywhere) it's actually produced today before deciding whether the
      new single-pane mode needs to suppress it too, or whether the test is guarding against a
      code path that no longer exists.
- [ ] The `/thread/{ref}` title-blanking quirk (§2.2, last item; cross-cutting finding #2) — for an
      unresolvable ref, should the new single-pane mode keep showing the fallback title
      indefinitely (fixing the current blank-out), or is blanking-to-empty an acceptable "we
      couldn't find this" signal worth keeping? No test currently pins either answer.
- [ ] `ShowSidebarToggle` (§2.2) is dead in the current code — confirm it isn't a leftover half-
      finished feature the new implementation is expected to actually wire up (a "collapse sidebar"
      button inside a thread-document pane), rather than genuinely vestigial.
- [ ] The double-served manifest (`/manifest.webmanifest` vs. `/assets/manifest.webmanifest`, §4.1)
      — harmless today, but worth deciding whether the new build should exclude the raw manifest
      file from the generic `/assets/`-equivalent static mount so only the token-injecting route can
      ever serve it.

## Item count

159 checklist items across 5 sections (40 in §1 Doc/image viewer, 36 in §2 Standalone thread
document, 48 in §3 panes.js, 17 in §4 PWA manifest, 18 in §5 Auth cookie flow), plus 10 numbered
(non-checkbox) cross-cutting findings at the top and 7 checkbox open-question items at the end —
166 checkbox lines in the document total.
