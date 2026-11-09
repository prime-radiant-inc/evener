# Tech-debt audit: appwire-server

Slice: `appwire/` (Go, non-test) and `server/` (non-test). Worktree on origin/main (a6c0b9c561). Read-only audit; no tests were run.

## Overview and inventory

| Area | Files (non-test) | Lines | Notes |
|---|---|---|---|
| `appwire/` | 36 + `appwiretest/` | 12,204 | `types.go` 5,898 (365 types, 20 funcs), `errors.go` 1,058, `client.go` 1,046, `stream_transport.go` 584, `protocol.go` 410 |
| `server/` | 13 | 7,070 | `appwire_runtime.go` 3,074 (697 of those are `//` comment lines), `server.go` 1,003, `thread_history.go` 824, `thread_envelope.go` 573 |
| Tests in slice | appwire + server `*_test.go` | ~36,000 | includes coverage-driven files (`cov_rhub_*`, `*_gaps*`, `*_covtest*`) |

Ownership model (docs/product/subsystems.md S05): `appwire/protocol.go` is the catalog and single source of truth; `docs/appwire-protocol.md` and `appwire-client/typescript/types.gen.ts` are generated from it. `server/` is the per-session daemon's AppWire runtime: projection of session events into notifications, a materialized "thread envelope" read by `thread/read`, retry-safe mutation handlers, history projection.

Overall health: the protocol catalog and generated-output discipline are good. The debt concentrates in (a) `server/appwire_runtime.go` which is a 3,000-line god file with copy-pasted handler bodies, (b) three-to-four parallel hand-copied struct families for diagnostics/delegates between `agent`, `server`, `appwire` and the hub, (c) dead surface (typed client wrappers, `StatusInfo` fields, reservation state, status helpers) kept alive only by tests that were written to cover it, and (d) very heavy narrating / history comments.

---

## Findings

### appwire-server-1: Seven retry-safe turn handlers are copy-pasted, and the copies have drifted (high)

Locations: `server/appwire_runtime.go:1592-1876` (`handleAppTurnStart` 1592, `Steer` 1644, `Interrupt` 1730, `Queue` 1749, `DrainAsSteer` 1785, `PromoteQueuedAsSteer` 1822, `CancelQueued` 1849); also `1904-1975` (notes, urls); `appwire/types.go:2615-2725` (param shapes).

Evidence:
- Every handler opens with the same 5 lines: trim `ClientMutationID`, trim `ExpectedInstanceID`, `lockRetrySafeMutation`, `defer unlock()`; and closes with `s.mu.RLock(); fn := s.retrySafeTurns.X; s.mu.RUnlock()`, nil check, `fn(params)`, `s.refreshFacets(facetQueue)`, `agent.NormalizeClientMutationError(...)`. `ClientMutationID = strings.TrimSpace` appears 10 times in non-test server code.
- Start, Steer, Queue and Drain each repeat the ~20-line input gate: `NormalizeMutationInput`, `ValidateSkillInputSupport`, `ValidateCommandInputSupport`, `HasContent`, `params.Input = input.Items`.
- Drift between the copies: Start/Steer say `"input is required"`, Queue says `"input required"` (line 1771 vs 1609/1665); DrainAsSteer has the three validators but no `HasContent` check (lines 1785-1820) so an all-blank drain is forwarded; Interrupt/Start/Steer pass `params.ThreadID` to the lock while Queue/Drain/Promote/Cancel pass `""` because `TurnQueueParams`/`TurnDrainAsSteerParams` have `Ref` (non-omitempty) and no `ThreadID` whereas `TurnSteerParams` has `Ref omitempty` + `ThreadID` (`types.go:2615`, `2636`, `2719`).
- The 80-line comment on Start's `refreshFacets` ("Every mutation handler refreshes... a uniform refresh means nobody has to re-derive which of the seven happens to emit") argues for a shared helper but the code repeats the call seven times.

Impact: a new retry-safe mutation costs ~40 copied lines and silently picks a variant of the validation; the drift above is already live. Large maintenance drag on the file that owns the mutation contract.

Fix plan:
1. Add a characterization test per handler for the validation order and error codes (assert on error code/`Data.EvenerErrorInfo`, not message text, per the repo testing rule).
2. Extract in `server/` one generic helper, e.g. `retrySafeMutation[P identity, R any](s *Server, p P, pick func(RetrySafeTurnFunctions) func(P) (R, error), unavailable string, opts) (R, error)`, where `identity` is a small accessor interface (`MutationIdentity() (ref, threadID, expectedInstance, mutationID *string)`) or take the already-extracted fields as arguments. The helper owns trim, lock, nil-check, call, `refreshFacets(facetQueue)`, error normalization.
3. Extract `normalizeInputForSeam(items, wired bool) ([]InputItem, error)` for the 4 input-bearing handlers (decide explicitly whether Drain requires content, and fix the one wrong copy red-test-first).
4. Optional follow-up, needs Jesse's call because it changes generated TS: embed a shared `MutationIdentity{Ref, ClientMutationID, ExpectedInstanceID}` in the 10 params types (`ClientMutationID  string` appears 13 times in `types.go`); the `appwirets` generator must handle embedded structs first.
5. Docs: no product-doc change; update comments only.

