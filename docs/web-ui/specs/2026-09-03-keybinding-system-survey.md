# Keybinding System — Field Survey and Recommendations

Status: Phase 1 survey for the comprehensive keybinding system. Feeds the
Phase 2 infrastructure design. All external facts fetched 2026-09-03; sources
listed per section. Claims we could not verify against a fetched source are
marked [UNVERIFIED].

## Purpose

Make the web hub fully keyboard-drivable: navigation, focus, scroll, the
command palette, and the ask-user question dialogs all operable without a
pointer. Bindings are configurable, unobtrusive by default, and discoverable:
holding a modifier shows hints for what is on screen, and a cheatsheet overlay
lists the bindings available in the current context.

Delivery is four PRs:

1. **Phase 1 (this document)** — survey of the field and integration options.
2. **Phase 2** — core infrastructure: binding registry, scope stack, matcher,
   server-persisted user overrides, conflict detection.
3. **Phase 3** — first bindings landed on the infrastructure: the existing
   global chords migrated, plus navigation bindings (next active session, pane
   focus) and fully keyboard-navigable ask-user dialogs.
4. **Phase 4** — the comprehensive binding map, hold-modifier hints, and the
   cheatsheet overlay.

## Current state (codebase inventory)

Six independent `keydown` listener sites exist today, each with its own guard
policy. There is no central dispatcher, no chord normalization, and no
conflict detection.

- `src/shell/AppShell.tsx:331-469` — the one global listener. Handles
  ⌘/Ctrl+K (palette), ⌘/Ctrl+I (focus composer), ⌘/Ctrl+J (needs-you cycle).
  Guards: `event.defaultPrevented`, Mod required, and for I/J only,
  `paletteStore.open` plus a `[aria-modal="true"]` ancestor check. K/I/J fire
  even while typing in inputs. The palette renders through the Dialog widget
  (`CommandPalette.tsx:206` → `widgets/dialog/OverlayPanel.tsx:126-127`, which
  sets `role="dialog"` and `aria-modal="true"`), so it belongs to the same
  overlay family as Settings; the `paletteStore.open` check covers window-level
  keydowns whose target falls outside the panel (e.g. focus not inside the
  palette), not a missing role.
- `src/shell/rail/RailHost.tsx:57-69` — ⌘/Ctrl+B sidebar toggle. Unlike
  AppShell, it *does* suppress in editable targets.
- `src/panes/settings/Settings.tsx:184-192` — bubble-phase Escape closes the
  pane, guarded by `defaultPrevented`.
- `src/panes/session/transcript/SelectionQuote.tsx:148-160` — Mod+' quote
  action; notes that AltGr arrives as ctrlKey+altKey.
- `src/panes/session/transcript/flow/ImageGallery.tsx:113` — gallery keys.
- `src/widgets/focusscope/index.tsx:81` — scope-local Tab cycling.

Component-level `onKeyDown` handlers additionally live in the Composer,
AskDock, menu, radiogroup, segmentedcontrol, tree, pathfield, and textarea
widgets. The Composer owns Enter/Mod+Enter semantics with `isComposing`
guards and an Enter-to-send preference.

**Palette.** `src/shell/palette/commands.ts:107-128` defines the `Command`
shape: stable `id`, `title`, `keywords`, `scope` ("global" | "session"),
optional hub-published `capability`, and `run(ctx)`. This is the right action
*vocabulary* for bindings, but `buildCommands()` rebuilds the registry on
every call, there are no when-clauses (gating is scope + capability), and
`run` requires a full `PaletteRunContext`. A keybinding layer should bind to
command ids, not reuse `run()` directly. `paletteContext.ts:49-64`
(`buildPaletteContext()`) already computes "current context" (sessionRef +
onPage) — the primitive a context-sensitive overlay needs.

**Ask-user dialogs.** `src/panes/session/composer/askDock/AskDock.tsx` is
partially navigable: ARIA tablist with arrow/Home/End and roving tabindex for
question batches, Mod+Enter submits, native radios/checkboxes. Missing:
Escape-to-dismiss, jumping between batches, focus return to the composer.

