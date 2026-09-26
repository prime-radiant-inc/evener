# Usability findings

Virtual usability testing of the Evener for iPhone prototype. Each participant is an AI agent playing a persona, driving the prototype in its own emulated iPhone (393×852, touch) through `harness/phone.mjs`: it looks at screenshots, taps, swipes, long-presses, scrolls and types, and may not read any source. Tasks are goals, never steps. Success is scored from the prototype's own action log by `score.mjs`, so the verdicts don't depend on what participants claim.

## Round 1 (2026-09-25, prototype at 115bc90bf)

**Participants:** Orchestrator (runs 20-50 coordinators, knows Evener deeply), Newcomer (Claude Code user, day 3 on Evener), Commuter (one-handed, 2-5 minute bursts), Editor (reviews plans and artifacts, not infrastructure), Operator (hosts, providers, failures, cost). **Tasks:** 14 goals, 5 or 6 per participant, with triage (T1) given to four people. Materials: `round1/tasks.json`, `round1/moderator.md`, `round1/scores.txt`.

**Result: 27 of 29 task attempts succeeded, 2 partial, 0 failed.** Confidence "which session needs me": 4, 4, 4, 4, 5 out of 5.

| Task | Result | Median actions |
|---|---|---|
| T1 find and answer the waiting question | 4/4 | 5.5 |
| T2 review the plan, request a change | 2/2 (task flawed, see below) | 14 |
| T3 choose a layout in the artifact | 2/2 | 7.5 |
| T4 start a fully specified session | 2/2 | 20.5 |
| T5 stop the failing subagent | 2/2 | 11.5 |
| T6 queue without interrupting | 3/3 | 3 |
| T7 diagnose a failure, fix it, retry | 1/1 | 5 |
| T8 pin to a category, organize by host | 2/2 | 6.5 |
| T9 find old work | 2/2 | 5 |
| T10 read through an interruption | 1/2 (1 partial) | 15 |
| T11 approve a sandbox write | 2/2 | 5 |
| T12 show raw commands and output | 2/2 (both guessed) | 6 |
| T13 fix an offline host | 0/1 (1 partial) | 9 |
| T14 change model and effort | 2/2 | 6 |

### Problems, worst first

| # | Severity | Problem | Evidence | Change |
|---|---|---|---|---|
| 1 | 4 | An edge swipe on the Board archived the row under the finger. On the root screen there is nothing to go back to, so the gesture fell through to the row's full-swipe Archive, and the undo toast was gone before people could react. | Logs: `reset > archive:s-namer` at the start of tasks for three participants. Editor rated it 4. | Row swipes that start in the 24pt edge zone never act. Undo toast lasts 8s. Smoke check `flow-edge-swipe-never-archives`. |
| 2 | 3 | Interruptions had no path back. The alert vanished in 5s; after handling it, the plan being read had dropped into the collapsed Idle fold, so returning took a search. | Commuter T10 (severity 3); Editor T10 partial. | Alerts stay 8s and never while a finger is on them. A "Continue reading · 62%" trail on the Board reopens the document at the same position. |
| 3 | 3 | The in-app alert covered the nav bar, so a tap on Back hit the alert instead. The Editor read this as "Back jumped two levels." | Editor T10 action trace. | Alerts drop in below the nav bar. |
| 4 | 3 | Detail levels (Chat, Intent, Tools, Activity, Full) had no explanations; "Intent" read as "the agent's plan." Both T12 successes were guesses. | Newcomer (3), Operator (2), Orchestrator and Editor noted "Intent". | Each level has a one-line description; the chip reads "Detail: Intent". |
| 5 | 3 | "Update host" was offered while the host was offline, and the version changed on an unreachable machine. | Operator T13. | Update is disabled until the host reconnects, with the reason; Reconnect shows "Connecting…". |
| 6 | 2 | "3 more ›" was read as "more questions" or "more approvals in this session." | Newcomer, Commuter. | Replaced by a full-width bar above the dock or tray: "3 other sessions need you · Next ›". |
| 7 | 2 | One "Allow once" appeared to approve a 214-file write. Allow once really covers one write, so a batch job keeps asking. | Commuter T11. | The dock explains the scope, the agent asks again after Allow once, and "Allow all writes in ~/sites/docs for this session" ends the prompts. Needs server addition S12. |
| 8 | 2 | "Your move" and "Needs you" were indistinguishable to the Editor. | Editor. | The band and state are now "Finished" (blue dot = not yet seen). |
| 9 | 2 | Sheet title and its primary button were both "Send review." | Commuter, Editor. | Sheet titled "Review." |
| 10 | 2 | Effort appeared twice in the launch flow (launch sheet and model sheet), one layer on top of the other. | Orchestrator. | The model sheet hides Effort when opened from New session. |
| 11 | 1 | The artifact card kept highlighting B after choosing C. | Commuter. | The card shows the chosen option. |
| 12 | 1 | "Lane" was unexplained in subagent rows. | Orchestrator. | Now "own branch fix-settle-race". |
| 13 | 1 | "Steer or queue…" placeholder read as jargon before typing. | Editor, Commuter. | Placeholder "Tell the agent something…"; the Steer/Queue hint still appears when typing. |
| 14 | 1 | The session title's tap target (opens Session info) wasn't obviously tappable and sat close to Back. | Editor. | Small chevron after the status line; more space from Back; pressed state. |