Size: net -250 to -300 lines in `appwire_runtime.go`, +60 helper. Risk: medium. These handlers are the retry-safety contract; keep existing tests (`appwire_*_test.go`, `thread_clear*`, `steering_identity_test.go`) as the guard and run them in CI.

---

### appwire-server-2: agent.DetailedStatus is hand-mirrored three times on its way to the wire, and the mirrors already drop a field (high)

Locations: `agent/status.go:121-215` (source structs); `server/server.go:81-215` (`JobStatusInfo`, `DelegateStatusInfo` 50 fields, `DetailedStatus`, `SkillInfo`, `MCPServerInfo`, `PluginStatusInfo`, `HookEventStatus`, `TurnSlotStatus`, `ToolInfo`); `cmd/evener/serve.go:2232-2350` (`agentToServerDetailedStatus` copies agent -> server); `server/appwire_runtime.go:2590-2860` (`appDiagnosticsFromDetailedStatus`, `appJobFromDetailedStatus`, `appWatchFromDetailedStatus`, `appDelegateFromDetailedStatus` copies server -> appwire); `cmd/evener-hub/app_threadread.go:1278` (`appwireDelegateFromAgentStatus`, a second agent -> appwire copy of the same 50 fields).

Evidence:
- `appDelegateFromDetailedStatus` (appwire_runtime.go:2827-2856) and `appwireDelegateFromAgentStatus` (app_threadread.go:1278-1305) are line-for-line the same field list with different clone-helper names (`cloneServerInt64` vs `cloneHubInt64`).
- Drift: `agent.DelegateStatusInfo` has `LatestIntent` (`agent/status.go` json `latest_intent`), and `appwire.EvenerDelegateInfo` has `LatestIntent`, `LogicalOwnerSessionID`, `ReportPreview`, `ReportPreviewTruncated` (`types.go:1798-1839`), but `server.DelegateStatusInfo` (server.go:105-163) has none of them and neither copy site maps `LatestIntent`; so the daemon's diagnostics path can never carry it.
- The mirror is not forced by an import cycle: `server` already imports `agent` and uses `agent.WatchStatusInfo` directly inside `server.DetailedStatus.Watches` (server.go:183) while Jobs and Delegates are mirrored. The mirror types also carry snake_case `json` tags although nothing in `server` marshals them (only `DetailedStatus.MarshalJSON`, `server.go:204`, which has no non-test caller).
- Tests exist only to pin the copies (`cmd/evener/serve_test.go` `TestAgentToServerDetailedStatus_*`, 7 tests).

Impact: every new diagnostics field has to be added in 3-4 places; one already fell out. About 450 lines of pure field copying.

Fix plan:
1. Red test first: a table test that populates every field of `agent.DelegateStatusInfo` via reflection and asserts the wire `EvenerDelegateInfo` carries it (fails today on `LatestIntent`).
2. Delete `server.JobStatusInfo`, `DelegateStatusInfo`, `SkillInfo`, `MCPServerInfo`, `PluginStatusInfo`, `HookEventStatus`, `TurnSlotStatus`, `ToolInfo`; make `server.DetailedStatus` an alias for (or directly use) `agent.DetailedStatus`. Where `agent.DetailedStatus` holds richer data (`SkillCatalog`, `skill.Descriptor`), do the skill-control join once.
3. One converter `agent.DetailedStatus -> appwire.EvenerDiagnostics` (and `agent.DelegateStatusInfo -> appwire.EvenerDelegateInfo`) in one package both the daemon and hub import (`agent` already imports `appwire`); delete `agentToServerDetailedStatus`, `appDelegateFromDetailedStatus`, `appwireDelegateFromAgentStatus`, `appJobFromDetailedStatus`.
4. Repoint `ThreadEnvelopeSource.DetailedStatus()` (thread_envelope.go:143) at the agent type; update `liveThreadEnvelopeSource`, `sessionTaskEnvelopeSource`, `wireProbeEnvelopeSource` in tests.
5. Docs: subsystems S05 row if it names the converters; otherwise none.

Size: roughly -450 / +80 lines across server, cmd/evener, cmd/evener-hub. Risk: medium; mechanical, guarded by existing lossless-copy tests migrated to the single converter.

---

### appwire-server-3: 48 typed `Client` wrappers and the `PendingCoordinator` seam have no production caller (medium)

