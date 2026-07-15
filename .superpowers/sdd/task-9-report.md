# Task 9 Report: Enforce the Clean Break in Local Readers

## RED evidence

Added deterministic legacy/new local fixtures before production changes. The required focused command initially failed:

- `TestResolveTranscript_CleanBreakSkipsLegacyLocalState`: legacy local lookup unexpectedly resolved.
- `TestEnumerateBuckets_CleanBreakSkipsLegacyProjectBucket`: legacy and new buckets were both enumerated.

This demonstrated the pre-change permissive token/bucket behavior.

## Parent-regression follow-up

The parent full-suite run exposed stale local fixtures beyond the original focused tests. These were real test-data regressions caused by the intended strict filtering, not production validation failures. The affected agent lookup/read/parent-bucket fixtures and hubcore PastIndex/observer fixtures were migrated to valid deterministic 22-character session IDs and valid project IDs. Explicit legacy-inert fixtures and external opaque refs remain unchanged.

The parent subsequently confirmed `(cd agent && go test . ./doctor -count=1)` passed (`agent` 61.244s; `doctor` 2.780s). A second hubcore fixture audit migrated the remaining synchronized local IDs in PastIndex, observer, roster, tree, and detail-panel fixtures; explicit legacy fixtures were preserved. A later local rerun reached only an environmental listener failure in the agent package while doctor still passed; see the final verification evidence below.

The third parent rerun exposed an incomplete fixture migration at the project-root boundary. `PastIndex.Rebuild` now enumerates only glob matches whose basename passes `ValidateProjectID`, and `Find`/meta enumeration accepts only valid UUIDv7 session payloads. The failing fixtures had valid-looking replacement IDs under invalid scratch roots (`proj`, `repo`, `x`, `sha1`), used short pagination IDs, or passed a valid-looking project key for a nonexistent working directory. All nine reported hubcore seeds were fixed by synchronizing valid project roots, filenames, `SessionMeta.ID`, `RefreshOne`/`UpdateMeta` arguments, and expectations. The parent hub rerun also exposed the same issue in observer grant-history, pass5, image-read, project-delete, rename, and web-core fixtures; those were migrated similarly. Spawn helper seeds were corrected to create a real temporary working directory rather than asserting success for `/a/b`.

The broad parent `cmd/serf-hub` failure then exposed a final fixture layer in document serving, API session details, persisted tasks, and the not-live steer action. The stale fixtures used invalid local session IDs such as `01DOC`, `01LIVEIMG`, `01WORKMETRICS`, `01RENAMED`, `01PASTTASK`, and `01STEEROFF`; several PastIndex fixtures also used invalid project buckets such as `x`. These were migrated systematically: each local session now uses one valid 22-character ID across metadata, roster/thread identity, local ref, route/action, task filename, lookup, and expectation, and each enumerated project bucket now has a readable portion plus a 10-character base62 suffix. Pure output-image helpers that use generic IDs without crossing a local reader remain unchanged.

A subsequent parent full-package run disproved the earlier incomplete audit and exposed the remaining `web_test.go` migration surface. Workspace partials, state/workspace data, ended-session send, API search, roster-ended workspace, fork/subagent navigation, compact-resume, and not-live action fixtures still crossed strict local boundaries with legacy IDs. The entire affected regions were audited rather than patching individual assertion strings. Their metadata IDs, transcript headers and filenames, roster/source IDs, local refs, request URLs, parent relations, and expected values now use valid UUIDv7-derived 22-character IDs. The shared `buildRPCParentSession` helper was migrated centrally in `app_rpc_test.go`, and its `web_test.go` callers now use a valid `project-past-0000000000` PastIndex bucket rather than `past`.

A further parent full-package run exposed the same incomplete migration across the broader hub RPC fixtures. `app_rpc_test.go`, `app_threadread_test.go`, and `app_transcripts_test.go` still seeded local PastIndex state beneath invalid buckets such as `past`, `local`, `repo`, `images`, `prelude`, `failed`, `stuck`, `midflight`, `userin`, `idle`, and `fork`, with stale or overlong local IDs in metadata, transcript filenames/headers, refs, and assertions. The affected project roots now use valid readable-plus-10-base62 project IDs and their local session fixtures use known-valid UUIDv7 payloads throughout. External Codex/source-qualified IDs remain unchanged.

