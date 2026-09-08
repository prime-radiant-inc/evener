# Packaged SDK notification observations — 7 September 2026

For current native implementation and release status, see the [project status](status.md)
and [acceptance ledger](acceptance.md). The audited union below includes **34 of
36 notification names** across separately identified packages and fixtures.
The dated observations remain scoped evidence rather than a release decision.

Source `58d1b079f` adds session, work, streaming and hub observation recipes.
Together with existing recipes, the cookbook handles 36 notification names and
90 of 91 method names across 34 recipes. The remaining method is reserved and
unsupported. These counts describe cookbook presence, not complete producer or
release acceptance.

## Artifact and deterministic checks

The [receipt](assets/sdk-notifications-receipt.json) records the retained tarball,
consumer, source hashes, fixture identity, private result hashes and cleanup.
Root independently compared all 18 new helper/recipe files with commit
`58d1b079f` and the installed package. The tarball SHA-256 is
`2a22e65e84da4fa2466a5406800f7b141ee710d4f4e4f73588fbf4873e73e731`.

All 49 new contracts passed. The external consumer passed 321 tests across 31
contract modules. Each actual CLI also returned a generic error and exit 1 when
required connection configuration was absent. The package's source-only
qualification runner is invoked from the repository; it is intentionally absent
from the distributed tarball. The canonical gate and separate vet run passed at SDK source `58d1b079f`;
the package gate also passed after removing the isolated install residue.
Their logs are recorded in the acceptance ledger.

## Actual owned-hub outcomes

The fixture uses an immutable Evener binary pair with the reviewed project
deletion fix and an isolated scripted OpenAI-compatible provider. All Evener
RPC, event routing, storage and readback paths are real. Each observer and the
mutation author use separate direct SDK connections; no frames are injected,
dropped or proxied.

- Session rename and restoration each emitted the expected event and matched
  both the recipe snapshot and an independent client's readback.
- Vision routing changed to `off`, then cleared to the empty setting. Both
  events were observed; the empty reset remained valid and was read back.
- Mobile transcript timing preference changed and was restored, using the
  current revision for each patch. Events and independent canonical settings
  agreed.
- The `palette.open` keybinding was explicitly unbound with `chord: null` and
  restored. Both canonical settings notifications and independent reads agreed.
- Clearing an already absent goal emitted `evener/goal/updated` with `goal:null`.
  The final recipe snapshot and independent thread read showed no active goal.
  This case did not create a goal or qualify goal continuation.
- Two real rename events with retention limited to one produced `uncertain`,
  `overflow:true`, an event count of two and one retained event. The final name
  was restored and independently read.
- All four installed CLI entry points completed quiet observations with exit 0,
  metadata-only stdout and mode-0600 private result files containing initial and
  final snapshots. Quiet observations qualify execution and readback, not event
  production.

The six retained helper sources describe setup, the external provider boundary,
session/settings qualification, actual CLI execution, overflow and cleanup.
Their embedded owned paths must be adapted to reproduce a fresh fixture.

## Cleanup and limits

The owned session was shut down, the hub/provider exited, their listeners were
absent, and the fixture bearer token and private hub log were removed. No owned
daemon remained. The original hub on port 54211, all seven native drafts and the
unrelated Apple patch were separately reverified and preserved.

This qualifies five actual notification names in this fixture, plus the stated
bounded observation and CLI behavior. Other producer outcomes, continuous
reconnect/replay, atomic multi-resource snapshots, public provider behavior,
native UI, iPad, physical devices, accessibility and signed release acceptance
remain separate requirements.

## Actual task completion and tool output

The [task receipt](assets/sdk-task-notifications-receipt.json) records a fresh
owned hub built at `d2d5eedf9`, using the unchanged installed SDK at `58d1b079f`.
The external provider authored `task_list` add, a matching update to done, and
`communicate` completion. The driver obtained the task ID from the actual SDK
`evener/tasks/list` response and passed it through the external provider's
private control endpoint; it did not parse human-readable tool output.