**Settings persistence.** Two tiers. localStorage: `stores/prefs.ts` (flat
`evener.prefs.*` keys). Server-persisted: `stores/transcriptDisplay.ts` uses
the wire methods `evener/settings/transcriptDisplay/{get,patch}` plus a
`.../changed` server push for cross-window sync
(`src/protocol/types.gen.ts:2118-2120, 2295-2297`), with a store shape of
local override + hub default + revision-stamped effective resolution. This is
the template to clone as `evener/settings/keybindings/{get,patch,changed}` —
with one scoping decision Phase 2 must make explicit: transcriptDisplay stores
hub-level *defaults*, while keybinding remappings are personal. The Phase 2
design must state whether bindings are per-user (and what "user" means for the
hub) or hub-shared, rather than inheriting the shared-default semantics by
accident.

**KeyHint.** `src/widgets/keyhint/index.tsx` renders `<kbd>` runs and owns the
⌘/Ctrl platform split, but it is render-only; every call site hardcodes its
chords. The palette's `HELP_ROWS` legend is hand-maintained and has already
gone stale once — any overlay must be generated from the live registry.

**Tests.** `AppShell.test.tsx` covers the existing chords via
`fireEvent.keyDown`; `paletteTestUtils.tsx` and `protocol/testing/fakeClient.ts`
give FakeClient seams for server-persisted settings tests. jsdom tests that
touch localStorage install a `MemoryStorage` global.

## Library field survey

All data fetched 2026-09-03 from npm registry, GitHub, and bundlephobia.

| Library | Latest / date | License | min+gzip | Sequences | Scopes | Mod alias | User-remap support | Maintenance |
|---|---|---|---|---|---|---|---|---|
| **tinykeys** | 4.0.0 / 2026-05 | MIT | 1.0 KB gz | Yes (`"g i"`, `"$mod+K $mod+1"`), optional modifiers, regex keys, `parseKeybinding()` | None | `$mod` | Parse, no recorder; bindings are a plain object | Episodic but alive (0 open issues, 4.1k★) |
| **hotkeys-js** | 4.0.7 / 2026-08 | MIT | 3.4 KB gz | No | Named scopes (flat, one active + `all`) | **No** (bind `ctrl` and `command` separately) | No | Very active; 133 open issues |
| **mousetrap** | 1.6.5 / 2020-01 | Apache-2.0 WITH LLVM-exception | 2.4 KB gz | Yes | None | `mod` | No | Dormant; 188 open issues |
| **react-hotkeys-hook** | 5.3.3 / 2026-06 | MIT | 3.0 KB gz | Yes (v5, `sequenceTimeoutMs`) | Named scopes via `HotkeysProvider` | **No** | No recorder; `enabled` option | Active; 20 open issues; React 19 OK |
| **@github/hotkey** | 3.1.4 / 2026-03 | MIT | 2.5 KB gz | Yes (1500ms window) | `data-hotkey` DOM-presence model — close to "on screen" but not a stack | `Mod` | No recorder | Active; 11 open issues |
| **@tanstack/hotkeys** + react adapter | 0.8.0 / 0.10.0, 2026-04; first published 2026-02 | MIT | 10.8 KB gz | Yes (vim-style, separate Sequence API) | No named scopes; central HotkeyManager with conflict detection | `Mod` | **Best in field**: HotkeyRecorder, `normalizeHotkey*`, display formatting, `useHeldKeys` | **Self-declared alpha**; 710★, 23 open issues |
| **kbar** | 1.0.0 / 2026-08 | MIT | 27.5 KB gz | No | Palette nesting, not binding scopes | Yes | No | Keybinding layer not separable; we already have a palette |
| **cmdk** | 1.1.1 / 2025-03 | MIT | 14.9 KB gz | n/a | n/a | n/a | n/a | Not a keybinding library (FAQ: "listen for ⌘K yourself") |
| **@react-aria/utils** `useKeyboard` | 3.34.1 / 2026-05 | Apache-2.0 | small tree-shaken | No | DOM-propagation model, compositional | `Mod` | No | Very active; designed for focused-element interactions, not global shortcuts |

### Key finding

No library natively combines multi-key sequences, named contextual scopes,
and user remapping in one model. The closest:

