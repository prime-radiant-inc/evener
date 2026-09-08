# Packaged SDK notification observations — 7 September 2026

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
