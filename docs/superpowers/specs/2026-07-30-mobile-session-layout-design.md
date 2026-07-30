# Mobile session layout — full-bleed shell, title in the top bar, 44px targets, overflow containment

Date: 2026-07-30
Status: approved (brainstorming session; user answers: full bleed, paradigm A, 44px everywhere, wrap prose + contain wide blocks)
Scope: the `<900px` layout only (StackHost and everything it mounts). Desktop DockHost is untouched.

## Intent

The phone layout reads as a desktop card shrunk into a phone frame: the
workspace floats inside a padded frame, the session title sits in a pane
header under an empty top bar, header buttons are 32px desktop targets, and
transcript content escapes its pane horizontally (visible page-level
horizontal scrollbar, prose clipped at the left edge). This spec makes the
mobile layout a native-feeling full-screen app: edge-to-edge content, the
focused pane's title in the top bar, honest touch targets, and a transcript
that wraps or contains anything wide.

## Decisions

1. **Full-bleed shell.** `AppShell.module.css .content` drops its
   `padding`/`gap` inside `@media (max-width: 899px)`; PaneScaffold's `.pane`
   drops its `border`/`border-radius` in the same query. Top bar, transcript,
   and composer span the full screen width; the `--surface-0`/`--surface-1`
   step carries the structure instead of a framed card.

2. **Title channel, paradigm A.** A small chrome store (`useChromeStore`,
   zustand, same pattern as the existing stores) carries `paneTitle`.
   PaneScaffold publishes `mobileTitle ?? title` on mount/title-change and
   clears it on unmount — always, host-agnostic; DockHost never reads it, so
   panes still never ask "am I mobile?". StackHost subscribes and renders the
   title between the back button and the drawer trigger, ellipsized. A pane
   that publishes nothing leaves the bar empty, as today.

3. **The in-pane header is hidden on mobile; header content relocates
   per-pane.** Inventory of what PaneScaffold headers ever carry:

   | Pane | Title | Cadence | Actions |
   |---|---|---|---|
   | Session | session name | liveness sparkline | — |
   | Spawn | "New session" | — | — (the primary Spawn button lives in the form's PromptCard, not the header) |
   | Doc / Transcript | filename / thread | — | ‹ Back to \<session\> |
   | Settings / Welcome | label | — | — |

   - Session **cadence** moves into the SessionChrome footer row.
   - Doc/Transcript **BackToParentAction** goes away with the header: on
     mobile you arrived from the parent, so the top-bar Back already returns
     there. The top-bar title carries the "whose child is this" identity.
   - Settings/Welcome/Spawn need nothing — their headers were title-only
     (Spawn's primary button already lives in the form body's PromptCard
     actions slot, verified in Spawn.tsx during implementation; the
     inventory line in an earlier draft of this table was wrong).

4. **44px touch targets everywhere on mobile.** `tokens.css`'s existing
   `@media (max-width: 900px)` block gains `--tap-min: 44px`. IconButton,
   Button sm/md, and the composer controls (Send, attach, model/reasoning
   chips, overflow) take `min-width`/`min-height: var(--tap-min)` under the
   mobile media query. The top bar grows to fit its buttons (~56px app bar).
   Desktop sizes are unchanged.

5. **Overflow: wrap prose, contain wide blocks.** PaneScaffold's `.body`
   gains `overflow-x: clip` so the pane can never scroll sideways. A
   `min-width: 0` containment chain runs from `.body` through the transcript
   list, `.turn`, the message columns, and the bubble wrappers. Markdown
   `.root` gains `overflow-wrap: anywhere` so long tokens/URLs/inline code
   wrap. Intrinsically wide widgets (CodeBlock, tables) get
   `max-width: 100%` with their own `overflow-x: auto` — they scroll inside
   their block, never the page.

## Non-goals

- Desktop layout, dockview, and the >900px media queries: untouched.
- Reworking the composer's internal layout beyond touch-target sizing.
- A generic actions-overflow menu: rejected (paradigm B) — the only real
  header actions in the app relocate cleanly, and burying Spawn's primary
  button behind a menu is worse.

## Edge cases

- **Long titles** ellipsize in the top bar (the existing `.title`
  truncation grammar, reused).
- **Breakpoint crossing mid-session**: PaneScaffold's unmount cleanup clears
  the chrome store, so no stale title survives into DockHost.
- **Sub-page back chevrons inside content** (Settings nav-as-page) are body
  content, not header chrome — unaffected by the hidden header.

## Acceptance

- `npm run overflowguard` (390/700/1024/1400 sweep) reports no page-level
  horizontal overflow.
- Full frontend gate green: `tsc` clean, vitest suites passing (new tests
  for the chrome store publish/clear, StackHost title rendering, cadence
  relocation, Spawn footer; CSS-module assertion tests for the new media
  rules in the repo's existing pattern), `biome ci` clean.
- Visual: at 390px the session view is edge-to-edge, the top bar reads
  back + title + drawer with 44px targets, and long prose/code never escapes
  the pane.
