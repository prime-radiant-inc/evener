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
