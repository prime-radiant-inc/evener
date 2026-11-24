# Mobile fluency cleanup — handoff brief for a vision-enabled reviewer

Written 2026-11-23 by the fluency pass's implementer, at Jesse's request.
You are taking over the `mobile-fluency-v8` branch: verify what shipped against
the approved mock, fix the listed gaps, and catch what nobody has listed yet.

## 1. Mission and ground rules

The goal, in Jesse's words: "a clean, fluent mobile experience for a
best-of-breed coding agent." The mobile web UI is the product; any native
container is a thin shell over it. The transcript owns the screen: at rest the
phone shows the top bar, one quiet whiteboard line, and the floating composer —
about 121px of an 844px viewport. Everything else sits behind the header's ⋯
sheet or a scroll.

The written design contract is [docs/web-ui/design-system.md](../../web-ui/design-system.md).
§12 ("Phone and tablet fluency") is the phone/tablet law; the preamble's bold
passages — especially "Keep session status inside its pane" and "Keep session
reading controls in the header" — govern every shell. When your change alters
a documented contract, update that file **in place**, in the same change.
Never create a new design document.

Scope boundaries:

- Below 899px is yours: the phone stack and the 768–899px tablet band.
- Desktop (DockHost and the desktop sidebar rail) is **out of scope** — Jesse
  stopped desktop work. Leave it untouched except where one shared component
  must keep serving both, and then leave the desktop half byte-identical.
- A request to find or audit does not become a redesign. Fix what is listed
  here or is a clear contract violation; flag judgment calls with a screenshot
  and a recommendation instead of restyling unilaterally.

## 2. Where everything is

| What | Where |
|---|---|
| Repo | `primeradiant.com/evener` · GitHub `prime-radiant-inc/evener` |
| Branch | `mobile-fluency-v8` (pushed at origin, rebased on main) |
| Frontend | `cmd/evener-hub/frontend` |
| Design contract | `docs/web-ui/design-system.md` (§12 + preamble) |
| Approved mock | `docs/web-ui/mock/evener-fluent-prototype-v8a.html` (also live at `https://artifacts.trout-rigel.ts.net/p/evener-fluent-prototype-v8a/`) |
| Mock status | Approved as "you've got the idea" — anatomy, hierarchy, tone are the contract; pixels are not. Dark-only. |

Notes on the mock: it is one self-contained HTML file; open the committed copy
directly (the hosting page wraps it in a sandboxed iframe that blocks DOM
inspection). Its device legend switches Phone 390×844 / Tablet ↕ 768×1024 /
Tablet ↔ 1024×768 / Duo interior. Its known rough edges are listed in §5 of
this brief — do not copy them.

## 3. What shipped

Eighteen commits on the branch, grouped:

- **Floating chrome** (4956d9936f, becb763457, 8b542891b4): the header
  collapses on transcript scroll into blur capsules; the composer is one
  floating card on `--radius-panel`; the whiteboard is one quiet line.
- **Board** (2d292fbb9a, cfec63c9ec, 1aac5ef4c4): Live → Pinned → Projects →
  Idle → Archive, StatusDot-only rows (no text badges), archive auto-load on
  scroll, New-session as a floating action glued to the list's bottom-right.
- **Inspectors** (e6ce393c69): the header's ⋯ opens a bottom sheet with
  session facts, activity counts, links, and Find.
- **Tablet band** (5ec0e9cb3c, 2fe5ead6ad): 768–899px renders the rail as a
  persistent board column with a 1px divider and a drag handle; one width pref
  (`sidebarWidth`, default 280) shared with the desktop rail; one ◧ glyph
  toggles the column, rotating 180°.
- **Radii** (03c4cea9d0, 6fae4f6919): composer card, capsules, and controls
  standardized on the token ladder.
- Docs and guard repairs (e5ccda32bd, c5b379f5a7, 1938920c93, 0c798747bb,
  027c822ce5).

## 4. The worklist

Ordered by severity. Each item: the contract, the evidence, the fix site, the
test. Write the red test first.

### W1 — BLOCKER: the engaged phone composer mounts a model control

Contract: "the card carries no chips and no counts line in any state"
(design-system.md, "Keep session status inside its pane") and "effort is set on
the settings surface, not in chat chrome" (§12).

Evidence: at phone width the resting composer is clean (Attach, session menu,
Send). Type one character and an editable "glm-5.3 — change model" trigger
mounts in the card's leading row.

Fix: `cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx`. Today
`controlsCollapsed` (near line 1008) is `(isMobile || landscapePhoneLayout) &&
resting`, and the engaged and ask-pending branches (near lines 1296–1351)
render `SessionChrome` with `placement="composer"` whenever the card is
expanded. Introduce `phoneShellLayout = isMobile || landscapePhoneLayout` and
gate the chrome rows on it, so every phone state — resting, engaged,
ask-pending — renders the menu-only treatment. Keep the geometry keys
(`data-compact`, `foldedCompact`) keyed to the resting state so the folded
card's measurements do not change.