Earlier fixes from my own review before the round: document chips show the document's title, not its path; the long-press preview no longer mangles markdown tables.

### What worked

- Steer vs Queue explained at the moment of choice was the single best moment for three participants ("the best piece of design in the app").
- The Board's Needs you band with a reason on every row: every participant found the waiting question in about 5 actions.
- Approval cards naming the exact tool, path and scope.
- "Ask coordinator to stop it" with its explanation and pre-filled message.
- Long-press to comment in the Reader "felt exactly like Google Docs or Figma"; the artifact comparison with an editable proposal was the Editor's highlight.
- Search found week-old archived work in one query.
- The model sheet (search, effort, "Applies from the next turn").

### Test-method problems found and fixed

- The harness tapped elements hidden behind sheets (the launch sheet's Effort "Max" behind the model sheet, the Board's Reconnect notice behind the Hosts sheet). This caused the "Max selected the wrong model" and "Reconnect did nothing" reports. Taps now go only to what a finger would touch, at a visible, uncovered point.
- Placeholders weren't matchable by text; now they are.
- The test server's missing favicon produced one console error per participant; it now answers 204.
- T2 asked for a change the plan already contained. Round 2 asks the reviewer to answer the plan's open question instead.

## Round 2 (2026-09-25, prototype at d76654cae plus round-1 fixes)

