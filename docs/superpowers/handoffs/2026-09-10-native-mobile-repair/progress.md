# SDD ledger — plan: docs/design/mobile/2026-09-09-iphone-main-checkpoint.md

Jesse authorized starting small reviewable PRs and landing them on main on 9 September. This authorizes publishing candidate branches/PRs and merging once required review and checks pass. Do not bypass protection or self-approve.

The authoritative native worktree remains on its existing branch, preserving the exact dirty Apple files and all historical work. Additional sibling landing worktrees are isolated candidates prepared from that source, never the default checkout. Git is used because no available native tool creates a worktree without also creating a user-owned task.

## First batch and dependency review

| Task | Produces | Dependency / overlap | Decision |
| --- | --- | --- | --- |
| Empty navigation updates | Canonical empty entities and behavioral delta regression | Existing navigation core only; independent of auth and native source | Two-file PR on current main |
| Device authorization binding | Mismatched-provider rejection with retained valid flow | Uses app_auth.go at DevicePoll; logout edits a separate method | Separate test file and PR |
| Accurate logout result | Actual stored removal and unchanged environment source | Uses app_auth.go at Logout; no DevicePoll dependency | Separate test file and PR |
| Each candidate | Small committed diff and deterministic proof | CI and one GitHub approval required; main may advance | Refresh/verify each exact head before protected merge |

No core TypeScript state extraction or legacy-app removal is included. Future batches cover roster performance, lifecycle/transcript fixes, packaged SDK, shared mobile modules, and native app/CI/TestFlight. Preserve implementation dependencies; do not label this first prerequisite batch the completed mobile checkpoint.

Navigation audit: the original behavioral regression passes on main because navigationSnapshotsEqual already treats nil and empty slices as equivalent. The proposed normalization is redundant, and an added non-nil assertion only pins representation. Park the candidate and its working files without a PR. Replace it in the first batch with the actual thread/list null-to-empty-array response correction; preserve all audit evidence.

First published candidates: #1066 device auth at df7c8ba67; #1067 logout at f0cb0297d; #1068 empty list at 1c18e6866. All focused tests and package vet pass with independent Luna review. Full package on logout has three existing macOS updater path failures; extracted update-entrypoint candidate is in progress with a portable alias-parent regression. Root queried branch protection: seven required checks with strict up-to-date base, one approving review, auto-merge disabled. No admin bypass or self-approval. Async question asks Jesse which reviewer to request for the concrete published PRs while work continues.

Update-entrypoint published as #1069 at 3705a9098. Full package passed in candidate (75.614 s), root focused -race and vet pass, independent review clean. First CI snapshot: #1066 web failed; other completed checks green and remaining race/artifact checks pending. #1067 web passed on identical frontend source. Root inspecting exact failed web job before any rerun. #1066 and #1068 RoboRev comments report no issues. SDK sequence audit includes an erroneous direct-main subtraction and claims a generated-types addition even though types.gen.ts has no branch delta against the actual common base; do not propagate those claims. Verify each later SDK candidate against both exact source and current main.

#1066 web failure diagnosed from completed job 102575030428 logs: Chrome failed to meet the existing 30-second startup deadline; no layout test case ran. The remaining four browser guards passed and the same frontend source passed web on #1067. Requested one rerun of only that failed job at unchanged df7c8ba67; no timeout changes, test skips or unrelated code edits. Retained log: /private/tmp/evener-main-rebase-3ify96zd/pr1066-web.log.

The first rerun request returned "job 102575030428 cannot be rerun"; no retry was started. The parent run is still active. Root is waiting with gh run watch (30-second interval) before retrying the same failed job. Do not claim the retry has run or passed. Reviewer question is still pending; no review request sent and no admin merge attempted.

The targeted #1066 rerun was accepted after original workflow completion: attempt 2, unchanged head df7c8ba67466b0fa5ef3f89d4b25eb5b761093ff, new web job 102580179729. Watch log /private/tmp/evener-main-rebase-3ify96zd/pr1066-retry-watch.log. Actual RoboRev comment #1067 contains a medium concurrency finding despite SUCCESS status: credentialWrite is an RLock, and Get then Clear can interleave with a writer. Luna implementer investigating smallest correction with deterministic regression. #1069 exact-head comment reports no issues. No merge or reviewer request yet.