The same run identified remaining `web_test.go` local fixtures for project settings, persisted session images, and thread-document state, plus two resume fixtures using nonexistent `/tmp/project` paths. Those local IDs and project roots were migrated. `TestWeb_Send_EndedRosterEntryResumesForwardsAndKeepsReplay` now carries a real `t.TempDir()` working directory through the ended roster entry, resume request assertion, and resumed rendezvous entry. The shared resume-request fixture now uses a valid project bucket, valid session IDs, and real temporary working/worktree/restore directories.

The next parent full hub run (`job_01KXHBR7VA2PAYWM8G04Y235ZW`) provided final RED evidence for four incomplete fixtures: the two resumed web tests still read `/private/tmp/project` from the persisted shared-helper metadata; live search ordering had only one of four local IDs migrated; and the encoded-local thread-document route still used `child-A` beneath invalid `project1`. Both resume tests now overwrite the persisted metadata with their real `t.TempDir()` working directory before rebuilding PastIndex and carry that same path through resume assertions and returned rendezvous entries. Live search now uses four valid deterministic IDs and an empty query so it tests ordering rather than legacy ID text. The encoded-local route now uses a valid project bucket and valid local session ID while retaining the encoded-colon contract.

Parent full hub job `job_01KXHC0C16Y1BWDAX36GVDMEAS` then isolated the final six deterministic failures to the same shared fixture root: `buildRPCParentSession` still persisted nonexistent `/private/tmp/project` for RPC compact, model-set, and turn-start resume tests. A new `buildRPCParentSessionWithWorkingDir` helper now persists a caller-supplied real temporary project directory in both transcript and metadata. Each of the six tests creates its own `t.TempDir()`, asserts that exact directory in the resume request, and carries it in the resumed rendezvous entry. The original helper delegates to the new helper with its own real temporary directory, removing the fixed nonexistent path for all callers.

## Independent-review follow-up

The independent Task 9 review identified three valid clean-break gaps, one terminology cleanup, and one routing-coverage improvement. Tests were added before the corresponding production changes.

RED evidence:

- `TestListSessionMetas_CleanBreakValidatesFilenameAndMetadataID` initially indexed mixed local state whose legacy filename declared a valid new metadata ID, and did not enforce filename/metadata equality.
- `TestLoadSessionObserverGrants_SkipsInvalidLocalSessionIDs` initially surfaced legacy worker and observer session IDs from otherwise valid durable job/grant records.
- `TestResolveSessionMeta` initially allowed direct resume of a legacy local session ID. The CLI list fixture likewise demonstrated the need to omit legacy local entries while retaining valid new entries.

GREEN implementation and coverage:

- Shared local metadata enumeration now accepts a `*.meta.json` entry only when its filename ID passes `identifier.ValidateSessionID`, its decoded `SessionMeta.ID` passes the same validator, and the two IDs are equal. Invalid, legacy, corrupt, and mismatched entries are skipped without rewriting them. The mixed-state test verifies both bytes and modification times remain unchanged.
- Durable local observer-grant enumeration now validates observer session IDs and locally decoded worker session IDs. Cross-project and external/source-qualified opaque refs remain outside local validation.
- Direct CLI resume validates an explicitly supplied local session ID before loading it. CLI list output omits legacy local metadata while retaining valid entries.
- Remaining production names and comments in the reviewed scope now use project ID terminology rather than hash/bucket-hash terminology.
- `TestLocalRouteID_CleanBreakAndExternalRefs` now exercises the actual `/api/sessions/codex:thread_abc` source-specific route. It asserts HTTP 200 and that every source read preserves the opaque ref exactly; it does not merely test route classification helpers.

Parent comprehensive review-fix verification then exposed one stale test fixture after the shared enumeration fix: `agent/session_perf_test.go:920 TestListSessionMetas_SortedByUpdatedAt` returned zero entries because all three metadata filename/ID pairs used legacy IDs and were correctly filtered. The fixture now uses three valid deterministic 22-character IDs consistently in metadata and filenames. Its original `UpdatedAt` ordering contract is unchanged and still expects meta2, meta3, then meta1.

