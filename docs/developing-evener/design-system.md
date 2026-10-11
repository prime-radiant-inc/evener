# Building hub web UI with the design system

How to build or change UI in the hub frontend (`cmd/evener-hub/frontend/src`)
so it stays on the design system. The design law itself (why the palette,
type, motion, and widget contracts are what they are) lives in
[docs/web-ui/design-system.md](../web-ui/design-system.md); this page is the
working procedure.

## Where the truth lives

- **Tokens:** `src/styles/tokens.css`. Every color, space, radius, type
  size, z-index, shadow, and motion duration the app uses is a custom
  property declared there, in both themes.
- **Widgets:** `src/widgets/`, imported only through the barrel
  `src/widgets/index.ts`. The props each widget accepts are listed in §3 of
  the design-system spec and typed in each widget's `index.tsx`.
- **Galleries:** run the frontend dev server (`npm run dev` in
  `cmd/evener-hub/frontend`) and open `/dev/widgets` (every widget, every
  state, both themes), `/dev/surfaces` (composed surfaces), and `/dev/type`
  (the type ramp). They exist only in dev builds.

## The rules

1. **Compose from widgets.** A button is `Button` or `IconButton`, a text
   field is `Input` or `Textarea`, a choice is `Select`, `SegmentedControl`,
   `RadioGroup`, or `Switch`, a count is `Badge`, a label pill is `Chip`, an
   empty pane is `EmptyState` (`size="compact"` inside a list or section), a
   modal is `Dialog`/`Sheet`/`ConfirmDialog`. An inline status, hint, or
   error is `InlineNotice`; a list row is `ListRow`; an uppercase group label
   is `Eyebrow`; label/value facts are `PropertyList`; a shortcut reference
   is `ShortcutList`; an inline text action is `Button variant="link"`;
   screen-reader-only text is `VisuallyHidden`.
   Do not write a raw `<button>`, `<input>`, `<select>`, or `<textarea>`
   outside `src/widgets/`, and do not write a module class that re-creates a
   widget (`.chip`, `.card`, `.empty`, `.notice`, `.srOnly`, ...).
2. **Import from the barrel.** `import { Button } from "../../widgets"`,
   never `widgets/button` or anything under `widgets/internal/`.
3. **Never style a widget's insides.** No descendant, `:global`, or
   attribute selectors that reach into a widget's DOM, and no wrapper div
   whose only job is overriding a widget's look. If a widget needs to look
   different in your context, that is a widget change (below), not a local
   override. Layout around a widget (grid placement, gaps, alignment) is
   yours; the widget's own box is not.
4. **Tokens only.** Write `var(--name)` for every color, space, radius,
   font size, weight, z-index, shadow, and duration. Color literals fail CI
   (`src/styles/token-contract.test.ts`). Each attention hue
   (`--attention`, `--alive`, `--danger`, `--accent`) has exactly one
   meaning; using one outside its meaning is a bug.
5. **Use the right widget for the state.** Loading is `Skeleton` or
   `Loader`, not an `EmptyState` titled "Loading…". Destructive
   confirmation is `ConfirmDialog`, not a hand-built dialog.
6. **Session and agent status is always `StatusDot`.** Its labels (Idle,
   Working, Needs you, Failed, Ended) are the one status vocabulary; text
   naming a session's state uses `STATUS_LABEL`. Do not hand-roll a status
   dot or spinner: StatusDot's `working` state already carries the activity
   motion, gated by reduced motion.

## When no widget fits

Work down this list and stop at the first step that holds:

1. **A widget fits as-is.** Use it. Visual drift from what you pictured is
   not a reason to hand-roll; the widget's look is the system's look.
2. **A widget almost fits.** Extend it: add the prop or variant to the
   widget, and only if a second call site needs it or it fixes a real misuse.
   A one-site need usually means the design should bend to the widget
   instead.
3. **The pattern recurs and no widget covers it.** Add a new widget.
4. **It is genuinely bespoke** (transcript grammar like `ToolRunGroup`, a
   composite with its own roving focus, a drag surface). Keep it local and
   say why in a comment at the definition.

Raise steps 2 and 3 with Jesse before building them: the widget API is
locked and documented, so changing it is a design decision.

## Adding or extending a widget

A widget is `src/widgets/<name>/` holding `index.tsx`, `<name>.module.css`,
and `<name>.test.tsx`. Every widget change lands with all of:

- the barrel export in `src/widgets/index.ts`;
- a gallery section `src/dev/gallery-sections/<name>.tsx` showing every
  documented state (`src/dev/WidgetGallery.test.tsx` fails if a widget has
  none);
- its row in §3 of [docs/web-ui/design-system.md](../web-ui/design-system.md),
  updated to the shipped props;
- tests that drive the widget through its role and accessible name.

`Button` is the exemplar for file layout, CSS, and test style. A widget that
sets one of the attention hues must be on the token-contract allowlist; see
the comment above `SEMANTIC_USE_ALLOWLIST` in `token-contract.test.ts`.

## Checking your work

- `node scripts/ds-audit.mjs` (from `cmd/evener-hub/frontend`) reports raw
  controls and re-implemented widget classes outside `src/widgets`, plus
  literal values that bypass tokens. `--files bypass` lists the sites. A
  change should never raise these counts.
- `make test-web` runs the token contract, the gallery completeness test,
  typecheck, and Biome.
- Look at your change in `/dev/widgets` or the real pane in both themes.

[docs/web-ui/widget-adoption-catalog-2026-10-11.md](../web-ui/widget-adoption-catalog-2026-10-11.md)
records every site that bypassed the widget library as of that date, with
the planned fix for each.
