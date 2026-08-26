# Transcript detail design-system correction

Date: 2026-08-26
Status: Proposed — independent review complete; awaiting final approval
Decision owner: Jesse
Amends: `docs/superpowers/specs/2026-08-25-transcript-detail-configuration-design.md`

## Summary

The transcript detail feature is behaviorally complete, but its presentation violates Evener's current Web design system. The implementation imports shared widgets, then overrides their internal roles and geometry to create a private visual language. The resulting segmented control, switches, buttons, cards, Select, Disclosure, and Popover content do not match the platform gallery.

This correction promotes the familiar pre-feature **Everything | Intent** treatment into a first-class `SegmentedControl` widget. It updates that prior art to the current Beautiful UI-derived token system, adds complete gallery coverage, and refits the transcript feature by composing shared widgets without styling their internals.

The feature's persistence, hub protocol, projection, critical-row invariants, scroll/focus preservation, and preview data isolation remain unchanged. One domain rule changes: an explicitly selected Custom content vector keeps its Custom identity even when its booleans equal a named preset.

## Design authority

The current sources of visual law are:

1. `docs/web-ui/design-system.md`;
2. `docs/superpowers/specs/2026-08-13-webui-beautiful-ui-retheme-design.md`;
3. the shared widgets under `cmd/evener-hub/frontend/src/widgets/`;
4. the two-theme widget gallery under `src/dev/gallery-sections/`.

Shared widgets own their chrome. Feature CSS may arrange a widget, but it must not reach through the widget's public API to restyle role descendants, hide internal marks, change control geometry, or duplicate shared surface/elevation behavior.

## Existing visual precedent

Before this feature, Session rendered **Everything | Intent** through `RadioGroup` plus private `.viewSelector` CSS in `session.module.css`. The treatment is the visual ancestor of the approved inset-neutral direction. It was not a reusable control:

- Session CSS reached into `role="radiogroup"` and `role="radio"` descendants;
- it hid RadioGroup's standard dot;
- it used superseded `--surface-2` and accent-wash selection treatment;
- it had no shared API, design-system entry, or gallery states;
- its Mobile behavior wrapped.

The new widget preserves the familiar compact grouping but replaces the private skin with a platform-owned contract.

## Goals

1. Add one first-class, reusable SegmentedControl for concise single-choice sets.
2. Match Evener's current field, selection, density, focus, elevation, color, and motion grammar.
3. Represent Chat, Intent, Tools, Activity, Full, and Custom as six explicit transcript choices.
4. Refit the transcript editor, live Popover/Sheet, and Settings cards by composing existing widgets.
5. Demonstrate actual phone-width Mobile preview geometry in Settings.
6. Cover every shared state in both themes and every required responsive boundary in a real browser.
7. Preserve all accepted transcript behavior except the explicit Custom-normalization change.

## Non-goals

1. Redesign unrelated Settings sections.
2. Migrate existing verbose RadioGroup consumers.
3. Change the hub protocol, durable file schema, revisions, notifications, or capability negotiation.
4. Change transcript projection or critical-row visibility.
5. Add icons, vertical orientation, theme-specific props, or arbitrary styling escape hatches to SegmentedControl.
6. Add a decorative phone bezel or an inner scroller to the Mobile preview.
7. Introduce a new general-purpose Callout/status widget in this correction.

## Approved product decisions

| Decision | Approved behavior |
| --- | --- |
| Shared control | Add a first-class generic SegmentedControl. |
| Visual direction | Inset neutral track, based on Everything\|Intent prior art. |
| Transcript choices | Chat, Intent, Tools, Activity, Full, Custom. |
| Custom selection | Restore the mounted editor's last Custom vector; otherwise clone the current preset. |
| Custom normalization | Preserve explicit Custom identity even when its vector equals a preset. |
| Customize/Advanced disclosure | Reset closed whenever its editor mounts. |
| Mobile Settings preview | Render a centered phone-width production preview. |
| Critical-row explanation | Remove the explanatory note from live and Settings editors. |
| Settings hierarchy | One selected-state owner; remove duplicate Current detail readouts. |
| 320px geometry | Keep six equal segments in normal Card padding; require 44px minimum height, allow flexible inline widths and visible-label ellipsis. |