Locations: `appwire/client.go:553-1046` (77 `c.request(ctx, Method...)` sites), `appwire/client.go:268-275` and `668-760` (pending coordinator hooks), `appwire/optimistic.go` (28 lines), `appwire/client.go:496-523`.

Evidence: for each exported `*Client` method I grepped non-test callers outside `client.go`. 49 have zero production callers, including `ThreadStart`, `ThreadResume`, `ThreadFork`, `TurnSteer`, `TurnInterrupt`, `TurnQueue`, `TurnDrainAsSteer`, `TurnPromoteQueuedAsSteer`, `PathsComplete`, `DirsCreate`, `GitHead`, `NavigationRead`, `FavoriteSet`, `ArchiveSet`, `ProjectDelete`, `SessionDelete`, `AuthStatus`, `AuthLogin*`, `AuthLogout`, `UpdateCheck/Apply`, all `Marketplace*` (6), all `Plugin*` (9), all `DevicePairing*` (4), `CommandList`, `SpawnSlashCatalog`, `MobilePairing`, `HarnessList`, `SetPendingCoordinator`. The hub's remote source uses the generic `client.Request(ctx, method, params, out)` (`cmd/evener-hub/internal/appsource/remote_hub_source.go:280,1741,1822`; `restart.go:124`; `app_host_probe.go:57`). `PendingCoordinator`/`PendingHandle` (`optimistic.go`) have zero non-test implementers; the only effect is that `TurnSteer`/`TurnDrainAsSteer` call `Register` when `c.pendingCoord != nil`, which is never set in production. `TestClientRequestWrappersRoundTrip` (`cov_rhub_appwire_test.go:86`) and the per-wrapper `TestClient*RoundTrip` tests exist to cover the thin wrappers.

Impact: ~320 lines of non-test code plus several hundred lines of tests that assert the wrappers marshal what the generic `Request` already marshals; every new method is invited to add another wrapper.

Fix plan: delete wrappers with no production caller (keep the handful that are used: `ThreadList`, `ThreadRead`, `ThreadUnsubscribe`, `ThreadTurnsList`, `TurnStart`, `ThreadModelSet`/`NameSet`/`ReasoningEffortSet`/`VisionModelSet`, `TasksList`, `Jobs*`, `TurnCancelQueued`, `ThreadShutdown`, `Search`, etc. as the grep shows), delete `PendingCoordinator`, `optimistic.go`, `pendingTargetRef`, `inputText`; collapse the `Turn*` pending-registration branches. Migrate the tests that exercise the real transport behavior (send-slot, keepalive, wire errors) to `Request`; delete wrapper-coverage tests. Then add a one-line `typedRequest[R any](c, ctx, method, params) (R, error)` generic if the remaining wrappers still repeat the `var out; err :=` three-liner. Check `test/e2e/turncpu/main.go` and any TS-facing docs before deleting. Update `docs/product/subsystems.md` S05 only if it lists the Go client surface.

Size: -400 lines code, -600 tests (estimate from file layout). Risk: low; pure deletion, compile-checked. Confirm with Jesse that "no future caller" is intended (YAGNI says yes).

---

### appwire-server-4: `StatusInfo` carries ~15 fields and an entire `ActionCapabilities` type that nothing writes or reads (medium)

Locations: `server/server.go:227-292` (`StatusInfo`), `server/server.go:295-307` (`ActionCapabilities`), `server/server.go:287-293` (`ContextMetrics` is used elsewhere).

Evidence: `StatusInfo` has `ContextPressure/Used/Window/Remaining`, `Detailed`, `Capabilities ActionCapabilities`, `DescendantSessionIDs`, `DescendantStates`, `Usage`, `WorkMillis`, `ActiveTurnStartedAt`, `FailedToolCalls`, `PendingAsk`, `PendingEscalation`. Non-test writes to `s.status.*` are only `SessionID`, `Model`, `Profile`, `WorkingDir`, `State`, `Turns` (server.go:617-639, bridge.go:225-262, appwire_runtime.go:221,363,596,617). Reads are only `SessionID`, `State`, `Model`, `Profile`, `WorkingDir`, `Turns`. `ActionCapabilities` is referenced only as the type of the unused `Capabilities` field. `SetStatus` has only test callers. Everything else lives in `threadEnvelope`, which superseded it. The `json` tags (snake_case) are unused: nothing marshals `StatusInfo`.

Impact: a reader trying to learn "where does thread state live" meets two parallel models with 100+ lines of authoritative-looking comments on the dead one.

Fix plan: reduce `StatusInfo` to the six live fields, drop json tags, delete `ActionCapabilities`, delete `SetStatus` if tests can use `UpdateSessionInfo`/`SetState` (otherwise keep). Compile errors drive the cleanup; no behavior change. Size -90 lines. Risk: low.

---

### appwire-server-5: Turn "reservation" state is dead but threaded through eight sites and a function parameter (medium)

