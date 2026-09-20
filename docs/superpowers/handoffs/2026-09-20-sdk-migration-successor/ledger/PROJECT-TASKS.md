# AppWire SDK migration: completion checklist

Coordinator snapshot: 2026-09-19 PDT, after #1973 merged as 5dfd06d299.
This is the consolidated execution list. Historical handoff, completion-scope audit,
web-first audit and decomposition notes supply requirements, not current PR status.
PR status is refreshed as each lane is resumed; exact receipts identify verification heads. Local qualification
means implementation exists; it does not mean the work has landed.

## Priority and finish rules

Web extraction and adoption first; native second. Use Luna medium for clear bounded implementation slices; reserve xhigh for difficult debugging. Keep dependency order, review findings against actual contracts, and do not
reopen disproved findings without changed evidence. Merge Low-only parents once
qualified, then land focused Low follow-ups. Five product review rounds trigger
reassessment/decomposition, not indefinite patch growth.

A task is complete only when its required behavior is on main with current-head CI,
review dispositions, and post-merge verification. The full goal remains active.

## Completed foundation

- [x] Offline/settings generation predecessor replacement: #2009, #2014, #2024.
- [x] Shared settings generation #1841; post-merge CI passed.
- [x] #1841 teardown-order Low follow-up #2036 merged; post-merge CI35476059387 passed.
- [x] History foundations #1961, #1968, #1982; post-merge CI passed.
- [x] Marketplace server outcome #1940 merged; post-merge CI35475303830 passed.
- [x] Ask/carrier #1907 and earlier correction chain merged; interrupt follow-up remains below.
- [x] Existing web D18 usage, D25 mutation core, D28 connection extraction landed.

Thirty-six takeover merges are recorded in progress.md. This is not a percentage
of total scope and does not count local-only work as merged.

## 1. Finish the web settings and transcript package

- [x] P9 checkpointed-editor primitives: #1844 mergedb3694ccc8d989bfec7ffeb954f4d39bbc745490d.
      Pinned98cb71188, all16 CI checks and all3raw reviews PASS; post-merge CI35486345461 PASS.
- [x] P10a wire contracts: merged #2051 as cf85b5c62; post-merge CI35486108155 PASS.
      All16 current-head checks passed; raw Low disposition retained; comment-only #1984 fix merged #2053 asfded33afe; issue1984 CLOSED; post-merge CI35486790423 PASS.
- [ ] P10b read-generation store: parent2055 merged1fb7b32a31ea5bc4b93889170d90a1ac7b510269; generation correction2056 must land before API/adoption complete.
      Read-store parent2055 landed from frozen0d57f3b88 after five local fullbranch rounds and complete current-head CI/rawreview disposition. Atomic snapshot/reentrant confirmation corrections included; remaining2691 loading-on-directgeneration-replacement issue decomposed into tiny generation-settlement successor. Successor localfullweb/package and3remote rawreviewsPASS; immediate mainrestack/currentheadCI required; webadapterblocked until correctionlands.
- [ ] P10c PATCH/preview reconciliation: frozen c660aee9999e, independent review and RoboRev2677 PASS; land after P10b.
      Preserve current-value fenced replies, preview contradictions, generation retirement.
- [ ] Retire superseded #1845 only when all its retained P10 requirements are accounted for.

P9 and P10 can be prepared in parallel. P9 is required before checkpointed transcript
drafts below; it is not a semantic prerequisite for P10's wire/read/PATCH work.

## 2. Adopt the package throughout web (retained Stack A)

- [ ] A1: audit/publish the complete package exports, build and external-import surface.
      Reuse exports already shipped by predecessor slices; do not add duplicate work.
- [x] A2: browser-local persistence extracted. #2041 merged 4b684c8e after all15 CI checks and all3 raw reviewers passed; post-merge CI35482638440 passed.
- [x] A3: cross-tab synchronization extracted. #2049 merged4cca1d991 after all checks and three raw reviews PASS; post-merge CI35486054110 PASS.
- [x] A4: effective-setting transitions extracted in #2052, merged7b72ec4a6a402930eec0de189eb586ed1f37e311. Pinned4cc99f957, all16checks and3rawreviews PASS; post-merge CI35486854632 FAILED in skillguard continuation; investigation active.
- [ ] A5: wire the Zustand adapter while preserving StoreApi, nine mirrored fields,
      and the 14 existing consumer import contracts.
- [ ] A6: replace the web hub implementation with the shared store; verify all four
      settled behaviors: current-value fenced PATCH replies, support-flap reload,
      missed-notification refresh, and preview contradiction reconciliation.
- [ ] A7: remove unused callback/ready-generation seams once package ownership is real.
- [ ] A8: add package transcript checkpointed drafts with edit/save/discard/rebase,
      persistence errors, conflicts and unavailable/unreadable storage behavior.
- [ ] A9: adopt web draft/gate state, including saving, writeUncertain and settledWrite.
- [ ] Run retained web settings/session/transcript oracles and browser gates; verify
      remaining duplicated web logic has actually become host adapters.

