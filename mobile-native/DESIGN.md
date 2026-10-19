---
name: Evener Native
description: "The native iPhone app: the Board orders live sessions by who needs you, and the Session is one conversation with its agent."
colors:
  light-background: "#FFFFFF"
  light-surface: "#EDEDF2"
  light-text: "#1C1C1E"
  light-secondary: "#55555E"
  light-border: "#D8D8DF"
  light-accent: "#0064C2"
  light-accent-fill: "#0070E0"
  light-error: "#C51D23"
  light-raised: "#FFFFFF"
  light-canvas: "#F2F2F7"
  light-attention-ink: "#9C4908"
  light-quiet-ink: "#62626D"
  light-prose: "#1C1C1E"
  light-pressed: "#E5E5EA"
  light-strong-border: "#B8B8C1"
  light-on-fill: "#FFFFFF"
  light-attention: "#F59E0B"
  light-attention-bg: "#FFF1E2"
  light-attention-edge: "#E6C3A0"
  light-alive: "#189A4D"
  light-alive-ink: "#12763B"
  light-danger: "#E3474C"
  light-accent-mark: "#0285FF"
  light-accent-bg: "#DFEEFF"
  light-accent-edge: "#94BAEF"
  dark-background: "#111113"
  dark-surface: "#252528"
  dark-text: "#F5F5F7"
  dark-secondary: "#B8B8C1"
  dark-border: "#38383D"
  dark-accent: "#459EFF"
  dark-accent-fill: "#0070E0"
  dark-error: "#F17478"
  dark-raised: "#1C1C1E"
  dark-canvas: "#08080A"
  dark-attention-ink: "#F68F3C"
  dark-quiet-ink: "#A5A5B0"
  dark-prose: "#E3E3E8"
  dark-pressed: "#303034"
  dark-strong-border: "#56565F"
  dark-on-fill: "#FFFFFF"
  dark-attention: "#F68F3C"
  dark-attention-bg: "#382C26"
  dark-attention-edge: "#805B45"
  dark-alive: "#3DBB72"
  dark-alive-ink: "#3DBB72"
  dark-danger: "#EE5C61"
  dark-accent-mark: "#3D9AFF"
  dark-accent-bg: "#232D3B"
  dark-accent-edge: "#405E86"
typography:
  your-message:
    fontFamily: "Source Serif 4, Georgia, serif"
    fontSize: "17px"
    lineHeight: "25px"
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
    fontSize: "15px"
    fontWeight: 600
    lineHeight: "20px"
  body:
    fontFamily: "Source Serif 4, Georgia, serif"
    fontSize: "17px"
    lineHeight: "26px"
  document:
    fontFamily: "Source Serif 4, Georgia, serif"
    fontSize: "18px"
    lineHeight: "28px"
  machine:
    fontFamily: "Menlo, monospace"
    fontSize: "13px"
    lineHeight: "18px"
  group-label:
    fontFamily: "SF system, system-ui, sans-serif"
    fontSize: "15px"
    fontWeight: 600
    lineHeight: "20px"
  sheet-row:
    fontFamily: "SF system, system-ui, sans-serif"
    fontSize: "17px"
    lineHeight: "22px"
  sheet-value:
    fontFamily: "SF system, system-ui, sans-serif"
    fontSize: "15px"
    lineHeight: "20px"
  sheet-footer:
    fontFamily: "SF system, system-ui, sans-serif"
    fontSize: "13px"
    lineHeight: "18px"
rounded:
  input: "9px"
  action: "24px"
  chip: "16px"
  group: "12px"
  segmented: "12px"
  segment: "10px"
  search: "10px"
spacing:
  content-inset: "16px"
  mark-column: "28px"
  row-gap: "10px"
  title-inset: "54px"
  signal-row-min-height: "64px"
  quiet-row-min-height: "48px"
  action-target: "44px"
  group-inset: "16px"
  footer-inset: "32px"
  sheet-row-min-height: "44px"
  search-target: "44px"
  composer-target: "44px"
components:
  board-row:
    textColor: "{colors.light-text}"
    typography: "{typography.board-title}"
  band-header:
    textColor: "{colors.light-text}"
    typography: "{typography.band-header}"
  chip:
    backgroundColor: "{colors.light-raised}"
    textColor: "{colors.light-text}"
    rounded: "{rounded.chip}"
  ghost-bubble:
    textColor: "{colors.light-secondary}"
    typography: "{typography.your-message}"
    rounded: "18px"
  ask-dock:
    backgroundColor: "{colors.light-raised}"
    textColor: "{colors.light-text}"
    rounded: "12px"
  sheet-group:
    backgroundColor: "{colors.light-raised}"
    textColor: "{colors.light-text}"
    typography: "{typography.sheet-row}"
    rounded: "{rounded.group}"
  group-label:
    textColor: "{colors.light-secondary}"
    typography: "{typography.group-label}"
  segmented:
    backgroundColor: "{colors.light-surface}"
    textColor: "{colors.light-text}"
    rounded: "{rounded.segmented}"
  search-field:
    backgroundColor: "{colors.light-surface}"
    textColor: "{colors.light-text}"
    typography: "{typography.sheet-row}"
    rounded: "{rounded.search}"
  button-primary:
    backgroundColor: "{colors.light-accent-fill}"
    textColor: "{colors.light-on-fill}"
    typography: "{typography.sheet-row}"
    rounded: "25px"
    padding: "6px 20px"
  sheet-status:
    textColor: "{colors.light-secondary}"
    typography: "{typography.sheet-footer}"
---

# Design System: Evener Native

## Overview

**Creative North Star: "Attention is the product"** (spec principle 1)

This records the native iPhone implementation, not visual or release acceptance. The Board is home: every live session is ordered by who needs you, then the user’s own organization follows. The settled direction is a quiet native instrument: neutral light/dark surfaces, SF interface text, familiar native navigation and touch-sized controls. The current visual direction is `docs/superpowers/specs/2026-10-19-native-apple-design.md`; the interaction foundation remains `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`.

The Board is one native scrolling list: Live’s bands, then pinned categories and inline expandable Projects, Test runs and Archived sections when present. Category and project disclosures reveal their rows in place; they are not placeholder links to separate screens.

A row opens the Session, one conversation with its agent, recorded in "The Session" below. The Board's hub button opens the Hub, and its New session button opens New session: two sheets recorded in "The Sheets: New session and the Hub" at the end. The colors, type and shapes that follow serve all three.

**Key Characteristics:**
- Every live session in one list, ordered by who needs you, then working, then idle.
- Calm: unavailable actions keep each surface’s established disabled-or-hidden behavior, and nothing moves unless its data moved.
- Native actions, an automatic connection, and per-device state for what you've seen and which sections you folded.