Locations: `server/server.go:392` (`appReservedTurnID`), `server/server.go:860`, `server/appwire_runtime.go:210, 646, 2179, 2220, 2250, 2483, 2897`, `server/bridge.go:191`, `appStatus(state, processing, turnReserved)` at `appwire_runtime.go:3023`.

Evidence: the code's own comment says it: "turnReserved is in fact unreachable in the shipped daemon today: reserveAppTurnIDForStart is the only writer of appReservedTurnID and has no non-test callers" (`appwire_runtime.go:3013-3020`). I confirmed: `reserveAppTurnIDForStart` is defined only in `server/appwire_mutation_test_helpers_test.go:126`; production only clears the field. The comment also says it is "kept because this function must stay correct for the reservation if it is ever wired up", the textbook YAGNI case. The model-set and vision-set handlers build a `"turn " + reservedTurnID + " is active"` conflict message from it (2220-2232, 2250-2262).

Impact: every status/capability/clear/model-change decision carries a dead input; a test helper reaches into production state to set it.

Fix plan: remove `appReservedTurnID`, the `turnReserved` parameter of `appStatus`, and the branches; move the test helper's use to a real turn path (`SetProcessingTurn`) or delete those tests (31 test references). Same change: `appStatus`'s `switch` (3023-3050) returns each recognized state unchanged and defaults to idle, so it collapses to a membership check; `inputFromItems` (`appwire_runtime.go:3053`) has only test callers and belongs in a test helper. Docs: none. Size -90 lines. Risk: low-medium (tests reference the field heavily; verify the active-status cases still hold via `processing`).

---

### appwire-server-6: `RecordAppEvent` and `RecordDescendantAppEvent` duplicate the per-notification projection switch; both are 140-180 line functions (medium)

Locations: `server/appwire_runtime.go:339-516` (`RecordAppEvent`), `676-815` (`RecordDescendantAppEvent`).

Evidence: both run `switch params := item.Params.(type)` over `ThreadStartedParams`, `TaskUpdatedParams`, `GoalUpdatedParams`, `NotesUpdatedParams`, `UrlsUpdatedParams` with near-identical bodies, differing only in the sink (`s.appEnvelope.*` plus generation bumps for the root, `projection.thread.Evener.*` for a descendant). The task-routing prelude (`acceptTaskUpdatePublicationLocked`, `routeOwner` blanking when epoch or revision is 0, `taskCarrierTargetsLocked`, `applyTaskCarrierLocked`) is pasted in both. `sourceIDForProjection` and an inline `if sourceID == "" { sourceID = "local" }` coexist (`appwire_runtime.go:2484-2486` vs `825`; `refreshFacets` line `stampSourceID`); `Ref{SourceID,ThreadID}.String()` is rebuilt ~14 times.

Impact: any new typed carrier (the last three were notes, urls, cwd) must be added twice and its generation/fence semantics kept in sync by hand.

Fix plan: introduce a small `projectionSink` (root envelope vs descendant thread) with methods `setGoal`, `setNotes`, `setURLs`, `setTasks`, and one `foldProjected(sink, item) []pendingAppNotification`; root and descendant functions keep only their distinct gating (deferred terminal notifications, execution boundaries, resync, history). Add `(s *Server) refFor(threadID)` for the repeated ref construction. Guard with the existing `appwire_notes_*`, `descendant_watches`, `appwire_activity` tests plus one new table test that drives the same carriers through root and descendant and asserts equal wire output. Size: -120 lines. Risk: medium (projection ordering under `projectionMu`).

---

### appwire-server-7: Callback-field boilerplate: 25 `Set*Func` setters, 39 RLock-copy-nil blocks, a 50-method single-implementation interface (medium)

Locations: `server/server.go:380-460` (fields), `486-830` (setters), `server/appwire_runtime.go` (39 `s.mu.RLock()` sites, 26 `appwire.Unavailable(...)` messages), `server/appwire_session_activity.go:12-105`, `server/appwire_daemon.go:18-60`, `cmd/evener/serve.go:101-170` (`serveServer` interface), `appwire/types.go:2899` (`JobsListResponse.Data any`).

Evidence:
- Four activity handlers (`appwire_session_activity.go:26,47,68,89`) differ only in the field they read and the response type; each repeats RLock, `sourceIDForProjection`, `validateSessionActivityRef`, nil check with the same `"session activity is unavailable"`, and `readCurrentSessionActivity`.
- Each simple handler repeats `s.mu.RLock(); fn := s.xFunc; s.mu.RUnlock(); if fn == nil { return ..., Unavailable("... not available") }` (goal, compact, shutdown, model, vision, name, reasoning, jobs output, job get, urls, notes, sandbox, delegate stop).
- Inconsistent unwired policy: `handleAppTasksList`/`JobsList`/`ModelList` return empty success when unwired, `JobsOutput`/`JobsGet` return Unavailable.
- `serveServer` (serve.go:101) re-declares ~50 methods of `*server.Server` as an interface; the only production implementation is `server.NewServer` and the code type-asserts back (`s.(*server.Server).AppSubscriberCount`, serve.go:291).
- `jobsFn` is `func(...) (any, error)` and `JobsListResponse.Data` is `any`, the one untyped wire result in the slice.

