---
name: Evener Native
description: The native iPhone app: the Board, its home, orders every live session by who needs you, and the Session is one conversation with its agent.
colors:
  light-background: "#FAF9F6"
  light-surface: "#F4F3EE"
  light-text: "#252521"
  light-secondary: "#5F5F57"
  light-border: "#DDDCD4"
  light-accent: "#0064C2"
  light-accent-fill: "#0070E0"
  light-error: "#C51D23"
  light-raised: "#FCFBF8"
  dark-background: "#191918"
  dark-surface: "#20201E"
  dark-text: "#F2F1EB"
  dark-secondary: "#B0AFA6"
  dark-border: "#34342F"
  dark-accent: "#459EFF"
  dark-accent-fill: "#0070E0"
  dark-error: "#F17478"
  dark-raised: "#232320"
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
  status-tray:
    textColor: "{colors.light-secondary}"
    typography: "{typography.why-line}"
    height: "36px"
  ghost-bubble:
    textColor: "{colors.light-secondary}"
    typography: "{typography.your-message}"
    rounded: "18px"
  ask-dock:
    backgroundColor: "{colors.light-raised}"
    textColor: "{colors.light-text}"
    rounded: "12px"
---

# Design System: Evener Native

## Overview

**Creative North Star: "Attention is the product"** (spec principle 1)

This records the shipped Board, the native iPhone app's home. It replaces the project-first Sessions browser: every live session is ordered by who needs you, then the user's own organization follows. The Board uses native navigation, SF Pro typography, semantic light and dark surfaces, and touch-sized controls. The full design is `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`, section 7; this phase's plan is `docs/superpowers/plans/2026-09-26-iphone-redesign-phase2-board.md`.

The Board is one native scrolling list: Live's bands, then pinned categories, Projects and Archived as plain section rows that open their own screens until a later PR brings them inline.

A row opens the Session, one conversation with its agent, recorded in "The Session" below. The colors, type and shapes that follow serve both.

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
- **Last line** (SF Pro, 13px, 18px line-height, ink-low): Task progress (with a checklist glyph) while a task is in progress, then the project (with a folder glyph) and the host (with a server glyph), each only when it differs from the fleet's usual one. With none of these, the row has no last line.
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
- **Pulse meter:** A 22×15pt, 7-bar activity meter, shown on Working rows in the Live band and, every working session's bars summed minute by minute, as the fleet meter beside the Live summary's counts. One fixed log scale (full at 64 events per minute) so meters compare across sessions; a 1pt baseline always draws, older bars fade, and the meter grays when the connection is down. Each bar is a minute of real transcript and tool-output activity, read every 10 seconds from the hub while the Board is in front (S5, `evener/activity/read`); a read counts only while it's under 20 seconds old (two poll intervals) and the connection is up; a working row with no read yet, a hub that predates S5, a dropped connection, or a connected hub whose reads have silently stopped landing, all show a single bar in its newest minute instead - a stale read would otherwise keep ticking toward a false "stuck" as the gap runs long, whether or not the connection itself ever reported a drop. Quiet for 3 minutes reads "Quiet 4m" in the why line; past 10, "May be stuck · no updates for 12m" in amber, the meter itself goes flat and amber, and the row floats to the top of Working - unless the session is waiting on subagents, which is never quiet or stuck. The row takes a `moving` flag so a still copy of the same session elsewhere shows a plain dot instead, once a later PR renders sessions inline outside Live.

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
- **Do** keep a Board control mounted and disabled, rather than hiding it, while it can't act. The Session hides a control that needs the hub while the hub is away (see the Session's rules below).
- **Do** use human readable state words, and copy that says exactly what happened.
- **Do** persist each section's collapsed state per device.

### Don't:
- **Don't** show a Reconnect or a Refresh control anywhere on the Board, or offer pull-to-refresh: a control appears only when it can act.
- **Don't** move or animate a row unless its underlying data moved.
- **Don't** give a hue more than its one job: amber means a human is needed, green means working, red means failed, blue means tappable, selected or unread. Only the state word takes a hue; the reason stays in ink.
- **Don't** wrap every ordinary row in a rounded card or add decorative shadows.

## The Session

This records the shipped Session, the screen a session opens to: one conversation, what the agent is doing now, and the one place you talk to it. The full design is `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`, section 8; this phase's plan is `docs/superpowers/plans/2026-09-26-iphone-redesign-phase3-session.md`. The Session uses the Board's palette, type roles and shapes above; what follows is what it adds.