Separate work and streaming recipe clients observed the same authored turn.
The work recipe retained two `evener/task/updated` events, from zero done to one
done. The streaming recipe retained two `item/toolOutput/delta` events and the
automatic `evener/steering/injected` completion reminder. Both recipes returned
`read`; independent task and transcript reads confirmed the same task was done
and the matching turn completed. All nine private result files were checked for
mode0600 and content hashes. These add three actual notification names to the
five qualified above; user-authored steering is a separate requirement.

The preceding run completed the task but exposed `images:null` in the automatic
steering event, violating the generated optional-array contract. Its strict SDK
observer rejected the payload and that run remains unqualified. The producer
fix omits absent images; its nil/empty serialization regression failed before
the fix and passed afterward. The repeated actual run above observed the
corrected payload. No compatibility fallback was added to the SDK.

The new session was shut down through the SDK; the owned hub/provider exited,
their listeners and bearer token were absent, and private hub logs were removed.
Raw results and provider metadata remain available for review. This scenario
does not qualify jobs, delegates, reasoning, retry, user steering, native UI or
continuous reconnect/replay.

## Actual shell job and stable delegate

The [job/delegate receipt](assets/sdk-job-delegate-notifications-receipt.json)
records two more independent owned fixtures using the same installed SDK and
backend binary pair as the task run. Root checked the binary hashes, private
result hashes, actual event identities and authoritative readbacks after the
Luna workers completed the runs. Helper snapshots were hashed after each run;
the receipt distinguishes that evidence from captured runtime inputs.

The shell case observed `evener/job/started`, `evener/jobs/treeUpdated` and
`evener/job/finished`. Its work observer returned `read`; the matching canonical
shell row was terminal and completed with exit code zero and 32 output bytes.
The fixture did not independently read output contents. A preceding failed
fixture assumed the optional origin-turn field was present and rejected a
normal follow-up provider request; it remains unqualified. The successful
fixture correlated by the owned session, authored command and returned job ID.

The delegate case observed nine `evener/delegate/updated` notifications, with a
stable identity progressing from running to an idle terminal report. The work
observer returned `read` and its canonical activity tree contained the same
delegate. The returned child transcript reference yielded the authored result;
both the matching parent completion event and an independent thread read
confirmed the parent turn completed. The external provider held the parent's
next response until the child's response was delivered. Delegate follow-up,
stop/resume and failure recovery were not exercised.

Both fixtures stopped their owned hub/provider and descendant daemon processes;
root independently rechecked the recorded hub/provider PIDs and listeners and
removed any retained private hub log. Bearer tokens were absent. Raw private
evidence was retained. Across this document's fixtures, twelve distinct
notification names have actual producer/readback evidence, with the specific
scope and limitations stated above. This is not full notification or release
acceptance.

## Actual retry and reasoning notifications

The [retry receipt](assets/sdk-retry-notification-receipt.json) records a
strengthened disposable-provider run. Qualification request 3 returned HTTP
503 and request 4 returned HTTP 200. The SDK observer received one
`evener/thread/modelRetry` event with `attempt: 1`, `statusCode: 503`, and the
same `turn_m2` identity that the final independent read reported as completed.
The run returned `read` with no overflow or connection interruption. Its exact
fixture source and drivers are retained as helper snapshots; raw
provider bodies, reads, events and provider status records remain private.

The [reasoning receipt](assets/sdk-reasoning-notification-receipt.json) records
the same controlled retry boundary with a streamed reasoning delta. The SDK
observer received `item/reasoning/summaryTextDelta` for `turn_m2`, item
`item_reasoning_28`, summary index 0, alongside the retry event. The provider's
raw SSE response and the AppWire event carry the same opaque sentinel. The
final read independently reports `turn_m2` completed. The fixture's private
hub log was removed after process/listener checks; raw provider/session evidence
remains private.