Impact: adding a verb means editing the struct, a setter, the handler, the capability set, the `serveServer` interface and often a test fake.

Fix plan: (1) a generic `hook[T any]` on `Server` with `Set(fn)`/`Get() (fn, ok)` (mutex-guarded) replacing the 25 fields + setters + 39 lock blocks; (2) `requireHook(fn, "msg")` helper or have handlers call `hook.Get()`; (3) one generic `sessionActivityHandler[P, R]`; (4) delete `serveServer` and use `*server.Server` in `serveDeps`, or shrink it to the 3 methods the fuzz test fakes; (5) give `JobsListResponse.Data` a real type, regenerate TS and docs (`go generate ./appwire`); (6) decide one unwired policy and pin it. Capabilities (`appCapabilitiesLocked`) read the same hooks; keep them reading `hook.Has()`. Size -300 lines. Risk: medium (touches every wiring site in `cmd/evener/serve.go`); mechanical. Needs Jesse sign-off on (4) since tests currently inject fakes through that seam.

---

### appwire-server-8: Thread envelope keeps two authorities (sampled facets and typed carriers) plus five generation fences for "old producers" that live in the same binary (medium)

Locations: `server/thread_envelope.go:30-120` (struct, `taskCarrierGeneration`, `goalCarrierGeneration`, `cwdCarrierGeneration`, `notesCarrierGeneration`, `urlsCarrierGeneration`), `thread_envelope.go:296-326` (`facetsByEvent` goal rows), `thread_envelope.go:366-476` (`refreshFacets` snapshot of five generations, `assign(... cwdStale, notesStale, urlsStale)`), `appwire_runtime.go:832-882` (`legacyTaskPublication`, "old-producer compatibility path").

Evidence: file comments state the sampled path is retained for "checkpoint/legacy recovery"; "The older lifecycle events remain refresh triggers for compatibility with producers that predate the structured update" (thread_envelope.go ~line 301); `acceptTaskUpdatePublicationLocked` says "Zero metadata remains the old-producer compatibility path". The producers (agent session, delegates) are compiled into the same binary as the server, so no old producer can exist unless an in-repo emitter omits metadata. `refreshFacets` is a 130-line function that snapshots five counters under RLock, samples outside the lock, re-takes the lock, compares all five and then conditionally drops facets.

Impact: concurrency invariants that need a page of comments to explain; every new carrier (notes, urls, cwd were each added in turn) adds another counter and another `assign` parameter.

