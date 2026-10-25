# Audit: x-go-ts-protocol (Go/TypeScript protocol mirrors, wire-struct duplication)

Worktree: `.worktrees/debt-audit` at origin/main (a6c0b9c561). Read-only audit; no tests run.

## Overview and inventory

The Go to TypeScript boundary is generated, but only partly. `internal/appwirets` (emit.go, 673 lines)
reflects over `appwire.Methods`, `appwire.Notifications` and a few hubapi roots and writes
`appwire-client/typescript/types.gen.ts` (5338 lines, 438 `export`s, 421 interfaces, 8 `as const`
name catalogs). It is drift-tested (`TestGeneratedFileCurrent`, emit_test.go:751). That part is healthy:
a name-collision scan found essentially no hand-written TS interface that re-declares a generated one,
and most hand-written wire types are `Pick`/`Omit`/`MethodTypes[...]` derivations.

What the generator does not cover is everything that is a *value* rather than a *shape*:

| Boundary item | Go | Emitted to TS? |
|---|---|---|
| Struct shapes | appwire/types.go (5898 lines, 365 types), hubapi | yes |
| Method and notification names | `appwire.Methods` | yes (`METHOD_NAMES`) |
| 5 enums (session scope/resource/watch state, steering kind, thread-item event kind) + navigation kinds | typed consts | yes, each hand-wired in emit.go:304-313 and 634-639 |
| ~20 other string-enum types (TaskStatus, TaskType, TranscriptLevel, TranscriptHookExitDetail, TranscriptViewportClass, TranscriptContentKind, MutationOutcome, RetryDisposition, HostMutationOutcome, OperationState, OverlayKind, MutationProjectionState, MutationDisposition, DelegateStopOutcome, TurnItemsView, ArchiveTargetKind, NavigationRepresentation, ...) | typed consts | no, fields render as `string` |
| `ErrorInfo` discriminators | 45 consts, appwire/errors.go:19-240 | no |
| JSON-RPC error codes | appwire/errors.go:10-16 | no |
| `ErrorData`-family payloads | errors.go:518-... | no (hand mirror `MutationErrorData`) |
| `ThreadItem.type`, `ThreadStatus.type`, turn status | untyped string consts (types.go:367-380) or bare literals | no |
| Protocol version, size limits, shipped defaults | consts | no, retyped by hand |

Inventory of the TS side: `appwire-client/typescript` has 119 non-test `.ts` files in the package root
(50,275 non-test lines overall including subdirectories). appwire Go package: 106 files, 12,204 non-test lines.

Go-side duplication is concentrated in one pipeline: `agent` (status.go) to `server` (server.go) to
`appwire`, with `cmd/evener/serve.go`, `cmd/evener-hub/app_threadread.go` and
`internal/appprojector` each carrying a field-by-field converter.

Where the repo already knows about this, it has patched drift with parsing tests
(`TestRetrySafeMutationMethodsMatchSharedDispatcher`, `TestHubFailureKeywordsMatchWebClient`) rather than
generating. That is the pattern findings 1, 2, 10 and 11 would retire.

Reviewed and deliberately left alone (so nobody re-litigates them):

- `agent/schema/turn.go` `TurnFailureCause` vs `agent/events` `ErrorCause` vs `appwire.DiagnosticCause`
  (four identical fields each). The schema doc comment (turn.go:378-382) says the persisted shape must
  evolve independently of the event payload. Leave it.
- `agent/doctor/mutations.go` `clientMutationStore*` structs (mirror unexported runtime types, strict decode
  documented at mutations.go:146-153). Leave it.
- Keybindings: Go validates structure only and the action registry lives in TS by design
  (appwire/keybindings.go:86-89). Not a mirror.
- `plugins.ListItem` to `appwire.PluginEntry` (`time.Time` vs epoch int64) is a legitimate conversion layer.

---

## Findings

### x-go-ts-protocol-1: ErrorInfo discriminators and JSON-RPC codes are hand-mirrored in three TS homes plus raw literals

Severity: high. Category: inconsistency (cross-cutting).

Locations:
- Go source of truth: appwire/errors.go:10-16 (7 `Code*` consts), errors.go:19-240 and following (45 `ErrorInfo` consts incl. `"stale-entry"`, `"cursor-too-large"`, `"host-busy-operation"`, `"connectionBackpressure"`).
- TS home A: appwire-client/typescript/errors.ts:30-32 (`CODE_INVALID_REQUEST`, `CODE_METHOD_NOT_FOUND`, `CODE_INVALID_PARAMS`, unexported), errors.ts:61,118,131,145,155,168,189,207,219 (9 exported `Error*` consts).
- TS home B: appwire-client/typescript/hostMutations.ts:24 (`ErrorStaleEntry = "stale-entry"`), re-exported through index.ts:284 and cmd/evener-hub/frontend/src/stores/hosts.ts:104.
- TS home C: cmd/evener-hub/frontend/src/stores/hostOps.ts:49-66 (`HostOpRefusalKind`, 15 kinds, each a verbatim ErrorInfo string) and its switch at hostOps.ts:125-190.
- Second copy of a code: appwire-client/typescript/client.ts:82 (`const CODE_INVALID_REQUEST = -32600`, duplicate of errors.ts:30).
- Raw numeric codes: appwire-client/typescript/jobOutput.ts:125 (`-32014`), state/navigation/types.ts:9 (`NAVIGATION_UNAVAILABLE_CODE = -32014`), sessionActivityStore.ts:1503 (`-32600/-32601/-32602`), mobile-native/src/board/hubSeen.ts:26 (`INVALID_PARAMS = -32602`), mobile-native/src/dev/demoSetup.ts:48 (`CODE_CONFLICT = -32013`).
- Raw ErrorInfo literals in comparisons (non-test): `"conflict"` x4, `"actionUnavailable"` x4, `"sessionActivityCursorStale"` x2 (sessionActivityStore.ts:669,1504), `"resourceNotFound"` x2, `"hubLaunch"` x2, plus one each of `transcriptItemCursorStale`, `sessionUnavailable`, `methodNotFound`, `jobOutputPruned`, `invalidParams`, `internal`, `marketplaceUnregisteredCloneRemains` (grep of `evenerErrorInfo ===` across appwire-client, frontend, mobile-native).
- Payload mirror: errors.ts:441-448 `MutationOutcome`, `MutationRetryDisposition`, `MutationErrorData` re-declare Go `ErrorData` (errors.go:518-524); `ErrorData` is not in the catalog so it never reaches types.gen.ts.