Test: extend the Composer mobile cases — engaged draft and ask-pending assert
that no model switch, status row, or effort control mounts, using
`installMobileViewport` from
`src/panes/session/testing/mobileViewport.ts`.

### W2 — Jesse's note: the board column must stand beside the title, not under it

Jesse: "the sidebar is under the title as opposed to next to the title."

Shipped evidence (measured at an 800px-wide viewport): the top bar spans the
full width (x 0–800, y 0–61) and the board column starts below it (y 61–1025,
x 0–280).

Mock reference (measured in the committed artifact, tablet landscape, sidebar
open): the top bar belongs to the conversation column — it starts at the
divider (x 89, width 742 in the rendered frame) — and the board column runs
the full pane height, carrying its own header row.

Fix: in the tablet band, the board column extends to the pane's top and the
StackHost title bar renders inside the conversation column. Files:
`cmd/evener-hub/frontend/src/shell/mobile/StackHost.tsx` (topBar render near
lines 830–866; the split render near lines 912–959) and
`StackHost.module.css` (`.boardColumn` / `.paneColumn`, near lines 199–231).

Watch: the header-collapse contract is unchanged — session-family panes
still collapse on scroll, the board header is still sticky, and the phone
(<768px) still has one full-width bar.

Test: extend the StackHost split tests — the band's title bar must not span
the board column; the column's top must equal the pane's top.

### W3 — Jesse's note: the collapse toggle must live inside the sidebar

Jesse: "when the sidebar is open you've moved the toggle outside the sidebar."

Shipped evidence (800px viewport): the ◧ handle floats at x 288–340 —
entirely outside the board column (x 0–280), overlapping the conversation.
§12's current sentence says the glyph rides "the divider's conversation lip";
Jesse's 2026-11-23 note supersedes it — **update §12 in the same change**.

