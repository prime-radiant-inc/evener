---
name: Evener Native Board
description: The Board, the native iPhone app's home: every live session ordered by who needs you, then the user's own organization.
colors:
  light-background: "#FAF9F6"
  light-surface: "#F4F3EE"
  light-text: "#252521"
  light-secondary: "#5F5F57"
  light-border: "#DDDCD4"
  light-accent: "#0064C2"
  light-accent-fill: "#0070E0"
  light-error: "#C51D23"
  dark-background: "#191918"
  dark-surface: "#20201E"
  dark-text: "#F2F1EB"
  dark-secondary: "#B0AFA6"
  dark-border: "#34342F"
  dark-accent: "#459EFF"
  dark-accent-fill: "#0070E0"
  dark-error: "#F17478"
typography:
  board-title:
    fontFamily: "SF system, system-ui, sans-serif"
    fontSize: "17px"
    fontWeight: 600
    lineHeight: "22px"
  why-line:
    fontFamily: "SF system, system-ui, sans-serif"
    fontSize: "15px"
    lineHeight: "20px"
  last-line:
    fontFamily: "SF system, system-ui, sans-serif"
    fontSize: "13px"
    lineHeight: "18px"
  age:
    fontFamily: "SF system, system-ui, sans-serif"
    fontSize: "13px"
  band-header:
    fontFamily: "SF system, system-ui, sans-serif"
    fontSize: "13px"
    fontWeight: 600
    letterSpacing: "0.4px"
  body:
    fontFamily: "Source Serif 4, Georgia, serif"
    fontSize: "17px"
    lineHeight: "26px"
rounded:
  input: "9px"
  action: "24px"
  chip: "16px"
spacing:
  content-inset: "16px"
  mark-column: "28px"
  row-gap: "10px"
  title-inset: "54px"
  signal-row-min-height: "64px"
  quiet-row-min-height: "48px"
  action-target: "44px"
components:
  board-row:
    textColor: "{colors.light-text}"
    typography: "{typography.board-title}"
    height: "{spacing.signal-row-min-height}"
  band-header:
    textColor: "{colors.light-secondary}"
    typography: "{typography.band-header}"
  chip:
    backgroundColor: "{colors.light-surface}"
    textColor: "{colors.light-text}"
    rounded: "{rounded.chip}"
    height: "32px"
---

# Design System: Evener Native Board

## Overview

**Creative North Star: "Attention is the product"** (spec principle 1)

This records the shipped Board, the native iPhone app's home. It replaces the project-first Sessions browser: every live session is ordered by who needs you, then the user's own organization follows. The Board uses native navigation, SF Pro typography, semantic light and dark surfaces, and touch-sized controls. The full design is `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`, section 7; this phase's plan is `docs/superpowers/plans/2026-09-26-iphone-redesign-phase2-board.md`.

The Board is one native scrolling list: Live's bands, then pinned categories, Projects and Archived as plain section rows that open their own screens until a later PR brings them inline.

**Key Characteristics:**
- Every live session in one list, ordered by who needs you, then finished, then working, then idle.
- Calm: a control appears only when it can act, and nothing moves unless its data moved.
- Native actions, an automatic connection, and per-device state for what you've seen and which sections you folded.

## Colors

The implementation switches between light and dark palettes through the native color scheme. The values live in `src/design/tokens.ts`, which follows the redesign spec (`docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`, section 16.1); change them there first.

### Primary
- **Accent** (`#0064C2` light, `#459EFF` dark): Search, links and selected controls. Primary buttons fill with accent-fill (`#0070E0` in both themes) and carry white text.

### Neutral
- **Paper background** (`#FAF9F6` light, `#191918` dark): Main reading and browsing canvas.
- **Soft surface** (`#F4F3EE` light, `#20201E` dark): Inputs and raised native-looking controls.
- **Primary text** (`#252521` light, `#F2F1EB` dark): Project and session content.
- **Secondary text** (`#5F5F57` light, `#B0AFA6` dark): Counts, metadata, and quiet state explanations.
- **Quiet border** (`#DDDCD4` light, `#34342F` dark): Sparse row and input boundaries.
- **Semantic error** (`#C51D23` light, `#F17478` dark): Read and action failures.

### Named Rules
**The Quiet Idle Rule.** Omit idle status noise from session rows; reserve state text for working, questions, warnings, and failures.

## Typography

Dimensions below describe React Native logical units (points on iOS). The `px` values in the serialized tokens support documentation previews; they are not physical screen pixels or CSS used by the app.