## SegmentedControl

### Public API

```ts
export interface SegmentedControlOption<T extends string = string> {
  value: T;
  label: string;
  accessibleLabel?: string;
  disabled?: boolean;
}

export interface SegmentedControlProps<T extends string = string> {
  label: string;
  value: T;
  options: readonly SegmentedControlOption<T>[];
  onChange(value: T): void;
  disabled?: boolean;
  size?: "sm" | "md";
  fullWidth?: boolean;
  id?: string;
  "aria-describedby"?: string;
}
```

The widget accepts two through six concise peer options. Before rendering, it throws a developer error when:

- the option count is outside that range;
- option values are not unique;
- `value` does not match exactly one option;
- the group `label` is empty after trimming;
- any option `label` is empty after trimming; or
- any supplied option `accessibleLabel` is empty after trimming.

Each option's effective accessible name is `accessibleLabel ?? label`. `size` defaults to `"md"`; `fullWidth` defaults to `false`. The widget does not accept `className`, icons, orientation, colors, per-option styling, or a presentation variant.

`RadioGroup` remains the standard control for verbose or vertically stacked choices. `Select` remains the standard compact choice when labels or option counts exceed the SegmentedControl contract.

### Semantics

The visible label owns the inner group's accessible name through `aria-labelledby`. A supplied `id` is applied unchanged to the `radiogroup`; without one, the widget generates the group ID. `aria-describedby` is forwarded to the `radiogroup`, not its wrapper or options. The group uses:

- `role="radiogroup"`;
- `aria-orientation="horizontal"`;
- real buttons with `role="radio"`;
- `aria-checked` on every option;
- native `disabled` on disabled options;
- `aria-disabled="true"` when the group-level `disabled` prop is true;
- roving `tabIndex`.

Exactly one option is always checked, including when that selected option is disabled. Tab enters on the selected enabled option. If selection is disabled, focus enters on the first enabled option. A disabled group or a group with no enabled option has no tabbable option.

Each rendered option button receives `disabled={disabled || option.disabled === true}`. The selection handlers also guard group disablement, so pointer, Enter, Space, and navigation keys emit no `onChange` call.

Keyboard behavior follows the ARIA radio-group pattern:

- Right and Down select the next enabled option;
- Left and Up select the previous enabled option;
- movement wraps and skips disabled options;
- Home selects the first enabled option;
- End selects the last enabled option;
- Space, Enter, and click select the focused option;
- one user action emits at most one `onChange` call.

Selecting the current value does not emit a duplicate change.

### Visual contract

The widget contains no theme branch. Theme-sensitive categories—color, type, radius, focus, elevation, and motion—use design-system tokens. Stable control geometry uses the literal values documented below.

The root uses `display: flex`, `flex-direction: column`, `align-items: flex-start`, `min-inline-size: 0`, and `gap: var(--space-2)`. Its visible label uses `font-family: var(--font-sans)`, `font-size: var(--font-size-ui)`, `line-height: var(--line-height-body)`, `font-weight: var(--font-weight-medium)`, and `color: var(--ink-hi)`, matching RadioGroup's field-label grammar.

The group track uses `display: inline-grid`, `align-items: stretch`, and one `minmax(0, 1fr)` column per option. It has:

- `background: var(--field)`;
- `border: 1px solid var(--edge)`;
- `box-shadow: var(--shadow-inset-field)`;
- `border-radius: var(--radius-control)`;
- `padding: 2px`;
- `gap: 2px` between segments.

The 2px padding and gap are an explicit control-microgeometry exception to the 4px spacing grid, consistent with the shared Switch's 2px internal geometry. The track uses `box-sizing: border-box` and leaves visual overflow visible so option focus outlines cannot be clipped.

Each option button uses:

- `display: flex`, `align-items: center`, and `justify-content: center`;
- `min-inline-size: 0` and `inline-size: 100%`;
- `border: 0` and `border-radius: var(--radius-control)`;
- transparent background;
- `padding-block: 0` and `padding-inline: var(--space-1)`;
- `--font-sans`, `--font-size-ui`, `--line-height-body`, and `--font-weight-medium`;
- centered text and a pointer cursor.

