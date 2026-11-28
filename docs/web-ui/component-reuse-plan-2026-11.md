# Hub web UI component reuse plan

Status: Jesse answered the nine decision cards and the fourteen-item override on 2026-11-25; every decision and override is built except `W1-F18` (kept as is). Two hundred twenty-three findings have shipped 2026-11-22 through 2026-11-25 across 137 reuse PRs; two were disproved, thirty-nine are settled outside the tables, and 36 remain (no High, `P5-F06` and the `Y1-F01` remainder at Medium, 34 Low). The shipped ledger, the settled list, the decisions record, and those 36 rows are tracked in `docs/web-ui/component-reuse-unfixed-findings-2026-11.md`.
Base: `347036b94b`.
Scope: `cmd/evener-hub/frontend/src` (production and dev UI source), plus the shared widget and style layers it composes.
Out of scope: Go/appwire behavior, the native client, visual redesign, and feature changes not needed to remove duplication.

## 1. Bottom line

Evener's web UI is not missing a component library. It has one, and most surfaces use it. The duplication that remains is concentrated in four places: shared CSS recipes restated across widget stylesheets, widget APIs that stop one step short of the shape callers need, local surface grammars the widget layer cannot own, and raw controls or module classes that bypass existing widgets.

The highest-value first moves are mechanical: share the field chrome, tap floor, focus ring, and disabled/hover recipes; adopt `InlineNotice` and one Retry convention; delete the dead CSS. The truncation quartet alone appears in 60 declarations, and the settings surface adds the same help/note caption in 22 CSS rules and a ten-section load/refresh gate. The next tier extends five existing widgets (quiet `Select`, prominent `PathField`, `ListRow` value, `RadioGroup` list, and `ModelSwitchTrigger` sizing) and adds one new `Listbox` where four hand-built ARIA listboxes repeat the same keyboard contract; both need your approval. Session chrome adds two Highs of its own: the reasoning-effort control is copied between StatusRow and Spawn, and ModelSwitchTrigger hand-builds a picker trigger that `fieldTrigger` already recipes. The transcript tool-card cluster, the three chunk-failure boundaries, the spawn mobile rows, and the session chrome are the largest local-component wins.

The plan orders these by risk and dependency. The API extensions are proposals, not defaults, and every change names its duplicated sites, migration steps, and verification.

The October 2026 widget-adoption catalog is largely landed. The remaining work is not a first widget library; it is five kinds of residue:

1. Existing widgets that were built but not adopted in whole surfaces (spawn, shell, zoom, session).
2. Widget APIs that stop one step short, so callers copy the last mile (quiet select, path tile, list-row value, list radio group, picker trigger).
3. Large components that repeat a local grammar the widget layer cannot own (transcript tool chrome, session chrome).
4. CSS recipes repeated instead of composed (field chrome, focus ring, tap floor, disabled/hover, card/row/meta chrome, safe-area padding).
5. Local helpers and dead code left behind by earlier migrations.

## 2. Scope and method

### 2.1 Coverage

| Unit | Count | Lines |
|---|---:|---:|
| Production TSX/CSS files | 468 | 83,628 |
| Dev-only gallery/harness TSX/CSS | 133 | 13,712 |
| Audit lanes | 22 | |
| Files assigned to exactly one lane | 601 (468 production + 133 dev) | 97,340 |
| Non-test test-utility TSX excluded from the lane count | 10 | 2,105 |

Every production and dev TSX/CSS file under `src` except ten non-test test utilities was assigned to exactly one file-bearing lane (P1–P8, S1–S3, G1–G3, W1–W3, M1, I1, Y1, D1). A 22nd lane, X1, ran a mechanical cross-cutting census and owned no files. The ten excluded files are shared test fixtures (`*TestUtils.tsx`, `testing/Committed*.tsx`), not product UI; lanes read them only where a proposed change touches their assertions. Adjacent `.ts` render-model helpers, widgets, styles, tests, and design docs were read where a lane needed them to prove a cross-boundary claim. Test files were read selectively for the assertions a proposed change would touch, not line by line.

**Coverage at handoff.** All 21 file-bearing lanes reported: D1, G1, G2, G3, I1, M1, P1, P2, P3, P4, P5, P6, P7, P8, S1, S2, S3, W1, W2, W3, and Y1. Together they were assigned all 601 files: 468 production and 133 dev. The change list has 300 change findings (37 High, 113 Medium, 150 Low). The lane ledger counts 303 recorded entries (37 High, 113 Medium, 152 Low, plus one unranked keep-bespoke entry), because P4, S1, and G2 each count one keep-bespoke entry; P1's four and P8's one keep-bespoke notes sit outside both counts. The X1 census is cross-cutting, owns no files, and is Appendix C, not a lane. P6's findings are parent-recovered from its surviving analysis (see Appendix A); five of its claimed 17 findings are unrecorded. G3's findings are also parent-recovered: 14 recorded (2 High, 7 Medium, 5 Low), with four Low findings counted but never drafted. The late lanes (G2, P1, P2, P4, P8, S1, W2) were recovered from their transcripts and every cited line was re-opened on base `347036b94b`; P2's original coverage record is relied on and its cited sites were re-opened. Claims a lane marked suspected remain suspected, not promoted.

The audit is reproducible from the lane manifests in the audit scratch directory and the base commit. It did not modify the frontend tree.

### 2.2 What counted as duplication

- The same DOM or CSS concept implemented twice, whether in two components or in a component and a stylesheet.
- A local implementation of something an existing widget already owns.
- Two public APIs expressing the same concept (`Badge`/`Chip`/`Meter`, `ListRow`/`Card`, `Tooltip`/`HoverCard`, `Disclosure`/`DisclosureHeading`).
- Repeated logic in helpers or render models (status/label/formatting, file picking, metadata rows).
- A widget API that forces callers to copy its internals to get a common shape.
- Dead classes, props, exports, and wrappers left behind by a migration.

Not counted: deliberate bespoke layout that carries a product ruling or a distinct grammar (transcript tool cards, activity spine), test fixtures, and demo-only repetition with no production pattern.

### 2.3 Evidence standard

Every finding in the lane ledger names exact files and lines, the existing shared thing, the proposed change, the approval class, the migration risk, and an estimated size. Counts in the summary are file/site counts, not invented savings. Where a lane could not prove a claim by grep, the finding says so or is marked suspected.

RoboRev findings are leads, not verdicts. RoboRev is prone to overzealous or speculative findings, so a finding that would change this plan or block a PR is re-verified against the base code or a failing test before it is acted on; a refuted finding is answered with the code's own construction, not with a weaker assertion. This plan does not cite RoboRev as evidence for any change.

### 2.4 Cross-cutting census

X1 ran a mechanical census over all of `src` and owned no files. Its numbers are cross-checks and sizing inputs, not additional findings: they corroborate the lane evidence and are not counted in the lane totals. The highest-signal results are 703 barrel imports across 238 files, with 153 imports reaching into `widgets/internal/*`; 30 raw controls in 18 files; 173 native `title=` attributes in 71 files against 6 files importing `Tooltip`; 27 hand-built `role="status"` and 6 `role="alert"` sites; 13 inline `<svg>` and 9 local icon components; and 86 token-literal bypasses with 12 declared-but-unused tokens. Appendix C holds the full table and the census's explicit gaps.

## 3. Constraints

### 3.1 Locked widget APIs

The design system treats the widget API as a contract. These rules shape every proposal:

- Compose from the `src/widgets` barrel; a widget composing a sibling imports by directory.
- No raw `<button>`, `<input>`, `<select>`, or `<textarea>` outside `src/widgets` and dev galleries.
- Do not style a widget's internal DOM from outside. Use a prop, or add one with approval.
- `Button` computes `className` only; callers cannot override it.
- `PaneScaffold` owns `bodyScroll` and the footer/edge-footer bottom-inset rules.
- `ModelCatalog`/`ModelCatalogPanel` take a qualified `"provider/model"` or `""`, an injected `loadCatalog`, and `variant="sheet"`.
- `DirectoryPicker` is draft browsing; only "Use this folder" commits once, after validation.
- `Textarea` already has a seamless variant.
- `widgets/internal/fieldTrigger.module.css` is the shared closed-state trigger recipe for `ModelCatalog` and `PathField`.
- `StatusDot` is the one status glyph; `Skeleton`/`Loader` are loading, not `EmptyState`.
- Mobile forms use 48px rows on a grouped surface, not floating labels.
- Collection pages use `SegmentedControl` + `ListRow` + a detail `Sheet`, never a trailing action cluster per row.
- Loading states are honest about cold starts; do not show an empty state before the first read resolves.

### 3.2 Product rulings that constrain component changes

- **The empty composer stays folded on focus** in every orientation, including Duo landscape (951×669 pt with the session card below 620 px), including session-opening autofocus. Typing or adding other sendable content expands it; Attach remains available in the collapsed row; empty inline Steer is hidden. Fine-pointer narrow desktop keeps the stacked layout while its field stays one line. This supersedes the 2026-11-20 rule that focus restored the full controls.
- **The edge footer alone owns the bottom safe-area band.** When a flush edge footer follows the padded composer footer, the composer footer keeps only the ordinary gap; `footerBeforeEdge` must not add safe-area padding.
- **The Agents tab is backed by the live `subtree` store.** Active descendants nest under their parent and collapse; settled subagents fold into one bottom `N inactive subagents` disclosure grouped by parent; active + inactive equals the tab/footer counts; an open run keeps the spinning green `working` dot.
- **The ActivityPanel sheet is retired.** Do not restore or rewire it; `ActivityTree`/activity surfaces are the live path.

### 3.3 Test and gate rules

- No test asserts phrasing, copy, or a hardcoded human-readable string. Assert behavior.
- Responsive behavior uses one consolidated case set, not a copy of the same focus, visibility, or layout assertion per viewport.
- A migrated component keeps its accessible role/name and any pinned test id unless the plan explicitly changes the contract.
- The widget gallery is the public API's visual index: a new widget or variant needs a gallery section, and `WidgetGallery.test.tsx` enforces completeness.
- Frontend gates: `make test-web` (Biome, typecheck, vitest), `make test-web-browser` on a Chrome-capable host, and targeted lane tests. Run Biome from `cmd/evener-hub/frontend`, never the repo root.
- No Windows support work. No new Windows-only checks.
- No PR absorbs an unrelated finding. File it, link it, keep the change in budget.

## 4. Approval boundary

| Class | Meaning | Examples | Approval |
|---|---|---|---|
| A. Adoption / deletion | Use an existing widget or recipe, delete local CSS/dead code, share a local helper inside one area | `InlineNotice` sweep, `ListRow` adoption, dead CSS, metadata-row helper | No API approval; normal review |
| B. Widget API extension | Add a prop or variant to an existing widget | `Select variant="quiet"`, `PathField variant="prominent"`, `ListRow value`, `RadioGroup variant="list"`, `ModelSwitchTrigger fill/shrink`, `ZoomableMedia radius`, `Button collapseLabel` (if chosen) | Jesse, per variant, before implementation |
| C. New widget | A new shared component in `src/widgets` | PickerTrigger/Listbox, `ImageThumbnail`, CountButton, InspectorCard (if confirmed) | Jesse, with gallery and tests |
| D. Product/behavior change | Changes a ruling, default, or user-visible contract | Composer layout, the composer storage-notice box if it changes the folded row, status semantics, navigation model, FormRow's error-color token swap, the chunk-boundary rethrow policy | Jesse; not part of a duplication PR |

The plan keeps A separate from B/C so the mechanical cleanup can land without reopening the widget API. B and C are grouped by the call sites that justify them; a variant with one caller stays a proposal, not a default.

## 5. Findings by workstream

## Workstream 1 — Field chrome and control geometry

**Current duplication.** `widgets/input/input.module.css`, `widgets/select/select.module.css`, `widgets/textarea/textarea.module.css`, and `widgets/internal/fieldTrigger.module.css` repeat the same recipe:

- height 32px (Input `:2`, Select `:8`, fieldTrigger `:19`)
- border-radius `--radius-control` (Input `:4`, Select `:10`, Textarea `:11`, fieldTrigger `:22`)
- border `1px solid var(--edge)` (Input `:5`, Select `:11`, Textarea `:12`, fieldTrigger `:21`)
- background `var(--field)` (Input `:6`, Select `:12`, Textarea `:13`, fieldTrigger `:23`)
- box-shadow `var(--shadow-inset-field)` (Input `:7`, Select `:13`, Textarea `:14`, fieldTrigger `:24`)
- disabled opacity 0.5 (Input `:28`, Select `:30`, Textarea `:44`)
- focus-visible `outline: var(--focus-ring)` (Input `:32`, Select `:34`, Textarea `:48`, fieldTrigger `:41`)

Drift: Select has a 32px tap-floor media query (`select.module.css:40-41`); Input does not. fieldTrigger uses `--font-size-ui` (`:26`) while the controls use `--font-size-control`; Textarea deliberately uses `max(control, body)` plus body line-height (`:21-22`).