- **@tanstack/hotkeys**: sequences ✓, recorder/parse/serialize ✓, held-key
  hooks ✓ (directly relevant to hold-modifier hints), scopes ✗.
- **react-hotkeys-hook**: sequences ✓, named scopes ✓, remapping ✗, and no
  `mod` alias.
- **tinykeys**: sequences ✓, parse ✓, scopes ✗.

### Recommendation: tinykeys as matcher, own the registry and scope stack

Adopt **tinykeys 4.0.0** as the event-matching engine and write the scope
stack, binding registry, and when-clause evaluation ourselves (~200 lines,
zustand-backed). Rationale:

- tinykeys has the best grammar (sequences, `$mod`, optional modifiers, regex
  keys), the smallest footprint (1 KB gz), and `parseKeybinding()` for overlay
  rendering and user-binding round-trips. `createKeybindingsHandler` gives an
  attach-anywhere, testable matcher. Its default input-field suppression is a
  starting point, not a policy: today's Mod+K/I/J chords deliberately fire from
  editable targets, and FocusScope must handle Tab while an input has focus.
  The registry must carry a **per-binding editable-target policy** (an
  `allowInEditable`-style flag), with matcher-level suppression disabled or
  overridden where a binding requires it. Phase 2 migration tests must cover
  Mod+K/I/J fired from inputs and FocusScope Tab cycling from text fields.
  The dispatcher must also ignore `event.isComposing` keydowns so Enter or
  single-character bindings cannot fire mid-IME-confirmation.
- The scope requirement — bindings active only for what is on screen, with
  stacking — matches dockview panel visibility and route state, which no
  library models. hotkeys-js and react-hotkeys-hook scopes are flat named
  sets, not a visibility-driven stack.
- TanStack Hotkeys is the credible alternative: it uniquely bundles a
  recorder, conflict detection, and held-key hooks. But it is a self-declared
  alpha, five months old, 10× tinykeys' size, with sequences behind a second
  API. Adopting an alpha as infrastructure is a risk we need not take; its
  `HotkeyRecorder`/`normalizeHotkey*` ideas are portable patterns (~50 lines
  around `keydown` capture) if we want them later.
- Hand-rolling the matcher itself is viable (tinykeys' model is a plain
  object map over `event.key` + modifiers) but buys little; tinykeys' tested
  edge cases are worth 1 KB.

Hand-rolled beats a library only if Phase 4 demands exotic grammar
(per-sequence-step when-clauses, leader-key modes). Revisit then.

### Binding grammar recommendation

Support **chords plus optional multi-key sequences** from the start. The cost
is zero with tinykeys (its grammar includes sequences), and deferring
sequences would force a grammar migration of persisted user bindings later.
Keep `Mod` (⌘ on macOS, Ctrl elsewhere) as the primary modifier convention;
single-character bindings (Gmail-style `g i`) must be scope-gated and ship
with a turn-off setting (see Accessibility).

## Discoverability: hold-modifier hints and the cheatsheet overlay

### Hold-modifier hints: viable, with required mitigations

A page reliably receives `keydown` **and** `keyup` for a modifier key itself
(MDN: keydown "fires for all keys"). The classic macOS bug — Bugzilla
1299553, affecting all macOS browsers — swallows `keyup` for *other* keys
released while ⌘ is held, but the modifier's own keyup is delivered, and
hold-modifier hints need only that. `getModifierState()` is a polling
fallback on any event. The UX is validated at OS level: iPadOS shows per-app
shortcuts while ⌘ is held (verified via two secondary sources; Apple's own
doc [UNVERIFIED], guide pages returned TOC only). We found no desktop-web app
shipping this today — we would be early, but not unproven.

Required mitigations:

1. Dismiss on `window` blur and `document.visibilitychange` — the modifier's
   keyup is lost when focus leaves the page; MDN calls `hidden` the last
   reliably observable event.
2. Belt-and-braces safety timeout or first-mouse-move dismissal.
3. Hold key is ⌘ on macOS, Ctrl on Windows/Linux. Never bare Alt on
   Windows/Linux: tapping Alt raises the browser menu bar (verified for
   Firefox).