Fix plan (needs Jesse's approval per the no-new-compat rule, and it is deletion of compat): (1) grep every emitter of `EventTaskUpdated`/`GoalUpdated`/`NotesUpdated`/`UrlsUpdated`/`EnvironmentChanged` and confirm each always sets owner/epoch/revision; add a test asserting it; (2) if so remove `legacyTaskPublication`, the legacy event rows from `facetsByEvent` for goal/tasks, and have SessionStart seed (already "self-contained") be the only non-carrier writer; (3) replace the five counters with a single `carrierGeneration` per identity bumped by any carrier, or drop sampling for those fields entirely. Update the comment block at the top of `thread_envelope.go` and docs/product/session-activity.md if it describes the sampled path. Size: -150 lines, large comment reduction. Risk: medium-high (recovery after clear and identity replacement); do it behind the existing envelope tests (`thread_envelope_*_test.go`) in CI. If (1) finds an emitter that omits metadata, fix the emitter instead and keep the rest of the plan.

---

### appwire-server-9: Cross-repo duplication of nil-safe pointer clone helpers and state-root resolution (medium, cross-cutting)

Locations: `server/appwire_runtime.go:2858,2866` (`cloneServerBool`, `cloneServerInt64`), `cmd/evener/serve.go:2347,2355` (`cloneServeBool`, `cloneServeInt64`), `cmd/evener-hub/app_threadread.go:1309,1317` (`cloneHubBool`, `cloneHubInt64`), `internal/appprojector/appwire_projection.go:573,581` (`cloneBoolPointer`, `cloneInt64Pointer`), `agent/status.go:569` (`cloneBool`), `agent/jobs_activity.go:1293` (`cloneInt64`), `cmd/evener-hub/internal/launchconfig/wire.go:129,158` (`copyIntPtr`, `copyBoolPtr`), `llm/api_attempt.go:643`; canonical candidate `appwire/clone.go:125` (`clonePointer[T any]`, unexported). Also `server/appwire_runtime.go:965-982` (`cloneGoalState`, `cloneThreadAccess`, each the same three lines), `appwire/frame_recorder.go:94-107` (`recorderStateRoot`, "duplicated here only to keep appwire... free of a dependency") vs `cmdutil/statedir.go:26` (`DefaultStateRoot`) and eight more `XDGStateHome` consumers (`agent/runtime_dir.go:75`, `agent/doctor/statedir.go:29`, `auth/openai/storage.go:100`, `cmd/evener-hub/config.go:389`, `cmd/evener-migrate/main.go:134`, `cmdutil/legacymigration.go:88`, `rendezvous/rendezvous.go:53`).

Evidence: at least 12 near-identical `func clone<X>(v *T) *T { if v==nil {return nil}; c := *v; return &c }` with drifted names, plus the generic one already exists unexported.

Fix plan: export `appwire.ClonePointer[T]` (or put a generic in `invariant`/a tiny util package every layer imports; `appwire` is already imported by all of these except `llm`/`launchconfig`), delete the per-package copies, and replace `cloneGoalState`/`cloneThreadAccess`. For state root, move the XDG resolution to `envvars` (already imported by `appwire`) as one function and make `cmdutil.DefaultStateRoot` and the recorder call it. Size -90 lines. Risk: low.

---

### appwire-server-10: Protocol and server surface kept for compatibility or never implemented (medium; each item needs Jesse's call)

Locations and evidence:
- `appwire/client.go:496-523` `legacyProtocolVersion` parses a frozen English sentence from older servers' InvalidRequest to synthesize `ProtocolVersionMismatchError` ("Those binaries cannot change"). Same file: `Initialize` branch at 535.
- `appwire/paging.go:42-60` `isLegacyNumericCursor` rejects old numeric cursors (retired in v4).
- `appwire/protocol.go:170` `ThreadTurnItemsList` registered `ScopeUnimplemented`: constant (`types.go:80`), params/response types (`types.go:2431,2439`), client wrapper (`client.go:577`), catalog row, generated TS (`types.gen.ts:4478-4486,5161`), a `remote_hub_refs.go:620` case; no server handler on any router. `ScopeUnimplemented` exists solely for this one method.
- `appwire/errors.go:966` `QueuedDrainPartial` (error code -32013) has no producer; `cmd/evener-hub/frontend/src/stores/threads.ts:5143` and `Composer.tsx:1023` still reference `isQueuedDrainPartial`.
- `appwire/protocol.go:316` and `appwire/input.go:66` describe "flag-day v2" validation; `ForkFromTurn: false` literals in `server/server.go:524` and `appwire_runtime.go:2942`, `DirectoryComplete: false` (zero values stated explicitly).
- `appwire/protocol.go:362-370` comment documents an `NotifyEvenerContextPressure` constant "defined but emitted by nothing".

Fix plan: ask Jesse, per item, whether the old-binary tolerance still protects a real mixed-version case (daemons outlive hubs, `types.go` comment on `ProtocolVersion`): keep `legacyProtocolVersion` if yes, else delete. Delete `ThreadTurnItemsList` end to end (Go + `go generate` for TS/doc + hub case) and then `ScopeUnimplemented`; delete `QueuedDrainPartial` and the dead TS branch (separate PR in the frontend slice); delete `NotifyEvenerContextPressure` if truly unreferenced. Update `docs/appwire-protocol.md` via generate. Size -150 Go, -100 TS. Risk: low for the dead items, medium for the version shim.

---

### appwire-server-11: `appwire/status.go` has duplicate and dead helpers (low)

Location: `appwire/status.go:3-40`.

Evidence: `IsActiveItemStatus` and `IsActiveTurnStatus` are the same body (`status == TurnStatusInProgress`); `IsTerminalItemStatus` and `IsTerminalTurnStatus` are the same switch. `IsActiveThreadStatus`, `IsActiveTurnStatus`, `IsActiveItemStatus` and `IsTerminalItemStatus` have no non-test caller (external-reference scan; only `status_test.go`). Only `IsRestingThreadStatus` and `IsTerminalTurnStatus` are used outside the package.

Fix plan: delete the four unused helpers and `status_test.go` cases for them; keep the two used ones. Size -25. Risk: nil.

---

### appwire-server-12: `types.go` is a 5,898-line monolith; method constants and retry-safe tables are scattered and have drifted (medium)

Locations: `appwire/types.go` (365 types), constants also in `appwire/keybindings.go:12-17` and other files; `appwire/protocol.go:323-334` (`ValidateMutationParams` required-field map, rebuilt on every call), `appwire/errors.go:923-933` (`retrySafeMutationMethods`), `appwire-client/typescript/state/mutation/dispatcher.ts:313-323` (`RETRY_SAFE_MUTATION_METHODS`).

Evidence:
- `types.go` mixes core thread/turn/item types (lines 1-3300) with hub-only concerns: auth (`3347-3963`), launch config (`3812-4470`), provider instances (`4145`), plugins/marketplaces (`4469-4647`), host management (`4922-5898`, where a section header already says so at 5450 and 5691). 1,190 of its lines are comments. `protocol.go` says "Order matches the Method* constants in types.go" but some constants live in other files.
- Two hand-maintained method lists disagree: `ValidateMutationParams` requires `clientMutationId`+`expectedInstanceId` for 10 methods including `urls/remove`, while `retrySafeMutationMethods` (documented as "each carries a clientMutationId") lists 9 and omits `urls/remove`; a third copy lives in TS and a test pins only the Go/TS pair.
- Param shape drift for the same identity triple: `TurnQueueParams.Ref string json:"ref"` vs `TurnSteerParams.Ref ... omitempty` plus `ThreadID` (types.go:2615 vs 2636).

Fix plan: (1) mechanical split of `types.go` by domain into `types_thread.go`, `types_auth.go`, `types_launch.go`, `types_plugins.go`, `types_host.go` (no content change; `go generate` output must be byte-identical, which proves it); (2) move all `Method*`/`Notify*` constants next to their catalog rows or keep in one `methods.go`; (3) derive `ValidateMutationParams`' requirement table and `retrySafeMutationMethods` from one `MutationSpec` list (`{Method, RequiresExpectedEntryID, RequiresQueueRevision, BackpressureRefusable}`), hoisted to package scope, and decide explicitly whether `urls/remove` is backpressure-refusable; extend the existing Go/TS pin test to the derived set. Docs: regenerate. Size: +/-0 for the split (pure move), -40 for the table. Risk: low for the split, low-medium for the table.

---

### appwire-server-13: `handleAppThreadClear` is a 165-line, nine-exit lock dance, and its journal reimplements atomic durable write (medium)

Locations: `server/appwire_runtime.go:2013-2176`; `server/thread_clear_journal.go:84-165`.

Evidence: eight manual `s.mu.Unlock()` before returns, two Lock/Unlock re-entries in the crash-recovery branch (2078-2098), three snapshots of `clearRecords` (`beforeReservation`, `reserved`, current). `persistThreadClearJournal` hand-rolls tmp-file, write, fsync, chmod, rename, directory fsync with 11 error wraps; `journalSyncUnsupported` (thread_clear_journal.go:155) is the sixth copy of the ENOSYS/ENOTSUP/EINVAL predicate (others: `auth/openai/storage.go:229`, `agent/session_client_mutation_persist.go:423`, `agent/internal/worktree/sync_unix.go:36`, `cmd/evener-hub/app_host_manage.go:1054`, `cmd/evener-hub/internal/hubcore/state_file.go:127`, plus canonical `cmd/evener-hub/internal/fsdurability/fsdurability.go:20`). 15 non-test `os.Rename(tmp, ...)` sites exist repo-wide. `fsdurability` lives under `cmd/evener-hub/internal`, so `server` cannot import it, which is why each package copied it.

Fix plan: (1) move `fsdurability` to a root-level shared package and add one `WriteFileAtomic(path, data, perm)` doing the full durable idiom; migrate `thread_clear_journal.go` first (it deletes ~55 lines) and open follow-ups for the other five predicate copies; (2) split `handleAppThreadClear` into `admitClear` (validation + replay/recovery decision returning a tagged result), `reserveClear`, `finishClear`, each taking and releasing `s.mu` itself, so no function has more than two unlock paths. Tests: existing `thread_clear_journal_test.go`, `appwire_clear_test.go`, `appwire_mutation_recovery_test.go` are the guard; add a fault-injection test for the directory-sync tolerance on the shared helper. Size -120 net. Risk: medium (clear is the identity-transition contract).

---

### appwire-server-14: Four near-identical `stamp*OnStatusChange` functions with 150 lines of comments around ~40 lines of code (low)

Location: `server/appwire_runtime.go:1036-1176` (`stampFailureCountOnStatusChange`, `stampAskPendingOnStatusChange`, `stampCapabilitiesOnStatusChange`) and `557-576` (`stampActiveTurnOnStatusChange`), called back to back at 547-550.

Evidence: each begins with the same `method != NotifyThreadStatusChanged` check and `params.(ThreadStatusChangedParams)` assertion, then sets one field. Each takes `s.mu` separately (so four lock round-trips per root status frame) via helpers `envelopeFailedToolCalls`, `envelopeAskPending`, `appCapabilities`.

Fix plan: one `stampRootStatus(method, params) any` that does the assertion once, reads the envelope/flags under a single RLock, and sets all four fields; keep the policy comments, trimmed. Guard with `appwire_failure_push_*`, `appwire_askpending_*`, `appwire_capabilities_push_test.go`. Size -50. Risk: low.

---

### appwire-server-15: `appThreadWithDiagnosticsLocked` copies ~40 envelope fields through locals; `appCapabilitiesLocked` carries ~60 lines of history in comments (low)

Location: `server/appwire_runtime.go:2477-2588`, `2891-2979`.

Evidence: `pressure := envelope.ContextPressure`, `queue := envelope.Queue`, ... 30 single-use local aliases (2493-2527) assigned straight into the `appwire.EvenerThread` literal; the comment on `Interrupt` cites kata 5gdv/vewa and describes bugs of earlier versions. `ForkFromTurn: false` is stated explicitly.

Fix plan: build the literal from `envelope.*` directly; trim comments to the invariant (capabilities derive from the published status and from the wired hooks) and leave history to `git log`. Size -45 lines. Risk: nil.

---

### appwire-server-16: History-narrating and issue-number comments are pervasive (low, doc-drift)

Evidence: `appwire_runtime.go` is 3,074 lines of which 697 are `//` lines (23%); `appwire/types.go` 1,190 of 5,898 (20%). Examples: `ProtocolVersion` doc (`types.go:13-35`) is a v2-to-v7 changelog; `bridge.go:93` "the recommendation that used to close this comment"; `appwire_runtime.go:2460` "It used to pull sixteen live session callbacks"; `appwire_runtime.go:2999` "They used to be separate expressions"; `client.go:354` "pre-round-nine behavior"; 23 `kata <id>` references in non-test code; `appwire/client.go:491` "(kata zedg)". The user rule is explicit: comments about how something used to work belong in the commit message.

Fix plan: a comment-only pass per file (no code change): keep contract/invariant sentences, drop "used to", round numbers, PR/kata ids, and the version changelog (move the per-version notes into `docs/appwire-protocol.md`'s generator header if wanted). Do it while touching each region for findings 1-15 rather than as a separate PR to avoid churn. Size -600 comment lines. Risk: nil.

---

### appwire-server-17: Test debt in the slice: coverage-driven files, wrapper-only tests, a few copy assertions (low-medium)

Locations: `appwire/cov_rhub_appwire_test.go` (969), `appwire/client_gaps2_test.go`, `appwire/protocol_gaps_test.go`, `appwire/types_covtest_test.go`, `appwire/jsonrpc_coverage_test.go`, `appwire/registered_coverage_*_test.go`, `server/cov_rhub_server_test.go`, `server/server_gaps_test.go`, `server/appwire_runtime_gaps_test.go`; copy assertions at `server/appwire_server_test.go:653` (`Message != "session is closed"`), `server/appwire_skillinput_test.go:303`.

Evidence: `TestClientRequestWrappersRoundTrip` exists because the wrappers (finding 3) have no real caller; `server/cov_rhub_server_test.go` `TestAppStatus` only restates the status switch (finding 5). File names describe the coverage goal ("gaps", "cov_rhub", "covtest"), not the behavior. AGENTS.md forbids asserting on human-readable strings.

Fix plan: as findings 3 and 5 land, delete the wrapper/status tests; fold the rest into behavior-named files (`client_request_test.go`, `status_test.go`); change the two copy assertions to assert the wire error code and `Data.EvenerErrorInfo`. Size: -700 test lines. Risk: low (check coverage gates in `coverage_covstmt_audit_test.go` before deleting).

---

## Recommended fix sequence

1. Quick deletions with compile-checked safety, one PR: findings 11 (status helpers), 4 (`StatusInfo` dead fields), 5 (dead reservation, `appStatus`, `inputFromItems`), 15 (locals/comments in `appThread...`).
2. Finding 3 (dead Client wrappers, `PendingCoordinator`) together with its test cleanup from 17; ask Jesse first about the single-owner-per-method rule.
3. Finding 10 after Jesse decides each compat item; `ThreadTurnItemsList` and `QueuedDrainPartial` deletions are regenerate-and-delete.
4. Finding 9 (shared `ClonePointer`, state-root) so later PRs use one helper.
5. Finding 2 (single `agent.DetailedStatus` -> wire converter; fixes the `LatestIntent` drop red-test-first). This is the highest-value structural change and unblocks trimming `server.go`.
6. Finding 1 (generic retry-safe handler + input gate), then 14 (status stampers) and 7 (hook generic + activity handler + `serveServer`), all in `appwire_runtime.go`.
7. Finding 6 (shared projection fold for root and descendant), then 8 (retire legacy envelope sampling) once 1 and 6 reduce the surface; 8 needs the emitter audit and Jesse's approval.
8. Finding 13 (clear handler split + shared `WriteFileAtomic`/`fsdurability` move); follow-ups for the other five predicate copies belong to the owning slices.
9. Finding 12 (split `types.go`, derive mutation tables) as a pure move PR verified by byte-identical `go generate`.
10. Finding 16 comment trimming rides along with each PR above, not separately.