**Display Font:** SF system (with the platform system fallback)
**Body Font:** Source Serif 4 for conversation prose (agent prose 17/26, your messages 17/25); headings inside a reply are SF Pro semibold at 20/17/15, and code is Menlo. See `typeRoles` in `src/design/tokens.ts`, which follows the redesign spec (`docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`, section 16.2).

**Character:** A compact hierarchy gives the row title priority, the why line room to explain itself, and metadata the smallest, quietest type.

### Hierarchy
- **Title** (SF Pro semibold, 17px, 22px line-height): Row title, one line (two for Needs you).
- **Why line** (SF Pro, 15px, 20px line-height): The state word and reason for Needs you, or the current activity for Working. Finished rows carry no why line yet: the spec's last-message excerpt (Source Serif 15/21) waits for a server addition (S1).
- **Last line** (SF Pro, 13px, 18px line-height, ink-low): The project (with a folder glyph) and the host (with a server glyph), each only when it differs from the fleet's usual one. Task progress and subagent failures join this line once the hub sends them (S13 and S3); until then the row has no last line when its project and host are the usual ones.
- **Age** (SF Pro, 13px, tabular figures, ink-low): Trailing on line 1: "2m", "1h", "3d".
- **Band header** (SF Pro semibold, 13px, uppercase, +0.4pt tracking, ink-mid): "NEEDS YOU · 4", "FINISHED · 4", "WORKING · 9".

## Layout

