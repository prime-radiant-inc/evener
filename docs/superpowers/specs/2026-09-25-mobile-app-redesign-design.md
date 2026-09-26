# Evener for iPhone: redesign spec

Date: 2026-09-25. Status: design approved in conversation with Jesse (Board approach, sections 1 and 2); later sections decided under his "I trust you" delegation. A clickable HTML prototype and virtual usability testing accompany this spec (see "Prototype and usability testing").

This document is the full design brief. It is written so that a designer (or Claude Design) can produce every screen from it, and so that an engineer can map every element to its data source. Where the design needs something the hub does not provide today, the gap is named in "Server additions" with the fallback the phone uses until it lands.

## 1. Why a redesign

The current native app (`mobile-native/`) grew screen by screen out of protocol capabilities. An audit at `64e8323ae` found:

- One navigation stack with 19 routes, 22 modal sites, 19 alert dialogs and no tab bar or overview. Home is a project tree with only the first project expanded.
- Nothing answers "what needs me?". The hub already publishes a needs-you section, but home never shows it. A pending question is 13pt gray text styled like a branch name.
- Every control is blue text (one `Action` component, 249 call sites). State has no color or shape language.
- Raw internal values and plumbing reach the screen: `restartRequired`, `write_file · completed`, "This session's daemon has exited", about 45 distinct Retry/Refresh/Reconnect labels.
- The composer is a control panel: up to eleven controls in one footer, Stop as gray text after "Recovery", and the text field disappears when the agent asks a question.
- Transcript tool calls show raw names and JSON in a proportional font. There are no timestamps and no diffs.
- Desktop administration crowds the phone: editing web keyboard shortcuts by typing "Meta+Shift+P", pasting OAuth redirect URLs, starting sessions from a hub filesystem path.
- Deleting a running session takes about ten taps. There are no swipe actions, long-press menus or haptics.

Jesse's standing feedback on mobile: "you're not making good use of visual space"; the send button belongs on the controls row, not the text row; model and effort controls belong in the composer; the app must refresh itself and never show "refresh to see the latest" (#1257); sessions should be organized by project, scrolling should load more, and opening a session must feel fast.

## 2. Who this is for, and how they work

The primary user is a power user running Evener as an engineering organization. Real usage on Jesse's hub (magic-kingdom), last 14 days, measured 2026-09-25. Jesse notes these numbers are the bottom of the range; design for several times more.

| Measure | Value |
|---|---|
| Top-level sessions started | 102 (about 7 a day) |
| Subagents spawned | 2,516 (about 25 per top-level session) |
| Subagent tree size | median 5, p90 54, max 467; nesting up to 3 deep |
| Top-level session length | median 204 turns and 46M tokens; p90 1,879 turns |
| Peak concurrency | 16 top-level sessions; about 500 counting subagents |
| Projects | 14, with 88% of sessions in one (evener) |
| Provider profiles in use | 10, about 12 models; effort mostly xhigh or high |
| Plugins | 14 installed from 5 marketplaces; each session runs 6 to 13 of them |
| Hosts | 2 (magic-kingdom, paradise-park over SSH) |
| Organization features | 1 pin, 0 favorites, 271 archived |

What this means for the design:

- The thing being managed is a fleet of long-running coordinators, each running a swarm of subagents. It is not a list of chats.
- Subagents must be summarized on their parent and never listed flat at the top level.
- A project-first home puts nearly everything in one bucket. Home is ordered by attention; project and host organization sit below it.
- Model, effort and plugin choice at launch matter because plugins, sandbox and host are fixed once a session starts.

## 3. Goals, non-goals, success

The phone is a full workbench. Jesse runs many sessions entirely from it for long stretches, reads whole transcripts, and reviews plans and artifacts (documents and interactive views the agents produce). Diff review is secondary.

Success looks like:

1. With 50 live coordinators, you always know which one needs you, without hunting.
2. Reading a plan or artifact and sending feedback on it feels comfortable on a phone.
3. Starting a session with the right host, project, model, effort and plugins takes seconds.
4. The app never loses your place: drafts, reading position, filters and scroll survive reconnects, backgrounding and relaunch.

Non-goals for this version: a decision inbox (a future design; Jesse has not designed it yet), iPad, Android, voice, OS push notifications (designed here as phase 2, not built), a cross-session document library, and desktop-only administration (keyboard shortcuts, raw launch configuration fields, AGENTS.md editing, MCP server configuration).

## 4. Principles

Every screen is checked against these.

1. **Attention is the product.** Every screen answers "what needs me next?". One ordering everywhere: failed, then needs you, then finished, then working, then idle.
2. **Summarize the swarm.** A coordinator's subagents appear on it as counts and a proportional strip. Detail is one tap away.
3. **Read like a book, act like a remote.** Reading surfaces get a real reading typeface and a comfortable measure. Controls are compact and within thumb reach.
4. **Honest liveness.** Show only real signals: what it is running, "quiet 40s", "no updates for 12m", "waiting on 8 subagents". No decorative spinners. Motion is evidence.
5. **Never lose my place.** Drafts, scroll and reading positions, and filters survive everything. The app refreshes itself. Reconnecting never blanks a screen.
6. **One word per thing, no plumbing.** No daemon, harness, thread, delegate, drain or runtime in the interface.
7. **Color is meaning.** Four hues, one job each, inherited from the web: amber means a human is needed, green means working, red means failed or destructive, blue means tappable, selected or unread. Everything else is ink on paper.
8. **Native chrome, Evener content.** iOS supplies the structure (glass bars, sheets, swipe actions, context menus, haptics, Dynamic Type). Evener supplies the content's voice (the web's reading serif and palette).

## 5. Vocabulary and copy