A1-A9 remain required. The four A6 decisions are settled and need implementation
and tests, not another product question. Early independent slice audit is active.

## 3. Land remaining shared correctness and web consumer chains

- [x] Marketplace SDK #1954: merged5c407b152cac4fa6b6a39debf8647a114f4f7ddc after all16CI/3rawreviews PASS at623d1d301; post-merge CI35486967211 PASS.
- [x] Marketplace accepted-publication #1973 merged5dfd06d299 after all16checks and raw-review qualification at f947bba6b; Low successor prepared. Post-merge CI35487938222 SUCCESS verified at merged5dfd06d299.
- [ ] Web removal/retry consumer #1960: publisheda6e134f2125166b654fbabe669c09dc00bd8cc2f; 94tests/fullweb/Biome/RoboRev2706PASS; currentCI/rawreviewpending. Sharedhelperis2027extensiondependency.
- [ ] Marketplace store-error replacement for capped #1897: qualified S3 slice,
      restack after #1940 in progress; publish/land and close superseded parent.
- [ ] History public helper #2035: published, current CI/review pending.
- [ ] History alias-coverage correction a0879342: qualified local, land after public helper.
- [ ] History empty-turn metadata correction c512216: qualified local, land after alias.
- [ ] Promised interrupt durability #2005: CI passed at 755f6a1; two current Medium
      claims triaged: nondurable in-memory history divergence reproduced; isolated durable-boundary successor assigned. Preserve five-round cap; decompose
      a real remaining defect rather than silently add a sixth product round.

Known successor fixes are disclosed; they are not represented as already fixed on main.

## 4. Complete native adoption after web

- [ ] History consumer #1919: local cd15c466 integrates paging/rehydrate and passed
      scoped gates; independent review in progress, then restack/land after helper chain.
- [ ] Session usage #1920 after #1919: package totals, cumulative/per-turn separation.
- [ ] Native model projection #1737 -> #1738 -> #1740.
- [ ] D23d model-owned older-page/cursor ownership; remove remaining projectOlderTurns
      and pageOwnedIds ownership after the preceding projection work.
- [ ] D24 native shared projector adoption with content levels, warnings, attachments,
      grouping, usage, anchors and config presets; requires D23d.
- [ ] Durable runtime/foundations #1981 and local qualified successors.
- [ ] Recovery hook exact runtime-identity/render fence: current test-only checkpoint
      62a15b68 needs contract audit; do not equate passing effect tests with proof.
- [ ] Recovery UI: real Restore/Copy/Dismiss, durable draft save before outbox deletion,
      preserve errors and exact target/revision. Screen integration has NOT started.
- [ ] Actual send/steer/queue/interrupt dispatcher activation and host cleanup.
- [ ] Durable pending rows instead of only transient pending markers.
- [ ] Restart/reconnect recovery: known receipts settle once, unknown outcomes block,
      never-attempted records dispatch only when ready; storage and hub/ref isolation.
- [ ] Native reconnect #1952 -> #1955 -> #1922; requalify each current head.
- [ ] Native marketplace #1972 -> #1978 -> #1983 after shared/publication contracts.
- [ ] TUI marketplace #1966 -> #1976 after server contract.
- [ ] A10 native transcript preferences projection after shared transcript settings/drafts.

## 5. Focused Low fast-follows (actual PRs, not issue-only parking)

- [x] P8 ordering coverage: #2036 merged.
- [ ] #2006 active tool-result provenance-cost coverage. Assigned Luna xhigh task history_cost_low.
- [ ] #2026 canonical raw identity coverage. Assigned Luna xhigh task checkpoint_identity_low.
- [ ] #1941 remaining offline fixture/read duplication and observable nudge rejection;
      remeasure current code before changing it.
- [ ] #1948 raw-string draft backend conformance.
- [ ] #1946 kindless journal live/restore oracle and only a reachable correction.
- [ ] #2015 native salvage label/gap and #2016 honest TUI render test: local child
      1b114a47 prepared; publish after #2005's final parent settles.
- [ ] #2027 post-apply unavailable-list outcome: existing PR2050 at06841197c4 introduces marketplaceRemoveApplied; SDK consumption gap confirmed; bounded Luna medium implementation marketplace_applied_unavailable assigned. Server2050 mergedd55475199dbaa93a15ae4a720a7408f29e688126 afterall16checks/3rawPASS. SDKlocalb240b03ae passes38tests/types/Biome/package; restackonproducerassigned toresolve review2700 dependency. Hold2027completion untilSDK andneutralwebconsumerland. #1944 wire round-trip coverage;
      #1951 secondary read-failure logging, as bounded server follow-ups.