Five fresh participants with the same personas, so nobody had learned the app. Tasks re-tested every round-1 change; T2 was replaced by T2b (answer the plan's open question in a review). Materials: `round2/tasks.json`, `round2/scores.txt`.

**Result: 23 of 23 task attempts succeeded.** Against round 1: approving the batch write fell from 5 median actions to 2 (both chose the new scoped allow), reading through an interruption from 15 to 7, fixing the offline host went from partial to done in 8, launch from 20.5 to 17.5. Confidence "which session needs me": 5, 4, 4, 4, 5.

### Problems, worst first

| # | Severity | Problem | Evidence | Change |
|---|---|---|---|---|
| 1 | 3 | Long-press to comment on a plan failed: the menu opened while the finger was down, and lifting it produced a click that either landed on the scrim (closing the menu) or on the menu item that appeared under the finger ("Copy"). Paragraphs higher on the screen happened to work. | Commuter T2b: `block_menu` logged on every attempt with no comment sheet; Editor T2b: every long-press "just copied". Both took 16-25 actions. | The one click produced by lifting a long-press finger is swallowed; any new touch clears that. Pressed paragraphs highlight while their menu is open. Smoke check `flow-comment-on-low-paragraph`. |
| 2 | 3 | Alerts held while reading were invisible: nothing in the Reader said something was waiting. | Commuter T10: "no interruption came up". | The Next bar appears in the Reader and Artifact viewer too, with "1 new" for alerts that arrived while reading. |
| 3 | 3 | Edge-swipe back did nothing in the Reader and in sessions: their scroll areas let the browser claim the drag and cancel the pointer. | Editor T10, twice. | Edge-swipe back is also detected from raw touch events. |
| 4 | 3 | "Reply" in the Reader's review bar jumped to the session's chat, which read as a document reply. | Editor. | Removed; Send review is the way to respond to a document. |
| 5 | 3 | A Board notice wasn't tappable, so the host's actual error was two screens away. | Operator T13. | Tapping a notice opens its host or provider detail; its action button still acts directly. |
| 6 | 3 | The context-chip row scrolled sideways with no hint; Detail was off-screen. | Newcomer T12. | Detail is the first chip (dashed, since it's a view setting); the row fades at its right edge. |
| 7 | 2 | Needs you rows were told apart only by their marks. | Newcomer, Editor. | Why lines lead with a word: "**Question** ·", "**Approval** ·", "**Failed** ·", "**Restart needed** ·", "**May be stuck** ·". Alert cards use the same words. |
| 8 | 2 | The scoped approval was a small link under the buttons. | Newcomer. | Three stacked choices that state their consequence: "Allow all of ~/sites/docs · for the rest of this session", "Allow this file only · it will ask again for the next one", "Deny". |
| 9 | 2 | The Projects chip renamed itself to "Hosts" when grouping changed. | Orchestrator. | The chip always says Projects; only the section heading changes. |
| 10 | 2 | Editing a setting silently deselected the "Last used" recipe. | Orchestrator. | A "Custom" chip lights up when the settings match no recipe. |
| 11 | 2 | The plugin picker had no search. | Operator. | Search, and the footer lists what's on. |
| 12 | 1 | "Ask coordinator to stop it" read oddly on a subagent that had already failed. | Orchestrator. | Failed or waiting subagents offer "Ask coordinator to stop retrying it". |
| 13 | 1 | A question's last option could sit under the fold unnoticed. | Orchestrator. | The option list fades at its bottom edge when it overflows. |
| 14 | 1 | Whether a detail level re-renders history was unclear. | Operator. | "…including what's already there." |
| 15 | 1 | "Access: Workspace write" was unexplained at launch. | Operator. | The Access row says what the choice allows. |

### Test-method problems found and fixed

- Two reported problems were artifacts of each task starting from fresh data ("the Board shows stale status", "changes since you last read reappeared"). Round 3 instructions say every task starts fresh.
- Taps right after a sheet opened could miss while the sheet was still sliding in. The harness now waits for finite animations to settle before locating a target.

## Round 3 (2026-09-25, prototype at cce8ae3df)

Three fresh participants (Editor, Commuter, Operator) on the tasks round 2 changed most, plus launch, sign-in and the offline host. Materials: `round3/tasks.json`, `round3/scores.txt`.

**Result: 9 of 9 task attempts succeeded.** The problems were about understanding, not completion:

| # | Severity | Problem | Evidence | Change |
|---|---|---|---|---|
| 1 | 3 | "N other sessions need you · Next" was one control, so there was no way to see which sessions before jumping, and alerts held while reading weren't served first. | Editor and Commuter, T10. | Split into a list of what needs you and Next; Next serves held alerts first. (Phase 2 later turned this into the Next capsule.) |
| 2 | 3 | A comment on a bullet attached to the whole list. | Editor, T2b (answering one open question). | Comments attach to the list item under the finger, which highlights while the menu is open. |
| 3 | 2 | The review sheet pre-selected a verdict. | T2b. | Nothing is chosen for you; Send stays disabled until you choose. |
| 4 | 2 | The "Changed" marker sat on a heading, reading as part of the title. | T2b. | The blue rule alone marks a change. |
| 5 | 2 | Edge-swipe back inside the plugin picker (a sheet over the launch sheet) did nothing. | Operator, T4. | Edge-swipe back closes the top stacked sheet; one swipe can no longer go back twice. |
| 6 | 2 | "Last used" didn't say what it would set; effort wasn't explained. | Operator, T4. | "Same as last time", a line saying what it sets (later removed in phase 2, when the rows beneath were found to say the same), and "How long it thinks before acting". |
| 7 | 2 | Sign-in copy promised a page with the code filled in. | Operator, T7. | The hub's device flow sends only a page URL and a code, so the app copies the code on the way and says so. |
| 8 | 2 | The host's "Update host" button had nothing real behind it. | Operator, T13, checked against the hub: a protocol-compatible host attaches on its own build (`cmd/evener-hub/internal/sshconn/manager.go`), and the web offers no update action. | Removed; a note says sessions keep working. |

Also noted, not changed: the artifact's note field couldn't be typed into by the harness (its focus stays on the iframe; a harness limit, not a design problem), and the Editor would like a role filter that hides engineering failures from Needs you (future work).

## Phase 2: intelligibility and craft (2026-09-25)

With usability holding, phase 2 asked whether people can read the app at a glance and whether it holds together. Two instruments, each run twice (before and after the changes):

- **Three design critics** (an Apple Design Award-style iOS juror, an information designer, a brand and craft reviewer; Opus) scored 22 to 23 gallery screens and ranked twelve changes each. They saw screenshots and the spec, never the source.
- **A first-glance comprehension test**: three personas (Newcomer, Editor, Orchestrator; Sonnet) answered questions about each screen from a single look, graded against a key.

Critic scores, first pass → rerun:

| Critic | Improved | Unchanged | Worse |
|---|---|---|---|
| iOS juror | color 5→7, elegance 6→7 | hierarchy 7, type 8, spacing 7, consistency 6, native feel 7, density 6, distinctiveness 8, touch 7 | iconography 7→6 |
| Information designer | data-ink 5→7, encoding 5→6, density 4→5, redundancy 4→5, Board 5→6, transcript 6→7, type 7→8, color 6→8 | consistency 5, numbers and time 6 | none |
| Brand and craft | color 5→7, restraint 4→6, family resemblance 6→7, craft 6→7, dark mode 6→7 | distinctiveness 5, copy 7, delight 5 | type voice 7→6, signature 5→3 |

What phase 2 changed (details in the spec, sections 7, 8, 9, 10, 16):

- **Board:** the fleet pulse meter moved into a Live summary line ("4 need you · 4 finished · 9 working · 3 idle", each a jump); notices became rows; only the state word and the mark carry color; the finished excerpt is in the serif; project and host print only when unusual; the last line shows task progress ("Task 3 of 4 · Cap retries per provider", Jesse's call) and subagents only when some failed; meters move only in the Live band.
- **Session:** the full-width amber Next bar became a floating capsule ("4 others need you  Next"), hidden while this session asks you something and in the Reader; the tray shows only while working; Detail moved from the chip row into the ⋯ menu; the docks drop their transcript duplicate and push the composer aside; approval choices are buttons, narrowest grant first; notes and links, like the web.
- **Everywhere:** icon tiles, amber pins, green switches and ink-filled chips are gone; three subagent states (the hub's own); one model name; a darker fill blue so white text passes contrast in dark mode; a two-arrow restart mark and filled-disc marks.

What the first-glance retest caught, and what changed after it:

- "Next" alone, with the next session's red mark, confused all three participants, and all three guessed it meant "the next failure"; the capsule now has words.
- All three read the notes bar as the original prompt, with its link count as attachments; it now says "Your note:" and "3 links".
- "2/4 · Cap retries per provider" read like a setting to one participant; it now says "Task 3 of 4".
- "lunaroute" under Model meant nothing to all three; it reads "via lunaroute".
- All three guessed the blue dot right (finished, not yet seen) but with low confidence; unchanged.

Where the critics disagreed with the evidence, the evidence won. Two critics wanted the Live summary line gone and one wanted it kept; participants relied on it for "how many are working", so it stayed. One critic wanted the folder-wide approval back as a link under the buttons; round 2 showed people miss it there. One critic wanted your messages in SF Pro; the web sets them in the serif (`usermessageitem.module.css`), so the phone matches the web.

## Round 4 (2026-09-25, prototype at 32225b853)

Four fresh participants (Orchestrator, Newcomer, Commuter, Editor) on the tasks phase 2 touched most (answering, approving, reading through an interruption, finding commands with Detail moved into the menu, stopping a subagent, queueing, reviewing, launching) and two new ones: reading a session's progress off the Board without opening it (T16), and opening a session's PR from its links and leaving it a note (T15). Materials: `round4/tasks.json`, `round4/scores.txt`.

**Result: 11 of 12 succeeded, 1 partial.** Reading progress off the Board took 2 actions and no taps into the session: the task line and the activity line carried it. Both participants who looked for commands found Detail level in the ⋯ menu (9 and 7 actions), so moving it out of the chip row cost nothing. The approval (2 and 4 actions) and queueing (5) were rated 7 of 7 easy. The note was the failure: 32 actions each, one partial.

| # | Severity | Problem | Evidence | Change |
|---|---|---|---|---|
| 1 | 4 | The note editor didn't look or act like an editor: no visible field or focus, taps landed mid-word, and the save was delayed and silent. | Editor T15 concluded the note couldn't be edited and sent a steer instead (partial); Newcomer T15 corrupted the note twice before finding the end of it, and couldn't tell whether it saved. | A visible field with a focus ring; opening from the notes bar puts the cursor at the end of your note; closing the sheet saves at once with a "Note saved" toast; the status line no longer echoes Steer's wording. |
| 2 | 3 | Next went somewhere other than what had just alerted you. | Commuter T10: the alert for a question vanished, and Next went to a failed session instead. | Next goes to whichever session alerted you most recently, then Needs you order, and names it: "Next  <title> ›". |
| 3 | 3 | Back lost your place after Next. | Commuter T10: after handling an interruption via Next, Back went to the Board, not to the plan being read. | Next pushes from a session you chose, and replaces only within a run of Nexts, so Back returns to where you started. |
| 4 | 3 | A comment on the second open question showed its marker on the first. | Editor T2b. | Markers sit on the list item they belong to. |
| 5 | 3 | "Stop requested" never visibly completed. | Orchestrator T5. | When the subagent stops, its row reads "Stopped at your request" and a toast names it. |
| 6 | 2 | The Reader's bare amber dot on Back meant nothing. | Commuter T10. | Back shows a count. |
| 7 | 2 | Changing detail level gave no feedback when the change was above the visible text. | Orchestrator and Newcomer, T12. | A toast names the level and what it shows. |
| 8 | 2 | A pinned live session repeated its full row in its category, read as a second job. | Orchestrator T5. | Rows in pinned categories are quiet one-line rows. |
| 9 | 1 | "Ask aside…" had no explanation; "Restart needed" didn't say what to restart. | Newcomer; Commuter. | "A side question in its own session; this one keeps working"; "restart this session to pick up the hub's update". |

Also noted, not changed: the model picker lists a recent model twice (Recent and its provider group), the usual iOS pattern; "XHigh" read like a typo to one participant, but effort labels come from the shared `effortLabel` vocabulary the web also uses, so a rename belongs there.