## Colors

The implementation follows the phone’s appearance unless Display selects Light or Dark. `src/design/tokens.ts` owns the native palette; the frontmatter records its values. Shared web/native identity means the same semantic roles, not identical surface hex values. `useColors()` in `src/ui.tsx` retains compatibility aliases: its `surface` is `palette.inset`, and its `accent` is `palette.accentInk`; the guide’s `raised` tokens are `palette.surface`.

### Primary
- **Accent ink:** Links, actions and checks. Accent-fill with on-fill text is the primary button pair; the brighter accent mark identifies interactive or unread state.

### Neutral
- **Page:** White in light appearance, near-black in dark; the reading and Board ground.
- **Inset surface:** A neutral gray well for fields and compact controls, distinct from the grouped canvas.
- **Raised surface:** White in light appearance and charcoal in dark; grouped rows, the composer and ask dock.
- **Grouped canvas:** Pale gray in light appearance and the darkest neutral in dark; behind sheet groups and their headers.
- **Primary, secondary and quiet ink:** Content, supporting explanations, then metadata. Long-form prose has its own softer dark ink.
- **Edges and pressed surface:** Sparse separators, stronger field/ghost outlines, and a neutral pressed state; not decoration.

### Semantic states
- **Attention:** Amber marks a need for the person. Attention ink carries state words, offline hosts and expired sign-in; attention-bg and attention-edge tint the surfaces that need them.
- **Alive:** Green marks real work, including the activity meter; it is not the switch tint.
- **Danger:** Red marks failure and destructive action; danger ink is the readable text variant.
- **Accent tint:** Blue selection and message backgrounds come from the native palette’s OKLab mix, not a web color copied by hand. Semantic backgrounds mix the hue at 15% over the native raised surface; semantic edges mix it at 40% over the native edge.

### Named Rules
**The Quiet Idle Rule.** Omit idle status noise from session rows; reserve state text for working, questions, warnings, and failures.

## Typography

Dimensions below describe React Native logical units (points on iOS). The `px` values in the serialized tokens support documentation previews; they are not physical screen pixels or CSS used by the app. The sidecar’s HTML, SVG approximations and generated tonal strips are illustrative browser aids, not native captures or additional palette tokens.

**Interface Font:** SF system (with the platform system fallback), not a decorative display face
**Body Font:** Source Serif 4 by default for conversation prose (agent prose 17/26, your messages 17/25) and documents (18/28), with the existing Sans reading preference preserved. Headings inside a reply use SF Pro semibold at 20/17/15; machine evidence uses Menlo (13/18). See `typeRoles` in `src/design/tokens.ts`. Serialized family stacks are browser-preview fallbacks; the iPhone embeds the Source Serif 4 faces and uses their PostScript names.

**Character:** A compact hierarchy gives the row title priority, the why line room to explain itself, and metadata the smallest, quietest type.

### Hierarchy
- **Title** (SF Pro semibold, 17/22): Board titles allow two lines in signal rows and one in quiet rows at ordinary text sizes. Above a 1.2 text scale, titles have no line cap and age/unread metadata moves below them.
- **Why line** (SF Pro, 15px, 20px line-height): The state word and reason for Needs you, or the current activity for Working: the intent of the newest tool call the session itself made ("Reading the board's row tests."), else the job the session is running, else the bare "Working".
- **Last line** (SF Pro, 13/18, ink-low): A wrapping metadata row: task progress with a checklist symbol, project and host when they differ from the usual fleet context, and the model when the existing Display preference requests it. Each fact can wrap rather than consume the title’s width.
- **Age** (SF Pro, 13pt, tabular figures, ink-low): Trailing beside the title at ordinary sizes, below it at large text: "2m", "1h", "3d".
- **Section headings** (SF Pro semibold, 15/20, no added tracking): Sentence case for authored headings such as "Needs you · 4", "Working · 9" and "Where". Board bands use ink-hi; grouped sheets and model section labels use ink-mid. Machine labels remain as typed.

### Named Rules
**The Sentence-case Section Rule.** Use the shared untracked 15/20 section role for authored section headings; preserve user and machine names as typed.

**Navigation text:** The fixed-height bar caps title and Back text at `23 / 17` of the default size (`src/headerText.ts`). Session title, Back count and the separate live-jump button expose the iOS Large Content Viewer and full accessibility labels. Body text and grouped rows retain their own Dynamic Type scaling; the header cap is not a general text-size limit.

## Layout

The Board is one native scrolling list. Each row reserves a 28-unit leading mark column and a 10-unit gap to its content, inset 16 logical units on each side. Hairline separators start at the title, inset 54 units (16 + 28 + 10). Signal rows have a 64-unit minimum; quiet rows have a 48-unit minimum, not a fixed height. Both grow with content and Dynamic Type. Board facts and grouped row values wrap instead of squeezing the title or label. Section chips draw at 32 units and summary counts at 30, with parent room and hit slop for a 44-unit target; shared search fields, clear buttons and composer fields have real 44-unit minimum bounds.

## Elevation & Depth

The Board’s content uses tonal layering and sparse hairlines rather than decorative shadows, gradients, bevels, or cards. Native navigation and supported system-glass headers keep their platform materials and accessibility fallbacks; the flat-content rule does not ban them. Rows are defined by spacing and a single hairline, not a border box. The first-load skeleton uses three soft rounded placeholder rows to show content still arriving, never decorative cards.

### Named Rules
**The Flat Board Rule.** Use background, surface, spacing, and sparse hairlines to convey structure; do not add decorative elevation to ordinary rows.

## Shapes

Board rows and section rows stay open on the page; no enclosing card. Section chips are fully rounded capsules (16-unit radius on a 32-unit-tall chip). Search uses a gently rounded 10-unit inset; the general bordered input style retains its 9-unit corner. Grouped surfaces and segmented containers use 12-unit corners, with 10-unit corners on individual segments. Primary action pills (New session, in the empty state) use the native action treatment with a 24-unit radius. Every action preserves a native minimum target of 44 units on iOS.

## Components

### The hub button
- **Shape:** One glass capsule in the header's leading slot: the hub's name at 17px in ink-hi and a `chevron.down` glyph. It's a custom header view, because a native bar item given both a title and an image draws only the image.
- **Behavior:** Opens the Hub sheet (below).

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
- **Typography:** The shared 15/20 semibold section role, sentence case, ink-hi, without added tracking.
- **Behavior:** "Needs you · 4", "Working · 9". A band with nothing in it has no header.