Evidence: of 45 Go discriminators, 9 have an exported TS constant in errors.ts, 1 in hostMutations.ts, 15 appear only as string-union members in hostOps.ts, and at least 13 more are compared as bare literals. The same status code is written in 4 different spellings (`-32014` inline, `NAVIGATION_UNAVAILABLE_CODE`, `CODE_*` in two files, `CodeUnavailable`). Nothing links any of them to errors.go. A rename or a new discriminator in Go compiles everywhere and silently stops matching in TS. errors.ts:39 even checks `error.evenerErrorInfo === "methodNotFound"` literally beside a `CODE_METHOD_NOT_FOUND` constant.

Impact: every error-handling branch in the clients is an untyped string compare against a protocol value that has no TS source of truth. Adding a discriminator needs a grep, not a compiler.

Fix plan:
1. Extend appwirets to emit `export const ERROR_CODES = {...} as const` and `export const ERROR_INFO = {...} as const` (+ `ErrorInfoName`) from a Go table. Today the Go consts are loose; add `appwire.AllErrorInfos` / `AllErrorCodes` slices next to them (same pattern as `AllThreadItemEventKinds`, types.go ~2060) and a unit test that every `ErrorInfo` const is in the slice (reflect over the package source like `fieldDoc` already does, or a go/ast test).
2. Emit `ErrorData`, `MutationErrorData`-equivalent structs by adding `appwire.ErrorData` to the generator roots; delete errors.ts:441-448's hand declarations in favour of the generated ones.
3. Replace in TS: delete errors.ts `CODE_*` and the 9 `Error*` consts and hostMutations.ts:24 and re-export the generated names from errors.ts (keeps public import paths); change hostOps.ts `HostOpRefusalKind` to `Extract<ErrorInfoName, ...> | "invalid-params" | "unknown"`; replace raw literal compares with `ERROR_INFO.x`; replace `-3201x` literals with `ERROR_CODES.x`.
4. Add a lint-ish vitest (no copy assertions) that fails on `evenerErrorInfo ===` followed by a string literal in non-test sources, or rely on `ErrorInfoName` typing `WireError.evenerErrorInfo` (change errors.ts:16 from `string` to `ErrorInfoName | (string & {})`-free `ErrorInfoName`, accepting unknown values as `undefined` after a runtime check).
5. Docs: appwire-client/typescript/README.md (list the new generated exports) and the S05 row in docs/product/subsystems.md ("generated contracts" sentence gains "error vocabulary").

Size: ~120 lines added in emit.go plus tests; ~200 lines of TS edits across ~15 files (mostly deletions).
Risk: low to medium. Pure rename-level refactor in TS, guarded by tsc. Keep `index.ts` exports stable to avoid touching mobile-native imports. Preserve unknown-discriminator tolerance (a newer hub may send a value the client does not know).

---

### x-go-ts-protocol-2: Enum-typed Go fields render as `string`; the generator hand-wires 3 enums and TS hand-copies the rest

Severity: high. Category: duplication / architecture (codegen gap).

Locations:
- Generator special cases: internal/appwirets/emit.go:36-37 (`threadItemEventKindType`, `navigationTargetKindType`), emit.go:304-313 (`typeExpr` hard-codes `SessionActivityScope`, `SessionActivityResource`, `SessionWatchState`, `ThreadItemEventKind`, nav kinds), emit.go:346-352, emit.go:634-639 (five `writeNameCatalog` calls with hand-listed values, e.g. `[]string{string(appwire.SessionActivityScopeSession), ...}`).
- Un-emitted Go enums: appwire/task.go:6-27 (`TaskStatus`, `TaskType`), transcript_display.go:18-48, errors.go:241,504,512, types.go:549,1531,1874,2280,2325,2643,2651,5352,5455.
- Generated result: types.gen.ts:3751-3756 (`Task { type: string; status: string }`), 4572-4597 (`TranscriptDisplayAdvanced.hookExits: string`, `TranscriptDisplayContent { kind: string; level?: string }`).
- Hand TS unions: taskListData.ts:9 and :75 (`TaskStatus` union plus `KNOWN_STATUSES` set), transcriptDisplayConfig.ts:23-25 and :59-60 (`ContentLevel`, `HookExitDetail`, `TranscriptViewportClass`, `CONTENT_LEVELS`, `HOOK_EXIT_DETAILS`), errors.ts:331 (`HostMutationOutcome`), errors.ts:441-442, hostOps.ts:483 (`SETTLED_OPERATION_STATES = new Set(["complete","failed","interrupted"])`) and :592-609 (`OperationState` values), state/mutation/dispatcher.ts:187 (`"applied"`/`"replayed"`).