**Key Characteristics:**
- One screen from top to bottom: the nav bar, the context chips and notes bar, the transcript, then whatever is live at the bottom (the tray, a dock, the ghosts, the composer).
- One way to send: the composer's Send sends, or queues while the agent works. Stop lives in the tray; steering is something you do to a queued message.
- The transcript shows as much of the agent's work as you choose, per session.

### Session Components

#### The nav bar
- **Back:** The chevron, then the amber count (17pt, attention-ink) of the other sessions that need you, so you know before leaving whether anything is waiting.
- **Title:** The session's name (15pt semibold, one line) over its state line (13pt, ink-mid, tabular figures): a still state mark, the state ("Failed", "Restart needed", "Warning", "Shut down", "Asks a question", "Asks for approval", "Working · 7m", or "Working" before the turn's start is known, "Finished · 35m ago", or "Finished" when no turn has ended) and a small chevron. The mark never animates; the Session's one meter is the tray's. The whole title is one button that opens the Session sheet.
- **Title swipe:** A horizontal pan on the title moves to the next session in Live order (to the left, as a page turns) or the previous one (to the right). It starts only once a drag goes 10pt sideways, and it moves after 60pt or a 500pt/s flick; a pan flicked back the other way moves nowhere. The session slides in from the side it came from. VoiceOver gets "Previous session" and "Next session" actions in its place.
- **⋯ menu:** A native menu: Detail level (with the five levels inline), Find in session, then Files & artifacts, Subagents, Tasks and Notes & links, each only when it can act, Session info, Ask aside… (when the session can fork and the hub is reachable), Pin to category…, Archive, and Shut down (destructive, only when it can). Delete lives in the Session sheet.

#### The connection bar, context chips and notes bar
- **Connection bar:** A 24pt line of 13pt ink-low text under the nav bar, with the Board toolbar's words: "Reconnecting…" once the connection has been down 2 seconds, "Offline · updated 3m ago" after 30, and "Update needed" at once when the hub speaks an incompatible protocol version. A live connection shows nothing.
- **Context chips:** 32pt capsules (16-unit radius, 1pt border, inset fill) in one sideways-scrolling row, each only when it has content: Subagents with its count and "2 failed" in danger ink (`person.2`), Files with its count and a blue dot when a document is new or changed (`doc.text`), Tasks "3/7" (`checklist`), Goal (`target`, amber when blocked) and Queue with its depth (`tray`). Subagents and Tasks open live sheets, so they hide while disconnected; the others always show. Dragging the list down more than 8pt from where the scroll last turned slides the row up in 200ms, behind the connection bar when it shows and behind the nav bar otherwise; any upward scroll, or reaching the top, brings it back. Scrolls the app makes itself (restoring your place, following the latest message) never hide it. The slide is a transform, so the list and your place in it never move.
- **Notes bar:** One 32pt line under the chips, in the same sliding row: "Your note: …" with a person glyph, else "Agent's note: …" with a sparkles glyph, else the one link's label or "3 links", 15pt in ink-hi, with a trailing "3 links" in 13pt ink-mid when a note shows beside links. It shows only when the hub offers shared notes and there is something to preview. Tapping it opens Notes & links.