These runs add two distinct producer names to the twelve previously qualified
names, bringing this document's actual producer/readback evidence to fourteen.
The retry name appearing in both receipts is one notification name, not two.
They qualify one transient retry and one reasoning-delta path only; warning
producers, broader retry policy and continuous reconnect/replay remain open.

## Actual interruption without a warning

The [cancellation receipt](assets/sdk-cancellation-receipt.json) records an
applied `turn/start`, an applied/reflected `turn/interrupt`, and a final
`turn_m2` status of `interrupted` on the same session. The external HTTP
provider observed cancellation of its held request. The packaged observer
finished with `read` and received zero warning events.

The original runner expected a warning and failed that assertion; that failure
is retained. Source inspection explains the outcome: the `modelErrorCancel`
branch in `agent/session_model_call.go` settles the interrupted round and
returns without emitting `EventError` or `EventWarning`. Root independently
checked the actual interruption receipts, final status, provider cancellation
and unchanged fixture-source hashes. This qualifies one interruption outcome
and leaves warning producer coverage open; the distinct-name count stays 14.

## Actual content-filter warning

The [content-filter warning receipt](assets/sdk-contentfilter-warning-receipt.json)
records a fresh owned direct-v4 hub run using the immutable `d2d5eedf9` binary
pair and packaged SDK `58d1b079f` (139 tarball files compared; SHA-256
`2a22e65e84da4fa2466a5406800f7b141ee710d4f4e4f73588fbf4873e73e731`). A real
scripted OpenAI-compatible provider returned request 3 as HTTP 503, request 4
as HTTP 400 with `error.code=invalid_prompt`, and request 5 as HTTP 200. The
actual SDK observer returned `read` and received one `warning` notification
inside the started/completed window for `turn_m2`, with source `provider`, title
`Provider error`, and message `Content filter hit — compacting context and
retrying`. An independent final read reported the same turn completed. The
warning payload has no `turnId` field; correlation is by the owned session and
event window.

This qualifies `warning` as the fifteenth distinct notification name with actual
producer/readback evidence. The first cleanup pass left owned daemon PID 10532
on port 60886; that daemon was subsequently terminated and a second proof found
no owned processes or listeners, with the bearer token and private hub log
removed. The fixture credentials file was verified empty and mode 0600, then removed. This remains a
single warning-producer qualification and does not qualify the broader warning
surface, reconnect/replay, native UI, devices, accessibility, or release
acceptance.

## Turn and item lifecycle producer evidence

The [additional producer receipt](sdk-additional-producers-evidence.md) qualifies
six names through actual status transitions, turn lifecycle, and a correlated
agent item start/delta/completion. The evidence series then covered 21 distinct
notification names. The delta is correlated by wire item ID; stable transcript
key and position are verified on started/completed items and canonical readback.
This is scoped producer evidence, with remaining outcomes and recovery open.

## Queue and thread-close producer evidence

The [queue/close receipt](assets/sdk-queue-close-receipt.json) adds two distinct
producer names to the dated union: `thread/queueChanged` was observed after a
real `turn/queue` acknowledgment and canonical queued-entry readback, and the
current-source run observed the exact target `thread/closed` after shutdown.
The close run used backend source `3284d6ac5`, retained 28 raw events, and
confirmed the final canonical thread status was `awaiting`; the earlier failed
close attempts remain historical and are not counted. That run brought the union to
27 of 36 notification names with scoped producer/readback evidence. This
reconciles the earlier 21-name summary with the separate real navigation
invalidation receipt, the two model-setting producer/readback names, and the
credential update receipt, which were omitted from that historical subtotal.

The [navigation receipt](assets/sdk-navigation-receipt.json),
[settings receipt](assets/sdk-settings-receipt.json) and
[credential receipt](assets/sdk-credentials-receipt.json) supply those additional
names. Each records real mutations, observed notifications and authoritative
readback; their package and backend identities remain distinct. The four names
outside the current audited union need consolidated producer evidence or fresh
qualification; this is not a claim that every historical source was retested.