Parent final verification exposed one analogous observer-grant fixture regression: `FuzzHubcoreScenarios/seed#89` returned no observers because its durable `observer_session_id` payloads were the legacy strings `OBS1` and `OBS2`, which the new local grant validation correctly filters. The seed now uses valid deterministic 22-character observer IDs consistently as session metadata/directory IDs, grant payload keys, and expected observer IDs. Its single local worker ID remains synchronized between `local:` transcript refs and the `ObserversOf` query, and the two-observer union/deduplication contract is unchanged.

## Implementation summary

- Enforced `identifier.ValidateProjectID` when enumerating local project buckets and `identifier.ValidateSessionID` for local session refs/files.
- Kept structural ref parsing separate from local validation so opaque external/provider refs remain untouched.
- Updated transcript lookup, session discovery, doctor locate/selector, hub past indexing, and hub local route classification.
- Skipped invalid legacy session directories during hub observer-grant scans.
- Renamed doctor/find bucket terminology to `ProjectID`/`projectID`; removed the serialized `bucket_hash` field in favor of `project_id`.
- Preserved traversal guards and verified legacy files are not modified.
- Enforced valid, matching filename and metadata session IDs at the shared `schema.ListSessionMetas` enumeration boundary.
- Filtered invalid local worker and observer IDs when reconstructing durable observer grants.
- Rejected explicit legacy local CLI resume IDs and filtered legacy local list entries.

## Changed files

- `agent/cov_s3_find_test.go`
- `agent/doctor/doctor.go`
- `agent/doctor/filesystem_program_fuzz_test.go`
- `agent/doctor/locate.go`
- `agent/doctor/locate_test.go`
- `agent/doctor/selector.go`
- `agent/doctor/selector_test.go`
- `agent/doctor/tree_test.go`
- `agent/session_tools_find.go`
- `agent/transcript_lookup.go`
- `agent/transcript_lookup_test.go`
- `agent/transcript_ref.go`
- `agent/transcript_tools_test.go`
- `cmd/serf-hub/internal/hubcore/past.go`
- `cmd/serf-hub/internal/hubcore/past_test.go`
- `cmd/serf-hub/internal/hubcore/past_observers_test.go`
- `cmd/serf-hub/internal/hubcore/roster_test.go`
- `cmd/serf-hub/internal/hubcore/tree_test.go`
- `cmd/serf-hub/internal/hubcore/cov_rhub_session_order_test.go`
- `cmd/serf-hub/internal/hubcore/external_edges_test.go`
- `cmd/serf-hub/web_api_project_delete_test.go`
- `cmd/serf-hub/web_api_rename_test.go`
- `cmd/serf-hub/cov_spawn_main_fuzz_test.go`
- `cmd/serf-hub/cov_thread_data_pass5_fuzz_test.go`
- `cmd/serf-hub/cov_threadread_images_fuzz_test.go`
- `cmd/serf-hub/cov_web_core_api_test.go`
- `cmd/serf-hub/doc_serve_test.go`
- `cmd/serf-hub/app_rpc_test.go`
- `cmd/serf-hub/app_threadread_test.go`
- `cmd/serf-hub/app_transcripts_test.go`
- `cmd/serf-hub/web.go`
- `cmd/serf-hub/web_test.go`
- `cmd/serf-hub/web_api_tree_test.go`
- `agent/s4cov_transcript_read_test.go`
- `agent/job_watch.go`
- `agent/observer_grants.go`
- `agent/observer_grants_test.go`
- `agent/schema/cov_s5_snapshot_test.go`
- `agent/schema/snapshot.go`
- `agent/session_perf_test.go`
- `cmd/serf/run.go`
- `cmd/serf/run_test.go`
- `cmd/serf/run_coverage_fuzz_test.go`
- `cmdutil/cmdutil.go`
- `cmdutil/cov_rhub_cmdutil_test.go`

