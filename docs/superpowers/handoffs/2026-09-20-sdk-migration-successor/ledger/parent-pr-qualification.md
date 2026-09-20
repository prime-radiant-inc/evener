# Parent PR qualification

Fresh bounded read-only snapshot taken 2026-09-19 from the `mobile-sdk-1934-pickup/evener` worktree. No PR, branch, metadata, ref, or product source was changed. Completed raw member bodies are mirrored under `reviews/raw/`; running members are recorded by status only.

## Receipts

The requested post-merge main receipt is `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e`: 19 exact-head check-runs, with executed checks successful and release-only checks skipped. Its combined REST status is `pending` with no status rows. The remote `main` branch has since advanced to `6cf3f0263887914e87dbc70d558979ecea143abb`, whose 19-run receipt is also all success/skipped. GitHub's `pull.base.sha` is the current target-main tip, not the branch ancestor, so the actual merge-base is recorded separately below.

| PR | exact current head | pull target tip (`base.sha`) | actual merge-base | merge state | exact-head CI | status | raw panel |
|---|---|---|---|---|---|---|---|
| [#1958](https://github.com/prime-radiant-inc/evener/pull/1958) | `50f6d6bf8393b0460f5d1335bc3ba5646862b9af` | `617b188c8221c92f8a4c117a1ee49aac572a3757` | `617b188c8221c92f8a4c117a1ee49aac572a3757` | open, ready, **BEHIND** | 15/15 success | `roborev=pending` | `d521616b-7983-4b56-a4d9-fdbe96df9a7e`, no synthesis/outcome |
| [#1962](https://github.com/prime-radiant-inc/evener/pull/1962) | `eb38fefeccd8d9fd22a23fcd0dcb7892fd9fe7a6` | `617b188c8221c92f8a4c117a1ee49aac572a3757` | `617b188c8221c92f8a4c117a1ee49aac572a3757` | open, ready, **BEHIND** | 15 exact; 11 success, 1 skipped, 3 failures | `roborev=pending` | `60c633a9-737e-4f34-975c-052c48a3f7dc`, no synthesis/outcome |
| [#1961](https://github.com/prime-radiant-inc/evener/pull/1961) | `a35f181df4fc9008b98bb2e8b9e26db2c5f4231a` | `617b188c8221c92f8a4c117a1ee49aac572a3757` | `617b188c8221c92f8a4c117a1ee49aac572a3757` | open, ready, **BEHIND** | 15/15 success | `roborev=pending` | `8d04f393-98c5-4635-b430-c0edf435594e`, no synthesis/outcome |
| [#1965](https://github.com/prime-radiant-inc/evener/pull/1965) | `7e44afc038bfbdbf311b5b1521e42d9e3aa41146` | `6cf3f0263887914e87dbc70d558979ecea143abb` | `3ca68fad7ab4dec468ab2d51d1f33bcca2009b3e` | open, ready, **BEHIND** | 14 exact currently: 13 success, web in progress; 15th not present yet | `roborev=pending` | `1c54a205-25bd-45aa-9998-fd2ed955b79b`, GLM running |

Unavailable or running reviewers are not passes. The raw panels for #1958/#1961/#1962 each have GLM running; #1965 has GLM running after Luna completed. No synthesized panel is available for any of the four.

## #1958 — ask restore oracle

The prior same-round reminder Medium was measured and refuted by the complete outer restore scan: the resolving user entry is still reached before older asks. The optional combined-fixture Low remains #1946.

The current remote panel has two new Luna Mediums, both belonging to the live-boundary successor seam owned by #1962 rather than to the 384-line restore oracle itself:

- live steering passes empty text evidence, so a kindless/provenance-less human-note steer can clear `askPending` live while restore preserves it;
- `processOneInput` clears pending asks before durable user-turn admission, so a failed admission can clear live state without a durable resolving boundary.

DeepSeek's only finding is a Low test-comment/documentation mismatch around non-carrier `TurnFailure`; the implementation is described as correct. Muse found no issues. GLM is still running, so the raw panel is incomplete.

Qualification: the oracle own implementation has no new unresolved production finding after the reminder refutation, but #1958 is not merge-ready. It has pending RoboRev, no completed raw synthesis, is behind the requested post-merge main, and its live-boundary successor #1962 currently has actual findings and failed CI.

Next step: resolve #1962's live-boundary findings, refresh #1958 and #1962 from their `617b…` bases onto current main, then rerun exact-head CI and obtain complete raw panels before qualifying the A/B stack.

## #1962 — live ask boundaries

The current raw panel reports three own findings:

- Medium: transcript-poison refusal settles directly to `SessionIdle`, bypassing the pending-aware failure boundary and diverging from restore's `SessionAwaiting`.
- Medium: `finishProcessingAtFailureBoundary` samples pending state and settles under separate locks, leaving a race between the sample and transition.
- Low: `clearAskPendingForResolvingSteer` passes empty text evidence, diverging from restore's write-path text-shape fallback for kindless notes.

DeepSeek found no issues. GLM is still running. Exact-head CI is not green: `race-modules / agent`, `tests`, and `race-modules` failed; `Build snapshot artifacts` is skipped. RoboRev remains pending.

Qualification: #1962 is blocked by two actual Mediums, three CI failures, incomplete raw review, pending RoboRev, and its `617b…` target tip behind current main. The text-fallback Low should be tracked as a separate follow-up under the Lows-only rule rather than forced into a later-qualified PR. The two Luna findings on #1958 reinforce the same live/restore seam and should be handled in this successor, not counted twice.

Next step: fix the two Mediums and the text-fallback Low with regression coverage, refresh onto current main, rerun all 15 checks, and obtain a completed four-member raw panel.

## #1961 — history piece one

Luna found a new own Low at `appwire-client/typescript/reducer.ts:697-699`: `itemIdentityMatches` can let a keyless fragment bridge two fragments with different `transcriptKey` values, collapsing distinct turns. The Low audit is assigned. Under the Lows-only rule, track it as a separate issue/follow-up rather than forcing a fix into this 75-line PR. Muse and DeepSeek found no issues; GLM is still running.

Exact-head CI is 15/15 success and RoboRev is pending. This is the 75-line piece-one PR; the later retained-placement work is a separate unpushed branch and is not evidence for this head.

Qualification: #1961 is not merge-ready because the raw panel is incomplete, RoboRev is pending, and its `617b…` target tip is behind current main. The separate Low audit remains open but is not itself a reason to enlarge this PR.

Next step: continue the separate Low audit, refresh onto current main, rerun CI/raw review, then continue with the separate placement piece.

## #1965 — native outbox follow-up

All three completed raw members (Luna, Muse, and DeepSeek) found no issues; their scope covers the partial #1927/#1929 conformance follow-up. GLM is still running, so there is no complete panel or synthesis yet.

The exact-head REST snapshot has only 14 check-runs so far: 13 successful and `web` in progress; the expected fifteenth check is not present in this snapshot. RoboRev is pending. Although GitHub reports pull target tip `base.sha=6cf3…`, the actual merge-base of head `7e44afc…` and target `6cf3…` is `3ca68…`. The branch is therefore behind the current target main and does need a refresh onto `6cf3…` before merge qualification.

Qualification: #1965 is not ready. CI, RoboRev, and the GLM raw reviewer remain incomplete; partial #1927/#1929 coverage is not a clean-panel or merge receipt.

Next step: let the existing CI and GLM reviewer settle, refresh the branch onto current target main `6cf3…`, then rerun the full exact-head CI and read the completed raw panel. Keep #1927/#1929 follow-ups separate under the Lows-only rule.

## Overall disposition

None of #1958, #1962, #1961, or #1965 is merge-ready from this single snapshot. #1958/#1961 have green exact-head CI but incomplete review and stale bases; #1962 has three exact-head CI failures plus actual own findings; #1965 has unsettled CI/review and only 14 checks recorded. No watcher was left running.