### Board rows (signal and quiet)
- **Shape:** Draw no separator of their own; the list draws hairlines inset to the title (54 units: 16 padding + 28 mark + 10 gap). Signal rows have a 64-unit minimum and quiet rows a 48-unit minimum; neither caps the height needed for larger text or wrapping facts.
- **Typography:** Title semibold 17/22 (up to two lines in signal rows, one in quiet rows at ordinary sizes); why line 15/20, with a semantic state word when applicable; metadata 13/18 in ink-low; age 13pt tabular in ink-low. Above a 1.2 text scale, the title and why line lose their line limits and age/unread moves below the title.
- **Behavior:** Draft and running-subagent tags share a wrapping line below the title/why, rather than competing with title and age. Task, project, host and optional model facts wrap on their own line. Tapping a row opens its session.

### State marks and the pulse meter
- **Shape:** A 28-unit leading mark column, shared by every row.
- **Marks:** SF Symbols through `SymbolView`, always pairing shape with color so they read without it: `xmark.octagon.fill` (Failed, danger), `questionmark.circle.fill` (Question, attention), `exclamationmark.bubble.fill` (Needs you, attention), `hand.raised.circle.fill` (Approval, attention), `exclamationmark.triangle.fill` (Warning, attention), `arrow.triangle.2.circlepath.circle.fill` (Restart needed, attention). A row with anything new since you last opened it, wherever it sits, carries a small blue dot beside its age (accent, 8pt); folded, Idle's header carries it for the rows inside.
- **Pulse meter:** A 22×15pt, 7-bar activity meter, shown on Working rows in the Live band and, every working session's bars summed minute by minute, as the fleet meter beside the Live summary's counts. One fixed log scale (full at 64 events per minute) so meters compare across sessions; a 1pt baseline always draws, older bars fade, and the meter grays when the connection is down. Each bar is a minute of real transcript and tool-output activity, read every 10 seconds from the hub while the Board is in front (S5, `evener/activity/read`); a read counts only while it's under 20 seconds old (two poll intervals) and the connection is up; a working row with no read yet, a hub that predates S5, a dropped connection, or a connected hub whose reads have silently stopped landing, all show a single bar in its newest minute instead - a stale read would otherwise keep ticking toward a false "stuck" as the gap runs long, whether or not the connection itself ever reported a drop. Quiet for 3 minutes reads "Quiet 4m" in the why line; past 10, "May be stuck · no updates for 12m" in amber, the meter itself goes flat and amber, and the row floats to the top of Working - unless the session is waiting on subagents, which is never quiet or stuck. Every read also carries what the session's own root turn last set out to do: the newest tool-call intent its daemon saw, one bounded line, dropped when a turn begins. That words the why line in place of the job the session is running - the intent says what the job is for - and a session waiting on subagents, quiet or stuck still says so first. The row takes a `moving` flag so a still copy of the same session elsewhere shows a plain dot instead, for the inline quiet rows outside Live.

### The Idle fold
- **Shape:** A single 48-unit row: "Idle · 3" in ink-mid at 15px (sentence case, like the band headings), with a chevron that rotates 90° when open.
- **Behavior:** Folded by default; the fold state persists per device. Unfolding, directly or by jumping from the summary line, reveals Idle's quiet rows. Folded, it shows the blue dot while any session inside has something new; VoiceOver says "unread sessions inside".

### Section rows
- **Shape:** Open disclosure headers and indented rows, not cards. Project/host rows have 48-unit minimums; section disclosures and deeper branch/archive rows have 44-unit minimums. Nesting adds 16 units of indentation per level.
- **Project tree:** Project, host and branch labels wrap above a 1.4 text scale. Their symbols stop scaling at 1.4, leaving room for the full name; 8pt vertical padding lets the row grow with it.
- **Behavior:** Each pinned category expands inline, including an empty category as a place. Projects (or Hosts, with the alternate organization), Test runs and Archived expand their trees in the same scrolling Board when present. Project/host/archive folds reveal tier captions and quiet session rows; the fold state stays per device. A session row opens the conversation, not the enclosing category or project.

### The floating compose button
- **Shape:** A 56-unit accent circle floating over the Board's end, 16 units in from the trailing edge and clear of the home indicator, with a centered `square.and.pencil` in the fill's on-color. The Board insets and pads its end by the button's room (its circle, its edge and the home indicator), and the toast floats above it.
- **Behavior:** Opens the New session sheet (below); disabled (not hidden, dimmed) while disconnected. The Board carries no connection status of its own: the fleet meter grays while the hub is away, the rows keep their last state, and each control that needs the hub is disabled or holds its action, so a bar announcing the drop would be redundant chrome.

### The notice row
- **Shape:** Sits on the page like a row: a mark, one sentence, a bottom hairline.
- **Behavior:** The incompatible-version warning, "This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub.", shown with an amber `exclamationmark.triangle.fill` mark; and, while a close a retry can fix lasts, the connection's own sentence for why it closed ("Couldn't reach the hub. Check its address, its token and your network."), which goes when the connection returns. The row takes no action of its own.

### Skeleton, empty and failed-first-read states
- **Skeleton:** Three still placeholder rows, 64 units tall with a 10-unit rounded fill, on the very first load only.
- **Empty:** "Nothing's running. Start a session to put an agent to work.", with a primary New session action, shown when Live has no rows.
- **Failed first read:** "Couldn't load this hub's sessions. Trying again shortly.", shown when the very first read of Live fails. Any failed read (Live, Needs you, the pin catalog or the manifest) is retried on its own with a growing backoff while the Board is in view, and when it comes back into view; there is no Retry button.

### Message actions
- **Style:** A three-dot 44-unit action beside eligible user messages.
- **Behavior:** iOS uses the native `ActionSheetIOS` action surface with the selected message title, Fork from here, and Cancel. The Fork preview is read-only; explanatory copy says the created fork opens with an editable draft.

## Do's and Don'ts

### Do:
- **Do** order every live session by who needs you, then working, then idle.
- **Do** keep a Board control mounted and disabled, rather than hiding it, while it can't act. The Session hides a control that needs the hub while the hub is away (see the Session's rules below).
- **Do** use human readable state words, and copy that says exactly what happened.
- **Do** persist each section's collapsed state per device.

### Don't:
- **Don't** show a Reconnect or a Refresh control anywhere on the Board, or offer pull-to-refresh: a control appears only when it can act.
- **Don't** move or animate a row unless its underlying data moved.
- **Don't** give a hue more than its one job: amber means a human is needed, green means working, red means failed, blue means tappable, selected or unread. Only the state word takes a hue; the reason stays in ink.
- **Don't** wrap every ordinary row in a rounded card or add decorative shadows.

## The Session

