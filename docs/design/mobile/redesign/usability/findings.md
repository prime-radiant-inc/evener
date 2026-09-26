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
