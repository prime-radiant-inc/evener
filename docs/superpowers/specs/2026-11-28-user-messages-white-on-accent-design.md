# User Messages: White on Accent Across Platforms

Status: **Approved** (2026-11-28; visual mockups reviewed at `https://artifacts.trout-rigel.ts.net/p/evener-user-message-accent-mockups/`)

## Intent and Why

Delivered user messages in Evener must render as white text on the brand accent color on every color client (Web Desktop, Web Mobile, and Native iOS/Android) in both light and dark themes. Previously, web rendered delivered user prose directly on the page surface and native used a pale 15% accent wash (`#E1E8FF` light / `#2B3041` dark) with dark/light reading ink, making user turns blend into the surrounding transcript.

## Goals, Non-Goals, and Anti-Goals

- **Goals:**
  - Promote the existing brand fill pair `accent-fill` (`#4F5BF5`) and `on-fill` (`#FFFFFF`) into the shared web+native palette (`@evener/appwire-client/palette`) and `tokens.css` so both themes on every client render genuine white-on-accent with >=4.5:1 text contrast (5.06:1) and >=3:1 non-text separation from both light (4.72:1) and dark (3.70:1 / 3.23:1) grounds.
  - Web (`UserMessageItem` / `usermessageitem.module.css`): render delivered `data-speaker="user"` messages (including mid-turn user steers and manager inputs) inside a `fit-content` rounded indigo bubble with white text, keeping the existing left-aligned speaker header and mobile grid placement.
  - Web rich content inside user bubbles: keep markdown prose, headings, lists, links, inline code, blockquotes, tables, `EntityRef` triggers, inline `OpenButton` actions, focus rings, and text selection legible on indigo, while keeping embedded self-surfaced blocks (`CodeBlock`, `MermaidDiagram`, narrow-viewport reflowed table cards, `ImageGallery`, and portaled `HoverCard` popups) on their own neutral surfaces with page ink.
  - Native (`TimelineItem` / `design/tokens.ts` / `ui.tsx`): alias `palette.bubble` to `accent-fill` and set `YourMessage` text color to `palette.onFill`, keeping its right-aligned bubble geometry.
  - Keep evergreen docs (`docs/web-ui/design-system.md`, `mobile-native/DESIGN.md`, `appwire-client/typescript/README.md`) and tests synchronized.
- **Non-Goals:**
  - Changing Playdate (1-bit monochrome LCD in a separate repository) or the noninteractive CLI.
  - Changing web message alignment (keeps the left-aligned Slack-lean speaker row) or removing native right-alignment.
  - Adding new props to locked web widgets (`Button`, `IconButton`, `OpenButton`, `Markdown`).
- **Anti-Goals:**
  - Flipping dark mode user messages to dark text on pale `#8A96FF` (`--accent`/`--on-accent`) or putting white text on `#8A96FF` (fails WCAG AA at 2.66:1).
  - Turning non-user `UserMessageView` callers (`delegate_send`, `speaker="agent"`) indigo.
  - Swallowing the dashed pending/provisional state on web (`PendingRowStack`) or native (`GhostBubble`).
  - Turning native `NoteRow` (`You updated your note` on bare page) white.
  - Reaching into widget DOM with descendant or `:global` selectors.

## Constraints

- One shared color palette in `appwire-client/typescript/palette.ts`; `tokens.css` and `mobile-native/src/design/tokens.ts` stay drift-checked against it (`token-contract.test.ts` and `mobile-native/src/design/tokens.test.ts`).
- Web styling uses design tokens only, no color literals outside `tokens.css`, no raw focus-ring geometry, and no widget-internal overrides (`docs/developing-evener/design-system.md`).
- Host re-tinting of `Markdown`, `EntityRef`, `Button` quiet variant, and `::selection` uses CSS custom-property hooks with declared-token fallbacks, with self-surfaced nested blocks (`CodeBlock`, narrow reflowed table rows, `ImageGallery`) resetting those hooks back to neutral surface tokens.

## Approaches Considered

1. **Promote `accent-fill` (`#4F5BF5`) and `on-fill` (`#FFFFFF`) to shared web+native tokens (Chosen):** Reuses the exact brand indigo pair already defined in `palette.ts` for native filled controls. Gives identical white-on-accent appearance in light and dark themes across web and native while clearing WCAG AA (5.06:1 text, >=3.23:1 fill separation).
2. **Use web's existing `--accent` and `--on-accent`:** Rejected because dark `--accent` (`#8A96FF`) pairs with near-black `--on-accent` (`#111214`), violating the white-on-accent requirement in dark mode.
3. **Put white text on dark `--accent` (`#8A96FF`):** Rejected because contrast drops to 2.66:1, failing WCAG AA and both clients' token-contrast gates.

## Decisions Made

- **Shared palette:** Move `accent-fill` and `on-fill` from `NativeExtras` into `WebPalette` in `appwire-client/typescript/palette.ts`; declare `--accent-fill: #4F5BF5` and `--on-fill: #FFFFFF` in both dark and light blocks of `cmd/evener-hub/frontend/src/styles/tokens.css`.
- **Web user bubble:** Scope the indigo bubble strictly to `.message[data-speaker="user"]:not(.provisional) .body` (`align-self: flex-start; justify-self: start; width: fit-content; max-width: 100%; padding: var(--space-2) var(--space-3); background: var(--accent-fill); border: 1px solid transparent; border-radius: var(--radius-chip) var(--radius-pane) var(--radius-pane) var(--radius-pane); box-shadow: var(--shadow-card); color: var(--on-fill);`). Keep base `.body` neutral for `speaker="agent"` (`delegate_send`), and keep `.provisional .body` on `--surface-1` with its dashed `--accent` border.
- **Web nested content legibility:** Use CSS custom-property hooks with neutral fallbacks (`--markdown-ink`, `--markdown-muted`, `--markdown-link`, `--markdown-edge`, `--markdown-edge-strong`, `--markdown-hover`, `--markdown-focus-ring`, `--quiet-ink`, `--quiet-hover`, `--quiet-active`, `--quiet-focus-ring`, `--selection-surface`, `--selection-ink`, `--mermaid-surface`, `--mermaid-radius`, `--mermaid-pad`) set on the delivered user `.body`, with self-surfaced nested containers (`CodeBlock .root`, `Markdown .tableReflow tbody tr`, `ImageGallery .thumb`) resetting hooks back to neutral surface tokens and `MermaidDiagram .svg` painting a neutral `--surface-1` well inside user bubbles.
- **Native user bubble:** Alias `NATIVE_ALIASES.bubble` to `"accent-fill"` in `mobile-native/src/design/tokens.ts`, add optional `color?: string` to `Copy` in `mobile-native/src/ui.tsx`, and pass `color={palette.onFill}` in `YourMessage` (`mobile-native/src/TimelineItem.tsx`), leaving `NoteRow` on `palette.prose` and `GhostBubble` unfilled dashed.

## Left to the Builder

- Exact test organization within existing test files (`token-contract.test.ts`, `UserMessageItem.test.tsx`, `transcript-guard.ts`/`shared-assert.mjs`, `mobile-native/src/design/tokens.test.ts`, `mobile-native/src/TimelineItem.test.tsx`).
- Exact wording of evergreen doc updates matching each document's present-tense style.