Evidence: `TaskStatus`'s four values exist as: appwire/task.go:9-16; agent/internal/tool/definitions.go:910 (tool JSON-schema enum); taskListData.ts:9; taskListData.ts:75; cmd/evener-hub/frontend/src/dev/taskcardmockups/fixture.ts:34. Five copies, no link. Measured: 25 string-enum types declared in appwire, 5 reach TS as typed catalogs, about 20 reach TS as `string`. The three special cases in `typeExpr` are exactly the shape a generic mechanism would handle; each new enum costs a new `case` plus a new hand-listed catalog.

Impact: every Go-side enum addition is a silent client gap. The TS side normally guards with runtime sets (`KNOWN_STATUSES`), which then also need updating, so drift shows up as dropped rows rather than build errors.

Fix plan:
1. Introduce one Go registry, e.g. `appwire.Enums = []EnumSpec{{Name:"TaskStatus", Values: AllTaskStatuses}, ...}` (or reuse the existing `AllX` slices), consumed by emit.go. `typeExpr` looks up `reflect.Type` in a map built from the registry instead of per-type `case`s; `writeNameCatalog` is driven by the same loop. Delete emit.go:36-37, 304-313, 346-352, 634-639 special cases (net deletion).
2. Emit string-literal union types for enum-typed fields (`status: TaskStatus`). Keep catalogs as `X_VALUES` consts for runtime membership checks.
3. Migrate the hand unions listed above to imports from types.gen (TaskStatus, ContentLevel, HookExitDetail, TranscriptViewportClass, MutationOutcome, RetryDisposition, HostMutationOutcome, OperationState); delete `KNOWN_STATUSES` and `CONTENT_LEVELS`/`HOOK_EXIT_DETAILS` in favour of the generated catalogs.
4. Make `agent/internal/tool/definitions.go:910` build its enum from `appwire.AllTaskStatuses`.
5. Tests: the existing drift test (`TestGeneratedFileCurrent`) covers regeneration; add one emit_test case that an enum type in the registry produces a union and a catalog. No behavior change, so no TDD red phase beyond the emitter test.
6. Docs: README and S05 row (same as finding 1).

Size: net negative in emit.go (~-40 / +60), ~-80 lines of TS hand unions, ~+150 generated lines.
Risk: medium. Widening `status: string` to a union can expose latent type errors in the frontend (that is the point); fix them in the same PR. Consumers parsing untrusted wire data must keep their runtime narrowing.

---

### x-go-ts-protocol-3: ThreadItem.type and thread/turn status values have no constants on either side

Severity: medium. Category: duplication.

Locations: appwire/types.go:2067-2068 (`ThreadItem.Type string`), types.go:367-380 (untyped `ThreadStatus*`/`TurnStatus*` consts), types.gen.ts:4044-4045, 4065; item_type_test.go:8-30 (pins only three or five type strings).

Evidence: literal counts for item types in non-test sources: `"agentMessage"` 11 Go occurrences in 6 files, 38 TS occurrences in 24 files; `"commandExecution"` 11/7 files Go, 45/23 files TS; `"systemMessage"` 12/5 Go, 25/14 TS; `"reasoning"` 29/17 Go, 38/19 TS; `"warning"` 16/8 Go, 107/43 TS. Status literals: `"failed"` 244 TS occurrences, `"idle"` 174, `"completed"` 144, `"active"` 108. `ThreadStatus*` consts exist in Go but are untyped (`const ThreadStatusIdle = "idle"`), so even Go callers cannot be checked, and TS has no mirror at all (`ThreadStatus` in types.gen.ts:4388 is the object `{type: string}`).

Impact: the discriminant that drives transcript rendering is a free-form string in 80+ files. A misspelling (`"agentMesage"`) compiles on both sides.

Fix plan: define `type ThreadItemType string` with consts for the closed set, plus `ThreadStatusType` and `TurnStatus` typed consts; register them with the enum registry from finding 2 so TS gets unions; tighten `ThreadItem.Type`/`Status` field types. In TS, narrow `item.type === "..."` comparisons by the union (tsc then flags typos). Do the Go rename mechanically with gopls rename or a sed on the typed consts; leave the high-count `"warning"` in TS for opportunistic cleanup because tsc will already protect it once the field is a union. Update docs/appwire-protocol.md with the closed list.

Size: ~60 Go lines of consts, ~200 mechanical call-site edits Go, TS edits only where tsc complains.
Risk: medium. The item `type` set may not actually be closed (mcpToolCall, dynamicToolCall appear in item_type_test.go:29); inventory first, and keep an explicit "other" escape if the set is open by design. If so, downgrade this to status values only.

---

### x-go-ts-protocol-4: The delegate read model is copied four times with five hand-written converters, and two converters already dropped a field

Severity: high. Category: duplication (drift confirmed).