4. Accept that OS chords (⌘Q, ⌘W, ⌘Tab) tear down the page mid-hold; the
   Keyboard Lock API (fullscreen-only, Chromium-only, WICG draft) is not a
   fix.
5. Do not extend the pattern to tracking printable keys held during ⌘ — that
   hits the swallowed-keyup bug.

### Cheatsheet overlay: ⌘/ primary, ? secondary

Precedents split on trigger. `?` has more (GitHub, Gmail, Linear) and is more
intuitive, but it is a character-key shortcut and falls under WCAG 2.1.4: it
must ship with a turn-off or remap setting. `⌘/` (Ctrl+/ on Windows/Linux)
is Slack's trigger, appears on no browser-reserved list, and escapes WCAG
2.1.4 entirely because it includes a modifier. Ship **⌘/ primary + `?`
secondary with a disable setting**. `⌘.` has no verified precedent; do not
lead with it.

Context-sensitivity patterns observed: filter overlay contents by current
view/focus (GitHub, Slack), `when`-clause expressions per binding (VSCode —
the rigorous model), live panel that tracks context (Figma). Our registry
should evaluate bindings against the active scope stack so the overlay,
hold-modifier hints, and dispatch all read one source of truth.

## Accessibility (WCAG 2.1.4)

SC 2.1.4 "Character Key Shortcuts" (Level A): a shortcut using only
letter/punctuation/number/symbol keys must offer at least one of turn-off,
remap-to-modifier, or active-only-on-focus. Modifier chords are out of scope.
Consequences:

- Any single-character bindings (e.g. `?`, vim-style sequences starting with
  a printable key) need a disable/remap setting. GitHub ships turn-off; Gmail
  ships turn-off + remap.
- Modifier-chord triggers need no compliance machinery — another argument for
  ⌘/ as the primary overlay trigger.
- Overlays need standard dialog behavior: focus management, Escape to close,
  keyboard-reachable.

## Reserved-chord landscape

Verified against Chrome and Firefox shortcut lists, VSCode-web docs, and the
WICG Keyboard Lock spec.

**Never use** (not interceptable by pages): ⌘W / ⌘N / ⌘T / ⌘Q (macOS),
Ctrl+W / Ctrl+N / Ctrl+T / Ctrl+Tab / Ctrl+Shift+Tab, ⌘1–9 / Ctrl+1–9,
Ctrl+Shift+P in Firefox (private window — this is why VSCode-web falls back
to F1), ⌘Shift+N, ⌘Shift+T, F11, Alt+F4, Ctrl+Alt+Del, Escape in fullscreen.

**Use with care** (overridable via preventDefault, only when focus context
justifies it): ⌘F, ⌘S, ⌘P, ⌘K (Chrome search), ⌘R, ⌘+/-/0, F12 / ⌘⌥I,
⌘Shift+[ / ⌘Shift+] (Safari tab switching [UNVERIFIED]; Chrome Mac uses
⌘⌥←/→). Bare Alt on Windows/Linux raises browser menus. Browser-chrome
chords such as ⌘L (address bar) and ⌘D (bookmark) are not reliably
interceptable across browsers — delivery and preventDefault behavior vary —
so treat them as unavailable rather than overridable.

Any remap UI must validate user chords against a per-platform blocklist — and,
better, against a tested allowlist of chords known to dispatch reliably, so
the UI can never accept a binding the browser will never deliver.

## User remapping precedents

Remapping is rare in web apps. VSCode (desktop and web share the model):
keybindings.json rules `{key, command, when, args}` evaluated bottom-up, with
conflict detection and when-clauses; browser-reserved keys simply cannot be
bound on the web. Gmail: opt-in remapping, one action per key. GitHub:
disable character shortcuts only. Figma and Slack: no remapping. Excalidraw:
open feature request.

Our user has already decided bindings are user-configurable and
server-persisted via the hub — closer to VSCode/Gmail than to GitHub. The
pragmatic scope for Phase 2: JSON-serializable `{key, command, when?}` rules
over command ids, validated against the platform blocklist, with conflict
detection, cloned onto the `evener/settings/transcriptDisplay` get/patch/
changed template. The persisted format needs strict, versioned validation: a
schema version field, command ids checked against the live registry (unknown
or stale rules rejected at load and at patch time, never dispatched by
string), and `when` as a structured, non-executable predicate grammar — not
evaluated JavaScript. Without this, persisted settings become an injection or
crash surface. A full settings-UI binding editor (click-to-record) is
Phase 4 scope; Phase 2 can ship patch-via-API plus a read-only settings
section.