Unselected segments are transparent and use `--ink-mid`. Hover uses `--hover-1`. The selected segment uses `--hover-2`, `--ink-hi`, and semibold text. Selection never changes border width, padding, or box size. Persistent accent borders are forbidden.

Keyboard focus uses `outline: var(--focus-ring)` with `outline-offset: 2px` and appears only on `:focus-visible`. Because the track leaves overflow visible, first and last option rings remain unclipped. A selected and focused option keeps both the neutral selected fill and the focus ring.

Every natively disabled option suppresses hover, uses the platform's not-allowed cursor, and owns one `opacity: 0.5` declaration. The root and track never add opacity, so group-disabled and individually disabled options are not attenuated twice. A disabled selected option retains its selected fill beneath that opacity. The disabled treatment does not depend on hue.

Transitions name only background, border, color, and box-shadow properties and use `--motion-duration-hover` with `--motion-easing-standard`. The widget disables its own transitions under reduced motion.

### Density and responsive behavior

Desktop option-button block sizes align with Button density:

- `sm`: 28px high;
- `md`: 32px high.

The outer track is 34px for `sm` and 38px for `md`: option block size plus two 2px padding edges and two 1px borders. At `(max-width: 899px)`, each option instead uses `min-block-size: var(--tap-min)` (44px). The resulting Mobile track is 50px high. Mobile does not impose a 44px minimum inline size on these concise labelled options; this follows the shared labelled Button precedent, which enlarges block size without forcing every text button to 44px wide.

`fullWidth` sets the track to `inline-size: 100%` and distributes options as `repeat(n, minmax(0, 1fr))`. Options use `min-inline-size: 0`; their visible label spans use `white-space: nowrap`, `overflow: hidden`, and `text-overflow: ellipsis`. The control stays on one row and never wraps. Each DOM label remains the complete supplied string, and every option keeps its complete accessible name; a tooltip or `title` is never the sole name.

The six-option Settings case has this computed geometry:

| Viewport | After PaneScaffold padding | After Card padding / track width | Track space after borders, padding, and five gaps | Equal option width | Mobile track height |
| --- | ---: | ---: | ---: | ---: | ---: |
| 320px | 288px | 256px | 240px | 40px | 50px |
| 390px | 358px | 326px | 310px | 51.667px | 50px |

The 320px row is the deliberate lower bound: 44px applies to each option's block size, while the 40px inline size may ellipsize its visible label. No feature-specific negative margin or full-bleed escape is permitted.

The widget must not create horizontal scrolling in its documented two-to-six-option range. Consumers with longer labels or more options use RadioGroup or Select.

### Gallery and documentation

Implementation adds:

- `src/widgets/segmentedcontrol/index.tsx`;
- `src/widgets/segmentedcontrol/segmentedcontrol.module.css`;
- `src/widgets/segmentedcontrol/segmentedcontrol.test.tsx`;
- barrel exports in `src/widgets/index.ts`;
- `src/dev/gallery-sections/segmentedcontrol.tsx`;
- an inventory row in `docs/web-ui/design-system.md`.

The gallery shows these states under `ThemeFlip`:

1. interactive `md` intrinsic width;
2. `md` full-width six-option transcript case;
3. `sm`;
4. first, middle, last, and Custom selected;
5. one disabled unselected option;
6. selected disabled option;
7. disabled group;
8. keyboard focus-visible state;
9. 320px and 390px narrow frames.

## Explicit Custom state

### Normalization

Frontend normalization no longer converts an explicit Custom vector to a named preset merely because its booleans match that preset. The frontend `ContentSelection` is flat, and its `kind` remains authoritative:

```ts
{ kind: "preset", level: "tools" }
```

and

```ts
{
  kind: "custom",
  toolIntent: true,
  toolCalls: true,
  reasoning: false,
  expandByDefault: false,
}
```

remain distinct content selections even though their vectors match.

The existing wire and durable shape remains nested:

```ts
{
  kind: "custom",
  custom: {
    toolIntent: true,
    toolCalls: true,
    reasoning: false,
    expandByDefault: false,
  },
}
```

`toWireConfig` maps the flat frontend Custom selection to that nested shape. `fromWireConfig` flattens the nested vector for frontend use. Both directions preserve `kind: "custom"`, including when the vector equals Chat, Intent, Tools, Activity, or Full. Backend validation and persistence already accept the nested shape, so no protocol or durable-schema migration is required.

### Mounted-editor memory

Each mounted `TranscriptDetailEditor` caches the most recent Custom vector it receives or emits.

- When the effective value is Custom, the editor refreshes its cache.
- Selecting a named preset emits that preset and keeps the cache.
- Selecting Custom emits the cached vector when one exists.
- Before the first Custom value, selecting Custom clones the currently selected preset into an explicit Custom vector.
- Remounting the editor clears dormant history. Selecting Custom after remount clones the current preset unless the effective value is already Custom.

The cache is editor state, not persisted product state. The local/hub configuration remains the single source of the active choice.

Content selection never alters independent Metrics or Diagnostics fields.

### Full baseline semantics

Rendering visibility follows the content vector, regardless of whether its kind is preset or Custom. Baseline reset behavior remains stricter:

- only `{ kind: "preset", level: "full" }` establishes the Full open baseline and clears prior per-item closed overrides for currently eligible rows;
- a Custom vector with `expandByDefault: true`, including one equal to Full, supplies the ordinary default-open value but does not clear explicit disclosure choices;
- an explicit collapse continues to win after either default is established;
- leaving and re-entering the named Full preset establishes a new baseline, as in the amended specification.

Required transition coverage includes named Full to equivalent Custom, equivalent Custom to named Full, remount under each kind, and manual collapse before and after each transition.

## Shared Disclosure controlled mode

The editor must use the shared Disclosure chrome, but the approved behavior resets the disclosure closed on mount. The shared widget therefore gains an optional controlled mode while preserving its existing store-backed mode.

The API combines common content and disabled props with a discriminated state union:

```ts
interface DisclosureCommonProps {
  summary: ReactNode;
  children: ReactNode;
  disabled?: boolean;
  "data-testid"?: string;
}

type DisclosureStateProps =
  | {
      id: string;
      defaultOpen?: boolean;
      open?: never;
      onOpenChange?: never;
    }
  | {
      open: boolean;
      onOpenChange(open: boolean): void;
      id?: never;
      defaultOpen?: never;
    };

type DisclosureProps = DisclosureCommonProps & DisclosureStateProps;
```

Existing consumers continue to pass `id` and retain disclosure-store persistence. Controlled consumers pass `open/onOpenChange` and own state. The implementation separates controlled and store-backed internals so no retained component calls hooks conditionally.

`disabled` has the same contract in both modes. Disclosure keeps the supplied or stored `open` state but makes its native `<summary>` inert: it sets `aria-disabled="true"` and `tabIndex={-1}`, prevents native toggle behavior, and emits neither `onOpenChange` nor a store mutation. Removing `disabled` restores ordinary focus and activation. Prop rerenders update controlled `open` and `disabled` state without remounting.

Only the disabled summary owns visual attenuation. Its `aria-disabled="true"` selector sets `cursor: not-allowed` and `opacity: 0.5`, and the hover wash applies only to enabled summaries. An open disabled summary retains the normal open `--hover-2` wash under that opacity. The `<details>` root and body never receive disabled opacity, so already-open content stays fully legible.

TranscriptDetailEditor uses local `useState(false)`. Each mount starts closed and passes its existing editor-level `disabled` state through to Disclosure. Shared Disclosure still owns summary markup, Chevron, focus, click/keyboard behavior, motion, and reduced-motion treatment.

The Disclosure gallery and `docs/web-ui/design-system.md` inventory are part of this shared-API change. Under `ThemeFlip`, the gallery adds a disabled collapsed store-backed example and a disabled open controlled example, proving that disabled treatment works in both themes without dimming the open body.

## TranscriptDetailEditor refit

The editor composes:

- SegmentedControl for six content states;
- controlled Disclosure for **Customize & advanced**;
- unmodified Switch controls;
- FormRow plus Select for Hook exit messages.

Feature CSS retains only layout:

- vertical stack and gaps;
- `gap: var(--space-3)` normally and `gap: var(--space-2)` when `compact`;
- fieldset reset and semantic grouping;
- a named `transcript-detail-editor` inline-size container on the editor root;
- three Advanced columns above 34rem;
- one Advanced column at `max-width: 34rem`.

Feature CSS deletes:

- every RadioGroup descendant selector;
- the raw Advanced-button skin and text triangles;
- Switch role/geometry overrides;
- raw Select styles;
- separate Current detail readout and Custom accent treatment;
- the critical-row explanatory callout;
- local reduced-motion blocks already owned by shared widgets.

The disclosure summary is:

- `Customize & advanced · N extras` for a named preset;
- `Customize & advanced · Custom content · N extras` for Custom.

`N` counts enabled Metric and Diagnostic extras only. It does not count Content switches.

Content, Metrics, and Diagnostics remain semantic fieldsets. They use flat grouping rather than Card-like borders or nested raised surfaces.

## Live Popover and Sheet refit

The live trigger becomes a shared secondary Button. It keeps the concise summary:

- `Detail: Tools` when no extras are enabled;
- `Detail: Activity · 2 extras` when extras are enabled;
- `Detail: Custom · 2 extras` for Custom.

Desktop continues to use Popover. Popover alone owns background, radius, shadow, portal, position, and overlay motion. The feature child supplies only:

- `box-sizing: border-box`;
- `inline-size: min(42rem, calc(100vw - var(--space-8)))`;
- `padding: var(--space-4)`;
- `max-block-size: calc(100dvh - var(--space-8))`;
- `overflow-y: auto`;
- internal layout.

The consumer passes `closeOnScroll={false}` because the panel itself can scroll. Scrolling inside the panel must not close it. The desktop child uses `role="dialog"`, `aria-modal="false"`, and a heading referenced by `aria-labelledby`. The trigger uses `aria-haspopup="dialog"` and reflects open state through `aria-expanded`. If implementation adds `aria-controls`, its stable value must reference the mounted dialog's actual ID.

Mobile continues to use the shared bottom Sheet. Sheet owns its scrim, surface, close control, focus trap, focus return, safe geometry, and motion. The same trigger `aria-haspopup` and `aria-expanded` contract applies to both Desktop and Mobile.

The footer actions compose shared Button:

- secondary **Use hub default** when a local override exists;
- quiet **Edit hub defaults**.

Parent grid/flex controls placement and stretching. Feature CSS does not restyle Button internals.

## Settings refit

### Section hierarchy

The section uses one concise introduction:

> Transcript display defaults sync to devices paired with this hub. Live transcript choices remain browser-local.

The section removes repeated Current detail lines and the critical-row explanation. SegmentedControl is the only selected-state presentation within each card.

Desktop and Mobile cards remain vertically stacked.

### Card composition

Each semantic `<article>` wraps a shared Card. Card alone owns surface, padding, radius, and `--shadow-card`. Feature CSS retains heading, revision, editor, preview, inventory, status, and stack layout.

No feature selector duplicates Card's border, background, padding, radius, or elevation.

### Preview treatment

The Desktop preview uses the available card width.

The Mobile card's outer preview well is the centered, width-bearing canvas. This is the element that contains the **Example only—not your data** heading, padding, and production preview host; the inner TranscriptBody host does not own phone width. The outer well uses `box-sizing: border-box`, the existing test marker becomes `data-testid="transcript-display-preview-canvas-mobile"`, and its layout is:

```css
width: min(390px, 100%);
margin-inline: auto;
```

It renders the same production TranscriptBody and fixed fabricated model. It has no device bezel, fake browser chrome, inner VirtualList, or independent scroll region.

The browser assertion measures `transcript-display-preview-canvas-mobile` and compares its border box to `min(390px, available preview-section width)`: it is exactly 390px when the card offers at least 390px and exactly the available width below that point. Under the documented current PaneScaffold and Card padding, the 320px viewport case is 256px and the 390px viewport case is 326px. The shown/hidden inventory remains a separate sibling and does not affect this measurement.