The pre-existing `.superpowers/sdd/task-1-report.md` was not edited, staged, or committed. `.superpowers/sdd/progress.md` was not modified.

## Test commands and results

Passed:

```text
(cd agent && go test . ./doctor -run 'Test.*(Legacy|Lookup|Locate|Find|Selector)' -count=1)
ok agent
ok agent/doctor

(cd agent && go test . -run 'Test.*(Transcript|ParentBucket|JSONL|Outline|Range|Window)' -count=1)
ok

go test ./cmd/serf-hub/internal/hubcore -run '^Test' -count=1
ok

go test ./cmd/serf-hub -run '^TestDetailsPanel_ShowsTranscriptPathWithCopy$|^TestDetailsPanel_ShowsAPILogPathWithCopy$|^TestLocalRouteID_CleanBreakAndExternalRefs$' -count=1
ok

(cd agent && go test ./doctor -run '^TestLocate_CleanBreakSkipsLegacyProjectAndSession$' -count=1)
ok

go test ./cmd/serf-hub -run '^TestLocalRouteID_CleanBreakAndExternalRefs$' -count=1
ok

go test ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub -run '^$' -count=1
ok compile-only

(cd agent && go test . ./doctor -run '^$' -count=1)
ok compile-only

git diff --check
passed

go test ./cmd/serf-hub/internal/hubcore -run 'FuzzHubcoreScenarios/seed#(67|79|92|99|101|102|121|137|161)$' -count=1
ok

go test ./cmd/serf-hub -run 'FuzzThreadDataPass5/seed#(0|1|4|5)$|FuzzCovThreadreadImagesSeed100/seed#(0|3)$|FuzzCovWebCoreAPI/seed#(0|1|2)$|FuzzSpawnMainHelpers/seed#(13|14)$|TestWeb_WorkspaceData_(CarriesObserverFromGrantHistory|IncludesEndedGrantHistoryObserver|UnionsStampAndGrantHistory)$|TestRenameEndedSessionEditsMetaAndRefreshesIndex|TestRenameLiveRaceDaemonFailureHardFails' -count=1
ok

go test ./cmd/serf-hub -run '^(TestDocImageServesPNG|TestDocImageServesLiveDescriptorURLWithoutPast|TestDocImageRejectsTraversalAndSVG|TestDocFile_ServesTextFileEscaped|TestDocFile_RendersMarkdown|TestDocFile_ServesWorktreeRelativePathForPaneNavigation|TestDocFile_BinaryNotice|TestAPISessionDetailCarriesWorkMetricsForEndedSession|TestAPISessionDetailHonorsRenamedMetaForLiveThread|TestWeb_SessionTasks_PastReturnsPersistedFile|TestWeb_Steer_NotLive_404)$' -count=1 -v
PASS (all 11 migrated fixtures)

go test ./cmd/serf-hub -run '^(TestWeb_WorkspacePartial_LiveSession_RendersHeader|TestWeb_WorkspacePartial_LocalRefCanonicalizesToLiveSession|TestWeb_WorkspacePartial_PastSession_RendersTitleAndState|TestWeb_WorkspacePartial_RendersBottomStripAffordances|TestWeb_WorkspacePartial_RendersWorkingDirInStatusRow|TestWeb_State_RendersInputStatusPartial|TestWeb_State_RendersCostEstimate|TestWeb_StatePartialRefreshesGeneratedSessionTitle|TestWeb_WorkspaceInitialMetaDoesNotDuplicateTitleOOB|TestWeb_WorkspaceDataUsesPersistedWorktree|TestWorkspaceData_PastSessionCarriesCostEstimate|TestWorkspaceData_NoCostWhenUsageNil|TestWeb_ApiSearch_FiltersPast|TestWeb_ApiSearch_PastUsesGeneratedNameTitle|TestWeb_WorkspacePartial_RosterEndedSessionKeepsResumeSendEnabled|TestWeb_Workspace_ForkOriginalBanner|TestWeb_Workspace_SubagentParentBreadcrumb|TestWeb_SessionAction_NotLive_404|TestWeb_Steer_NotLive_404)$' -count=1
ok primeradiant.com/serf/cmd/serf-hub 0.479s

go test ./cmd/serf-hub -run '^(TestHubThreadListOrdersPastSearchByUpdatedCreatedTitleAndID|TestHubThreadListOrdersLiveThreadsUsingPastTimestamps|TestPastThreadReadReconcilesDelegateRawWithTerminalJobstoreState|TestPastThreadReadProjectsThinkingFromTranscript|TestPastThreadReadProjectsToolResultOutputImages|TestResumeRequestForConfigPassesThroughOpenAIProfileID|TestResumeRequestForConfigPassesThroughCustomProfileID|TestResumeRequestForConfigErrorsOnEmptyProfileID|TestResumeRequestForConfigUsesRestoreRootWhenWorktreeActive|TestWeb_ProjectSettingsListEscapesWorkingDir|TestWeb_SessionImage_ServesShaReferencedInputImage|TestWeb_SessionImage_ServesShaReferencedToolResultImage|TestWeb_SessionImage_UnknownSha|TestWeb_ThreadDocument_StateRefreshPreservesCompactLocationMode)$' -count=1
ok primeradiant.com/serf/cmd/serf-hub 0.469s

go test ./cmd/serf-hub -run '^(TestWeb_ThreadDocument_RouteEncoding|TestWeb_ThreadDocument_StateRefreshPreservesCompactLocationMode|TestWeb_ApiSearch_OrdersLiveResultsByStartedAtAndID)$' -count=1
ok primeradiant.com/serf/cmd/serf-hub 0.382s

go test ./cmd/serf-hub -run '^(TestWeb_Send_EndedRosterEntryResumesForwardsAndKeepsReplay|TestWeb_SessionAction_CompactResumesPastThread|TestWeb_ApiSearch_OrdersLiveResultsByStartedAtAndID|TestWeb_ThreadDocument_RouteEncoding|TestWeb_ThreadDocument_StateRefreshPreservesCompactLocationMode)$' -count=1 -v
thread-document route/state: PASS
ended-send: blocked before assertions by `listen tcp6 [::1]:0: bind: operation not permitted`

go test ./cmd/serf-hub -run '^(TestWeb_SessionAction_CompactResumesPastThread|TestWeb_ApiSearch_OrdersLiveResultsByStartedAtAndID)$' -count=1 -v
search ordering: PASS
compact-resume: blocked before assertions by `listen tcp6 [::1]:0: bind: operation not permitted`

go test ./cmd/serf-hub -run '^(TestHubRPCThreadCompactStartResumesPastThread|TestHubRPCThreadModelSetResumesPastThread|TestHubRPCTurnStartResumesPastThread|TestHubRPCTurnStartResumesPastThreadAfterRelaySubscribeUnavailable|TestHubRPCTurnStartResumesPastThreadAndRelaysNotifications|TestHubRPCTurnStartResumesPastThreadAfterLocalTransportError)$' -count=1 -v
first test blocked before assertions by `listen tcp6 [::1]:0: bind: operation not permitted`

Each remaining test was then run independently. The first five are blocked before assertions by the same exact tcp6 `httptest` bind. `TestHubRPCTurnStartResumesPastThreadAfterLocalTransportError` is blocked before assertions by its explicit listener setup: `listen tcp 127.0.0.1:0: bind: operation not permitted`.

go test ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub -run '^$' -count=1
ok compile-only

(cd agent && go test . ./doctor -run '^$' -count=1)
ok compile-only

git diff --check
passed
```