#1066 web attempt2 passed; dependent Build snapshot artifacts running. #1068 and #1069 all CI checks pass and actual RoboRev comments are clean; GitHub approval still required. Bounded startup audit (pr1066-web-startup-audit.md) locates original failure between DevTools announcement and HTTP readiness; missing readiness HTTP error/status and runner resource diagnostics prevent a root-cause conclusion. Passing retry is not a durable fix claim. No timeout changes or product/layout patch justified by current evidence. SDK report corrected to actual common-base comparison; generated types and Go AppWire have no original branch delta.

#1067 correction published at 5eaebfe6e9522a2d8cd9715ddc85c26df7aedd9c (3 files,138 additions,4 deletions total PR). Logout uses existing controller lock exclusively across check/remove; other writers unchanged. Real ApiKeySet/Logout regression RED with old shared lock and GREEN with correction. Root all TestAuth_ -race pass9.680s in candidate and10.012s in authoritative source; focused store race/vet pass, independent final Luna review clean. Root carried production correction and new test back into authoritative branch, leaving protected Apple edits untouched. #1066 targeted retry and artifact now pass; #1066/#1068/#1069 fully green at published heads. #1067 new head needs fresh CI/RoboRev. All four still require GitHub approval; no merges, reviewer requests or protection bypasses.

## Second landing batch — authorized after first four merged

GitHub verifies #1066/#1067/#1068/#1069 MERGED (aa7206376,5dfe2febd,0b1197881,7b24b1a75). Pin fresh main50565eb465981d4b5a8fbbc939ad0bfb245aec68, preserve source394203d56. Jesse requests a much larger batch; continue publication after local validation and independent review, then exact-head CI/RoboRev. No fresh routine approval needed.

| Task | Produces / consumes | Overlap / ruling |
| --- | --- | --- |
| Initialize runtime boundary | Strict JSON decoder and handshake observer / existing current-main client | client.ts + tests only; retain main transport recovery; build independently |
| Question answer formatting | Portable pure formatter / existing web question components | Protocol helper and existing consumer imports move together; independent of client decoder |
| Roster loading | Bounded source fanout and metadata enrichment / existing registry APIs | app_threadlist files; already merged empty-array behavior retained |
| Transcript goal grouping | Goal continuation projected into its own logical turn / existing typed goal data | apptranscript only; index and projection semantics must ship together |
| Live projector lifecycle | Complete reasoning plus environment identity / existing event schema | Shared projector file; split disjoint changes only if independently buildable; review application order |
| Server handshake identity | Versioned daemon initialize / buildinfo | server source and focused test, no generated protocol delta |
| Cold/live relay ownership | Loading/subscription startup / current stable-ref and lifecycle APIs | Larger intertwined changes need dependency audit before extraction; no wholesale source replacement |
| Portable activity helpers | Framework-independent projections / web imports | Overlaps future package exports, not question/client slices; consumers move in same PR |

All tasks select already implemented source behavior against common base48dcab480, then adapt to new main. Source-only comparison must never remove newer-main fields. Obsolete regressions that pass main are omitted, not forced into representation changes. No higher-level state extraction, native simulator/build changes or prototype deletion in this wave. Candidate sibling worktrees retain source and Apple edits.

Task goal grouping: Ruling: typed GoalContinuation metadata does not yet exist on main, so the transcript reader is not independently buildable. Include agent/schema/turn.go metadata, only acceptContinuationInput persistence hunk, and its behavioral test with apptranscript grouping/index changes in one coherent PR. Exclude unrelated CloseForShutdown lifecycle changes. Initial compile failure is dependency evidence, not regression RED. After adding the declaration, three real regressions fail: saved grouping, incremental grouping, persisted live notice identity.

