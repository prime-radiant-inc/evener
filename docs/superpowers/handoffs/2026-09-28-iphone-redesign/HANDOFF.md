# iPhone redesign: handoff (2026-09-28)

This memo is for whoever picks up landing the iPhone redesign. It records where the work stands on 2026-09-28, every ruling Jesse has made, and how a PR gets to main. The plans say what to build; this says what's decided and what's in flight. Update it at the end of each phase.

The goal: land phases 2 through 7 of the roadmap on main, built to the spec. It's done when every phase is merged, `make test-native` passes on main, and every Appendix A frame has been shown from a Release simulator build.

## What to build

| Document | Path |
|---|---|
| Spec | `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md` |
| Roadmap | `docs/superpowers/plans/2026-09-25-iphone-redesign-roadmap.md` |
| Phase 1 plan | `docs/superpowers/plans/2026-09-25-iphone-redesign-phase1-foundations.md` |
| Phase 2 plan, parts 1-3 | `docs/superpowers/plans/2026-09-26-iphone-redesign-phase2-board.md`, `...-board-part2.md`, `...-board-part3.md` |
| Phase 3 plan | `docs/superpowers/plans/2026-09-26-iphone-redesign-phase3-session.md` |
| Phase 4 plan | `docs/superpowers/plans/2026-09-26-iphone-redesign-phase4-subagents-reader.md` |
| Phase 5 plan | `docs/superpowers/plans/2026-09-26-iphone-redesign-phase5-new-session-hub.md` |
| Phase 6 plan, part 1 | `docs/superpowers/plans/2026-09-26-iphone-redesign-phase6-attention-resilience.md` (parts 2 and 3 are open PRs #2588 and #2586) |
| Server (phase 7) | `docs/superpowers/plans/2026-09-26-iphone-redesign-server-additions.md`, with S4/S5/S3 in `...-server-s4-s5-s3.md` and S1/S13b in `2026-09-27-iphone-redesign-server-s1-s13b.md` |

The plans carry numbered rulings (for example, phase 2 part 3's ruling 21). Where a ruling below changes one, this memo says so.

## Status by phase

| Phase | State |
|---|---|
| 1 Foundations | Merged: #2436, #2448, #2443, #2454. |
| 2 Board | Merged: #2463, #2471, #2518, #2591, #2618, #2619, #2630, #2596, #2581, #2623, #2615, #2639, #2625, #2634. Left: PR 8 activity pulse (#2651), PRs 4a/4b/4c (swipes, long-press menu, select mode), search's Projects results group, fast-follows #2682 and #2689, and the phase's screenshots (Appendix A frames 1-7, part 3 Task 17). |
| 3 Session | 13 PRs in three lanes (A: 1, 2, 3, 10, 11, 12; B: 4, 5, 6; C: 7, 8, 9; then 13). PR 1 (#2578) and PR 7 (#2575) are being brought up to main to merge. PR 4 is in its review round. The rest haven't started. |
| 4 Subagents and Reader | Plan merged (#2515). 9 PRs, not started. |
| 5 New session and Hub | Plan merged (#2505). 13 PRs, not started. |
| 6 Attention and resilience | Part 1 of the plan merged (#2511). Parts 2 and 3 (#2588, #2586) are being revised to fix their review findings. Not started. |
| 7 Server | Merged: PR 1 #2473, PR 2 #2514, PR 3 #2508, PR 4 #2560, PR 5 #2513, PR 6 #2526, PR 7 #2641, PR 8 #2675, PR 10 #2652, PR 12 #2502, PR 13 #2642; S5 #2561, #2582, #2593; S4 #2566, #2584; S3 #2570, #2587. Left: PR 9 (S1c failure summary), PR 11 (S1d on rows), S11 and S14 (a plan is being written), then S12, S7, S9, S8, S6 (stop only), S15-S19, and #2479 (a rejected token refresh isn't recorded; the Board's sign-in notice wants it). |

A live status board with one row per PR and issue is the tracker artifact in Jesse's claude.ai account: https://claude.ai/artifact/J9GdVqPL6dCZGkFYHqUn86 (private until he shares it).

## In flight

Branches marked "local" exist only in a worktree under `.claude/worktrees/` on Jesse's Mac until their owner pushes.

| Work | Branch | Next step |
|---|---|---|
| Phase 2 PR 8, activity pulse (#2651) | `claude/iphone-redesign-p2-activity` | Last review and /simplify, merge main, merge on green |
| Phase 2 PR 4a, swipes | `claude/iphone-redesign-p2-swipes` (local) | Review, /simplify, open the PR; checked on the simulator |
| Phase 2 PRs 4b and 4c | not started | Same owner as 4a, in order |
| Search's Projects results group | not started | Deferred from #2634; uses 3b's `revealProject` |
| Seen-marker fast-follow (#2682) | open | Merge on green |
| Search duplicate fast-follow (#2689) | open | Merge on green |
| Phase 3 PR 1 (#2578) | `claude/iphone-redesign-p3-pr1` | Merged with main, its tests failed: the navigation mock lacks `useNavigationState` and `__DEV__` is undefined. Fix, then merge on green |
| Phase 3 PR 7 (#2575) | `claude/iphone-redesign-p3-pr7` | One review round (below), a `screens.tsx` conflict with main, then merge on green |
| Phase 3 PR 4 | `claude/iphone-redesign-p3-pr4` (local) | Its review found one bug and one spec deviation; fix, re-review, open the PR |
| Server PR 9 (S1c) | `claude/iphone-server-p9-s1c` (local) | Tasks 9.3-9.5 on top of PR 8, then the PR |
| Server S11 and S14 plan | `claude/iphone-server-plan-s11-s14` (local) | Being written; a docs PR |
| Phase 6 plan parts 2 and 3 (#2588, #2586) | open | Fixing RoboRev's findings, then merge |
| Flake fix for #2607 (#2650) | open | A cleanup pass and one RoboRev Medium, then merge |

#2575's review round, ruled on 2026-09-27: runs show durations at Intent, while a step shown only as its summary keeps its clock time (spec 8.2); the copies of `timeOf` use `board/attention`'s `hubTime`; opening a run skips attachment rows; `RunRow` gets stable keys.

Handed to an engineer (small, off the critical path): #2664 (server Lows batch 5, pushed on `claude/iphone-server-lows-5`, two review findings left), #2665 (five Board search and notice polish items), #2614, #2646, #2655, #2669, #2670, #2681, #2629 (a "doesn't need you" band on the web), and #2497 (tests that fail or hang only on macOS).

## Jesse's rulings

### Answers to the plans' questions (2026-09-26)

1. Detail levels: Intent is Chat plus one folded "n actions" line per run of tool calls. Tools, Activity and Full follow the shared presets.
2. Superseded by 18.
3. Retry on a failed turn sends "Something went wrong. Please try again."
4. The phone hides "may be stuck" while subagents run.
5. Full administration on the phone is a later phase (#2539). Phase 5 deletes none of today's admin screens.
6. Backward compatibility for drafts saved before phase 5 is approved, not required. Keep the optional `source` field.
7. Hub update installs and restarts, as the web does, through one client API in `@evener/appwire-client` shared with the web.
8. Phase 4 retires the Activity sheet. A view of running commands and subagents is #2538.
9. The UI sends to whatever session is open, coordinator or subagent. Nothing reroutes a subagent's review to its coordinator.
10. "Ask coordinator to stop it" shows only while that subagent's work runs.
11. Sheets are native-stack `formSheet` routes behind one `sheetOptions()` and `<Sheet>` pair. Hub and New session stay modal routes.
12. A Board row is red only when the coordinator itself failed. Subagent failures show on the Subagents chip and list.
13. An unseen Finished session doesn't keep its daemon from retiring.
14. Narrowed by 19.
15. The phone archives sessions on other hosts. This supersedes phase 2 part 3's ruling 20.
16. Board actions taken offline are held and sent on reconnect (spec 7.5). Phase 6 builds the hold. Until then, part 3's ruling 21 stands: hub-writing Board actions are hidden while offline. A held Stop names its turn and is dropped if that turn ended.
17. Select mode holds the list until Done or an action.
18. Approvals are rare in real use. The approval dock offers Allow and Deny, as the web does. No redirect.
19. Messaging a running subagent is tabled. S6 is direct stop only. A subagent's screen shows the normal composer once its run has ended, and "Ask coordinator to stop it" plus "Open coordinator" while it runs.
20. A banner catches up on what changed during a drop that recovers while you're in the app. None after returning from the background.
21. Banners don't show over a sheet. They're held and shown when it closes.

### Later rulings

- 2026-09-27: Needs you follows the hub's order (`hubapi.NeedsYouBand`): failed first, then questions and approvals, then warnings and restart-needed. The web should move merely finished sessions out of Needs you (#2629).
- 2026-09-27: A small Medium from review gets merged, then fixed in a quick follow-up PR.
- 2026-09-27: PR owners are Sonnet when the plan carries the code. Opus goes to screen-heavy and design PRs, server designs, /simplify (one reviewer), whole-branch reviews and debugging.
- 2026-09-27: Flakes and small issues off the critical path become GitHub issues for a less senior engineer.
- 2026-09-27: Ready phase 2 PRs can merge without waiting for RoboRev.
- 2026-09-28: "Don't hold back. Merge." Phases no longer land in order. Every ready PR merges once CI is green on its merged head.
- 2026-09-28: One alert per needs-you state ("no. one alert is great."). A session that already needs you does not alert again when it asks a new question or names a new approval target (phase 6 part 2, question 3).
- Sequencing (coordinator, 2026-09-27): S11 and S14 come before S12, since approvals are rare.

### Standing rules

- Ask Jesse only about decisions the spec doesn't settle that change product behavior, and about wire changes that aren't additive. Additive AppWire changes (optional keys an old peer reads as "no information") need no protocol version bump.
- Open regular PRs, never drafts.
- Coordinator tooling stays out of the repo.

## How a PR lands

1. The owner works in its own worktree: a failing test first for each task, one fresh reviewer, then /simplify with one Opus reviewer, then a merge of `origin/main`, then one push.
2. CI must be green on the PR's merged head. CI builds the PR merged with main, so a PR that's far behind can pass and still break once merged: #2578 passed its typecheck and failed 10 native tests after a main merge. Merge main and rerun before landing a stale PR.
3. Read RoboRev's combined comment itself, not only its check. It edits one comment in place, and the header names the commit it reviewed. Critical and High findings are fixed, or refuted on the PR with file:line evidence that someone other than the owner checks. A small Medium merges with a follow-up PR. Lows go to a follow-up or an issue.
4. Merge with `gh pr merge <N> --squash --admin --match-head-commit <full 40-character SHA>`.
5. Retire the worktree and update the tracker.

After five review rounds on one PR, stop and split it rather than going around again.

## Tools and notes outside the repo

These live in gitignored `.superpowers/` folders on Jesse's Mac.

- `.superpowers/bin/` at the repo root:
  - `pr-gate [--body] <PR>` prints CI state and RoboRev's findings, and the merge command.
  - `pr-wait <PR>` waits until CI and RoboRev settle on the current head.
  - `orphan-sweep` lists worktrees that are merged, have a PR, or hold commits with no PR and no owner.
  - `retire-worktree <names>` removes merged worktrees after archiving their `.superpowers/` to `.superpowers/archive/`.
  - `agent-status` and `tracker-show` read this session's subagent transcripts and a tracker dump.
- `.superpowers/protocols/`: `pr-owner.md`, `sonnet-owner-steps.md` (the owner checklist) and `planner.md`.
- Each worktree's `.superpowers/sdd/<branch>/progress.md` is its owner's ledger. Retired ones are under `.superpowers/archive/`.
- Research notes, including the verbatim wording behind Jesse's answers and the sheet-detent research, are in the coordinator worktree `.claude/worktrees/modest-chatelet-2344ac/.superpowers/research/`.

## Gotchas

- Every worktree needs its own `npm ci` in `mobile-native`, `cmd/evener-hub/frontend` and `appwire-client/typescript`. Never run `npm ci` through a symlinked `node_modules`; it deletes the shared install under every other worktree.
- Never run Biome in `mobile-native` or `mobile`: neither has a Biome config.
- Run gofmt as `$(go env GOROOT)/bin/gofmt`. Run `go vet` also with `-tags evenerfuzz` and with `GOOS=windows`.
- Some tests fail or hang only on macOS (#2497). CI is the judge.
- The simulator is shared: one lane at a time. Metro runs on 8081 and the demo hub on 9196 (`cd mobile-native && EVENER_DEMO_FLEET=1 npx tsx scripts/demo-hub.mts`). The booted iPhone 17 Pro has a Debug build of main's native code, so JavaScript-only changes need no `xcodebuild`. The demo fleet answers no mutations and doesn't serve the `location` navigation read.
- If Xcode builds stall, check for kernel pipe memory exhaustion. ChatGPT's Codex app-server caused it once; quitting ChatGPT fixed it.
- Subagent owners: dispatch reviewers in the foreground, since a background reviewer's result has twice failed to reach its owner. After a usage-limit stop, check `git status` before resuming (one owner stopped partway through a merge), and run `orphan-sweep` for work nobody owns.