The preview well uses `--surface-canvas`. The shown/hidden inventory uses a neutral `--surface-inset` region without an accent stripe. Preview interactions retain isolated disclosure scope and perform no thread-store read, RPC, or real-data access.

## Status, warnings, and failures

Feature CSS no longer uses accent bars as decoration or as generic status/error treatment.

- Loading and ordinary status use neutral ink and neutral/inset surfaces.
- Passive loading, older-hub support, and ordinary hub-state updates use one `role="status"` region with explicit copy.
- Retryable configuration load, browser-storage, and save failures use one inline `role="alert"` region with explicit copy and a shared Button when retry is available.
- Acknowledged save success continues through the existing Toast system. Save failures remain inline retryable alerts and do not also emit a Toast.
- Retry actions use shared Button.
- Feature paths do not gain a new semantic-hue token-contract exception. A semantic hue appears only through an already reviewed shared widget.

One state change produces at most one live-region announcement. No message appears simultaneously in Toast and an inline `status` or `alert` region.

All copy uses sentence case and platform type/line-height tokens. Raw `1.45`/`1.5` line-height declarations are replaced by `--line-height-body`.

## Accessibility

Acceptance requires:

1. complete source label strings in the DOM and complete accessible names; visible labels may ellipsize only when constrained;
2. **Full** visible and **Full detail** accessible;
3. explicit Custom selected state;
4. keyboard selection independent of pointer input;
5. focus visible only under `:focus-visible`;
6. selected state conveyed by `aria-checked` and neutral fill, not hue alone;
7. 44px Mobile block-size targets without forcing 44px SegmentedControl inline sizes or enlarged Switch visuals;
8. labelled desktop nonmodal dialog semantics;
9. existing Sheet modal semantics and focus return;
10. labelled Select through FormRow;
11. no duplicate detail-change live regions;
12. disabled options skipped by keyboard movement;
13. `aria-labelledby`, `aria-orientation`, group `id`, and descriptions forwarded as specified;
14. DOM focus moves to the option selected by every navigation key;
15. all-disabled and group-disabled controls expose no tab stop;
16. Disclosure disabled semantics match controlled and store-backed modes;
17. live triggers expose `aria-haspopup="dialog"` and accurate `aria-expanded` state.

## Responsive behavior

The browser guard covers:

- 320px Mobile;
- 390px Mobile;
- 899px Mobile;
- 900px Desktop boundary;
- 1024px Desktop with a narrow dock pane;
- 1400px wide Desktop.

At every width:

- SegmentedControl stays on one row;
- no segment or ancestor scrolls horizontally;
- selected geometry remains stable;
- the Detail trigger remains reachable;
- Popover or Sheet stays within the viewport;
- Mobile SegmentedControl options meet 44px in block size; their inline size may shrink below 44px;
- Advanced fieldsets stack when their container is narrow;
- Settings cards remain stacked;
- the Mobile preview is `min(390px, available width)` and creates no inner scroll.

## Testing strategy

### SegmentedControl tests

Tests cover:

- two and six options;
- developer errors for invalid option counts, duplicate values, unmatched controlled values, empty group or option labels, and empty supplied accessible labels;
- controlled selection;
- visible and accessible labels;
- `aria-labelledby`, horizontal `aria-orientation`, group `id`, `aria-describedby`, and group `aria-disabled` forwarding;
- click, Enter, and Space;
- arrow movement, wrapping, disabled skipping, and DOM focus after every navigation key;
- Home and End;
- roving tabindex;
- selected-disabled, all-disabled, and group-disabled tab-stop behavior;
- group-disabled click, Enter, Space, and synthetic selection attempts emit nothing;
- no duplicate emissions;
- Full detail accessible name;
- Custom selection;
- root/label typography, option alignment/padding/border/radius, default `md`/intrinsic behavior, explicit size/full-width behavior, hover, disabled-opacity ownership, and focus-offset CSS contracts;
- reduced motion;
- 320px and 390px overflow geometry;
- first, middle, and last focus rings remain visually unclipped in a real browser.