Independent-review focused verification passed:

```text
(cd agent && go test ./schema -run '^TestListSessionMetas_CleanBreakValidatesFilenameAndMetadataID$' -count=1)
ok primeradiant.com/serf/agent/schema 0.190s

(cd agent && go test . -run '^(TestLoadSessionObserverGrants_ResolvesWorkerToObserver|TestLoadSessionObserverGrants_SkipsInvalidLocalSessionIDs)$' -count=1)
ok primeradiant.com/serf/agent 0.559s

go test ./cmdutil -run '^TestResolveSessionMeta$' -count=1
ok primeradiant.com/serf/cmdutil 0.291s

go test ./cmd/serf -run '^(TestListSessions_PrintsFormattedList|FuzzRunCoverage)$' -count=1
ok primeradiant.com/serf/cmd/serf 0.383s

go test ./cmd/serf-hub -run '^TestLocalRouteID_CleanBreakAndExternalRefs$' -count=1
ok primeradiant.com/serf/cmd/serf-hub 0.368s

(cd agent && go test ./schema -count=1)
ok primeradiant.com/serf/agent/schema 0.206s

go test ./cmdutil ./cmd/serf ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub -run '^$' -count=1
ok (all four packages; compile-only)

(cd agent && go test . ./schema ./doctor -run '^$' -count=1)
ok (all three packages; compile-only)

git diff --check
passed
```