Locations:
- Struct copies (all ~50 identical fields): agent/status.go:124-176 `DelegateStatusInfo`; server/server.go:105-163 `DelegateStatusInfo`; agent/events/payloads.go:748 `DelegateUpdatedData` (52 fields); appwire/types.go `EvenerDelegateInfo` (camelCase, plus `LatestIntent`, `LogicalOwnerSessionID`, report preview).
- Converters: cmd/evener/serve.go:2315-2336 (agent to server, 22 lines of positional copy); server/appwire_runtime.go:2827-2850 `appDelegateFromDetailedStatus`; cmd/evener-hub/app_threadread.go:1278-1306 `appwireDelegateFromAgentStatus`; agent/status.go:577-615 `delegateUpdatedDataFromStatus`; internal/appprojector/appwire_projection.go:466-530 `appwireDelegateInfo`.

Evidence: a pairwise json-tag Jaccard scan reports `server.DelegateStatusInfo` vs `agent.DelegateStatusInfo` at 0.98 (50 vs 51 tags) and vs `DelegateUpdatedData` at 0.96. The missing tag is `latest_intent`: agent/status.go:158 has `LatestIntent`, and status.go:517 and :588 populate/copy it, but server/server.go:105-163 does not declare it, and neither serve.go:2315-2336, appwire_runtime.go:2827-2850 nor app_threadread.go:1278-1306 copy it. `grep LatestIntent` shows it is only carried by agent/status.go, events/payloads.go:786 and appprojector:485. So on the live path (agent to server to appwire) and the past-session path (app_threadread) the field is silently dropped, while the projector path carries it. Whether a user-visible gap results depends on `appwire.SlimDelegateForRoster` and which path serves which client; I did not trace that, but the drift itself is the concrete proof that the copy is not maintained. Fields were evidently added in step with `Usage`/`Worktree`, which each converter also re-clones by hand (see finding 6).

Impact: every new delegate field requires editing 4 structs and 5 converters; a missed edit is silent. This is the highest-churn wire struct in the repo (50 fields).

Fix plan:
1. Make `agent.DelegateStatusInfo` the one in-process read model. Delete `server.DelegateStatusInfo` (use `agent.DelegateStatusInfo` directly in `server.DetailedStatus`; server already imports agent, see server.go `agent.WatchStatusInfo` at :183).
2. Delete the serve.go copy loop; assign `out.Delegates = ds.Delegates` (clone once with a single `Clone()` method on the type).
3. Add one `func (d DelegateStatusInfo) ToWire() appwire.EvenerDelegateInfo` (or a function in appwire taking an interface-free struct) and replace appDelegateFromDetailedStatus and appwireDelegateFromAgentStatus with it. Add `LatestIntent` there.
4. Decide whether `events.DelegateUpdatedData` stays a separate struct (it is a persisted/event payload, so likely yes) and keep `delegateUpdatedDataFromStatus` and `appwireDelegateInfo` as the two intentional conversions; add a reflection test that every exported field of `agent.DelegateStatusInfo` has a counterpart in `EvenerDelegateInfo` and `DelegateUpdatedData` (allow-list for intentional differences) so the next added field fails a test.
5. TDD: first add a failing test that a status with `LatestIntent` set round-trips through `agentToServerDetailedStatus` and `appDelegateFromDetailedStatus`; then make the deletion.
6. Docs: the S05/S10 rows in docs/product/subsystems.md only if ownership statements change; note the single read model in docs/product/session-activity.md.

Size: roughly -190 lines (server struct 60, serve.go converter 22, two converters ~45 each) +40 for `ToWire`/clone + test.
Risk: medium. Behavior change is intentional (LatestIntent now flows on two paths), so confirm with Jesse that it should; the roster slimming may still hide it from clients.

---

### x-go-ts-protocol-5: server.DetailedStatus is a field-for-field copy of agent.DetailedStatus

Severity: medium. Category: duplication.

Locations: server/server.go:50-203 (`ToolInfo`... `SkillDiagnosticInfo`), agent/status.go:32-100,183-210; cmd/evener/serve.go:2232-2345 `agentToServerDetailedStatus` (114 lines); mapping to wire at server/appwire_runtime.go:2590-2700.

Evidence: scan results at Jaccard 1.00: `server.MCPServerInfo` = `agent/mcpconfig.ServerInfo` = `appwire.EvenerMCPServerInfo` (4 tags each); `server.PluginStatusInfo` = `agent.PluginInfo` (6); `server.JobStatusInfo` = `agent.JobStatusInfo` (13); `server.TurnSlotStatus` = `agent.turnSlotOccupancy` (4); `server.SkillDiagnosticInfo` = `skill.Diagnostic` (6); `server.HookEventStatus` = `agent.HookEventStatus` = `appwire.EvenerHookEventStatus` (4; agent uses `plugin.HookEvent`, server uses `string`). `DetailedStatus` is 0.92 (12 vs 13 tags; server lacks `skill_catalog`). serve.go:2232-2345 copies each element by hand, and also `server/server.go:204-225` carries a custom `MarshalJSON` workaround for the plugins slice that exists only because of that mirror (comment: "old or unwired sources").

Impact: ~114 lines of copy plus 7 mirrored struct definitions that must be edited in lockstep. server already imports agent, so the dependency direction is not an obstacle.