The [8 September launch-layer receipt](assets/2026-09-08-sdk-launch-producer.json) adds `evener/launch/updated`: two real project-layer events, authoritative `maxRounds: 7` readback and restoration. Its provider failed before a successful turn; the scope is this producer only. No `thread/started` notification was captured. This brought the union to 28 of 36 distinct names.

The subsequent [marketplace/plugin receipt](assets/2026-09-08-sdk-plugin-marketplace-producers.json) adds `evener/marketplace/updated` and `evener/plugin/updated` through two independently installed AppwireClient instances. Seven owned mutations produced the matching empty notification payloads and authoritative catalog readbacks. The coordinator independently checked the retained payloads and state. An earlier raw-WebSocket attempt is excluded from SDK qualification. Manifest-only installation does not establish plugin execution.

The [sandbox receipt](assets/2026-09-08-sdk-sandbox-producers.json) adds `evener/sandbox/escalation/requested` and `evener/sandbox/escalation/resolved`. Both Allow and Deny cases retain exact session/escalation identity, resolved events and completed-turn readbacks. The coordinator verified the persisted successful/denied `write_file` results, allowed file content and denied target absence. The allow target was reused across attempts, so fresh creation is not claimed. Each resolve used one driver request invocation; wire dispatch counts were not instrumented. All owned attempt processes and bearer tokens were removed, including leftovers found during coordinator review. That batch brought the audited union to **32 of 36** names.

The [message-reset receipt](assets/2026-09-08-sdk-message-reset-producer.json) adds `item/agentMessage/reset`. A scripted provider interrupted its first partial response. The installed SDK observed the exact stale item delta, retry, matching reset, replacement delta and completed target turn; its authoritative readback contained the replacement and excluded stale text. The coordinator verified raw event order, hashes and cleanup across all three attempts. This does not qualify a continuously reconnecting reducer or native rendering.

The [attention receipt](assets/2026-09-08-sdk-attention-producer.json) adds `evener/attention/changed`. A held turn produced an idle-to-working transition for the exact started session. The subsequent authoritative navigation manifest summary matched the event: needsYou=2, error=0, working=1, compared with working=0 before. The live count moved from 2 to 3. The intermediate invalidation is retained without treating its revision as the final snapshot. The coordinator verified target identity, raw capture, binary/package hashes and fixture cleanup. The audited union is now **34 of 36** names.

The audited receipt union is:

`evener/delegate/updated`, `evener/goal/updated`,
`evener/job/finished`, `evener/job/started`,
`evener/jobs/treeUpdated`, `evener/navigation/invalidated`, `evener/launch/updated`,
`evener/settings/keybindings/changed`,
`evener/settings/transcriptDisplay/changed`, `evener/steering/injected`,
`evener/task/updated`, `evener/thread/modelRetry`, `evener/auth/updated`,
`evener/thread/name/changed`, `item/agentMessage/delta`, `item/completed`,
`item/reasoning/summaryTextDelta`, `item/started`, `item/toolOutput/delta`,
`thread/model/changed`, `thread/reasoning-effort/changed`,
`thread/queueChanged`, `thread/status/changed`,
`thread/vision-model/changed`, `turn/completed`, `turn/started`,
`thread/closed`, `warning`, `evener/marketplace/updated`,
`evener/plugin/updated`, `evener/sandbox/escalation/requested`,
`evener/sandbox/escalation/resolved`, `item/agentMessage/reset`, and
`evener/attention/changed`.

This union is producer/readback evidence for the cited bounded fixtures. It does
not include producer/readback qualification for the other two catalog names: `thread/started` and
`evener/thread/resync`. It also does not qualify every outcome or ordering path, continuous
reconnect/replay, native UI, or release acceptance.