#### The transcript
- **Detail levels:** Chat (just the conversation), Intent (plus one folded line for each run of steps), Tools (plus every command it ran; tap one for its output), Activity (plus every command's output, open as it arrives) and Full (everything, including the agent's reasoning). The level is chosen per session in the ⋯ menu and remembered on this device; with none chosen, the session shows the hub's display setting. A toast confirms a change, since it often lands above the visible part of the transcript.
- **Your message:** A right-aligned bubble in the accent wash (`#DDEBFC` light, `#2A343D` dark), Source Serif 17/25. A message steered into a running turn carries "Steered in mid-turn" beneath it.
- **Agent message:** Source Serif 17/26 on the page, no bubble; headings in SF Pro, code in Menlo.
- **Time markers:** A centered 13pt marker ("Today 3:00 PM") before the first turn, before a turn that starts after ten quiet minutes, and at a new day.
- **Runs:** Consecutive steps fold into one line: "▸ 3 steps · 1m · read 1 file, edited 1 file, ran npm test", with "(1 failed)" in danger ink. The duration shows only when every step carries its times. Expanded, each step is a line with a check or `xmark.octagon.fill`, its intent and its target in Menlo; a step with evidence opens under its line: command output in a Menlo inset (the first 40 lines, then "Show all 60 lines"), an edit as a diff, the file a write wrote, an error, and the step's images. The step in progress is the tray's line, never the transcript's; the run still growing never folds.
- **Subagent:** The web's shape: a 2pt left rail in the state's hue, no card. The title and "running · 10m" or "failed · 11m" share one line, the latest activity ("Quiet 5m", "Waiting on 3 subagents", or the failure's reason) beneath. Tapping opens its own transcript.
- **Thought:** A settled thought reads "Thought for 12s ›", folded, and opens to the thought in the serif. A live thought is the tray's.
- **Question (history):** An amber left rule, each question in the reading serif, and "You answered: Drop them" beneath. While a question is still open, the dock is the question, and the transcript leaves it out.
- **Your note:** A saved note shows as its own row where the agent was told: a 2pt left rule in the strong edge color, "You updated your note" in 13pt ink-mid, and the note in the your-message serif ("You cleared your note" alone when it was emptied).
- **Document chip:** A document the agent named sits under its message: its kind and title, the file name, and how long ago the session wrote it. Tapping opens the Reader.
- **System event:** A diamond in a 16pt gutter and the event in 13pt ink-low, two lines at most until tapped.
- **Error:** A red left rule, the hub's words as they are, and at most one action: Resume when the session is paused, Sign in when a sign-in failed, otherwise Retry on the latest turn's own failure while Send can act.
- **Scrolling:** A session opens at the right place and never moves what you are reading. New content below reads "↓ 3 new" in a floating capsule; tapping it scrolls to the end. Until the first read lands, three still blocks stand in for the conversation.

#### The status tray and Stop
- **Shape:** One 36pt line above the composer, only while the agent works: the session's pulse meter, what the agent is doing now in 15pt ("Running go test ./cmd/evener-hub/… · 5m", "Waiting on 32 subagents", "Thinking…", "Quiet 40s"), and Stop (`stop.fill`) at the trailing edge in a 44pt target.
- **Behavior:** After ten minutes with no update the line reads "May be stuck · no updates for 12m" in amber, unless the agent is waiting on subagents, which is never stuck. A provider retry reads "Retrying · rate limited · attempt 2 of 5". Tapping the line jumps to the live end. Stop hides while disconnected and is only disabled while a change is in flight.

#### Floating controls
- **Next:** A capsule floating 10pt above the bottom while another session needs you: "Next" in accent ink, then that session's title and a chevron. A tap opens it; touch and hold lists everyone who needs you. The Next capsule, "↓ 3 new" and toasts share one column so none covers another.

#### The ghosts
- **Shape:** A dashed, unfilled bubble (1pt `#B7B6AC` light, `#51514A` dark, 18-unit radius, at most 85% wide) where your message will land, its text in the your-message serif (17/25) in ink-mid, a 13pt ink-low caption beneath (danger ink for a refused message), and its actions as text buttons: Cancel and Discard in ink, the rest in accent ink.
- **States:** Queued ("Queued · sends when this turn ends", with Steer now), held after a Stop ("Held · you stopped this turn", with Send now and Cancel), steering ("Steering · arrives at the next step"), sending ("Sending…"), unconfirmed ("Couldn't confirm this was sent", with Check and Discard) and refused ("Couldn't send this", with Edit and Discard).
- **Behavior:** At most three queued messages show; the rest read "2 more queued", which opens the Queued messages sheet with every one and Steer all now. Tapping a bubble opens the rest of its actions (Edit, "Cancel message"). A queued or held message swipes left to Cancel. A queued message's actions hide while the hub is away; the message itself stays.

#### The ask dock
- **Shape:** An amber-edged card (1pt `#E9C598` light, `#7D583A` dark, 12-unit radius, raised fill: `#FCFBF8` light, `#232320` dark) in the tray's place, the same card for both kinds.
- **Question:** "Question 1 of 2", one question at a time in the reading serif with its why beneath, then its options as radio rows (checkboxes for a multi-select), the recommended one first and marked "Recommended". "Other answer…" answers in your own words through the composer; one primary button reads "Next question", then "Send answers". A chevron folds the dock to one bar that says what waits ("Answer 2 questions"), and the composer returns.
- **Approval:** "Wants to write outside the workspace" with an amber hand, the tool and its target in Menlo (a path wraps only at its slashes), the session's scope ("This session can only write inside its project folder."), then "Allow this file only" (with "It will ask again for the next one") and Deny. There is no redirect, and the composer stays away while an approval waits.

#### The composer
- **Shape:** One rounded field (22-unit radius, 1pt border, raised fill: `#FCFBF8` light, `#232320` dark) holding any ghosts, then the text, which grows to six lines and then offers the full-screen editor, then one controls row that looks the same whether or not the agent works: +, the model chip, and Send.
- **Send:** A 44pt paper airplane on the accent fill, the only Send. Its placeholder and VoiceOver label say what it will do: "Message" and Send, "Tell the agent something…" and Queue message while the agent works, "Message to resume" and Send and resume on a shut-down session, "Answer or ask…" and Send answer while a question is open.
- **+ menu:** Photo library, Camera, and Commands and skills, which opens a sheet of the built-in commands, then skills and plugin commands grouped by plugin; a choice puts its "/name" at the start of the draft.
- **Model chip:** "DeepSeek 4.1 Flash · XHigh ⌄" opens the model sheet: a search field and the Effort control (Low to Max, "Applies from the next turn") in its header, then Recent and each provider's models with their context. It steps aside while the composer answers an open question.
- **In the composer's place:** A session that runs an older Evener and needs a restart, or one that is paused, shows plain text with its one button instead.

#### The Session sheet
- **Shape:** A sheet that opens at large: the title and its state line, then grouped sections.
- **Sections:** Where (host, project, folder in Menlo, branch), Model (the model and effort, and the vision model), Plugins ("10 plugins · chosen at start" and their names), Access (sandbox and network, when the hub reports them), Usage (tokens with their split, cost, work time, a context gauge with no compaction marker, and failed tool calls), Goal (edit, set or clear), Tasks, Notes & links, and Actions (Aside, Fork from latest, Compact context, Pin to category…, Archive, Shut down, and Delete for a saved session that isn't loaded). It shows only facts the thread carries.

#### Notes & links
- **Shape:** A sheet that opens at large with three groups: Your note, Agent and Links.
- **Your note:** An editor while the session can take notes, with a status line beneath: "Your note stays on this session. The agent is told when it changes." while the agent works, and "Your note stays on this session. Saving it will wake the agent." while it rests. Leaving the field saves ten seconds later ("Saves in 10 seconds, or when you close this."), and closing the sheet saves at once; the line then reads "Saving…" and "Saved". A note that can't reach the hub stays on this phone ("Couldn't save your note yet. It's kept on this phone."). A session that can't take notes (shut down, needing a restart, paused, or without shared notes) shows your note as text, read-only.
- **Links:** Each link shows a globe or document glyph, its label, and its URL in Menlo. A web link opens in the in-app browser; a file link inside the session's folder opens in the Reader. Touch and hold offers Open (for a web link), Copy link and, where the session takes notes, Remove link. Where the session takes notes, a link swipes left to Remove ("The agent adds links as it works. Swipe left on one to remove it."); a removed link reads "Link removed. Only the agent can add links."

#### Find in session
- **Shape:** "Find in session" in the ⋯ menu puts a find bar where the chips sit: a field, where you are among the matches, steps to the older and newer match, and Done. The bar holds still while the list moves from match to match.

### Session Do's and Don'ts

#### Do:
- **Do** show the tray only while the agent works, and keep Stop there, never in the composer.
- **Do** keep one meter per view: the tray's. The title's state mark stays still.
- **Do** keep a queued message where it will land, as a ghost, with what it waits for spelled out beneath it.
- **Do** let reads retry on their own, and let the skeleton stand in without a spinner, shimmer or sentence about connecting.

#### Don't:
- **Don't** show connection controls in the composer, or any Reconnect or Refresh control: a control appears only when it can act, and one that needs the hub hides while the hub is away.
- **Don't** move what the reader is reading: new content is announced by "↓ 3 new", and the chips slide away with a transform, never by changing the list's layout.
- **Don't** show the live step or the live thought in the transcript; the tray says what's happening now.
- **Don't** offer a redirect on an approval, or the composer while one waits.