Second batch: seven PRs1071–1077 published at ledger heads. All local focused gates and independent reviews pass. Question canonical web gate was interrupted prematurely by worker; root reran to terminal0 with all3PASS, log questions-web-gate.log. Goal reviewer retracted previous-record flag finding: TurnSteering is a continuation so group remains open; no change needed. Task roster: Ruling: source fastpath drops cheap metadata and timeout contract was overstated. Preserve existing candidate and split safer metadata-sharing PR into mobile-land-roster-metadata; SDKworker implementing. CI1073 fuzz failure FuzzHubReplayLiveVsReload/seed0: harness blindly replaces reasoning delta text with status-only completion. Existing source replay_fuzz_test.go correction was omitted; worker preparing dependency fix without weakening equality. Log pr1073-fuzz.log from job102608438554. #1076 must land after#1071 for directdaemon identity.


Latest checkpoint: twelve further PRs are published. Nine are verified merged: #1071, #1072, #1074, #1075, #1077, #1078, #1079, #1081 and #1082. The three remaining PRs are #1073 reasoning at e7710cdcb, #1076 handshake at 79ccc0471 and #1083 browser ports at 2b52d0c5b. Reasoning and handshake were refreshed on main e80594562. The tracked landing document contains each validation boundary and the ledger records current heads.

Reasoning now defers projector reservation to the ordered stable carrier while exposing a separate pending identity to controls. Processing clear releases this hint even when a failed claim never emits a carrier. Behavioral regressions fail against published code for ownership and against the prior attempted reservation fix for no-carrier cleanup. Full server and projector race suites, replay seeds, vet, lint and independent Luna review pass on the refreshed branch. No full Session event-consumer E2E claim is made.

The handshake retains only two SDK files after prerequisite merges. All 87 client/reconnect tests pass after refresh; the SDK source is byte-identical to the revision with canonical web-gate evidence and clean RoboRev review. The browser guard now lets its real Vite process bind port zero and report the bound address. All 61 Node tests and all five canonical browser guards pass, including two real concurrent listeners, HTTP reads and awaited cleanup. The browser bind-error listener review correction is published; all 61 Node tests and all five browser guards pass again. Handshake CI and its actual RoboRev comment are clean at the current head. The reasoning and browser revisions have fresh CI/review pending. All three open PRs require approving review.

The runtime source remains 394203d56. Both protected Apple file hashes remain unchanged. Reconcile reviewed changes after the batch lands, qualify the existing SDK package in a clean installed consumer, then land the native checkpoint and produce a fresh artifact. Higher-level state extraction has not started.

Status follow-up: Jesse confirmed deletion of the legacy Tauri directory was accidental. Restored all 76 tracked files and recovered the two exact unfinished Apple edits from retained stash 67a86da; hashes match the prior checkpoint and only those two files remain unstaged. Reasoning head 79085cb3e fixes old SessionEnd status effects and obsolete notifier frames while retaining old lifecycle completion. Both guard/filter regressions fail without their fixes; final full server/projector race, vet and lint pass. Browser files refreshed byte-identically on main at 73aaa0255; a later Vite failure-diagnostic review finding is being corrected.

Browser diagnostic follow-up published at 287c74ce1. Root corrected cleanup ordering so arbitrary/frozen abort reasons are wrapped only after cleanup, preserving the original cause. Three early-exit/cancellation diagnostic regressions fail against the prior implementation; all 64 Node tests and all five canonical browser guards pass. Reasoning remains at 79085cb3e; current GitHub review/checks, not older green statuses, govern readiness.

## Unattended checkpoint continuation authorized by Jesse

Jesse asked to push all the way through while he sleeps. Continue the existing SDK/native checkpoint and fresh-artifact sequence; no routine approval pauses. Preserve restored Apple edits. Ruling: parallel implementation uses separate sibling worktrees and disjoint file ownership, as Jesse explicitly requested; root serializes dependency integration and publication. Ruling: normal protected merges only; no admin bypass or self-approval. A blocked merge does not stop independent candidate preparation.

| Task | Produces | Consumes / overlap ruling |
| --- | --- | --- |
| Portable activity/job helpers | Pure protocol modules and web consumer import moves | Own helper files; package export metadata excluded |
| SDK package substrate | Public exports, package metadata and real installed-consumer gate | Depends on strict client1076 and portable helper commits; no duplicate helper implementations |
| Native checkpoint | Native tree, eight shared runtime modules and necessary test/build closure | Depends on portable protocol paths; no protocol worker file edits or shared infrastructure mutations |
| Root integration/release | Exact-head review, protected merge attempts, reconciliation, artifact and TestFlight evidence | Serializes shared refs/builds/devices; physical/device blockers remain distinct from upload |