Independent-review unfiltered-suite attempts remain limited by this sandbox rather than deterministic assertions in the focused clean-break tests:

```text
(cd agent && go test . ./doctor -count=1)
agent: FAIL TestSession_OpenAIResponsesContinuationOffUsesFullHistory before assertions:
listen tcp6 [::1]:0: bind: operation not permitted
doctor: ok

go test ./cmd/serf-hub/internal/hubcore -count=1
FAIL FuzzHubcoreScenarios/seed#54 before assertions:
listen tcp6 [::1]:0: bind: operation not permitted

go test ./cmd/serf-hub -count=1
FAIL TestHubRPCAuthStatusUsesUserScopedOpenAIAuth before assertions:
listen tcp6 [::1]:0: bind: operation not permitted

go test ./cmdutil -count=1
FAIL TestQueryModelContextWindow_UsesInstanceBaseURL before assertions:
listen tcp6 [::1]:0: bind: operation not permitted

go test ./cmd/serf -count=1
Several serve tests timed out waiting for rendezvous entries after sandboxed child startup; the run later stopped at TestUpgradeSubcommandInstallsSnapshot with:
listen tcp6 [::1]:0: bind: operation not permitted
```

Parent follow-up fixture verification:

```text
(cd agent && go test . -run '^TestListSessionMetas_SortedByUpdatedAt$' -count=1)
ok primeradiant.com/serf/agent 0.491s

(cd agent && go test ./schema . ./doctor -count=1)
schema: ok 0.232s
agent: stopped at TestSession_OpenAIResponsesContinuationOffUsesFullHistory before assertions:
listen tcp6 [::1]:0: bind: operation not permitted
doctor was included in the command; the package run was constrained by the same sandboxed full-suite execution.

go test ./cmdutil ./cmd/serf -count=1
full-package execution remains constrained by sandbox listener/child-startup behavior described above. Focused clean-break reruns pass:
go test ./cmdutil -run '^TestResolveSessionMeta$' -count=1
ok 0.204s
go test ./cmd/serf -run '^(TestListSessions_PrintsFormattedList|FuzzRunCoverage)$' -count=1
ok 0.313s

go test ./cmd/serf-hub/internal/hubcore -count=1
stopped at FuzzHubcoreScenarios/seed#54 before assertions:
listen tcp6 [::1]:0: bind: operation not permitted

go test ./cmd/serf-hub -count=1
stopped at TestHubRPCAuthStatusUsesUserScopedOpenAIAuth before assertions:
listen tcp6 [::1]:0: bind: operation not permitted

Compile-only checks for schema, agent, doctor, cmdutil, cmd/serf, hubcore, and hub all pass. `git diff --check` passes.
```

Observer seed follow-up verification:

```text
go test ./cmd/serf-hub/internal/hubcore -run '^FuzzHubcoreScenarios/seed#89$' -count=1
ok primeradiant.com/serf/cmd/serf-hub/internal/hubcore 0.365s

(cd agent && go test . -run '^(TestLoadSessionObserverGrants_ResolvesWorkerToObserver|TestLoadSessionObserverGrants_SkipsInvalidLocalSessionIDs)$' -count=1)
ok primeradiant.com/serf/agent 0.511s

go test ./cmd/serf-hub/internal/hubcore -run '^(FuzzHubcoreScenarios/seed#89|Test.*Observer)' -count=1
ok primeradiant.com/serf/cmd/serf-hub/internal/hubcore 0.276s

go test ./cmd/serf-hub/internal/hubcore -count=1
stopped only at FuzzHubcoreScenarios/seed#54 before assertions:
listen tcp6 [::1]:0: bind: operation not permitted

go test ./cmd/serf-hub -count=1
stopped only at TestHubRPCAuthStatusUsesUserScopedOpenAIAuth before assertions:
listen tcp6 [::1]:0: bind: operation not permitted
```