The Board is one native scrolling list. Each row reserves a 28-unit leading mark column and a 10-unit gap to its content, inset 16 logical units on each side. Hairline separators sit inset 54 units, clear of the mark, to the title (16 padding + 28 mark + 10 gap). Signal rows (Needs you, unseen Finished, and Working, in the Live section) run about 64 to 88 units depending on how many lines they carry; quiet rows (Idle's, today) hold to 48 units. Every tappable target keeps a native minimum of 44 units; the section chips (32 units) and the summary line's counts (30 units) draw smaller and reach 44 with hit slop.

## Elevation & Depth

The Board uses tonal layering and sparse hairlines rather than shadows, gradients, bevels, or decorative cards. Rows are defined by spacing and a single hairline, not a border box. The one exception is the first-load skeleton, whose three placeholder rows use a soft rounded fill to read as content still arriving, never as a card.

### Named Rules
**The Flat Board Rule.** Use background, surface, spacing, and sparse hairlines to convey structure; do not add decorative elevation to ordinary rows.

## Shapes

Board rows and section rows stay open on the page; no enclosing card. Section chips are fully rounded capsules (16-unit radius on a 32-unit-tall chip). The search field keeps its gently rounded 9-unit corner. Primary action pills (New session, in the empty state) use the native action treatment with a 24-unit radius. Every action preserves a native minimum target of 44 units on iOS.

## Components

### The hub button
- **Shape:** One glass capsule in the header's leading slot: the hub's name at 17px in ink-hi and a `chevron.down` glyph. It's a custom header view, because a native bar item given both a title and an image draws only the image.
- **Behavior:** Opens an action sheet titled with the hub's name: Hub settings (disabled while the hub is out of reach) and Switch hub. Phase 5's Hub sheet replaces it.

### Section chips
- **Shape:** A horizontal row, sticky under the header; fully rounded capsules with a hairline border. The row fades at its trailing edge, so a cut-off chip reads as "there's more."
- **Typography:** Name semibold 14px; count 14px medium, tabular, in ink-low.
- **Behavior:** Live (badged with the Needs you count, in amber, when it is above zero), one chip per pinned category with a pin glyph, Projects, and Archived, each with a count. A section with nothing in it has no chip. Tapping a chip scrolls to that section.

### The Live summary line
- **Shape:** One wrapped line above the bands.
- **Typography:** 14px, 20px line-height; the Needs you count is semibold in attention-ink, the others are regular in ink-mid.
- **Behavior:** Shows only when at least two bands have sessions. Carries the fleet pulse meter (section below) before "working," gray while the connection is down. Tapping a count jumps to its band; jumping to Idle unfolds it first.

### Band headers
- **Shape:** Open text, no rule or card.
- **Typography:** 13pt semibold, uppercase, ink-mid, +0.4pt letter-spacing.
- **Behavior:** "NEEDS YOU · 4", "FINISHED · 4", "WORKING · 9". A band with nothing in it has no header.

### Board rows (signal and quiet)
- **Shape:** Draw no separator of their own; the list draws hairlines inset to the title (54 units: 16 padding + 28 mark + 10 gap). Signal rows (Needs you, unseen Finished, and Working, in Live) run 64 to 88 units; quiet rows (Idle's, today) hold to 48 units and show only the title and age.
- **Typography:** Title semibold 17/22 (two lines for Needs you); why line 15/20, with the state word semibold in its hue when one applies; last line 13/18 in ink-low; age 13pt tabular figures in ink-low.
- **Behavior:** A small blue "Draft" tag sits before the age when the session has an unsent draft. Tapping a row opens its session.

### State marks and the pulse meter
- **Shape:** A 28-unit leading mark column, shared by every row.
- **Marks:** SF Symbols through `SymbolView`, always pairing shape with color so they read without it: `xmark.octagon.fill` (Failed, danger), `questionmark.circle.fill` (Question, attention), `hand.raised.circle.fill` (Approval, attention), `exclamationmark.triangle.fill` (Warning, attention), `arrow.triangle.2.circlepath.circle.fill` (Restart needed, attention), and a small `circle.fill` dot (Finished, accent, 8pt).
- **Pulse meter:** A 22×15pt, 7-bar activity meter, shown on Working rows in the Live band. One fixed log scale (full at 64 events per minute) so meters compare across sessions; a 1pt baseline always draws, older bars fade, and the meter grays when the connection is down. Until the hub reports per-minute activity, a working row shows a single bar in its newest minute. The row takes a `moving` flag so a still copy of the same session elsewhere shows a plain dot instead, once a later PR renders sessions inline outside Live.

### The Idle fold
- **Shape:** A single 48-unit row: "Idle · 3" in ink-mid at 15px (title case, not the band headers' uppercase), with a chevron that rotates 90° when open.
- **Behavior:** Folded by default; the fold state persists per device. Unfolding, directly or by jumping from the summary line, reveals Idle's quiet rows.

### Section rows
- **Shape:** Open 48-unit rows with a top hairline and a trailing chevron.
- **Behavior:** One per pinned category (with a pin glyph, its name and count), even an empty one, since a category is a place; then Projects and Archived when they hold anything. Each opens its own screen. This is a placeholder for the inline treatment a later PR brings.

### The bottom toolbar and its connection status
- **Shape:** A 50-unit bar on the page color with a top hairline.
- **Behavior:** The center shows the connection status only when it is not live: "Reconnecting…" once the connection has been down 2 seconds, "Offline · updated 3m ago" once it has been down 30 seconds, or "Update needed" at once, and never "Reconnecting…", when the hub speaks an incompatible protocol version. A live connection shows nothing there. Trailing is New session, a 44-unit `square.and.pencil` glyph in accent-ink, disabled (not hidden) while disconnected.

### The notice row
- **Shape:** Sits on the page like a row: a mark, one sentence, a bottom hairline.
- **Behavior:** Today's only notice is the incompatible-version warning, "This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub.", shown with an amber `exclamationmark.triangle.fill` mark. The row takes no action of its own; the toolbar's status carries the matching "Update needed" wording.

### Skeleton, empty and failed-first-read states
- **Skeleton:** Three still placeholder rows, 64 units tall with a 10-unit rounded fill, on the very first load only.
- **Empty:** "Nothing's running. Start a session to put an agent to work.", with a primary New session action, shown when Live has no rows.
- **Failed first read:** "Couldn't load this hub's sessions. Trying again shortly.", shown when the very first read of Live fails. Any failed read (Live, Needs you, the pin catalog or the manifest) is retried on its own with a growing backoff while the Board is in view, and when it comes back into view; there is no Retry button.

### Message actions
- **Style:** A three-dot 44-unit action beside eligible user messages.
- **Behavior:** iOS uses the native `ActionSheetIOS` action surface with the selected message title, Fork from here, and Cancel. The Fork preview is read-only; explanatory copy says the created fork opens with an editable draft.

## Do's and Don'ts

### Do:
- **Do** order every live session by who needs you, then finished, then working, then idle.
- **Do** keep a control mounted and disabled, rather than hiding it, while it can't act.
- **Do** use human readable state words, and copy that says exactly what happened.
- **Do** persist each section's collapsed state per device.

### Don't:
- **Don't** show a Reconnect or a Refresh control anywhere on the Board, or offer pull-to-refresh: a control appears only when it can act.
- **Don't** move or animate a row unless its underlying data moved.
- **Don't** give a hue more than its one job: amber means a human is needed, green means working, red means failed, blue means tappable, selected or unread. Only the state word takes a hue; the reason stays in ink.
- **Don't** wrap every ordinary row in a rounded card or add decorative shadows.