### Disclosure tests

Tests prove:

- controlled mode starts from its supplied value;
- click and keyboard request one state change;
- controlled changes do not feed back through native toggle events;
- controlled `open` prop rerenders update the rendered state;
- disabled controlled and store-backed summaries expose `aria-disabled`, leave open state unchanged, leave the summary out of the tab order, and emit no state change;
- reenabling after a prop rerender restores focusability and activation;
- store-backed consumers preserve existing behavior;
- TranscriptDetailEditor starts closed after remount;
- the disabled summary alone owns 0.5 opacity, suppresses hover, and uses a not-allowed cursor while an open body remains fully opaque;
- the `ThemeFlip` gallery contains disabled store-backed and controlled examples.

### Domain/editor tests

Tests prove:

- equivalent explicit Custom remains Custom;
- presets remain presets;
- frontend-to-wire and wire-to-frontend codecs preserve nested-wire versus flat-frontend Custom shapes and explicit Custom kind;
- selecting Custom restores the mounted cache;
- first Custom selection clones the current preset;
- remount resets dormant cache;
- preset/Custom changes preserve Metrics and Diagnostics;
- the six-option selected state always matches the content kind;
- only named Full resets the open baseline;
- Full→equivalent Custom, equivalent Custom→Full, remount, and manual-collapse transitions preserve the specified disclosure behavior.

### Composition tests

Tests prove that transcript surfaces mount shared SegmentedControl, Disclosure, Switch, FormRow, Select, Button, Card, Popover, and Sheet. They also prove that the Detail trigger reports `aria-haspopup="dialog"` and accurate `aria-expanded`, and that the desktop consumer passes `closeOnScroll={false}`. Static/CSS tests reject:

- RadioGroup role-descendant skins;
- Switch geometry overrides;
- raw replacement Select/trigger/action controls;
- duplicate Card/Popover chrome;
- wildcard descendant motion overrides;
- decorative accent bars;
- duplicate Current detail copy;
- the removed critical-row explanation.

Announcement tests prove that passive state uses `status`, retryable load/storage/save failure uses one inline `alert`, acknowledged success uses Toast, and a failure never appears in both Toast and an inline live region.

### Settings and preview tests

Tests cover:

- independent Desktop/Mobile drafts, acknowledgements, errors, and retries;
- the outer `transcript-display-preview-canvas-mobile` border box is the measured width owner: exactly 390px when available and exactly its preview-section width below 390px;
- production TranscriptBody flow;
- no preview RPC or thread-store dependency;
- isolated disclosure state;
- no inner scroll;
- neutral inventory and preview surfaces.

### Real-browser and mutation tests

The production-backed overflow guard adds 320px and retains 390, 899, 900, narrow dock, 1024, and 1400 cases. It measures six segments, option block sizes, the exact 320px and 390px computed widths, viewport containment, the 34rem editor-container transition, phone-preview width, and horizontal/inner scrolling. A real-browser interaction scrolls the open desktop panel and proves that it remains open and viewport-contained.

Before acceptance, one path-scoped temporary mutation in `cmd/evener-hub/frontend/src/widgets/segmentedcontrol/segmentedcontrol.module.css` adds `min-inline-size: var(--tap-min)` to the `(max-width: 899px)` option rule, deliberately reintroducing the rejected 44×44px contract. At 320px, the guard must fail because each option's measured inline size is at least 44px instead of 40px: with the documented grid, the rightmost option's local right edge becomes at least 257px, beyond the track border box's 256px right edge. The mutation is removed, the same named measurements pass, and no mutation remains.

### Visual acceptance

Before the transcript migration is accepted:

1. review the SegmentedControl and updated Disclosure galleries in light and dark themes;
2. review selected, disabled, focus-visible, Custom, 320px, and 390px SegmentedControl states plus disabled controlled/store-backed Disclosure states;
3. capture every listed real-component state in both light and dark themes: Desktop closed/open, Mobile closed/open, Custom selected, and stacked Settings cards with phone-width preview;
4. store corrected proof separately from the defect captures.

### Repository gates

Run:

```bash
make test-web
make test-web-browser
make lint
make vet
make test
```