The Session is one conversation, what the agent is doing now, and the place you talk to it. Its interaction foundation is `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`, section 8; the shared native visual direction is recorded above. The Session uses the Board’s palette, type roles and shapes; what follows is what it adds.

**Key Characteristics:**
- One screen from top to bottom: the nav bar (whose state line is the live status), the context chips and notes bar, the transcript ending in any ghosts, then whatever is live at the bottom (a dock, or the composer).
- One way to send: the composer's slot sends or, while the agent works with an empty field, stops. Steering is something you do to a queued message.
- The transcript shows as much of the agent's work as you choose, per session.

### Session Components

#### The nav bar
- **Back:** The chevron, then the amber count (17pt, attention-ink) of the other sessions that need you. Text and chevron use the shared navigation scale cap; the button has 44pt minimum bounds and a Large Content Viewer label with the full count.
- **Title:** The session’s name (15pt semibold, one line) over its state line (13/18, ink-mid, tabular figures) and a small chevron. While working, the text is the live line: the latest intent or activity and elapsed time ("Checking for an existing remote… · 23s"). Every other state reads its still mark and words: "Failed", "Restart needed", "Warning", "Shut down", "Asks a question", "Needs you", "Asks for approval", "Idle · 35m ago" or "Idle" when no turn has ended. The text button opens the Session sheet. Beside it, a separate 44×44pt meter button jumps to the live end; the title has no nested jump target. The title and jump button expose the Large Content Viewer. Both title and Back use the `23 / 17` header scale cap, and width allocation accounts for the Back count’s digits. A horizontal pan on the title moves to the next or previous session.
- **Title swipe:** A horizontal pan on the title moves to the next session in Live order (to the left, as a page turns) or the previous one (to the right). It starts only once a drag goes 10pt sideways, and it moves after 60pt or a 500pt/s flick; a pan flicked back the other way moves nowhere. The session slides in from the side it came from. VoiceOver gets "Previous session" and "Next session" actions in its place.
- **⋯ menu:** A native menu: Detail level (with the five levels inline), Stop (destructive, only while the turn can be stopped — where the turn stays stoppable once a draft fills the composer's slot), Find in session, then Files, Agents, Jobs, Tasks and Notes & links, each only when it can act, Session info, Ask aside… (when the session can fork and the hub is reachable), Pin to category…, Archive, and Shut down (destructive, only when it can). Delete lives in the Session sheet. The rows come from one source (`sessionMenu()`), so Android's sheet (where the native menu isn't drawn) shows the same rows as grouped rows, its level submenu opened out as checked rows.

#### The connection bar, context chips and notes bar
- **Connection bar:** A 24pt line of 13pt ink-low text under the nav bar: "Reconnecting…" once the connection has been down 2 seconds, "Offline · updated 3m ago" after 30, and "Update needed" at once when the hub speaks an incompatible protocol version. A close a retry can fix shows the connection's own sentence for why it closed ("Couldn't reach the hub. Check its address, its token and your network.") in place of Reconnecting/Offline while the connection stays down, and nothing more once it returns. A live connection shows nothing.
- **Context chips:** 32pt capsules (16-unit radius, 1pt border, inset fill) in one sideways-scrolling row, each only when it has content: Agents with its subagent count (`person.2`), Files with its count and a blue dot when a document is new or changed (`doc.text`), Tasks "3/7" (`checklist`, settled: done plus cancelled), Goal (`target`, amber when blocked) and Queue with its depth (`tray`). Agents and Tasks open live views, so they hide while disconnected; the others always show. Jobs has no chip; the menu reaches it. Dragging the list down more than 8pt from where the scroll last turned slides the row up in 200ms, behind the connection bar when it shows and behind the nav bar otherwise; any upward scroll, or reaching the top, brings it back. Scrolls the app makes itself (restoring your place, following the latest message) never hide it. The slide is a transform, so the list and your place in it never move.
- **Notes bar:** One 32pt line under the chips, in the same sliding row: "Your note: …" with a person glyph, else "Agent's note: …" with a sparkles glyph, else the one link's label or "3 links", 15pt in ink-hi, with a trailing "3 links" in 13pt ink-mid when a note shows beside links. It shows only when the hub offers shared notes and there is something to preview. Tapping it opens Notes & links.

#### The transcript
- **Detail levels:** Chat (just the conversation), Intent (plus one line for each action, saying what it set out to do, as the web draws it), Tools (plus every command it ran; tap one for its output), Activity (plus every command's output, open as it arrives) and Full (everything, including the agent's reasoning). The level is chosen per session in the ⋯ menu and remembered on this device; with none chosen, the session shows the hub's display setting. A toast confirms a change, since it often lands above the visible part of the transcript.
- **Your message:** A right-aligned bubble in the accent wash (`#DDEBFC` light, `#2A343D` dark), Source Serif 17/25. A message steered into a running turn carries "Steered in mid-turn" beneath it.
- **Agent message:** Source Serif 17/26 on the page, no bubble; headings in SF Pro, code in Menlo.
- **Time markers:** A centered 13pt marker ("Today 3:00 PM") before the first turn, before a turn that starts after ten quiet minutes, and at a new day.
- **Runs:** Consecutive steps fold into one line behind a chevron that points right folded and turns down open: "3 steps · 1m · read 1 file, edited 1 file, ran npm test", with "(1 failed)" in danger ink. The duration shows only when every step carries its times. Expanded, each step is a line with a check or `xmark.octagon.fill`, its intent and its target in Menlo; a step with evidence opens under its line: command output in a Menlo inset (the first 40 lines, then "Show all 60 lines"), an edit as a diff, the file a write wrote, an error, and the step's images. The step in progress is the live status line's, never the transcript's; the run still growing never folds.
- **Subagent:** The web's shape: a 2pt left rail in the state's hue, no card. The title and "running · 10m" or "failed · 11m" share one line, the latest activity ("Quiet 5m", "Waiting on 3 subagents", or the failure's reason) beneath. Tapping opens its own transcript.
- **Thought:** A settled thought reads "Thought for 12s" with the runs' chevron, folded, and opens to the thought in the serif. A live thought is the live status line's.
- **Question (history):** An amber left rule, each question in the reading serif, and "You answered: Drop them" beneath. While a question is still open, the dock is the question, and the transcript leaves it out.
- **Your note:** A saved note shows as its own row where the agent was told: a 2pt left rule in the strong edge color, "You updated your note" in 13pt ink-mid, and the note in the your-message serif ("You cleared your note" alone when it was emptied).
- **Document chip:** A document the agent named sits under its message: its kind and title, the file name, and how long ago the session wrote it. Tapping opens the Reader.
- **System event:** A diamond in a 16pt gutter and the event in 13pt ink-low, two lines at most until tapped.
- **Error:** A red left rule, the hub's words as they are, and at most one action: Resume when the session is paused, Sign in when a sign-in failed, otherwise Retry on the latest turn's own failure while Send can act and the input that opened the turn can be re-sent. Retry re-sends that input — your words and images, not a sentence invented for you; with no input to re-send there is no Retry.
- **Scrolling:** A session opens at the latest, never at a spot saved up the history. With "Open at unread replies" on (Display's setting, on at first), a reply that finished since you last reached the end opens at its start. New content below reads "↓ 3 new" in a floating capsule (a ghost coming or going is never new content); tapping it scrolls to the end. Until the first read lands, three still blocks stand in for the conversation.

#### The live status line
- **Place:** The nav bar title's state line, while the agent works; there is no separate tray above the composer. It carries the session's pulse meter and the running turn's most recent nonblank tool-call intent, with the session's elapsed time after it ("Checking for an existing remote… · 23s"). Before a tool intent arrives, the line falls back to current progress such as "Running go test ./cmd/evener-hub/… · 5m", "Waiting on 32 subagents", "Thinking…", or "Quiet 40s".
- **Behavior:** The newest tool-call intent remains verbatim until the running turn ends, including after that tool call completes, and outranks progress and liveness fallbacks. Before the first intent, ten minutes with no update reads "May be stuck · no updates for 12m" in amber, unless the agent is waiting on subagents, which is never stuck; a provider retry reads "Retrying · rate limited · attempt 2 of 5". The separate meter button beside the title jumps to the live end; tapping the text still opens session info. Its own clock ticks the seconds once a second while the agent works, so the transcript below never re-renders for them.

#### Floating controls
- **Next:** A capsule floating 10pt above the bottom while another session needs you: "Next" in accent ink, then that session's title and a chevron. A tap opens it; touch and hold lists everyone who needs you. The Next capsule, "↓ 3 new" and toasts share one column so none covers another.

#### The ghosts
- **Place:** The transcript's last rows, after its last message and just above the composer. They scroll with it, so they take none of its room; scrolled up in history, you don't see them.
- **Shape:** A dashed, unfilled bubble (1pt strong-border, 18-unit radius, at most 85% wide) where your message will land, its text in the your-message serif (17/25) in ink-mid, a 13pt ink-low caption beneath (danger ink for a refused message), and its actions as text buttons: Cancel and Discard in ink, the rest in accent ink.
- **States:** Queued ("Queued · sends when this turn ends", with Steer now), held after a Stop ("Held · you stopped this turn", with Send now and Cancel), steering ("Steering · arrives at the next step"), sending ("Sending…"), unconfirmed ("Couldn't confirm this was sent", with Check and Discard) and refused ("Couldn't send this", with Edit and Discard).
- **Behavior:** At most three queued messages show; the rest read "2 more queued", which opens the Queued messages sheet with every one and Steer all now. Tapping a bubble opens the rest of its actions (Edit, "Cancel message"). A queued or held message swipes left to Cancel. A queued message's actions hide while the hub is away; the message itself stays.

#### The ask dock
- **Shape:** An amber-edged card (1pt attention-edge, 12-unit radius, raised fill) above the composer, the same card for both kinds.
- **Question:** "Question 1 of 2", one question at a time in the reading serif with its why beneath, then its options as radio rows (checkboxes for a multi-select), the recommended one first and marked "Recommended". "Other answer…" answers in your own words through the composer; one primary button reads "Next question", then "Send answers". A chevron folds the dock to one bar that says what waits ("Answer 2 questions"), and the composer returns.
- **Approval:** "Wants to write outside the workspace" with an amber hand, the tool and its target in Menlo (a path wraps only at its slashes), the session's scope ("This session can only write inside its project folder."), then "Allow this file only" (with "It will ask again for the next one") and Deny. There is no redirect, and the composer stays away while an approval waits.

#### The composer
- **Shape:** One rounded field (22-unit radius, 1pt border, raised fill). The input's minimum is one scaled 24pt line plus its vertical padding, never less than 44pt. Native intrinsic layout owns growth and shrinkage; no explicit height is derived from its last measurement. It stops at six scaled lines or one third of the window height, whichever is smaller, unless its minimum needs more room. Reaching that cap, a content measurement above it, or explicit lines above it exposes Expand editor. The inline input uses native font scaling with base 17/24 typography, so changing Dynamic Type remeasures an existing draft without remounting it. A controls row below holds +, the model chip, and the trailing slot - Stop or Send. Empty and unfocused the whole composer collapses to a single row - +, the field and Send (or Stop) - with the model chip stepping aside; touching it or typing opens it to the field over the full controls row. A field that cannot take focus (a draft that failed to load, so it is read-only) keeps the open shape: focus is the only way back to the controls row, so collapsing it would strand the model chip. An attached image keeps it open the same way: an image-only draft is a message, so it rests no more than typed text does. The card sits close to the screen's bottom edge: a 4pt margin under it, over the trimmed home-indicator room the bar reserves. The field, model control and Send/Stop slot each retain a 44pt minimum height. Add, Expand and Send/Stop keep 44pt targets; the Add and Expand symbols stay 20pt and 15pt rather than scaling with body text. Resting/open layout changes reuse the same input rather than remounting it.
- **Send:** A 44pt paper airplane on the accent fill. Its placeholder and VoiceOver label say what it will do: "Message" and Send, "Tell the agent something…" and Queue message while the agent works, "Message to resume" and Send and resume on a shut-down session, "Answer or ask…" and Send answer while a question is open.
- **Stop:** While the agent works and the draft is empty (no text and no image), Stop (`stop.fill` on the danger fill, in the same 44pt slot) replaces Send: with nothing to send, stopping is the immediate action. Any text or an attached image brings Send back. Stop needs the hub, so it hides while disconnected, and is disabled while a stop is in flight.
- **+ menu:** Photo library, Camera, and Commands and skills, which opens a sheet of the built-in commands, then skills and plugin commands grouped by plugin; a choice puts its "/name" at the start of the draft.
- **Model chip:** "DeepSeek 4.1 Flash · XHigh ⌄" opens the model sheet with search pinned under its title. Effort choices (Low to Max, "Applies from the next turn") and errors scroll above Recent and each provider’s models, so stacked choices at large text sizes cannot consume the list's viewport. A rejected change brings its error into view above the effort choices, including repeated refusals. Choices reserve a 24pt leading check column: selected rows draw an accent check, unchecked rows keep the same space; selection does not tint the whole row. Provider headings use the shared 15/20 section role without case transformation. It steps aside while the composer answers an open question.
- **In the composer's place:** A session that runs an older Evener and needs a restart, or one that is paused, shows plain text with its one button instead.

#### The Session sheet
- **Shape:** A sheet that opens at large: the title and its state line, then grouped sections.
- **Sections:** Where (host, project, folder in Menlo, branch, and the session id — its ref in Menlo, which a tap copies), Model (the model and effort, and the vision model), Plugins ("10 plugins · chosen at start" and their names), Access (sandbox and network, when the hub reports them), Usage (tokens with their split, cost, work time, a context gauge with no compaction marker, and failed tool calls), Goal (edit, set or clear), Tasks, Notes & links, and Actions (Aside, Fork from latest, Compact context, Pin to category…, Archive, Shut down, and Delete for a saved session that isn't loaded). It shows only facts the thread carries.

#### Notes & links
- **Shape:** A sheet that opens at large with three groups: Your note, Agent and Links.
- **Your note:** An editor while the session can take notes, with a status line beneath: "Your note stays on this session. The agent is told when it changes." while the agent works, and "Your note stays on this session. Saving it will wake the agent." while it rests. Leaving the field saves ten seconds later ("Saves in 10 seconds, or when you close this."), and closing the sheet saves at once; the line then reads "Saving…" and "Saved". A note that can't reach the hub stays on this phone ("Couldn't save your note yet. It's kept on this phone."). A session that can't take notes (shut down, needing a restart, paused, or without shared notes) shows your note as text, read-only.
- **Links:** Each link shows a globe or document glyph, its label, and its URL in Menlo. A web link opens in the in-app browser; a file link inside the session's folder opens in the Reader. Touch and hold offers Open (for a web link), Copy link and, where the session takes notes, Remove link. Where the session takes notes, a link swipes left to Remove ("The agent adds links as it works. Swipe left on one to remove it."); a removed link reads "Link removed. Only the agent can add links."

#### Find in session
- **Shape:** "Find in session" in the ⋯ menu puts a find bar where the chips sit: a field, where you are among the matches, steps to the older and newer match, and Done. The bar holds still while the list moves from match to match.

### Session Do's and Don'ts

#### Do:
- **Do** show the live status line only while the agent works, in the nav bar title; Stop takes the composer's slot with an empty field, and Send with text.
- **Do** keep one meter per view: the live status line's.
- **Do** keep a queued message where it will land, as a ghost, with what it waits for spelled out beneath it.
- **Do** let reads retry on their own, and let the skeleton stand in without a spinner, shimmer or sentence about connecting.

#### Don't:
- **Don't** show connection controls in the composer, or any Reconnect or Refresh control: a control appears only when it can act, and one that needs the hub hides while the hub is away.
- **Don't** move what the reader is reading: new content is announced by "↓ 3 new", and the chips slide away with a transform, never by changing the list's layout.
- **Don't** show the live step or the live thought in the transcript; the live status line says what's happening now.
- **Don't** offer a redirect on an approval, or the composer while one waits.

## The Sheets: New session and the Hub

New session sets up and starts a session; the Hub looks after the hub and this phone. Their interaction foundation is `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`, sections 11, 12 and 14. Both use the shared native palette, type and grouped controls; what follows is what they add.

**Key Characteristics:**
- Each is a full-height page sheet (a `presentation: "modal"` route) holding its own native stack, so pickers and detail pages push inside it with a Back to where you were. Swiping the sheet down closes it.
- Both are built from one grouped list (`src/sheet/Grouped.tsx`), so a row means the same thing everywhere.
- Calm: a page keeps its last data while the connection is away, says so on one line, and disables what needs the hub rather than hiding it. Nothing asks you to reconnect or refresh.

### The grouped list
- **Page:** The sheet canvas behind, rows on the raised surface in groups with a 12-unit radius, inset 16 units from each side. Hairlines sit between rows, never around a group: inset 50 units when both adjacent direct rows have symbols (16 + 22 + 12), otherwise 16. No shadows.
- **Section label:** SF Pro semibold 15/20, sentence case, no added tracking, ink-mid, inset 32 units ("Where", "Fleet"). A machine label (a marketplace or provider profile) stays as typed in Menlo, without the semibold interface treatment.
- **Row:** At least 44 units, with 16 units of horizontal and 12 units of vertical padding. A bare SF Symbol in ink-mid at 17pt leads it in a 22-unit column, never a colored tile. The label is SF Pro 17/22 in ink-hi, with an optional second line at 13/18 in ink-mid (Menlo for a path/id, danger ink for a problem). A value uses ink-mid 15/20 with tabular figures; label and value share a wrapping line, so a long value can move beneath its label. A 13pt chevron in ink-low remains separate when the row opens a page. Action labels use accent ink; destructive labels use danger ink. Picker checks occupy the leading symbol slot, with the same space reserved when unchecked. Disabled rows keep retained values opaque and use secondary ink for the label; native disabled state blocks activation.
- **Switch row:** The label and second line stay opaque, then a switch in the accent color, never the working green. When disabled, the label uses secondary ink unless it remains a separately actionable detail opener. The native switch carries its disabled state and is the VoiceOver control; an independently actionable row label remains its own element.
- **Editable row:** `TextFieldRow` keeps its value visible when editing is unavailable: secondary ink at full opacity, with `editable={false}`. Machine values stay in Menlo; empty placeholders retain the interface face.
- **Segmented control:** An inset container with a 12-unit radius and 2-unit padding/gaps; each segment has 10-unit corners, a 44pt minimum height and wrapping 15/20 text. Selection is a neutral raised surface in light appearance or pressed surface in dark, with semibold primary ink, not a blue or ink-filled pill. It becomes a vertical set of checked rows when text scale exceeds 1.2 or measured width per option is below 88 units; stacked rows reserve a leading check slot. Disabled labels use secondary ink. Choosing a different segment plays the selection tick; tapping the chosen segment does nothing.
- **Tag:** A small capsule beside a value, 11pt semibold: amber for something a human must see ("Offline", "1 offline"), gray for version drift ("Hub runs 0.9.412", "1 on another version").
- **Footer:** 13/18 under a group, inset 32 units, in ink-mid; in attention ink when it names something that needs a human, danger ink for a failure, and Menlo for text the hub wrote (a host's last error).
- **Search field:** A rounded inset well (10-unit radius) drawn inside a 44pt-minimum input row; the fill is inset 4 units at top and bottom. The containing page supplies the shared 16pt horizontal inset, including Launch defaults. Text follows the 17pt interface role, with quiet placeholder ink. A nonempty field shows a separate 44×44pt-minimum clear button inside the row. It filters as typed, with no capitalization or autocorrection.
- **Header buttons:** Accent ink at 17pt in a 44-unit target: "Cancel", "Done", "Save", "Start". The one that finishes the page is semibold; one that can't act reads in ink-low and does nothing.

**The Grouped List Rule.** In sheets and administration, use inset raised groups over the neutral canvas, wrapping labels and values, bare semantic SF Symbols and internal hairlines. Keep checks leading and save actions outside the scrolling fields when the form already owns a pinned Save strip.

**The Retained Value Rule.** Disabling editing must not fade retained values. `Row`, `SwitchRow` and `TextFieldRow` keep content opaque and use secondary ink for disabled labels or input, while native disabled/editable semantics prevent changes. Button-only disabled opacity remains unchanged.

### The calm connection line
- One centered line at the top of a sheet page, 13/18 in ink-mid, with the Board's words: "Reconnecting…" after 2 seconds down, "Offline · updated 3m ago" after 30, and "Update needed" at once for an incompatible hub. A close a retry can fix shows the connection's own sentence for why it closed in place of Reconnecting/Offline while the connection stays down. A live connection shows nothing.
- The page underneath keeps its last data. A page that has never loaded says "Connecting to magic-kingdom…" in its place, or the incompatible-versions sentence when no retry can help.
- Controls that need the hub stay on screen, disabled; the line already says why. There is no Reconnect, Refresh or pull-to-refresh anywhere in either sheet.

### New session
- **Header:** "Cancel", the title "New session", and "Start" (semibold). Start reads in ink-low and does nothing while something holds it.
- **Opening:** The saved draft, when it has a project, a prompt or an image; else the newest start remembered on this hub (host, project, model, effort, plugins, access and the options); else the hub's most recent project once the hub lists them.
- **The prompt:** First, focused on open: SF Pro 17 with the placeholder "What should the agent do?", growing to six lines and then scrolling, with its image thumbnails and a "+" that attaches images.
- **Why Start waits:** A danger footer under the prompt says it in a sentence: "paradise-park is offline. Connect it or choose another host.", "paradise-park is no longer a host on this hub. Choose another host.", a chosen model the host's list couldn't confirm ("Choose Hub default to start."), or each chosen plugin with a blocking problem. The row to blame turns danger ink. A missing project blames Project quietly. When the hub refuses a start, its reason shows the same way and the sheet stays.
- **Where:**
  - Host (`server.rack`): the host's name, the hub's own machine named after the hub, with an amber "Offline" tag when it is. When the host changes and the project isn't there, the group's footer says so: "evener isn't on paradise-park, so the project changed to docs."
  - Project (`folder`): the folder's name, or "Choose a project".
  - Branch (`arrow.triangle.branch`): the project's current branch, for information only, with no chevron. It is left out outside a repository or while unknown.
- **Agent:**
  - Model (`cpu`): the model's display name with "via <provider>" beneath, or "Hub default".
  - Effort: only when the model lists levels. "How long it thinks before acting", then a segmented control of those levels (Low, Med, High, XHigh, Max), none lit until one is chosen.
  - Plugins (`puzzlepiece.extension`): "10 of 14", "…" while the host's plugins are read, "Listed once a project is chosen" with no project, "Couldn't list this host's plugins" in danger ink when that failed, and "1 need attention" in danger ink when a chosen plugin has a blocking problem.
  - Access (`lock.shield`): the access level, with what it allows beneath. It stays blank until the hub says its default, so it never guesses the least safe level.
  - More options: "Context strategy, subagent depth, turn limit", valued "Custom" once one is set.
- **Footer:** "Host, plugins and access are fixed once the session starts. Model and effort can change later."
- **Start and Cancel:** Start replaces the sheet with the new session and remembers its setup. Cancel closes at once with nothing to lose; with a prompt or an image it asks "Delete this draft?" with "Keep draft" and "Delete draft" (destructive). Swiping the sheet down keeps the draft for next time.
- **The pickers**, each pushed inside the sheet:
  - Host: the hub's own machine first, then each host, the chosen one checked. A connected host shows its system and live sessions ("macOS · arm64 · 1 live"); an offline one shows its state, can't be chosen, and carries an accent "Connect" of its own (reading "Connecting…" while it works) when the hub isn't already reaching for it. A refused Connect is said under the list.
  - Project: a search field ("Projects on paradise-park"), "Recent on paradise-park" with each project's name over its path in Menlo, and "Browse folders on paradise-park…" in accent ink.
  - Browse folders: one page whose folder changes in place, headed by the folder in Menlo (or "Home"), with "Up one folder", each folder with a chevron, then "Use this folder" (hidden at home, which the phone can't name) and "New folder", which asks for a name (iOS only) and moves into the folder it made.
  - Model: a search field, "Hub default" first, up to five recent models, then one group per provider as typed. Each row reserves the same 24pt leading check column as the Session model list, then shows the display name, its context and price per million tokens, the registry's notes in attention ink, and `eye` ("Sees images") and `wrench.and.screwdriver` ("Uses tools") glyphs in ink-mid. There is no effort control here; effort lives on the form.
  - Plugins: a search field, "All" and "None" in accent ink, then the host's plugins grouped by marketplace (Menlo, as typed) or "Other plugins". Each row carries its name, description, what it brings in ink-low ("38 skills · 3 agents"), its warnings in attention ink, any blocking problem in danger ink, and a switch. The preview's other diagnostics show on the page in attention ink; a chosen plugin the host no longer has is named with "Remove <name>". The footer: "10 of 14 on. Plugins can't be changed after the session starts."
  - Access: the four levels (Full access, Workspace write, Read-only, Restricted) as checked rows with what each allows, and a Network switch ("Lets the session's commands reach the internet") only inside a sandbox. Choosing what the hub already defaults to follows the hub again. The footer: "Access is fixed once the session starts."
  - More options: Context strategy, Max subagent depth and Max turns, each a segmented control that starts on Default, with the hub's default beneath ("The hub's default is compact."). A value the control doesn't offer lights no segment, and the footer names it ("This session uses 4. The hub's default is 2."). The page ends: "Everything else uses the hub's launch defaults. Edit them from the Hub, under Launch defaults."

### The Hub
- **Header:** The hub's name as the title, "Done" (semibold) to close, and one line beneath in ink-mid: the connection ("Connected", "Connecting…" before the hub is ready, or the calm line's words), the hub's version and whether it is current ("Connected · evener 0.9.412 · up to date", or "Update available").
- **Home:** Grouped rows, each pushing its page:
  - Fleet: Hosts (`server.rack`), counting every machine with the hub's own, tagged amber "1 offline", or gray "1 on another version".
  - Setup: Providers (`key`), the provider count; Plugins (`puzzlepiece.extension`), the installed count.
  - This phone: Display (`textformat.size`, valued System, Light or Dark), In-app alerts (`bubble.left`) and Hubs (`point.3.connected.trianglepath.dotted`, the saved count).
  - More: Web keyboard shortcuts, Launch defaults and Hub settings push inside `HubSheet`’s native stack. Back returns within the sheet; these pages use the same grouped canvas, rows and footers as the Hub home.
  - About: "Evener for iPhone" with the app's version and build, and "Update hub" in accent ink only when an update is waiting. It confirms, installs and restarts the hub, and the app reconnects on its own; a footer says it is restarting into the new release meanwhile.
- **Hosts:** The hub's own machine first, named after the hub ("Connected · 2 live" and the hub's version), then each host with its state in words ("Connected", "Connecting…", "Offline · reconnecting", "Offline"), system and live sessions ("3 live", or "3 live, out of reach" while offline), and its version with a gray "Hub runs 0.9.412" tag when it differs. The page polls every 2 seconds while it is in front. A host's detail lists Status, Version, System, Sessions and Project roots, then its last error in danger Menlo while offline, then Connect, Edit and Remove. Edit is its own page with Cancel and Save: the SSH address keeps its help line, each optional field says in its placeholder what empty means, and one footer says only the SSH address is required.
- **Providers:** Each provider with its sign-in state in words: "Signed in", "Key set", "Not signed in", "No key", "No sign-in needed", or "Sign-in expired", the only one in amber. A provider’s detail pushes inside the Hub stack, with sign-in in the in-app browser. Manage keeps adding, editing, making default, clearing and removing on the phone.
- **Plugins:** The hub's installed plugins grouped by marketplace, each with its "On by default" switch and a detail sheet; the marketplaces; and Browse, to install from their catalogs.
- **Display:** Appearance (System, Light, Dark) and Reading font (Serif, Sans; "For what agents write: messages, plans and documents.") for this phone, and the hub's Default detail level, a page of its own that saves a choice at once. "Each session can override this from its menu." Show model on Board rows and Open at unread replies (on at first; off has every session open at the very bottom) are this phone's switches.
- **In-app alerts:** Which banners show, whether they wait while you read or type, and Haptics, kept on this phone.
- **Hubs:** The saved hubs, the selected one checked, then "Scan pairing code", "Paste pairing link" and a quieter "Enter the address", with "In Evener on your computer, open Settings, then Mobile app, to show a pairing code." A hub's details show its Name and Address and "Remove this hub".

### Administration inside the Hub
- **Hub settings:** `src/HubSettingsScreen.tsx` uses `GroupedPage` and `Group` for hub facts, runtime/storage information, agents and MCP servers. Supporting explanations and errors stay next to their content. This is not a new save workflow.
- **Launch defaults:** `src/LaunchSettingsScreen.tsx` groups schema options into rows, with shared search and a pinned "Save defaults" strip above the scrolling form. Its existing dirty, loading, saving and conflict gates still own whether Save can act. A scalar choice opens `LaunchScalarEditor` in a `ModalSheet` titled with the setting’s label, with Cancel and Done in the header; Done applies to the form draft, and Save defaults commits it. Effective values remain selectable. Each rejected Done reveals its error before the description and effective value, in both the ordinary editor and the scrolling model catalog. Repeated refusals preserve drafts, search and choices.
- **Web keyboard shortcuts:** `src/KeybindingPreferencesScreen.tsx` uses grouped rows and editors. With a draft, no conflict and no open shortcut editor, "Save changes" stays in a strip above the scroll. Local draft recovery, conflicts, unsupported rules and warnings retain their existing actions; the visual grouping does not change their owners or silently discard them.
- **Selectable data:** Hub identities, runtime facts, launch values, shortcut previews and unknown-rule diagnostics retain native text selection. Data-bearing grouped rows and footers opt into selection without changing their tap actions. Ordinary navigation and action text stays nonselectable.
- **Refusals:** The launch-defaults and shortcut pages put errors first in their scrolling content, before hub names or introductions that can grow with Dynamic Type. A failed action reveals that first block and keeps the draft. Repeating the same validation failure reveals it again.

### Sheet Do's and Don'ts

#### Do:
- **Do** push pickers and details inside the sheet, so Back returns to the form or the Hub's home.
- **Do** keep amber for what needs a human (an offline host, a sign-in that expired, a Start held by its host) and gray for version drift.
- **Do** name the hub's own machine after the hub, and say "Hub default" rather than guessing a value the hub hasn't given.
- **Do** keep a sheet's last data and its controls in place while the connection is away, disabled where they need the hub.

#### Don't:
- **Don't** offer Reconnect, Refresh or pull-to-refresh; the calm line says what the app is doing.
- **Don't** add decorative colored glyph tiles. Ordinary row symbols use ink-mid; selection checks and semantic status marks keep their meaningful colors.
- **Don't** let one host's answers land in another host's form, or show a place's plugins after the form has left it.
- **Don't** discard a draft without asking; only Cancel discards, and swiping the sheet down keeps it.

## Share to Evener

The iOS share sheet offers Evener for text and web links. The extension saves what was shared and opens the app; the choice of where it goes happens here, in one sheet over whatever was in front. Nothing is sent by picking a destination.

### The Share target sheet
- **Header:** the title "Share to Evener" and Cancel. Cancel asks before dropping the share.
- **The shared text:** up to four lines at the top, in 15pt ink-mid, the same quiet inset the sheets use for a summary. It is the content being placed, not an editable field.
- **New session:** a row under it with a `plus` glyph in accent ink and "New session" in 17pt semibold accent. It opens New session with the text already in the prompt, so the host, project, model and effort this phone remembers still apply.
- **The search field:** "Search sessions", the Board's own field and its 250ms debounce. A query replaces the live list with the Board's grouped results: live matches first, then past ones. A past session is a normal result and opens the same way.
- **Live:** a LIVE band of the active hub's sessions in the Board's attention order (Needs you, Working, Idle), each row a title, its state word and its project. No swipes, no Row menu: this sheet chooses, it doesn't manage.
- **States:** with no ready hub it says the session list waits for one and the share stays put. A share that can't be written to a draft stays in the sheet with the reason, and its file is kept.

### Share Do's and Don'ts

#### Do:
- **Do** write the shared text into the destination's saved draft before dropping the share, so a crash or a failed open loses nothing.
- **Do** append to an existing draft rather than replacing it.
- **Do** use the active hub, the one the Board shows; switching hubs stays in the Hub sheet.

#### Don't:
- **Don't** send anything automatically; the composer holds the text and Send is yours.
- **Don't** drop a share the person dismissed without choosing; the next launch or foreground offers it again.
- **Don't** explain a past session's shut-down state here; its own screen already does.