Required affected-suite commands were also run. The parent-reported deterministic hubcore seeds 67, 79, 92, 99, 101, 102, 121, 137, and 161 now pass individually, as do the reported deterministic hub seeds for observer history, spawn helpers, pass5, image reads, web-core API, project delete, rename, workspace/state/search/action routes, document serving, API detail, persisted tasks, and not-live steer.

Final unfiltered commands are blocked only by listener creation:

```text
go test ./cmd/serf-hub -count=1
FAIL TestHubRPCAuthStatusUsesUserScopedOpenAIAuth before assertions:
httptest: failed to listen on a port: listen tcp6 [::1]:0: bind: operation not permitted

go test ./cmd/serf-hub/internal/hubcore -count=1
FAIL FuzzHubcoreScenarios/seed#54 before assertions:
httptest: failed to listen on a port: listen tcp6 [::1]:0: bind: operation not permitted

(cd agent && go test . ./doctor -count=1)
agent: FAIL TestSession_OpenAIResponsesContinuationOffUsesFullHistory before assertions:
httptest: failed to listen on a port: listen tcp6 [::1]:0: bind: operation not permitted
doctor: ok 2.273s
```

The earlier temporary precompiled-test-binary audit was not sufficient evidence: a later parent full-package run exposed deterministic stale fixtures that the audit had not validly cleared. That earlier zero-failure claim is withdrawn. After the comprehensive follow-up migration, the exact 19-test regex above covers the non-listener deterministic failures named by the parent and passes. Listener-dependent reported tests such as ended-session send and compact-resume cannot reach assertions in this sandbox because their `httptest.NewServer` calls panic on bind. The required unfiltered package commands were still run and stop only at the exact listener failures recorded above.

After the additional hub RPC migration, the exact 14-test regex above covers the newly exposed non-listener deterministic failures and passes. The RPC server tests and the two resumed web tests are listener-dependent in this sandbox: for example, `go test ./cmd/serf-hub -run '^(TestWeb_Send_EndedRosterEntryResumesForwardsAndKeepsReplay|TestWeb_SessionAction_CompactResumesPastThread)$' -count=1 -v` stops in the first test at `httptest.NewServer` with `listen tcp6 [::1]:0: bind: operation not permitted`, before any resumed-session assertion. The parent environment reports full hubcore passing; the local full hubcore command reaches only the same exact listener restriction at `FuzzHubcoreScenarios/seed#54`.

Final local verification after `job_01KXHBR7VA2PAYWM8G04Y235ZW`: `go test ./cmd/serf-hub -count=1` stops only at `TestHubRPCAuthStatusUsesUserScopedOpenAIAuth` with the exact listener bind before assertions. `go test ./cmd/serf-hub/internal/hubcore -count=1` locally stops only at `FuzzHubcoreScenarios/seed#54` with the same bind (the parent full hubcore run passes). `go test ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub -run '^$' -count=1` passes, and `git diff --check` passes.

Final local verification after `job_01KXHC0C16Y1BWDAX36GVDMEAS` is unchanged environmentally: the unfiltered hub command stops only at `TestHubRPCAuthStatusUsesUserScopedOpenAIAuth` with exact tcp6 bind denial; local hubcore stops only at seed 54 with the same denial while the parent hubcore run passes. Hub/hubcore compile-only checks and `git diff --check` pass. No deterministic assertion was reached or failed in the corrected six fixtures locally because listener creation is prohibited.

## Legacy fixture integrity

Clean-break tests snapshot legacy transcript bytes and modification times and assert they remain unchanged. Those tests pass. Legacy project/session state is skipped or rejected and is never renamed or deleted.