## Risks

1. **Precedence policy.** Six listener sites today disagree on guards
   (AppShell fires in inputs; RailHost suppresses them). The registry must
   define and enforce a single precedence — editable focus > modal > scope
   stack > global — or it will regress Enter/Mod+Enter and existing chords.
2. **Stale legends.** HELP_ROWS already rotted once. Overlay, hints, and
   dispatch must all derive from the live registry.
3. **Session-scoped commands.** Session commands deliberately run only from
   the composer surface, behind capability gating. Binding a hotkey to one
   must go through that gating, not around it.
4. **macOS swallowed keyups** forbid any "⌘+letter held" state tracking.
5. **Overlay family decision.** The palette already joins the Dialog/
   OverlayPanel family (`role="dialog"`, `aria-modal="true"`); the cheatsheet
   overlay must do the same so the guard matrix stays one rule
   (`[aria-modal="true"]` ancestor) plus the palette's store check, rather
   than growing a third bespoke case.
6. **Alpha dependency risk** if we ever adopt TanStack Hotkeys; mitigated by
   the tinykeys-first recommendation.

## Phase roadmap sketch

- **Phase 2 (infrastructure PR):** `keybindings/` module — chord parser/
  serializer, tinykeys-backed matcher, zustand scope-stack registry,
  precedence policy, `evener/settings/keybindings/{get,patch,changed}` wire
  methods + server storage, per-platform blocklist, conflict detection.
  Migrate the six listener sites onto the dispatcher behind the existing
  chords (no user-visible change). Developer doc: architecture + how to
  register a binding.
- **Phase 3 (first bindings PR):** next/previous active session and pane
  navigation (driving `workspaceStore.focusedPaneId` ⇄ dockview `setActive`),
  transcript scroll keys, full keyboard operation of AskDock (Escape,
  batch-jump, focus return). Developer doc: binding map as shipped.
- **Phase 4 (map + discoverability PR):** comprehensive binding map design,
  cheatsheet overlay (⌘/ + `?`), hold-⌘/Ctrl hints, settings-UI binding
  editor with recorder. Developer doc: user-facing customization guide.

## Sources

Library data: registry.npmjs.org, github.com/{jamiebuilds/tinykeys,
jaywcjlove/hotkeys-js, ccampbell/mousetrap, JohannesKlauss/react-hotkeys-hook,
github/hotkey, TanStack/hotkeys, timc1/kbar, pacocoursey/cmdk},
tanstack.com/hotkeys docs, react-spectrum.adobe.com, bundlephobia.com — all
fetched 2026-09-03.

Discoverability: developer.mozilla.org (keydown_event,
getModifierState, visibilitychange_event), bugzilla.mozilla.org/1299553,
support.google.com/chrome/answer/157179, support.mozilla.org menu-bar article
(via web.archive.org), code.visualstudio.com (codebasics, vscode-web,
configure/keybindings), wicg.github.io/keyboard-lock,
docs.github.com/en/get-started/accessibility/keyboard-shortcuts,
support.google.com/mail/answer/6594, slack.com/help/articles/201374536,
help.figma.com/hc/en-us/articles/360040328653,
linear.app/changelog/2021-03-25-keyboard-shortcuts-help,
new.superhuman.com/keyboard-shortcuts-97407,
iphonelife.com/content/how-to-find-ipad-keyboard-shortcuts,
w3.org/WAI/WCAG21/Understanding/character-key-shortcuts.html — all fetched
2026-09-03.

Unverified items: Apple's official hold-⌘ iPad doc; Chromium issue 40880986
(login wall; substance verified via the Firefox twin bug); Discord's overlay
trigger (image-rendered page); Safari ⌘Shift+[/] tab switching; macOS
CheatSheet app (vendor page repurposed); VSCode hold-Alt menu claim;
hotkeys-js import-time DOM behavior; kbar's rendered zero open-issue count.