Fix plan: delete the seven server mirror types and `agentToServerDetailedStatus`; have `server.DetailedStatus` be `type DetailedStatus = agent.DetailedStatus` (or have `thread_envelope.go:143` accept `agent.DetailedStatus` directly) and move the three small projections (skills catalog join at serve.go:2252-2268, plugin nil-vs-empty rule, hook event string cast) into `appDiagnosticsFromDetailedStatus` where the wire shaping already happens. Keep the nil-vs-empty plugin rule with a test (existing coverage of `MarshalJSON` is in server tests; keep it behavior-identical). Land after finding 4 so the delegate part is already gone.

Size: about -250 lines, +40.
Risk: medium. `cmd/evener` test helpers that construct `server.DetailedStatus` literals need updating; the nil/empty plugin distinction must be preserved exactly.

---

### x-go-ts-protocol-6: Eleven reinvented pointer-clone helpers

Severity: low. Category: duplication.

Locations: server/appwire_runtime.go:2858,2866 (`cloneServerBool/Int64`); cmd/evener/serve.go:2347,2355 (`cloneServeBool/Int64`), plus `cloneServeUsage` :2363 and `cloneServeWorktree` :2371; cmd/evener-hub/app_threadread.go:1309,1317 (`cloneHubBool/Int64`); internal/appprojector/appwire_projection.go:573,581 (`cloneBoolPointer/cloneInt64Pointer`); agent/status.go:569 (`cloneBool`); agent/jobs_activity.go:1293 (`cloneInt64`); llm/api_attempt.go:643 (`cloneAPILogInt`); cmd/evener-hub/internal/launchconfig/wire.go:129,158 (`copyIntPtr`, `copyBoolPtr`). Generic versions already exist 5 times: appwire/clone.go:125 `clonePointer[T]`, llm/registry/load.go:622, agent/internal/jobstore/event_clone.go:32 `clonePtr`, cmd/evener-hub/navigation_projection.go:1887, cmd/evener-hub/internal/appsource/remote_hub_probe.go:278 `ptrClone`.

Evidence: all bodies are the same four lines (`if v == nil {return nil}; c := *v; return &c`). 14 type-specific plus 5 generic = 19 definitions of one operation.

Impact: each new converter picks a package-local copy; pure noise.

Fix plan: export one `appwire.ClonePointer[T]` (appwire/clone.go:125 is already the canonical one), delete the 14 typed helpers and the 4 other generic copies, and use it in the callers. Many call sites disappear anyway under findings 4 and 5, so do this last. Repo rule: do not touch the llm and jobstore copies unless the import direction allows (llm cannot import appwire; leave those two).

Size: about -120 lines.
Risk: low.

---

### x-go-ts-protocol-7: Exact-duplicate wire structs in hubapi, hostops and appwire, justified by a stale comment

Severity: medium. Category: duplication / doc-drift.

Locations:
- hubapi/types.go:30-45 `AttentionSummary` (doc: "hubapi ... deliberately does not import appwire") vs appwire/attention.go:31-35; converter cmd/evener-hub/web_api_tree.go:1123-1125 `hubAttentionSummaryFromCore`.
- hubapi/navigation.go:179-183 `NavigationSubagentTally` vs appwire/types.go:1302-1306 `SubagentTally`; converter cmd/evener-hub/navigation_projection.go:1753-1756.
- hubapi/navigation.go:7 imports `primeradiant.com/evener/appwire` and :322 aliases `NavigationMutation = appwire.NavigationMutation`.
- cmd/evener-hub/internal/hostops/boundary.go:34-45 `Boundary` vs appwire/types.go:5324-5328 `HostBoundary` (converted at cmd/evener-hub/app_host_ops.go:765,798).
- internal/plugins/resolve.go (`PluginSelectionError`) vs appwire/types.go `PluginSelectionError` (identical 2 fields).

Evidence: the hubapi comment's premise is false; the same package already imports appwire. Both `AttentionSummary` and `NavigationSubagentTally` appear as separate identical interfaces in types.gen.ts (:137 and :2727/:3745).

Impact: duplicate generated TS interfaces, hand converters, and a comment that teaches the next author the wrong rule.