## Self-review

- Local project/session validators are applied only after a ref is identified as local state or at local filesystem enumeration boundaries.
- `codex:thread_abc` remains opaque and is not classified as local; its source-qualified ref is preserved.
- Path separators remain rejected by structural parsing and route handling.
- Legacy 16-hex project buckets and 26-character ULID session payloads are rejected or skipped without mutation.
- Stale `bucketHash`, `BucketHash`, `bucket_hash`, and related terminology audits are clean in the changed production reader scope.
- Diff hygiene passes.
- Parent-reported stale local fixtures now use synchronized metadata IDs, filenames/directories, roster/thread entries, local refs, routes/actions, selectors, and expected assertions.
- The remaining `tree_test.go` ordering fixtures now use valid fixed-width local IDs; intentionally generic tree IDs and explicit legacy/external fixtures were not converted.
- Scratch PastIndex roots in the migrated fixtures now use valid readable-tail plus 10-character base62 project IDs; direct tree tests use an actual temporary directory and its canonical resolved project ID.
- Document-serving, API-detail, persisted-task, and action fixtures now use valid project/session identifiers at every local boundary; generic output-image helper IDs remain intentionally opaque because they do not cross a local reader.
- The remaining workspace/state/search/fork/subagent fixtures and the shared RPC parent-session helper now use synchronized valid IDs; `web_test.go` PastIndex callers no longer seed the helper under an invalid `past` bucket.
- Hub RPC past-list/read/merge/collision/action/fork/transcript fixtures now seed valid project buckets and synchronized local IDs; app thread-read and transcript-target fixtures follow the same boundary rules.
- Resume configuration tests now use real canonicalizable directories, including worktree restore-root coverage, rather than nonexistent `/tmp/project` fixtures.
- Local metadata enumeration validates both the filename ID and decoded metadata ID and requires exact equality; review fixtures prove rejected mixed state is untouched.
- Durable observer grants validate only local worker/observer session IDs. External opaque refs remain unvalidated and the actual Codex route preserves `codex:thread_abc` end to end.
- Explicit direct local resume rejects legacy IDs, while list/resume-last consume the filtered shared metadata enumeration.
## Concerns/blockers

No deterministic concern remains. The independent-review sandbox could not bind local test listeners, but the parent environment subsequently ran every affected package unfiltered after the review fixes:

```text
(cd agent && go test ./schema -count=1)
ok   primeradiant.com/serf/agent/schema   0.546s

(cd agent && go test . -count=1)
ok   primeradiant.com/serf/agent   68.909s

(cd agent && go test ./doctor -count=1)
ok   primeradiant.com/serf/agent/doctor   2.664s

go test ./cmdutil -count=1
ok   primeradiant.com/serf/cmdutil   1.622s

go test ./cmd/serf -count=1
ok   primeradiant.com/serf/cmd/serf   5.331s

go test ./cmd/serf-hub -count=1
ok   primeradiant.com/serf/cmd/serf-hub   52.544s
```

That run exposed only the stale observer-grant seed described above. After fixing it, the final parent command passed both hub packages and the diff check:

```text
go test ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub -count=1 && git diff --check
ok   primeradiant.com/serf/cmd/serf-hub/internal/hubcore   1.063s
ok   primeradiant.com/serf/cmd/serf-hub                   52.212s
```

## Final independent re-review

The first final re-review found one Minor issue: stale `<hash>`, `hashes`, and `hashed` project terminology remained in `agent/doctor/selector.go` and `agent/doctor/locate.go`. Commit `3ed8e194f` replaced it with project-ID terminology. Fresh parent verification passed the focused selector/locator tests, scoped terminology audit, and `git diff --check`.

The independent re-review of that fix reported no remaining findings:

- Spec compliance: pass.
- Task quality: Approved.
- Critical: 0; Important: 0; Minor: 0.

## Commits

- `578b09835` — clean-break reader implementation and fixture migration.
- `029f440ff` — independent-review fixes and regressions.
- `3ed8e194f` — final review cleanup replacing stale project-hash terminology.