| Say | Never say |
|---|---|
| Session | thread |
| Subagent | delegate |
| Host | source, controller, daemon |
| Hub (only in connection and settings) | server, endpoint |
| Question, Approval | ask_user, escalation, sandbox exemption |
| Needs you | awaiting, attention level |
| Finished (the agent finished its turn; a blue dot means you haven't looked yet) | your move, idle, awaiting |
| Working, Quiet, May be stuck | active, streaming, stalled |
| Failed | errored, systemError, error (as a state) |
| Steer (arrives at the agent's next step) | inject, interrupt and redirect |
| Queue (waits until the current turn ends) | drain, promote |
| Stop (ends the current turn; the session stays open) | interrupt, cancel in-flight turn |
| Shut down (closes the session; sending a message resumes it) | stop runtime, stop daemon, force stop |
| Detail level | verbosity, transcript display, transcript detail |
| Plan, Document, Artifact | doc pane, file, resource |
| Category (a named group of pinned sessions) | pin section |
| Pin (a session to a category; a project to the top) | favorite |
| Effort | reasoning effort (in tight spaces) |
| Aside (a side session forked from the latest point) | side thread |
| "N other sessions need you · Next" (moves to the next one) | "N more" |

Copy rules:

- Sentence case everywhere. Uppercase only for section labels of one or two words, with letter-spacing.
- Buttons say what happens: "Send answer", "Allow once", "Start session", "Shut down". The result echoes the verb: "Answer sent", "Session shut down".
- Errors say what went wrong and what to do, in one sentence, with one action: "paradise-park couldn't find /home/jesse/git/evener. Choose another project."
- Never narrate plumbing. The phone handles retries itself and speaks only when a person must act.
- Numbers use tabular figures. Durations are compact: 40s, 12m, 3h, 2d. Token counts: 39.8K, 46M. Cost: "~$4.12" (the hub's estimate string).

## 6. Structure and navigation

No tab bar. The app is a list-then-detail workbench, like Mail: one home, one place where work happens, and occasional destinations reached from the home header.

```
Board (home)
├── Notices (hub-level problems, only when present)
├── Session
│   ├── Subagents → one subagent (read-only transcript)
│   ├── Files & artifacts → Reader | Artifact viewer
│   ├── Session sheet: where, model, effort, plugins, access, usage, goal, tasks, notes, actions
│   └── Queue, tasks, notes (sheets)
├── New session (sheet)
├── Search (from the header)
└── Hub (from the header button): hosts, providers, plugins, recipes, display, alerts, hubs, about
```

Movement:

- Push/pop between Board, Session, Subagents and Reader with the standard iOS slide. The left-edge swipe goes back everywhere.
- Sheets (New session, Session sheet, Hub, pickers) use system detents: medium and large. Swipe down dismisses. Sheets with unsaved input ask before discarding.
- Inside a session, the **Next** pill moves to the next session that needs you with a lateral slide. Swiping left or right on the session's title bar moves to the previous or next session in Live order, with a selection haptic.
- Tapping an in-app banner opens that session at the relevant spot and pushes it onto the stack; Back returns to where you were.

## 7. The Board

The Board is home. It shows every session, ordered by who needs you, followed by the user's own organization.

### 7.1 Layout

```
◉ magic-kingdom ▾                              ⌕
[Live 17 (3)] [Pinned 3] [Projects 14] [Archived 271]

⚠ codex-jesse-fsck.com sign-in expired · 3 sessions          Sign in

NEEDS YOU · 3
✕  Fix Endless Provider Retry Loop                 2m
   Failed: provider returned 429 after 5 retries
   ▰▰▱ 4 subagents · evener · paradise-park
?  Audit Tool Descriptions for Implied Options    14m
   Asks: keep or drop the implied options?
   ▰▰▰▱▱ 8 subagents · evener
✋ Mirror Docs Site Locally                        21m
   Wants to write outside the workspace: ~/sites/docs
FINISHED · 4
•  Host Project Hierarchy UI Mockups               1h
   "Three layouts are ready. I recommend B because…"
   [Plan] hierarchy-plan.md   [Artifact] Hierarchy layouts
WORKING · 12
▂▅▇  Get PR 2138 Test Clean                        38m
   Waiting on 31 subagents
   ▰▰▰▰▰▰▱▱▱ 54 subagents, 2 failed · evener
Idle · 6                                            ›
PINNED
  Release                                           ⋯
  …
PROJECTS                          Project, then host ⇄
  📌 evener                                         ▾
     magic-kingdom · 12 live                        ▾
     paradise-park · 2 live                         ›
  c-to-wasm                                         ›
Test runs · 3                                       ›
ARCHIVED · 271                                      ›
─────────────────────────────────────────────────────
Select              Live                            ✎
```

- **Header (glass nav bar).** Leading: the hub button (hub name, a connection dot, a chevron). Trailing: Search. No large title; the section chips carry orientation.
- **Section chips (sticky under the header).** Live, Pinned, Projects (or Hosts), Archived, each with a count. Tapping scrolls to that section. The Live chip carries an amber badge with the Needs you count when it is above zero. Chips for empty sections are hidden.
- **Notices (only when present).** Hub-level problems that block sessions: a provider sign-in expired or expiring within a day, a host offline, a plugin marked broken. A host on a different version than the hub is shown in Hub > Hosts, not here, because it doesn't block work. One row each: a mark, one sentence naming the affected count, and one action ("Sign in", "Reconnect", "Update host"). Notices dismiss themselves when resolved.
- **Continue reading (only when present).** Leaving a plan or document before its end leaves one row under the notices for two hours: "Continue reading · 62%" and the document's title. Tapping it reopens the document at the same position, inside its session. This is the way back after an interruption.
- **Live** holds every live, unarchived top-level session, in four bands:
  - Needs you: failed first (oldest failure first), then questions, approvals, warnings and restart-needed, oldest waiting first.
  - Finished: sessions whose turn ended, newest first. A blue dot marks the ones you haven't opened since.
  - Working: stable order by start time, newest first. Sessions that "may be stuck" float to the top of this band.
  - Idle: finished sessions you have already seen, collapsed by default, most recent first.
  Band headers show counts. Empty bands are omitted.
- **Pinned** shows the user's categories in the order the hub returns them. Each category is a collapsible group with a ⋯ menu (Rename, Delete). Deleting a category unpins its sessions; it never deletes sessions. A pinned session also appears in Live while it is live, so pinning never hides attention.
- **Projects / Hosts** mirrors the web's "Organize by" control: "Project, then host" (default) or "Host, then project". The toggle sits in the section header, flips the section title between Projects and Hosts, and appears only when more than one host exists. Pinned projects float to the top with a pin mark. Inside a project, sessions split like the web: today, recent, and a folded archived group. Each project and host row shows its live count.
- **Test runs** (collapsed): projects whose sessions all came from test runs, as on the web.
- **Archived** (collapsed): archived sessions and projects, newest first. Unarchive from the row's swipe or menu.
- **Bottom toolbar (glass).** Leading: Select (multi-select mode with Archive, Pin, Mark as read). Center: the connection status ("Live", "Reconnecting…", "Offline · updated 3m ago"). Trailing: New session.
- Every section's collapsed state persists per device.

### 7.2 Row anatomy

Signal rows (needs you, finished and not yet seen, working) have up to three lines; quiet rows (idle, and rows inside Projects and Archived) have one.

| Part | Spec |
|---|---|
| Leading mark | 28pt column. State mark (see 13.1). For working rows the mark is the pulse meter. |
| Title | SF Pro semibold 17/22, one line (two for Needs you), tail truncation. |
| Age | Trailing on line 1, 13pt tabular, ink-low. "2m", "1h", "3d". For working rows, time since the session last started a turn. |
| Why line | 15/20. Needs you: the reason in the state's ink color. Finished (not yet seen): an excerpt of the last agent message in ink-mid, quoted, up to two lines. Working: the current activity in ink-mid ("Running go test ./agent/...", "Thinking", "Waiting on 31 subagents", "Quiet 4m"; "May be stuck · no updates for 12m" in amber ink after 10 minutes). |
| Attachments (finished, not yet seen) | Up to two chips for documents or artifacts named in the final message: "[Plan] hierarchy-plan.md", "[Artifact] Hierarchy layouts". Tapping a chip opens it directly. |
| Meta line | 13/18, ink-low: the subagent strip (48×4pt) with "54 subagents" and ", 2 failed" in red ink when above zero; then project; then host (only when more than one host exists); then the model's short name in SF Mono (truncated first). |
| Draft tag | A small blue "Draft" tag before the age when the session has an unsent draft. |

Row heights: signal rows about 76 to 88pt; quiet rows 48pt. Horizontal padding 16pt. Hairline separators inset to the title.

### 7.3 Row interactions

- **Tap** opens the session at the right spot: the pending question or approval, the start of the unread result, or the live end of the transcript.
- **Swipe right** (leading): Archive (blue-gray action). Full swipe archives. An "Archived · Undo" toast appears at the bottom for 8 seconds. Swipes that begin in the 24pt screen-edge zone never act on a row, so the system back gesture can't archive anything (in round 1 it did, on the root screen, where there is nothing to go back to).
- **Swipe left** (trailing): Stop (only when working; ends the current turn), Pin, More (the long-press menu).
- **Long-press** opens a context menu with a preview card (title, state, why line, subagent strip, model and effort, last message excerpt) and actions: Open, Pin to category… (category list plus "New category…"), Mark as read / Mark as unread, Stop, Shut down, Archive, Copy link, Rename.
- **Pull down** at the top reveals Search.
- The list never reorders while a finger is on it or it is scrolling. Changes apply when the list settles, rows move with a 250ms spring, and a row entering Needs you gets a brief amber wash (1.2s fade).

### 7.4 Search

Tapping Search (or pulling down) shows the search field at the top with scope chips: All, Live, Archived. Results come from `evener/search` and group into Sessions (title and prompt matches with their state mark), In sessions (message text matches with a highlighted snippet) and Projects. Tapping an "In sessions" hit opens that session scrolled to the hit, with the matched text highlighted. Recent searches show when the field is empty.

### 7.5 Board states

- **First launch after pairing:** three skeleton rows, then content. After that, the Board opens instantly from its cache and updates live.
- **Nothing live:** "Nothing's running. Start a session to put an agent to work." with a New session button, then Pinned, Projects and Archived as usual.
- **Reconnecting / offline:** content stays visible and navigable. The toolbar status changes. Rows keep their last known state; the pulse meters stop moving and gray out. Actions go to the outbox (section 14).

## 8. Session (the workbench)

### 8.1 Layout

```
‹ 3      Get PR 2138 Test Clean           ⋯
         ▂▅▇ Working · 38m
[Subagents ▰▰▰▱ 54] [Files 3 •] [Tasks 3/7] [Goal] [Queue 1]
────────────────────────────────────────────
                     Today 2:14 PM
                 ┌──────────────────────────┐
                 │ Also cover the empty      │
                 │ state please              │
                 └──────────────────────────┘
The flaky test is caused by a race between the
tree settle pass and the retirement drain. I'm
splitting the fix into two subagents…

▸ 12 steps · read 6 files, ran go test (2 failed), edited 3 files
◇ Subagent · Fix race in tree settle           running · 4m
[Plan] docs/superpowers/plans/2026-09-25-settle-race.md
────────────────────────────────────────────
  2 other sessions need you                  Next ›
▂▅▇ Running go test ./agent/... · 42s
┌──────────────────────────────────────────┐
│ Message                                  │
│ +   glm-5.3 · xhigh   /             ■    │
└──────────────────────────────────────────┘
```

- **Nav bar (glass).** Back shows an amber count of sessions that need you (excluding this one). The center holds the title (15pt semibold, one line) and a subtitle with the state mark, the state ("Working · 38m", "Finished", "Asks a question", "Failed") and a small chevron that says the title is tappable. Tapping the center opens the Session sheet; it has a pressed state and sits clear of Back. Trailing: the ⋯ menu.
- **Context chips (under the nav bar; hide on scroll down, return on scroll up).** Subagents (with a mini strip and the count; a red dot if any failed), Files (count; blue dot when something new or changed), Tasks (done/total), Goal (when set; amber when blocked), Notes (when either note exists), Queue (count, when non-empty). Chips appear only when they have content.
- **Transcript** (section 8.2).
- **Status tray** (section 8.3), replaced by the **Ask dock** when a question or approval is pending (section 8.4).
- **Composer** (section 8.5).

### 8.2 Transcript

The default detail level on the phone is Intent, matching the hub's shipped mobile default. The "Detail: Intent" chip and the ⋯ menu open the level picker, where every level says what it shows (round 1 participants could only guess):

| Level | Shows |
|---|---|
| Chat | Just the conversation |
| Intent | Plus one line for each step the agent took |
| Tools | Plus every command it ran; tap one for its output |
| Activity | Plus system events, like compaction and model changes |
| Full | Everything, with command output shown |

These are the hub's levels; Custom lives in Hub > Display. The choice is per session and remembered.

| Item | Rendering |
|---|---|
| Time marker | Centered caption, ink-low: "Today 2:14 PM". Shown at a turn start after a gap of 10 minutes or more, and at day changes. |
| Your message | Right-aligned bubble, max 85% width, accent tint (accent at 12% on surface), SF Pro 17/24, continuous 18pt radius. Steered messages carry a small "Steered" caption; queued messages that were delivered carry "Queued". Long-press: Copy, Fork from here, Quote. |
| Agent message | No bubble. Source Serif 4, 17/26, ink-hi, full width with 16pt margins. Markdown: headings in SF Pro semibold (20/17/15), lists, block quotes with a 2pt ink-low rule, tables and code blocks in their own horizontally scrolling insets, links in accent ink. Paths to files become document chips. Long-press: Copy, Quote in reply, Select text. |
| Activity run | One collapsed line per run of tool calls: "▸ 12 steps · read 6 files, ran go test (2 failed), edited 3 files" in 14pt ink-mid. Failure counts show in red ink even when collapsed. Tap to expand into one line per step: intent sentence, target in SF Mono, and a status mark. Tapping a step shows its evidence: command output in an SF Mono inset (first 40 lines, then "Show all 412 lines" which opens a full-screen log viewer), diffs as unified hunks with the web's add/delete washes and a "+18 −4" summary. A live run never folds. |
| Thinking | Settled: "Thought for 12s ›" (collapsed). Live: "Thinking… ~1.2K tokens" with the one sanctioned pulse. |
| Subagent launch | A row with a ◇ mark: "Subagent · Fix race in tree settle" and a state pill (running · 4m, done, failed). Its latest line appears beneath in ink-mid while running. Tap opens the subagent. A finished subagent's report appears as "✓ Subagent finished · <first line of its report>". |
| Document chip | The kind (Plan, Spec, Doc, Code, Image), the document's own title from its first heading in the reading serif, then the file name, line count and age. Tap opens the Reader. A blue dot and "changed since you last read" mark a document that changed since you last opened it. |
| Artifact card | Title, one-line summary, a static preview image when available, version ("v3"), and "Open". If you answered its proposal, the card says so ("You chose B"). |
| Question (history) | Amber left rule, the question, and your answer beneath ("You answered: Drop them"). |
| Approval (history) | "Allowed: write ~/sites/docs" or "Denied: …" in ink-mid with the mark. |
| System event | A ◇ gutter mark with 13pt ink-low text, shown at Tools level and above: "Context compacted · 412K → 38K tokens", "Model changed to glm-5.3-vision", "Plugin superpowers loaded". Model changes and compactions also show at Intent. |
| Error | Red left rule, the error in plain words, and one action (Retry, Resume, Sign in). |
| Images | Thumbnails in a row (96pt), tap for a full-screen viewer with swipe between images. |

Scrolling:

- The session opens at the right spot (section 7.3). Older history loads automatically as you scroll up; there is no "Load more" button.
- New content streams in without moving what you are reading. When you are not at the bottom, a "↓ 3 new" pill appears above the tray; it is hidden when you are at the bottom.
- Reading position persists per session.

### 8.3 Status tray and Next

A single 36pt line above the composer:

- Working: the pulse meter and the current activity with elapsed time ("Running go test ./agent/... · 42s", "Thinking… · 1.2K tokens", "Waiting on 12 subagents", "Quiet 40s", "May be stuck · no updates for 12m" in amber ink).
- Finished: "Finished 4m ago" in ink-low.
- Shut down: "Shut down · sending a message resumes it".
- Whenever other sessions need you, a full-width **Next bar** sits above the tray or ask dock: "2 other sessions need you · Next ›" on an amber wash. Tapping it moves to the next session that needs you (Needs you order), opening it at its question or failure. It lives outside the dock on purpose: a count inside the dock was read as "more questions in this session".

Tapping the activity text jumps to the live end of the transcript.

### 8.4 Ask dock

When the session has a pending question or approval, an amber-edged dock replaces the status tray. The composer stays available below it.

**Question** (the agent's `ask_user`; top-level sessions only; 1 to 4 questions per ask, 2 to 5 options each):

```
┌ QUESTION 1 OF 2 ───────────────────────── ⌄ ┐
│ Keep or drop the implied options?           │
│ Several tool descriptions imply flags that  │
│ the tools don't accept.                     │
│ ◯ Drop them                    Recommended  │
│   Remove the implied options from all 14    │
│ ◯ Keep them and add the flags               │
│ ◯ Ask me per tool                           │
│ Other answer…                               │
│                               Next question │
└─────────────────────────────────────────────┘
```

- Header: "Question 1 of 2" and a collapse chevron. The question in SF Pro semibold 17; the agent's "why" in 15 ink-mid.
- Options as full-width rows with label and detail; the recommended option carries a "Recommended" tag and is listed first. Single-select uses radio marks; multi-select uses checkboxes.
- "Other answer…" moves focus to the composer with the question quoted.
- The last question's primary button is "Send answer" (or "Send answers"). Answers go out as one message, the same way the web and current native compose them.
- Collapsing the dock leaves a slim amber bar: "Answer 2 questions".

**Approval** (sandbox escalation; blocks the agent mid-step):

```
┌ APPROVAL NEEDED ────────────────────────────┐
│ Wants to write outside the workspace        │
│ write_file  ~/sites/docs/index.html         │
│ Sandbox: workspace write                    │
│                     Deny      Allow once    │
└─────────────────────────────────────────────┘
```

- The tool and target in SF Mono, and one plain sentence of scope in ink-mid ("This session can only write inside its project folder. It's about to write the first of 214 pages."). "Deny" (secondary) and "Allow once" (primary). The result shows as a toast ("Allowed once") and in the transcript history.
- "Allow once" covers exactly one action, so a batch job asks again for its next file. When the action targets a folder, a full-width row under the buttons offers **Allow all writes in ~/sites/docs for this session**, which ends the prompts (server addition S12).

### 8.5 Composer

Layout: the text field on top (grows to six lines, then scrolls; an expand control opens a full-screen editor), and a controls row beneath it, per Jesse's ruling that send sits on the controls row.

Controls row, left to right:

- **+**: Photo library, Camera. Attached images show as removable thumbnails above the text.
- **Model chip**: "glm-5.3 · xhigh". Opens the model sheet: recent models first, then all models grouped by provider, each with context size and price; an Effort segmented control at the bottom showing only the levels the model supports. Changes apply from the next turn ("Applies from the next turn").
- **/**: Commands and skills. A sheet with search, built-in commands first (Goal, Compact context, Aside, Tasks, Model, Effort, Clear), then skills grouped by plugin. Choosing one inserts it as a token.
- **Primary button** (trailing), by state:

| Session state | Field empty | Field has text |
|---|---|---|
| Idle / finished | Send (disabled) | **Send** (accent, arrow up) |
| Working | **Stop** (square, ink fill) | **Steer** (accent, primary) and **Queue** (secondary text button to its left) |
| Question pending | Send (disabled) | **Send** (sends as the answer; the dock updates) |
| Shut down | Send (disabled) | **Send** (resumes the session) |
| Offline | Send (disabled) | **Send later** (goes to the outbox) |

- The first time Steer and Queue appear, a one-line hint sits above the field: "Steer arrives at the agent's next step. Queue waits until this turn ends." It does not return after two uses.
- **Queued messages** appear as dashed ghost bubbles above the composer: "Queued · sends when this turn ends". Tap for Steer now, Edit, or Cancel. Swipe left to cancel.
- **Steering in flight** appears as a ghost bubble "Steering · arrives at the next step" until the agent picks it up; then it becomes a normal message with the "Steered" caption.
- **Stop** ends the current turn immediately (no confirmation; stopping is recoverable by sending again). A toast confirms: "Stopped".
- Placeholder copy: "Message" (idle), "Tell the agent something…" (working), "Answer or ask…" (question pending), "Message to resume" (shut down).
- The draft persists per session across navigation, backgrounding and relaunch.

### 8.6 Session sheet

Opened by tapping the title. A large-detent sheet:

- **Title** (tap to rename) and state line.
- **Where:** host (with its connection dot), project, working directory (SF Mono), branch or worktree lane.
- **Model:** model and effort (tappable, same sheet as the composer chip); vision model if set.
- **Plugins:** "10 plugins · chosen at start" and the list. Footer: "Plugins are chosen when a session starts. To change them, start a new session or fork this one."
- **Access:** sandbox mode and network, read-only.
- **Usage:** total tokens with input/output/cache split, estimated cost, work time, a context gauge (used of window, with a marker at the compaction threshold), and failed tool calls when above zero.
- **Goal** (edit), **Tasks** (list with current task highlighted), **Notes** (your note, the agent's note, links).
- **Actions:** Aside (ask something in a side session), Fork from latest, Compact context, Copy link, Pin to category…, Archive, Shut down (destructive, confirms), Delete (only when shut down, destructive, confirms).

### 8.7 ⋯ menu

Detail level ▸, Find in session, Files & artifacts, Subagents, Tasks, Notes, Aside, Pin to category…, Archive, Shut down.

## 9. Subagents

Opened from the Subagents chip or a subagent row in the transcript.

```
‹ Back        Subagents · 54
              Get PR 2138 Test Clean
▰▰▰▰▰▰▰▰▰▰▰▰▰▰▰▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱
[All 54] [Running 31] [Waiting 3] [Failed 2] [Done 18]

✕  Fix race in tree settle                        6m
   Failed: go test exited 1 (3 times)
   glm-5.3 · lane fix-settle-race · 1.2M tokens
▂▅▇ Explore retirement drain callers              2m
   Reading agent/retirement.go
   ├ ▂▃▅ Check drain ordering in tests            1m
Done · 18                                          ›
```

- A full-width proportional strip (running green, waiting ink-mid, failed red, done ink-low), then filter chips with counts. Default filter: All.
- Order: failed first, then running (newest first), then waiting, then done (folded as "Done · 18").
- Rows: state mark (pulse meter for running, ✕ failed, ✓ done, a hollow ring for waiting on the coordinator), mandate as title, latest activity or outcome as the why line, and a meta line with model, "own branch fix-settle-race" when the subagent works in its own worktree, elapsed and tokens.
- Nesting shows two levels inline with a thin tree rule; deeper levels collapse behind "›".
- The list virtualizes; trees of 500 must scroll smoothly. A search field filters by title.
- **Subagent transcript** opens read-only with the same renderer. A banner at the top: "Subagent of Get PR 2138 Test Clean. Talk to it through its coordinator." The composer is replaced by an action bar: **Ask coordinator to stop it** and **Open coordinator**.
- **Ask coordinator to stop it** opens a sheet with a prefilled, editable steer to the parent ("Stop subagent 'Fix race in tree settle': it has failed three times.") and Steer / Queue buttons. When the hub gains a direct stop call (server addition S6), this becomes **Stop subagent** with a confirmation and no message.

## 10. Review: plans, documents and artifacts

Plans and artifacts are the main things reviewed on the phone.

### 10.1 Files & artifacts

A sheet listing everything the session wrote or linked, newest first: documents (with type labels Plan, Spec, Doc, Code, Image, the path in SF Mono, line count and last update) and artifacts (title, version, last update). Blue dots mark items new or changed since you last opened them.

### 10.2 Reader

```
‹ Back   settle-race.md                 ☰  ⋯
         Plan · updated 3m ago
3 changes since you last read          ‹  ›
───────────────────────────────────────────
# Fix the settle/drain race
The retirement drain and the tree settle pass
both take the tree lock, but…              💬 1
▎ Changed: the drain now waits for settle.
…
───────────────────────────────────────────
Comments 2        Reply        Send review
```

- Full-screen reading surface: Source Serif 4 18/28, ink-hi on page, 16pt margins. Headings in serif semibold. Code blocks in SF Mono 13 insets that scroll horizontally. Task lists render as checkboxes (read-only). Tables scroll horizontally.
- Header: the document's title (its first heading), with the kind, file name and age as the subtitle; the outline button (headings list for jumping); and ⋯ (Open in session, Copy path, Copy text).
- **Changes since you last read:** changed paragraphs get a blue left rule; a summary line at the top ("3 changes since you last read") with previous/next arrows.
- **Comment:** long-press a paragraph (or select text) for Comment, Quote in reply, Copy. A comment attaches to the paragraph; the paragraph shows a comment marker with a count. Comments are drafts until sent and persist per document.
- **Review bar (bottom):** Comments (count; opens the list), Reply (opens the session composer with nothing quoted), Send review.
- **Review** sheet (titled "Review" so its title never repeats the "Send review" button): choose Approve, Request changes, or Comment only; an optional overall note; the comments listed with their quoted paragraphs. The primary button matches the session state: Send (idle), Steer and Queue (working). The message format:

```
Review of docs/superpowers/plans/2026-09-25-settle-race.md: request changes.

> The retirement drain and the tree settle pass both take the tree lock…
Split this into two steps; the drain should never wait on settle.

Overall: close. Fix the ordering and go.
```

- Documents over 512 KB show the first 512 KB with "Showing the first 512 KB of 1.3 MB".
- Non-markdown files render as code with line numbers (SF Mono) or as images; binary files show a notice.
- Reading position persists per document. Leaving before the end leaves the Board's "Continue reading" row (section 7.1).

### 10.3 Artifact viewer

Artifacts are interactive HTML views an agent publishes (the shared-artifacts work on the `codex/shared-artifacts-*` branches, not on main yet). The viewer:

- Full-screen, with a thin top bar: back, title, "v3 · updated 2m ago", and ⋯ (About: summary, versions; Diagnostics).
- The artifact runs in the sandboxed viewer and can save its own state; a small "Saved" appears in the bar when it does.
- **Proposals:** when the artifact proposes a message to the session (its `ui/message`), a sheet rises: "From the artifact", the proposed text (editable), and Discard plus Send (idle) or Steer and Queue (working). Nothing is sent without that explicit choice.
- When the artifact cannot run on this connection (remote use without the sandbox host), the viewer shows its title, summary and last static preview, with one sentence: "This artifact can't run over this connection. Open it on a computer on the same network as the hub."

## 11. New session

A full-height sheet from the Board's New session button (or the ⋯ menu's "New session like this" on a session, which copies its setup).

```
Cancel          New session          Start
┌──────────────────────────────────────────┐
│ What should the agent do?                │
│                                          │
│ +                                        │
└──────────────────────────────────────────┘
[Last used] [Evener coordinator] [Quick question] [+]
Host        magic-kingdom ●                    ›
Project     evener                             ›
Model       GLM 5.3 Vision · lunaroute         ›
Effort      [Low][Med][High][XHigh][Max]
Plugins     10 of 14                           ›
Access      Workspace write                    ›
Branch      Current branch (main)              ›
More options                                   ›
```

- **Prompt** first, focused on open, SF Pro 17; dictation works through the keyboard; + attaches images.
- **Recipes:** chips for "Last used" (selected by default; last used for the chosen project), saved recipes, and "+" to save the current setup as a recipe. A recipe sets host, project, model, effort, plugins, access and branch.
- **Host:** hosts with connection state. Offline hosts are disabled and offer "Connect". Changing host keeps the project when it exists on the new host; otherwise it switches to that host's most recent project and says so under the list.
- **Project:** recent projects on the chosen host (name and path), then "Browse folders on <host>…" with path completion and "New folder".
- **Model:** recent (up to five), then all models grouped by provider profile. Each row: display name, provider profile, capability icons (vision, tools), context size, price per million tokens.
- **Effort:** a segmented control showing only the levels the model supports. The model sheet opened from here has no effort control of its own, so effort lives in one place in this flow.
- **Plugins:** "10 of 14" opens a checklist grouped by marketplace, with All and None. Each row: name, one-line description, counts (skills, agents, commands, hooks, MCP servers), and any preview warning. Footer: "Plugins can't be changed after the session starts." Start is disabled with an explanation while a selected plugin has a blocking problem.
- **Access:** Full access, Workspace write, Read-only, Restricted; network on or off.
- **Branch:** current branch, or a new worktree branch (name field).
- **More options:** the few launch settings worth touching on a phone (context strategy, max subagent depth, max turns), each showing the hub default. Everything else stays at hub defaults.
- **Start** creates the session and replaces the sheet with the new session, live. If the hub rejects the start, the sheet stays open with the hub's reason inline and the field it concerns highlighted.

## 12. Hub (settings and fleet)

Opened from the hub button. A large-detent sheet with a grouped list.

- **Header:** hub name, connection state, version ("evener 0.9.412 · up to date" or "Update available").
- **Hosts:** each row shows name, state (Connected, Connecting, Offline · last seen 2d, Error), OS and architecture, version (with a warning when it differs from the hub), and live session count. Host detail: status, version, roots, last error, and actions: Connect / Reconnect (showing "Connecting…" while it tries), Update host (when versions differ; disabled with "Reconnect first" while the host is offline, because the update runs over the same connection), Edit and Remove (only for hosts added from the app or web; hosts from `hub.toml` are read-only and say so).
- **Providers:** instances with sign-in status (Signed in, Expires in 3d, Sign-in expired in amber, Key set, Error). Detail: default model, models, Sign in (device code flow: the code, a copy button, "Open sign-in page", and automatic completion), Replace key (paste), Test.
- **Plugins:** Installed (on-by-default switch, update badge, Upgrade, Remove), Marketplaces (add by GitHub repo or URL, refresh, remove), Browse and Install.
- **Recipes:** list, edit, reorder, delete.
- **Display:** Appearance (System, Light, Dark), Reading font (Serif, Sans), Default detail level, Show model on Board rows.
- **In-app alerts:** banners for failures (on), questions and approvals (on), finished results (off); hold alerts while reading (on); haptics (on).
- **Hubs:** the connected hub, add a hub (scan pairing code or paste link), switch, remove.
- **About:** phone app version, hub version, Update hub (confirms).

Not on the phone, with a footer line "More settings are in the web app": keyboard shortcuts, raw launch configuration fields, AGENTS.md, MCP servers, storage paths and daemon tuning.

## 13. Attention system

### 13.1 States

| Hub signal | Phone state | Mark | Band | Why line |
|---|---|---|---|---|
| `errored` / `systemError` | Failed | ✕ red (`xmark.octagon.fill`) | Needs you | "Failed: <error summary>" |
| `awaiting` + `askPending` | Question | ? amber (`questionmark.circle.fill`) | Needs you | "Asks: <first question>" |
| pending sandbox escalation | Approval | ✋ amber (`hand.raised.fill`) | Needs you | "Wants to <action> <target>" |
| `warning` | Warning | ⚠ amber (`exclamationmark.triangle.fill`) | Needs you | the warning text |
| `restartRequired` | Restart needed | ↻ amber (`arrow.clockwise.circle.fill`) | Needs you | "Needs a restart to finish updating" |
| `active` | Working | pulse meter, green | Working | current activity |
| `active`, no activity 3 to 10 min | Quiet | flat pulse meter | Working | "Quiet 4m" |
| `active`, no activity 10 min or more | May be stuck | amber hollow ring | top of Working | "May be stuck · no updates for 12m" (amber ink) |
| turn ended, not seen since | Finished | blue dot (`circle.fill`, 8pt) | Finished | last message excerpt |
| turn ended, seen | Idle | none | Idle (collapsed) | age only |
| shut down / not loaded | Shut down | none | Projects only | age only |

Marks always pair shape with color so they read without color.

### 13.2 Counts

- **Needs you** = Failed + Question + Approval + Warning + Restart needed, over live, unarchived, top-level sessions. This is the single number used on the Live chip badge, the session Back button and the Next pill.
- Finished and Working counts appear only in their band headers.
- Subagent attention (a subagent waiting on its coordinator) never counts toward Needs you; it shows in the subagent strip as "waiting".

### 13.3 In-app alerts (this version's push)

- **When:** a session you are not looking at becomes Failed, Question, Approval, Warning or Restart needed; or a hub notice appears.
- **Alert card:** drops in just below the nav bar (never over it, so Back, the title and the ask dock stay reachable), with an amber edge, the mark, session title and why line. It stays 8 seconds and never goes away while a finger is on it; swipe up to dismiss. Tap opens the session at the relevant spot, pushed onto the stack so Back returns to where you were.
- **Coalescing:** events within 5 seconds combine: "3 sessions need you". Tapping opens the Board scrolled to Needs you.
- **Quiet while reading:** in the Reader, the Artifact viewer, or while typing in the composer, banners are held; the Back button's count updates and a small amber dot appears on it. Held banners show when you leave, combined.
- **Haptics:** warning for failures, light notification for needs you, none for finished results.

### 13.4 Phase 2: OS notifications, Live Activity, widget (designed, not built)

- **Notifications:** categories Question (actions: up to three options plus "Reply…" with text input), Approval (Allow once, Deny; requires device unlock), Failed (Open, Retry), Finished (Open; delivered passively). Grouped per session. Questions and approvals use the time-sensitive interruption level. Payloads carry only titles and short reasons unless the user opts into message previews.
- **Live Activity:** "Follow" a session from its ⋯ menu. Lock Screen: title, state, the subagent strip with counts, current activity, elapsed time. Dynamic Island compact: leading pulse meter, trailing "31 ▸ 2 ✕"; it turns amber when the session needs you. Expanded: title, activity, strip, and an Open button.
- **Widget:** small shows the Needs you count and the oldest item; medium shows the top three Needs you rows. Taps deep-link.
- **App icon badge:** the Needs you count.
- Requires server addition S10.

## 14. States and resilience

- **Connection.** Live: nothing extra. Reconnecting (after 2 seconds without a connection): the Board toolbar says "Reconnecting…" and the session shows a thin ink-low bar under the nav bar; everything stays visible and scrollable. Offline (after 30 seconds): "Offline · updated 3m ago". Coming back is silent: content updates in place.
- **Outbox.** Every action taken while disconnected or unconfirmed shows its state where it was taken: a message ghost says "Sending…", then disappears into the transcript; an archive shows the row dimmed until confirmed. If delivery can't be confirmed after reconnecting, the item says "Couldn't confirm this was sent" with Check and Discard, inline. There is no separate recovery screen.
- **Drafts.** Per session and per document (review comments), persisted locally. Board rows show a Draft tag.
- **Errors.** Inline, specific, one action. Starting a session that the hub rejects keeps the sheet open with the reason. A failed session shows its error in the transcript with Retry or Resume.
- **Loading.** The Board and each session open from cache and update live. Older transcript pages load automatically when scrolling up. The only skeletons are on the very first load.
- **Empty.** Absence is the signal: empty bands and chips are hidden. The only full empty state is "Nothing's running" on the Board.

## 15. First run and pairing

- First launch shows "Connect to your hub" with two actions: **Scan pairing code** (an in-app camera scanner; this avoids the iOS system scanner stripping the token, #431) and **Paste pairing link**. One line explains where the code lives: "In Evener on your computer, open Settings, then Mobile app."
- After pairing: "Connecting to magic-kingdom…", then the Board.
- The hub token is stored in the Keychain; it never appears on screen or in logs.

## 16. Visual system

### 16.1 Color

The phone uses the web's tokens (`cmd/evener-hub/frontend/src/styles/tokens.css`) so both surfaces speak one language. The `-ink` forms are for text and are contract-tested to 4.5:1 on their surfaces and tints.

| Token | Light | Dark | Use |
|---|---|---|---|
| page | #FAF9F6 | #191918 | app background |
| canvas | #F1F0EB | #1D1D1B | grouped-list background, sheets |
| surface | #FCFBF8 | #232320 | rows in grouped lists, bubbles' base |
| inset | #F4F3EE | #20201E | code, evidence, output |
| hover / pressed | #F0EFE9 | #2B2B28 | pressed rows |
| edge | #DDDCD4 | #34342F | hairlines |
| edge-strong | #B7B6AC | #51514A | control borders |
| ink-hi | #252521 | #F2F1EB | primary text |
| ink-mid | #5F5F57 | #B0AFA6 | secondary text |
| ink-low | #6D6D64 | #99998F | tertiary, timestamps |
| attention / attention-ink | #F59E0B / #AD5209 | #F68F3C / #F68F3C | a human is needed |
| alive / alive-ink | #189A4D / #12763B | #3DBB72 / #3DBB72 | working |
| danger / danger-ink | #E3474C / #C51D23 | #EE5C61 / #F17478 | failed, destructive |
| accent / accent-ink | #0285FF / #0064C2 | #3D9AFF / #459EFF | tappable, selected, unread, links |
| diff add / delete bg | #E9F4EE / #F5EAF0 | #19251A / #170B17 | diffs only |

Tints: each hue's `-bg` is the hue at 15% over surface; `-edge` is the hue at 40% over edge. No other hues exist in the app. Diff washes are not status colors.

### 16.2 Type

| Role | Face | Size / line | Notes |
|---|---|---|---|
| Row title | SF Pro semibold | 17/22 | Dynamic Type: Headline |
| Why line | SF Pro | 15/20 | Subheadline |
| Meta, captions | SF Pro | 13/18, 12/16 | Footnote, Caption; tabular figures |
| Your messages, controls | SF Pro | 17/24 | Body |
| Sheet titles | SF Pro semibold | 17 | Headline |
| Section labels | SF Pro semibold | 12, uppercase, +0.06em | one or two words |
| Agent prose | Source Serif 4 | 17/26 | scales with Dynamic Type; optical size on |
| Documents | Source Serif 4 | 18/28; headings semibold 24/20/18 | Reader |
| Machine text | SF Mono | 13/18; tags 12 | paths, commands, model ids, code |

The serif is Evener's voice for anything the agent wrote. SF Pro is the phone's voice for everything you operate. SF Mono marks anything a machine reads. "Reading font: Sans" in Display swaps the serif for SF Pro.

### 16.3 Layout, shape, elevation

- 4pt spacing base. 16pt side margins. 12pt vertical row padding. 44pt minimum touch targets everywhere.
- Radii: capsules for chips, pills and primary buttons; 18pt continuous for message bubbles; 12pt for insets and code; system radii for sheets.
- Content is flat: rows, not cards. Borders only where a boundary matters (insets, the ask dock, ghost bubbles).
- Liquid Glass (system materials) for nav bars, toolbars, sheets, menus and banners. Shadows only on floating elements (banners, menus, the Next pill when it floats over content).

### 16.4 The pulse meter (signature element)

The one piece of expression in the app: a live 7-bar activity meter (22×14pt) standing in for the state mark on every working session, in rows, the session title bar, the status tray and subagent rows. Each bar is one minute of activity (transcript items and tool output events), newest on the right, scaled to the busiest minute in view. It moves only when real activity arrives, so a healthy swarm flickers, a quiet one flattens, and a stuck one lies flat and turns into the amber hollow ring after 10 minutes. It is green in the alive hue; it goes gray when the connection is lost. With Reduce Motion, bars change height without animation.

### 16.5 Iconography

SF Symbols only, weight matched to adjacent text. Core set: `questionmark.circle.fill`, `hand.raised.fill`, `exclamationmark.triangle.fill`, `xmark.octagon.fill`, `arrow.clockwise.circle.fill`, `circle.fill` (unread), `magnifyingglass`, `square.and.pencil` (new session), `ellipsis.circle`, `pin.fill`, `archivebox`, `stop.fill`, `arrow.up` (send), `plus`, `command` (commands and skills), `cpu` (model), `server.rack` (host), `folder` (project), `puzzlepiece.extension` (plugins), `lock.shield` (access), `doc.text` (plan and documents), `square.stack.3d.up` (artifact), `person.2` (subagents), `checklist` (tasks), `target` (goal), `note.text` (notes), `text.quote` (quote), `bubble.left` (comment).

### 16.6 Motion and haptics

- State changes fade in 200ms. Row moves use a 250ms spring. Banners drop with a 300ms spring. Push, pop and sheets use the system.
- The only idle animation is the "Thinking…" pulse. Everything else moves because something happened.
- Reduce Motion replaces slides with crossfades and removes the Thinking pulse.
- Haptics: selection tick on chips, segments and lateral session moves; light impact on send; success on answer sent and approval allowed; warning on a failure banner; rigid on destructive confirmations.

## 17. Data sources

| Element | Source |
|---|---|
| Board sections, rows, children | `evener/navigation/read` (Live, needs-you, projects, pin sections, archived), `NavigationSessionSummary` (title, state, ask_pending, live, updated_at, host_id, project, branch, children, running_jobs, more_subagents, omitted_descendants), invalidated by `evener/navigation/invalidated` |
| Needs you count | `evener/attention/changed` (`AttentionSummary{NeedsYou, Error, Working}`) |
| Search | `evener/search` |
| Session content | `thread/read` with subscribe; `item/*` streaming notifications; `thread/status/changed`, `thread/queueChanged`, `evener/goal/updated`, `evener/task/updated`, `evener/notes/updated`, `evener/delegate/updated`, `evener/jobs/treeUpdated` |
| Capabilities | `ThreadCapabilities` gates every control (send, steer, interrupt, compact, clear, forkFromTurn, shutdown, changeModel, queue, goal, sharedNotes, rename, skillInput) |
| Send, steer, queue, stop | `turn/start`, `turn/steer`, `turn/queue`, `turn/cancelQueued`, `turn/promoteQueuedAsSteer`, `turn/interrupt` |
| Session actions | `thread/shutdown`, `thread/model/set`, `thread/reasoning-effort/set`, `thread/fork` (with `aside`), `thread/compact/start`, `evener/thread/name/set`, `evener/archive/set`, `evener/session-pin/assign` and `unpin`, `evener/pin-section/rename` and `delete`, `evener/favorite/set` (pin project), `goal/set`, `notes/human/set` |
| Approvals | `evener/sandbox/escalation/requested`, `EvenerThread.PendingEscalations`, `evener/sandbox/escalation/resolve` |
| Questions | `status=awaiting` + `AskPending`; the ask's questions come from the transcript item; answers are an ordinary message |
| Usage | `EvenerThread.Usage`, `.Cost`, `.WorkMillis`, context pressure fields, `FailedToolCalls` |
| New session | `thread/start` (host via `Source`, cwd, model, effort, `launchOverrides.enabledPlugins`, sandbox), `evener/projects/recent`, `evener/paths/complete`, `model/list`, `evener/plugin/preview`, `evener/launch/resolve` |
| Hub | `evener/host/*`, `evener/instance/*`, `evener/auth/*`, `evener/plugin/*`, `evener/marketplace/*`, `evener/mobile/pairing`, hub upgrade methods |
| Documents | `/doc/file?format=raw` through `docContent.readDocFile` (512 KB cap), `/doc/image` |
| Artifacts | artifact reference items and the viewer resource from the shared-artifacts work (not on main); proposals via the viewer's `ui/message`, sent only by explicit user choice |

The phone builds on `@evener/appwire-client` (the shared TypeScript package), as the SDK migration plan intends.

## 18. Server additions

Each has a fallback so the phone works before it lands.

| # | Addition | Why | Fallback |
|---|---|---|---|
| S1 | Row "why" payload on navigation summaries: first pending question (text, option labels), pending approval (action, target), error summary, last agent message excerpt (about 200 characters), documents and artifacts named in the final message | Rows say why they are there | Generic copy ("Has a question", "Failed"); fetch details by subscribing to the few Needs you sessions |
| S2 | Pending approval flag on navigation summaries and in attention state | Approvals show as ✋, not "working" (also fixes the web's rail) | Subscribe to sessions counted in Needs you to find escalations |
| S3 | Subagent tallies per top-level session: running, waiting, failed, done, including omitted descendants | Exact subagent strip on 500-node trees | Tally loaded children; show "+N more" |
| S4 | A per-user "seen through" marker per session, with a method to set it, included in summaries | Finished-and-unseen vs Idle agrees across phone and web | Phone-local marker |
| S5 | Activity buckets per live session (events per minute, last 7 to 10 minutes) and last activity time | The pulse meter and "may be stuck" | Use `updated_at`; show a single bar |
| S6 | Direct subagent stop (and optionally message) | Stop a runaway subagent without asking the coordinator | Steer the coordinator |
| S7 | Image and document proxying for sessions on other hosts | Images and plans in remote sessions render | Show "Open on the host" notice |
| S8 | Launch recipes stored on the hub (shared with the web) | Recipes follow you across devices | Phone-local recipes |
| S9 | Document revision identity in doc reads | "Changes since you last read" | Diff against the phone's cached copy |
| S10 | Push delivery (APNs sender, device registration, event payloads, Live Activity updates) | Phase 2 | In-app alerts |
| S12 | Scoped approvals: "allow writes under this folder for the rest of this session" as an escalation resolution | A batch job (a 214-page mirror) doesn't ask 214 times | Allow once, repeatedly |
| S11 | Hub notices feed: provider sign-in expired or expiring, host offline or version mismatch, broken plugin, each with affected session counts | Notices row on the Board | Derive from host and instance status reads |

## 19. Out of scope and future

- **Decision inbox.** Jesse expects to want one eventually and has not designed it. The Board keeps decisions inside sessions. When it exists, it would slot in as a destination from the Board header and reuse the ask dock's question and approval components.
- iPad and Android (paused and deferred), voice, a cross-session document library, desktop-only administration, and diff review beyond the evidence rendering in 8.2.

## 20. Prototype and usability testing

- Prototype: `docs/design/mobile/redesign/prototype/` (a clickable HTML model of this spec with fixture data shaped like the real usage in section 2).
- Harness and study materials: `docs/design/mobile/redesign/usability/`.
- Findings and the changes they caused are recorded in `docs/design/mobile/redesign/usability/findings.md` and folded back into this spec. Round 1 (five participants, 27 of 29 tasks succeeded) changed: edge-zone row swipes, the Next bar, alert placement and timing, Continue reading, detail-level descriptions, scoped approvals, "Finished", host update while offline, and effort in the launch flow.

## Appendix A: frames to produce

For Claude Design or any visual pass. Each frame at 393×852pt, light and dark unless noted.

1. Board, busy: a notice, Needs you (failed, question, approval), Finished (two with attachments), Working (six, one "may be stuck"), Idle collapsed.
2. Board, organized by host: Projects section flipped to "Host, then project" and expanded.
3. Board, Pinned categories and Archived expanded.
4. Board, search with "In sessions" hits.
5. Board row actions: leading swipe (Archive), trailing swipe (Stop, Pin, More), long-press preview menu.
6. Board, in-app banner arriving; coalesced banner.
7. Session, working, Intent level: chips, messages, activity runs, a subagent row, a document chip, status tray with Next.
8. Session, question pending: ask dock with two questions, a recommended option, multi-select variant.
9. Session, approval pending.
10. Session, typing while working: Steer and Queue, the first-use hint, a queued ghost bubble.
11. Session, failed: error block with Retry.
12. Session, Tools level with an expanded step showing command output and a diff.
13. Session sheet: where, model, plugins (fixed), usage with context gauge.
14. Model sheet with Effort control.
15. Subagents: strip, filters, failed first, nested running rows, Done folded.
16. Subagent transcript, read-only, with "Ask coordinator to stop it" and its sheet.
17. Reader: plan with changes since last read and comment markers.
18. Reader: Send review sheet.
19. Artifact viewer with a proposal sheet.
20. New session, filled.
21. New session: plugin checklist.
22. New session: model picker.
23. Hub: hosts (one offline), providers (one expired), plugins (one update).
24. Host detail: paradise-park offline with its last error.
25. First run: Connect to your hub.
26. Phase 2 (light only): lock screen with a question notification and actions; Live Activity; Dynamic Island compact and expanded; medium widget.

## Appendix B: fixture content

Use content shaped like real usage (section 2): about 17 live top-level sessions across evener and a few other projects, two hosts (magic-kingdom, paradise-park), subagent trees from 0 to 54 with at least one of 467 in Archived, models from several provider profiles (lunaroute: deepseek-4.1-flash, glm-5.3-vision; codex-jesse-fsck.com: gpt-5.6; meta: muse-spark-1.3; kimi-code: k3), effort mostly xhigh and high, sessions running 6 to 13 of these plugins: superpowers, elements-of-style, frontend-design, go, go-release, go-spec-reviewer, fileflow-pathologize, claude-session-driver, private-journal-mcp, shepherd-pr, iterative-development, study-skills, simplify-code, superpowers-chrome. Titles are four to six words in title case, auto-named from the prompt. The prototype's `data.js` is the canonical fixture.