The checkpoint plan and current source define scope. These are extraction/integration tasks for existing implemented behavior, not creative feature expansion.

Jesse explicitly authorized skipping separate human approval when current-head CI and actual RoboRev review are clean. This supersedes the earlier no-admin-merge ruling only for that conditional approval bypass; do not bypass failed checks or unresolved findings. #1076 merged at1a5a1e4 after all named CI checks and clean review79ccc0471. #1083 refreshed byte-identically onto newmain; fresh checks/review pending. #1073 adds the requested buffered oldSessionEnd-after-processing-return case; current code retains old/new ownership and settles idle correctly. SetProcessing(false) occurs after Process* returns, including failed claims with no carrier. No production behavior change made for this concern; comment/test document actual lifecycle. Full server race passed13.959s; projector race1.355s and vet passed. One combined command initially named a nonexistent projector directory; corrected actual internal/appprojector gate passed. New head32d3c517f awaiting independent review and CI.

Ruling: preserve existing live round timing diagnostics using a dedicated append-only presentational transcript record, as documented in docs/design/mobile/2026-09-10-round-timing-replay-plan.md. This follows the existing diagnostic-record architecture and repairs demonstrated live/cold identity drift; it does not change model inputs or the frontend state architecture. Jesse authorized finishing the checkpoint and skipping separate approval only when current-head CI and actual RoboRev are clean. Treat those as mandatory merge conditions. Cost if wrong: a separate reversible transcript-kind extension to revise before delivery.

### 10 September 18:26 UTC — PR queue fanout

Jesse clarified workers should cover all PRs. Luna workers audited existing activity/native/environment heads and newest fork/SDK/shutdown batch. Root verified and pushed shutdown comment fix7a7c65aba and SDK current-contract fixture/documentation fixesf258dbd1e after installed qualification. New shutdown7a review raised concurrent clear/close duplicate boundary; Mendel assigned a real regression and minimal repair.

#1039 and #1102 were externally merged into the native branch, advancing #1096 to1fc2d2c4; this is not a main landing. Native clean worktree safely fast-forwarded; independent delta audit found no executable Tauri content. Source f6614d6cc, Apple edited-file hashes and preservation stash remain unchanged.

#1100 local normal self-compaction replay now passes full six-item identity parity (live/full/one-item pages), and root found/fixed incremental index GroupOpen/StartsGroup parity plus zero-payload Raw. Full apptranscript20.027s/appprojector0.365s/server15.355s pass. Context-manager/export/schema full package check passes. Async external Compact A-to-B owner race remains unproven/unfixed; regression assigned, not safe to publish completion yet. New persisted-event writer failure regression also assigned.

#1106 exact370045f all CI and actual Robo clean. Canonical local gate underway, no merge yet. #1105 exactf17 local canonical gate passed; actual review still pending. Activity/environment Robo logs continue active writing as of18:19; no review daemon mutation.

## 2026-09-10 parallel queue update

Jesse clarified that workers must span all PRs. Three Luna medium workers own native gate repair, fork-capability findings, and shutdown/clear findings; root owns timing/compaction and integration. PR1104 and1106 are merged. Native1096 current1fc review is clean, but its final local naming gate scans ignored dependencies; repair is underway. Activity1091 and environment1098 current-head reviews remain running. SDK1108 current32ecd is pushed after package, web and browser gates. Fork1105 and shutdown1109 have new local fixes under independent validation.

1100: explicit compaction event ownership fixes the confirmed idle-completion replay identity mismatch. An expanded regression exposed duplicate timing records from recovery-tail rewrites; context-recovery copies now preserve resume history while being excluded from visible full/bounded replay and ATIF accounting. Incremental indexed reads after every append now pass. A real PreCompact plugin fixture also exposed dropped late steering; owned steering is being routed through typed item lifecycle. These local changes are not yet published or fully qualified.

Merged counts include1039 and1102 merged into the native branch; they are not yet on main. Original Tauri source and unfinished Apple edits remain preserved and excluded from landing.