Mock reference (sidebar open): the ◧ sits inside the board column's header
row, at its right end (~8px from the column's right edge), vertically aligned
with the bar row. Collapsed, it slides to the conversation's leading edge —
shipped already does this; keep it.

Fix: keep the one-glyph contract — a single ◧, 180° rotation, exactly one
visible at a time, never over the rail's own controls — and re-anchor it
inside the column while open. Files: `StackHost.tsx` (`boardHandle` near line
949) and `StackHost.module.css`.

Test: the existing single-toggle assertions in `StackHost.test.tsx`, plus a
measured position check — the toggle's box must sit inside the column's box
while open.

### W4 — CurrentWork rows render above the phone composer

Contract: "nothing arrives above it to move the persistent controls. The card
remains the lowest interactive surface." (same preamble passage as W1)

Evidence: with a task or goal set, visible CurrentWork rows
(`data-testid="current-work"`) render above the phone composer. Mount site:
`Composer.tsx` near line 1365; the visible block is
`src/panes/session/composer/CurrentWork.tsx` lines ~60–104, with no phone
gating.

Fix: gate the visible block on the phone shell (reuse W1's
`phoneShellLayout` as a prop). **Keep** the `VisuallyHidden` announcement
(CurrentWork.tsx lines ~55–59) and the TasksPanel entry — a screen reader
still hears the current work.

Test: phone case — no visible `current-work` above the card; the announcement
remains in the accessibility tree.

### W5 — Activity tabs: underline, not SegmentedControl

Contract: §12: "Activity tabs are underline tabs, never pills."

Evidence: `cmd/evener-hub/frontend/src/shell/activitybar/ActivitySidebar.tsx`
(near line 202) renders the tabs with the rounded `SegmentedControl` widget.

Fix, phone and tablet only: a `role="tablist"` row of existing `Button`
`variant="quiet"` inside wrapper spans that carry `tab`/`aria-selected` (never
CSS reaching into the Button widget's DOM), transparent background, no track
or radius, a 2px bottom underline on the selected tab, horizontal overflow
with roving `tabIndex` (Arrow/Home/End). Keep the store, labels, counts, and
About behavior. The desktop sidebar's `SegmentedControl` stays as is.
CSS: `activitybar.module.css` — drop the padded bordered enclosure for phone.

Test: `ActivitySidebar.test.tsx` — tablist semantics, selection moves,
keyboard roving.

### W6 — OPEN QUESTION, do not build without Jesse's yes: board inline search

The mock's board column carries an inline Search field below its header; the
shipped board has a magnifier that opens the global command palette instead.
That is a real gap against the mock, but it is a feature addition (in-place
board filtering) against §12's "One search field" line. Ask Jesse first. If
yes: filter in place across titles, subtitles, and project names; preserve
section ancestors; hide empty sections; keep the Live → Pinned → Projects →
Idle → Archive organization and the archive auto-load; remove only the mobile
magnifier duplicate and keep the desktop palette trigger.

### W7 — Your own visual pass

You are the vision-enabled reviewer; the worklist above came from a comparison
run without working eyes (its visual claims were verified by DOM and source,
but it could not see). At 400×844 (phone), 768×1024 and 800×1025 (tablet
portrait), 1024×768 (tablet landscape), and 840×600 (Duo interior), put the
committed mock and the running app side by side — same theme, both dark — and
judge with your eyes. Fix clear contract violations; flag judgment calls with
a screenshot and a one-line recommendation. Weight what the reader sees at a
glance over pixel differences: rhythm, alignment, weight, contrast, breathing
room.

## 5. Deliberate divergences — do not undo these

The mock predates these rulings; the shipped branch is correct:

1. **Radii**: the token ladder supersedes the mock's pill composer and
   circular send. The card is `--radius-panel` with `--radius-control` verbs
   concentric.
2. **New-session floating action**: postdates the mock. Icon-only,
   accent-filled, glued to the list's bottom-right even on short lists, and
   clearing the home-indicator band.
3. **One width pref**: the tablet column reads the same `sidebarWidth` pref
   as the desktop rail (default 280), not the mock's viewport-proportional
   width.
4. **Effort and model**: facts in the Overview sheet, settings on the
   Settings surface, never chat chrome (see W1).
5. **Board organization**: Live → Pinned → Projects → Idle → Archive,
   StatusDot-only rows, no text badges, idle rows visible without a "More"
   tap, archive auto-loads on scroll.
6. **The mock auto-collapses the tablet board** on narrow viewports; §12 pins
   a persistent column across the band. The mock is the rough edge here.
7. **Zero ⌘K hint text** anywhere in the UI; the palette still opens on ⌘K.
8. Real transcript content — timestamps, tool-call rows, per-turn
   attribution — is denser than the mock's demo prose. That is content, not a
   defect. If a specific element reads too busy at phone width, flag it with
   a screenshot instead of restyling unilaterally.

## 6. Rules of engagement

- **Testing**: read `docs/developing-evener/testing.md` before writing tests.
  Never assert on copy, error text, or any human-readable string ("Copy Is Not
  a Test Oracle"). Red test first, then the fix. Extend the nearest existing
  test; add a new one only when nothing reaches the path.
- **Biome**: from `cmd/evener-hub/frontend`, run
  `npx biome check --write <touched paths>` on every touched file under `src/`.
  Never run `npx biome` from the repository root.
- **Gates**: `make test-web` is the canonical frontend gate (typecheck, unit
  suite, Biome). `make vet`, `make lint` for the rest. For the browser guards:
  `make build-web` first (the Go guards refuse a stale build), then
  `make test-web-browser`. Run heavy gates one at a time on this machine.
- **Commits**: on this branch, exact-path staging only (never `git add -A`),
  read the staged diff, end every message with:

  ```
  Co-authored-by: Evener e711934 (lunaroute/glm-5.3) <0352CaOtjpsouT35M3icfY@m5max.local>
  ```
- **Docs**: update `docs/web-ui/design-system.md` in place, in the same change
  as any contract change.

## 7. Acceptance and report

Done means: W1–W5 fixed with red-green tests, W7's violations fixed and its
judgment calls flagged, `make test-web` / `make vet` / `make lint` green, the
browser guards green, live verification screenshots at the §12 sizes
(400×844, 768×1024 or 800×1025, 1024×768, 840×600), design-system.md
reconciled, and the branch pushed.

Report back: a per-item verdict (fixed / flagged / refuted, with the why), the
commit SHAs, the gate outputs' verdict lines, and one screenshot per flagged
judgment call.

## Appendix — environment recipes

**Dev server (runs this branch's source).** From the worktree:

```sh
cd cmd/evener-hub/frontend && npm run dev -- --port 5199
```

Browse `http://localhost:5199` (it proxies `/rpc`, `/api`, `/auth`, `/doc`,
`/s` to the local hub on 9180). The hub's own frontend at
`https://m5max.local:9180` is the deployed pre-fluency build — the status quo
ante, useful for before/after only.

**Auth.** If the rail renders empty with a Retry button, the localhost cookie
expired. Re-auth without printing the token:

```sh
printf '<meta http-equiv="refresh" content="0;url=http://localhost:5199/auth/%s">' \
  "$(cat ~/.local/state/evener/auth-token)" > /tmp/auth-redirect.html
# navigate the browser to file:///tmp/auth-redirect.html, then:
rm /tmp/auth-redirect.html
```

**Dark theme.** The automation browser resolves the system scheme (light on
this Mac); the mock is dark-only. Before any side-by-side:
`localStorage.setItem("evener.prefs.theme", "dark")` then reload.

**Viewport scale.** The Chrome automation browser applies a 1.25 device scale:
`set_viewport {"width": W}` lands `window.innerWidth` at W × 1.25. Request
target / 1.25 (e.g. 615 → 769, 640 → 800, 672 → 840), and confirm with
`window.innerWidth` before asserting which layout is showing. The request
floor is 320 wide, so the nearest phone size is 400×844.
