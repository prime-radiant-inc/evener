# Handoff: native iPhone landing (#1116) and the AppWire SDK migration

Written 2026-09-16 ~08:30 PDT by the coordinator session that ran this queue from 2026-09-12. Everything below is what the next owner needs to continue without the transcript. The files beside this memo are the working artifacts: `progress-ledger.md` (timestamped ledger of every action, authoritative for what happened when), `phase-d-worksheet.md` (row research, waves, shared files, re-scope notes), and `piece-a-seq-plan.md` (the A2 design and its correction). The PR watcher script is coordinator tooling and is not kept in the repo; the coordinator keeps it beside the live ledger under the gitignored `.superpowers/` directory of its worktree.

## 1. The charge

Two standing assignments from Jesse:

1. Land the native iPhone v1 (issue #1116 is the spec) to main, including a TestFlight build and device acceptance.
2. Migrate both frontends, web (`cmd/evener-hub/frontend`) and native (`mobile-native` + `mobile/src`), onto the TypeScript AppWire SDK package `@evener/appwire-client` (source at `appwire-client/typescript/`), one small PR at a time, following `docs/superpowers/plans/2026-09-12-sdk-migration.md`.

Plus a third thread that grew out of #1100: the split of "round diagnostics replay" into pieces A, B, C. Piece A is being landed as small PRs on the transcript's existing `Entry.Seq`; A2 is the one still open.

## 2. Where things stand (as of this memo)

Main is `3e4f3a32a` (D13, its main run still in progress when this was written); every earlier merge today passed its main run, green through `74d6c81d6`. The only red runs were the `race-modules / agent` time-cap flake (#1394), now fixed by #1545 (the race job on main's run for that merge took 7m44s against a 10m cap; it had been 600 to 657s). One new root-module hang was seen once on a PR run (#1551).

### SDK migration

- Phase A: done (package moved to `appwire-client/typescript`, resolver plumbing, qualify script).
- Phase C (27 pure-module relocations): complete as of 2026-09-16 ~01:40 PDT.
- Phase D (state cores): **18 rows or slices merged**: D30, D7, D2, D3, D20, D10, D1, D8 1a, D19', D21, D22, D23a, D8 1b, D9, D23b, D23c-1, D5, D13. Plus follow-ups #1510, #1518, #1521, #1528, #1532 (#1516), #1544, #1546, #1548.
- Withdrawn rows: D19 (not a twin; replaced by D19' which adopts native's `delegateDetails` on the web), D4 (hub update store is not a twin). D17 re-scoped to D17'' (cycle cut only). D8 split into 1a/1b, D9 became native-only, D23 split into a, b, c-1, c-2a, c-2b, d.
- Open PRs in the phase D serial queue, in order:
  1. **D17'' #1498** @cd3e52b07. CONFLICTING on the plan doc; CI never ran on this head (GitHub does not run PR workflows on a conflicting head). RoboRev has one Low: the plan table's D17 row still describes the superseded `activityList` adoption; annotate it in place ("re-scoped, see status row"). Needs: refresh onto main, fold the Low, re-arm, /simplify check (I do not have a record that /simplify ran on it; run it), merge on green+clean.
  2. **D11 #1502** @737554882 (plugins store core at `state/extensions`). CONFLICTING; needs refresh, then CI/RoboRev; /simplify status unknown, run it. Agent id in the ledger: `ad8fe23dd0ce4b2a6`.
  3. **D6 #1549** @145f7fba2 (transcript display hub-defaults store core). Complete per its implementer, all gates green locally, watcher armed. **/simplify has NOT been run** on it. Two decisions are stated in the PR body (see section 6). Serial-queue PR: touches `index.ts`, `tsconfig.build.json`, the qualify smoke, README and the plan.
  4. **D23c-2a #1547** @420413ff1 (dual-write: model live under every item frame). /simplify done. CI 15/15 on this head; RoboRev round 2 found one Medium and one Low (disposition posted on the PR): the reducer's `item/toolOutput/delta` does `output + delta` (O(n²), unbounded) while `pendingText`/reasoning use the O(1) `appendChunk`, so c-2a's model half retains full tool output after the row froze at 64 KiB. The right fix is in the reducer (package change: `output` onto the chunk append; run `test-api-package` + falsification and `test-web`), and it connects to #1535. The Low is a stale contract comment in `mobile/src/conversation/project.ts:44-46`. The PR is CONFLICTING again after the D13 merge (plan table) and needs a refresh. Mobile-only plus the plan row.
- Follow-ups awaiting review: **#1539** (test-side: stop leaking delegate stop reconcile drivers) is RoboRev-clean; its `tests` job timed out at 10m on head `c4d135c97`, but the hang was in the **root module** (`TestMakeTestWebInterruptDoesNotSignalReapedCheck` ran 9m28s), unrelated to #1539's agent-package change; filed as #1551. Rerun (or rebase onto main, which now has #1545) and merge on green.
- **A2 #1480** @c49762f2a (piece A2: the fold names its retained entries by Seq, closes #1200). 10 review rounds, /simplify folded in round 9, CI green and RoboRev down to Lows for the last three rounds. Round 10's Low (disposition posted): the fork-provenance boundary in `agent/session_init.go:922-931` is a leading-run scan, but a fold record emits the head marker first, so a compacted fork child's retained parent turns are classified as own and can be routed through the child's mutation journal; derive the boundary from the retained turns' own origins. That is round 11 for the next owner. **Merge is HELD for Jesse's compatibility ruling**: pre-A2 readers fail a whole transcript cleanly on the new `FOLD_RECORD` entry (`decodeStrictJSON`). Recommendation on file: accept the break.

### Phase D rows not started

D12 (extensions dirs+MCP, after D11), D14 (navigation store, after D13, High: zustand + `railExpansion` localStorage at module init), D15 (selectors, after D14), D16 (roster reads, after D15), D18 (activity counts/summary, Jesse question on D17'/D18 convergence), D23c-2b and D23d (briefs in section 5), D24 (native adopts `transcriptDisplay/projector.ts`, **visible product change**: native gains config-driven content levels), D25 to D28 (queue/outbox/dispatcher/connection lifecycle, all High), D29 (native doc pane, Jesse's Q8).

### #1116 deliverables not started

TestFlight build 5 and the device acceptance loop (Jesse-gated; build 4 is live, see memory `testflight-build-4-live`). SDK publication decision after phase D.

## 3. Jesse's rules (apply all of them)

Merge gate: current-head CI green AND RoboRev's Combined Review body reads clean (read the body; never trust the watcher's verdict) AND /simplify has run on the PR (four read-only reviewer agents: reuse, simplification, efficiency, altitude; apply the fixes; for small PRs, "simplify by reading" is acceptable and is recorded in the ledger as such).

- Merge command: `gh pr merge N --squash --admin --match-head-commit <full 40-char SHA from gh pr view N --json headRefOid>`. Never pad the watcher's 9-char head.
- Lows-only review → merge and immediately open a follow-up PR fixing the Lows. Never spend a review round on Lows alone. Exception used today: when a PR's merge is held anyway (A2), fix Lows in place.
- Merge on green+clean even if main moved since the review; fix forward if the post-merge main run finds a semantic conflict.
- No verdict carry-over across a refresh (merge-only or otherwise): the new head gets its own CI and RoboRev.
- No draft PRs. Split PRs smaller. Fix real findings; there is no "stop reviewing" rule.
- Never rewrite an implementation without Jesse's permission. No backward compatibility without Jesse's explicit approval. Measure before refuting or accepting a review finding. No commit trailers. Comments describe what code does now, not history.
- The native app never shows a "refresh to see the latest" affordance; it auto-refreshes (ruling 2026-09-13). This has been applied repeatedly (D5, D6, D23).
- Never `rm -rf`. Never bare `git stash` (shared stash across worktrees). Never `git checkout -- <file>` to restore a falsification; use `git diff | git apply -R`.
- Every residual, smell or design question becomes a GitHub issue the same turn it is noticed (Jesse, 2026-09-11).
- Ask Jesse questions one at a time with tradeoffs, naming the decision, not an identifier.
- Time estimates in lines of code, never wall-clock.

Standing decisions on the SDK plan: decision 1 zero runtime deps (framework-free stores; apps wrap them); decision 2 both frontends adopt the web's two layers and, where the two appliers disagree, the web's rule wins (native-only special cases only when the phone user would lose something the web user never had); decision 3 package at `appwire-client/typescript/`; decision 4 keep the web's session-usage derivation.

## 4. How the queue runs (mechanics)

Coordinator pattern: one persistent implementer subagent per lane (worktree under `.claude/worktrees/<lane>`, absolute paths), briefed with the full gate list; read-only reviewer agents for /simplify; the coordinator posts dispositions and merges.

- **Disposition comments** on each PR: `## Review round N disposition (<sha9>)` listing CI state and each RoboRev finding with accepted/refuted and why. Implementers post `## Round N fixes (<sha9>)`.
- **Verify every push** before trusting a report: `git fetch origin +refs/pull/N/head:pr-N; git diff -M --stat $(git merge-base origin/main pr-N) pr-N` and `gh pr view N --json mergeable,headRefOid`.
- **Watchers**: `bash <coordinator-worktree>/.superpowers/sdd/<ledger-dir>/bin/watch-pr.sh N HEAD9 > watch/N-HEAD9.log` (the script is kept outside the repo beside the live ledger, so give bash its explicit path; not committed) (polls once a minute; prints CI failures, CI complete, the RoboRev verdict for exactly that head, and "HEAD MOVED"). Pair it with a one-shot background loop that waits for the process to exit and echoes the log. Never merge on the watcher's CLEAN line; open the body (`gh pr view N --json comments --jq '[.comments[] | select(.body | startswith("<!-- roborev-pr-comment -->"))] | last | .body'`). RoboRev edits one comment in place per PR.
- **Main CI**: a per-SHA notifier per merge (`gh run list --branch main --commit <sha>`), because a rolling notifier stalled when heads changed faster than they concluded. When main goes red, check the touched paths of the merge before attributing (`git diff --stat <merge>^..<merge>`), then rerun failed jobs (`gh run rerun <run> --failed`) and file an issue with the frames.
- **Refresh procedure** (implementers do this themselves now whenever their push leaves the PR CONFLICTING): `git merge --no-ff origin/main` (repo config refuses a plain merge on divergence), union resolutions on shared files (plan status table: theirs then ours; README subpath prose; qualify manifest), `git diff origin/main...HEAD --stat` must show only the row's files, then the gates that read merged files. Never wait on background gates.
- **Serial queue for shared-file PRs**: phase D rows share `appwire-client/typescript/index.ts`, `tsconfig.build.json`, `scripts/qualify-package.mjs`, `README.md` and the plan's status table. Merging one forces a refresh of the others (#1409 lesson). Today's practice: a green+clean PR merges when ready even out of order, since the queued rows refresh at their turn anyway; the order matters for who refreshes when.
- **Gate list for a phase D PR** (paste into briefs): `make test-api-package` plus the falsification (delete the row's `index.ts` re-export → the qualifier must fail with "shipped module unreachable from every published specifier: <mod>", restore via `git diff | git apply -R`); `make test-web`; `make test-native` (Metro bundle); `make test-web-browser` (six guards, skillguard sometimes needs a second run); `make lint-package-imports`; `npx biome ci ../../../appwire-client/typescript` run from `cmd/evener-hub/frontend` (never `npx biome` from the repo root); `npm run lint` in the frontend; `EVENER_GITLEAKS_REQUIRED=1 make secret-scan` (it scans the worktree it is run from since #1564; the separate `gitleaks detect --no-git` on the changed paths that #1513 called for is retired, and was reporting allowlisted fixtures as leaks because a bare invocation over a subdirectory loads no ruleset at all); root `go test -short -count=1 .`. Go PRs: toolchain gofmt (`$(go env GOROOT)/bin/gofmt`, never PATH gofmt), `go vet ./...`, `go vet -tags evenerfuzz ./...`, `GOOS=windows go vet -tags evenerfuzz ./...` per module, `golangci-lint run`, `make lint`, race suites solo.
- **Phase D template**: framework-free store factory over `createFrameworkFreeStore(init)` (`appwire-client/typescript/frameworkFreeStore.ts`) → `{getState, getInitialState, setState, subscribe}`; per-module client port `Pick<AppwireClient, "request" | "onNotification">`; web adapter keeps its path and hook; native twin deleted or made a thin projection; isolation test with two instances; old tests are oracles; every post-await write fenced (one reply-fence predicate for every await, one payload-retire helper for every reset site: D5 spent six rounds learning this, D6 built it in first); qualify smoke probes synchronous-shaped (`assert.rejects(...).catch(exit 1)`); subpath plumbing = package.json exports + qualify manifest + README + resolver aliases (vite subpath entries before root).
- **Memory**: the coordinator's durable facts live in the ledger-memory store at `/Users/jesse/.claude/projects/-Users-jesse-git-prime-radiant-inc-evener/memory/` (write only through the wrapper described in `MEMORY.md` there). Worth reading: `mobile-landing-queue-status`, `sdk-d23-split-and-fencing-ruling`, `appwire-read-response-is-the-snapshot-cut`, `agent-race-job-runs-at-its-time-cap`, `go-telemetry-sidecar-races-tempdir-cleanup`, `roborev-clean-needs-body-read`, `merge-pin-needs-full-sha`, `flake-attribution-check-touched-paths`, `review-drift-close-the-class`.

## 5. Per-lane state and next steps

### D23 lineage (native conversation store onto the reducer) — implementer `acca34d508823b2d5`, worktree `sdk-d21-native-model`, branch `claude/sdk-d23c2a-model-live`, clean and pushed

Landed: D21 (#1488), D22 (#1515), D23a (#1519), D23b (#1522), D23c-1 (#1540), follow-ups #1510, #1518, #1546. Open: D23c-2a #1547.

Key design fact (memory `appwire-read-response-is-the-snapshot-cut`): a `thread/read` response is ordered at the snapshot cut (`docs/superpowers/plans/2026-07-28-appwire-retry-safe-mutations-and-atomic-rejoin.md:19, 372-373`). Frames received before the response are already in the snapshot; frames after apply on top. Native's per-field owner revisions and D23b's first buffer-and-replay both modelled an impossible ordering; D23b round 3 adopted the literal cut rule (reread snapshot authoritative, no replay), D23c-1 applied it to the initial read.

**D23c-2b brief** (base: #1547's merge): rows become a projection of the model. After each accepted frame, `set({ conversation: capAndTruncate(projectConversation(applied)) })`; `projectTimeline(model.turns)` exists since D22. Q10 caps (`capItems` 500 rows, `truncateItem` 64 KiB + marker) become a pure post-projection pass; the truncation freeze becomes unnecessary. Delete from `mobile/src/state/conversation.ts`: `projectSingleItem`, `decorateLifecycleItem`, `containingTurnStatus`, the cluster splicing (`findActivityTarget*`, `activityClusterSegments`, `mergeLiveActivityMembers`, `projectActivityMembers`, `replaceActivityTargetDetail`), all seven row appliers and `publishModel`, `liveOwnedRevs`/`markLiveOwned`/`liveRevisionForItem`/`isLiveOwned`, `truncatedItemIds` + `truncateAndRecordSingle`/`reconcileTruncationFrom`, the rehydrate rows merge (`rereadIds`…`committedItems`), `getTruncatedItemIds` (interface + impl + 3 Plan3 tests), `attachToSources`/`itemsAbsentFromSnapshot` if unused; keep `pageOwnedIds` and `projectOlderTurns` for D23d. From `project.ts`: `isActiveItem`/`activityState` exports, `projectItemAttachments` + `inlineImageSrc`/`inputImage`/`outputImage`, the `clusterActivities` export (keep private for `projectTimeline`). Invert the 14 item-level "live wins over stale snapshot" scenarios (describes "Residual 2: live-owned item IDs" ×2, "Fix round 1: per-item live ownership with monotonic revision" ×10, "Task 2A live-matrix" ×2), same fixtures, snapshot authoritative; page-owned prepend half stays until D23d. Mechanism→behaviour mapping table in the PR body, one row per rewritten test, none dropped: lifecycle/cluster/F7 (~50 tests) assert rows; truncation (~44) keeps every visible outcome and drops only ownership-set assertions. Decision-2 deltas to list: `agentMessage/reset` removes the item; missing-target deltas ignored; `toolOutput/delta` without the callId guard; `warning` without an active turn dropped (visibly now); ask_user/unknown/userMessage resolved locally; per-`summaryIndex` reasoning chunks; steering rows live; sparse assistant completion keeps streamed text (fixes today's row blanking); model text under deltas unbounded (cite #1535). Size: ~1,100 code lines deleted, ~150 added; ~7,000 test lines / ~140 tests touched. Gates: `make test-native`, `lint-package-imports`, `secret-scan`; `test-api-package` only if the package changes (a reducer change is a stop-and-ask).

**D23d brief** (base: c-2b's merge): `loadOlder` merges the page into the model with `mergeOlderItemPage(model, response)` and rows re-project; `olderCursor` from the model. Delete `projectOlderTurns` and its fake `Thread`, `pageOwnedIds`, `loadOlderToken`'s row-merge half, the row-level page dedupe; keep `hasEarlierItems`/`hasLaterItems` and decide the I3 "filter question rows from pages" rule by measuring whether `liveAskQuestions` already excludes settled asks in older turns. Tests to remap: "loadOlder" (2), "I7 retained cap and ordering" (2), F10 paging (4), "R2: page-race preserves authoritative outcome" (6), "I3: page-owned item tracking" (2), "within-page paging dedupe" (4), "Task 2A-Cluster: paging dedupe" (2); the sha-route test (#1518) and the call/result page test (#1519) stay as oracles. ~400 code lines, ~1,500 test lines.

Related issues: #1514 (hub past-session read stamps no InstanceID/AskPending; the service's `instanceId ?? thread.id` fallback goes when the hub stamps it), #1535 (bounded model text), #1523 (generic `applyNotification` typing removes native's cast).

### Credentials lineage — implementer `a33d563fb787dbe91`

Landed: D8 1a (#1483 lineage), D8 1b (#1508), D9 (#1524), #1516 → #1532, follow-ups #1521, #1528, #1544. Then D6 (#1549, open, complete, needs /simplify + review + refresh at its turn). Worktrees `sdk-d8-*`, `sdk-d9-*`, `sdk-1516-credentials-core`, `sdk-1532-low`, `sdk-d6-transcript-display`, all clean and pushed.

Core facts: `createCredentialInstancesStore({ ownClientId })` owns the mutations, sign-in RPCs, `authStatus`, the auth/updated echo with own-echo markers, `foreignListingChange`, `listingEstablished` (survives a client change; `listingFromPreviousConnection` says whose rows they are); refused writes rely on the hub echo (the hub broadcasts on every applied write, except the two rollback-failure paths in #1543). Native `ProvidersScreen` owns one store per hub via `useCredentialStore()` (unbound at construction, bound in a layout effect so it precedes the child list's passive `start()`).

Open issues from this lineage: #1517 (instance echo lacks originClientId), #1525 (core publishes connection state; flow drops `setConnection`), #1543 (hub rollback paths must broadcast), #1513 (secret-scan from worktrees), #1526 (no native render/hook harness), #1481 (delete the `ProviderInstances` adapter; screen reads the core), #1506/#1507.

### Keybindings — implementer `a5f6e94d41ccbc692` (now unassigned), worktree `sdk-d5-keybindings-store`, branch `claude/sdk-d5-followups`, clean and pushed

D5 (#1489) merged after 8 rounds; follow-up #1548 merged; its Low (whitespace-only ids/chords, #1550) is open for a two-line fix. Lessons that shaped D6: one reply-fence predicate for every await; one payload-retire helper for every reset site; the post-await path of a write is one ordered sequence (fence → decode → apply-with-settle → storage); the draft repository normalizes both sides of a byte comparison; test fakes use non-integer ids.

### Navigation — implementer `a50332dbd90ee53de` (unassigned), worktree `sdk-d13-navigation-revalidator`, clean and pushed

D13 merged (#1492). D14 next (High: `store.ts` imports zustand and `railExpansion.loadExpansion()` reads localStorage at module init; see the worksheet). Residuals in #1491.

### Extensions — D10 landed (#1476); D11 #1502 open (implementer `ad8fe23dd0ce4b2a6`, worktree `sdk-d11-plugins`); D12 after D11; #1503 (share the notification-refetch lifecycle across marketplaces/plugins, D12 would be the third copy).

### Activity — D17'' #1498 open (implementer `af854ec309919445d`, worktree `sdk-d17-activity-panel`); D18 needs Jesse (convergence with `activityList`).

### Piece A (transcript Seq) — implementer `af382e36b35abaf8a`, worktree `transcript-a2-seq`, branch `claude/transcript-a2-seq`, clean and pushed

A1 landed earlier (Append/AppendDurable/AppendSynced return `(seq, error)`, fail-closed). A2 #1480 at round 10, held for Jesse. Design: `schema.Turn.Seq` (`json:"-"`), `FoldRecord{Layers, RetainedSeqs}` written last in a fold's commit, `recordedSeq(seq, err)` the sole stamp, sentinels `NoTranscriptEntrySeq`/`seqHeldPreAttach`/`seqFoldInjectedSteering`, `TurnKind.IsPrivateRecord()` used by every projection, `resumeAnchor` one backward pass (a record older than the newest marker is stale → legacy anchor), `DecodeEntry` seeds `Turn.Seq`, binary search over monotonic `Entry.Seq`, unresolved retained seqs warned (fail-open). Follow-ups filed: #1529 (write-failed sentinel vs `mergeBackCount`), #1530 (door stamps `Turn.Seq`). Pieces B (durable turn ownership) and C (apptranscript index grouping) of the #1100 split are not started; brief them off A2's merge.

### Flake lineage (all owned today, all root-caused)

- #1394 agent race job "hang" = the suite running at the 10m cap; fixed by #1545 (fold perf). #1539 (test-side driver leaks) open with a timeout to triage. #1538 (product: close whose stop-join budget expires never wakes the driver) filed.
- #1527 skill-catalog TempDir race = the go command's telemetry sidecar writing after `go env` exits; fixed test-side by #1534 + #1541 (audit refuses variable configs); #1531 root-module twin open.
- #1533 hubcore roster deadlock = test fake shared one release channel; fixed by #1536.
- Earlier today: #1505 (thread/shutdown reply race, real daemon bug), #1504 (skillguard diagnostics, #1434 still open awaiting a trace), #1511 (sshconn gate ordering), #1496 (mcpprobe shared transport), #1473, #1495, #1419.

## 6. Decisions waiting on Jesse (ask one at a time, in this order)

1. **A2 compatibility ruling** (#1480): pre-A2 readers fail the whole transcript cleanly on the new field. Recommend accepting the break. Unblocks the merge and pieces B/C.
2. **D24's visible product change**: native gains config-driven content levels it does not have today.
3. **D17'/D18 convergence**: permission to rewrite native's activity summary onto the web's rules (or keep native's stricter fencing per the plan row).
4. **Native render/hook test harness** (#1526): two Lows today could not be pinned without one.
5. **D6's two stated decisions** (#1549 body): (a) a lost reply now shows the `writeUncertain` fact instead of the "could not be confirmed" string; `TranscriptPreferencesEditor.tsx` may need to render `writeUncertain` like the keybindings screen does; (b) `retirePayload` keeps the direct write's preview draft (web oracle pins it).
6. The smaller queue: D7's two rulings, D19' quiet-age rule moved shared, phase D Q4 to Q10 in the worksheet, #1431 (delete ~23 zero-consumer aliases), race-job sharding as a fallback (likely moot after #1545).

Decisions I made as migration owner that Jesse may veto: the D23 fencing test-shape change (~90 mechanism tests rewritten as behaviour tests over the same scenarios); adopting the reducer's rule wherever native's applier disagreed (listed per PR under "Behaviour deltas (decision 2)"); the literal cut rule in D23b/c-1; dropping the refused-write refetch in favour of the hub echo (#1532); `listingEstablished` surviving a client change.

## 7. Housekeeping the next owner should know

- The ledger directory `.superpowers/` is gitignored; the copies beside this memo are the only durable ones. `progress-ledger.md` timestamps are authoritative (my memory saves early on overstated the clock by ~10h; corrected).
- The plan's status table rows say "open" even for merged rows (they were written at PR-open time). A docs pass marking them merged with the squash SHA would help the next reader; low priority.
- Watchers: all of mine were stopped at handoff (the #1480 and #1547 ones had concluded; #1549's was killed). Re-arm with `bash <coordinator-worktree>/.superpowers/sdd/<ledger-dir>/bin/watch-pr.sh N HEAD9` (the out-of-repo copy, invoked by explicit path) for any head you care about; it exits on its own after 120 polls or when CI and RoboRev conclude.
- Open issues filed by this queue that are not yet claimed: #1550 (whitespace-only ids/chords, two-line fix), #1551 (root-module test hang, one occurrence), #1543, #1538, #1535, #1531, #1530, #1529, #1526, #1525, #1523, #1520, #1517, #1514, #1513, #1512, #1507, #1506, #1503, #1497, #1491, #1490, #1481, #1479, #1477, #1470, #1467, #1465, #1464, #1460, #1455, #1446, #1445, #1444, #1442, #1438, #1437, #1436, #1435, #1434, #1433, #1432, #1431, #1430, #1429, #1427, #1425, #1423, #1421, #1406, #1402, #1394 (close after a few more green main runs), #1386, #1383, #1382, #1378. Each carries its context in the body.
- Worktrees with uncommitted state that are not mine to clean: `evener-dev-bounded-list` (two modified files from the closed #1145/#1265 lineage) and `mobile-app-integration-6d4885` (46 untracked `.private-journal` files from an earlier session). Everything else under `.claude/worktrees` is clean and pushed; merged-branch worktrees can be removed at will.
- Open PRs from earlier lanes that predate this queue and were not part of it: #1458, #1410, #1393, #1392, #1391, #1390, #1362, #1248, #1169, #1100, #1035, #617, #472, #380, #1542.
- CI facts: biome runs only from `cmd/evener-hub/frontend`; heavy race suites run solo; the `tests` job shards the agent package four ways; the race job does not shard; `race-root` covers the root module; local gates prove macOS only (three Go tests green locally failed on the ubuntu runner on 2026-09-13).
