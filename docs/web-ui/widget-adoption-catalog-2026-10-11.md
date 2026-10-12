# Widget adoption catalog (2026-10-11)

Every site in the hub frontend that bypasses or misuses the widget library, classified SWAP / EXTEND / NEW / BESPOKE / MISUSE. Produced by three read-only audit passes; line numbers are as of commit 64bbde6940. Re-measure with `node cmd/evener-hub/frontend/scripts/ds-audit.mjs`.


---

## Area: shell

### Design-system drift catalog: shell / zoom / doc / mermaid

Scope: `src/shell/**`, `src/panes/zoom/**`, `src/panes/doc/**`, `src/widgets/mermaid/**` (no tests, no `src/dev/`).
Paths relative to `cmd/evener-hub/frontend/src/`. Read-only survey; nothing edited.

Note on the locked API: §3 of `docs/web-ui/design-system.md` is stale against shipped widgets (see the "documented-API drift" row). Judgements below use the shipped source where it is a superset of §3.

| file:line | element/class | classification | target widget + props / proposed change | notes |
|---|---|---|---|---|
| shell/statusbar/StatusBar.tsx:52; statusbar.module.css:34-95 | raw `<button>` `.chip` (glyph + name + count) | NEW | **CountButton** `{glyph; label; count; accessibleLabel; compact?; orientation?: "row"\|"column"}` on Button's quiet chrome | Same widget as zoom `.activityChip`. Has no `:focus-visible` ring (zoom copy does). Uses native `title` in place of Tooltip. Container-query collapse (hide name ≤600px, hide glyph ≤320px) needs to be a `compact` prop or stay as caller layout. |
| panes/zoom/ActivityPeek.tsx:79; zoom.module.css:173-214 | raw `<button>` `.activityChip`, plus `.spine .activityChip` / `.spine .activityLabel` overrides | NEW | **CountButton** (same as above), `orientation="column"` + `compact` for the spine | Second copy of the status-bar chip with different padding/gap/focus. Is a Popover trigger with `aria-expanded`/`aria-haspopup="dialog"`, so CountButton must forward ref + rest-spread. |
| shell/statusbar/ScopeCrumbs.tsx:59; scopeCrumbs.module.css:30-46 | raw `<button>` `.crumbBtn` (inline breadcrumb) | EXTEND | Button `variant="link"` (text-only, no height/padding, underline on hover, inherits font size, ellipsis-safe) | No focus-visible style today. |
| shell/statusbar/scopeCrumbs.module.css:60-72 (`.hierarchy .crumbBtn`) | same button in hierarchy mode: full-width row with hover wash | NEW | **ListRowButton** `{children; onClick; selected?; disabled?}` | See the ListRowButton group below. |
| shell/rail/RailRow.tsx:688 | raw `<button>` "Retry" on the project row | SWAP | `Button size="xs" variant="quiet"` with `onClick={(e)=>{e.stopPropagation(); retry();}}` | Currently a fully unstyled UA button. Keep `stopPropagation` (sits inside an activatable text column). Should probably also take `tabIndex={-1}` like the row's other controls (roving-tab contract). |
| shell/rail/RailRow.tsx:198 | `<span onClick>` `.chevronButton` (TrailingChevron) | BESPOKE | n/a | Deliberately mouse-only, aria-hidden, non-focusable; Tree owns Left/Right. `.chevronButton` name is misleading (not a button). |
| shell/rail/RailRow.tsx:565 | `<button tabIndex=-1>` session title inside HoverCard | BESPOKE | n/a | HoverCard function-child trigger that proxies focus to the owning treeitem on mousedown. Tree owns focus. |
| shell/rail/RailRow.module.css:290-303 | `.sessionTitle > span, .sessionTitle .label` (resets UA button + targets HoverCard's wrapper `<span>`) | MISUSE | EXTEND HoverCard `block?: boolean` (wrapper fills/shrinks with its flex parent) | `.sessionTitle > span` styles HoverCard's internal wrapper element. Breaks if HoverCard's wrapper changes tag/depth. |
| shell/rail/RailRow.tsx:601, 684, 763 | `<span onClick={info.activate}>` (text column / project label / host label) | BESPOKE | n/a | Mouse shortcut for the treeitem's Enter; Tree owns role/keyboard. |
| shell/rail/RailRow.tsx:823-826 | `<span onClick>` `.overflow` "+N older" | BESPOKE | n/a | Same treeitem-owned activation. |
| shell/rail/RailRow.tsx:831 | `LoadingRow` "Loading…" text with role=status | SWAP | `Skeleton lines={1}` | Skeleton already announces "Loading" once. Check that the 1-line bar fits the 28px tree row height. |
| shell/rail/RailRow.tsx:222-238 (ActionsMenu); shell/rail/Rail.tsx:418-424 (pinned section menu); shell/sessionMenu/SessionMenu.tsx:236-245 | Menu trigger = `⋯` glyph + `<span class=srOnly>` label; three local `.srOnly` copies (RailRow.module.css:456, Rail.module.css:~392, sessionmenu.module.css:3) | EXTEND | Menu `label?: string` (becomes trigger `aria-label`) + `icon?: ReactNode` / `iconOnly` mode with square IconButton geometry | Three hand-made visually-hidden recipes duplicate `widgets/internal/VisuallyHidden` (which already exists). The Menu prop removes all three. |
| shell/rail/RailRow.module.css:65-79, 162-164 | `.actions button`, `.actions button[aria-haspopup="menu"]` (padding override, tap-floor min-size, glyph centering) | MISUSE | EXTEND Menu: `size: "xs"\|"sm"` square icon-only trigger with the IconButton tap floor built in, plus `align="end"` | Reaches into both Menu's trigger and IconButton's DOM by tag/attribute. The coarse-pointer tap floor belongs in the widget (IconButton already has one at ≤899px). |
| shell/rail/RailRow.module.css:145-146, 180 | `.actions:has(button[aria-expanded="true"])` | MISUSE (minor) | Menu exposes `data-open` on its root, or an `onOpenChange` callback | Relies on Menu trigger's internal ARIA attribute to keep the row revealed. Defensible because aria-expanded is a public contract; still a DOM reach-in. |
| shell/rail/RailRow.tsx:610-640; RailRow.module.css:305-369 | `.contextCard` / `.contextRow` `<dl>` key/value list inside HoverCard (eyebrow + status + rows, per-row mono) | NEW | **PropertyList** `{rows: {label; value; mono?}[]}` (read-only `<dl>` grid) | InspectorCard is the nearest widget but is an editable inspector with card chrome. Other `<dl>` key/value sites outside this area: panes/session/chrome/DetailsPanel.tsx, settings sections, NotificationCard, EntityRef. |
| shell/SessionStatusIndicator.tsx:39-56; SessionStatusIndicator.module.css (whole file) | hand-rolled status dot (8px spinner = working, dot = needs-you, diamond = failed) | EXTEND | StatusDot `variant="signal"` (spinner/diamond/dot shapes, renders nothing for idle/ended), moved into widgets/statusdot | Visible drift today: PaneTab.tsx:73 uses StatusDot for the *same* three states, so a tab and its rail row show different glyphs for one session. Consumers: RailRow.tsx:135, ActivityPeek.tsx:104. Moving it retires a token-contract semantic-var exception (token-contract.test.ts:572). |
| panes/zoom/ActivityPeek.tsx:103-108; zoom.module.css:97-111 | `.stateIndicator` wrapper + `.stateLabel` that `composes` visuallyHidden then overrides `clip`/`margin` | MISUSE | Use `widgets/internal/VisuallyHidden` as is (or StatusDot's own accessible name once extended) | Overriding the composed recipe's `clip` defeats the shared helper. The dot's own `role=img aria-label` already names the state, so the label may be redundant. |
| shell/rail/Rail.tsx:198-206; Rail.module.css:122-212 | `<h3><button aria-expanded class=sectionDisclosure>` + trailing Chevron + action slot | NEW | **DisclosureHeading** `{level; label; open; onOpenChange; action?; eyebrow?}` (the body is rendered separately by the caller) | Disclosure can't fit: it's `<details>/<summary>`, and a summary can't live inside a heading. Used by all 4 rail sections. |
| shell/rail/Rail.module.css:~385 | `.section [role="group"] { border-left }` | MISUSE | EXTEND Tree `guides?: boolean` (per-level hairline guide rail) | Reaches into Tree's nested group DOM by role. The comment admits it. |
| shell/rail/Rail.tsx:2007; Rail.module.css (`.newSession {display:grid}`) | wrapper div that only stretches the primary Button | MISUSE | EXTEND Button `fullWidth?: boolean` | Classic override wrapper. |
| shell/rail/Rail.tsx:2117, 2153, 2172; PinSectionPicker.tsx:109, 118; SessionMenu.tsx:255, 291, 319; ForceStopDialog.tsx:36; railDialog.module.css:13; sessionmenu.module.css (`.footer`) | `.dialogActions` / `.footer` flex wrapper inside `Dialog footer` | MISUSE | Pass the Buttons straight to `footer` | dialog.module.css `.footer` is already `display:flex; justify-content:flex-end; gap:var(--space-2)`. The 9 wrappers are redundant. |
| shell/rail/Rail.tsx:2147-2163 (section delete), 2166-2182 (project delete) | Dialog + Cancel/danger Button pair | SWAP | `ConfirmDialog open title confirmLabel="Delete section"/"Delete" onConfirm onCancel` | The binding confirm contract exists as a widget. |
| shell/sessionMenu/SessionMenu.tsx:286-308 (shutdown), 314-334 (delete); ForceStopDialog.tsx:29-50 | Dialog + Cancel/danger Button pair | SWAP | `ConfirmDialog ... busy={busy}` | ForceStop ignores close while busy. ConfirmDialog's onCancel can gate on busy, but Escape/scrim also route to onCancel, so keep that guard. SessionMenu's Cancel isn't disabled while busy today; ConfirmDialog would disable it (behaviour change, arguably a fix). |
| shell/rail/Rail.tsx:2127-2144; PinSectionPicker.tsx:129-176; SessionMenu.tsx:274-283 | `<label class=dialogField/field>` + Input + `<p class=pickerError role=alert>` | SWAP | `FormRow label="Section name"/"Name" htmlFor=… error={…}` wrapping `Input aria-describedby={`${id}-error`}` | FormRow renders the error as `${htmlFor}-error` with role=alert. Callers must switch their `aria-describedby` ids to that. `.pickerError` uses raw `--danger` (railDialog has a token exception for it). |
| shell/rail/PinSectionPicker.tsx:149, 160; railDialog.module.css:19-51 | raw `<button class=pickerItem>` full-width list rows | NEW | **ListRowButton** | Choosing a destination from a list. RadioGroup + confirm would add a step; a row button is the right shape. |
| shell/rail/RailHost.tsx:77; RailHost.module.css:15-39 | raw `<button class=chip>` "☰" + Badge (show sidebar) | EXTEND | IconButton `badge?: {count; tone}` → `<IconButton label="Show sidebar" icon={<SidebarIcon/>} variant="secondary" badge={{count:needsYou,tone:"attention"}}/>` | Glyph drift: the same action is ☰ here, `SidebarIcon` on the rail's Hide button, and a third hand-drawn `SessionsIcon` in TreeDrawer. |
| shell/mobile/TreeDrawer.tsx:25-31; treedrawer.module.css | `.triggerWrap` + `.badgeOverlay` positioning a Badge over IconButton | EXTEND | IconButton `badge` (same prop) | Wrapper exists only to decorate a widget. |
| shell/singlePane/global.css:39 | `[data-single-pane] button[aria-label="Sessions"] {display:none}` | MISUSE | StackHost should skip rendering `<TreeDrawer>` in single-pane mode | Hides a widget by accessible name. Likely bug (unverified): only the button is hidden, so TreeDrawer's `.badgeOverlay` Badge sibling would still render when needsYou>0. |
| shell/rail/RailResizeHandle.tsx:128 | `div role=separator` with pointer/key handlers | BESPOKE | n/a | Drag handle. |
| shell/activitybar/activitybar.module.css:63-80, 207-217 | `.tabs [role="radiogroup"]`, `.tabs [role="radio"]`, `.tabs [role="radio"] > span` | MISUSE | EXTEND SegmentedControl `wrap?: boolean` (options wrap and labels may be two-line; tap floor built in) | Reaches into SegmentedControl's internals three levels deep. The 207-217 block re-declares 68-80 unconditionally (looks like a lost `@media` wrapper), so the 28px floor on line 72 is always overridden. |
| shell/activitybar/activitybar.module.css:40-46 | `.sidebar > div, .sidebarMobile > div` | MISUSE | EXTEND FocusScope `className?` (or `asChild`) | Targets FocusScope's unstyled wrapper `<div tabIndex=-1>` by structure. |
| shell/activitybar/activityRows.tsx:90, 120; activitybar.module.css:105-120 | raw `<button class="row rowBtn">` full-width activity rows | NEW | **ListRowButton** | No focus-visible style on `.rowBtn` (relies on UA ring). |
| shell/activitybar/activityRows.tsx:69, 102, 134; activitybar.module.css:122-145 | typed `⌘` / `$` glyphs tinted via `.glyphAlive/Attention/Danger/Quiet` | EXTEND | ToolIcon `tone?: "alive"\|"attention"\|"danger"\|"quiet"`, using `kind="delegate"` / `kind="job"` | A drawn ToolIcon already exists for delegate/job. The typed glyphs fall back to system fonts (the same problem RailRow's WatchGlyph comment calls out). Retires another semantic-var exception. |
| shell/activitybar/AgentsTab.tsx:70-73; activitybar.module.css:181 | Button + Chevron that toggles the disclosure store (`toggleDisclosure(foldID)`) | SWAP | `Disclosure id={foldID} summary={`Inactive subagents (${n})`}` | Sibling drift: JobsTab.tsx:53 renders its equivalent "completed jobs" fold with Disclosure. Risk: `ActivityPageBoundary enabled={foldOpen…}` reads the same store, so keep that read. |
| shell/activitybar/AgentsTab.tsx:58; JobsTab.tsx:31; WatchesTab.tsx:34; TasksTab.tsx:28 | `<span class=emptyNote>Loading …</span>` | SWAP | `Skeleton lines={2}` | 4 sites. |
| shell/activitybar/AgentsTab.tsx:56, 63, 107; JobsTab.tsx:29, 35, 75; WatchesTab.tsx:32, 37, 54; activitybar.module.css:191 | `<span class=emptyNote>` unavailable / none / updating notes | EXTEND | EmptyState `size="compact"` (caption type, left-aligned, no vertical padding) | 9 sites. EmptyState today is a centered pane-title block, which is wrong for a 320px sidebar list. |
| shell/palette/CommandPalette.tsx:890-896; commandpalette.module.css:214-231 | local `Empty` component (`.empty/.emptyTitle/.emptyBody`) | EXTEND | EmptyState `size="compact"` | Reimplements EmptyState beside an import list that already has the widget barrel. |
| shell/activitybar/ActivitySidebar.tsx:163; panes/zoom/ActivityPeek.tsx:48 | `IconButton icon="×"` (text glyph) | EXTEND | Export `CloseIcon` (exists at widgets/dialog/CloseIcon.tsx, not in the barrel), then `icon={<CloseIcon/>}` | Dialog/Sheet close uses the drawn icon. These two use a font glyph. |
| panes/zoom/ActivityPeek.tsx:43-49; zoom.module.css:216-235 | `div role=dialog` + `.peekHead` (title + close) + `.peekBody` (max-height scroll) inside Popover | EXTEND | Popover `title?: string` (renders header + close IconButton, sets `role=dialog` + `aria-labelledby`) | One site in area. Saves every rich popover from rebuilding a header. |
| shell/palette/CommandPalette.tsx:649-655; commandpalette.module.css:15-44 | `.pill` + raw `<button class=pillBack>×</button>` | EXTEND | Chip `onRemove={exitArgsMode}` + new Chip `removeLabel?: string` (keeps "back to command list") | Chip derives "Remove <title>" today, which changes the accessible name tests may query. The pill's `--accent-bg` fill would become a Chip tone, so a design call is needed on which tone. |
| shell/palette/CommandPalette.tsx:656-680; commandpalette.module.css:46-60 | raw `<input role=combobox>` with ref/autoFocus/onKeyDown/aria-activedescendant | EXTEND | Input: `forwardRef` + rest-spread of `aria-*`, `role`, `onKeyDown`, `autoFocus`; `size="lg"` for the palette's taller field | Combobox widget doesn't fit: it debounces 150ms and owns its own listbox. The palette's listbox is its own. |
| shell/palette/CommandPalette.tsx:919-931 | raw `<button role=option>` palette rows | BESPOKE | n/a | Options of an aria-activedescendant listbox; real focus stays in the input. Visually the same family as ListRowButton, so share its CSS tokens if that widget lands. |
| shell/palette/CommandPalette.tsx:840-845; commandpalette.module.css:194-212 (`.helpRow/.helpKeys/.helpDesc`); shell/cheatsheet/CheatsheetOverlay.tsx:137-155 | key-chord + description rows built twice | NEW | **ShortcutList** `{rows: {keys: string[][]; label}[]}` composing KeyHint | `.helpKeys` also sets color/font-size around KeyHint (styling a widget by inheritance). Use KeyHint `compact` instead. |
| shell/palette/commandpalette.module.css:90; cheatsheet.module.css (`.groupTitle`); Rail.module.css (`.staticSectionLabel`); RailRow.module.css:319 (`.contextKind`) | uppercase caption eyebrow label recipe (4 copies) | NEW | **Eyebrow** / SectionLabel `{as?; children}` | Typography rather than a control, but four hand copies of the same `--tracking-eyebrow` recipe. |
| shell/holdhints/HoldHints.tsx:138; holdhints.module.css:27 | `.chip` floating key-hint bubbles | BESPOKE | n/a | Many simultaneous decorative bubbles anchored to arbitrary DOM, aria-hidden, pointer-events none. Tooltip is a single hover/focus trigger. Already composes KeyHint. |
| shell/DockRegion.tsx:50-66; shell/rail/index.tsx:120-139 | EmptyState `action={<span>…{" "}…</span>}` to lay out Retry + Reload | EXTEND | EmptyState action slot lays out multiple children with `gap` (or `actions: ReactNode[]`) | The `{" "}` text node is the spacing. The two chunk-failure fallbacks are also near-duplicates of each other (journal-worthy). |
| shell/PaneTab.module.css:20; shell/DockHost.module.css:9; shell/dockview-theme.css; shell/singlePane/global.css:29 | `:global(.dv-…)` selectors | BESPOKE | n/a | Third-party dockview chrome, no widget equivalent. |
| panes/zoom/CascadeSpine.tsx:15; zoom.module.css:136-162 | raw `<button class=spinePop>` full-height vertical-text column | BESPOKE | n/a | Rotated vertical label filling a 52px spine. No widget geometry fits. Has its own focus ring. |
| panes/zoom/CascadeColumn.tsx:32 | `Button variant="quiet" size="xs"` "Open conversation" | EXTEND | OpenButton with visible words in button form (`word` only works in the anchor form today) | §3 says every open-out site routes through OpenButton. Confirm this counts as open-out: it switches the view to the conversation and sometimes returns from the cascade. |
| panes/doc/DocPane.tsx:79; docpane.module.css:20-27; widgets/mermaid/index.tsx:99; mermaid.module.css:3-22 | raw `<button>` wrapping media (image / diagram) that opens a lightbox, with UA-chrome resets | NEW | **ZoomableMedia** `{label; onOpen; children}` (block button, reset chrome, zoom-in cursor, focus ring) | 2 sites. Neither has a `:focus-visible` style. Mermaid uses `cursor:pointer`, doc uses `zoom-in` (drift). |
| panes/doc/DocPane.tsx:84 | `Dialog` default size for the image lightbox | MISUSE | `Dialog size="large"` | dialog/index.tsx documents `large` as exactly the lightbox case. |
| panes/doc/DocPane.tsx:68; docpane.module.css:1-11 | `<pre class=pre>` plain-text file body | SWAP | `CodeBlock text={content.text}` (optionally `showLineNumbers`) | CodeBlock already wraps (pre-wrap, overflow-wrap:anywhere) and adds copy. Check that its font size (caption vs body) is acceptable for a full-pane reader. |
| widgets/mermaid/DiagramViewer.tsx:121, 124 | raw unstyled `<button>` "Show source" / "Reset zoom" in footer | SWAP | `Button variant="quiet" size="sm"` (the source toggle could instead be `SegmentedControl label="View"` with Diagram/Source) | They currently render as UA buttons beside a styled CopyButton. |
| widgets/mermaid/DiagramViewer.tsx:113-120; mermaid.module.css:52-69 | uses internal `OverlayPanel` + `dialog.module.css` `.panel` directly | EXTEND | Dialog `bodyClassName?` (parity with Sheet), then `Dialog size="large"` | `.viewerPanel` re-derives Dialog's large geometry. The only reason to bypass Dialog is the body class. |
| widgets/mermaid/DiagramViewer.tsx:133-136; mermaid.module.css:99-109 | `<pre class=source><code>` | SWAP | `CodeBlock text={source}` (drops the separate CopyButton) | CodeBlock wraps lines where `.source` uses `white-space:pre`. A long-line diagram source would wrap instead of scrolling. |
| widgets/mermaid/DiagramViewer.tsx:139 | pointer-driven `.zoomSurface` | BESPOKE | n/a | Pan/zoom canvas. |
| Documented-API drift (props used but absent from §3): ActivitySidebar.tsx:168-176 SegmentedControl `hideLabel`, option `accessibleLabel`; RailRow.tsx:561 HoverCard `sideAnchor`; RailRow/Rail/SessionMenu Menu `triggerTabIndex`, `variant`, separators; CascadeColumn.tsx:32 Button `size="xs"` (also `secondary`, `dangerQuiet` variants exist); DocPane Dialog `size`; MobilePanel.tsx:30 Sheet `side="left"`, `size="wide"`, `panelClassName`; KeyHint `compact`; OpenButton actual shape (`describedBy`, no `iconOnly`/`size`); ConnectionBanner uses `Banner` (not in §3 at all) | widget props outside documented API | MISUSE | Update §3 to the shipped surface, or remove the props | MobilePanel's `panelClassName` (`overflow:hidden`) is also a styling override of Sheet's panel. |

## Proposed widget API changes

EXTEND (existing widgets):
- **Menu** `label` (trigger aria-label) + icon-only square `size` with tap floor + `align="end"`: 3 sites (RailRow actions, rail pinned-section, SessionMenu). Removes 3 `.srOnly` copies and the `.actions button…` reach-ins.
- **Button** `fullWidth`: 1 (rail New session). `variant="link"`: 1 (breadcrumb).
- **IconButton** `badge: {count; tone}`: 2 (RailHost show-sidebar, TreeDrawer).
- **StatusDot** `variant="signal"` (absorbs SessionStatusIndicator): 2 consumers + PaneTab alignment.
- **EmptyState** `size="compact"`: 10 (9 activity notes + palette Empty). Multi-action layout: 2 (DockRegion, rail chunk failure).
- **SegmentedControl** `wrap`: 1 (activity sidebar tabs).
- **Tree** `guides`: 1 (rail).
- **HoverCard** `block`: 1 (rail session title).
- **FocusScope** `className`: 1 (activity sidebar).
- **ToolIcon** `tone`: 3 glyph sites (agent, job, watch rows).
- **Chip** `removeLabel`: 1 (palette args pill).
- **Input** forwardRef + rest-spread (role/aria/onKeyDown/autoFocus) + large size: 1 (palette).
- **Popover** `title` header with close: 1 (activity peek).
- **Dialog** `bodyClassName`: 1 (mermaid viewer).
- **OpenButton** visible words in button form: 1 (cascade "Open conversation"), pending a semantics check.
- **CloseIcon** exported from the barrel: 2 (`icon="×"` sites).

NEW:
- **ListRowButton** (full-width row button, hover wash, focus ring, selected/disabled): 4 sites (PinSectionPicker ×2, activity AgentRow/JobRow ×2, ScopeCrumbs hierarchy). Palette option rows should share its styling.
- **CountButton** (glyph + label + count quiet chip-button, compact/column modes): 2 (status bar, zoom activity controls).
- **DisclosureHeading** (heading-wrapped aria-expanded button + trailing chevron + action slot): 1 component, 4 rail sections.
- **ZoomableMedia** (media block that opens a lightbox): 2 (doc image, mermaid diagram).
- **PropertyList** (read-only `<dl>` key/value grid): 1 here (rail context card); several more outside this area.
- **ShortcutList** (KeyHint rows): 2 (cheatsheet, palette help).
- **Eyebrow** label: 4 CSS copies.

## Risks

- **Roving focus in the rail.** Every control inside a rail row must stay `tabIndex=-1` (Menu `triggerTabIndex`, IconButton `tabIndex`). Any Menu/IconButton extension must keep forwarding it, and the Retry swap must add it. Menu's key handler `stopPropagation` keeps keys from reaching Tree; a Menu refactor must keep that.
- **stopPropagation.** The project-row Retry and TrailingChevron stop clicks from reaching `info.activate`. OpenButton owns `stopPropagation` internally, so switching CascadeColumn to it is harmless, but any row-button swap inside activatable rows must keep it.
- **Reveal-on-hover CSS.** `.actions:has(button[aria-expanded="true"])` and `.rightSlot` visibility depend on Menu's trigger DOM. Changing Menu's trigger element or attribute silently breaks keep-open-while-menu-open (layoutguard case `rail-row-chevron-actions-overlap` pins it).
- **Accessible names used as selectors.** `singlePane/global.css` hides `button[aria-label="Sessions"]`. Renaming the TreeDrawer label (or adding a badge prop that changes the DOM) breaks single-pane hiding. Chip `onRemove` would rename the palette pill's "back to command list". Tests and the hold-hints/global CSS query these names.
- **Test ids / data attributes.** `data-testid` values (rail-row-title, rail-chevron, rail-row-overflow, rail-*-status-spinner/dot via SessionStatusIndicator `testIdPrefix`, statusbar, cascade-activity-controls, doc-image) and `data-activity-tab`, `data-pane-id`, `data-session-ref`, `data-rail-toggle`, `data-search-trigger`, `data-activity-anchor`, `data-session-actions-ref` must survive. New widgets need rest-spread for `data-*`.
- **ConfirmDialog swap.** ForceStopDialog blocks close while busy, and ConfirmDialog routes Escape/scrim to onCancel, so the guard must move into onCancel. SessionMenu's Cancel becomes disabled while busy.
- **FormRow swap.** Error ids change to `${htmlFor}-error`. Inputs' `aria-describedby` must follow, and `role=alert` announcements stay.
- **Disclosure swap in AgentsTab.** ActivityPageBoundary's `enabled` reads `foldOpen` from the same disclosure store. Disclosure renders the body only when open (the transcript fold does), so paging behaviour should hold. Verify with the activity scroll-progress anchors.
- **Popover triggers.** CountButton (zoom) and anything put in a Popover/Tooltip trigger must forward refs and spread `aria-expanded`/`aria-haspopup`.
- **Skeleton in tree rows.** Skeleton's bar height and `role=status` inside a `role=treeitem` row need a layout and a11y check.
- **CodeBlock swaps** change wrapping (mermaid source) and font size (doc pane), and add a copy button.
- **Status glyph unification** changes what users see on the rail/peek (or on tabs, depending on direction). Pick the canonical look first.

## Other observations (not counted)

- Rail header icon buttons are inconsistent: only Search has a Tooltip. Settings and Hide sidebar have none.
- `activitybar.module.css:207-217` unconditionally overrides `68-80`, so the `max(28px, …)` floor never applies (likely a dropped `@media`).
- StatusBar chips and `.rowBtn`/`.pickerItem`/`.crumbBtn` lack explicit `:focus-visible` styling, or have it inconsistently.

---

## Area: session

### Design-system drift catalog: `src/panes/session/**`

Scope: `cmd/evener-hub/frontend/src/panes/session/**` (excluding `*.test.tsx`, `*TestUtils*`). Paths below are relative to `src/panes/session/`.
Widget API reference: `docs/web-ui/design-system.md` §3, checked against the shipped `src/widgets/*/index.tsx` (the shipped API is wider than the doc in places: Button has `secondary`/`dangerQuiet`/`xs`, Menu has `variant`, Switch has `pending`, Textarea has `seamless`/`minLines`, KeyHint has `compact`, and RadioGroup, Popover, Banner, PromptCard, Timestamp exist).

| file:line | element/class | classification | target widget + props / proposed change | notes |
|---|---|---|---|---|
| composer/AttachmentTile.tsx:82 | `<button .imageOpenButton>` wrapping a thumbnail `<img>` | NEW | **ImageThumbnail** `{src, alt, onOpen, size?: "sm"\|"lg", caption?}` | Same pattern as flow/ImageGallery.tsx:136. Both open a `Dialog size="large"` lightbox, so the widget could own the lightbox too. |
| composer/AttachmentTile.tsx:99 | `<button .removeImageButton>` (28px round, hover-reveal) | SWAP | `IconButton label={\`Remove ${name}\`} icon={<RemoveIcon/>} variant="secondary" size="sm"` in a positioning span. The tile keeps the hover/focus-within reveal on the span | Hand-rolled focus ring and hover colors. The layoutguard case `hk8v-remove-button-focus-visible` measures this button. |
| composer/Composer.tsx:1692 | hidden `<input type="file">` driven by `fileInputRef.click()` | EXTEND | **Dropzone**: add `accept?`, `multiple?` and an imperative `openPicker()` handle so the pick path and the drop path share one `onFiles` | Composer already wraps the card in `Dropzone`. Low priority. |
| composer/composer.module.css:192 | `.composer :global(button):has(> .submitLabel)` and the `@container .submitLabel` hide | MISUSE | Reaches into Button's DOM to add a tap-width floor and collapse the label. Correct usage: **Button** `collapseLabel` (EXTEND, see below) | The comment says the spawn pane's Start button does the same trick (outside this area). |
| composer/SlashCompletionMenu.tsx:47,55 | `role=listbox` div + `<button role=option>` rows, `.menu/.option/.hint` | NEW | **Listbox** `{id, items, activeIndex, onSelect, renderOption}`, an option list driven by an external `aria-activedescendant` (the focus stays in the editor) | 1 site in this area. Outside it: shell/palette/CommandPalette, widgets/modelCatalog, widgets/pathfield. The `onMouseDown preventDefault` blur guard has to move into the widget. |
| composer/CurrentWork.tsx:67 | `<button .value.link>` (task, underlined text) | NEW | **LinkButton** `{children, onClick, aria-*}`: an inline, wrapping, underlined text action | Uses `title=` for the full text. Would become `Tooltip`. |
| composer/CurrentWork.tsx:86 | `<button .value.link>` (goal) | NEW | **LinkButton** | Same as above. |
| composer/CurrentWork.tsx:65 | `<span .dot>` (6px alive ring) | EXTEND | **StatusDot** `state="working" decorative` | StatusDot always names itself. This dot is decorative because the row carries a "Task" label and its own live region. |
| composer/RepoLocation.tsx:177 | `<a .link>` (forge mark + owner/repo#branch, new tab) | BESPOKE | n/a | A real external hyperlink, so a native `<a>` is correct. The `title=` tooltip is still drift. |
| composer/askDock/AskDock.tsx:308,313 | `role=tablist` div + `<button role=tab .tab>` with roving tabindex and arrow-key handler | NEW | **Tabs** `{label, tabs: {id, label, badge?}[], value, onChange, panel}` (tablist + tabpanel + roving focus) | 1 site in this area. SegmentedControl is a radiogroup with no tabpanel wiring, so it doesn't fit. |
| composer/askDock/AskDock.tsx:296 | `.batch` amber envelope (`color-mix(--attention 24%)`) | EXTEND | **Card** `tone="attention"` (bordered attention envelope) | Same idea as sandboxEscalation `.card`, but built on different tokens (`--attention-bg` there). That mismatch is drift. |
| composer/askDock/AskQuestionCard.tsx:153 | raw `<input type=radio\|checkbox>` option rows | EXTEND | **RadioGroup**: add `option.detail`, `option.recommended`, `labelledBy` (in place of the required visible `label`), `multiple` (checkbox mode), `name` | The header comment says no RadioGroup exists. That is stale: it is in the barrel now. |
| composer/askDock/AskQuestionCard.tsx:179 | "Something else…" radio that can be deselected | EXTEND | **RadioGroup** `otherOption={{label, active, onToggle}}` (a deselectable trailing option) | Same EXTEND as above. |
| composer/askDock/AskQuestionCard.tsx:235 | raw `<input type=text .textInput>` | EXTEND | **Input**: forward `ref`, `aria-labelledby`, `data-*` (rest-spread like Button) | `data-ask-free-input` is how AskDock's Enter handling finds the field. |
| composer/queue/QueueStrip.tsx:116 | `ActionButton` = `Tooltip` around a `disabled` IconButton for `disabledReason` | MISUSE | A natively `disabled` button can't take focus, so the reason is mouse-only. Use **IconButton/Button** `disabledReason` (EXTEND: renders `aria-disabled` + Tooltip) | 5 call sites (lines 550, 576, 590, 699, 707). The icons are text glyphs (`⇧ ✎ ✕`), off the line-art icon contract. |
| pending/PendingChips.tsx:53 | `<li .chip>` (surface-2 pill, opacity .6, ellipsized text) | EXTEND | **Chip** `truncate` + `pending` (dimmed) | Chip has a fixed 24px height and no ellipsis today. |
| Session.tsx:170 | `<div role=alert>` with text + raw `<a>` "Open owning session" + Button | EXTEND | **Banner** `tone="attention" message action={{label, onClick, inFlight: refreshing}}` + new `link?: {label, href}` | Banner exists in `widgets/banner` but is **not in the barrel**. Add it. |
| Session.tsx:176 | raw `<a>` "Open owning session" | EXTEND | Banner `link` (above) | Counted inside the Banner row's change. |
| session.module.css:114 | `.coldStart > div { max-width }` | MISUSE | Reaches into Skeleton's root div. Put the measure on a wrapper that coldStart.tsx owns | |
| transcript/ToolRow.tsx:899 | `<button .bodyTrigger>` (body disclosure overlay) | BESPOKE | n/a | Transcript grammar: overlay triggers coexist with nested links/OpenButtons, a two-level disclosure, scroll/focus anchors. |
| transcript/ToolRow.tsx:936 | `<button .trigger.intentOverlayTrigger>` | BESPOKE | n/a | Same as above. |
| transcript/ToolRow.tsx:974 | `<button .trigger>` (intent line) | BESPOKE | n/a | Same as above. |
| transcript/ToolRow.tsx:1002 | `<button .trigger>` (intent-less row) | BESPOKE | n/a | Same as above. |
| transcript/ToolRow.tsx:466 | `<a>` linkified URL inside the summary | BESPOKE | n/a | Inline hyperlink in running text. |
| transcript/toolcallitem.module.css:149,281 | `.summaryLine a, button, input…{pointer-events:auto}` | BESPOKE | n/a | Overlay opt-in. Restores hit-testing only, never restyles a widget. |
| transcript/TurnBlock.tsx:150 | hand-rolled `<details>/<summary .intentGroupSummary>` on disclosureStore | EXTEND | **Disclosure** `id defaultOpen={fallback} variant="inline"` | No chevron today. |
| transcript/ToolRunGroup.tsx:59 | `<details>/<summary>` | BESPOKE | n/a | Exempt by §3 (transcript grammar). |
| transcript/messages/SteeringDivider.tsx:63 | `<details>/<summary>` + Chevron + rail icon | EXTEND | **Disclosure** `id variant="inline" leading={<SteeringGlyph/>} readOptions={{ignoreBaseline}}` | Testids `steering-chevron` and `steering-rail-icon` are asserted. |
| transcript/messages/SystemNoticeItem.tsx:147 | scaffold `<details>/<summary>` | EXTEND | **Disclosure** `id variant="inline" readOptions` | No chevron, so it doesn't look expandable (visible drift). |
| transcript/messages/SystemNoticeItem.tsx:355 | system-event group `<details>/<summary>` | EXTEND | **Disclosure** `id variant="inline"` | No chevron. |
| transcript/messages/ThinkBlock.tsx:272 | `<details>/<summary>` + Chevron hugging the label | EXTEND | **Disclosure** `id variant="inline" leading={thoughtIcon}` | Label ellipsizes so the chevron stays visible; `inline` must keep that. |
| transcript/messages/NotificationCard.tsx:559 | `<summary .head>` that contains OpenButton + EntityRef trigger | EXTEND | **Disclosure** `trailing` slot (rendered as a sibling of `<summary>`, not inside it) + `variant="inline"` | Interactive controls nested in a `<summary>` (implicit role button). |
| transcript/messages/NotificationCard.tsx:448 | click-guard `<span onClick>` (stopPropagation + preventDefault) | MISUSE | Exists only because a control is nested inside the summary. Goes away with Disclosure `trailing` | biome-ignore ×2. |
| transcript/messages/NotificationCard.tsx:647 | uncontrolled `<details .raw>` "Raw notification" | SWAP | **Disclosure** `open/onOpenChange` (local state) `summary="Raw notification"` | |
| transcript/messages/MemoryContextItem.tsx:47 | uncontrolled `<details .source>` "Source" | SWAP | **Disclosure** `open/onOpenChange` `summary="Source"` | Testid `memory-context-source`. |
| transcript/messages/UserMessageItem.tsx:93 | `IconButton title="Fork from here"` | MISUSE | A native `title` duplicates `label`. Wrap in **Tooltip** `label="Fork from here"` | |
| transcript/tools/subagentModule.tsx:286 | `IconButton title=… aria-expanded` | MISUSE | Same: use **Tooltip** in place of `title` | |
| transcript/tools/jobWatch.tsx:93 | `<details>` with no body, used as a "Show full note / Show less" toggle | MISUSE | Not a disclosure (the content clamps elsewhere). Use **LinkButton** `aria-expanded` (NEW) | `.disclosureSummary` class. |
| transcript/tools/jobWatch.tsx:293 | full-width `<button .row>` disclosure (Chip + EntityRef + phrase) | SWAP | **Disclosure** `open/onOpenChange summary={…}` | EntityRef `embedded` stays non-focusable. Hover wash uses an ad-hoc `color-mix`. |
| transcript/tools/jobWatch.tsx:797,803,820 | `.card` (surface-1, edge border, pane radius) | EXTEND | **Card** `variant="outlined"` | Card is flat/borderless only. |
| transcript/tools/sandboxEscalation.tsx:92 | `.card` attention envelope | EXTEND | **Card** `tone="attention"` | role=group + Mod+Enter keydown stays on a wrapper. |
| transcript/tools/delegateStatus.tsx:394 | `<span .toolChip>` (mono pill) | EXTEND | **Chip** `mono` | Reimplements Chip next to other Chip usage. |
| transcript/tools/webTools.tsx:83 | `<a>` result link | BESPOKE | n/a | Inline hyperlink. |
| transcript/TurnFailureEndCap.tsx:620 | `<button .hintSummary>` "What can I do?" toggle + `.hint` | SWAP | **Disclosure** `open={hintOpen} onOpenChange summary="What can I do?"` | Today the hint renders inline in the head flex row before Retry. Layout changes. |
| transcript/flow/LoadOlderRow.tsx:143 | `<button .retry>` (accent underlined text) | NEW | **LinkButton** | Testid `load-older-retry`. |
| transcript/flow/ImageGallery.tsx:136 | `<button .thumb>` + img + caption | NEW | **ImageThumbnail** | See AttachmentTile:82. |
| transcript/flow/NewContentPill.tsx:83 | `<button .pill>` | SWAP | **Button** `variant="secondary" size="sm" icon={<Chevron direction={dir}/>}` with Chip/Badge children | Testid `new-content-pill`. Coarse-pointer `--tap-min` is handled by Button's phone rule. |
| composer/SkillEditor.tsx:184 / skilleditor.module.css:27 | ProseMirror skill atom `.skill` (chip look) | BESPOKE | n/a | ProseMirror `toDOM` node, so no React widget can mount there. Its token choice should mirror Chip's. |
| composer/SkillEditor.tsx:392 | contenteditable editor | BESPOKE | n/a | ProseMirror editor that hosts atoms. Textarea can't. |
| chrome/ActivityTree.tsx:795 | hand-rolled `role=tree`, roving tabindex, Arrow/Home/End handler | EXTEND | **Tree**: add `renderDetail(node, info)` (a detail strip inside the tree under a row), a `ref` handle `{focusRow(id)}`, per-node `aria-label`/`aria-description` | A widget reimplemented beside the real one. `releaseModifierKeys` already exists on Tree. |
| chrome/ActivityTree.tsx:277,291 | fold row `<div role=treeitem onClick>` + `<button .rowToggle tabIndex=-1>` | EXTEND | **Tree** `renderRow` + `info.toggle` | Covered by the Tree EXTEND. |
| chrome/ActivityTree.tsx:338,355 | RowShell `<div role=treeitem onClick>` + `<button .rowToggle>` | EXTEND | **Tree** | Same. `stopPropagation` on the toggle. |
| chrome/activitypanel.module.css:60 | `.denseRow > [data-open-shell] > button` | MISUSE | Reaches past OpenButton's documented shell hook into its inner IconButton. Use **OpenButton** `fill="row"` (EXTEND) | |
| chrome/ActivityRowDetail.tsx:168 | `.timelineDot` | BESPOKE | n/a | Timeline marker, not a status. |
| chrome/ModelSwitchTrigger.tsx:264 | raw `<button .trigger>` (value + down Chevron + sr-only action) | NEW | **PickerTrigger** `{value, actionLabel, onClick, disabled, compact?}`, a quiet value-readout trigger for Popover/Sheet pickers | The comment cites the same pattern in widgets/pathfield and widgets/modelCatalog. |
| chrome/ModelSwitchTrigger.tsx:310 | `<span .triggerWrapper>` that only mirrors Popover's DOM depth | MISUSE | A wrapper that exists so a cross-component selector keeps matching. Goes away with PickerTrigger `compact` | |
| chrome/statusrow.module.css:111,115 | `.identity > span:first-child > :global(button) > :global(span:first-child)` | MISUSE | Reaches into Popover's trigger wrapper and the trigger's internals. Use PickerTrigger `compact` | |
| chrome/StatusRow.tsx:114 | raw `<select>` laid transparent over a readout + Chevron | EXTEND | **Select** `variant="quiet"` (borderless readout + chevron, native select overlaid) | The comment says it bypasses Select because Select has no overlay variant. |
| chrome/StatusRow.tsx:150 | sr-only raw `<meter>` + `aria-hidden` wrapper around `<Meter>` | MISUSE | Duplicate meter beside the real one. Let **Meter** `label=…` speak (drop the raw `<meter>` and the `aria-hidden`). Keep the 64px `.contextMeter` as layout only | |
| chrome/TopNotesPanel.tsx:132 | full-width `<button .summary/.expandedHeader>` + leading Chevron | SWAP | **Disclosure** `open={expanded} onOpenChange={toggle} summary={…}` | Testids `top-notes-summary` and `top-notes-collapse-trigger` swap with state. |
| chrome/TasksPanel.tsx:231 | `<details>/<summary .promptSummary>` with a text `▸` glyph chevron | SWAP | **Disclosure** `id={…\0prompt} summary={<>Prompt <preview/></>}` | Text glyph in place of the Chevron widget. The sibling task rows already use Disclosure. |
| chrome/TasksPanel.tsx:213,218 | `<span title={absoluteTime}>{relativeTime}</span>` | SWAP | **Timestamp** `value now` | Needs a `now` tick from the panel. |
| chrome/NotesPanel.tsx:235 | `<a>` link list | BESPOKE | n/a | External links that show the URL as an anti-phishing measure. |
| chrome/notespanel.module.css:40 | `.editor textarea { width:100% }` | MISUSE | Reaches into Textarea. Use **Textarea** filling its container (EXTEND) | |
| chrome/detailspanel.module.css:22 | `.session [data-testid="inspector-row"] > span:last-child` | MISUSE | Reaches into InspectorCard's rows via a test id. Use **InspectorCard** wrapping long values (EXTEND) | |
| chrome/sessionchrome.module.css:65 | `.right button:focus-visible { outline-offset:-2px }` | MISUSE | Overrides the focus ring of SessionMenu's trigger (a widget). Fix the clipping in the widget (inset-ring option) or in the container's overflow | |

## Counts

- SWAP: 9
- EXTEND: 22
- NEW: 8 sites (5 proposed widgets; jobWatch "Show full note" is counted as MISUSE but its fix is LinkButton)
- BESPOKE: 13
- MISUSE: 14
- Total: 66 rows

## Proposed widget API changes

### EXTEND

| Widget | Change | Sites in area |
|---|---|---|
| Disclosure | `variant="inline"` (flush, no padding/hover fill, chevron hugs the end of the label, label ellipsizes), `leading?: ReactNode` (rail icon), `readOptions?: {ignoreBaseline}` passed to `isDisclosureOpen`, `trailing?: ReactNode` rendered as a sibling of `<summary>` | 6 (TurnBlock, SteeringDivider, SystemNotice ×2, ThinkBlock, NotificationCard head) |
| Tree | `renderDetail(node, info)` slot, `ref` handle `{focusRow(id)}`, per-node `aria-label`/`aria-description` | 3 (ActivityTree root, fold row, RowShell) |
| Card | `tone="attention"` (bordered envelope) and `variant="outlined"` | 5 (AskDock batch, sandboxEscalation; jobWatch ×3) |
| RadioGroup | `option.detail`, `option.recommended`, `labelledBy`, `multiple`, `otherOption` | 2 (AskQuestionCard) |
| Chip | `truncate`, `pending`, `mono` | 2 (PendingChips, delegateStatus toolChip) |
| Banner | `link?: {label, href}`, and **add Banner to the barrel** | 2 (Session alert + its `<a>`) |
| Input | forward `ref`, `aria-labelledby`, `data-*` rest-spread | 1 |
| Select | `variant="quiet"` | 1 (StatusRow effort) |
| StatusDot | `decorative` (aria-hidden, no name) | 1 |
| Dropzone | `accept`, `multiple`, `openPicker()` handle | 1 |
| Button / IconButton | `collapseLabel` (container-query label drop + tap-width floor); `disabledReason` (aria-disabled + Tooltip) | fixes for 2 MISUSE sites (composer Send, QueueStrip ActionButton) |
| OpenButton | `fill="row"` (stretch to a dense row's height) | fix for 1 MISUSE site |
| Textarea | fill container width by default | fix for 1 MISUSE site |
| InspectorCard | top-align rows, wrap long values (`overflow-wrap:anywhere`, `min-inline-size:0`) | fix for 1 MISUSE site |

### NEW

| Proposed widget | API sketch | Sites in area |
|---|---|---|
| LinkButton | inline, wrapping, underlined text action; `{children, onClick, tone?: "accent"\|"inherit", aria-expanded?}` | 4 (CurrentWork ×2, LoadOlderRow retry, jobWatch "Show full note") |
| ImageThumbnail | `{src, alt, caption?, size, onOpen}`, optionally owning the Dialog lightbox | 2 (AttachmentTile, ImageGallery) |
| PickerTrigger | quiet value readout + down chevron for Popover/Sheet pickers; `{value, actionLabel, onClick, disabled, compact?}` | 1 here (ModelSwitchTrigger). Same pattern in widgets/pathfield and widgets/modelCatalog, and it is the fix for the statusrow `:global` reach and the triggerWrapper |
| Listbox | activedescendant-driven option popup | 1 here (SlashCompletionMenu). Also CommandPalette, modelCatalog, pathfield |
| Tabs | tablist + tabpanel + roving focus | 1 (AskDock) |

## Risks

- **Disclosure swaps in the transcript.** Disclosure mounts its body only when open. ThinkBlock and SystemNotice scaffold render the body inside `<details>` today. Expand-all/collapse-all baselines (`disclosureDefault`, `expandDetailsByDefault`) must flow through `defaultOpen`, and `ignoreBaseline` has no API yet. Virtual-list row heights change when chevrons and padding are added, so `useTranscriptScroll` anchoring and the scroll tests may shift. Asserted testids (`steering-chevron`, `think-block-chevron`, `system-notice-scaffold`, `notification-card` on the `<summary>`, `task-prompt-summary`) live on elements whose shape changes.
- **NotificationCard head.** The click-guard span exists because EntityRef and OpenButton sit inside `<summary>`. Moving them to a `trailing` sibling changes the wrapping and the "chevron hugs the last word" geometry.
- **stopPropagation.** OpenButton owns it. ActivityTree `rowToggle`, subagentModule's IconButton and the NotificationCard guard all stop clicks reaching an outer clickable row. A Tree migration must keep the toggle from also firing `onActivate`.
- **Keyboard and roving focus.** ActivityTree has its own Arrow/Home/End handling plus the Alt-chord release, a fold-row model and an imperative focus handle. AskDock tabs have roving tabindex, and their keyed-sibling structure keeps an in-progress answer's focus alive when a batch grows. SlashCompletionMenu relies on the `onMouseDown preventDefault` blur guard and the `optionId` scheme shared with Composer's activedescendant wiring.
- **AskDock Enter routing.** It finds the free-text field by `data-ask-free-input`, so Input must pass `data-*` through before that swap.
- **Disabled-button tooltips.** Moving to `aria-disabled` keeps focus on the button. The click handler then has to refuse the press itself.
- **Guard scripts.** The layoutguard case `hk8v-remove-button-focus-visible` measures AttachmentTile's remove button. spawnguard selects `data-testid="attachment-tile"`. StatusRow's phone compression (`min-width:1rem`, `padding-inline:0`) protects the model label from being squeezed to zero by the fixed facts, so any PickerTrigger must keep that floor or the overflow and layout guards regress. The ActivityPanel dense-row Open target sizing was a roborev fix about overlapping hit targets, so OpenButton `fill="row"` must keep the row-height cap.
- **Composer Send.** The label collapse is container-query based on pane width, not viewport width. A Button `collapseLabel` has to use a container query too.
- **Card tone change.** The AskDock envelope and the sandbox card use different attention mixes today. Unifying them changes contrast in dark mode (the comment cites a /dev/surfaces gallery check).
- **Barrel bypass.** Several files import from deep widget paths (`widgets/disclosure`, `widgets/openbutton`, `widgets/hovercard`, `widgets/timestamp`, `widgets/markdown`) and use `disclosureStore` directly. Barrel-based lint or tests won't see them.
- **Local sr-only classes.** composer, currentwork, askquestioncard, statusrow and modelswitch each define their own visually-hidden class, while `widgets/internal/VisuallyHidden` exists. This isn't a widget-concept swap, but it is the same kind of drift.

---

## Area: settings

### Design-system drift catalog: settings, spawn, sessionPanels, transcript, welcome, src/*.tsx, notifications

Scope: `cmd/evener-hub/frontend/src/` (paths below are relative to it). Test files and `src/dev/` skipped.
`App.tsx` and `main.tsx` render no UI of their own (route switch + `createRoot`). `notifications/` is `.ts` only (title, favicon, channels); no DOM. `panes/sessionPanels/` has one finding.

Widget APIs were checked against the shipped sources, which are wider than §3's table. Button has `secondary`, `dangerQuiet`, and `xs`. Sheet has `size` and `left`. Switch has `pending`. Textarea has `minLines`, `seamless`, and `aria-label`. Menu has `variant`. Popover has `stretchTrigger`. Where §3 is stale, the code was treated as the API.

| file:line | element / class | classification | target widget + props / proposed change | notes |
|---|---|---|---|---|
| panes/settings/SettingsNav.tsx:38 (+ settings.module.css:30-68, 102-137) | raw `<button>` `.link` / `.linkActive` / `.linkChevron` nav row | NEW | `ListRow` with `current` (aria-current) and `chevron="mobile"` | Same full-width-row-with-chevron recipe as the collection rows below; uses an accent `border-inline-start` for current. |
| panes/settings/SettingsNav.tsx:69 (+ settings.module.css:87) | `<label class=visuallyHidden>` for a placeholder-labelled `Input` | EXTEND | `Input` gets `"aria-label"?` | Hand-copied visually-hidden recipe, 1 of 3 copies in this area. |
| panes/settings/settings.module.css:20 `.clusterHeader`; launchShared/LaunchConfigForm.tsx:408 `.groupHeader`; credentials/ProviderInstanceGroups.tsx:68 `.groupHeader` | uppercase/semibold caption group label | NEW | `GroupLabel {children; rule?: boolean}` | 3 copies, each with slightly different rule/padding. |
| panes/spawn/Spawn.tsx:2422 (+ Spawn.tsx:215 `hostSelect: selectStyles.select`, import at :70) | raw `<select>` borrowing `widgets/select/select.module.css` | EXTEND | `Select` with `SelectOption.disabled?: boolean` | Also a MISUSE: it imports a widget's private stylesheet. The only reason given is the missing per-option `disabled`. |
| panes/spawn/Spawn.tsx:2475 (+ spawn.module.css:256-298) | raw `<button>` `.directoryButton` opening `DirectoryPicker` directly | EXTEND | `PathField kind="dir"` with `variant="prominent"` (basename headline, full path subline, trailing "Change…") | "Directory selection" says every directory field must go through PathField. `.directoryButton > svg`, `> span:last-child`, and `.directoryText strong` are element selectors. |
| panes/spawn/Spawn.tsx:2650-2680 (+ spawn.module.css:114-212) | quiet effort trigger: readout + chevron + zero-opacity native `<select>` + `.srOnly` label | EXTEND | `Select` with `variant="quiet"` (value-as-control readout, chevron) and `"aria-label"?` | The comment says it mirrors statusrow.module.css (outside this area), so this is a 2+ site pattern. It sets `font-size: var(--font-size-control)` itself. |
| panes/spawn/spawn.module.css:60, :66 | `.modelTrigger > :global(span)`, `.modelTrigger :global(button)` | MISUSE | Pierces ModelSwitchTrigger's Popover wrapper and button. Give ModelSwitchTrigger a `fill`/`shrink` prop that maps to Popover's existing `stretchTrigger` and the trigger's own ellipsis. | statusrow.module.css does the same piercing (outside this area). |
| panes/spawn/spawn.module.css:364 | `.form textarea { min-height: 96px }` | MISUSE | Reaches into SkillEditor/Textarea DOM. Use `minLines` (already passed as 6) or a PromptCard/Textarea `minHeight` prop. | Breaks if the field stops being a `<textarea>`. |
| panes/spawn/Spawn.tsx:2757 | hidden `<input type="file">` clicked via ref from the Attach IconButton | EXTEND | `Dropzone` with `accept?` and an imperative `open()` handle (or `pickerTrigger`) | The composer does the same (outside this area). |
| panes/spawn/Spawn.tsx:2392, :2709, :2729, :2785 (+ spawn.module.css:320 `.notice`) | bordered status strip with message + Retry/Dismiss | NEW | `InlineNotice {tone?; children; action?; onDismiss?; live?: "status"\|"alert"}` | `Banner` exists but is absolutely positioned overlay chrome and is not in the barrel. |
| panes/spawn/Spawn.tsx:2777 `.modelNote` role=alert | caption note | NEW | `InlineNotice tone="neutral" size="caption"` | |
| panes/spawn/Spawn.tsx:2795, :2809 (+ pluginSelection.module.css:49) | `.summary button { align-self }` | MISUSE | Descendant selector into Button. Wrap the Retry in a layout box, or use the `InlineNotice` action slot. | |
| panes/spawn/MobileSettingRows.tsx:90-108 (+ MobileSettingRows.module.css:5-58, 117-131) | label/value row button + read-only twin | NEW | `ListRow` with `label` + `value` (truncating), `onSelect`, `aria-haspopup`; read-only when no `onSelect` | This is §11's settings-style row. The `[data-label="Working directory"]` CSS keys styling off a test/data attribute. |
| panes/spawn/MobileSettingRows.tsx:81 | text caret `›` | SWAP | `Chevron direction="right"` | Every other row in the app uses Chevron. |
| panes/spawn/MobileSettingRows.tsx:134 (+ MobileSettingRows.module.css:65-106) | OptionSheet `.option` buttons with `aria-pressed` | EXTEND | `RadioGroup variant="list"` (48px full-width rows, trailing selected mark) inside `Sheet side="bottom"` | `aria-pressed` is toggle semantics, not single choice. `.selection` paints "Selected" in `--accent` text, which drifts from color-is-attention. |
| panes/spawn/MobileSettingRows.tsx:252 `.sheetBody` role=status | inline error + Retry | NEW | `InlineNotice` | `.sheetBody` is also reused at :300 as the Sheet footer's button row. |
| panes/spawn/PluginSelectionPanel.tsx:102 (+ pluginSelection.module.css:112-129) | raw `<details>/<summary>` diagnostics | SWAP | `Disclosure id="spawn-plugin-diagnostics" summary=…` | |
| panes/spawn/PluginSelectionPanel.tsx:122-133 (+ pluginSelection.module.css:83-89, 159-161) | Switch row + `.metadata` indented `calc(32px + space-2)` | EXTEND | `Switch description?: ReactNode` (secondary lines aligned under the label) | The indent hard-codes Switch's internal track width. |
| panes/spawn/pluginSelection.module.css:163-193 | `.panel .row [role="switch"]` rebuilds the track via `::before` at 44px | MISUSE | Move the 44px phone hit target into `Switch` itself (`switch.module.css` below 899px) | Restyles a widget's internals from outside and redraws its checked color. |
| panes/spawn/PluginSelectionPanel.tsx:143, :153 `.errors` | bordered error callouts with list + Retry | NEW | `InlineNotice` with `title` + list children + action | |
| panes/spawn/PluginSelectionPanel.tsx:140 `.empty` | empty-list paragraph | EXTEND | `EmptyState size="inline"` | EmptyState's pane-title size is too big for in-list use, which is why every list here rolls its own. |
| panes/spawn/pluginSelection.module.css:1 `.panel`; spawn/advancedOptions.module.css:42 `.panel`; settings/sections/credentials/ProviderConnection.module.css:7 `.review` | bordered surface box | EXTEND | `Card variant="bordered"` | `Card` is flat, with no outside border by contract. Three different radius/background combos. |
| panes/spawn/AdvancedOptions.tsx:216 (+ advancedOptions.module.css:9-40) | raw `<button aria-expanded>` toggle + conditional panel | SWAP | `Disclosure open={open} onOpenChange={setOpen} summary="Advanced options"` | Disclosure mounts the body only while open, same as today. |
| panes/spawn/AdvancedOptions.tsx:464, :531 (+ advancedOptions.module.css:70-91) | `.fieldBlock/.fieldLabel/.fieldHelp` | EXTEND | `FormRow` group mode: `htmlFor` optional, `labelId`, renders `role="group" aria-labelledby` | The CSS comment says it copies FormRow's inset by hand. |
| panes/settings/sections/launchShared/fields.tsx:250 (+ fields.module.css:30-51) | `.modelBlock/.modelLabel/.modelHelp` | EXTEND | `FormRow` group mode (as above) | Same hand-copied inset comment. |
| panes/settings/sections/launchShared/collectionFields.tsx:59 (used :104, :430, :497) | `SectionHeader` `.label/.help` | EXTEND | `FormRow` group mode | |
| panes/settings/sections/launchShared/fields.tsx:207, :352 (+ fields.module.css:7-17) | `.radioBlock` + `.radioHelp` after RadioGroup | EXTEND | `RadioGroup help?` (aria-describedby) | |
| panes/spawn/AdvancedOptions.tsx:598, :663 (+ advancedOptions.module.css:97-112) | `pickerAddRow` + own `Button type=submit` "Add" (quiet) | EXTEND | `CollectionEditor`: `renderAddField` replaces only the field; the widget keeps owning the Add submit and row layout | Add button variant drifts across sites: quiet here, primary in dirListSetting. |
| panes/settings/sections/launchShared/collectionFields.tsx:175/228, :291/323, :446 (+ collectionFields.module.css:25-88) | `pathAddRow`, `modelAddRow`, `envRow` + own Add Button | EXTEND | `CollectionEditor` (as above) | Triplicated grow recipe with near-identical comments. |
| panes/settings/sections/dirListSetting.tsx:131-150 (+ dirListSetting.module.css:39-54) | FormRow + addRow + primary "Add" inside `renderAddField` | EXTEND | `CollectionEditor` (as above) + `addLabel` | |
| panes/spawn/AdvancedOptions.tsx:250 `.resolved` | raw `<pre>` JSON dump | SWAP | `CodeBlock text={resolved} language="json"` | Gains copy. |
| panes/spawn/AdvancedOptions.tsx:243 `.error` | inline error paragraph | NEW | `InlineNotice tone="danger"` | |
| panes/settings/sections/launchShared/collectionFields.tsx:379, :391 (+ collectionFields.module.css:42) | visually-hidden `<label>` for env Inputs | EXTEND | `Input "aria-label"?` | 2nd copy of the visually-hidden recipe. |
| panes/settings/sections/keybindings.tsx:408 (+ keybindings.module.css:82-89) | `.marker` "Customized" pill | SWAP | `Chip tone="neutral"` | |
| panes/settings/sections/keybindings.tsx:428, :437 (+ keybindings.module.css:136-156) | raw `<button>` `.actionButton` Unbind / Reset | SWAP | `Button variant="quiet" size="xs"` | |
| panes/settings/sections/keybindings.tsx:544 | raw `<button>` `.actionButton` Retry | SWAP | `Button variant="quiet" size="xs"` | |
| panes/settings/sections/keybindings.tsx:414 (+ keybindings.module.css:111-134) | raw `<button>` `.chordButton` wrapping KeyHints | SWAP | `Button variant="quiet" size="sm" aria-label=…` with KeyHint children | Hover changes from a border to a wash. `chordButtonRef` focus-return must survive (Button forwards ref). |
| panes/settings/sections/keybindings.tsx:243 (+ keybindings.module.css:189-216) | `div role="textbox"` chord capture box | BESPOKE | n/a | A key recorder must swallow every key and render KeyHints. No widget covers it and it is the only site. |
| panes/settings/sections/keybindings.tsx:264, :460, :534 | `.captureError/.rowError/.error` paragraphs | NEW | `InlineNotice tone="danger"` | |
| panes/settings/sections/keybindings.tsx:405 + panes/welcome/WelcomeContent.tsx:89-97 (+ welcome.module.css:72-86) | action label + KeyHint run rows / `dl` hint grid | NEW | `ShortcutList {items: {keys; label}[]}` | The shortcuts overlay (outside this area) probably shares it. |
| panes/settings/sections/marketplacesPlugins/BrowseSection.tsx:270 (+ marketplacesPlugins.module.css:114-160) | raw `<button aria-expanded>` tree toggle + rotating chevron | SWAP | `Disclosure open={expanded} onOpenChange={onToggle} summary={name + count}` | The `ul` is labelled "browse tree" but has no tree semantics. `Tree` is the alternative, but rows hold Install buttons, which is awkward inside `treeitem`. |
| panes/settings/sections/marketplacesPlugins/BrowseSection.tsx:299 (+ css:173) | `.installedBadge` "Installed" | SWAP | `Chip tone="neutral"` | |
| panes/settings/sections/marketplacesPlugins/MarketplacesSection.tsx:99 (+ css:54) | `.rowKind` source-kind tag | SWAP | `Chip tone="neutral"`, or move into the meta line | §10: state chips + one meta line. |
| panes/settings/sections/marketplacesPlugins/MarketplacesSection.tsx:96 (+ css:182-221) | raw `<button>` `.rowButton` + `.rowChevron` | NEW | `ListRow {title; meta; chips?; status?; onSelect}` | §10's canonical collection row, re-implemented in 4 stylesheets. |
| panes/settings/sections/marketplacesPlugins/InstalledSection.tsx:76 | raw `<button>` `.rowButton` with StatusDot + chips + meta | NEW | `ListRow` | |
| panes/settings/sections/credentials/InstanceRow.tsx:122 (+ :91 read-only; InstanceRow.module.css:16-97) | raw `<button>` `.row.rowButton` + read-only `<li>` twin | NEW | `ListRow` (read-only when no `onSelect`, optional trailing `action`) | |
| panes/settings/sections/credentials/ProviderConnection.tsx:134 (+ ProviderConnection.module.css:14-36) | raw `<button>` `.provider` + chevron | NEW | `ListRow` | Its comment says it copies marketplacesPlugins' `.rowButton`. |
| panes/settings/sections/hosts.tsx:315-397 (+ hosts.module.css:28-80) | host row with chip + up to 7 trailing quiet Buttons (Connect/Deploy/Restart/Edit/Remove/Teardown/Recover) | NEW | `ListRow` + a host detail `Sheet` carrying the actions | Direct violation of §10: "A row NEVER grows a trailing cluster of small action buttons." |
| panes/settings/sections/mcp.tsx:220; panes/settings/sections/agents.tsx:60 (+ mcp.module.css:48-75, agents.module.css:29-48) | read-only bordered list rows | NEW | `ListRow` (read-only) | |
| panes/settings/sections/agents.tsx:65 (+ agents.module.css:44) | `.builtin` "built-in" text tag | SWAP | `Chip tone="neutral"` | |
| panes/settings/sections/marketplacesPlugins/BrowseSection.tsx:186, :190, :286; InstalledSection.tsx:70, :72; MarketplacesSection.tsx:92; mcp.tsx:217, :267 | `li.empty` messages | EXTEND | `EmptyState size="inline"` | Each stylesheet has its own `.empty`. |
| panes/settings/sections/marketplacesPlugins/BrowseSection.tsx:188, :280-282; project.tsx:194; launchServer.tsx:169 | loading as text / `Loader` inside an `li.empty` | SWAP | `Skeleton` (or `Loader` outside the empty slot) | Plain-text "Loading…" does not use the honest-liveness widgets. |
| panes/settings/sections/mcp.tsx:265-321 (+ mcp.module.css:82-92) | hand-built inline-server list + IconButton remove + local `RemoveIcon` svg + FormRow-wrapped 3-Input add form | SWAP | `CollectionEditor<MCPServerSpec>` | `launchShared/collectionFields.tsx:461-512 McpServerListField` already does this through CollectionEditor, so this is a widget reimplemented beside the real one. |
| panes/settings/sections/marketplacesPlugins/MarketplaceSheet.tsx:409-419; PluginDetailSheet.tsx:158-174; credentials/InstanceSheet.tsx:768 (+ marketplacesPlugins.module.css:237-261, InstanceSheet.module.css:53-70) | `.metaRow/.metaLabel/.metaValue` 96px label column | NEW | `MetaTable {rows: {label; value; mono?}[]}` | §10's "meta table idiom" exists only as copied CSS. |
| panes/settings/sections/hosts.tsx:673 `PlanField` (used :767, :797, :829, :1090) (+ hosts.module.css:90-115) | label/value plan rows, 9rem label | NEW | `MetaTable` | Different label width (9rem vs 96px). |
| panes/settings/sections/settingsField.tsx:35 (used by general.tsx:70, storage.tsx:68, hub.tsx:58, sessionCacheRow.tsx:128) | `dl` label/value/help stacked rows | NEW | `MetaTable layout="stacked"` with `help` + trailing actions | De facto a widget already. Promote it rather than duplicate. |
| panes/settings/sections/marketplacesPlugins/MarketplaceSheet.tsx:421, :427 (+ marketplacesPlugins.module.css:297) | `.sheetActions > button { width:100%; justify-content:flex-start }` | MISUSE | `Button block` (full-width, start-aligned) | Wrapper exists only to restyle Button. |
| panes/settings/sections/credentials/InstanceSheet.tsx:871, 899, 921, 937, 944, 951, 958, 974, 985, 992 (+ InstanceSheet.module.css:81) | `.fullRow > button` wrappers | MISUSE | `Button block` | The CSS comment claims "not a widget reskin", but it changes Button's alignment and width. |
| panes/settings/sections/credentials/InstanceSheet.module.css:29 | `.formError { composes: errorText from widgets/formrow/formrow.module.css }` | MISUSE | `InlineNotice tone="danger"`, or `FormRow error=` on the field | Imports a widget's private class. Used at InstanceSheet.tsx:836, :846. |
| panes/settings/sections/credentials/InstanceSheet.tsx:853 `.layers` | credential layer list with effective/shadowed Chip | BESPOKE | n/a | One domain-specific readout. It already composes Chip. |
| panes/settings/sections/hubResidents.tsx:324 (+ hubResidents.module.css:28-47) | raw `<table>` with th/td styling | SWAP | `Table columns=[…render] rows={daemons} rowKey={residentRowKey}` | Row `aria-label` (:362) has no Table equivalent, so check tests that query by it. |
| panes/settings/sections/display.tsx:26; theme.tsx:67, :80, :90, :100; notifications.tsx:89, :98, :107, :112, :117 | `.row` wrapper + `.help` paragraph under Switch/RadioGroup | EXTEND | `Switch help?` / `RadioGroup help?` wired via aria-describedby | Help text is not associated with its control today. |
| panes/settings/sections/agentsDoc.module.css:24 | `.editor textarea { width; font-family: mono }` (wrapper at agentsDoc.tsx:177) | MISUSE | `Textarea monospace` | Wrapper exists only to restyle Textarea. |
| panes/settings/sections/credentials/instanceDialogs.tsx:568 (+ instanceDialogs.module.css:18) | raw `<textarea>` mono, `spellCheck={false}`, rows=8 | EXTEND | `Textarea monospace spellCheck={false} rows={8}` | It sets `--font-size-control` by hand today. |
| panes/settings/sections/credentials/ProviderConnection.tsx:205, :846 (+ ProviderConnection.module.css:4-5) | raw `<details>/<summary>` + `.body summary/details` | SWAP | `Disclosure id=… summary=…` | The same dialog already uses `Disclosure` at :190. |
| panes/settings/sections/credentials/ProviderConnection.tsx:719; credentials/oauthDialogs.tsx:98 (+ ProviderConnection.module.css:3 `.body a`) | raw external `<a target=_blank>` | SWAP | `OpenButton href=… word="get a key"` / `word="re-open authorize URL"` | `rel` drifts: `noreferrer` vs `noopener`. OpenButton standardizes it. |
| panes/settings/sections/credentials/ProviderConnection.tsx:179, :841 | `div role=alert tabIndex=-1` error (`.review`) | NEW | `InlineNotice tone="danger"` with a focus ref | Focus-on-error must be kept. |
| panes/settings/sections/credentials/oauthDialogs.tsx:299 (+ oauthDialogs.module.css:28) | `.code` large device-code display | BESPOKE | n/a | Single big-type code to be read across devices. |
| panes/settings/sections/credentials/CredentialsSection.tsx:109; launchServer.tsx:37 (+ CredentialsSection.module.css, launchServer.module.css) | "Warnings" box: heading + ul | NEW | `InlineNotice title="Warnings"` with list children | Two copies with different border/background. |
| panes/settings/sections/credentials/CredentialsSection.tsx:543 | `"+ Add provider instance"` literal glyph | MISUSE | `Button icon={<PlusIcon/>}` | |
| panes/settings/sections/hosts.tsx:334, :649, :768, :809, :830, :945, :1094 (+ hosts.module.css `.error/.formError/.rowOperationError`) | danger-colored error text | NEW | `InlineNotice tone="danger"` | Uses raw `var(--danger)` for text; every other site uses `--danger-ink` or ink. |
| panes/settings/sections/hubResidents.tsx:305, :431 `.errorBanner` | status / alert paragraphs | NEW | `InlineNotice` | |
| panes/settings/sections/transcript.tsx:128, :138; TranscriptDisplayCard.tsx:123 | `.error` inset strip + Retry | NEW | `InlineNotice` with action | |
| panes/settings/sections/agents.tsx:52; agentsDoc.tsx:164, :189, :203, :223; project.tsx:196, :206; launchServer.tsx:171, :181; inrepo.tsx:139, :143; CredentialsHostScope.tsx:260, :292, :520, :525, :532, :542; hubUpdates.tsx:99, :117; LaunchConfigForm.tsx:417; MarketplaceSheet.tsx:399, :404; InstanceSheet.tsx:932; CredentialsSection.tsx:583 | per-file `.error/.note/.status` paragraphs, some with an inline Retry | NEW | `InlineNotice` | About 25 sites. Error ink is inconsistent: `--ink-hi`, `--ink-mid`, `--danger-ink`, `--danger`. |
| panes/settings/sections/transcript.tsx:13-15; about.tsx:4 | imports from `widgets/button`, `widgets/toast`, `widgets/disclosure` | MISUSE | Import from the `widgets` barrel | Bypasses the controller-owned barrel. |
| panes/settings/sections/marketplacesPlugins/InstalledSection.tsx:12; credentials/InstanceRow.tsx:24; panes/transcript/ReadOnlyThreadContent.tsx:15 | `import { VisuallyHidden } from widgets/internal/VisuallyHidden` | MISUSE | Promote `VisuallyHidden` to the barrel | §3: `internal/` is "implementation machinery, not a widget". Hand-copied recipes are in settings.module.css:87, spawn.module.css:202, collectionFields.module.css:42. |
| panes/settings/sections/about.tsx:85; inrepo.tsx:177 | raw `<pre>` license / config preview | SWAP | `CodeBlock text=…` | Check CodeBlock wraps (`pre-wrap`) for the license text. |
| panes/welcome/welcome.module.css:36-46 | `.actions button { white-space: normal; height: auto; min-height }` | MISUSE | `Button wrap` (multi-line label, height auto, keeps tap floor) | Re-declares the phone tap floor because it out-specifies button.module.css. |
| panes/transcript/JobLog.tsx:355; panes/transcript/Transcript.tsx:90; panes/sessionPanels/SessionPanelPane.tsx:35 | `EmptyState title="Loading …"` | MISUSE | `Skeleton` (or `Loader label=…`) | EmptyState means "nothing here", not "still fetching" (honest-liveness, §11). |
| panes/transcript/JobLog.tsx:337 (+ transcript.module.css `.joblogCommand`) | raw `<code>` pinned command | SWAP | `ShellCommandBlock command={command}` | Gains copy and shell tokenizing. Keep `data-testid="joblog-command"`. |
| panes/transcript/JobLog.tsx:350 `.joblogNote role=status` | output error note | NEW | `InlineNotice` | |
| panes/transcript/JobLog.tsx:332, :343, :383 `.joblogNote` | job status / byte counts / unloaded-row markers | BESPOKE | n/a | Log-viewer grammar inside a VirtualList row. |
| panes/settings/sections/mobile.tsx:87 | `div role=img` QR | BESPOKE | n/a | Single QR render. |

## Proposed widget API changes

### EXTEND (existing widgets)
- **Select** (2 sites): `SelectOption.disabled?`; `"aria-label"?`; `variant="quiet"` for the value-as-control readout (Spawn host picker, Spawn effort; statusrow outside this area).
- **Input** (3 sites): `"aria-label"?` (SettingsNav filter, two env-add fields).
- **Textarea** (2 sites): `monospace?: boolean`, `spellCheck?: boolean` (AGENTS.md editor, credential JSON dialog).
- **Switch** (11 sites): `help?` (described-by) for display/notifications rows; `description?: ReactNode` for plugin rows; built-in 44px phone hit target, replacing pluginSelection's `[role=switch]` rebuild.
- **RadioGroup** (8 sites): `help?` (theme ×4, notifications, fields.tsx ×2); `variant="list"` (48px full-width rows) for the mobile OptionSheet.
- **FormRow** (5 sites): group mode with no `htmlFor`, a `labelId`, and `role="group"` for composite controls (AdvancedOptions ×2, fields.tsx modelBlock, collectionFields SectionHeader ×3 uses).
- **CollectionEditor** (6 sites): with `renderAddField`, the widget still renders the Add submit and row layout. Callers stop hand-rolling the Add button and grow wrappers (AdvancedOptions ×2, collectionFields ×3, dirListSetting).
- **EmptyState** (8 sites): `size="inline"` for in-list empty messages.
- **Button** (14 sites): `block` (full-width, start-aligned) for sheet action lists (InstanceSheet ×10, MarketplaceSheet ×2); `wrap` for long labels (Welcome).
- **Card** (3 sites): `variant="bordered"`.
- **PathField** (1 site): `kind="dir" variant="prominent"` (Spawn working directory).
- **Dropzone** (1 site; composer outside this area): `accept?` + an imperative `open()`.

### NEW widgets
- **InlineNotice** `{tone?: "neutral"|"danger"; title?; children; action?; onDismiss?; live?}`: about 45 sites (Spawn ×6, MobileSettingRows, PluginSelectionPanel ×2, AdvancedOptions, keybindings ×3, hosts ×7, hubResidents ×2, transcript ×2, TranscriptDisplayCard, CredentialsSection/launchServer warnings ×2, ProviderConnection ×2, InstanceSheet ×3, JobLog, plus the ~25-site paragraph group).
- **ListRow** `{title; meta?; status?; chips?; label/value?; onSelect?; current?; action?}`: 10 sites (SettingsNav, MobileSettingRow, MarketplacesSection, InstalledSection, InstanceRow ×2 variants, ProviderConnection, hosts, mcp discovered, agents).
- **MetaTable** `{rows; layout?: "inline"|"stacked"}`: 6 sites (MarketplaceSheet, PluginDetailSheet, InstanceSheet, hosts PlanField, SettingsField and its 4 consumers).
- **VisuallyHidden** (promote to barrel): 3 imports + 3 hand-copied CSS recipes.
- **GroupLabel**: 3 sites.
- **ShortcutList**: 2 sites (Welcome hints, keybindings rows).

## Risks
- **Focus handling.** MobileSettingRows restores focus to `lastTriggerRef` (`HTMLButtonElement` check), so ListRow must render a real `<button>` and forward refs. keybindings `chordButtonRef` refocus after capture needs a forwarded ref on Button, which it has. ProviderConnection's `errorRef.focus()` on `tabIndex=-1` alerts needs InlineNotice to forward a ref. Disclosure swaps (AdvancedOptions, BrowseSection) change the trigger from `<button aria-expanded>` to `<summary>`, so tests or scripts that query `button[aria-expanded]` break. Switch's `pending` focus-retention contract must survive a hit-target change.
- **Keyboard nav.** RadioGroup `variant="list"` gives roving arrow keys where the OptionSheet had Tab-through buttons. That is better semantics, but it is different, and "select then close sheet" has to fire on Enter/Space without arrow keys auto-closing. Converting BrowseSection to `Tree` (not recommended) would put Install buttons inside treeitems.
- **iOS 16px rule.** The Spawn effort overlay and the credential-JSON textarea set `font-size: var(--font-size-control)` by hand. Select `variant="quiet"` and Textarea `monospace` must keep the control size on the actual form element. A mono override must not shrink it to caption.
- **Test selectors.** These must be preserved: `data-testid` values `settings-nav-chevron`, `mobile-spawn-row` plus `data-label` (also a CSS hook), `spawn-effort`/`spawn-effort-value`, `spawn-model-slot`, `spawn-plugin-summary`, `plugin-row-metadata`, `joblog-command`, `transcript-display-*`, `show-all-providers`. Also ids `spawn-cwd`, `spawn-host`, `spawn-reasoning-effort`, `settings-host`, `provider-search`. hubResidents rows are reached by `tr[aria-label]`, which Table does not emit. Many tests probably query by role+name, e.g. "Remove x", "Working directory: …", "Start".
- **spawnguard** (`scripts/spawnguard`, `npm run spawnguard`). It drives the real spawn form, so any change to `#spawn-cwd` (directoryButton to PathField), `#spawn-host` (raw select to Select), the effort select overlay, the 44px tap floors, or the `.form textarea` min-height can trip it. Re-run spawnguard (and layoutguard/overflowguard for the Welcome button-wrap and narrow-pane effort ellipsis) after each spawn-area swap.
- **Visual drift.** keybindings chord button (border-on-hover becomes a wash), OpenButton adding a glyph to inline prose links, CodeBlock adding a copy header to the license/preview, and Chip replacing plain "Installed"/"built-in" text. Each is an intended visual change, so the snapshots and gallery need updating.
- **Barrel.** New exports (InlineNotice, ListRow, MetaTable, VisuallyHidden, GroupLabel, ShortcutList) each need a gallery section to satisfy `WidgetGallery.test.tsx`'s completeness test.