**Target.** Add `src/styles/field.module.css` with `field`, `field:hover`, `field:focus-visible`, `field:disabled`, and `field::placeholder` recipes (W1-F01). Each control composes the shared recipe and keeps only its genuine differences (Textarea's font metrics, Select's native-appearance reset, Input's padding). Extend `fieldTrigger` to compose it instead of restating it. Put the phone tap floor in `src/styles/tapfloor.module.css` and compose it into Input, Select, Textarea, and RadioGroup, so the floor stops being a Select-only hand-written block (W1-F03, W1-F10).

**Changes.**
1. Create the shared recipe, migrate the four modules, delete the repeated declarations.
2. Compose the shared tap floor on every touch-target control (Input, Select, Textarea, RadioGroup); document any control that stays 32px (fine-pointer-only surface) rather than leaving the inconsistency implicit.
3. Align fieldTrigger's font size with `--font-size-control` or record why the trigger is denser.
4. Keep Textarea's seamless variant overrides intact.

**Verification.** Widget gallery for Input/Select/Textarea/fieldTrigger; existing visual guards; computed-style check that the only changes are the intended tap floor and font-size alignment.

**Approval.** A (CSS recipe, no widget API).

### 1.1 Input API gap for combobox hosts

`Input` is controlled-only and exposes no `ref`, rest props, `role`, combobox `aria-*`, `onKeyDown`, or `autoFocus` (`widgets/input/index.tsx:5-20`). That is why `CommandPalette.tsx:820-846` still renders a raw `<input>` (S3-F02). Extending `Input` to forward its ref and the native input attributes is a widget API change (class B), and the palette must keep `role="combobox"`, `aria-expanded`, `aria-controls`, `aria-activedescendant`, and its key handling. Do this with the Listbox work, not as a drive-by: a half-extended `Input` that still cannot host the combobox leaves the raw control in place.

**S3-F17 adds the size and field-chrome requirement.** `commandpalette.module.css:48-72` also re-derives Input's border, radius, `--field` fill, inset shadow, hover/focus border, and `:focus-visible` ring. Input's control is 32px; the palette's padding-driven field is about 44px. The extension needs `forwardRef`, native rest props, `role`/`aria-*`/`onKeyDown`/`autoFocus`, and a `size?: "md" | "lg"` (or `padding="roomy"`) variant that carries the `max(--font-size-control, --font-size-body)` floor and the phone 16px rule. Tests query `getByRole("combobox")` (`CommandPalette.edge.test.tsx:160`) and `aria-activedescendant` on the input (`CommandPalette.test.tsx:850-886`), so the ARIA must land on the real `<input>`. Keep the existing `autoFocus` biome-ignore and delete the palette's `.input` hover/focus rules after the swap.

### 1.2 Shared help/note caption

G1-F01 finds the settings `help`/`note` paragraph recipe in 22 CSS rules: 17 byte-identical `margin: 0; color: var(--ink-mid); font-size: var(--font-size-ui)` rules and five caption-size variants. G3-F02 independently found the same recipe in eight of those modules. Add `helpNote` (UI size) and `helpNoteCaption` to `src/styles/surface.module.css` and `composes` them onto each existing class name; no JSX changes, no `requireClass` keys move. `devicePairing` currently omits `margin: 0`; normalize it rather than preserving the drift. A layoutguard case that loads a composing stylesheet must list `surface.module.css` in its `cssFiles`. No widget API change; class A.

## Workstream 2 — Quiet pickers and trigger variants

### 2.1 `Select variant="quiet"`

**Sites.** `panes/spawn/Spawn.tsx:2757-2787` + `spawn.module.css:120-220`; `panes/session/chrome/StatusRow.tsx:115-140` + `statusrow.module.css:172-241`. The spawn source comment says it mirrors StatusRow.

**Current API.** `SelectProps` is `value`, `onChange`, `options`, `disabled`, `id`, `name` (`widgets/select/index.tsx:14-21`); no variant or ariaLabel.

**Proposed API.**
```ts
SelectProps & {
  variant?: "default" | "quiet"; // quiet: current value is the control, with chevron and transparent native select overlay
  ariaLabel?: string;            // names the control when there is no visible label
}
```
**Migration.** Convert both sites; forward `id` and `data-testid`; keep the readout aria-hidden. Preserve `spawn-effort` / `spawn-effort-value` selectors and the compact 12px StatusRow line.

**Third quiet-trigger site, CSS-only.** `ModelSwitchTrigger`'s closed state is a Popover `<button>` with the same pill geometry, `--hover-1` wash, inset focus ring, caption ink, chevron, and coarse-pointer tap floor (`modelswitch.module.css:7-71`). It is not a native `<select>` and should not become one. Share the closed-state trigger recipe between it and the new Select variant; keep ModelSwitch's Popover mechanics.

**Why a variant, not a new widget.** Two independent surfaces need the same closed-state native-select trigger, and `widgets/internal/fieldTrigger.module.css` already establishes that recipe for other pickers.

**Approval.** B.

### 2.2 `PathField variant="prominent"`

**Site.** `panes/spawn/Spawn.tsx:2932-2957` + `spawn.module.css:277-320,340-355`. The tile is a raw button that opens DirectoryPicker directly, missing PathField's draft/commit wiring.

**Current API.** `PathFieldProps` has `id`, `value`, `onChange`, `complete`, `listRecents`, `fallbackDir`, `onPanelClose`, `placeholder`, `disabled`, `ariaLabel` (`widgets/pathfield/index.tsx:60-104`). No variant.

**Proposed API.**
```ts
PathFieldProps & {
  variant?: "field" | "prominent"; // prominent: basename headline, full path subline, trailing "Change…"
}
```
**Migration.** Keep `#spawn-cwd` on the trigger, the caller's aria-label, `aria-expanded`, and `data-state`. Verify the picker stays keyed by the committed value and recents/draft behavior is unchanged.

**Why approval is needed.** The first caller is one tile; the design system sanctions the tile's DirectoryIcon but not a second directory-field shape. Jesse should decide whether the variant is worth its API surface.

**Approval.** B.

### 2.3 `ModelSwitchTrigger fill/shrink`

**Sites.** `spawn.module.css:81-91` and `statusrow.module.css` both pierce the trigger's Popover DOM with `:global(span)` and `:global(button)`.

**Current API.** `ModelSwitchTriggerProps` (`panes/session/chrome/ModelSwitchTrigger.tsx:46-82`) has no sizing prop.

**Proposed API.**
```ts
ModelSwitchTriggerProps & {
  fill?: boolean;   // stretch the trigger to the wrapper width (Popover stretchTrigger)
  shrink?: boolean; // truncate the value instead of widening the row
}
```
**Migration.** Delete both `:global` blocks. Check the 399px narrow-pane compression and layoutguard/overflowguard; the element that truncates may change.

**Approval.** B.

### 2.4 Shared Listbox and picker trigger

**Verified from S3, re-verified by P5.** Four hand-built ARIA combobox/listbox implementations share the same behavior: `shell/palette/CommandPalette.tsx` (combobox `:820-846`, listbox `:852-865`, options `:1141-1157`), `panes/session/composer/SlashCompletionMenu.tsx:47,59` (P5-F06), `widgets/modelCatalog/index.tsx:261-361`, and `widgets/pathfield/index.tsx:462-527`. Three sites duplicate the same option-id helper: `rowDomId` at `modelCatalog/index.tsx:109` and `pathfield/index.tsx:140`, and `optionId` at `SlashCompletionMenu.tsx:39`; the palette uses `palette-row-N`. All four keep DOM focus on the input and track the active option through `aria-activedescendant`; SlashCompletionMenu also carries a `mousedown` blur guard.

**Target.** A `widgets/listbox/` widget: a non-focusable `role="listbox"` container plus `ListboxOption` rows with `id`, `selected`, `disabled`, `onSelect`, and free content, plus `optionDomId(id, key)` and `ariaActiveDescendant(id, activeKey)`. It is not a `renderRow` API; every site renders its own row content. `ModelCatalog` and `PathField` are widgets themselves, so they import `../listbox`, never the barrel, to avoid a cycle. The palette's input stays a raw `<input>` until `Input` gains `ref`, rest props, `role`, the combobox `aria-*` (`expanded`/`controls`/`activedescendant`), `onKeyDown`, and `autoFocus` (S3-F02; see the `Input` gap in Workstream 1).

**Why this is not appearance-only.** The duplicated part is the keyboard/DOM contract, not the pixels: active-descendant ids, scroll-into-view, the blur guard, and the option role/selection state. P5 re-verified the fourth site and adds that `Composer.tsx:1522-1523` owns the `aria-activedescendant` wiring, so the listbox must remain externally driven.

**W3 measured the engine, not only the ARIA.** `modelCatalog/index.tsx:109-111,202-254,290-379` and `pathfield/index.tsx:140-142,349-456,481-546` each carry the option DOM, the Arrow/Home/End map, `scrollIntoView`, and the mousedown blur guard, about 120 duplicated lines. The two implementations have already drifted in small ways. Expose `useListbox` (active key, id helpers, keyboard map, scroll) plus a presentational `Listbox`/`ListboxOption`; migrate ModelCatalog and PathField first, then CommandPalette and SlashCompletionMenu. Preserve `ul > li[role="option"]`, `aria-selected` only on the active option, `aria-activedescendant`, Home/End/Enter, the mousedown guard, and the sheet variant's `listSheet` class hook. The palette still needs the `Input` extension before it can adopt the component.

**PickerTrigger is now proven, not deferred (W3-F01).** Three closed triggers carry the same structure and comments: `modelCatalog/index.tsx:455-489`, `pathfield/index.tsx:588-661`, and `ModelSwitchTrigger.tsx:266-293`. Each renders a button with a value span, a `Chevron`, and a load-bearing whitespace node before a visually hidden action. The ARIA has drifted: PathField sets `aria-haspopup`/`aria-expanded` for its directory dialog (`:595-596`), while ModelCatalog and ModelSwitchTrigger set neither. Add an internal `PickerTrigger` that owns the button, value/default rendering, chevron, action label, whitespace node, and popover trigger wiring, with an `ariaMode: "listbox" | "dialog"` (or explicit `aria-haspopup`/`aria-expanded`) and a `variant="quiet"` for ModelSwitchTrigger's chip geometry and `title`. It must keep the whitespace text node as a direct child so the accessible name stays `/home/jesse — browse`, not `/home/jesse— browse`. PathField's accessible-name tests and ModelSwitchTrigger's tests are the pins. About 35-40 duplicated lines across three sites. Export it from the barrel only if ModelSwitchTrigger adopts it; otherwise keep it internal. Approval C, and it needs Jesse alongside the Listbox.

**Approval.** C (new widget and locked API). This needs Jesse's approval before any code.

### 2.5 Shared image-picker input

**Sites.** `Spawn.tsx:3033` + `:2184-2192`; `Composer.tsx:1626` + `:1251-1264` (I1-F06, P5-F03). P5 confirms both surfaces and notes `AttachImageControl` already takes `inputRef`, so the prop change is one place; the re-pick-resets-value behavior and `imagePicker`'s capture toggle are load-bearing.

**Target.** Move the hidden `<input type="file">` and its change handler beside `AttachImageControl` as `ImagePickerInput`, or a `useImagePicker()` hook returning `{inputRef, onFileChange}`. Both surfaces keep `AttachImageControl` for the visible control. The handler must keep `event.target.value = ""` so the same file can be picked twice, and the camera-free menu path must still reach the input ref. Keep the per-surface `data-testid` as a prop (`spawn-attach` vs `composer-attach`).

**Approval.** A (local component or hook). Extending `Dropzone` with `accept` and an imperative open is the alternative, but it is a widget API change and not the default recommendation.

### 2.6 Picker panel and field CSS

**Sites.** `widgets/modelCatalog/modelCatalog.module.css:40-158`, `widgets/pathfield/pathfield.module.css:46-218`, and the third field/list copy at `widgets/directorypicker/directorypicker.module.css`. W3 measured 64 identical declaration lines, about 59 restated, and the copies have drifted on field background, font, and active-row hover.

**Target.** Move the panel/field/list/row recipes into `widgets/internal/pickerPanel.module.css` and have all three stylesheets `composes` from it. Keep genuinely local declarations local; parameterize PathField's mono face as `--picker-field-font` rather than copying the block. If the Listbox/PickerTrigger widgets land first, those widgets can own the recipes instead; either way there is one home.

**Migration.** Expect a visual diff where the pickers disagree today; that is the consistency fix. Preserve the mobile sheet width (`max-width: calc(100dvw - var(--space-4))`) and the 320px/60vh list cap. No test IDs or accessible names change. DirectoryPicker must stop importing a widget's private stylesheet when it moves to `Dialog` (F11).

**Approval.** A (internal CSS share). The visual diff needs design review, not a widget API decision.

## Workstream 3 — Rows, settings, and single-choice lists

### 3.1 `ListRow value` for settings rows

**Sites.** `panes/spawn/MobileSettingRows.tsx:79-118` + `MobileSettingRows.module.css:11-81,140-154`; five rows (Harness, Working directory, Branch, Access mode, Plugins).

**Current API.** `ListRowVariant = "collection" | "plain"` (`widgets/listrow/index.tsx:13`); content props are `label`, `leading`, `chips`, `meta`, `variant` (`:15-26`); no trailing value.

**Proposed API.**
```ts
ListRowProps & {
  value?: ReactNode; // trailing readout on the same line, truncates before the chevron
}
```
or a third variant `variant="settings"` if the row's geometry differs enough. Convert `MobileSettingRow` to compose ListRow.

**Migration.** Keep `data-testid="mobile-spawn-row"`, `data-label`, `data-state`, `aria-label="Label: value"`, `aria-expanded`, the 48px floor, and the inset focus ring. The multi-line path value needs a wrap escape hatch; do not lose it.

**Approval.** B.

### 3.2 `RadioGroup variant="list"`

**Sites.** `panes/spawn/MobileSettingRows.tsx:139-156` + CSS `:87-130`; used twice (Harness, Access mode). Each option is a toggle button with `aria-pressed` although the sheet is single-choice.

**Current API.** `RadioGroupProps` has `label`, `value`, `onChange`, `options`, `disabled`, `description` (`widgets/radiogroup/index.tsx:12-25`); no variant.

**Proposed API.**
```ts
RadioGroupProps & {
  variant?: "stack" | "list"; // list: full-width 48px rows, no dot, trailing selected mark, for a Sheet
}
```
**Migration.** The semantic change is `role="radio"` + `aria-checked` instead of `aria-pressed`; update queries from `getByRole("button")` to `getByRole("radio")`. Keep the sheet's focus return.

**Shared keyboard logic.** RadioGroup, SegmentedControl, and Menu each carry a roving-index implementation; RadioGroup and SegmentedControl differ on the all-disabled case (`from` vs `-1`) (W1-F02). Extract `widgets/internal/rovingRadio.ts` (index helpers plus a `useRovingRadio` hook for Arrow/Home/End, focus-and-select, and an all-disabled no-op) and use it in RadioGroup and SegmentedControl. Leave Menu's copy for a follow-up unless the helper fits without changing its menu semantics. AskDock's tablist is the fourth copy (`AskDock.tsx:258-278`; P6-R02): it has no disabled options and wraps with modulo, so give the helper a tablist mode rather than writing a second hook. Keep the tablist itself local: a Tabs widget would serve one site and fail the design system's recurrence test. The tab's browser-default focus ring is part of the same gap; the helper should own the ring recipe.

**Tap floor.** `radiogroup.module.css:19-46` has no phone tap floor while the sibling choice controls do (W1-F10); compose the shared tap-floor recipe from W1-F03 and verify the taller option still fits at 375px.

**Approval.** B.

### 3.3 ListRow adoption

**Verified usage.** 11 production consumers: `panes/plugins/PluginRow.tsx`, `panes/session/NextNeedsYou.tsx`, `panes/settings/sections/agents.tsx`, `credentials/InstanceRow.tsx`, `credentials/ProviderConnection.tsx`, `hosts.tsx`, `marketplacesPlugins/MarketplacesSection.tsx`, `mcp.tsx`, `settings/SettingsNav.tsx`, `shell/activitybar/activityRows.tsx`, `shell/rail/PinSectionPicker.tsx`. Seven are settings files; spawn, zoom, and transcript have none. The remaining question for S1/P8 is which local rows that do not use ListRow should adopt it, not whether the existing adoption is correct. Only move rows whose semantics match a collection or plain row. S2 has already answered for the activity rows: `AgentRow` and `JobRow` use ListRow, `WatchRow` does not (3.3's WatchRow bypass).

**WatchRow bypass.** `activityRows.tsx:168-186` and `activitybar.module.css:99-154` hand-build the row grammar ListRow owns (S2-F03). Either give `Disclosure` a summary slot (`summaryAsChild?: boolean` or `summaryClassName?: string`, class B) so a plain `ListRow` can sit inside the summary without extra padding, or add a local `ActivityRowContent` that mirrors ListRow's geometry. Keep `data-activity-anchor={row.id}`, `id="sidebar-${row.id}"`, the summary text, and the `sidebar-watch-<id>` testid. `<ListRow>` is a `<div>`, which is valid flow content inside `<summary>`; reconcile the summary's own padding and hover before deleting the local classes.

### 3.4 `SegmentedControl` wrapping and focus

`segmentedcontrol.module.css:113-118` ellipsizes every option label, while the design-system contract says the Overview category strip wraps without shortening labels or counts at narrow widths and XL text (`docs/web-ui/design-system.md:199-201`, S2-F06). Add `wrap?: boolean` (or `optionLayout?: "single" | "stacked" | "wrap"`) that lets `.optionLabel` use `white-space: normal; overflow-wrap: anywhere` and lets the track grow in block size; pass it from `ActivitySidebar.tsx:195-208`. This is a widget API extension (class B) with a visual change, so it needs Jesse's approval. Add a layout guard for the 320px and XL-text cases; the `ActivitySidebar.test.tsx:780-800` span-count assertions survive.

The same lane found `ActivitySidebar.tsx:80-89` querying `[role="radio"][aria-checked="true"]` inside the widget to focus the checked option (S2-F12). Extend `SegmentedControl` with `autoFocus?: boolean` (focus the selected option, not the container) or a forwarded ref to the selected option, then delete the query. Keep `FocusScope autoFocus={false}` deliberate and the mobile focus-restore test green. This is lower priority than the wrap mode and the same widget API review can take it.

### 3.5 DirectoryPicker adoptions

W3 found the picker bypassing five existing widgets. These are separate from the dialog-geometry extension in 4.1.

- **Rows → ListRow (W3-F07).** `directorypicker/index.tsx:234-250,399-412` and `directorypicker.module.css:105-167,256-260` re-implement ListRow for the browse rows and recents. Render both through `ListRow variant="plain"` with `leading`, `label`, `meta`, `trailingAction`, `onSelect`, `aria-label`, `title`, and `disabled`; the drill `›` is the trailing action, not row content. Preserve the tests that query `Open /work/app` and `Open recent /recent/app` (`directorypicker.test.tsx:52,56,445`) and verify the 40px tap floor against ListRow's plain variant before deleting the local rule.
- **Glyphs and disclosure (W3-F08).** `directorypicker/index.tsx:265,271-280,410` draws parent, up, and drill glyphs as text and restyles native `<details>`; `directorypicker.module.css:71-82` carries the styling. Use `Chevron` and `Disclosure`, keeping `aria-label="Earlier directories"` and the summary interaction the tests drive. If `Chevron` lacks `up`/`right`, that part becomes a class-B extension rather than an adoption; verify before promising it.
- **Field errors → InlineNotice (W3-F09).** The picker's `.error` at `index.tsx:374-378` is one of three hand-rolled field errors (also ModelCatalog `:279-283` and CollectionEditor `:195-199`). Use `InlineNotice tone="danger"` and keep the `role="alert"` semantics and the asserted error text. If the box is too heavy for a one-line error, a `plain` variant is a class-B extension, not a silent restyle.
- **Empty and loading → EmptyState/Skeleton (W3-F10).** `collectioneditor/index.tsx:150-153` and `directorypicker/index.tsx:251,390-397` hand-roll empty and loading placeholders. Use `EmptyState size="compact"` for the empty messages and `Skeleton` (or `Loader` with `role="status"` preserved) for "Loading directories…". "Press Go to browse this path." is instructional, not empty; leave it a status line or route it through InlineNotice.

**Approval.** A for the swaps; B only for the Chevron or InlineNotice extension if the widget cannot express the existing state.

### 3.6 AskDock answer rows

P6 read the four AskDock files in full (1,154 lines) and found three raw controls in `AskQuestionCard.tsx`, plus a tablist and two keyboard handlers. The answer rows are a real grammar the widget layer cannot own: native radio/checkbox inputs in one continuous prose line with a bold label, an optional `· recommended` suffix, a detail line, and a deselectable "Something else…" row. `RadioGroup` is a dot-led stack and has no `detail`, `recommended`, checkbox mode, or `aria-labelledby`; adding a one-site `variant="answer"` would put AskDock's grammar in every RadioGroup consumer's API.

**Local `AnswerOptionRow` (P6-R01, P6-R05).** Extract a local component in `askDock/` that renders the label, native input, label text, recommended suffix, detail, and the alternative row. `AskQuestionCard.tsx:151-178` and `:181-201` become two calls; `askquestioncard.module.css:59-79` and `:117-127` share one `.rowBox` recipe. Keep the single-select radio name, the multi-select checkbox array, the deselect-on-second-click alternative, the free-text/note dual mode, `data-ask-question`, and `data-ask-free-input`. The alternative row currently has no `:has(:checked)` wash even though choosing it sets the radio; apply the same selected recipe as the option rows (P6-R05/R07). Approval A.

**Adopt `Input` after the S3-F17 extension (P6-R01).** `AskQuestionCard.tsx:239-251` hand-rolls the field and `askquestioncard.module.css:129-148` re-creates the field chrome because `Input` has no `ref`, `aria-labelledby`, or `data-*` passthrough. Once the Input extension in Phase 3 lands, pass the ref, `aria-labelledby`, `data-ask-free-input`, placeholder, value, and onChange through `Input` and delete `.textInput`. This is the same extension the palette needs; it is not a second Input API.

**Recommended marker (P6-R08).** The dock shows `· recommended`; the read-only transcript card shows `Eyebrow` "Recommended" (`askUser.tsx:75-78`). Pick one presentation before migrating either surface. The suffix is the current ask-dialog rework, so the likely change is updating the transcript card, but that is a product/copy call for Jesse or the P3 owner, not a silent dedupe.

**Small cleanups (P6-R09, P6-R10, P6-R12).** Derive the enabled control selector from one `ANSWER_CONTROL_SELECTORS` list and mark the controls with `data-ask-control`; replace the `✓` text glyph with `ToolIcon kind="check"`; use one `ASK_PROMPT` constant and the shared `answeredCountLabel` for the visible anchor, the live region, and the footer count.

**Tests and size.** `AskQuestionCard.test.tsx` (351 lines) drives the native controls and the recommended suffix; `AskDock.test.tsx` drives the tablist, the count announcement, and the Mod+Enter path. Keep the test IDs, the `aria-label` answered suffix, and the announcement text. The raw-control bypass count for this file should go from 3 to 0. About 140 lines of TSX+CSS; about 40 are deleted by the local row and the Input adoption.

**Approval.** A for the local row, selector, glyph, and copy constants; B only for the already-planned `Input` extension. No new widget.

### 3.7 Marketplace source fields and PathField wiring

G3-F04 finds the add and edit marketplace forms build the same source group: `RadioGroup` over `MARKETPLACE_SOURCE_OPTIONS`, then the same Git URL, owner/repo, and Local path branches with identical placeholders and the same `PathField` wiring (`MarketplacesSection.tsx:347-408` and `MarketplaceDetailBody.tsx:352-408`). Extract a local `MarketplaceSourceFields({ value, onChange, disabled })`; keep the emitted `{ kind, url | repo | path, ref? }` shape. The add form has Branch/tag and Name, while the edit form has no ref field, so a marketplace's ref cannot be edited; decide whether to expose it or record the omission as a product ruling (class D).

G3-F06 finds four copies of the `PathField` bundle (`fields.tsx:110-148`, used at `:261` and `:363`; `collectionFields.tsx:190-219`; `MarketplacesSection.tsx:376-385`; `MarketplaceDetailBody.tsx:396-406`): same directory source, `kind`, `complete` callback, `ariaLabel`, and placeholder. Export `PathFieldRow` from `launchShared/fields.tsx` for the launch and collection callers, and add a small marketplace wrapper for the two forms. Do not change `PathField`'s API. Approval A for both.

### 3.8 Session-chrome controls and settings rows

- **Reasoning-effort control (P7-F01, High).** `StatusRow.tsx:97-151` + `statusrow.module.css:172-241` and `Spawn.tsx:2758-2776` + `spawn.module.css:131-212` are the same "value is the control" native-select overlay: readout, chevron, hidden label, transparent `<select>`, and focus-within ring. Extract `ReasoningEffortControl` in `panes/session/chrome/` taking `{levels, current, defaultLevel, onChange, testId, disabled?}`; keep each caller's testid prefix and its existing radius/disabled treatment. This is a local component, not the quiet `Select` variant; the two can converge later. Approval A.
- **Double meter (P7-F05, Medium).** `StatusRow.tsx:176-197` renders a visually hidden raw `<meter>` and a visible `Meter` for the same value. Delete the raw element, keep `Meter` as the spoken source, and keep the accessible name string unchanged. Approval A; verify the meter announcement in `StatusRow.test.tsx`.
- **TopNotesPanel disclosure (P7-F06, Medium).** `TopNotesPanel.tsx:201-235` hand-builds the button/chevron/`aria-expanded` disclosure that `Disclosure`/`DisclosureHeading` already provide. Adopt `Disclosure` with a summary ReactNode; keep the per-state testids (`top-notes-summary`, `top-notes-collapse-trigger`), `data-reason`, the 28px header, outside-press dismissal, and Escape-without-blur. Approval A unless the per-state accessible name forces a Disclosure extension.
- **Panel section rhythm (P7-F07, Medium).** `detailspanel.module.css:4-16` and `notespanel.module.css:6-21` are byte-identical section rules; `DetailsPanel.tsx:130-139` hand-rolls an `<h3>`, `NotesPanel.tsx:56-63` uses `Eyebrow`, `TasksPanel.tsx:357-359` uses an `<h4>`, and `TopNotesPanel.tsx:218` uses an `Eyebrow` span. Extract a local `PanelSection({ title, headingAs, children, action? })` in `session/chrome/` that renders `Eyebrow as={headingAs}` and the shared section CSS. Approval A.
- **PropertyList row attributes (G1-F10, Low, class B).** `settingsField.tsx:10-16` and `hosts.tsx:562` wrap values in `<span data-field>` because `PropertyList` rows cannot carry `data-*`/`id`. Add `rowProps` to `PropertyListRow`/`StackedPropertyListRow` and delete `FieldValue`; this is a widget API extension, so it needs Jesse and a gallery row.

## Workstream 4 — Raw controls and local widget-concept classes

The design-system audit (`scripts/ds-audit.mjs`, baseline at the audit scratch root) reports:

- Raw controls: the ds-audit baseline reports 20 `<button>`, 6 `<input>`, 2 `<select>`, 2 `<textarea>` outside `src/widgets` and `src/dev`. Three of those (2 button, 1 textarea) are in `transcriptReaderTestUtils.tsx`, so the production count is 18 `<button>`, 6 `<input>`, 2 `<select>`, 1 `<textarea>` across 17 files.
- 35 module classes re-implementing a widget concept.

Exact raw-control files:
`ModelSwitchTrigger.tsx`, `StatusRow.tsx`, `TopNotesPanel.tsx`, `Composer.tsx`, `SlashCompletionMenu.tsx`, `askDock/AskDock.tsx`, `askDock/AskQuestionCard.tsx` (3), `ToolRow.tsx` (4), `tools/jobWatch.tsx`, `transcriptReaderTestUtils.tsx` (test utility, exclude from production count), `credentials/instanceDialogs.tsx`, `MobileSettingRows.tsx` (2), `Spawn.tsx` (3), `zoom/ActivityPeek.tsx`, `zoom/CascadeSpine.tsx`, `CommandPalette.tsx` (3), `rail/RailRow.tsx`, `statusbar/ActivityChips.tsx`.

Exact module-class sites:
`plugins/pluginDetail.module.css .chips`; `chrome/detailspanel.module.css .meter`; `chrome/statusrow.module.css .contextMeter`; `composer/slashcompletionmenu.module.css .menu`; `session/session.module.css .loadingSkeleton`; `transcript/entityref.module.css .card`; `transcript/flow/flowoverlay.module.css .pillSlot`; `transcript/toolcallitem.module.css .failureBadge`; `transcript/tools/delegateStatus.module.css .card .toolsChips .toolChip`; `transcript/tools/jobWatch.module.css .card`; `transcript/tools/subagentmodule.module.css .card .quotesEmpty`; `transcript/tools/taskcard.module.css .card`; `credentials/ProviderConnection.module.css .cardHead`; `hosts.module.css .buttonRow .chipRow`; `marketplacesPlugins.module.css .chipRow .switchRows`; `settings/sections/transcript.module.css .cards`; `spawn/MobileSettingRows.module.css .rowButton`; `spawn/spawn.module.css .directoryButton`; `zoom/zoom.module.css .activityChip`; `holdhints.module.css .chip`; `commandpalette.module.css .pill .pillBack`; `rail/RailHost.module.css .chipBar`; `rail/RailRow.module.css .chevronButton .contextCard`; `statusbar.module.css .chips .chip .chipGlyph .chipName .chipCount`.

**Target.** Classify each as (a) replace with Button/IconButton/Select/Input/Textarea, (b) replace with Chip/Card/Meter/StatusDot/Badge, (c) legitimate layout container (`.buttonRow`, `.chipRow`, `.chipBar`) and keep, or (d) bespoke transcript grammar and keep with a reason. The lane reports supply the classification; the plan should not mechanically replace every hit.

**Approval.** A for replacements; B where a widget lacks a needed prop.

### 4.1 Existing-widget swaps found in the shell

- **Palette pill → Chip.** `CommandPalette.tsx:812-819` hand-builds Chip's pill and an `×` remove button; `commandpalette.module.css:17-46` repeats its geometry. `Chip` cannot take an explicit remove label (it derives `Remove <text>`) and has no accent tone. Add `removeLabel?: string` and an accent treatment, then delete `.pill`/`.pillBack` (S3-F03). The existing test name `back to command list` must survive.
- **SessionMenu dialogs → ConfirmDialog.** `SessionMenu.tsx:298-320` and `:326-346` reproduce ConfirmDialog's title/body/Cancel-danger footer. ConfirmDialog already drives `ForceStopDialog.tsx`. Swap both (S3-F04). Behavior change: ConfirmDialog disables Cancel while `busy`; SessionMenu's Cancel stays enabled today. State it in the PR.
- **Palette command error → InlineNotice.** The palette renders search errors with `InlineNotice` (`:1071`, `:1081`) but command errors as a danger `Chip` inside `.error` (`:847-851`). Render both with InlineNotice and delete `.error` (S3-F05). `role="alert"` still holds because danger InlineNotice defaults to `live="alert"`.
- **PaneTab dot → StatusDot.** `PaneTab.tsx:51-55` wraps StatusDot in a `.dot` span only to add a margin; put `gap: var(--space-1)` on `.tab` and render StatusDot directly (S3-F09).
- **DirectoryPicker and DiagramViewer → Dialog.** `directorypicker/index.tsx:5,206-212` renders `OverlayPanel` directly with `panelClassName`/`bodyClassName`, and `mermaid/DiagramViewer.tsx:4-5,21-22,113-121` composes `dialog.module.css`'s private `.panel` and passes it to `OverlayPanel` (W3-F11). Add `panelClassName` and `bodyClassName` to `Dialog` (parity with `Sheet`), then render both through `Dialog`. Keep the mobile full-screen width, the 760px viewer geometry, and `motion="float"`; if Dialog cannot express float, document an explicit OverlayPanel exemption rather than leaving the reach-in. Tests query `role="dialog"`, which both paths preserve. Class B.

### 4.2 Activity count button (proposal)

`ActivityPeek.tsx:84-96` and `statusbar/ActivityChips.tsx:55-77` render the same four fields from `ACTIVITY_TABS` (`shell/activitybar/activityTabs.tsx:51-106`): glyph, label, count, and accessible count label. S2 and M1 found the pair independently (S2-F02, M1-F01); S2 rates it High, M1 Medium. They differ in activation (peek vs Overview) and chrome (hover wash vs `--surface-canvas` pill). Two options: a new `CountButton` widget with `glyph`, `label`, `count`, `accessibleLabel`, `compact`, `orientation`, and `pressed`, built on Button's quiet chrome and forwarding its ref plus rest/`aria-*` so the zoom copy can stay a Popover trigger; or a local shared component under `shell/activitybar/` if the widget API is not wanted. This is a new widget API (class C), so it needs Jesse's approval, and two call sites is the minimum, not a mandate. Preserve the accessible names and `data-activity-tab`/`data-pane-id`/`data-session-ref`, which `ActivityPeek.test.tsx`, `StatusBar.test.tsx`, and `scripts/cascadeguard/run.mjs` query, and preserve focus returning to the chip after the sidebar closes.

### 4.3 Activity category glyphs

`activityTabs.tsx:55,66,88` uses typed `"⌘"`, `"$"`, and `"☑"` while the watch category at `:77` renders the drawn `WatchGlyph` (S2-F05). Replace the three strings with existing `ToolIcon` kinds (`delegate`, `job`, `tasks`) if those marks read correctly, or add three category glyphs to `railIcons.tsx` or a neutral `shell/icons` module. This changes what users see on both chip surfaces, so it is a design call; the `activity-tab-watch-glyph` testid and accessible names must survive. Set an explicit size to match the 13px watch glyph or accept ToolIcon's 14px.

### 4.4 Key sequences in the cheatsheet and hold hints

`CheatsheetOverlay.tsx:137-161` and `HoldHints.tsx:159-167` render key sequences by hand while `ShortcutList` and `KeyHint` exist; `ShortcutList` cannot express alternative bindings or multi-press sequences (S2-F04). Extend `ShortcutList` (class B) or add a shared `KeySequence` renderer (`{sequences, orLabel}`) that `ShortcutList`, the cheatsheet, and HoldHints all call. Keep `serializeChord`-based keys and the "Unbound" copy. The cheatsheet test asserts `listitem` and `.closest("li")`; ShortcutList renders a `<dl>`, so those queries move to term/definition or the test changes.

### 4.5 FocusScope wrapper style

`activitybar.module.css:40-46` reaches into FocusScope's unstyled wrapper with `.sidebar > div` / `.sidebarMobile > div` (S2-F08). Add `className?: string` (or `asChild`) to `FocusScope` (class B) and pass the flex class from `ActivitySidebar`; delete the selectors. No behavior change; verify the sidebar body fills on mobile and desktop.

### 4.6 Phone Overview overlay semantics

The phone Overview is a hand-rolled aside (`ActivitySidebar.tsx:150-165`, wrapped at `StackHost.tsx:701-703`) while the phone Sessions drawer is a `Sheet` (`MobilePanel.tsx:26-36`) (S2-F07). At minimum set `role="dialog"` and `aria-modal="true"` on the mobile aside, leaving desktop `complementary` (the existing test asserts that role on desktop). The larger option is a chrome-less `Sheet` variant, which is a visual change and a class B/C decision. Preserve the `activity-sidebar` testid's ancestor expectations and the Escape focus-return path (`ActivitySidebar.test.tsx:840`).

## Workstream 5 — Notices, status, and loading consistency

- `InlineNotice` is already used in 47 production TSX files (JSX search, tests and dev excluded); the remaining problem is not adoption but inconsistent status rendering. I1-F01 (plugin preview three ways) and I1-F02 (Retry in four Button variants) are the concrete spawn cases. Fix the convention once in the design system: a notice's resolving action is quiet xs; form-level recovery is quiet default.
- The surrounding shared widgets are also healthy: `FormRow` 18 files, `ConfirmDialog` 16, `EmptyState` 31, `Skeleton` 31, `Loader` 15, `StatusDot` 9, `CollectionEditor` 4, `ShortcutList` 4 (same JSX-search basis). These are not adoption emergencies; the plan targets the specific outliers the lanes found.
- **Contrast defect, not just duplication.** FormRow's error text uses `--danger` at 3.98:1 on white, while InlineNotice correctly uses `--danger-ink` at 6.28:1 (W1-F11). Swap the token in `formrow.module.css:52`; this changes a user-visible color, so it needs design sign-off before landing. The ratios were computed from token hex values, not measured in a browser.
- **Plugin metadata rows.** `LauncherPluginDetails.tsx:72-80` and `PluginDetailBody.tsx:157-171` hand-build the same rows (I1-F12). Export a `pluginMetadataRows({version?, marketplace?, source})` helper from `panes/plugins/` and keep the settings-only marketplace label formatting at the call site; the version fallback (`"unknown"` vs omitted) must stay explicit.
- **Doc pane notices → InlineNotice.** `DocPane.tsx:63-70` and `:140-144` (with `docpane.module.css:23-34`) hand-build the in-flow notice that `InlineNotice` owns; the sibling plugin pane already uses it (M1-F02). Pass `live="off"` for the truncation notice so it cannot become a second `role="status"` and break `DocPane.test.tsx:459`. The truncation site pairs a Chip with text, so either give InlineNotice an inline body row (an EXTEND for Jesse) or keep that one local and say why. The caption-to-UI-font-size and margin changes are visible, so verify them in the gallery.
- **Disabled-Start caption.** `Spawn.tsx:2812-2816` deliberately renders the reason as a caption, not a notice (I1-F13); keep it, and record it in the intentionally-bespoke section rather than folding it into the InlineNotice convention.
- **Composer storage-stall notice.** `Composer.tsx:1370-1375` + `composer.module.css:20-23` hand-build a `role=status` div while the same file already imports `InlineNotice` (`:1477-1493`) (P5-F10). Rendering `InlineNotice tone="neutral" live="status"` adds notice chrome and changes the folded composer's height; get a visual sign-off (class D if the folded layout moves) or keep the caption and record why. The `aria-label` does not replace the visible text.

### 5.1 Chunk-failure boundary

**Verified.** Three copies of the same React failure boundary: `shell/DockRegion.tsx:19-133`, `shell/rail/index.tsx:46-203`, and `panes/settings/sections/credentials/ConnectProviderDialogBoundary.tsx:30-171` (S3-F01). The loaders already share `shell/chunkRetry.ts`, but the state machine is copied: `getDerivedStateFromError` capturing `error.message`, `retry()` clearing the failure and swapping in a fresh lazy payload, the second-strike `reloadAvailable` rule, the stale-error test, and the `reset*ForTests` singleton reset. About 150 duplicated lines. The rail and connect dialog rethrow non-stale errors; DockRegion catches every error, which is the one behavioral disagreement.

**Target.** One `shell/chunkBoundary.tsx` exporting `useChunkHost({create, isStale})` and `ChunkFailureBoundary({isStale, onRetry, reloadAvailable, renderFailure, children})`. Each caller keeps its presentation: DockRegion renders EmptyState, the connect dialog renders Dialog, the rail keeps its width-preserving shell. Keep the test ids (`dock-loading`, `rail-chunk-failure`, `connect-dialog-load-failure`), the exact failure copy, and the `resetDockChunkForTests` / `resetRailChunkForTests` / `resetConnectDialogChunkForTests` exports tests import. DockRegion's `cancelConversationHandoff(generation)` moves to the boundary's `onError`. Unify on the rail/connect rethrow behavior for non-stale errors; call out that DockRegion changes behavior.

**Approval.** A for the shared boundary. The DockRegion rethrow change is a behavior change (a logic bug would now unwind past the dock instead of being shown as a chunk failure), so get Jesse's sign-off on that half before landing; if the dock must keep catching every error, keep that policy in the caller's `onError` instead of the shared boundary.

**Also.** DockRegion's loading placeholder re-derives EmptyState's centered stack (`DockRegion.module.css:4-10`, S3-F11). Keep the Loader (EmptyState is not a loading state) and record the six lines, or add a `centered` layout to Loader only if a third caller appears.

### 5.2 Activity collection gate

AgentsTab, JobsTab, and WatchesTab each copy the same loading/empty/error sequence: permanent-empty → `EmptyState`; missing or incomplete rows → `Skeleton`; complete-empty → `EmptyState`; body; `ActivityPageBoundary`; error → `InlineNotice live="off"` (S2-F01). One local `ActivityCollectionGate` in `shell/activitybar/`, taking `{noun, collection, totalKnown, total, children}`, removes about 30 duplicated lines. It is not a widget; the four states already exist. Preserve `data-activity-anchor`, `role="status" aria-label="Loading"`, `data-testid="empty-state"`, `live="off"`, and the Agents inline-empty placement if its scroll-retention tests need it. Approval A.

- **P7/P8 now reported.** P7's status, meter, timestamp, and loading findings are merged in Workstreams 5 and 8; P8's local status-dot and live-status findings are P8-F08/F09 in Workstream 10. Verify every remaining local status dot against StatusDot/Meter/Timestamp/Skeleton/Loader before proposing a change. EmptyState is not a loading state.
- **P8 cold-start honesty.** P8 recorded no separate cold-start duplication finding; its live-status and conversation-face work is P8-F05/F09 in Workstream 10.

### 5.3 Marketplace load notices and the launch save status

G3-F07 finds three copies of the "Failed to load X, Retry" notice: `marketplacesPlugins/index.tsx:196-209`, `:210-223`, and `MarketplaceDetailBody.tsx:415-431`. They differ only in noun, fetch method, loading flag, and `data-reason`; extract a local `LoadErrorNotice({ label, error, retrying, onRetry })`, keep the Retry labels and `data-reason="unavailable"`, and leave `InlineNotice` unchanged. Approval A.

G3-F13 finds the launch save error (`LaunchConfigForm.tsx:434-436` + `LaunchConfigForm.module.css:29-33`) rendered as ordinary caption ink even though `data-state="error"` is set. Style `[data-state="error"]` in danger ink (check the token contract) or render the error branch as `InlineNotice tone="danger"`; keep `data-testid="launch-config-status"`, `aria-live="polite"`, and the self-clear behavior for `saved`. Approval A; this is a visible color change.

### 5.4 Settings section load/refresh gate and status treatments

G1-F02 finds ten settings sections copy the same load/refresh state machine: `hosts.tsx:339-356`, `storage.tsx:38-56`, `hub.tsx:49-67`, `inrepo.tsx:101-108`, `project.tsx:176-188`, `launchServer.tsx:151-163`, `agents.tsx:46-50`, `dirListSetting.tsx:254-259`, `agentsDoc.tsx:154-158`, and `mcp.tsx:294-296`. The first three are the identical `loading && !data → Skeleton`, `!data && error → InlineNotice` trio; the project/launchServer pair is the byte-identical `refresh-failed` arm. Add a local `SectionGate({ loading, error, title, onRetry, skeletonLines, children })` plus `RefreshErrorNotice` in `panes/settings/sections/`, not a widget. Preserve `data-reason="unavailable"`/`"refresh-failed"` and the early-return ordering; the four untitled "Failed to load: {error}" sections are a second variant, converted one at a time. Approval A.

G1-F05 and G1-F06 add the status-line and warning variants: `about.tsx:73-77` is the remaining plain error paragraph, while three quiet-status implementations coexist; `Diagnostics` (`diagnostics.tsx:15`) and a local warnings box both render the same `InlineNotice title="Warnings"` shape. Standardize on one status line and either widen `Diagnostics` with per-item `message`/`reason` or reuse it. Approval A, with the `Diagnostics` API widening as class B.

## Workstream 6 — Transcript tool chrome

**Verified current state.** The tools directory has 27 `*Body` component declarations across 16 files. Shared bodies already exist in `tools/bodies.tsx`: `HeadClippedOutputBody`, `TailFoldedOutputBody`, `EditFileBody`, `WriteFileBody`. The remaining duplication is the card/row chrome around them, not 243 separate bodies.

Four modules define a local `.card`: `delegateStatus.tsx` (`:323`), `jobWatch.tsx` (`:779,785,802`), `subagentModule.tsx` (`:266`), `taskCard.tsx` (`:332`). `askuser.module.css`, `compactcontext.module.css`, and `sandboxescalation.module.css` add further local row/chip/status classes.

**Target.** A local `ToolCard`/`ToolStatusCard` in the transcript tools layer with slots for leading status, title, meta, body, and footer/actions. It is not a widget: the transcript grammar is bespoke. Migrate the four card modules where the semantics match; keep tool-specific layouts where they differ.

**Verified from P3.** P3 read all 27 files. Two High findings sit above the local `ToolCard`:

- **One delegate status vocabulary (P3-F01, High).** `subagentModule.tsx:84-104,285-288` owns `STATUS_GLYPH` and `DELEGATE_LABEL`; `ToolCallItem.tsx:46-52` owns `DELEGATE_INDICATOR_STATE` mapping to Cadence; `delegateStatus.tsx:166-184,330` owns `statusToTone`/`statusLabel` and renders a Chip. The same delegate can therefore read with a different glyph, word, and tone on different surfaces. Put one `{ label, chipTone, cadence, glyph }` table in `subagentModuleStore.ts` (or appwire-client, since native also renders delegate status) and consume it from both cards and `ToolCallItem`. Use `StatusDot` for the dot seat; if the card needs a done-check or stopped-square that StatusDot lacks, add a documented variant (class B). Keep `data-kind`, `delegate-status-word`, `subagent-status-glyph`, and the rule that `needs_attention` never becomes a status word. About 30 duplicated lines across three maps and two renderers.
- **Empty and unavailable states (P3-F02, High).** Five hand-rolled states (`jobTools.tsx:77-79`, `jobWatch.tsx:376-383`, `subagentModule.tsx:322-330,371-375`, `askUser.tsx:110-118`) sit beside `EmptyState` and `InlineNotice`. Use `EmptyState size="compact"` with a `reason` for the three list-empty cases and `InlineNotice tone="neutral" live="off"` for the two unavailable/malformed fallbacks. Keep the `data-reason` vocabulary (`no-activity` maps to `empty`; keep a wrapper if a test needs the old value) and the testids. About 25 lines.

**Tool-card adoptions (P3-F03–F10).** Delegate environment table → `PropertyList`; the only gap is `muted`, which is either dropped or added to `PropertyListRow` (class B). Available-tools pill → `Chip`; add `mono?: boolean` (class B) or keep the local inset ground with a recorded reason. `job_watch` rows → `Disclosure` (controlled `open`/`onOpenChange`), keeping the static non-expandable branch as a `div`, the testids, and the direct-child separator rule. `compact_context` raw arguments → `MCPToolArguments`; extract a local `ToolArgumentsBlock` only for the leftover section. Sandbox escalation's failure row → `InlineNotice tone="danger"`; keep the visible "Failed" text and decide `live` from whether the card already announces. Two meta/status rows → a local `ToolMeta` primitive with `segments`, `trailing`, and `className`; standardize the separators on `aria-hidden`. Tool card chrome → `Card variant="outlined"` plus a `padding="none"` prop (class B), or keep the local `.card` where the grouped-row body owns its own padding. `job_list` rows → `ListRow variant="plain"`, keeping the `job-list-row` testid and the separate total line. Card section labels → `Eyebrow`, deleting `subagentmodule.module.css`'s `.sectionLabel` (P3-F14).

**Y1-F03 adds the jobWatch card evidence.** `jobWatch.module.css:16-21` hand-builds `--surface-1` + `--edge` + `--radius-pane` + `overflow: hidden`, three of the four outlined-Card declarations, beside `Card variant="outlined"`. Render Card and move only the padding reset to `styles.card`; account for Card's 12px inset and its lift, or use `composes: grouped` if the padding fight is not worth it. This is the same card P3-F09 targets, so land it in that PR.

**P1/P4 now reported.** ToolRow's four disclosure variants, ToolRunGroup, and TurnFailureEndCap are P1-F01 through P1-F07 in Workstream 10; the flow and overlay surfaces are P4-F02 through P4-F04. Use those rows as the lane evidence and keep the final shape in the existing workstream when the recipe overlaps.

**Approval.** A for the local table and adoptions; B for the `PropertyListRow muted`, `Chip mono`, and `Card padding` extensions.

## Workstream 7 — Code, diff, and media presentation

**Verified from W3.** Four code-block renderers share one visual language but not one markup helper. `widgets/codeblock/index.tsx:106-163` owns the class table; `markdown/index.tsx:41-47,73-85` and `mermaid/index.tsx:25-31,87-99` each define their own `CODEBLOCK_CLASS` object and re-create the `div > pre > code` markup; `mermaid/DiagramViewer.tsx:133-136` hand-builds a fourth source block. Export one small markup helper from `codeblock` (a string renderer for Markdown's `dangerouslySetInnerHTML` path and a component for JSX) and use it in CodeBlock itself. Add a `wrap` option so DiagramViewer keeps `white-space: pre` and horizontal scroll while the others wrap. Keep Markdown's `escapeHtml` and Mermaid's sibling `<p class="error">` outside the helper. About 40 duplicated lines at four sites.

**DiffBlock copy affordance (W3-F16).** DiffBlock is the only code/diff block with no copy control (`diffblock/index.tsx:103-122`), while CodeBlock, ShellCommandBlock, and DiagramViewer all have one. Add the shared block header/chrome slot from the CodeBlock helper and render `CopyButton text={unified} label="Copy diff"`. Keep the diff's own horizontal scroller and expect a small vertical-rhythm change in the transcript. Approval A (existing CopyButton plus the shared header).

**DiffTable owner decision (W3-F17).** `widgets/difftable` is exported from the barrel but has no production caller; only the gallery imports it. Do not delete on the audit's evidence alone. Ask the owner whether the structured column diff is still planned. If not, remove the widget, test, gallery section, and barrel export in one change; if it is, mark it gallery-only. This is a product/ownership decision, not a duplication fix.

**Kept bespoke from W3.** VirtualList and Tree have different structures and keyboard models; do not merge them (F18). PaneScaffold is the only pane shell; do not fold Dialog into it (F19). Chevron, SendIcon, OpenButton, SteeringGlyph, FailureGlyph, SpeakerAvatar, and CopyButton are distinct marks inside ToolIcon's shared frame, not duplicates (F20). Dropzone and PromptCard already compose correctly; do not move file policy into Dropzone (F21). DiffBlock and DiffTable keep their own presentation; only the copy gap is real (F22).

**Verified from M1.** Three Dialog-large lightboxes size their image independently: `docpane.module.css:15-21`, `imagegallery.module.css:56-64`, and `composer.module.css:120-127`. Two use 80vh and one uses 85vh on the same Dialog-large panel (M1-F04). Add `widgets/internal/lightbox.module.css` with one `.image` rule at one agreed height and compose it in all three; Dialog already owns the panel geometry, so this is caller CSS, not a locked-widget change. Visual only; no test asserts image height.

**Verified from P5.** `AttachmentTile.tsx:81-106` and `ImageGallery.tsx:142-197` each build a thumbnail plus a `Dialog size="large"` lightbox; the frame/lightbox CSS is duplicated at `composer.module.css:88-141` and `imagegallery.module.css:8-64` (P5-F01, about 45 duplicated lines at two sites). The 2026-10-11 catalog already proposed an `ImageThumbnail` widget that owns the frame, the `ZoomableMedia` trigger, the `<img>`, and an optional lightbox; callers supply overlay, remove control, caption, and navigation. That is a new widget (approval C). Until it is approved, extract the duplicated frame/lightbox CSS. Keep AttachmentTile's single root element type across pending→settled, and its `data-testid`/layoutguard contracts. M1-F04's shared lightbox `.image` rule is the CSS half; P5-F01's widget is the structural half, so do the CSS first and decide the widget separately.

**ZoomableMedia radius.** `composer.module.css:188-194` styles `ZoomableMedia`'s button by tag to clip the touch tile (P5-F09). Give `ZoomableMedia` a `radius?: "control" | "inherit"` prop, or have `fill` imply `border-radius: inherit; overflow: hidden`, and drop the tag selector. One-prop extension, approval B. The desktop tile already clips at the tile; only the touch media block changes.

## Workstream 8 — Shared recipes, logic, tokens, and dead code

- **Field chrome and interaction recipes.** W1-F01 and W1-F03 through W1-F06 define the shared set: `field.module.css` (Workstream 1), `tapfloor.module.css` (8 hand-written copies), and `interaction.module.css` (hover wash in 8 widgets, focus ring in 14 stylesheets, disabled dim in 9). The focus-ring share needs a prototype check: CSS Modules cannot obviously compose a `:focus-visible` pseudo-class, so if the composition does not survive the build, keep the finding as documentation and do not force it. Add `--opacity-disabled: 0.5` to tokens for the dim, replacing the 0.55 outlier in `directorypicker.module.css`.
- **Visually hidden.** `VisuallyHidden` is barrel-exported but KeyHint and the `fieldTrigger`-based PathField/ModelCatalog hand-roll the recipe (W1-F07). Render the widget and delete the CSS copies; keep the hidden announcement text and the `aria-hidden` visible keys unchanged. `zoom.module.css:115-120` goes further and composes the widget's private stylesheet while cancelling its `clip`/`clip-path` (M1-F08); either give VisuallyHidden native rest-spread so `data-runtime-label` can sit on the hidden element (a locked-API EXTEND for Jesse), or drop the overrides and record the private-module compose as a known gap. `Zoom.test.tsx:1248` and `cascadeguard/run.mjs:816` query `[data-runtime-label]`, so the attribute must survive. W3-F14 finds the same local `.srOnly` in `fieldTrigger.module.css:72-84`, used by ModelCatalog `:488` and PathField `:621`; render `VisuallyHidden` and keep the load-bearing whitespace node a direct sibling, or the accessible name concatenates without its space.
- **Eyebrow.** The eyebrow recipe is re-derived in PropertyList and in `pathfield.module.css:120-128` and `modelCatalog.module.css:92-99` (W1-F08). Prefer a shared `src/styles/` typography recipe over rendering `<Eyebrow>` inside the `<dt>`; PropertyList's label color comes from inheritance, so a naive component swap changes it.
- **Fold headers.** Disclosure and DisclosureHeading share one fold-header surface but disagree on the chevron cue (W1-F09). Add `foldHeader.module.css` for the row/hover/inset-ring/tap-floor declarations, keep the summary-element semantics in Disclosure, and pick one chevron implementation; keep the deliberate `space-2` vs `space-4` padding as a variable.
- **Pills and close glyphs.** Badge and Chip share pill geometry but drift on metrics; share only the inline-flex/height/padding/radius base and keep tone maps separate (W1-F12). Three widgets each define a private 10px `RemoveIcon` (`chip/index.tsx:31`, `collectioneditor/index.tsx:83`, `AttachmentTile.tsx:52-58`) (P5-F08, W1-F13). `CloseIcon` is 14px on a 16 viewBox with stroke 1.5 (`dialog/CloseIcon.tsx:6-8`), so rendering it at 10px thins the stroke to about 0.94; do not swap it in unparameterized. Add one `RemoveIcon` parameterized by size/viewBox/stroke, or give `CloseIcon` a size prop that preserves the 1.5 stroke, and use it at all three 10px sites. The glyphs are `aria-hidden`; each host supplies the accessible name. W3-F12 adds PathField's local `FolderIcon`/`FileIcon` (`:144-170`) and CollectionEditor's `RemoveIcon` (`:80-86`); use `ToolIcon kind="folder"|"file"|"cross"`. Do not replace FailureGlyph's 10×10 danger cross; it is a status mark, not a close control.
- **Status family map.** Cadence owns the state→family map at `cadence/index.tsx:34-42` and its family colors at `cadence.module.css:44-62`; StatusDot copies both at `statusdot/index.tsx:18-31` and `statusdot.module.css:1-7,52-66`, and its own comment says the mapping is private (W3-F06). Export `STATE_FAMILY`/`stateFamily()` from Cadence and share the four family color declarations through a `statusFamily.module.css` or `composes`; keep `STATUS_LABEL` as the `STATE_LABELS` alias. Do not unify dot size or the attention transition timing; that divergence is documented. About 20 duplicated lines. Approval A.
- **Tool meta and caption recipes.** `compactcontext.module.css:12-21`, `delegateStatus.module.css:23-28,60-63,111-152`, `jobWatch.module.css:55-58,127-138`, `askuser.module.css:33-46`, `readtranscript.module.css:3-8`, and `subagentmodule.module.css:139-154` repeat caption, mono, and spring declarations (P3-F11, about 30 lines). Add `styles/meta.module.css` with `caption`, `mono`, and `spring`, and compose it. No token or visual change.
- **Transcript row reset and hover wash.** `jobWatch.module.css:70-110,118-125` and `toolcallitem.module.css:48-66,232-241` repeat the interactive row reset and the ink-mix hover wash (P3-F12, about 35 lines). Prefer the `Disclosure`/`ListRow` adoptions above; if the raw row stays, compose a `rowReset` from `styles/surface.module.css` and share the wash. Keep the ink-mix approach only if its comment's light-theme measurement still holds.
- **IconButton sizing.** IconButton re-declares Button's size ladder and imports Button's private classes (W1-F14). Express both ladders through control-size tokens (`--control-xs|sm|md`) or have IconButton compose Button's size classes; do not turn IconButton into a Button wrapper, because it needs square geometry.
- **Barrel and public names.** `STATE_LABELS` and `STATUS_LABEL` export one status table under two names (W1-F15): `STATUS_LABEL` is the alias at `widgets/statusdot/index.tsx:35`, `STATE_LABELS` is the definition at `widgets/cadence/index.tsx:26`, the barrel exports both at `widgets/index.ts:13,115`, and the only production consumer outside StatusDot is `RailRow.tsx:73,572`. `isWebHref` is exported but missing from the barrel, forcing a deep import (W1-F16). Removing the duplicate public name needs Jesse's approval and a deprecation note; adding `isWebHref` to the barrel (or moving it to the shared helper location) is mechanical.
- **Borderline.** EmptyState and InlineNotice duplicate their action-row layout; two sites is borderline, so batch it with the tap-floor/interaction share rather than opening its own migration (W1-F17). Loader's elapsed readout has only gallery and test callers; treat it as suspected-dead, do not delete it from a source-tree grep alone (W1-F18).
- **Row chrome.** Palette option rows re-derive ListRow's reset/hover/press (`commandpalette.module.css:96-131`, `listrow.module.css:6-22,46-66`) (S3-F07). Extract the neutral reset/hover/press into `widgets/internal/rowChrome.module.css` and compose it in both; keep the palette's `aria-selected` background local and keep ListRow's two-track grid. Do not merge the active-option state with `aria-current`.
- **Truncation.** One-line truncation repeats at `commandpalette.module.css:167-173,200-207`, `dockview-theme.css:148-152,161-165`, `listrow.module.css:109-116,129-137` (S3-F08), at `zoom.module.css:80-87,93-100` and `pluginRow.module.css:17-25` (M1-F03), and at `activitybar.module.css:134-141,143-149`, `scopeCrumbs.module.css:26-32`, and `StackHost.module.css:44-48` (S2-F09). Add `widgets/internal/truncate.module.css` with a `truncate` class and compose it; scope the unscoped `.dv-tabs-overflow-container` selectors to `.dockview-theme-evener` while doing it. Do not sweep the 60 stylesheets that use `text-overflow: ellipsis` in the first pass; that is a separate mechanical decision.
- **`[hidden]` cascade.** Three stylesheets re-assert `[hidden] { display: none }` around authored display rules: `composer.module.css:76-81`, `promptcard.module.css:66-71`, and `settings/sections/keybindings.module.css:51` (P5-F11, about 9 lines). Prefer one app-level rule, or a shared `hidden.module.css` composed by the three; check the three named components before using `!important`, and note that `PromptCard`'s `hidden` also sets `inert`.
- **Dialog body.** `sessionmenu.module.css:1-5` `.body` re-declares Dialog's body typography; drop it when 4.1 replaces the dialogs (S3-F10). If the sans face is genuinely needed, put it on the dialog body once.
- **Zoom bars.** `zoom.module.css:66-70` and `:74-78` are byte-identical footer-bar rules, and `:238-247` `.peekHead` is `.header` with different padding/ink/type (M1-F05). Add `.bar { composes: header; border-bottom: 0; border-top: 1px solid var(--edge); }`, use it for both footers, and make `.peekHead` compose `.header` and override only padding/color/font-size.
- **Pane title.** `StackHost.module.css:40-55` copies PaneScaffold's `.title` recipe (`panescaffold.module.css:33-48`) (S2-F10), and `dialog.module.css:41-52` repeats it (W3-F13). Export a `paneTitle` recipe under `styles/` (avoid a widget API change) and compose it in all three; keep the font family per consumer and `text-align: center` at the caller.
- **Sidebar recipe.** `.sidebar` and `.sidebarMobile` (`activitybar.module.css:5-16,18-30`) are one recipe with four overrides (S2-F11). Use a base class plus `composes`, with the mobile override declared after the source so the cascade is unchanged.
- **Icons.** `TreeDrawer.tsx:6-12` defines its own `SessionsIcon` beside `railIcons.tsx:19-23,47-54` (S2-F13). Move it into `railIcons.tsx` or a neutral `shell/icons` module and share the `ICON`/`STROKE` constants; keep `aria-hidden` and the 16px box that `TreeDrawer.test.tsx` queries.
- **Keep bespoke.** `ToastRegion` is the single Toast mount point; fix its stale comment only (S3-F14). The `:global(.dv-*)` dockview overrides are intentional; scope the unscoped selectors, do not componentize (S3-F15). The palette's option rows stay bespoke ARIA options; the Listbox change gives them shared row chrome but not ListRow's grid (S3-F16). P5-F14 keeps SkillEditor's ProseMirror field bespoke; P5-F15 keeps CurrentWork's task dot bespoke because StatusDot's working state would claim liveness the row does not report.
- Add a `sheetBody` / dock-clearance composes target in `styles/surface.module.css` for the three copies at `MobileSettingRows.module.css:83-85`, `modelswitch.module.css:85-90`, and `dev/gallery-sections/modelCatalog.module.css:14`. `hosts.module.css:51-55` has a different `.sheetBody`; use composes, not a class rename. Also stop passing `.sheetBody` as the `Sheet` footer's wrapper (`MobileSettingRows.tsx:317-326`, I1-F09): pass the Buttons directly (or a fragment) and put any phone dock clearance in the Sheet footer padding once.
- Delete dead CSS such as the seven unreferenced `pluginSelection.module.css` classes (I1-F11).
- Token cleanup: 12 declared-but-never-referenced tokens and 13 `var()` references to undeclared names. Treat the undefined list carefully: `--token`, `--space-`, `--font-size-`, and `--ansi-` look like parser artifacts from partial names; verify each before changing code.
- Import hygiene: 187 barrel imports, 178 deep imports, 137 internal imports. Most deep/internal imports are legitimate helper modules (`requireClass`, `disclosureStore`, mermaid state, markdown lexer, codeblock ANSI). Only true deep component imports are a cleanup target; do not bulk-rewrite 178 sites.

### 8.1 Shared shell logic

- **`useAsyncConfirm`.** SessionMenu's `confirm` (`:147-157`) and ForceStopDialog's `confirm` (`:13-25`) implement the same busy-lock, close-on-success, stay-open-on-rejection rule; ForceStopDialog adds the `if (busy) return` guard. One `shell/useAsyncConfirm.ts` returning `{busy, run}` removes the copy and gives both the guard (S3-F06). Keep ForceStopDialog's "Cancel disabled while busy, Escape does not close, retry after rejection" behavior.
- **Card primary action (P6-R03).** `AskDock.tsx:239-256` and `sandboxEscalation.tsx:87-91` both treat Enter + Meta/Ctrl as the card's primary action, but only AskDock guards IME composition (`isIMECompositionKeydown`) and calls `stopPropagation`. Sandbox's copy can therefore approve on an IME commit or let a global binding fire alongside it. Add one `isPrimaryActionKey`/`useCardPrimaryAction` helper beside `keybindings/dispatcher.ts`; both cards use it. This is an accessibility fix, not only a dedupe: add a test that an IME-confirm Enter does not approve, and keep Escape's deliberate non-denial.
- **Answer count and label (P6-R04).** `AskDock.tsx:151-153,163,193,321,434-438` and the store's private `answerFor` (`appwire-client/typescript/askDock.ts:151-156,211,241,493`) repeat the same predicate and the same `"N of M questions answered"` label. `askAnswers.ts` has no `isAnswered`/`answeredCount`/`answeredCountLabel`. Export them from `askAnswers.ts` and use them in the store and all five AskDock sites; keep the missing-answer default and the exact rendered text.
- **`refParam` everywhere.** `routing.ts:41` already exports the non-empty-string reader. Five production sites hand-roll it: `DockHost.tsx:696-697` (accepts `""`), `AppShell.tsx:233-236` (local `sessionRefOf`, accepts `""`), `PaneTab.tsx:28-32` (accepts `""`), `deletedSessionPanes.ts:36` (accepts `""`), and `needsYouActions.ts:59-64` (hand-rolls the non-empty check) (S3-F12). Switching rejects an empty ref at the four lenient sites; that is the correct behavior (an empty ref is not a session) and needs a test. `needsYouActions` keeps its pane-type branches and calls `refParam` in the session branch.
- **`sameParams`.** AppShell's `sameRouteParams` (`:189-191`) and DockHost's inline `JSON.stringify` comparison (`:672-679`) compare the same params bags. Move one `sameParams(a, b)` beside `routing.ts`'s param readers; keep JSON-string semantics, including key order (S3-F13).
- **Absolute date and clock.** `jobWatch.tsx:398-407` hand-formats "created Sep 6, 09:41" while `formatClockTime` (`appwire-client/typescript/displayFormat.ts:175-182`) and `Timestamp` (`widgets/timestamp/index.tsx:40-48`) already format the clock half (P3-F13). Reuse `formatClockTime` and add `formatMonthDay`/`formatAbsoluteDate` beside it; pin `en-US` so snapshots stay deterministic, and keep the `created` prefix the tests assert.
- **`registerLazyPane`.** Six `panes/*/index.tsx` files repeat the same five-line `registerPane` + `lazy()` registration, differing in id, title, loader, `parseParams`, and `singleton` (M1-F06). Add a `registerLazyPane({id, title, load, parseParams?, singleton?})` helper in `shell/paneRegistry`; each pane still supplies its own dynamic `import()`, so the self-import footgun stays avoided. Keep registration order and `parseParams` behavior identical, and keep `panes/doc/index.tsx`'s side-effect import.
- **`useDirectoryListing`.** `directorypicker/index.tsx:60-138` and `pathfield/index.tsx:119-131,290-418` copy the directory fetch state machine: entries, recents, error, loading, cancellation, and out-of-order protection (W3-F05). Extract `useDirectoryListing({ path, debounceMs, source })` returning `{ entries, recents, error, loading, reload, browse }`. The picker validates on submit; PathField debounces on type and filters by `childrenPrefix`; the hook exposes both modes rather than choosing one. Keep the current error text and the tests that assert row names and status rows.
- **Doc frame.** `DocPane.tsx:129-147`, `:204-213`, and `:214-219` repeat the same PaneScaffold title/frame around different bodies (M1-F07). Compute the title once and render one frame around a small switch, as `BoundDocument` already does; keep `data-reason="unavailable"` and the filename headings tests assert, and keep the Reload action optional because only `BoundDocument` has it.
- **`middleTruncate`.** `RepoLocation.tsx:60-112` and `ToolRow.tsx:342-397` each split a string for middle truncation (P5-F05, about 45 lines). Export a `middleTruncate(text, cut)` or `<MiddleTruncated>` helper and let ToolRow keep its entity-aware cut strategy; preserve the never-empty-one-side rule and the whitespace walk, and keep ToolRow's id snapping opt-in. The CSS classes stay where they are, so layout is unchanged.
- **Pending-attachment refusal.** Five copies of the "attachment still processing" guard and message at `Composer.tsx:877-881`, `:1135-1138`, `:1170-1173`, and `useComposerActions.ts:380-383`, `:417-420` (P5-F07). Add one `refusalReason()` on the attachment store or a helper beside `useAttachments`; keep the user-visible string byte-identical and call it at the same point in `handleFormSubmit`, because the pending check precedes the unbacked-image check.
- **Composer emptiness.** Four implementations at `Composer.tsx:941-943`, `:686-692`, `sourceState.ts:376-384`, and `recovery/recoveryDraft.ts:63-76`; `compositionIsEmpty` already exists and is imported by `sourceState` (P5-F12). Use it with ref-reading adapters where the live refs are needed. Decide explicitly whether `mentions` count as content, because that changes both Send and the fold. Memoize the render-time value so it does not allocate per keystroke.
- **Range and selection equality.** `sourceState.ts:202-210` (ordered), `SkillEditor.tsx:206-220` (order-insensitive sets), and `:259-276` (`JSON.stringify`) use three equality rules (P5-F13). Add one `sameImageRanges` and one `sameStrings` helper; keep the comment's staged-range exclusion, and avoid false differences because they rebuild the editor and destroy undo history.

### 8.2 Composer and spawn prompt-card chrome

- **Send label collapse.** `composer.module.css:210-232` pierces Button's DOM with `:global(button):has(> .submitLabel)` and a container query, and `spawn.module.css:39-48` copies it (P5-F02). Button has no label-collapse API. Either add `collapseLabel` (approval B) or a shared `sendButton.module.css` recipe. Keep the pane-width container query, not a viewport query; `narrowComposer`/overflowguard bracket 399/400px. The accessible name stays `aria-label="Send"`, so hiding the word is safe. About 18 duplicated lines.
- **Prompt-card leading and attachment strip.** `composer.module.css:33-43,52-56,70-74,228-232` and `spawn.module.css:49-59,377-381,42-47` copy the leading-row and attachment-strip geometry (P5-F04). PromptCard owns the card and slots but exposes no class hooks. Move the geometry into `promptcard.module.css` and expose slot elements, or share a `surface.module.css` recipe; spawn's single-line leading needs a `flex-wrap` opt-out. Keep the composer's 200px basis and both overflow/layout guards. About 25 duplicated lines. Approval B if the widget API changes; A if pure CSS composes.
- **PromptCard's composer-only rule (W3-F15).** `promptcard.module.css:74-81` contains a `:global([data-composer])` and `:has()` rule for the Duo cover. Move it to `composer.module.css`, scoped to the composer's own wrapper, or expose a documented `collapsed` variant if the card must own the padding. This is a relocation, not a behavior change; the phone Duo cover ruling stays exactly as it is. Approval A.

### 8.3 AskDock typography and selection recipes

- **Caption recipe (P6-R06).** `.anchor` and `.count` in `askdock.module.css:20-23,81-84` and `.why` and `.optionDetail` in `askquestioncard.module.css:40-43,112-115` repeat `font-size: var(--font-size-caption); color: var(--ink-mid)`. Add `.caption` to `styles/surface.module.css` and compose it from the four sites. The accent `optionRecommended` marker stays local unless a second accent caption appears. This is the same shared-home rule as the field and tap-floor recipes: extend an existing layer, do not add a new one.
- **Selection recipe (P6-R07).** The selected tab uses an `--accent` border (`askdock.module.css:64-68`), the checked option uses `--accent-edge` (`askquestioncard.module.css:84-87`), and the alternative row has no checked rule at all. Pick one recipe, probably `--accent-edge` plus `--accent-bg` and `--ink-hi`, and apply it to all three. If the tab needs the stronger border, record the reason rather than leaving the drift.
- **Undefined tab font (P6-R11).** `askdock.module.css:51` uses `font-family: var(--ask-tab-font, revert)`, and no `--ask-tab-font` is declared anywhere. Replace it with `var(--font-sans)` or delete the declaration; the current fallback rolls the tab back to the user-agent font.

**Approval.** A for the caption and selection recipes; no new widget or API. The visual change needs a layout-guard pass at 320px and XL text because the option row's inline flow and the tab strip's wrap both depend on it.

### 8.4 Settings-surface helpers, recipes, and dead code

- **Write-failure toast helper (G3-F01, High).** Eight catch sites repeat `isHubWriteBusy(err) ? HUB_WRITE_BUSY : \`<label>: ${errorText(err)}\``: `MarketplacesSection.tsx:201,251`, `MarketplaceDetailBody.tsx:202,239`, and `PluginDetailBody.tsx:114,123,133,146`. Add `writeFailureText(err, label)` beside `isHubWriteBusy`/`HUB_WRITE_BUSY` in `@evener/appwire-client/state/extensions`, or keep it local if the package export is not wanted. Strings must stay byte-identical; tests assert toast text. Class B if the helper becomes a package export.
- **Flex-stack recipes (G3-F03, Medium).** `.section`, `.list`, `.tree`, `.page`, and `.addForm` re-declare the same column-flex and list-reset recipes across `collectionFields.module.css`, `LaunchConfigForm.module.css`, `marketplacesPlugins.module.css`, `mcp.module.css`, `dirListSetting.module.css`, and `collectioneditor.module.css`. Add `styles/stack.module.css` with gap variants and a list reset, compose it, and delete the dead `.tree`/`.treeChildren` copies with G3-F08. Class A.
- **Plugin and marketplace match predicates (G3-F05, Medium).** Three plugin predicates search different fields (`MarketplacesSection.tsx:49-52` name+description; `InstalledSection.tsx:41-47` plugin+marketplace; `MarketplaceDetailBody.tsx:248-256` name+description), and marketplace rows filter by name at `MarketplacesSection.tsx:154,220`. Add local `pluginMatchesQuery`/`marketplaceMatchesQuery` helpers and decide the searchable fields before changing results; if both conventions are intentional, pass a mode. Class A.
- **Dead settings CSS and stale headers (G3-F08, Medium).** 14 unused declarations in `marketplacesPlugins.module.css` and `devicePairing.module.css`, the unused `rowMeta` entry in `MarketplaceDetailBody.tsx:54`, and stale comments naming the retired browse/sheet components. Delete CSS and `CLASS` entries together; `requireClass` throws when a key is missing. Class A.
- **Device-pairing tag selector (G3-F09, Medium).** `devicePairing.module.css:8-10` aligns the Button through `.root > button`; replace it with a layout wrapper or an actions-row alignment, and delete the dead `.soundOption > p` rule. Class A.
- **Detail Escape helper (G3-F10, Low).** `PluginDetail.tsx:47-57` and `MarketplaceDetailBody.tsx:258-267` repeat the same `key === "Escape" && !defaultPrevented` / `preventDefault` / `onCollapse` handler. Add `handleDetailEscape(event, onCollapse)` beside `handleRowEscape` in `pluginRowFocus.ts`; keep `preventDefault`, not `stopPropagation`. Class A.
- **Draft-state updater (G3-F11, Low).** Five `setState` updaters in `LaunchConfigForm.tsx:233-247` differ only by state section. Replace them with one generic `updateSection(key, wireField, value)` and one-line wrappers. Class A.
- **Path-kind mapper (G3-F12, Low).** `collectionFields.tsx:131-134` falls back to `"dir"`; `fields.tsx:76-84` falls back to text. Export one map from `fields.tsx` and make the collection call site's `?? "dir"` explicit, so the deliberate difference is named once. Class A.
- **Launch-config pane adapter (G1-F03, Medium).** `project.tsx:73-79` and `launchServer.tsx:57-63` copy the identical `LaunchFormPaths` literal; both build the same `Promise.all([schema, getLayer, resolve])` load, and both render the same refresh notice. Export `launchFormPaths(store)` and the `LoadState` union from `launchShared/`; keep the two load bodies because project also fetches `global` and launchServer seeds diagnostics. Class A.
- **Section headings (G1-F04, Medium).** Four heading recipes (`mcp`, `dirListSetting`, `hubUpdates`, `hubResidents`) and seven bare `<h2>`/`<h3>` sites use three sizes. Add `settingsSectionHeading` to `surface.module.css` with pane-title/body modifiers and apply it to all eleven sites; do not claim a heading-order violation, only the internal size/markup drift. Class A.
- **Quiet status line (G1-F05, Medium).** `keybindings.tsx:627-631`, `transcript.tsx:144-148`, `TranscriptDisplayCard.tsx:124-127`, and `inrepo.tsx:93-98` use three different `role`/`aria-live` combinations; `about.tsx:73-77` renders an error as a plain paragraph. Add a local `SectionStatus` and convert `about` to `InlineNotice tone="danger" live="status"`. Keep every `data-reason`. Class A.
- **Warnings box (G1-F06, Low).** `keybindings.tsx:660-675` hand-builds the `InlineNotice title="Warnings"` list that `diagnostics.tsx:15-22` already renders. Reuse `Diagnostics`, or widen it to `{ message, reason }[]` and pass a title; the per-item `data-reason` must survive. Class B only if the widget is widened.
- **Section container and stacks (G1-F07, Medium).** 21 rules restate `gap: var(--space-4)`, `max-width: 720px`, and gap-3/2/1 stacks across the settings modules. Merge these with G3-F03's stack module: one `sectionRoot` with a narrow/measure variant plus `stack`/`stackTight`. Class A.
- **`mobile.tsx` borrows `devicePairing`'s stylesheet (G1-F08, Medium).** `mobile.tsx:8` imports `devicePairing/devicePairing.module.css` and `requireClass`es four of its classes. Give it its own layout classes, or compose G1-F07's container; preserve the `.root > h3, .root > p { margin: 0 }` reset. Class A.
- **Inline `Code` bypass (G1-F09, Low).** Raw `<code>` at `inrepo.tsx:91`, `:133`, `project.tsx:158`, `:172` should use the existing `Code` helper. Class A.
- **Settings poll hook (G1-F11, Low).** `hosts.tsx:222-223`, `:231-249`, `hubResidents.tsx:143-144`, and `HostPicker.tsx:40` repeat `usePaneDocument()` + `useVisibleInterval(...)`. Add a settings-local `useSettingsPoll(intervalMs, tick)`. Class A.
- **Remove-failure toast (G1-F12, Low).** `mcp.tsx:196`, `:228`, and `dirListSetting.tsx:232` repeat the same `Remove failed:` toast. Share it with G3-F01's `writeFailureText`. Class A/B with that helper.
- **Chord KeyHints (G1-F13, Low).** `keybindings.tsx:320`, `:473-476`, `:531-533` repeat the `binding.chord.map(...)` render. Add a local `ChordHints({ presses })`. Class A.
- **Internal hook import (G1-F14, Low).** `keybindings.tsx:49` imports `widgets/internal/useDismissOnOutsidePress`; promote it to the barrel or a public hooks entry. This is import hygiene, not an API redesign. Class A.

### 8.5 Session-chrome CSS and control recipes

- **Trigger wrapper (P7-F08, Low).** `ModelSwitchTrigger.tsx:309` composes Popover's private `.trigger` and keeps a `<span>` shim solely for the status row's `:global(button)` selectors. Let `PickerTrigger` (2.4) own the one-span shape on both sides of the breakpoint; delete `.triggerWrapper`. Class C with PickerTrigger.
- **Inset focus ring (P7-F09, Low).** Four in-lane sites (`sessionchrome.module.css:83-86`, `topnotespanel.module.css:62-66`, `modelswitch.module.css:44-47`, `statusrow.module.css:192-195`) and three outside repeat `outline: var(--focus-ring); outline-offset: -2px`. Add a `focusRingInset` recipe or a `--focus-ring-inset` token; rerun layoutguard. Class A.
- **Textarea width (P7-F10, Low, class B).** `notespanel.module.css:36-44` reaches into `.editor textarea` to set width because `Textarea` has no fill. Add a `fullWidth` prop or make it fill by default, then delete the wrapper rules; verify the spawn textarea. Class B.
- **Truncation recipe (P7-F11, Low).** Six spots repeat the ellipsis block: `modelswitch.module.css:29-33`, `statusrow.module.css:160-164`, and four in `taskspanel.module.css:86-94,108-115,129-133,206-213`. Add a shared `truncate` class and use it in `fieldTrigger`'s `.value` too. Class A.
- **Tabular figures (P7-F12, Low).** Six chrome modules re-declare `font-variant-numeric: tabular-nums` plus the face and caption size. Add a shared `.figure` recipe or `--numerals` token. Class A.
- **Intentional bespoke (P7-F13, no change).** The activity timeline, NotesPanel's visible-URL link rows (anti-phishing), the three-line clamped preview, and `ActivityTime`'s `data-activity-time`/`data-meaning` contract stay bespoke. Keep them in the intentionally-bespoke section.

### 8.6 Absolute time formatting

G3-F14 and P7-F04 find local absolute formatters at `MarketplaceDetailBody.tsx:57-59`, `LaunchConfigForm.tsx:105-107`, `detailsAccounting.ts:19`, and `TasksPanel.tsx:213-225,291-297`. `Timestamp` renders a relative primary label only. Add an opt-in absolute display or a shared `formatAbsoluteTime` helper, and keep `ActivityTime`'s `<time data-activity-time data-meaning>` semantics. Do not unify the sidebar task row with the panel task row until the `task-row-time` test split (`TasksPanel.test.tsx:441-443`) is decided; that is a product call. Class B for the Timestamp extension, D for the task-row split.

### 8.7 App-wide style recipes and tokens

- **Truncation recipe (Y1-F01, High; P7-F11).** 60 `text-overflow: ellipsis` declarations across 41 stylesheets repeat the same trio/quartet; the highest counts are `modelCatalog.module.css` (4), `taskspanel.module.css` (4), `pathfield.module.css` (3), and `RailRow.module.css` (3). Add `truncate` (`overflow: hidden; text-overflow: ellipsis; white-space: nowrap`) and an optional `truncateFlex` carrying `min-width: 0` to `surface.module.css`; keep conditional/hover/container-query truncation local. Add a contract case in the style of `surface.contract.test.ts`. Class A.
- **Visually-hidden recipe (Y1-F02, Medium).** `widgets/visuallyhidden/visuallyhidden.module.css:1-9`, `widgets/internal/fieldTrigger.module.css:72-82`, and `widgets/keyhint/keyhint.module.css:50-60` are three copies. Make KeyHint compose the shared recipe (or render `VisuallyHidden`), and make `visuallyhidden.module.css` compose one declaration set; pick the `clip-path` form and keep `margin: -1px`. Class A.
- **Eyebrow copies (Y1-F04, Medium).** `ConnectionFacts.module.css:21-27` and `currentwork.module.css:61-63` restate the eyebrow recipe. Render `Eyebrow` for the `<span>`/`<dt>` cases; the `<label htmlFor>` case needs `Eyebrow` inside the label, or an `as`/`className` extension (class B). Three widget-internal restatements (`modelCatalog`, `pathfield`, `propertylist`) should compose the canonical recipe too.
- **Route guards (Y1-F05, Low).** `App.tsx:42-81` repeats six identical `if (route) return <Suspense fallback={null}>…` blocks. Extract a local `DevRoute`/route table; keep the lazy imports and the `window.location.pathname` checks. Class A.
- **Type and spacing tokens (Y1-F06, Low).** `currentwork.module.css:26`, `delegateStatus.module.css:27`, and `subagentmodule.module.css:120` hardcode 600/500; `jobWatch.module.css:31,51` hardcode `line-height: 1.6`. Use `--font-weight-semibold`/`--font-weight-medium` and `--line-height-body`; keep the measured `9px` inset local. Class A.
- **Row dividers (Y1-F07, Low).** `jobWatch.module.css:56-59` can compose `groupedRows`; `delegateStatus.module.css:74-93` wraps its sections and should keep the local border with a comment. Class A.
- **Dead tokens (Y1-F08, Low).** Delete `--tooltip-muted` (`tokens.css:91,355`) and the `--ask-tab-font` hook (`askdock.module.css:51`); no consumer reads either. Class A.
- **Runtime-token allowlist (Y1-F09, Low).** Add `--menu-inline-pad` and `--menu-row-inline-pad` (`menu.module.css:105,120,191`; set in `menu/index.tsx:1273,1276`) to `UNDECLARED_BY_DESIGN` in `token-contract.test.ts:1349-1362`. Documentation only. Class A.
- **Zoom clip override (Y1-F10, Low).** `zoom.module.css:116-119` composes `visuallyHidden` then overrides `clip-path`; drop the override or report a needed variant. Class A.
- **Motion stays bespoke (Y1-K01/K02).** Respond/Press are property lists, not classes, and the enter/exit keyframe pairs are pinned by the motion contract; do not consolidate them.

## Workstream 9 — Dev galleries

D1 audited all 133 files / 13,712 lines. None of this ships; the payoff is a gallery that stays consistent with production and is cheaper to extend.

- **Gallery section and state-row helper (D1-F01, High).** 63 section modules render their own `<section><h2>` shell, 71 files in `gallery-sections/` import `ThemeFlip`, and 21 already import the shared `dev/gallery-section.module.css`. Add `dev/GallerySection.tsx` exporting `GallerySection({ title, description?, children })` and an optional `StateRow({ label, wide?, children })` that composes the existing `.row`/`.rowLabel`. Each section file keeps its default export, because `WidgetGallery.test.tsx` discovers sections by file and `import.meta.glob` requires a module default. About 500 lines at 63 sites. Dev-only, class A.
- **Section discovery helper (D1-F02, Medium).** `WidgetGallery.tsx:14` and `SurfaceGallery.tsx:21` copy the `import.meta.glob` discovery block. Extract `discoverSections(modules)`. Each gallery keeps its literal glob at module scope, since Vite requires that. Preserve the `SURFACE_GALLERY_SECTIONS` export. About 15 lines.
- **Harness mount helper (D1-F03, Medium).** 11 `*-entry.tsx` files repeat `getElementById` → throw → `createRoot().render` and the two style imports. Add `dev/mountHarness.tsx` and `dev/harnessStyles.ts`. `overflowharness-entry.tsx` mounts three trees, so it calls the helper three times. The helper takes the harness name for the throw. About 55 lines.
- **Gallery frame CSS (D1-F04, Medium).** Four modules declare `.demoFrame`; `modelCatalog.module.css:5` already composes from `sheet.module.css:11`. Promote the block and `.reopen` into `dev/gallery-sections/gallery-frame.module.css` and compose all five. Keep the 390px overflow derivation comment. About 45 lines, 28 of them comments.
- **Caption recipes (D1-F05/F06, Medium/Low).** Five modules re-declare `.rowLabel` beside the canonical `dev/gallery-section.module.css:19`; six re-declare `.hint`, while `.note` at `:43` is a fixture caption, not an instruction line. Compose `.rowLabel`, and add a `.caption` export beside `.note`. About 54 lines. The shared file is `dev/gallery-section.module.css`, not a file under `gallery-sections/`; D1's digest cites the wrong directory for the canonical copy.
- **Fixture thread (D1-F07, Medium).** `surface-sections/chrome.tsx:21` and `composer.tsx:29` duplicate `FULL_CAPABILITIES` and the thread fixture. Add `dev/surface-sections/thread-fixture.ts`, following the existing `transcript-fixture.ts`. About 35 lines.
- **Raw gallery triggers (D1-F09/F11, Medium/Low).** `gallery-sections/modelCatalog.tsx:36` uses a raw `<button>` where its three sibling overlay galleries use `Button`; `popover.tsx:26` and `hovercard.tsx:30` also use raw triggers. Use `<Button variant="quiet" size="sm">`. Keep `styles.reopen` as a wrapper class because it carries the absolute positioning. Button does not forward rest props, so Tooltip cannot wrap a Button yet; that is a separate class-B extension with one call site (`tooltip.tsx:16-21`), and it needs Jesse.
- **Leave the plugin mockup duplication (D1-F08).** `PluginsMockups.tsx:370/385/592` re-types the capability/status vocabulary and composes private sheet/dialog classes for headless screenshots. The mockup shows an unbuilt design and has no production `PluginView` type; importing `pluginStatusChips` would drag `PluginEntry` into it. Record it and revisit when a production view model exists.
- **Row flex cleanup (D1-F10, Low).** 14 modules re-declare the `.row` flex recipe. Do this after F01, because `StateRow` will delete several of the uses.

Test impact: `WidgetGallery.test.tsx:46-56` checks only name parity, so no test catches a missing widget state. Section discovery depends on a default export per file, so the helper must not change the per-file module shape. The existing gallery tests and the frontend gate stay green; no production behavior test changes.

## Workstream 10 — Late-evidence transcript, rail, and session findings

Seven lanes stopped before delivering their reports. Their transcripts were recovered after the first plan was assembled, and every cited line was re-opened on base `347036b94b` during recovery. This workstream is the change list for those lanes. Each row names the primary site, the approval class, and the change; the lane report carries the full evidence, migration risk, and size estimate. Cross-references point at the workstream that already owns the same recipe.

Several findings join work already planned: P2-F04/F05 are rail heading recipes beside Workstream 3; P4-F02/F04, P8-F01, and S1-F10 are measure and overlay recipes beside Workstream 8; P4-F05, S1-F16/F17/F18, and W2-F06 are widget API extensions beside Workstreams 1 and 3; W2-F01/F02/F09 are surface recipes beside Workstream 8. Do not land the same recipe twice. Sequence the existing workstream first and fold the late site into it, or land the late site with the existing recipe in one PR.

### 10.1 P1 — session transcript root

Coverage: 28/28 files, 5,491 lines. Eleven change findings (2 High, 4 Medium, 5 Low). Four keep-bespoke notes (P1-K01 through P1-K04) are recorded but are not changes.

| ID | Sev | Class | Site | Change |
|---|---|---|---|---|
| P1-F01 | High | A | `transcript/ToolRow.tsx:756-758, 942-955, 979-1041` | Hoist local `RowLead` and `RowTrigger`; five row leads and four disclosure buttons call them. |
| P1-F02 | High | A | `transcript/toolcallitem.module.css:86-91, 205-211, 485-490` | Extract `.overlay` and `.resetButton` recipes into a local shared class list; compose the full-line overlay, button reset, hover/focus, and pointer-events copies. |
| P1-F03 | Medium | A | `transcript/ToolRow.tsx:765-794, 835-907` | One `SummaryText` local component for the two character-identical summary spans. |
| P1-F04 | Medium | C | `transcript/ToolRunGroup.tsx:59-94` | Extend `Disclosure` with `leading` and `summaryClassName`; replace the hand-rolled details/summary and disclosureStore calls. |
| P1-F05 | Medium | A | `transcript/TranscriptDetailControl.tsx:103-111, 182-190` | Use two `InlineNotice` calls, neutral status and danger warning, preserving the asserted `data-kind` values. |
| P1-F06 | Medium | A | `transcript/EntityRef.tsx:81-124, 126-187, 208-240` | One local card shell for JobCard, DelegateCard, and WatchCard; pass kind, status, captions, summary, and rows. Keep the status divergence as data. |
| P1-F07 | Low | C | `widgets/disclosure/disclosure.module.css:117-119, 135-139` | Extend `Chevron` with `open`; own the rotation and reduced-motion transition, then delete the transcript copies. |
| P1-F08 | Low | A | `transcript/TranscriptBody.tsx:454-455, 470-471, 582` | One `TranscriptRow` and one `TranscriptColumn` local component. |
| P1-F09 | Low | A | `transcript/RawToolOutput.tsx:22-26`, `transcript/MCPToolArguments.tsx:9-13` | One local `jsonCodeBlock(text, { copyLabel, fold })` helper. |
| P1-F10 | Low | B | `transcript/ToolCallItem.tsx:56-59`, `transcript/messages/reasoningFormat.ts:195-197` | One `clipCodePoints` helper; agree its placement with the messages lane before moving the second call site. |
| P1-F11 | Low | A | `transcript/turnfailure.module.css:29-35` | Delete dead `.hint`; the Disclosure body owns the full-width layout. |

Keep bespoke: P1-K01 ToolRow nested links, P1-K02 SelectionQuote motion, P1-K03 mono error output, P1-K04 turn-close captions.

### 10.2 P2 — transcript messages

Coverage: 24/24 files, 4,925 lines. Sixteen findings (3 High, 5 Medium, 8 Low), including two keep-bespoke entries.

| ID | Sev | Class | Site | Change |
|---|---|---|---|---|
| P2-F01 | High | A | `transcript/messages/PendingRowStack.tsx:611-625` | Local `PendingRow` plus a `pendingItem()` view model; both row branches render it. |
| P2-F02 | High | A | `transcript/messages/AgentMessageItem.tsx:66-79` | Local `SpeakerHeader({ name, meta, actions })` plus a module; both message items use it and keep their test ids. |
| P2-F03 | High | B | `transcript/messages/ThinkBlock.tsx:470-475` | Add `useTranscriptDisclosure({ scope, id, defaultOpen })` returning the disclosure key and fallback; replace the copied store calls. |
| P2-F04 | Medium | B | `transcript/messages/steeringitem.module.css:14-19` | Add `.summaryGlyph` and `.glyphBox` to `railseat.module.css`; compose them in the three summary modules. |
| P2-F05 | Medium | B | `transcript/messages/steeringitem.module.css:21-30` | Add `railBody` to `styles/surface.module.css`; compose all three and choose one spacing token. |
| P2-F06 | Medium | A | `transcript/messages/NotificationCard.tsx:506-533` | Local `NotificationHead`; the static branch wraps it, the disclosure branch passes it as the summary. |
| P2-F07 | Medium | A | `transcript/messages/NotificationCard.tsx:602-608` | Use `Disclosure variant="quiet"` for the memory source and raw payload, with distinct ids and `defaultOpen={false}`. |
| P2-F08 | Medium | A | `transcript/messages/NotificationCard.tsx:47-49` | Import `FailureGlyph` and the disclosure helpers from the widgets barrel; add the missing barrel exports. |
| P2-F09 | Low | C | `transcript/messages/memorycontextitem.module.css:26-34` | Extend `Chip` with a solid or quiet variant and className/data-* forwarding; use it here. |
| P2-F10 | Low | A | `transcript/messages/PendingRowStack.tsx:583` | Local constants or an `ActionGlyph` for the repeated glyph and `aria-hidden`. |
| P2-F11 | Low | B | `transcript/messages/agentmessageitem.module.css:120-124` | Add a shared `textSecondary` or `caption` class; compose the six sites and keep local margins out of it. |
| P2-F12 | Low | A | `transcript/messages/SystemNoticeItem.tsx:105-115` | Parameterize `NoticeLine({ text, fallback })` and call it from both branches. |
| P2-F13 | Low | A | `transcript/messages/PendingRowStack.tsx:61` | Use `useSessionNow()` and drop the direct `SessionNowContext`/`useContext` imports. |
| P2-F14 | Low | B | `transcript/messages/notificationcard.module.css:270-281` | Add a shared `verbatim` recipe with a `wrap` option; compose the raw notification text. |
| P2-F15 | Low | keep | `transcript/messages/NotificationCard.tsx:169-175` | Keep the local `Field`; the catalog row was wrong. |
| P2-F16 | Low | keep | `transcript/messages/HeldSteerAnnouncements.tsx:1-11` | Keep both live regions; extracting them would couple two different announcements. |

### 10.3 P4 — pending, find, and job-log surfaces

Coverage: 21/21 files, 2,345 lines. Eleven recorded entries (1 High, 4 Medium, 6 Low), including one keep-bespoke entry.

| ID | Sev | Class | Site | Change |
|---|---|---|---|---|
| P4-F01 | High | A | `panes/session/pending/PendingMessageStack.tsx:19-171` | Delete the dead stack, its module, its test, the `usePendingMessageActions` hook, `displayCopyId`, and `actionContext.ts`; keep `pendingRowActionOwner`. Check `PendingRowStack` coverage before deleting the test and remove the dead `status` prop if no caller remains. |
| P4-F02 | Medium | A | `panes/session/transcript/flow/flowoverlay.module.css:22-41` | Add a shared `overlayDock` recipe module with `.dock` and `.dock > *`; compose it here and in `session.module.css:21-36`. |
| P4-F03 | Medium | C | `panes/session/transcript/flow/ImageGallery.tsx:136-169` | Add `ImageThumbnail` plus an optional `MediaLightbox`, composing `ZoomableMedia` and `Dialog`; migrate the composer `AttachmentTile`. |
| P4-F04 | Medium | A | `panes/session/find/findbar.module.css:4-14` | Add a `measure` recipe for width, max-width, and margin-inline; compose the seven sites and keep each site's flex, gap, and padding. |
| P4-F05 | Medium | B | `panes/session/find/FindBar.tsx:40-48` | Extend `Input` with `ref` and native rest props; remove the `querySelector("input")`. |
| P4-F06 | Low | B | `panes/transcript/JobLog.tsx:19`, `transcript/messages/NotificationCard.tsx:47` | Re-export `AnsiLineContent` from `widgets/codeblock` and the widgets barrel; import it by name in both panes. |
| P4-F07 | Low | A | `panes/transcript/transcript.module.css:16-23` | Delete dead `.list` and `.scope`; rewrite the header comment for the job-log surface. |
| P4-F08 | Low | A | `panes/transcript/transcript.module.css:8-14` | Add a shared `fillColumn` or `scrollChild` recipe; do not merge the distinct scroll owners. |
| P4-F09 | Low | A | `panes/transcript/JobLog.tsx:374-378` | Use `formatByteCount` or extend it with a compact/grouped option; pick one byte format. |
| P4-F10 | Low | A | `panes/sessionPanels/SessionPanelPane.tsx:34-40` | Optional local `PaneLoading`; the lane leans no action because each site is four lines. Decide in implementation. |
| P4-K01 | Low | keep | `transcript/flow/NewContentPill.tsx:90-110` | Keep the flow pill family; the three pills have different jobs. |

### 10.4 P8 — session and conversation surfaces

Coverage: 8/8 files, 1,942 lines. Ten change findings (2 High, 3 Medium, 5 Low). One keep-bespoke note (P8-K01) is recorded but is not a change.

| ID | Sev | Class | Site | Change |
|---|---|---|---|---|
| P8-F01 | High | A | `styles/tokens.css:426` | Add a `measureColumn` recipe to `styles/surface.module.css` carrying width 100% and max-width `var(--session-measure)`; compose the four column sites. |
| P8-F02 | High | C | `panes/session/NextNeedsYou.tsx:72` | Extract `useLongPress({ onLongPress, slopPx, enabled, cancelOnVisibilityHidden })` with handlers and cleanup; use it here and in HeldSteer. |
| P8-F03 | Medium | A | `widgets/inlinenotice/index.tsx:54-71` | Render a danger `InlineNotice` with `live="alert"`, `data-reason`, and the owning-session action; replace the bespoke session error banner. |
| P8-F04 | Medium | A | `panes/session/Session.tsx:71-84` | Use `errorText` or `sessionActionError` and one `stopAndRefreshSession` helper; remove the three inline ternaries. |
| P8-F05 | Medium | A | `panes/session/ConversationBody.tsx:138-142` | Export shared conversation faces, including `ConversationDeletedFace`, and use them across the deleted and error surfaces. Coordinate the Zoom side. |
| P8-F06 | Low | A | `panes/session/session.module.css:154-161` | Add `bodyHint` and optional `centeredStatusColumn` recipes to `styles/surface.module.css`; compose them. |
| P8-F07 | Low | A | `panes/session/Session.tsx:200-207` | Build one `scaffold` object and spread it into the four PaneScaffold branches. |
| P8-F08 | Low | A | `widgets/statusdot/index.tsx:8-15` | Render `StatusDot state="needs-you" decorative`; propose a size variant only if the 6px geometry is required. |
| P8-F09 | Low | A | `panes/session/Session.tsx:54` | Add a local `LiveStatus` rendering `role="status"`, `aria-live="polite"`, and `VisuallyHidden` when the text is visual-only. |
| P8-F10 | Low | A | `panes/session/ConversationBody.tsx:757-759` | One `degradedRemediation({ interrupted })` sentence helper shared by both strings. |

Keep bespoke: P8-K01 GoalGlyph.

### 10.5 W2 — overlay and floating-layer widgets

Coverage: 21/21 files, 4,706 lines. Fourteen findings (3 High, 5 Medium, 6 Low).

| ID | Sev | Class | Site | Change |
|---|---|---|---|---|
| W2-F01 | High | A | `widgets/popover/popover.module.css:16-23` | Add a `floatingPanel` recipe to `styles/surface.module.css` composing `float`, fixed positioning, pane radius, and overlay shadow; use it where Popover, Menu, HoverCard, and the Dialog panel share the role. |
| W2-F02 | High | A | `widgets/menu/menu.module.css:8-25` | Make each selected Menu class compose the real button recipe (`button md secondary`); keep the runtime class selection. |
| W2-F03 | High | B | `shell/rail/usePinSectionDialogs.tsx:134-143` | Extend `ConfirmDialog` with `confirmDisabled` and `pendingLabel`; the form dialogs render it instead of their own footer. |
| W2-F04 | Medium | A | `shell/rail/PinSectionPicker.tsx:171-182` | Add a local `ResponsiveModal({ open, onClose, title, footer, children })` that chooses Dialog or Sheet with `useIsMobile`; use it here and in TranscriptDetailControl. |
| W2-F05 | Medium | A | `widgets/dialog/dialog.module.css:79-104` | Render the close button as `IconButton` with `label="Close"`, `variant="quiet"`, and `size="xs"`; delete the copied button rules. |
| W2-F06 | Medium | B | `widgets/directorypicker/index.tsx:5` | Extend `Dialog` with `panelClassName`, `bodyClassName`, and a float-or-none motion option; keep DirectoryPicker's scroll ownership. |
| W2-F07 | Medium | A | `widgets/popover/index.tsx:131-133` | Move `visibleViewportBounds` and Menu's trigger translation into `widgets/internal/visibleViewport.ts`; use it in both measure paths. |
| W2-F08 | Low | D | `widgets/dialog/OverlayPanel.tsx:139-185` | Add `useScrollLock(open)` to OverlayPanel only if Jesse wants the behavior; one hook applies to Dialog, Sheet, and ConfirmDialog. |
| W2-F09 | Medium | A | `widgets/toast/toast.module.css:38-72` | Add `styles/tone.module.css` with alive, danger, attention, and accent classes that set the three tone custom properties; compose Toast, Banner, and InlineNotice. |
| W2-F10 | Low | D | `widgets/hovercard/hovercard.module.css:5-7` | Resolve the surface role: move Popover and HoverCard to `--surface-2`, or record a ground-versus-raised rule and fix the comment. |
| W2-F11 | Low | A | `widgets/sheet/sheet.module.css:11-26` | Share the left/right declarations; keep the side-specific border side and shadow. |
| W2-F12 | Low | A | `widgets/popover/index.tsx:193-201` | Add `useEscapeToClose(onClose, { stopPropagation: true })` in `widgets/internal`; use it in Popover and Menu, leave OverlayPanel alone. |
| W2-F13 | Low | A | `widgets/dialog/OverlayPanel.tsx:84-89` | Add `useShownWhileClosing(open, value)` beside `usePresence`; use it in Dialog and Sheet. |
| W2-F14 | Low | A | `widgets/sheet/sheet.module.css:112-117` | Delete the duplicate `background` on `.expandableHeader`; keep sticky positioning, top, and z-index. |

### 10.6 S1 — shell rail and navigation

Coverage: 15/15 files, 3,923 lines. Nineteen recorded entries (3 High, 4 Medium, 11 Low), including one keep-bespoke entry with no severity.

| ID | Sev | Class | Site | Change |
|---|---|---|---|---|
| S1-F01 | High | A | `shell/rail/Rail.tsx:700-716` | Extend `runAction` with `onSuccess`/`onConverge`, or extract a local `runOptimisticMutation`; route the three optimistic sites through it. |
| S1-F02 | High | A | `shell/rail/RailRow.tsx:800-810` | Extract a local `RailRowShell` and rebuild the four rail row kinds on it; keep activation on the element each row uses today or move all four together. |
| S1-F03 | High | A | `shell/rail/index.tsx:73-86` | Add a shared `ChunkFailureBoundary` plus `useChunkRetry` or a `lazyChunk` helper; parameterize the stale predicate, copy, and fallback. Use it in `DockRegion` and `ConnectProviderDialogBoundary`. |
| S1-F04 | Medium | A | `shell/rail/Rail.tsx:302` | Add a local `RailSectionShell({ label, action, nodes, renderRow, open, onToggleOpen, renderWhenEmpty })`; keep Pinned's node assembly in a thin wrapper. |
| S1-F05 | Medium | A | `shell/rail/sessionKind.ts:11-15` | Export `NESTED_KINDS` and `isNestedSessionKind`; use them in `railPending.ts`. Coordinate the SessionChrome call site. |
| S1-F06 | Medium | A | `shell/rail/PinSectionPicker.tsx:83-90` | Add local `validatePinSectionName` and a `PinSectionNameField`; use them in both form paths. |
| S1-F07 | Medium | A | `shell/rail/railIcons.tsx:17-24` | Export a `RailIconSvg` wrapper or the ICON/STROKE constants; rebuild WatchGlyph and HostGlyph on it. |
| S1-F08 | Low | A | `shell/rail/Rail.tsx:918-920` | Export `comparePinSections` from PinSectionPicker or a small `pinSections` module; import it in Rail. |
| S1-F09 | Low | A | `shell/rail/RailRow.tsx:548` | Add `offlineHostLabel(name, online)` for both text sites, returning the plain label or name plus suffix. |
| S1-F10 | Low | A | `shell/rail/RailRow.module.css:226-229, 312-315` | Add `.truncate` and `.truncateFlex` to the surface or a new text module; compose them here and at the cross-lane truncation sites. |
| S1-F11 | Low | A | `shell/rail/RailRow.module.css:284-290` | Add one `[data-hue]` recipe for attention and danger ink; apply it to contextStatus and its siblings. |
| S1-F12 | Low | A | `shell/rail/RailRow.tsx:917` | Derive `BAND_LABEL` from `STATE_LABELS` or export a `bandLabel(band)` helper. |
| S1-F13 | Low | A | `shell/rail/useRailReveal.ts:132-134` | Add local `findRowBySessionRef` and `revealChainFor`; call them from both reveal paths. |
| S1-F14 | Low | A | `shell/rail/railNodes.ts:308-310` | Delete or wire `armedWatchCount`, `subagentIsCurrent`, and `activeWorkSummary`; verify the armedWatchCount test maps active to armed before deleting. |
| S1-F15 | Low | A | `shell/rail/Rail.tsx:1064-1071` | Wrap Settings and Hide in Tooltip with Search's wording, or drop all three wrappers. |
| S1-F16 | Low | B | `widgets/button/index.tsx:22-36` | Extend `Button` with `fullWidth` or a block variant; delete the `.newSession` wrapper and its class. |
| S1-F17 | Low | B | `widgets/menu/index.tsx:76-95` | Expose Menu's open state through `data-open` or `onOpenChange`; replace `:has(button[aria-expanded])`. |
| S1-F18 | Low | B | `shell/rail/RailRow.tsx:672-673` | Extend `HoverCard` with `block` or `wrapperClassName`; remove the `.sessionTitle > span` reach-in. |
| S1-K01 | none | keep | `shell/rail/RailRow.tsx:208-224` | Keep the rail row's bespoke disclosure geometry. |

### 10.7 G2 — settings, credentials, and provider connections

Coverage: 20/20 files, 7,817 lines. Fifteen recorded entries (0 High, 4 Medium, 11 Low), including one keep-bespoke entry.

| ID | Sev | Class | Site | Change |
|---|---|---|---|---|
| G2-F01 | Medium | B | `settings/sections/credentials/ConnectionFacts.tsx:10-20` | Extend `PropertyList` with the connection-fact row shape: `htmlFor`, `help`, `notice`, and `actions`; replace the second `<dl>` implementation. |
| G2-F02 | Low | A | `settings/sections/credentials/ConnectionFacts.tsx:32-49` | Render `FormRow` in `connectionSettingsFields` and `ConnectionSettingsDisclosure`; keep the two-column grid in a wrapper class if it is required. |
| G2-F03 | Low | A | `settings/sections/credentials/instanceDialogs.tsx:383-396` | Add one local `DialogActions` with the `.actions` class and use it at all four sites, or add a `dialogActions` recipe. |
| G2-F04 | Medium | A | `settings/sections/credentials/instanceDialogs.tsx:190-197` | Add `checkCredentialHeader(value)` returning the reason and message; use it in both validation paths. |
| G2-F05 | Medium | B | `settings/sections/credentials/instanceDialogs.tsx:604-613` | Add `spellCheck` to `TextareaProps` and pass it to the native textarea; replace the raw `<textarea>` with `Textarea`. |
| G2-F06 | Low | B | `settings/sections/credentials/clipboard.ts:1-38` | Extend `CopyButton` or the shared clipboard layer with the fallback and a text-label variant; use it for the device code. |
| G2-F07 | Low | A | `settings/sections/credentials/ProviderConnection.module.css:46-49` | Add `styles/note.module.css` with `note` and `help` recipes; compose the four modules and keep local margins out. |
| G2-F08 | Low | A | `settings/sections/credentials/instanceDialogs.module.css:1-5` | Add `styles/stack.module.css` with `stack` and gap variants; compose the three dialog bodies. |
| G2-F09 | Low | A | `settings/sections/credentials/oauthDialogs.tsx:371-373` | Replace the single-line status paragraphs and raw failure span with `InlineNotice` neutral or danger, using the correct `live` value. |
| G2-F10 | Low | A | `settings/sections/credentials/CredentialsHostScope.tsx:263-275` | Add a local `HostRetryNotice` that renders `InlineNotice` plus the Retry action; keep both messages and the `live` and `data-reason` values. |
| G2-F11 | Medium | B | `settings/sections/credentials/InstanceSheet.tsx:1164-1173` | Add `block` to `Button` and delete the `.fullRow` wrappers; coordinate with S1-F16 so one prop serves both. |
| G2-F12 | Low | C | `settings/sections/credentials/CredentialsSection.tsx:534` | Use `Chevron direction="left"` and `direction="right"` for the arrows now; add a shared glyph module for Plus, Star, and Corner icons. |
| G2-F13 | Low | A | `settings/sections/credentials/oauthDialogs.module.css:18-22` | Delete dead `.error` and its `CLASS` map entry. |
| G2-F14 | Low | B | `settings/sections/credentials/ConnectionFacts.module.css:81-89` | Add `fill` or `className` to `Input` and `Select`, set width 100% and min-width 0 on the widget class; delete the four tag selectors. |
| G2-K01 | Low | keep | `settings/sections/credentials/ProviderConnection.tsx:2037-2068` | Keep the `.review` inset surface; it is deliberate, not a Card copy. |

All 21 file-bearing lanes have now reported. The remaining uncertainty is the parent-recovered P6 and G3 material recorded in Appendix A, and the unverified claims each lane marked `suspected` in its report.

## 6. Roadmap

### Phase 0 — Baseline and guardrails (one small PR)
- Record the `ds-audit` baseline and the duplicate counts in the plan.
- Add any missing gallery sections only where a later phase will add a variant; no production behavior.
- Confirm the widget gallery completeness test and the frontend gates are green at the base.

### Phase 1 — CSS recipes and dead code (2–3 PRs)
- Field chrome and tap-floor recipes (Workstream 1); the interaction recipe after the focus-ring composition check.
- Sheet-body/dock-clearance composes and dead class deletion (Workstream 8).
- VisuallyHidden, eyebrow, fold-header, and pill-geometry shares; FormRow's contrast token swap with design sign-off.
- Row chrome, truncation, Dialog-body, pane-title, sidebar, and icon recipes (S3-F07/F08/F10, S2-F09/F10/F11/F13); scope the unscoped dockview selectors while there.
- Picker panel/field recipes into `internal/pickerPanel.module.css` (W3-F03).
- Composer/spawn CSS: the `[hidden]` cascade, prompt-card leading/attachment-strip recipes, and PromptCard's composer-only `:global`/`:has` rule (P5-F04/F11, W3-F15); the Send label collapse goes to Phase 3 if it needs a Button API.
- AskDock caption and selection recipes, plus the undefined `--ask-tab-font` (P6-R06/R07/R11). These are shared-CSS edits, not a widget change.
- Settings help/note caption, section container, and stack recipes, plus the settings dead CSS and stale module headers (G1-F01/F07, G3-F02/F03/F08); session-chrome focus-ring, truncation, and tabular-figure recipes (P7-F09/F11/F12).
- Y1's 60-declaration truncation recipe, the second visually-hidden copy, the eyebrow copies, and the dead/runtime token cleanup (Y1-F01/F02/F04/F06/F07/F08/F09/F10).
- Late-lane CSS and dead code: the shared overlay dock (P4-F02), the measure recipe (P4-F04, P8-F01), fill-column and body-hint recipes (P4-F08, P8-F06), the floating-panel and tone recipes (W2-F01/F02/F09), sheet and close-button CSS (W2-F05/F11/F14), truncation and hue recipes (S1-F10/F11), and settings note/stack/actions CSS (G2-F03/F07/F08). The dead pending stack (P4-F01), P1-F11's hint, P4-F07's classes, S1-F14's suspected exports, and G2-F13's error class land as deletions; P4-F01 lands alone because it removes about 600 production lines and a test.
- Token audit and the unambiguous literal cleanup. No widget API change.

### Phase 2 — Existing-widget adoption sweeps (4–6 PRs, split by surface)
- ListRow/PropertyList/DisclosureHeading where the semantics fit.
- InlineNotice and the retry convention.
- ConfirmDialog/FormRow/CollectionEditor residuals.
- SessionMenu's two dialogs to ConfirmDialog and the palette's command error to InlineNotice (S3-F04/F05); PaneTab's dot wrapper to StatusDot (S3-F09).
- WatchRow through a Disclosure summary slot or a local row-content component (S2-F03).
- DirectoryPicker's ListRow, Chevron/Disclosure, InlineNotice, and EmptyState/Skeleton adoptions, plus the three field errors (W3-F07–F10).
- Transcript tool-card adoptions: PropertyList, Chip, Disclosure, MCPToolArguments, InlineNotice, ListRow, and Eyebrow (P3-F03–F10, F14); the two High overhauls (delegate status vocabulary and empty states) stay in Phase 4.
- CodeBlock markup helper and DiffBlock copy (W3-F04/F16); the broader CodeBlock/DiffBlock/ShellCommandBlock media consolidation follows.
- AttachmentTile/ImageGallery lightbox CSS after W3; the shared `lightbox.module.css` image rule can land independently.
- Marketplace source fields and PathField wiring (G3-F04/F06); the marketplace load-error notice and launch save status (G3-F07/F13).
- Settings `SectionGate`/`RefreshErrorNotice` and the quiet status/warnings convention (G1-F02/F05/F06); TopNotesPanel's disclosure adoption and StatusRow's double meter (P7-F05/F06).
- jobWatch's outlined Card adoption (Y1-F03) in the same PR as the P3 tool-card work.
- Late-lane adoptions: InlineNotice in TranscriptDetailControl and the session error banner (P1-F05, P8-F03), the NotificationCard disclosure and barrel imports (P2-F07/F08), `NoticeLine` (P2-F12), `AnsiLineContent` (P4-F06), the byte formatter (P4-F09), StatusDot (P8-F08), the rail Tooltip wrappers (S1-F15), FormRow and InlineNotice in credentials (G2-F02/F09/F10), and Chevron plus shared glyphs (G2-F12). Verify the P2-F08 barrel exports before importing.
Each PR is one surface or one widget, not a repo-wide sweep.

### Phase 3 — Widget API extensions (5–6 PRs, each needs Jesse's approval)
- `Select variant="quiet"` and its two call sites.
- `PathField variant="prominent"` and the spawn directory tile.
- `ListRow value` and the mobile settings rows.
- `RadioGroup variant="list"` and OptionSheet.
- `ModelSwitchTrigger fill/shrink` and the two `:global` pierces.
- `ZoomableMedia radius` and the one tag-selector pierce (P5-F09).
- `Button collapseLabel` or a shared Send recipe (P5-F02).
- `ImageThumbnail` only if the structural duplication survives the CSS share and Jesse approves a new widget (P5-F01).
- `Listbox` and `Input`'s combobox extension (ref, rest props, combobox ARIA, `onKeyDown`, `autoFocus`, `aria-labelledby`/`data-*` passthrough, and a `size` variant for the palette's taller field): S3 proves four listbox sites (CommandPalette, SlashCompletionMenu, ModelCatalog, PathField) and three duplicated option-id helpers, and P6 proves AskDock's free-text/note field needs the same passthrough. This is a new-widget API, so it needs Jesse's approval.
- `PickerTrigger` (W3-F01 proves three closed triggers and the ARIA drift) and `Dialog` `panelClassName`/`bodyClassName` (W3-F11).
- `Chip mono`, `PropertyListRow muted`, and `Card padding="none"` (P3-F04/F03/F09).
- `CountButton` (S2-F02, M1-F01), `SegmentedControl` wrap/focus (S2-F06/F12), `ShortcutList`/`KeySequence` alternatives (S2-F04), and `FocusScope` className/asChild (S2-F08); each is a class B/C API decision.
- Close-glyph parameterization: one `RemoveIcon`, or a `CloseIcon` size prop that preserves the 1.5 stroke; do not render the 14px 16-view icon at 10px. No new widget is justified by the widget-layer evidence. Do not merge Badge/Chip/Meter/ListRow/Card/ContextCard/Disclosure/Tooltip/HoverCard/Skeleton/Loader; W1-F19 records them as intentionally distinct.
- `Timestamp` absolute-display variant only if Jesse approves it (G3-F14).
- `Textarea` fill/`fullWidth` (P7-F10), `PropertyList` row attributes (G1-F10), and `Diagnostics` item shape if it is widened (G1-F06).
- Late-lane extensions, each needing Jesse's approval: Disclosure leading/summaryClassName and Chevron open (P1-F04/F07), the transcript disclosure hook and Chip quiet variant (P2-F03/F09/F11/F14), ImageThumbnail, Input ref/fill, and the AnsiLineContent export (P4-F03/F05/F06), `useLongPress` (P8-F02), ConfirmDialog and Dialog extensions (W2-F03/F06), Button block, Menu open state, and HoverCard block (S1-F16/F17/F18), and PropertyList facts, Textarea spellCheck, CopyButton fallback, Button block, and Input/Select fill in credentials (G2-F01/F05/F06/F11/F14). Land Button block once for S1-F16 and G2-F11.
Each PR includes the widget gallery section, behavior tests, and the call-site migration.

### Phase 4 — Local shared components (3–5 PRs)
- Transcript `ToolCard`/`ToolStatusCard` and the four card modules.
- Session chrome rows/panels after P7.
- Code/diff shell after W3.
- The chunk-failure boundary (S3-F01), `useAsyncConfirm` (S3-F06), and `sameParams` (S3-F13).
- The activity collection gate (S2-F01) and the phone Overview overlay semantics (S2-F07).
- `middleTruncate` (P5-F05), the pending-attachment refusal helper (P5-F07), composer emptiness (P5-F12), and range/selection equality (P5-F13).
- `useDirectoryListing` (W3-F05), the StatusDot/Cadence family map (W3-F06), and the CodeBlock markup helper plus DiffBlock copy chrome (W3-F04/F16).
- The delegate status vocabulary table (P3-F01), the transcript `ToolMeta` primitive (P3-F08), and the absolute date/clock helper (P3-F13).
- AskDock's local `AnswerOptionRow` and the tablist roving mode (P6-R01/R02/R05), the shared `useCardPrimaryAction` (P6-R03), the answer-count helpers (P6-R04), and the selector/glyph/copy cleanups (P6-R09/R10/R12). Adopt the extended `Input` for the free-text/note field in the same PR or immediately after it.
- Pick one recommended-option presentation for the dock and the read-only transcript card (P6-R08) once Jesse decides; this is a product/copy call, not a mechanical migration.
- `writeFailureText` (G3-F01), the plugin/marketplace match helpers (G3-F05), `handleDetailEscape` (G3-F10), `updateSection` (G3-F11), the shared path-kind map (G3-F12), and the device-pairing alignment (G3-F09).
- `ReasoningEffortControl` (P7-F01), `PanelSection` (P7-F07), the launch-config pane adapter `launchFormPaths` (G1-F03), and `useSettingsPoll` (G1-F11).
- Late-lane local helpers: ToolRow's RowLead/RowTrigger/SummaryText and the EntityRef card shell (P1-F01/F03/F06), TranscriptRow/TranscriptColumn and jsonCodeBlock (P1-F08/F09), PendingRow and SpeakerHeader (P2-F01/F02/F06/F10/F13), the optional PaneLoading (P4-F10), the session error/stop helper and conversation faces (P8-F04/F05/F07/F09/F10), ResponsiveModal, visibleViewport, escape-to-close, and shown-while-closing (W2-F04/F07/F12/F13), the rail optimistic mutation, RailRowShell, ChunkFailureBoundary, RailSectionShell, nested-kind and pin-section helpers, RailIconSvg, offlineHostLabel, comparePinSections, bandLabel, and reveal helpers (S1-F01 through S1-F09, S1-F12/F13), and credential-header validation plus HostRetryNotice (G2-F04/F10).
These are local components, not widget API changes.

### Phase 5 — Cleanup, dev galleries, and enforcement (2–4 PRs)
- Eliminate the remaining raw controls by surface.
- Replace or classify the 35 module classes.
- Remove the duplicate `STATUS_LABEL`/`STATE_LABELS` export after approval; export or relocate `isWebHref`; classify Loader's elapsed props.
- Use `refParam` at the five lenient session-ref sites (S3-F12), with a test for the empty-ref rejection.
- Import-path hygiene for true deep component imports, including `mobile.tsx`'s borrow of `devicePairing`'s stylesheet (G1-F08) and `keybindings.tsx`'s internal hook (G1-F14); the App.tsx route-guard table (Y1-F05).
- Add a guard only if it catches a real regression without phrasing tests.
- Dev gallery scaffold (Workstream 9): `GallerySection`/`StateRow`, section discovery, harness mount, gallery frame and caption CSS, fixture thread, and raw gallery triggers. Dev-only, no production behavior; one or two PRs. Leave the `PluginsMockups` duplication alone (D1-F08).
- Late-lane suspected work: recheck S1-F14's `armedWatchCount` test before deleting, confirm P4-F01 left no `pending-message-stack` reference, and confirm no production mount appeared for the removed components. The suspected entries stay out of the change count until they are proved.

### Phase 6 — Verification and documentation (one PR)
- Update `docs/web-ui/design-system.md` §8 known gaps and the widget-adoption catalog.
- Update the subsystem map and any product-doc contract changed by the migrations.
- Run `make test-web`, `make test-web-browser` where geometry changed, targeted tests, and `ds-audit`; record the before/after counts.
- Re-run the late lanes' static-only checks. The recovered reports did not run tests or the browser harness, so every migration note in Workstream 10 is a hypothesis until its targeted test passes.

## PR sizing

Keep each PR at the repo's default couple-hundred-production-line budget. A workstream that exceeds it is split by surface or widget, not grown into one large PR. A finding that needs new machinery is a scope decision for Jesse, not something a migration PR absorbs.

## 7. Acceptance checks

Each migration PR must show:

1. The duplicate existed at the base: a grep or code excerpt with file:line, and a before/after count where the finding is quantitative.
2. The shared target already exists, or the PR carries the approved API change and its gallery/test.
3. The migrated call site uses the shared target by its public API, with no new `:global` reach-in or copied recipe.
4. The nearest behavior test covers the migrated state (interactive, read-only, disabled, error, empty, mobile), and it fails on the base where the behavior was actually wrong. Visual-only migrations use the existing gallery/harness checks, not a new phrasing assertion.
5. `make test-web` passes, plus targeted tests and `make test-web-browser` where geometry changed.
6. The PR's duplicate count went down and no new local copy of the same concept was introduced.
7. Any finding left unfixed is filed with its location, trigger, impact, and a link to the PR.

## 8. Intentionally bespoke

These are not consolidation targets; the plan records them so a later sweep does not "fix" a deliberate choice:

- **Transcript grammar.** `ToolRunGroup` and `TaskCheck` are transcript grammar, not reusable primitives. The design system says `ToolRunGroup` deliberately has no barrel entry and no gallery section; `TaskCheck` serves both the transcript task card and the tasks pane. Do not extract a ToolCard widget; a local `ToolCard` in the transcript layer is the right scope (Workstream 6).
- **Rail status grammar.** The browser rail uses a deliberately smaller status grammar than `<Cadence>` (design system, "Signature — the cadence instrument"); do not replace it with Cadence.
- **ZoomableMedia's larger view.** `ZoomableMedia` owns the trigger only; the dialog, gallery lightbox, or diagram viewer stays with the caller as a design decision. That does not excuse the duplicated frame/lightbox CSS: share the CSS (M1-F04, P5-F01) without folding the dialog into `ZoomableMedia`. An `ImageThumbnail` widget that owns the lightbox is a separate class-C product decision, not an automatic consequence of the CSS share.
- **Widgets that look alike.** Badge (count), Chip (label with an optional remove control), Meter (measure), ListRow/Card/ContextCard (different row and card semantics), Disclosure vs DisclosureHeading (native `<details>` vs a heading-wrapped button), Tooltip vs HoverCard, and Skeleton vs Loader stay separate. Share geometry only where the findings name it; merging them would erase distinctions the design docs require (W1-F19).
- **ActivityPanel.** Retired by product ruling; do not restore or rewire it.
- **ToolCallItem's container.** It uses a `<div>` rather than a native `<details>` deliberately; do not convert it during the `<details>` sweep.
- **Native `<details>` in message bodies.** `MemoryContextItem.tsx:43-53` documents its `<details>` as intentionally collapsed and independent of the shared disclosure store; `NotificationCard.tsx:602-607` uses the same uncontrolled shape for raw notification text. Converting either to `Disclosure` would make open state shared and persistent. Keep them unless a product decision accepts that change.
- **Disabled-Start reason.** `Spawn.tsx:2812-2816` deliberately renders the reason as a caption beside the disabled Start button, not as an `InlineNotice` (I1-F13); keep the caption.
- **Cascade spine.** `CascadeSpine.tsx:12-26` and its zoom stylesheet are a 52px vertical-text control with exact runtime-label and decorative-StatusDot semantics; the geometry is genuinely bespoke (M1-F09). Do not turn it into a generic spine or CountButton without a product decision.
- **Launcher plugin row.** `PluginRow.tsx:172-214` keeps a Switch-owned shape outside ListRow; the ListRow rows in the same file cover other content (M1-F10). Do not force the launcher row into ListRow.
- **Widget-composite structure.** VirtualList and Tree have different structures and keyboard models; PaneScaffold is the only pane shell; the single-purpose glyph widgets are distinct marks inside ToolIcon's frame; Dropzone and PromptCard already compose as intended; DiffBlock and DiffTable keep their own presentation. Only DiffBlock's missing copy control is a real gap (W3-F18–F22). DiffTable has no production caller; that is an owner decision to keep, mark gallery-only, or remove, not an audit-driven deletion (W3-F17).
- **Transcript tool grammar.** `TaskCheck`'s status glyph is deliberately bespoke (P3-F15). `web_search`'s inline linkified anchor cannot be expressed by Markdown's prose links or ContextCard (P3-F16). The subagent card's icon-triggered recent-activity disclosure belongs inside its stats line, not a shared disclosure (P3-F17). The per-descriptor icon families follow the one-renderer-per-tool contract (P3-F18).
- **Palette option rows.** The palette's rows are bespoke ARIA options with free content and an inline glyph/title/id/hint layout (S3-F16). The Listbox change may share their neutral chrome, but not ListRow's grid.
- **Dockview chrome.** The `:global(.dv-*)` overrides in `PaneTab.module.css`, `DockHost.module.css`, `dockview-theme.css`, and `singlePane/global.css` are intentional third-party chrome theming (S3-F15, S2-F15). Scope the unscoped selectors; do not componentize them.
- **ToastRegion.** It is the single Toast mount point and owns no rendering that Toast does not already own (S3-F14). Fix its stale comment; do not add a component there.
- **Hold-hint bubbles.** `HoldHints.tsx:139-178` and `holdhints.module.css:11-44` use KeyHint and deliberately do not use Tooltip; the bubble presentation is bespoke (S2-F14). Reuse the shared key-sequence renderer from 4.4 if it lands, but keep the bubble.
- **MobilePanel's Sheet hooks.** `MobilePanel.tsx:26-36` passes public `Sheet` props for its flush body (S2-F16). Keep them; revisit a `Sheet variant="flush"` only when a second drawer needs the same shape.
- **Loading vs empty.** EmptyState is not a loading state; cold starts must not flash an empty state before the first read resolves.

## Appendix A — lane ledger

| Lane | Status | Coverage | High | Medium | Low | Findings | Residual catalog |
|---|---|---|---:|---:|---:|---:|---:|
| D1 | reported | 133/133 files in manifest, 13,712 lines (manifest `wc -l` = 13,712). Every manifest file was read in this session. Files whose content a finding cites were re-opened in the final pass; 43 gallery-section modules and 8 entry points were read in the earlier pass only (see "Unverified files"). | 1 | 7 | 3 | 11 | 1 |
| G1 | reported | 56/56 files, 7319 lines (every manifest file read in full with `read_file`; `wc -l` over the manifest = 7319) | 2 | 5 | 10 | 17 | 9 |
| G2 | reported | 20/20 files in manifest, 7817 lines (lane transcript; every production line cited below was re-opened on 347036b94b during recovery) | 0 | 4 | 11 | 15 | 9 |
| G3 | reported | 16/16 files, 3,588 lines (all manifest files read in full). | 2 | 7 | 5 | 14 | 5 |
| I1 | reported | 10/10 files, 5364 lines | 2 | 6 | 5 | 13 | 7 |
| M1 | reported | 20/20 files, 1926 lines (the `wc -l` total of the manifest; every file read in full) | 0 | 4 | 6 | 10 | 11 |
| P1 | reported | 28/28 files, 5491 lines | 2 | 4 | 5 | 11 | 10 |
| P2 | reported | 24/24 files, 4925 lines (lane-recorded; the recovery pass re-opened every cited site, not every line) | 3 | 5 | 8 | 16 | 4 |
| P3 | reported | 27/27 manifest files read in full, 4,737 manifest lines (per `wc -l` on `lanes/P3.txt`). Findings below are limited to what I read; adjacent-file reads were partial as noted, and no automated audit or test run backs this report. | 2 | 8 | 8 | 18 | 4 |
| P4 | reported | 21/21 files in manifest, 2345 lines read in full | 1 | 4 | 6 | 11 | 5 |
| P5 | reported | 14/14 files, 3255 lines | 0 | 8 | 7 | 15 | 6 |
| P6 | reported | 4/4 manifest files read in full, 1154 lines (askdock.module.css 98, AskDock.tsx 637, askquestioncard.module.css 164, AskQuestionCard.tsx 255). | 0 | 4 | 8 | 12 | 6 |
| P7 | reported | 19/19 files, 3854 lines (every manifest file read in full) | 2 | 6 | 5 | 13 | 15 |
| P8 | reported | 8/8 files in manifest, 1942 lines read in full | 2 | 3 | 5 | 10 | 3 |
| S1 | reported | 15/15 files in manifest, 3923 lines read in full | 3 | 4 | 11 | 19 | 10 |
| S2 | reported | 27/27 files, 3306 lines | 2 | 6 | 8 | 16 | 10 |
| S3 | reported | 23/23 files, 4676 lines | 2 | 7 | 8 | 17 | 6 |
| W1 | reported | 62/62 files, 5133 lines | 3 | 5 | 11 | 19 | 13 |
| W2 | reported | 21/21 files, 4,706 lines read (lane's own `wc -l` total; Menu read in four chunks) | 3 | 5 | 6 | 14 | 4 |
| W3 | reported | 45/45 files, 6936 lines | 4 | 8 | 10 | 22 | 10 |
| Y1 | reported | 8/8 files in manifest, 1231 lines read in full | 1 | 3 | 6 | 10 | 5 |

Reported lanes: 21/21.
Counts come from each lane's completion line. S1 counts one keep-bespoke entry with no severity, so its severity columns sum to one less than its total.

**P6 and G3 are parent-recovered, not the lanes' own final tables.** P6's report survived as coverage, summary counts, and catalog rows; its findings section was lost when the run was cut off. The 12 rows above were re-verified by the parent from the lane's surviving analysis and the cited lines, so P6's ledger row is 12 findings (4 Medium, 8 Low), not the lane's claimed 17. The five missing findings are unrecorded, not refuted. G3 is also parent-recovered: 14 recorded (2 High, 7 Medium, 5 Low); four further Low findings were counted but never drafted. The other late lanes (G2, P1, P2, P4, P8, S1, W2) were recovered from their transcripts and every cited line was re-opened on the base; P2's original coverage record is relied on and its cited sites were re-opened. The ledger uses each lane's completion line, with G3 and P6 set to the parent-recovered counts above. S1's completion line includes one keep-bespoke entry with no severity, so its row's severity columns sum to one less than its total.

## Appendix B — October 2026 catalog status

| Lane | Fixed | Changed | Still present | Other |
|---|---:|---:|---:|---:|
| D1 | 0 | 0 | 0 | 1 |
| G1 | 21 | 3 | 1 | 5 |
| G2 | 8 | 5 | 2 | 2 |
| G3 | 11 | 5 | 0 | 0 |
| I1 | 18 | 2 | 5 | 0 |
| M1 | 0 | 0 | 0 | 11 |
| P1 | 1 | 0 | 7 | 3 |
| P2 | 6 | 0 | 2 | 2 |
| P3 | 3 | 1 | 2 | 1 |
| P4 | 6 | 2 | 0 | 3 |
| P5 | 5 | 2 | 4 | 0 |
| P6 | 2 | 0 | 5 | 1 |
| P7 | 3 | 1 | 9 | 5 |
| P8 | 2 | 1 | 2 | 0 |
| S1 | 15 | 0 | 4 | 6 |
| S2 | 12 | 3 | 3 | 4 |
| S3 | 8 | 1 | 2 | 3 |
| W1 | 11 | 4 | 7 | 2 |
| W2 | 4 | 2 | 2 | 0 |
| W3 | 2 | 2 | 7 | 1 |
| Y1 | 3 | 3 | 0 | 2 |

Each lane's catalog uses its own status vocabulary; `Other` aggregates narrowed, keep, bespoke, and status rows. The lane report is authoritative for the exact rows, so the table does not sum them into a single verdict.

## Appendix C — cross-cutting census (X1)

X1 measured patterns across `src`; it read files only to confirm a handful of flagged sites. Every number is reproducible from the commands in `reports/X1.md` in the audit scratch directory. The census is not a lane: it owns no files and contributes no finding IDs.

| Measure | Result |
|---|---|
| Barrel imports into `src/widgets` | 703 statements across 238 files; 77 exported symbols, 5 with zero barrel imports |
| Deep imports into `widgets/internal/*` | 153 statements, the largest bypass surface |
| Raw controls outside widgets/dev | 30 in 18 files: 20 `<button>`, 6 `<input>`, 2 `<select>`, 2 `<textarea>`; matches `ds-audit` exactly |
| Widget-concept CSS classes outside widgets/dev | 35 names across 24 files |
| Native `title=` attributes vs `Tooltip` | 173 occurrences in 71 files vs 6 files importing `Tooltip` |
| Hand-built live regions | 27 `role="status"` in 20 files; 6 `role="alert"` in 5 files |
| Hand-built listbox/tab semantics | 2 `role="listbox"`, 2 `role="option"`, 1 `role="tablist"`, 1 `role="tab"`, 1 `role="radiogroup"` |
| Inline `<svg>` and local icons | 13 `<svg>` in 8 files; 9 local `*Icon` components |
| CSS recipes outside widgets/dev | 355 `display: flex` in 103 files; 140 `align-items: center` in 60; 37 `text-overflow: ellipsis` in 25; 55 `white-space: nowrap` in 35 |
| Token bypasses and gaps | 86 literal bypasses in 186 scanned CSS files; 12 unused tokens; 13 `var()` references to undeclared names |

The census leaves these unmeasured, and the plan does not treat them as settled: per-widget deep-import file lists, dead-export candidates, icon path-data comparison, per-file token-bypass ranking, and the local `srOnly`/`clip: rect`/`composes` search. Its `var()` undefined-name list includes likely parser artifacts; verify each before changing a token.