Fix plan: `type AttentionSummary = appwire.AttentionSummary` and `type NavigationSubagentTally = appwire.SubagentTally` in hubapi (aliases keep hubapi's callers intact; the generator then needs to dedupe aliases, which it likely does by reflect type name, so check emit output); delete the two converters; fix the comment; for `Boundary`, store `appwire.HostBoundary` in hostops if hostops may import appwire (check import graph first) else leave. Update docs only if the S05 row mentions hubapi independence (it does not).

Size: -40 lines, -2 generated TS interfaces.
Risk: low. Run `make generate` and confirm the generated diff is only the removed duplicate interfaces.

---

### x-go-ts-protocol-8: Transcript-display config is implemented twice (Go validators and a 624-line TS mirror) with its own hardcoded defaults

Severity: medium. Category: duplication.

Locations: appwire/transcript_display.go:18-48 (enums), :152-168 (`TranscriptDisplayShippedDefaults`: desktop `tools`, mobile `intent`), :170-225 (validation); appwire-client/typescript/transcriptDisplayConfig.ts:23-25,59-60 (enums), :62-68 (per-level content vectors), :80-87,226-243 (`ADVANCED_DEFAULTS`, `shippedDesktopConfig`/`shippedMobileConfig` using `tools` and `intent`), :180-220 (`normalizeConfig` re-validating version/levels/hook exits); Go version const `transcriptDisplayConfigVersion = 1` (transcript_display.go:150) vs TS `version: 1` literals.

Evidence: the same facts (five level names, three hook-exit values, version 1, shipped desktop=tools and mobile=intent, advanced all-false with `hookExits: "none"`) appear in both files with no cross-check; nothing in the Go tests reads the TS (grep of `_test.go` files that read `appwire-client` found only dispatcher.ts, types.gen.ts and turnFailure.ts). The preset-to-vector table (TS :62-68) is TS-only, which is fine, but the shipped defaults are protocol facts: `GET evener/settings/transcriptDisplay/get` already returns them from the hub (`TranscriptDisplayDefaults`), so the TS copy is only the pre-connect fallback.

Impact: changing a shipped default requires two edits; users on a not-yet-connected client see the stale one. Also the TS "V1" mirror interfaces (`TranscriptDisplayAdvancedV1`, `TranscriptDisplayConfigV1`, `HubTranscriptDisplayDefault`) shadow generated `WireAdvanced/WireConfig/WireDefault` imported at transcriptDisplayConfig.ts:1-7.

Fix plan: after finding 2 (enums generated), derive `ContentLevel`/`HookExitDetail`/`TranscriptViewportClass` and the V1 types from generated ones; emit shipped defaults as a generated constant (`TRANSCRIPT_DISPLAY_SHIPPED_DEFAULTS`) from `appwire.TranscriptDisplayShippedDefaults()` and have `shippedDesktopConfig/MobileConfig` read it. Keep the vector table and the client-side normalization (it protects against untrusted stored local config). Docs: docs/product/ transcript display section if present.

Size: ~-60 TS lines, +30 emitter lines.
Risk: low to medium; local-storage config parsing must remain strict.

---

### x-go-ts-protocol-9: Alias and duplicate-constant clutter in appwire

Severity: low. Category: dead-code / slop.

Locations: appwire/keybindings.go:51,58,60,62 (`KeybindingsGetResponse`, `KeybindingsPatchResponse`, `KeybindingsChangedParams`, `KeybindingsChangedNotification` all `= KeybindingsOverrides`); appwire/transcript_display.go:91,107,121,129 (`TranscriptDisplayConfigV1`, `...GetResponse`, `...DefaultsPatchResponse`, `...ChangedNotification`); transcript_display.go:115-127 (`TranscriptDisplayChangedParams` is field-for-field the same as `TranscriptDisplayPatchResponse`, 3 identical fields); transcript_display.go:23-47 duplicate const names (`TranscriptViewportDesktop` = `TranscriptViewportClassDesktop`, `TranscriptHookExitNone` = `TranscriptHookExitDetailNone`, `TranscriptContentPreset` = `TranscriptContentKindPreset`).

Evidence: zero uses outside the definitions for `KeybindingsGetResponse`, `KeybindingsChangedNotification`, `TranscriptDisplayConfigV1`, `TranscriptDisplayGetResponse`, `TranscriptDisplayChangedNotification` (`grep -w` over `*.go`); `TranscriptHookExitNone` has 1 use (the alias definition), `TranscriptContentPreset` 1, while the long names are used 6 and 8 times. The comments admit the purpose ("kept as an operation-oriented name for callers", "so later versions can introduce a distinct Go type"), which is speculative generality.

Impact: ~20 lines of indirection plus a confusing second name for each constant; two structurally identical wire structs the catalog registers separately.

Fix plan: delete the five unused aliases and the three duplicate const names (rename their one use each); keep `KeybindingsPatchResponse` (10 uses) and `TranscriptDisplayDefaultsPatchResponse` (11 uses) only if the catalog names matter, otherwise rename uses to the underlying type in one mechanical pass. Merge `TranscriptDisplayChangedParams` and `TranscriptDisplayPatchResponse` into one type (check the generated TS names used by frontend: `TranscriptDisplayChangedParams` appears at types.gen.ts:4581). Regenerate; the generated TS loses duplicate interfaces. No backward-compat shims. Needs Jesse's nod only for the catalog-name renames since they change TS export names.

Size: -40 lines Go.
Risk: low.

---

### x-go-ts-protocol-10: Protocol version, size limits and shared literals are retyped across the boundary with no pin

Severity: medium. Category: duplication.

Locations:
- Protocol version: appwire/types.go:35 `ProtocolVersion = "evener-appwire-v7"`; appwire-client/typescript/client.ts:49 `APPWIRE_PROTOCOL_VERSION = "evener-appwire-v7"`; appwire/flagday_contract_test.go:10; appwire-client/typescript/scripts/qualify-package.mjs:1148; mobile-native/src/dev/demoSessions.ts:1210 (inside prose); scripts/ops/deploy-hub-endpoint-test.sh:180.
- Attachment limits: appwire-client/typescript/attachmentLimits.ts:9-10 (`MAX_ATTACHMENTS = 8`, `MAX_ATTACHMENT_BYTES = 8 MiB`) vs cmd/evener-hub/internal/hubcore/types.go:6-7 (`SendMaxImageItems = 8`, `SendMaxImageBytes = 8 MiB`).
- Doc size: docContent.ts:51 `DOC_FILE_MAX_BYTES = 512 * 1024` vs cmd/evener-hub/doc_serve.go:36 `docFileMaxBytes = 512 * 1024`.
- Intent length: sessionActivity.ts:17 `MAX_INTENT_CODE_POINTS = 200` vs appwire/excerpt.go:29 `MaxIntentRunes = 200`.
- Navigation page limits: state/navigation/types.ts:70-71 (50, 100) vs `maxNavigationSectionRows`/`maxNavigationCatalogRows` in cmd/evener-hub/navigation_projection.go (the TS comment at :62-69 names the Go constants by hand).

Evidence: qualify-package.mjs is the only cross-language check on the version, and no test relates any limit pair. The navigation comment is itself evidence that someone knew these must agree.

Impact: bumping the protocol or a cap is a multi-file, multi-language edit with a documented-by-comment sync obligation; a miss yields rejected sends or wrong page keys.

Fix plan: emit a `PROTOCOL_CONSTANTS` block from the generator (`PROTOCOL_VERSION`, `MAX_INTENT_RUNES`, send image items/bytes, nav page limits, doc max bytes) sourced from exported Go consts (move the three hub-private consts into appwire where the TS-visible ones live; hubcore's `Send*` already is a protocol limit). Replace TS literals with imports; qualify-package.mjs reads the generated constant instead of a string. Leave tests that pin Go `ProtocolVersion` literal (flagday test is a deliberate gate).

Size: +40 emitter, -15 TS, ~10 Go moves.
Risk: low. Watch the import cycle: hubcore must import the constants from appwire, not vice versa.

---

### x-go-ts-protocol-11: Go tests regex-scrape TypeScript sources to keep mirrors in sync, and one mirror duplicates a decision

Severity: medium. Category: architecture / test-debt.

Locations: appwire/errors_test.go:274-300 `TestRetrySafeMutationMethodsMatchSharedDispatcher` (regex over state/mutation/dispatcher.ts:313-323) against appwire/errors.go:923-933 `retrySafeMutationMethods`; cmd/evener-hub/diagnostic_keyword_parity_test.go:15-80 `TestHubFailureKeywordsMatchWebClient` (regex over frontend/src/panes/session/transcript/turnFailure.ts:26-38 `RECONNECT_KEYWORDS`) against `diagnostic.HubFailureKeywords`.

Evidence: both tests carry long comments about why they exist; the keyword test says outright that the list "had drifted, in both directions", and that the TS file is "a second implementation of the same decision" (diagnostic_keyword_parity_test.go:15-19), classifying a message string client-side that Go already classified when it stamped source/title/hint. The retry-safe set is nine method names, a trivially generatable catalog (the generator already emits `METHOD_NAMES` and `HOST_REQUEST_METHODS`). The keyword test's own rationale for not generating ("a generator would cost a command, a go:generate directive...") no longer holds: the generator, drift test and `make generate` exist.

Impact: parity enforced by parsing source text breaks on any reformat of the TS array and tests the regex as much as the contract. The keyword mirror also means a hub failure can be classified differently by hub and client when messages change.

Fix plan:
1. Retry-safe set: emit `RETRY_SAFE_MUTATION_METHODS` from `appwire.retrySafeMutationMethods` (export an `AllRetrySafeMutationMethods` slice, sorted) via `writeNameCatalog`; `dispatcher.ts` imports it; delete the regex test (the generated-file drift test now covers it). Net -30 test lines.
2. Hub-failure classification: have Go stamp a boolean/source value on the wire error cause (it already computes source/title/hint) that says "reconnect-class", and have `turnFailure.ts` read it instead of matching `RECONNECT_KEYWORDS`; then delete the keyword array, `RECONNECT_RE`, and `TestHubFailureKeywordsMatchWebClient`/`...AreMatchable`. This is a small product-visible change (recovery affordance for old transcripts that lack the stamp), so discuss with Jesse first; the interim option is to leave the keyword test as is.
3. Docs: S05/S11 row notes on retry classification ("the hub owns forwarding authorization and retry classification" already says so).

Size: -90 test/TS lines, +25 emitter/Go.
Risk: step 1 low; step 2 medium (needs a decision on legacy rows).

---

### x-go-ts-protocol-12: Launch schema declares LaunchOption twice and copies it field by field; effort vocabulary repeated in five Go places

Severity: low. Category: duplication.

Locations: cmd/evener-hub/internal/launchconfig/schema.go:60-105 (`LaunchOption` with typed `Group/Kind/PathKind/DefaultableLayers`) vs appwire/types.go:3823-3842 (same 18 fields as strings); converter cmd/evener-hub/app_launch.go:70-105 (36 lines). Reasoning-effort set: llm/types.go:688 `ReasoningEffortVocabulary`, cmdutil/cmdutil.go:133 (inline `case "minimal","low",...`), llm/registry/schema.go:69 (documented as intentional, pinned by registry_shape_test), launchconfig/schema.go:162 (`reasoningChoices` with labels), envvars/envvars.go:80 (prose).

Evidence: the schema.go:55-63 comment already records the lesson ("so the schema here and the evener/launch/schema response cannot drift: a field added in appwire arrives on both sides at once instead of being dropped by a field-by-field copy") but stops at the two sub-types; the 18-field parent is still copied (app_launch.go:77-95), and a field added to one without the other would be dropped.

Fix plan: make the typed enums string-based type aliases at the appwire boundary (`appwire.LaunchOption` with `Group LaunchGroup`) or give `launchconfig.LaunchOption` an embedded `appwire.LaunchOption` plus the typed accessors it needs; delete the copy loop. For effort, change cmdutil.go:133 to use `llm.ValidateReasoningEffort`/`ReasoningEffortVocabulary` and build `reasoningChoices` from it; leave registry/schema.go (intentional, pinned).

Size: -45 lines.
Risk: low; the launch resolver tests exercise both types.

---

### x-go-ts-protocol-13: A defined, client-stubbed protocol method that no router serves

Severity: low. Category: dead-code.

Locations: appwire/protocol.go:170 (`MethodThreadTurnItemsList`, `ScopeUnimplemented`), appwire/types.go:80,2431-2445, appwire/client.go:577-581, cmd/evener-hub/internal/appsource/remote_hub_refs.go:620, types.gen.ts:4478-4490 and :5161.

Evidence: the catalog entry's own text: "Experimental even in Codex (returns method-not-supported) and served by no evener router." It is the only `ScopeUnimplemented` entry; non-test grep for the method name finds no TS consumer and no router registration.

Impact: generated types, a Go client method, a ref-rewriting case and a scope value (`ScopeUnimplemented`, protocol.go:24-26) exist for a method nobody serves.

Fix plan: delete the method, its param/response types, the Go client wrapper, the `remote_hub_refs.go` case, and the now-unused `ScopeUnimplemented` scope; regenerate. Confirm no test references (tests may list it as an unimplemented example; update). Docs: docs/appwire-protocol.md method table.

Size: about -60 lines.
Risk: low. This is removal of unreachable surface, not compat; confirm with Jesse because it is Codex-parity API surface.

---

### x-go-ts-protocol-14: appwire/types.go is a 5,898-line, 365-type monolith

Severity: low. Category: complexity.

Locations: appwire/types.go (largest non-test file in the package; next is errors.go at 1058 lines), with only four section dividers (`grep "^// ----"` = 4). It mixes thread/turn/item wire, launch config, auth/credentials, plugins/marketplaces, navigation mutations, host registry/deploy pipeline (lines ~5260-5900), and 137 `Method*` constants (types.go has 137 of the 148 method consts in the package).

Evidence: nearby domain files already exist (keybindings.go, transcript_display.go, task.go, session_activity.go, attention.go), so the pattern of one domain per file is established but not applied to the bulk.

Impact: review and conflict cost; the types_test.go (1158 lines) mirrors the monolith. Generator output ordering is alphabetical (emit.go sort), so splitting files does not change generated output; `fieldDoc` parses the package directory, so doc extraction is unaffected.

Fix plan: mechanical split by domain (thread/turn/item, host pipeline, plugins/marketplace, launch/auth, navigation, methods) with `gofmt` and no content changes; verify `make generate` produces a byte-identical types.gen.ts. Do this after findings 2-4 so the diff of moved code stays pure.

Size: 0 net lines (moves only).
Risk: low (verify via empty diff on generated file and `go build ./...`).

---

### x-go-ts-protocol-15: `connectFakeClient` is re-declared in 89 test files

Severity: medium. Category: duplication (test helper).

Locations: `grep -rln "function connectFakeClient"` across cmd/evener-hub/frontend/src, mobile-native/src and appwire-client finds 89 files, for example stores/threads.escalations.test.ts:33, stores/threads.sessionActions.test.ts:260, panes/session/transcript/messages/testing/heldSteerTestUtils.ts:68. The shared fake lives in appwire-client/typescript/testing/fakeClient.ts.

Evidence: 73 of the declarations are the same 4-line body (`const fake = new FakeClient("ready"); connectionStore.getState().connect(fake); return fake;`); 11 take a `state` parameter; 7 additionally register a `thread/unsubscribe` handler.

Impact: any change to how a test wires a fake to the connection store (for example moving off the singleton `connectionStore`) means 89 edits.

Fix plan: add `connectFakeClient(state?, { handlers? })` to the frontend's shared test utilities (the existing `testing` folder, not the published package, because it touches the frontend's `connectionStore`), plus the equivalent for mobile-native's store, and replace the local copies with an import by codemod. The `/testing/` specifier rules in AGENTS.md allow it in test files only.

Size: about -400 lines.
Risk: low; mechanical, tsc and vitest guard it. Overlaps whichever audit slice owns frontend tests; coordinate to avoid double work.

---

## Recommended fix sequence

1. Finding 4 (delegate model), then 5 (DetailedStatus mirror), then 6 (clone helpers). This is the only place where drift has already dropped a field, and the deletions shrink the surface for everything else. Start with the failing `LatestIntent` round-trip test.
2. Findings 7 and 9 (hubapi/appwire exact duplicates, alias clutter). Small, mechanical, and they remove duplicate generated TS interfaces before the generator work.
3. Finding 2 (generic enum registry in appwirets), then 3 (item/status types) and 8 (transcript-display mirror), since all three consume it.
4. Finding 1 (error vocabulary generation) using the same registry mechanism; do the TS migration in one PR with tsc as the guard.
5. Finding 10 (shared constants) and finding 11 step 1 (generate the retry-safe set). Both are small additions to the same emitter.
6. Finding 11 step 2 (server-stamped reconnect class) and finding 12 only after discussing the product consequence with Jesse.
7. Findings 13, 14, 15 whenever convenient; 14 should come after the Go type work so moved code is stable.

Documentation to update alongside: the S05 row in docs/product/subsystems.md (generated contract scope), docs/appwire-protocol.md (error vocabulary, item types, constants), appwire-client/typescript/README.md (new generated exports), and docs/developing-evener/ if there is a codegen how-to describing what `make generate` covers.