Every command must exit zero. A missing browser or environmental denial is incomplete, not a pass.

## Compatibility and migration

The wire and durable JSON shapes do not change. Existing preset configurations decode unchanged. Existing Custom configurations remain Custom after frontend normalization, including vectors that equal a preset.

This correction requires no durable migration and no backend rewrite. It changes only frontend interpretation of explicit Custom identity. Legacy preference migration and dual writes continue through the existing configuration adapters.

Because Custom identity now persists, fingerprints and equality tests must include `content.kind`; they must not compare vectors alone.

## Implementation boundaries

The implementation may change:

- shared SegmentedControl files, exports, gallery, tests, and design-system docs;
- shared Disclosure API, styles, gallery, tests, and design-system docs for controlled and disabled modes;
- transcript config normalization/tests;
- TranscriptDetailEditor/Control/tests/styles;
- TranscriptDisplayCard/section/tests/styles;
- preview/harness/browser-guard tests and corrected proof artifacts.

It must not change:

- AppWire methods or payload fields;
- hub persistence or conflict semantics;
- projection rules or critical-row classification;
- transcript store precedence or sync rules;
- unrelated RadioGroup consumers;
- unrelated Settings sections.

## Risks and controls

### Six segments at narrow widths

Six options leave less visible label space. The accepted 320px contract deliberately permits 40px option widths while preserving 44px option height and complete accessible names. The guard rejects wrapping, horizontal scrolling, negative-margin/full-bleed escapes, or block sizes below 44px; it accepts visible-label ellipsis.

### Explicit Custom identity

Preserving explicit Custom changes prior canonicalization. Tests cover both codec directions, flat frontend and nested wire shapes, fingerprints, local/hub acknowledgements, migration, all preset helpers, and the named-Full-only baseline rule. Backend payload shape remains unchanged.

### Shared Disclosure extension

Controlled mode could regress store-backed disclosures if implemented through conditional hooks. The design requires separate controlled and store-backed internals and runs the complete existing Disclosure suite.

### Visual-law drift

Geometry tests cannot prove design consistency. Gallery review in both themes is a required acceptance gate before feature migration.

### Phone-width preview height

A narrower preview may become substantially taller. It remains normal flow with no inner scroll. Browser guards measure card containment and page behavior at every required width.

## Acceptance criteria

The correction is complete only when:

1. SegmentedControl is a documented, exported, gallery-covered widget.
2. The gallery proves every documented state in both themes.
3. The transcript displays six explicit segments, including Custom.
4. Explicit Custom identity survives normalization and round trips.
5. Custom restore-or-clone behavior matches this specification.
6. No feature CSS reaches into RadioGroup, Switch, Button, Select, Disclosure, Card, Popover, or Sheet internals.
7. Shared Switch retains its standard 32×18 visual track.
8. Shared Button, Card, Select, Disclosure, Popover, and Sheet own their chrome.
9. Desktop option buttons use 28px/32px density; Mobile SegmentedControl options remain at least 44px high and may be narrower than 44px.
10. At 320px, the Settings track is 256px wide, each of six segments is 40px wide and at least 44px high, and neither it nor an ancestor scrolls horizontally.
11. Desktop Popover and Mobile Sheet keep correct dialog and focus semantics; the scrollable Popover remains open during internal scrolling.
12. Settings has one intro, no duplicate current-state readout, and no critical-row note.
13. Mobile Settings preview renders production content at `min(390px, 100%)` with no inner scroll and matches its exact computed width.
14. Passive help/inventory/status regions use correct neutral surface roles and no decorative accent bars.
15. Custom stays explicit across normalization and both wire-codec directions; only the named Full preset establishes the reset baseline.
16. SegmentedControl and Disclosure meet every documented disabled, focus, labelling, validation, and controlled-state contract.
17. Browser guards pass at 320, 390, 899, 900, narrow dock, 1024, and 1400.
18. The new guard fails under the required mutation and passes after restoration.
19. Corrected gallery and every listed feature screenshot receive human approval in both light and dark themes.
20. All required repository gates exit zero.
21. Final review reports no open Critical or Important findings.