- [ ] #1953 marketplace row validation after SDK parent; #1959 late consumer outcomes.
- [x] #1973 publication-reset docs/web facade Low: PR2054 mergedfae2b2e6bc15b3228528ed25d00973d48658326e at04:16:30Z after all16 currentheadchecks and3rawreviewsPASS; postmerge CI35488731142 SUCCESS.
- [ ] #1942 retained-screen connection cleanup after reconnect chain.
- [ ] #2008 runtime start retry after setup failure; #2020 recovery action eligibility.
- [ ] Reconcile additional historical Low dispositions (including P10a #1984) against
      exact current code: fix in focused PRs or explicitly withdraw with evidence.

## 6. Final completion audit

- [ ] Reconcile every retained handoff row and ruling against merged implementation.
- [ ] Verify web has adopted shared behavior rather than merely exporting unused APIs.
- [ ] Verify native paging, projector, dispatcher, durable rows and recovery end to end.
- [ ] Verify current-head and post-merge gates; inspect raw review bodies, not only badges.
- [ ] Close superseded implementation/reference PRs when their obligations are satisfied.
- [ ] Account for every retained Low as landed or explicitly disproved/withdrawn.
- [ ] Mark goal complete only after all retained requirements are verified on main.

Out of scope: parked #1480 transcript fold, unrelated cleanup such as #1947,
TestFlight and physical-device acceptance. #1934 is handoff reference; #1580 is
projection oracle/reference. Withdrawn native twins remain withdrawn.

## Continuation receipt 2026-09-20 03:58 UTC

- [x] #1973 merged at 5dfd06d299a60f3919e17b4d546589c5b2c92860 after all 16 current-head checks (including snapshot) and raw review qualification. Only Low reset issue retained in focused successor.
- [x] #1973 post-merge CI35487938222 SUCCESS at exact merge5dfd06d299.
- [ ] #2052 post-merge CI35486854632 failed in TestSkillComposerBrowser continuation PROSE_STEER_14e. Root-cause worker skillguard_continuation_failure active; no rerun-only dismissal.
- [ ] Interrupt successor review found attentionMu release before state publication race. interrupt_findings_triage assigned minimal correction and regression. Parent2005 remains unchanged.
- [ ] Restack and publish #1973 Low successor; restack web1960 independently. Native assignments remain deferred.

- P10b publication underway: parent PR2055 exact0d57f3b88; generation-settlement successor e2b62bb85 (3 production lines plus regression), focused54 tests/full web/package gates and RoboRev2692 PASS. Both await current PR qualification; known parent Medium disclosed.

- Transcript successor PR2056 is published at e2b62bb85 against parent branch0d57. CI workflow only runs PRs targeting main; child full current-head CI must run after parent landing/restack/retarget. Local full web/package gates and RoboRev2692 PASS already verified; this is not a claim of child PR CI. Both PRs attached.

- #2054 all3 raw reviewers PASS atb384e8c04; CI still active. #1973 postmerge live watch91587 (run35487938222); #2054 live watch29251 (run35488051335).

- #2056 exacte2b62: all3 remote rawreviews PASS. #2055 rawreviews pending finalLuna; MusePASS, DeepSeekLow missing named public type exports. Verified TranscriptDisplayChange/StoreFields/StoreActions absent index.ts; tiny separate export followup assigned, no parent amendment.
- #2052 failure evidence: distinct continuation m6 completed onserver; intervening thread/read names activeTurnIdm6 but historyonlythroughm5, m6 absentDOM. Worker tracing snapshot overwrite; retain deterministic reconciliation regression and browser validation requirement.

- #2055 public type Low local93cac301 on2056 e2b62; packagebuild/installed-consumerqualification/Biome/RoboRev2695 PASS, root reviewed. At finalrestack consolidate three names into existingexportblock; publish only after parent pair lands.

- Marketplace dependency correction:1960 ownedSDK marketplaceRemovalOutcome helper(two kinds) is requiredbefore2027extension. NewSDKworker b240 duplicateshelperoffmain; instructedfreeze andtransferonlyremovedkind/newdiscriminator/tests after1960final. Rootcaughtrestackdroppingexisting marketplacesLoading:false; workerrestorealongwithall1954cache-retirement semantics.
- P10c local integration preparation assigned transcript_patch_integration onqualified2056source; no publicationbefore2056lands, finalrestack/reviewrequired. This overlaps preparation only, notlanding dependencies.

- #2056 restacked/publishedmainbase d55475199 ->1bf707e70ba5b3bb45e871c931dff4abd8cc3877. Focused54/build/package/BiomePASS andexactbaseRoboRev2703PASS. Currentmain-targetCIstarted.
- Interrupt successor #2057 published5167475c68decac98a5410e47c234ea2e8129270 based2005; productionidenticalindependentlyreviewed980914, onlyfor-range lintchange thereafter. Rawremotequalificationpending; exactparentlocal2698PASS.

- Postmerge #2055 CI35488763856 SUCCESS; #2050 CI35488815957 SUCCESS; #2054 CI35488731142 SUCCESS.
- #2057 rawpanel: Luna/MusePASS, DeepSeekMediumfailedwritepair mayreappearviafold + Lowmissingmalformedaskwarning. Reproductionassigned; donotmerge2005/2057before verifieddisposition.
- #1960 loadingflagremoval concernwithdrawn: publishMarketplaceSnapshot itselfsetsfalse. No behaviorregression; currentheadcache-retirement preserved. #1959wholeissueremainsopenwithnativeandwebpagelifetimecoverageobligations.
