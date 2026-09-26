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
│   ├── Session sheet: where, model, effort, plugins, access, usage, goal, tasks, notes and links, actions
│   └── Queue, tasks, Notes & links → Reader | in-app browser (sheets)
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
magic-kingdom ▾                                            ⌕
[Live 20 (4)] [📌 Release 2] [📌 Research 1] [Projects 14] [Archi…

⚠  codex-jesse-fsck.com sign-in expired · 3 sessions    Sign in
─────────────────────────────────────────────────────────────
4 need you   4 finished   ▂▅▇ 9 working   3 idle

NEEDS YOU · 4
✕  Fix Endless Provider Retry Loop                         2m
   Failed · codex-jesse-fsck.com sign-in expired (401)
   ☑ Task 3 of 4 · Cap retries per… · 1 subagent failed · ⌂ paradise-park
?  Audit Tool Descriptions for Implied Options            14m
   Question · keep or drop the implied options?
✋ Mirror Docs Site Locally                                21m
   Approval · wants to write outside the workspace: ~/sites/docs
   ▭ prime-radiant-inc.github.io
FINISHED · 4
•  Host Project Hierarchy UI Mockups                       1h
   Three layouts are ready for review. I recommend B…
   [Plan] Host and project hierarchy  [Artifact] Hierarchy layouts
WORKING · 9
▂▅▇  Get PR 2138 Test Clean                               38m
   Waiting on 31 subagents
   ☑ Task 4 of 7 · Fix the settle/drain race · 2 subagents failed
Idle · 3                                                    ›
📌 RELEASE · 2                                              ⋯
  …
PROJECTS                                  Project, then host ⇄
  …
Test runs · 3                                               ›
ARCHIVED · 271                                              ›
─────────────────────────────────────────────────────────────
Select                                                      ✎
```

- **Header (glass nav bar).** Leading: the hub button (the hub name and a chevron). Trailing: Search. No large title; the section chips carry orientation. A connection that isn't live shows in the toolbar (section 14), never as a dot here: a meter in front of the hub name read as signal strength to two critics.
- **Section chips (sticky under the header).** Live, one chip per pinned category (with a pin glyph), Projects, Archived, each with a count. Chips are landmarks, so their names never change with display settings. Tapping scrolls to that section. The Live chip carries an amber badge with the Needs you count when it is above zero. Chips for empty sections are hidden. The row fades at its trailing edge, so a cut-off chip reads as "there's more".
- **Notices (only when present).** Hub-level problems that block sessions: a provider sign-in expired or expiring within a day, a host offline, a plugin marked broken. A host running a different Evener version than the hub is not a notice: the hub attaches a protocol-compatible host on its own build, so work continues (Hub > Hosts shows the version). Each notice sits on the page like a row: the ⚠ mark, one sentence naming the affected count in ink, and one action in blue ("Sign in", "Reconnect"). No tinted box: the mark says it needs you. Notices dismiss themselves when resolved.
- **Continue reading (only when present).** Leaving a plan or document before its end leaves one row under the notices for two hours: "Continue reading · 62%" and the document's title. Tapping it reopens the document at the same position, inside its session. This is the way back after an interruption.
- **Live summary.** One line counts the Live bands: "4 need you · 4 finished · ▂▅▇ 9 working · 3 idle", with the Needs you count in amber ink and the fleet pulse meter (the whole fleet's activity, section 16.4; gray while the connection is down) before "working", where its label says what it measures. Each count jumps to its band (Idle unfolds). It shows when at least two bands have sessions, so the first screen always says what's working even when Needs you fills it (a first-glance participant found no sign of working sessions without it).
- **Live** holds every live, unarchived top-level session, in four bands:
  - Needs you: failed first (oldest failure first), then questions, approvals, warnings and restart-needed, oldest waiting first.
  - Finished: sessions whose turn ended, newest first. A blue dot marks the ones you haven't opened since.
  - Working: stable order by start time, newest first. Sessions that "may be stuck" float to the top of this band.
  - Idle: finished sessions you have already seen, collapsed by default, most recent first.
  Band headers show counts. Empty bands are omitted.
- **Pinned categories.** Pinned sessions live in the user's named categories, and each category is its own section (as on the web's rail), in the order the hub returns them; there is no generic "Pinned" section. Each section header has a pin glyph, the name, a count, a collapse toggle and a ⋯ menu (Rename, Delete). Its rows are quiet one-line rows with a still mark: a pinned category is a place, and a live session's full row is already in Live (a round-4 participant read the repeated full row as a second job). An empty category says how to pin to it. Deleting a category unpins its sessions; it never deletes sessions. A pinned session also appears in Live while it is live, so pinning never hides attention.
- **Projects / Hosts** mirrors the web's "Organize by" control: "Project, then host" (default) or "Host, then project". The toggle sits in the section header, flips the section title between Projects and Hosts, and appears only when more than one host exists. Pinned projects float to the top with a pin mark. Inside a project, sessions split like the web: today, recent, and a folded archived group. Each project and host row shows its live count. A host shows its state only when it isn't connected (an amber "Offline"); connected hosts carry no dot, so green means working and nothing else.
- **Test runs** (collapsed): projects whose sessions all came from test runs, as on the web.
- **Archived** (collapsed): archived sessions and projects, newest first. Unarchive from the row's swipe or menu.
- **Bottom toolbar (glass).** Leading: Select (multi-select mode with Archive, Pin, Mark as read). Center: the connection status only when it isn't live ("Reconnecting…", "Offline · updated 3m ago"); a live connection shows nothing extra, since the header's meter is already moving (section 14). Trailing: New session, a plain compose glyph in the accent color.
- Every section's collapsed state persists per device.

### 7.2 Row anatomy

Signal rows (needs you, finished and not yet seen, working) have up to three lines; quiet rows (idle, and rows inside Projects and Archived) have one.

| Part | Spec |
|---|---|
| Leading mark | 28pt column. State mark (see 13.1). Working rows in the Live band show the pulse meter. The same session listed again under a pinned category or in Projects shows a still green dot: only the Live band moves. |
| Title | SF Pro semibold 17/22, one line (two for Needs you), tail truncation. |
| Age | Trailing on line 1, 13pt tabular, ink-low. "2m", "1h", "3d". For working rows, time since the session last started a turn. |
| Why line | 15/20, up to two lines for Needs you so the reason's key words survive. Needs you: the state word, semibold in its hue ("Failed", "Question", "Approval", "Restart needed", "May be stuck"), a middle dot, then the reason in ink. Only the word takes the hue; the reason is what you read. Finished (not yet seen): the opening of the last agent message in Source Serif 15/21, ink-mid, without quotation marks, up to two lines; the typeface says the agent is speaking. Working: the current activity in ink-mid ("Running go test ./agent/...", "Thinking", "Waiting on 31 subagents", "Quiet 4m"). |
| Attachments (finished, not yet seen) | Up to two chips for documents or artifacts named in the final message: "[Plan] Host and project hierarchy", "[Artifact] Hierarchy layouts". Tapping a chip opens it on top of its session, so Back goes to the session. |
| Last line | 13/18, ink-low, only what applies, in this order: task progress when the session keeps a task list and hasn't finished it ("☑ Task 4 of 7 · Fix the settle/drain race"); "2 subagents failed" in red ink when any failed; the project with a folder glyph, and the host with a server glyph, only when they differ from the fleet's usual ones; the model's display name when "Show model on Board rows" is on. With none of these, the row has no last line. The line never wraps: the task's title truncates first. |
| Draft tag | A small blue "Draft" tag before the age when the session has an unsent draft. |

Why task progress rather than subagents: the task line says where a session is in its own plan, and the activity line says what it's doing now. A subagent count says how big a job is, not how far along it is ("31 running" looks the same at minute 2 and minute 40), so subagents appear on a row only as an exception, when some failed. The strip and the full counts live on the session's Subagents chip (Jesse's call, 2026-09-25). "Task 4 of 7" is spelled out because a bare "3/7" beside a task name read like a setting to a first-glance participant.

Row heights: signal rows about 64 to 88pt; quiet rows 48pt. Horizontal padding 16pt. Hairline separators inset to the title. Printing project and host only when unusual keeps most rows to two or three short lines; on the fixture fleet it takes "magic-kingdom" off every row but one.

### 7.3 Row interactions

- **Tap** opens the session at the right spot: the pending question or approval, the start of the unread result, or the live end of the transcript.
- **Swipe right** (leading): Archive (blue-gray action). Full swipe archives. An "Archived · Undo" toast appears at the bottom for 8 seconds. Swipes that begin in the 24pt screen-edge zone never act on a row, so the system back gesture can't archive anything (in round 1 it did, on the root screen, where there is nothing to go back to).
- **Swipe left** (trailing): Stop (only when working; ends the current turn), Pin, More (the long-press menu).
- **Long-press** opens a context menu with a preview card (title, state, why line, task progress, subagent count with failures, project, host, model and effort, last message excerpt; tapping the card opens the session, so there is no "Open" item, whose chevron read as a submenu) and actions: Pin to category… (category list plus "New category…"), Mark as read / Mark as unread, Stop, Shut down, Archive, Copy link, Rename.
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
‹ 4      Get PR 2138 Test Clean           ⋯
         ● Working · 38m ›
[Subagents 55 · 2 failed] [Files 1] [Tasks 3/7] [Goal] [Queue 1]
👤 Your note: Don't skip or quarantine tests…   3 links ›
────────────────────────────────────────────
                     Today 2:14 PM
                 ┌──────────────────────────┐
                 │ Also cover the empty      │
                 │ state please              │
                 └──────────────────────────┘
The flaky test is caused by a race between the
tree settle pass and the retirement drain. I'm
splitting the fix into two subagents…

▸ 12 steps · 8m · read 6 files, ran go test (2 failed), edited 3 files
▎Fix race in tree settle                failed · 6m
▎Failed: go test exited 1 (3 times)
[Plan] Fix the settle/drain race
              ╭─────────────────────────────────────╮
              │ Next  Fix Endless Provider Retry… › │
              ╰─────────────────────────────────────╯
────────────────────────────────────────────
▂▅▇ Running go test ./agent/... · 42s
┌──────────────────────────────────────────┐
│ Tell the agent something…                │
│ +   GLM 5.3 Vision · XHigh           ■   │
└──────────────────────────────────────────┘
```

- **Nav bar (glass).** Back shows an amber count of sessions that need you (excluding this one). The center holds the title (15pt semibold, one line) and a subtitle with a still state mark (a green dot while working; the pulse meter lives in the tray) and the state with its time ("Working · 38m", "Finished · 1h ago", "Asks a question", "Failed"), then a small chevron that says the title is tappable. Tapping the center opens the Session sheet; it has a pressed state and sits clear of Back. Trailing: the ⋯ menu.
- **Context chips (under the nav bar; hide on scroll down, return on scroll up).** Subagents (the count, then "· 2 failed" in red ink when any failed; a 48pt strip made 2 of 55 a 1.7pt sliver), Files (count; blue dot when something new or changed), Tasks (done/total), Goal (when set; amber when blocked), Queue (count, when non-empty). Chips appear only when they have content. The transcript's detail level is not a chip: it is a view setting, and "Detail: Intent" on a chip meant nothing to first-glance participants. It lives in the ⋯ menu (section 8.7), where every level says what it shows.
- **Notes bar (under the chips, only when the session has a note or a link).** One 32pt line, the phone's form of the web's collapsed notes bar (section 8.8).
- **Transcript** (section 8.2).
- **Next capsule** (section 8.3), floating over the end of the transcript when other sessions need you and this one doesn't.
- **Status tray** (section 8.3) while the agent works, or the **Ask dock** when a question or approval is pending (section 8.4).
- **Composer** (section 8.5). While a dock is open, the composer steps aside until you ask for it.

### 8.2 Transcript

The default detail level on the phone is Intent, matching the hub's shipped mobile default. "Detail level" in the ⋯ menu opens the level picker, a menu headed "How much of the agent's work this session shows" in which every level says what it shows and the current one carries a leading check (round 1 participants could only guess at bare level names):

| Level | Shows |
|---|---|
| Chat | Just the conversation |
| Intent | Plus one line for each step the agent took |
| Tools | Plus every command it ran; tap one for its output |
| Activity | Plus system events, like compaction and model changes |
| Full | Everything, with command output shown |

These are the hub's levels and names, shared with the web because the setting is one hub-backed display config; Custom lives in Hub > Display. The choice is per session and remembered.

| Item | Rendering |
|---|---|
| Time marker | Centered caption, ink-low: "Today 2:14 PM". Shown at a turn start after a gap of 10 minutes or more, and at day changes. |
| Your message | Right-aligned bubble, max 85% width, a `bubble` fill (the accent tint in light mode, a warm neutral in dark), Source Serif 4 17/25 as on the web, continuous 18pt radius. Steered messages carry a small "Steered in mid-turn" caption; queued messages that were delivered carry "Queued". Long-press: Copy, Fork from here, Quote. |
| Agent message | No bubble. Source Serif 4, 17/26, ink-hi, full width with 16pt margins. Markdown: headings in SF Pro semibold (20/17/15), lists, block quotes with a 2pt ink-low rule, tables and code blocks in their own horizontally scrolling insets, links in accent ink. Paths to files become document chips. Long-press: Copy, Quote in reply, Select text. |
| Activity run | One collapsed line per run of tool calls: "▸ 12 steps · 8m · read 6 files, ran go test (2 failed), edited 3 files" in 14pt ink-mid, with how long the run took. A live run lists only its finished steps; the step in progress is the status tray's one live line, so "working" is said once. Failure counts show in red ink even when collapsed. Tap to expand into one line per step: intent sentence, target in SF Mono, and a status mark. Tapping a step shows its evidence: command output in an SF Mono inset (first 40 lines, then "Show all 412 lines" which opens a full-screen log viewer), diffs as unified hunks with the web's add/delete washes and a "+18 −4" summary. A live run never folds. |
| Thinking | Settled: "Thought for 12s ›" (collapsed). Live: "Thinking… ~1.2K tokens" with the one sanctioned pulse. |
| Subagent | The web's shape: a 2pt left rail in the state's hue (green running, red failed, edge-strong done), square corners, no card and no pill. The title in SF Pro semibold 15 with the state word and its time in that state at the trailing edge ("failed · 6m", in red ink when failed), and the latest activity beneath in SF Pro 14/19, ink-mid (it's status, not prose, and it reads the same as in the Subagents list). Tap opens the subagent. |
| Document chip | The kind (Plan, Spec, Doc, Code, Image), the document's own title from its first heading in the reading serif, then the file name, line count and age. Tap opens the Reader. A blue dot and "changed since you last read" mark a document that changed since you last opened it. |
| Artifact card | Title, one-line summary, a static preview image when available, version ("v3"), and "Open". If you answered its proposal, the card says so ("You chose B"). |
| Question (history) | Amber left rule, the question, and your answer beneath ("You answered: Drop them"). While the question is still open, the dock is the question, so the transcript doesn't repeat it. |
| Approval (history) | "Allowed: write ~/sites/docs" or "Denied: …" in ink-mid with the mark. Not repeated in the transcript while the dock is open. |
| System event | A ◇ gutter mark with 13pt ink-low text, shown at Tools level and above: "Context compacted · 412K → 38K tokens", "Model changed to GLM 5.3 Vision · XHigh", "Plugin superpowers loaded". Model changes and compactions also show at Intent. |
| Your note, saved | "You updated your note" over the note in the serif, with a left rule, at every level (section 8.8). |
| Error | Red left rule, the error in plain words, and one action (Retry, Resume, Sign in). |
| Images | Thumbnails in a row (96pt), tap for a full-screen viewer with swipe between images. |

Scrolling:

- The session opens at the right spot (section 7.3). Older history loads automatically as you scroll up; there is no "Load more" button.
- New content streams in without moving what you are reading. When you are not at the bottom, a "↓ 3 new" pill appears above the tray; it is hidden when you are at the bottom.
- Reading position persists per session.

### 8.3 Status tray and Next

**Tray.** A single 36pt line above the composer, only while the agent works: the pulse meter and the current activity with elapsed time ("Running go test ./agent/... · 42s", "Thinking… · 1.2K tokens", "Waiting on 12 subagents", "Quiet 40s", "May be stuck · no updates for 12m" in amber ink). Tapping it jumps to the live end of the transcript. Other states have no tray: the title's subtitle already says "Finished · 1h ago", "Failed" or "Shut down", and the transcript and composer say the rest ("Message to resume").

**Next capsule.** When other sessions need you, a small capsule floats at the trailing edge, 10pt above the tray or composer, on the raised surface with a strong edge so it floats in both themes: "Next" in accent, then the title of the session it goes to (truncated), then a chevron. Back already carries the count. Tapping it opens that session at its question or failure. The order: whichever session alerted you most recently (shown or held), then Needs you order; a round-4 participant expected Next to go to what had just pinged, and it didn't. Touch and hold opens the list of sessions that need you. The transcript keeps 60pt of room at its end so the capsule never sits on the last line.

Next pushes the session, so Back returns to the one you were in; from a session Next itself opened, it replaces instead, so working through the queue stays one step deep and Back still lands where you started. (With a replace from the start, a round-4 participant handling an interruption found Back went to the Board, not to the plan they had been reading.)

- It shows only when you're free to move on: never while this session's own question or approval is open.
- It never shows in the Reader or the artifact viewer, which stay quiet (section 13.3).
- It replaced a full-width amber bar that sat on almost every session screen, repeated Back's count, and was the largest amber shape in the app. A first try at a bare "Next" with the next session's mark failed first-glance testing: all three participants were unsure what "Next" moves through, and all three guessed "the next failure" from the red ✕. Then "4 others need you  Next" said what it moves through but not where it goes. Naming the destination does both.

### 8.4 Ask dock

When the session has a pending question or approval, an amber-edged dock replaces the status tray. While it's open, the dock is the input: the composer steps aside so every option fits, and comes back with "Other answer…" (question), "Tell the agent something else…" (approval), or when you fold the dock away.

**Question** (the agent's `ask_user`; top-level sessions only; 1 to 4 questions per ask, 2 to 5 options each):

```
┌ Question 1 of 2 ───────────────────────── ⌄ ┐
│ Keep or drop the implied options?           │
│ Fourteen descriptions mention flags the     │
│ tools don't accept. Models try them and fail│
│ ◯ Drop them · Recommended                   │
│   Remove the implied options from all 14    │
│ ─────────────────────────────────────────── │
│ ◯ Keep them and add the flags               │
│   Implement the 9 missing flags; ~400 lines │
│ ─────────────────────────────────────────── │
│ ◯ Ask me per tool                           │
│ Other answer…                 Next question │
└─────────────────────────────────────────────┘
```

- Header: "Question 1 of 2" in ink-mid and a collapse chevron. The question in Source Serif semibold 17/24; the agent's "why" in Source Serif 15/21, ink-mid.
- Options as borderless rows divided by inset hairlines, each with its label and detail; the radio or checkbox mark separates them. The recommended option is listed first and carries "· Recommended" as an ink-mid caption, not a badge. Single-select uses radio marks; multi-select uses checkboxes.
- "Other answer…" brings the composer back with the cursor in it, and whatever you send answers the question.
- The last question's primary button is "Send answer" (or "Send answers"). Answers go out as one message, the same way the web and current native compose them.
- Collapsing the dock leaves a slim bar, "Answer 2 questions", and brings the composer back for reading and typing.

**Approval** (sandbox escalation; blocks the agent mid-step):

```
┌─────────────────────────────────────────────┐
│ ✋ Wants to write outside the workspace      │
│ write_file  ~/sites/docs/index.html         │
│ This session can only write inside its      │
│ project folder. It's about to write the     │
│ first of 214 pages.                         │
│ ┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓ │
│ ┃        Allow this file only             ┃ │
│ ┃   It will ask again for the next one    ┃ │
│ ┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛ │
│ ┌─────────────────────────────────────────┐ │
│ │      Allow all of ~/sites/docs          │ │
│ │      For the rest of this session       │ │
│ └─────────────────────────────────────────┘ │
│                    Deny                     │
│        Tell the agent something else…       │
└─────────────────────────────────────────────┘
```

- No header: the ✋ mark and the amber edge say what this is. What it wants in SF Pro semibold (the hub's sentence, not the agent's prose); the tool and target in SF Mono; one plain sentence of scope in ink-mid.
- The choices are buttons, because each acts on the first tap, and they run from the narrowest grant to the broadest, so a reflex tap grants the least: "Allow this file only" as the filled primary, "Allow all of <folder>" accent-tinted, each with its consequence on a second line, then Deny as a plain text button in ink. Deny is the safe outcome, so it isn't red. The result shows as a toast ("Allowed once") and in the transcript history.
- "Allow this file only" covers exactly one action, so a batch job asks again for its next file. "Allow all of <folder>" appears when the action targets a folder and ends the prompts for this session (server addition S12). When the action has no folder scope, the first button is "Allow once · Just this action".

### 8.5 Composer

Layout: the text field on top (grows to six lines, then scrolls; an expand control opens a full-screen editor), and a controls row beneath it, per Jesse's ruling that send sits on the controls row.

Controls row, left to right:

- **+**: Photo library, Camera, and Commands and skills. Attached images show as removable thumbnails above the text. Typing "/" as the first character also opens Commands and skills. (A separate "/" button read as a divider between the model chip and Send.)
- **Model chip**: the model's display name and effort, "GLM 5.3 Vision · XHigh" (a model is called one way everywhere people read it). Opens the model sheet: recent models first, then all models grouped by provider, each with context size and price; an Effort segmented control at the bottom showing only the levels the model supports. Changes apply from the next turn ("Applies from the next turn").
- **Commands and skills** (from + or a leading "/"): a sheet with search, built-in commands first (Goal, Compact context, Aside, Tasks, Model, Effort, Clear), then skills grouped by plugin. Choosing one inserts it as a token.
- **Primary button** (trailing), by state:

| Session state | Field empty | Field has text |
|---|---|---|
| Idle / finished | Send (disabled) | **Send** (accent, arrow up) |
| Working | **Stop** (square, ink fill) | **Steer** (accent, primary) and **Queue** (secondary text button to its left) |
| Question or approval pending | The composer is hidden while the dock is open (section 8.4) | After "Other answer…": **Send** (sends as the answer; the dock updates) |
| Shut down | Send (disabled) | **Send** (resumes the session) |
| Offline | Send (disabled) | **Send later** (goes to the outbox) |

- The first time Steer and Queue appear, a one-line hint sits above the field: "Steer arrives at the agent's next step. Queue waits until this turn ends." It does not return after two uses.
- **Queued messages** appear as dashed ghost bubbles above the composer: "Queued · sends when this turn ends". Tap for Steer now, Edit, or Cancel. Swipe left to cancel.
- **Steering in flight** appears as a ghost bubble "Steering · arrives at the next step" until the agent picks it up; then it becomes a normal message with the "Steered in mid-turn" caption.
- While a dock is open and the composer has come back, the model chip steps aside: the dock is what you're answering.
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
- **Goal** (edit), **Tasks** (list with current task highlighted), **Notes & links** ("Your note · agent note · 3 links", or "None"; opens section 8.8's sheet).
- **Actions:** Aside (ask something in a side session), Fork from latest, Compact context, Copy link, Pin to category…, Archive, Shut down (destructive, confirms), Delete (only when shut down, destructive, confirms).

### 8.7 ⋯ menu

Detail level (with the current level and its description as a second line, "Intent · Plus one line for each step the agent took") ▸, Find in session, Files & artifacts, Subagents, Tasks, Notes & links (always present, so a session with neither can still get your note), Session info, Ask aside… ("A side question in its own session; this one keeps working"), Pin to category…, Archive, Shut down. Choosing a detail level confirms with a toast naming the level and what it shows ("Full: everything, with command output shown"): the change often happens above the visible part of the transcript, and two round-4 participants weren't sure it had taken.

### 8.8 Notes & links

The same shared notes the web shows in its Notes panel: your note, the agent's note, and the session's links (`humanNote`, `agentNote` and `sessionUrls` on the thread; updates arrive as `evener/notes/updated` and `evener/urls/updated`). Hidden when the session lacks the `sharedNotes` capability.

- **Notes bar.** Under the context chips, one line, shown only when there is something in it. It previews, in the web's order, your note ("Your note: …" with a person glyph), else the agent's note ("Agent's note: …" with a sparkle glyph), else the links: a lone link by its label, several as "3 links". When a note is showing and links exist, a trailing "3 links" says so in words; a bare glyph and count read as attachments to all three first-glance participants. The whole bar opens the sheet. The web also shows an empty "Add a note…" bar; the phone leaves it out to keep 32pt of transcript, and "Notes & links" in the ⋯ menu and the Session sheet is always there instead.
- **Sheet** ("Notes & links", large detent, Done). Three groups, as on the web:
  - **Your note.** A serif editor that looks like one: a visible field border, a focus ring in the accent color, and the placeholder "Make a note…" when empty. Below it one status line: "Your note stays on this session. Saving it will wake the agent." when the agent isn't in a turn, "Your note stays on this session. The agent is told when it changes." otherwise, "Saves in 10 seconds, or when you close this." once you leave the field, then "Saved". (The earlier "The agent gets your note at its next step" echoed Steer's own wording, and a round-4 participant sent a steer instead of a note.) Opening the sheet from the notes bar while it shows your note puts the cursor at the end of your note, ready to add to it: in round 4, without a visible field and focus, one participant twice typed into the middle of the note and another concluded the note couldn't be edited at all.
  - **Agent.** The agent's note in the serif, or "No agent note yet".
  - **Links.** Each row shows the label and, beneath it, the full URL in SF Mono, wrapping only after a slash. The URL is never hidden, so a trusted-looking label can't disguise where a link goes. Web links show a globe, file links a document. A web link opens in an in-app browser (SFSafariViewController) over the sheet, with Done coming back. A `file://` link opens in the Reader when it names a document the phone can show; any other file keeps its text and isn't tappable, as on the web. Swipe left for Remove; long-press for Open, Copy link and Remove link. Removal can't be undone from the phone, because only the agent adds links (`urls/add` is agent-only), and the toast says so. Footer: "The agent adds links as it works. Swipe left on one to remove it." Empty: "No links yet".
  - Ended sessions show what was saved, read-only, with no editor and no Remove; with nothing saved, "No shared notes".
- **Saving.** Saving your note is a steer: the daemon hands it to the agent at its next step and wakes an agent that isn't in a turn (`SetHumanNote` in agent/session_notes_rpc.go). Closing the sheet (Done or a swipe down) saves at once and confirms with a toast ("Note saved", or "Note saved. The agent is reading it." when it woke the agent): closing is a clear "done", and round 4 showed a delayed, unconfirmed save left people unsure anything was saved. Leaving the field while the sheet stays open schedules the save 10 seconds later, as the web does, and returning to it cancels that, so a burst of edits reaches the agent once.
- **Transcript.** A saved note shows where it reached the agent: "You updated your note" over the text in the serif, with a left rule, at every detail level (the web shows the same moment as a divider labeled "Human note").

## 9. Subagents

Opened from the Subagents chip or a subagent row in the transcript.

```
‹ Back        Subagents · 55
              Get PR 2138 Test Clean
▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬▬
[All 55] [■ Failed 2] [■ Running 32] [■ Done 21]

FAILED · 2
✕  Fix race in tree settle                   6m ago
   Failed: go test exited 1 (3 times)
   ⑂ fix-settle-race · 1.2M tokens
RUNNING · 32
●  Check drain ordering in tests                 4m
   Reading agent/retirement_test.go
   from Fix race in tree settle · 210K tokens
Done · 21                                          ›
```

- A subagent is running, failed or done, as the hub's job counts are (active, failed, completed). An earlier "waiting on the coordinator" state is folded into done: it meant "finished; the coordinator hasn't read the report yet", which the phone can't know, and it clashed with the coordinator's own "Waiting on 31 subagents".
- A full-width strip in the list's own order: failures in red (never thinner than 3pt, so 2 of 55 still shows), running work in green, then done in the lightest neutral, so the weight lands on what's live or broken; no empty track. Once every subagent is done the strip isn't drawn. Then filter chips with counts, each carrying its state's swatch, so the chips are the strip's legend. Default filter: All.
- One flat list by state: failed first, then running (newest first), then done (folded as "Done · 21"). Every section's count matches its chip. A subagent started by another subagent says so in its last line ("from Fix race in tree settle"); nesting it under its parent had put a running row inside the Failed section.
- Rows: a still mark (a green dot for running, ✕ failed, ✓ done; one pulse meter per view, and on this screen that's none), the mandate as title, the latest activity or outcome as the why line, and a last line with the model's display name (only when it differs from the coordinator's), a branch glyph and the branch name when the subagent works in its own worktree, and tokens. The trailing time is bare time in the current state, as the Board's ages are: how long a running subagent has run, how long since one failed or finished.
- The list virtualizes; trees of 500 must scroll smoothly. A search field filters by title.
- **Subagent transcript** opens read-only with the same renderer. A banner at the top: "Subagent of Get PR 2138 Test Clean. Talk to it through its coordinator." The composer is replaced by an action bar: **Ask coordinator to stop it** and **Open coordinator**.
- **Ask coordinator to stop it** opens a sheet with a prefilled, editable steer to the parent ("Stop subagent 'Fix race in tree settle': it has failed three times.") and Steer / Queue buttons. The row then says "Stop requested from the coordinator" until the subagent's state changes, and when it stops the row reads "Stopped at your request" with a toast naming it; a round-4 participant didn't trust a request that never visibly completed. When the hub gains a direct stop call (server addition S6), this becomes **Stop subagent** with a confirmation and no message.

## 10. Review: plans, documents and artifacts

Plans and artifacts are the main things reviewed on the phone.

### 10.1 Files & artifacts

A sheet listing everything the session wrote or linked, newest first: documents (with type labels Plan, Spec, Doc, Code, Image, the path in SF Mono, line count and last update) and artifacts (title, version, last update). Blue dots mark items new or changed since you last opened them.

### 10.2 Reader

```
‹ •                                          ☰  ⋯
Plan · updated 3m ago · 3 changes since you read it yesterday
# Fix the settle/drain race
The retirement drain and the tree settle pass
both take the tree lock, but…              💬 1
▎ The drain now waits for settle.
…
───────────────────────────────────────────
     Touch and hold a paragraph to comment on it
💬 2        ‹ Change 1 of 3 ›         Send review
```

- Full-screen reading surface: Source Serif 4 18/28 in the `prose` color on page, 16pt margins. Headings in serif semibold, ink-hi. Code blocks in SF Mono 13 insets that scroll horizontally. Task lists render as checkboxes (read-only). Tables scroll horizontally.
- Header: an empty nav bar until the document's own first heading scrolls away; then its title takes the nav bar, with the kind and age beneath. Above the heading, one caption line in ink-low: "Plan · updated 3m ago", then "3 changes since you read it yesterday" in accent when there are changes. The outline button (headings list for jumping) and ⋯ (Open session, Copy path, Copy text). Back carries a small amber dot while alerts are held (section 13.3).
- **Changes since you last read:** changed paragraphs get a blue left rule, and nothing else (no "Changed" label); the caption says how many ("3 changes since you read it yesterday"), and the bottom bar steps through them ("‹ Change 1 of 3 ›"), within thumb reach. The caption says when you read it, so it can't be mistaken for a first read.
- **Comment:** long-press a paragraph (or select text) for Comment, Quote in reply, Copy. In a list, the comment attaches to the item under your finger, which is highlighted while the menu is open. A comment attaches to its paragraph or item, and its marker (with a count) sits on that paragraph or on that list item, never on the list's first line. Comments are drafts until sent and persist per document. Until you comment, a one-line caption above the bottom bar says "Touch and hold a paragraph to comment on it"; there is no tip card over the text.
- **Review bar (bottom):** Comments (a count, once there are any; opens the list), the change stepper when there are changes, and Send review. No Next capsule here: the Reader stays quiet.
- **Review** sheet (titled "Review" so its title never repeats the "Send review" button): choose Approve, Request changes, or Comment only (nothing is chosen for you, and Send stays disabled until you choose); an optional overall note; the comments listed with their quoted paragraphs. The primary button matches the session state: Send (idle), Steer and Queue (working). The message format:

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

- Full-screen, with a thin top bar: back (with the held-alert dot, as in the Reader), title, "v3 · updated 2m ago", and ⋯ (About: summary, versions; Diagnostics). No Next capsule: the viewer stays quiet.
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
[Same as last time] [Evener coordinator] [Quick question] [+ Save]
Host        magic-kingdom                      ›
Project     evener                             ›
Model       GLM 5.3 Vision   via lunaroute     ›
Effort      How long it thinks before acting
            [Low][Med][High][XHigh][Max]
Plugins     10 of 14                           ›
Access      Workspace write                    ›
Branch      Current branch (main)              ›
More options                                   ›
```

- **Prompt** first, focused on open, SF Pro 17; + attaches images. (Dictation works through the keyboard's own mic; the sheet doesn't say so.)
- **Recipes:** chips for "Same as last time" (selected by default; the last setup used for the chosen project), saved recipes, and "+ Save" to save the current setup as a recipe. A recipe sets host, project, model, effort, plugins, access and branch, and the rows beneath show what it set. After any change a Custom chip lights up instead. ("Last used" was ambiguous to first-glance participants. A round-3 line spelling out what the recipe sets was later removed: all three critics found it repeated the rows directly beneath it.)
- **Host:** hosts; connected ones carry no mark, offline ones are disabled, marked "Offline" in amber, and offer "Connect". Changing host keeps the project when it exists on the new host; otherwise it switches to that host's most recent project and says so under the list.
- **Project:** recent projects on the chosen host (name and path), then "Browse folders on <host>…" with path completion and "New folder".
- **Model:** recent (up to five), then all models grouped by provider profile. Each row: display name, provider profile, capability icons (vision, tools), context size, price per million tokens. The launch row shows the provider as "via lunaroute"; a bare profile name meant nothing to first-glance participants.
- **Effort:** "How long it thinks before acting" under the label, then a segmented control showing only the levels the model supports. The model sheet opened from here has no effort control of its own, so effort lives in one place in this flow.
- **Plugins:** "10 of 14" opens a checklist grouped by marketplace (headers as typed, in SF Mono, never uppercased), with All and None. Its footer says "10 of 14 on" without re-listing names the checkmarks already show. Each row: name, one-line description, counts (skills, agents, commands, hooks, MCP servers), and any preview warning. Footer: "Plugins can't be changed after the session starts." Start is disabled with an explanation while a selected plugin has a blocking problem.
- **Access:** Full access, Workspace write, Read-only, Restricted; network on or off.
- **Branch:** current branch, or a new worktree branch (name field).
- **More options:** the few launch settings worth touching on a phone (context strategy, max subagent depth, max turns), each showing the hub default. Everything else stays at hub defaults.
- **Start** creates the session and replaces the sheet with the new session, live. If the hub rejects the start, the sheet stays open with the hub's reason inline and the field it concerns highlighted.

## 12. Hub (settings and fleet)

Opened from the hub button. A large-detent sheet with a grouped list.

- **Header:** hub name, connection state, version ("Connected · evener 0.9.412 · up to date" or "Update available"). About doesn't repeat the version.
- Rows carry bare SF Symbols in ink-mid, never colored Settings-style tiles (those brought five hues the app doesn't have). Hubs has its own glyph, distinct from Hosts.
- **Hosts:** each row shows name, state (Connected, Connecting, Offline · last seen 2d, Error), OS and architecture, version (a gray "Hub runs 0.9.412" tag when it differs from the hub), and live session count; no green dot for a connected host. Host detail: status in words ("Connected", "Connecting…", an amber "Offline"), version, sessions ("3 live", or "3 live, out of reach" while offline), roots, last error, and actions: Connect / Reconnect (showing "Connecting…" while it tries), Edit and Remove (only for hosts added from the app or web; hosts from `hub.toml` are read-only and say so), the same set the web offers. An offline host's footer leads with what's wrong and what to do: "This host is offline, so its sessions can't be reached. Reconnect to reach them." When a connected host's version differs from the hub's, the footer says instead: "This host runs a different version of Evener than the hub. Sessions keep working. Update Evener on the host when it's convenient." There is no Update button: the hub attaches a protocol-compatible host on its own build and logs the difference, and installs its own build on connect only when a deploy path is configured (`cmd/evener-hub/internal/sshconn/manager.go`), so the phone has nothing to call.
- **Providers:** instances with sign-in status (Signed in, Expires in 3d, Sign-in expired in amber, Key set, Error). Detail: default model, models, Sign in (device code flow: the code, a copy button, "Open sign-in page", and automatic completion), Replace key (paste), Test. The hub's device flow hands the app only a page URL and a code (`AuthDeviceStartResponse`), so "Open sign-in page" copies the code on the way and opens the page in an in-app browser; the sheet says "this code is copied for you. Paste it when the page asks for it," and the waiting state repeats the code.
- **Plugins:** Installed (on-by-default switch, update badge, Upgrade, Remove), Marketplaces (add by GitHub repo or URL, refresh, remove), Browse and Install.
- **Recipes:** list, edit, reorder, delete.
- **Display:** Appearance (System, Light, Dark), Reading font (Serif, Sans), Default detail level, Show model on Board rows.
- **In-app alerts:** banners for failures (on), questions and approvals (on), finished results (off); hold alerts while reading (on); haptics (on).
- **Hubs:** the connected hub, add a hub (scan pairing code or paste link), switch, remove.
- **About:** phone app version, and Update hub (confirms) only when an update is available.

Not on the phone, with a footer line "More settings are in the web app": keyboard shortcuts, raw launch configuration fields, AGENTS.md, MCP servers, storage paths and daemon tuning.

## 13. Attention system

### 13.1 States

| Hub signal | Phone state | Mark | Band | Why line |
|---|---|---|---|---|
| `errored` / `systemError` | Failed | ✕ red (`xmark.octagon.fill`) | Needs you | "Failed: <error summary>" |
| `awaiting` + `askPending` | Question | ? amber (`questionmark.circle.fill`) | Needs you | "Asks: <first question>" |
| pending sandbox escalation | Approval | ✋ in a filled amber disc (`hand.raised.circle.fill`), the same family as the other needs-you marks | Needs you | "Wants to <action> <target>" |
| `warning` | Warning | ⚠ amber (`exclamationmark.triangle.fill`) | Needs you | the warning text |
| `restartRequired` | Restart needed | two-arrow cycle, amber (`arrow.triangle.2.circlepath.circle.fill`; a single arc read as a "C" or a spinner) | Needs you | "Restart needed · restart this session to pick up the hub's update" (says what to restart) |
| `active` | Working | pulse meter, green | Working | current activity |
| `active`, no activity 3 to 10 min | Quiet | flat pulse meter | Working | "Quiet 4m" |
| `active`, no activity 10 min or more | May be stuck | the pulse meter gone flat and amber (a ring read like the restart mark at row size) | top of Working | "May be stuck · no updates for 12m" (amber ink) |
| turn ended, not seen since | Finished | blue dot (`circle.fill`, 8pt) | Finished | last message excerpt |
| turn ended, seen | Idle | none | Idle (collapsed) | age only |
| shut down / not loaded | Shut down | none | Projects only | age only |

Marks always pair shape with color so they read without color.

### 13.2 Counts

- **Needs you** = Failed + Question + Approval + Warning + Restart needed, over live, unarchived, top-level sessions. This is the single number used on the Live chip badge, the session Back button, the Live summary line and the Next capsule.
- Finished and Working counts appear only in their band headers.
- Subagent failures never count toward Needs you; they show on the row's last line ("2 subagents failed"), on the Subagents chip ("55 · 2 failed") and in the Subagents list's strip.

### 13.3 In-app alerts (this version's push)

- **When:** a session you are not looking at becomes Failed, Question, Approval, Warning or Restart needed; or a hub notice appears.
- **Alert card:** drops in just below the nav bar (never over it, so Back, the title and the ask dock stay reachable), with an amber edge, the mark, session title and why line. It stays 8 seconds and never goes away while a finger is on it; swipe up to dismiss. Tap opens the session at the relevant spot, pushed onto the stack so Back returns to where you were.
- **Coalescing:** events within 5 seconds combine: "3 sessions need you". Tapping opens the Board scrolled to Needs you.
- **Quiet while reading:** in the Reader, the Artifact viewer, or while typing in the composer, banners are held; the Back button shows how many are waiting as an amber count (a bare dot meant nothing to a round-4 participant). Neither screen shows the Next capsule. Held banners show when you leave, combined, and Next serves the held sessions first.
- **Haptics:** warning for failures, light notification for needs you, none for finished results.

### 13.4 Phase 2: OS notifications, Live Activity, widget (designed, not built)

- **Notifications:** categories Question (actions: up to three options plus "Reply…" with text input), Approval (Allow once, Deny; requires device unlock), Failed (Open, Retry), Finished (Open; delivered passively). Grouped per session. Questions and approvals use the time-sensitive interruption level. Payloads carry only titles and short reasons unless the user opts into message previews.
- **Live Activity:** "Follow" a session from its ⋯ menu. Lock Screen: title, state, the subagent strip with counts, current activity, elapsed time. Dynamic Island compact: leading pulse meter, trailing "31 ▸ 2 ✕"; it turns amber when the session needs you. Expanded: title, activity, strip, and an Open button.
- **Widget:** small shows the Needs you count and the oldest item; medium shows the top three Needs you rows. Taps deep-link.
- **App icon badge:** the Needs you count.
- Requires server addition S10.

## 14. States and resilience

- **Connection.** Live: nothing extra (the Board header's fleet meter moves; the toolbar says nothing). Reconnecting (after 2 seconds without a connection): the Board toolbar says "Reconnecting…" and the session shows a thin ink-low bar under the nav bar; everything stays visible and scrollable. Offline (after 30 seconds): "Offline · updated 3m ago". Coming back is silent: content updates in place.
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
| prose | = ink-hi | #E0DED6 | long-form reading text (agent prose, documents, your bubbles); a step below ink-hi in dark to cut glare over hours of reading |
| bubble | = accent-bg | accent at 15% over surface | your message bubble: the accent tint in both themes, as on the web (a neutral fill read as a pressed row in dark mode) |
| attention / attention-ink | #F59E0B / #AD5209 | #F68F3C / #F68F3C | a human is needed |
| alive / alive-ink | #189A4D / #12763B | #3DBB72 / #3DBB72 | working |
| danger / danger-ink | #E3474C / #C51D23 | #EE5C61 / #F17478 | failed, destructive |
| accent / accent-ink | #0285FF / #0064C2 | #3D9AFF / #459EFF | tappable, selected, unread, links |
| accent-fill | #0070E0 | #0070E0 | filled buttons (Send, Steer, Send review, the primary approval): white on it is 4.8:1 in both themes, where white on dark mode's #3D9AFF was 2.9:1 |
| diff add / delete bg | #E9F4EE / #F5EAF0 | #19251A / #170B17 | diffs only |

Tints: each hue's `-bg` is the hue at 15% over surface; `-edge` is the hue at 40% over edge. No other hues exist in the app. Diff washes are not status colors.

Each hue has one job, including in the details: switches are accent, not the working green; a selected chip or segment is accent-bg with accent ink, never an ink fill; pins and their swipe action are ink or slate, never amber; search hits are bold, never an amber wash; "Recommended" is an ink-mid caption; version drift is a gray tag; Deny is ink. Amber appears only where a human is needed: marks, the state word on a row, the Needs you counts, the dock's edge and alerts.

### 16.2 Type

| Role | Face | Size / line | Notes |
|---|---|---|---|
| Row title | SF Pro semibold | 17/22 | Dynamic Type: Headline |
| Why line | SF Pro | 15/20 | Subheadline |
| Meta, captions | SF Pro | 13/18, 12/16 | Footnote, Caption; tabular figures |
| Your messages | Source Serif 4 | 17/25 | as on the web |
| Controls | SF Pro | 17/24 | Body |
| Sheet titles | SF Pro semibold | 17 | Headline |
| Section labels | SF Pro semibold | 12, uppercase, +0.06em | one or two words |
| Agent prose | Source Serif 4 | 17/26 | scales with Dynamic Type; optical size on |
| Documents | Source Serif 4 | 18/28; headings semibold 24/20/18 | Reader |
| Machine text | SF Mono | 13/18; tags 12 | paths, commands, model ids, code |

The serif is the conversation's voice, as on the web: what the agent wrote, what you wrote, the dock's question, notes, and the Board's finished excerpts. SF Pro is the phone's voice for everything you operate. SF Mono marks only what a machine reads (paths, commands, branch names, code), never counts or model names. Status lines (a subagent's latest activity, the hub's approval prompt) are SF Pro: the serif is for words someone wrote. "Reading font: Sans" in Display swaps the serif for SF Pro.

### 16.3 Layout, shape, elevation

- 4pt spacing base. 16pt side margins. 12pt vertical row padding. 44pt minimum touch targets everywhere.
- Radii: capsules for chips, pills and primary buttons; 18pt continuous for message bubbles; 12pt for insets and code; system radii for sheets.
- Content is flat: rows, not cards. Borders only where a boundary matters (insets, the ask dock, ghost bubbles).
- Liquid Glass (system materials) for nav bars, toolbars, sheets, menus and banners. Shadows only on floating elements (banners, menus, the Next capsule).

### 16.4 The pulse meter (signature element)

The one piece of expression in the app: a live 7-bar activity meter (22×15pt). Each bar is one minute of activity (transcript items and tool output events), newest on the right, older bars fainter so time reads left to right. Every meter shares one fixed, fleet-wide log scale, so two meters can be compared and a trickle never looks like a flood. A 1pt baseline is always drawn, and a minute with nothing in it leaves only the baseline, so a session going quiet shows a flat right end and a stuck one lies flat before turning into the amber hollow ring after 10 minutes. It is green in the alive hue and grays when the connection is lost. With Reduce Motion, bars change height without animation.

One meter per view, so motion means the thing you're watching moved:

- **Board:** on working rows in the Live band only. The same session under a pinned category or in Projects gets a still green dot.
- **Live summary line:** a fleet meter before "9 working", the whole fleet's activity summed onto the same scale; it grays when the connection drops. (In the header, in front of the hub name, it read as signal strength.)
- **Session:** in the status tray only. The title's subtitle and the live step get a still green dot.
- **Subagents list and Tasks:** still marks.

### 16.5 Iconography

SF Symbols only, weight matched to adjacent text. Core set: `questionmark.circle.fill`, `hand.raised.fill`, `exclamationmark.triangle.fill`, `xmark.octagon.fill`, `arrow.triangle.2.circlepath.circle.fill` (restart needed), `circle.fill` (unread), `magnifyingglass`, `square.and.pencil` (new session), `ellipsis.circle`, `pin.fill`, `archivebox`, `stop.fill`, `arrow.up` (send), `plus`, `command` (commands and skills), `cpu` (model), `server.rack` (host), `folder` (project), `puzzlepiece.extension` (plugins), `lock.shield` (access), `doc.text` (plan and documents), `square.stack.3d.up` (artifact), `person.2` (subagents), `checklist` (tasks), `target` (goal), `note.text` (notes), `person` (your note), `sparkles` (the agent's note), `link` (links), `globe` (web link), `text.quote` (quote), `bubble.left` (comment), `power` (shut down), `doc.on.doc` (copy), `point.3.connected.trianglepath.dotted` (hubs), `arrow.triangle.branch` (branch).

### 16.6 Motion and haptics

- State changes fade in 200ms. Row moves use a 250ms spring. Banners drop with a 300ms spring. Push, pop and sheets use the system.
- The only idle animation is the "Thinking…" pulse. Everything else moves because something happened.
- Reduce Motion replaces slides with crossfades and removes the Thinking pulse.
- Haptics: selection tick on chips, segments and lateral session moves; light impact on send; success on answer sent and approval allowed; warning on a failure banner; rigid on destructive confirmations.

## 17. Data sources

| Element | Source |
|---|---|
| Board sections, rows, children | `evener/navigation/read` (Live, needs-you, projects, pin sections, archived), `NavigationSessionSummary` (title, state, ask_pending, live, updated_at, host_id, project, branch, children, running_jobs, more_subagents, omitted_descendants), invalidated by `evener/navigation/invalidated`; task progress on rows needs S13 |
| Needs you count | `evener/attention/changed` (`AttentionSummary{NeedsYou, Error, Working}`) |
| Search | `evener/search` |
| Session content | `thread/read` with subscribe; `item/*` streaming notifications; `thread/status/changed`, `thread/queueChanged`, `evener/goal/updated`, `evener/task/updated`, `evener/notes/updated`, `evener/urls/updated`, `evener/delegate/updated`, `evener/jobs/treeUpdated` |
| Capabilities | `ThreadCapabilities` gates every control (send, steer, interrupt, compact, clear, forkFromTurn, shutdown, changeModel, queue, goal, sharedNotes, rename, skillInput) |
| Send, steer, queue, stop | `turn/start`, `turn/steer`, `turn/queue`, `turn/cancelQueued`, `turn/promoteQueuedAsSteer`, `turn/interrupt` |
| Session actions | `thread/shutdown`, `thread/model/set`, `thread/reasoning-effort/set`, `thread/fork` (with `aside`), `thread/compact/start`, `evener/thread/name/set`, `evener/archive/set`, `evener/session-pin/assign` and `unpin`, `evener/pin-section/rename` and `delete`, `evener/favorite/set` (pin project), `goal/set`, `notes/human/set`, `urls/remove` |
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
| S3 | Subagent tallies per top-level session: running, failed, done (the hub's job counts are active, failed, completed), including omitted descendants | The Subagents chip's strip and the row's "2 subagents failed" on 500-node trees | Tally loaded children; show "+N more" |
| S4 | A per-user "seen through" marker per session, with a method to set it, included in summaries | Finished-and-unseen vs Idle agrees across phone and web | Phone-local marker |
| S5 | Activity buckets per live session (events per minute, last 7 to 10 minutes) and last activity time | The pulse meter and "may be stuck" | Use `updated_at`; show a single bar |
| S6 | Direct subagent stop (and optionally message) | Stop a runaway subagent without asking the coordinator | Steer the coordinator |
| S7 | Image and document proxying for sessions on other hosts | Images and plans in remote sessions render | Show "Open on the host" notice |
| S8 | Launch recipes stored on the hub (shared with the web) | Recipes follow you across devices | Phone-local recipes |
| S9 | Document revision identity in doc reads | "Changes since you last read" | Diff against the phone's cached copy |
| S10 | Push delivery (APNs sender, device registration, event payloads, Live Activity updates) | Phase 2 | In-app alerts |
| S12 | Scoped approvals: "allow writes under this folder for the rest of this session" as an escalation resolution | A batch job (a 214-page mirror) doesn't ask 214 times | Allow once, repeatedly |
| S11 | Hub notices feed: provider sign-in expired or expiring, host offline, broken plugin, each with affected session counts | Notices row on the Board | Derive from host and instance status reads |
| S13 | Task progress on navigation summaries: tasks done, total, and the current task's title | The row's task line ("Task 4 of 7 · Fix the settle/drain race") | No task line until it lands; the session's Tasks chip still reads `evener/task/updated` |

## 19. Out of scope and future

- **Decision inbox.** Jesse expects to want one eventually and has not designed it. The Board keeps decisions inside sessions. When it exists, it would slot in as a destination from the Board header and reuse the ask dock's question and approval components.
- iPad and Android (paused and deferred), voice, a cross-session document library, desktop-only administration, and diff review beyond the evidence rendering in 8.2.

## 20. Prototype and usability testing

- Prototype: `docs/design/mobile/redesign/prototype/` (a clickable HTML model of this spec with fixture data shaped like the real usage in section 2).
- Harness and study materials: `docs/design/mobile/redesign/usability/`.
- Findings and the changes they caused are recorded in `docs/design/mobile/redesign/usability/findings.md` and folded back into this spec. Round 1 (five participants, 27 of 29 tasks succeeded) changed: edge-zone row swipes, the Next bar, alert placement and timing, Continue reading, detail-level descriptions, scoped approvals, "Finished", host update while offline, and effort in the launch flow. Round 2 (23 of 23) fixed the long-press lift and the edge-swipe back inside scroll areas. Round 3 (9 of 9) split the Next bar into a list and Next, attached comments to list items, required an explicit review verdict, and led to "Same as last time", the sign-in copy, the host version note and edge-swipe back inside stacked sheets.
- Phase 2 moved from "can people do it" to "can people read it, and does it hold together": three design critics (an iOS design juror, an information designer, a brand and craft reviewer) scored 22 screens, a first-glance comprehension test asked three personas what each screen means, and the changes were checked by running both again. That phase produced the calmer Board (fleet meter, notices as rows, the Live summary line, color only on state words, the task line), the Next capsule, the slimmer docks, the serif conversation, one meter per view, three subagent states, and the color-discipline rules in 16.1.

## Appendix A: frames to produce

For Claude Design or any visual pass. Each frame at 393×852pt, light and dark unless noted.

1. Board, busy: a notice, Needs you (failed, question, approval), Finished (two with attachments), Working (six, one "may be stuck"), Idle collapsed.
2. Board, organized by host: Projects section flipped to "Host, then project" and expanded.
3. Board, Pinned categories and Archived expanded.
4. Board, search with "In sessions" hits.
5. Board row actions: leading swipe (Archive), trailing swipe (Stop, Pin, More), long-press preview menu.
6. Board, in-app banner arriving; coalesced banner.
7. Session, working, Intent level: chips, the notes bar, messages, activity runs with durations, a subagent row with its rail, a document chip, the status tray, the Next capsule.
8. Session, question pending: ask dock with two questions, a recommended option, multi-select variant; the composer stepped aside, and back after "Other answer…".
9. Session, approval pending.
10. Session, typing while working: Steer and Queue, the first-use hint, a queued ghost bubble.
11. Session, failed: error block with Retry.
12. Session, Tools level with an expanded step showing command output and a diff.
13. Session sheet: where, model, plugins (fixed), usage with context gauge.
13a. Notes & links: the notes bar on a working session; the sheet with your note, the agent's note and three links (two web, one file); the in-app browser over it; an ended session's read-only notes.
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
24. Host detail: paradise-park offline with its last error and the version note.
25. First run: Connect to your hub.
26. Phase 2 (light only): lock screen with a question notification and actions; Live Activity; Dynamic Island compact and expanded; medium widget.

## Appendix B: fixture content

Use content shaped like real usage (section 2): about 17 live top-level sessions across evener and a few other projects, two hosts (magic-kingdom, paradise-park), subagent trees from 0 to 54 with at least one of 467 in Archived, models from several provider profiles (lunaroute: deepseek-4.1-flash, glm-5.3-vision; codex-jesse-fsck.com: gpt-5.6; meta: muse-spark-1.3; kimi-code: k3), effort mostly xhigh and high, sessions running 6 to 13 of these plugins: superpowers, elements-of-style, frontend-design, go, go-release, go-spec-reviewer, fileflow-pathologize, claude-session-driver, private-journal-mcp, shepherd-pr, iterative-development, study-skills, simplify-code, superpowers-chrome. Titles are four to six words in title case, auto-named from the prompt. Shared notes on a few sessions: both notes and three links (PR, CI checks, plan file) on the PR session, a lone file link on a finished one, an agent note and a PR link on another, and read-only notes on a shut-down one. The prototype's `data.js` is the canonical fixture.
