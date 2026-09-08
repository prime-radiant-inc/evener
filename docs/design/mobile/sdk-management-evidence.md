# SDK management recipe evidence

## Scope and artifact

The provider-instance and plugin-management examples extend the packaged
`@evener/appwire-client` without changing its transport or generated contracts.
The continuation starts from `93e817cd3`. Luna medium agents supplied source
proposals and reviewed the integrated algorithms; their filesystem views were
unreliable, so root integrated the files and executed all checks locally in the
authoritative worktree.

The tarball installed in a separate consumer for the observations below has
SHA-256 `cf291967ba53461145a5d594632860a6f4a42ba267e6698f9c63290337195dc2`.
It was installed with scripts disabled and offline package resolution. The
package qualification gate separately packed and installed an artifact outside
the repository and ran its declaration, runtime-import and example contracts.

## Executed checks

- `make test-api-package`: passed, including ESM/CommonJS declarations and
  runtime imports, packed contents and installed example contract tests.
- All five `examples/*.contract.mjs` files: 52 tests passed. The 28 new checks
  exercise all six plugin and four instance mutation actions, exact targets,
  omitted/empty/false values, preflight refusal, malformed responses, lost
  replies, matching readback without an acknowledgment, and ordered error
  preservation. These use a scripted SDK boundary and issue no network calls.
- `make test-web`: typecheck, tests and Biome passed. Touched source files were
  formatted with Biome; `git diff --check` passed.
- Both installed CLI examples ran in their default read-only mode against the
  direct owned v4 hub on port 54211. Plugin count was zero; instance count was
  two and registry writes were allowed. Output contained counts/status only.
- The installed provider logic created `sdk-management-fixture-20260907` from
  the `ollama` base, edited its endpoint to a deliberately unused loopback
  address, set it as default, restored the original `fake` default, cleared the
  endpoint override, and removed the fixture. Each step received an
  acknowledgment and passed an independent list assertion. The final complete
  instance list equaled the initial list. No inference request was issued.

The catalog report lists 10 recipes containing 36 of 91 request names and three
notification names. This measures recipe presence, not live execution of every
listed request, branch/outcome coverage or release completion.

## Limits and next work

Actual plugin download/installation/upgrade and provider connectivity were not
exercised. The test hub had no installed plugin, so plugin mutation acceptance
still needs a disposable marketplace fixture. Credential flows, marketplace
management, decisions, hub upgrades and additional recovery recipes remain open.

The examples issue one mutation and one immediate readback attempt. A socket
loss can also prevent that read; they do not wait through a reconnect loop or
automatically repeat mutations. After an incomplete operation the caller must
read current state and prepare a deliberate next action. Readback does not
prove which writer made a change. These contracts have no mutation ID or
revision precondition; preflight checks are not atomic with writes.

No native source changed in this increment. Reader diagnosis and qualification
are recorded separately in [reader evidence](reader-continuity-evidence.md).
The full iOS release matrix and final canonical merge gate remain open.
Android source is preserved and qualification is deferred
for iOS-only v1. Neither recipe was published, and no branch was pushed or merged.

## Marketplace and sandbox approval recipes

The continuation from `92bfcbf5d` adds marketplace list/browse/add/refresh/remove
and sandbox approval read/resolve examples. Both use the existing one-mutation,
one-readback recovery helper and separate owned-endpoint confirmation. Approval
resolution captures the reviewed card before connecting, checks its full current
contents and instance identity, preserves explicit denial, and rejects a replaced
instance on readback. It always reports execution as unverified. The server
resolve contract lacks an expected-instance or mutation-ID precondition; the
client check cannot close the race between preflight and dispatch.

The final tarball used below has SHA-256
`707c7f1ff6798aff1ce3f1f1fcf901737499a620fef96d1c30f60fa51c92bca5`.
It was packed from the authoritative worktree and installed outside the checkout
with offline resolution and install scripts disabled. The final package gate
also installed and ran the artifact independently. Luna medium agents supplied
proposals and reviewed the algorithms; root corrected and integrated them and
ran the checks because the agents could not access the current filesystem.

- All seven example contract files pass: 81 tests, including 29 new cases.
  These exercise every action, source kinds, omitted/empty/false values, full
  approval-card equality, caller changes while connection awaits, stale bindings,
  malformed replies, one dispatch without replay, and both-error preservation
  including thrown `undefined` and `null`.
- The initial checks failed while the implementation modules were absent.
  Live alias browsing then exposed a wrong assumption in the first implementation:
  Browse returns the manifest catalog name, which can differ from its registered
  alias. The added alias regression failed before the correction and passed
  afterward. The request still uses the exact registered alias; the response
  preserves the manifest name. This matches `hubPluginsController.Browse`.
- Final `make test-api-package` and `make test-web` passed. The latter reports
  typecheck, tests and Biome passing; touched source formatting and diff checks
  pass. No generated contract or native source changed.
- The installed final CLI recipes read the direct owned v4 hub on port 54211:
  marketplace count two after cleanup, and zero pending sandbox approvals on
  the retained reader fixture. Output contained only counts/outcomes.
- The final installed marketplace helper read the alias left by the first live
  attempt, successfully browsed it, deliberately removed it, and added it again.
  `sdk-marketplace-acceptance` returned catalog name `native-git-acceptance` and
  one `native-tools` entry. Refresh fetched a changed local Git commit and its
  v4 content sentinel. Both owned marketplace registrations were then removed;
  the complete original two-entry marketplace list was restored exactly.

The earlier plugin-management tarball also installed the owned Git plugin,
upgraded it to a new revision, disabled/enabled it, toggled automatic upgrades
on/off and removed it. Its direct native notification and Git-failure recovery
acceptance is recorded in [plugin evidence](plugins-evidence.md#direct-v4-git-upgrade-and-recovery).
The local Git fixtures contain manifests and sentinel text only. Seeded
marketplaces were not browsed or refreshed and no production runtime was used.

The recipe catalog now names 12 examples containing 42 of 91 request names and
three notification names. These are presence counts, not executed-path or
release coverage. Actual sandbox approval/denial with tool execution still needs
an owned scripted-provider acceptance fixture; injected notifications are not
proof of resumption. Questions, credentials, continuous reconnect handling,
remaining SDK recipes and the full iOS release matrix remain open.

## Real sandbox decision qualification

The same final installed artifact subsequently completed two actual restricted
`read_file` decisions on the direct owned hub: approve true and approve false.
Each helper call dispatched one resolve request and returned acknowledged with
execution still unverified. Independent provider observations, completed-turn
events and full transcript reads proved the approved sentinel reached the model
and the denied sentinel did not. Native Allow/Deny completed the same two paths
in separate sessions. The temporary provider was removed and its complete
original registry restored after all five fixture sessions, including one
diagnostic attempt, were shut down. See [decision evidence](approval-evidence.md#direct-v4-execution-through-native-and-packaged-sdk-decisions)
for artifact identities, clean receipts, screenshots and remaining failure
scenarios. This closes the ordinary blocked-file execution gap above, not
concurrent decisions, lost acknowledgments or the broader iOS release matrix.

## Structured question recipe and real completion

The SDK now exposes the shared answer formatter and a guarded question recipe.
Readonly review pages through the latest user boundary and retains the complete
pending call set. Answer mode validates explicit selections, the reviewed batch
and session instance, submits one `turn/start`, checks the receipt and performs
authoritative readback without replay. Fourteen contract cases cover paging,
malformed questions, stale decisions, selection validation and uncertain results.
The total is 95 passing contract tests; external tarball and frontend gates pass.

The external tarball with SHA256
`c8a99dbf37e8c45cc1982294f2f11f48a527d2a4495da2eb11cf05810773c292`
completed a real two-question scripted-provider workflow on the owned direct
v4 hub. The helper reported acknowledged/execution-unverified; independent
provider, turn-completion and transcript checks proved one structured answer
with multiple selections and a note. See [current question evidence](real-question-harness-evidence.md#direct-v4-questions-restart-and-keyboard-qualification)
for native restart/keyboard evidence, the corrected diagnostic assertion and
cleanup. The catalog names 13 recipes and still 42/91 methods, with three
notification names: this is recipe presence, not complete support or acceptance.

## Packaged session settings and command catalog — 7 September

Source `cd975b991` adds reviewed model, reasoning-effort and vision-model setters
and a read-only command catalog recipe. The outside-checkout tarball SHA256 is
`3bed5ef3942c79a2915ac647ab632b95b517fd6065bdbe43447cbb7ce1e9cace`.
Its independent consumer ran against the direct owned v4 hub, using the completed
owned native question session. No provider turn or slash command was executed
by these SDK checks.

Seven setter requests were acknowledged exactly once each: reasoning high,
reasoning none, reset to default, vision off, reset to the session model, switch
to another configured provider and restore the original provider. Authoritative
readback checked high/none/off values, omitted reset fields and full restoration.
A stale reviewed settings object was rejected before another dispatch. The
observer also received all seven matching setting-change notifications. These
are observed by this acceptance driver; the recipes themselves do not own
subscriptions, so the cookbook notification-presence count remains three.

The command recipe read the hub's actual `commands: null` response as an empty
catalog. An exclusively created command file in the owned hub's XDG config then
produced one complete descriptor with source, description and argument hint.
The file was removed after checking its unchanged contents, and a final read was
empty again. The initial array-only proposal failed a regression for the real
nil-slice response before correction. The command was never dispatched.

[Structured proof](assets/sdk-settings-receipt.json) includes the package and
[driver](assets/sdk-settings-driver.mjs.txt) hashes, mutation counts, notifications
and cleanup assertions. Four command and twelve settings contracts pass, with
157 packaged contracts total. Outside-checkout TypeScript/ESM/CommonJS package
qualification, the canonical frontend gate, all five browser guards and the
repository secret scan pass. The cookbook now contains twenty recipes covering
54 of 91 catalog method names and three notification names; these counts measure
recipe presence, not complete outcome coverage.

The setters have no atomic revision or instance precondition. A valid RPC
acknowledgment remains distinct from proof that an exact requested value is
still current: the daemon can clamp it and concurrent writers can replace it.
The recipe returns actual readback without replaying. Native lost replies,
continuous reconnect, broader provider behavior and release acceptance remain
open. All prior native drafts and the new ordinary draft remain unchanged.


## Packaged stored credentials — 7 September

The independent `0.1.0` tarball with SHA256
`6cef0b2cdcaca667be9aa096b6b7178f1fe7e86ef71b370606369c84dc0a5521`
ran all six credential recipe methods against a separate authenticated loopback
hub built from `3c5feb442`. The hub used a private HOME and XDG roots, four owned
provider instances and fixture credential values. No provider inference, token
exchange or `evener/auth/test` was requested.

The driver verified stored-key set/clear, stale-review rejection before dispatch,
absent and environment-only logout, stored-key logout revealing the environment
credential, valid Google credential JSON, server rejection of incomplete JSON,
and clearing stored JSON while preserving an ADC file. OAuth instances reject
the API-key recipe before dispatch. A second independently connected client
checked each mutation's authoritative status and observed all eleven successful
credential update notifications. The packaged CLI performed the additional key
clear and emitted only the documented summary fields.

[Structured proof](assets/sdk-credentials-receipt.json) records the source, hub
binary, tarball and [driver](assets/sdk-credentials-driver.mjs.txt) hashes,
checks, request-count scope and cleanup. The first driver used the wrong
notification callback signature; its state checks passed, but its zero
notification count is excluded from notification acceptance. The corrected
driver uses `onNotification(callback)` and completed all 21 checks. All four
owned instances, stored entries and the fixture ADC file were removed; the
owned hub exited zero. The original hub, seven native drafts and unrelated
Apple patch remained intact.

Twelve recipe contracts cover required reviewed snapshots, real thrown request
failures, uncertain acknowledgments, readback failures, malformed/wrong-provider
responses, null/invalid lists and CLI privacy. The complete packaged contract
suite passed 169 tests across seventeen files. Fresh outside-checkout package
qualification passed ESM/CommonJS runtime imports and strict TypeScript. The
cookbook now contains 21 recipes covering 60 of 91 method names; its notification
count remains three because this acceptance driver owns the auth observer.

A status snapshot has no secret fingerprint or instance-definition revision and
cannot exclude concurrent writes. The shared recipe conservatively reports a
server-rejected mutation as uncertain when readback succeeds; the malformed JSON
case proves unchanged state without calling it acknowledged. No mutation is
automatically replayed. Provider execution remains unverified. Browser/device
OAuth recipes, disconnect/restart outcomes, full streaming recovery and native
iOS release qualification remain open.

## Packaged device and browser OAuth — 7 September

The OAuth recipe and shared credential-status decoder are recorded with this
entry. The outside-checkout package has SHA-256
`06b68ee1a7e2a318553158ada72e985fb908f5f1fb686c5682acaacc8ef849fc`.
It requires reviewed status for starts, captures flow ownership before connecting,
performs a single operation per call and preserves uncertainty after thrown or
malformed replies. Browser/device authorization receives one current status
readback; acknowledgment and current state remain separate. CLI starts reserve
private output before connection, and failed operations do not overwrite or
remove a pre-existing destination. The README documents explicit polling,
private flow/redirect files, fallback and readback outcomes.

The [receipt](assets/sdk-oauth-receipt.json) and
[driver](assets/sdk-oauth-driver.mjs.txt) record eight passing scenarios against
real registered hub handlers and temporary credential storage: pending device
poll, device authorization/readback, fresh-flow expiry, failed poll followed by
an explicit same-flow retry, browser fallback/completion, cross-instance handle
rejection, and lost replies during device and browser exchange. The external
OAuth boundary is scripted; this does not qualify a real provider account.

For each lost reply, a real SDK client issued its mutation while the fixture
held the external token exchange. The driver closed that client's connection,
released the exchange, observed the real auth notification on a second connected
client and read configured OAuth status there. The original recipe retained both
write and readback errors in an AggregateError. Delegating request counters assert
one mutation and one attempted status read, with no replay. There is no WebSocket
proxy or frame-drop wrapper. The device case begins signed out; the browser case
follows a verified normal completion and logout.

The driver cleared both owned provider credentials and verified signed-out state.
The harness then exited zero, its port closed and its temporary state was removed
by Go test cleanup. The recipe catalog now lists 22 examples containing 64 of 91
request names and three notification names. These are recipe-presence counts;
auth notifications observed by this acceptance driver are not claimed as another
notification recipe. Provider execution, publication, broader reconnect recovery
and the full mobile acceptance matrix remain open.

Validation: the canonical run passed lint, build, full Go/module tests and the
web gate, then caught type errors in new native test doubles. After correcting
those doubles, a fresh `make test-native` passed 663 tests in 73 files and strict
TypeScript, and `make test-api-package` passed outside-checkout installation,
ESM/CommonJS imports, declarations and all eighteen packaged contract files.
`make vet` and the focused exchange-gate race check also passed. This records
the successful individual phases; the earlier interrupted canonical invocation
is not reported as an exit-zero run. Independent Luna evidence review found no
remaining overclaim in this section or the native build/launch record.

## Packaged steering and composer drain — 7 September

Source `5429db421` adds `runQueue({ action: "steer" })` and optional text
input when draining a reviewed nonempty queue. The 13 queue contracts and
`make test-api-package` passed, including an outside-checkout installation.
The [receipt](assets/sdk-steer-receipt.json) identifies the independently
installed tarball:
`28a8559aced5d7847371bd71c8bf3ec2bd6c7c84aec9de08297f6dd1747839dc`.
Bot verified that its README, queue logic/contracts and coverage map match the
integrated source byte for byte.

A Luna medium tester ran the [driver](assets/sdk-steer-driver.mjs.txt) against
a fresh isolated real hub and [scripted model service](assets/sdk-steer-provider.go.txt).
Direct steering produced an applied receipt identifying its turn and no queue
entry IDs. The driver then queued two messages and drained them with authored
composer text. The drain receipt identified exactly the two reviewed queue IDs;
readback represented an empty queue. The service's
[request observations](assets/sdk-steer-provider-observations.json) show the
authored text reaching the actual model request. Those observations are boolean
presence checks, not counts within a request.

The tester released the provider through a real `communicate` completion and
separately read an awaiting thread with no active turn. Bot independently read
the persisted transcript, checked both mutation IDs against their receipts, and
counted the direct steer marker once and both drain markers once in the combined
steering entry. The receipt preserves those extracted entries and transcript
hash. The driver has one helper invocation per action; real wire dispatch
counters were not instrumented. Deterministic contracts establish the recipe's
single-dispatch/no-replay behavior, not a universal server exactly-once guarantee.

Root and the tester verified that the owned hub/provider ports and reported
processes were gone and the fixture token and continuation secret were removed.
No proxy or live model service was used. This closes the bounded SDK steering
recipe qualification; native steering UI, live model behavior, broader recovery,
the remaining 26 catalog method names without recipes (including one reserved
unsupported entry), notification coverage and publication remain separate work.

## SDK discovery and compaction command receipts — 7 September

Two disposable authenticated loopback runs exercised the packaged SDK against
the pre-`bb044658` SDK-only hub build (`759e7b10f-dirty`). The shared
`@evener/appwire-client` tarball was `87040c69db1f8e18ac69968862b32d69334306feb12fc9ec694971e090e55189`.
The source recipe commit recorded by both receipts was `bb044658d6ff28ab9d79d63a715d0663a7bdd659`.
The discovery receipt records binary SHA-256 values for `evener`
(`e9be7727a57cf626d5df1e6e41626d89484687097a98c07305efbc28c2e3852e`),
`evener-dev`
(`6bba94ff16e4542f90971c993a7bf2be1a3b2591df05fec509c16fa291afff4e`),
and `fakellm`
(`56002c555bdcc4e99d67e4466c246ad3fe962424ae508caa20c62ba58164226a`).
The [discovery receipt](assets/sdk-runtime-discovery-receipt.json) records SDK
connect, thread start, discovery reads, session rename/clear/compact/shutdown
calls, and independent post-shutdown rename readback. Its archived
[driver](assets/sdk-runtime-discovery-driver.mjs.txt) only reproduces connect
and `thread/start`; the operation driver for the recorded discovery calls was
not present in the disposable fixture and is therefore not archived. The fake
provider received zero requests: this session ended immediately after
`thread/start`, so its compact acknowledgment was an empty no-op and is not
compaction qualification.

The [compaction command receipt](assets/sdk-runtime-compaction-receipt.json)
records a follow-up run with scripted-provider activity before the compact
command. The direct and shared session-management recipes returned exit 0 and
an acknowledged result, with recorded `thread/read` projection counts changing
from three to four. Those counts do not establish completed compaction.

Root's independent audit decoded the exact API request bodies. The three
successful fake-provider calls comprised a structured session-name request
(response schema property `name`) followed by two tool-capable turn rounds,
with tool counts 0, 26 and 26. The previous worker attributed the first request
to summarization incorrectly. The persisted transcript contains environment,
user, assistant and tool-result entries; no completed-compaction result is
established by these artifacts. This receipt qualifies command acknowledgment
only. Actual compaction completion remains open and needs a new observed run.

The archived receipt records the original operation driver hash
`72f6f4de03e4ac34cf655ea1025e5d81449d02b88ec6f7566f25a5089463f7cc`, recipe
driver hash `2661ec87d5917f8bf115ab1ad2419038f22c1b3fccc2e7ccecd1385caa9cac0c`,
and fake-provider source hashes
`7703e61e6f0b29c94dc65ad2498fe38b2fc999ac76d6112e1ffa31c61ac5d310` and
`fdd8bb8822f42330e018676ab549d99ee55ce86e193f070500e0ad4e4acee408`.
The archived drivers replace fixture-specific prompt text with an environment
input placeholder; the receipt hashes identify the exact original repros.

For independent verification of the recorded compaction semantics, the
original run retained the API-attempt summary at
`/tmp/evener-sdk-compaction-9rqir8/run/api-summary.json`, the direct result at
`/tmp/evener-sdk-compaction-9rqir8/run/compaction-result.json`, and the recipe
result at `/tmp/evener-sdk-compaction-9rqir8/run/recipe-compaction-result.json`.
The direct session's API log and transcript were
`/tmp/evener-sdk-compaction-9rqir8/state/evener/projects/private-tmp-evener-sdk-compaction-9rqir8-workspace-paq7aBVPPc/sessions/034KzccqZf2GgCxfFTUffi.api.jsonl`
and the corresponding `.transcript.jsonl`; the recipe session used ref
`local:034Kzif1lYCp6Uvb1Smbsn` and its corresponding `.api.jsonl` and
`.transcript.jsonl`. These paths are source evidence only and their bodies are
not reproduced here.

Cleanup was verified for both runs: every reported hub, daemon and fake-provider
process stopped; every owned port was absent afterward; the compaction run
removed `state/evener/auth-token`, `prior-run/state/evener/auth-token`, and
`prior-run/run/token`; and the receipts report no repository modification and
no original-hub touch. No auth token, provider prompt, or private result body
is included in the archived assets. The evidence is limited to this packaged
SDK-only build and scripted provider; it does not qualify the latest backend,
live provider behavior, or native/mobile release acceptance.


## Packaged compaction effects, lineage, setup and maintenance — 7 September

These new direct authenticated loopback journeys use the real hub, daemon,
transcript store and AppWire handlers. Only the external model service is
scripted. The owned fixtures have isolated XDG roots, no inherited provider
credentials and plugin auto-upgrade disabled. There is no WebSocket proxy.
The backend executables carry `ccdde59b9-dirty`, built at
`2026-09-08T02:29:38Z`; exact executable hashes are recorded in each receipt.
This is backend provenance, separate from SDK and native artifact identities.

### Compaction effects and reserved rejection

The [compaction-effect receipt](assets/sdk-compaction-effect-receipt.json)
closes the SDK compaction-effect gap left by the command-only observations
above. Five authored turns completed before compaction. The persisted history
had 16 entries plus its header, and no summary request had occurred. One call
through the installed session-management recipe was acknowledged; the transcript
then gained `CHECKPOINT` and `SUMMARY` entries. The provider metadata identifies
exactly one no-tools, non-name-schema summary request. Its unique summary marker
also appeared in the next tool-capable model request. A second SDK connection
read the same session identity. The recipe conservatively retains
`execution: unverified`; these independent checks establish the observed effect.

The [exact driver](assets/sdk-compaction-effect-driver.mjs.txt) and
[scripted service/hub launcher](assets/sdk-compaction-effect-fixture.py.txt)
are archived with checksums. Raw model requests and transcript bodies remain
private; the receipt contains entry kinds, counts and hashes only. The installed
SDK tarball hashes to
`89bfc7445f3fbfe1f6a053b7f5c76dffdbcb0f715cf4f2f2cbe3ce2e1eeab2be`.
An additional actual request for reserved `thread/turns/items/list` was rejected
with code `-32601` and `methodNotFound`, as recorded separately in the receipt.
The earlier failed compaction runs remain excluded from acceptance. Summary
quality, public model services and the native compaction UI are unqualified.

### Lineage

The [lineage receipt](assets/sdk-lineage-receipt.json) and
[exact driver](assets/sdk-lineage-driver.mjs.txt) qualify transcript listing,
nonempty bounded preview, regular fork, aside fork and resuming an ended parent.
The source index came from the actual user item's `transcriptEntryIndex`.
The regular fork retained the original input and both distinct children referred
to the parent. Before resume, authoritative readback reached `notLoaded`;
afterward it retained the parent identity with an active runtime. A separately
connected SDK client verified the parent and both child relationships. Private
readback files were exclusively created with mode `0600` and are hashed in the
receipt. The [launcher](assets/sdk-lineage-fixture.py.txt) is also archived.

The driver imports an independently installed tarball with SHA-256
`01e1a26e0fabc7bdba878ebd75140299b65c720b3fc47e9a8ead98e2c6c753a0`.
It was packed while HEAD was `0410b2036`, before setup/saved-item changes were
committed at `a229b0c82`. Root compared the installed lineage and setup logic,
CLI, entrypoint and contract files byte-for-byte with committed source; the
receipt records those hashes. This does not relabel the tarball's build source.
The lineage journey uses the reusable logic; CLI output contracts were tested
separately. Lost fork replies, competing writers and native UI are not qualified
by this run. The earlier incomplete worker lineage run is superseded here.

### Hub setup and maintenance

The same installed tarball passed the actual hub-setup CLI against the compaction
fixture. [Setup proof](assets/sdk-setup-receipt.json) and the
[exact driver](assets/sdk-setup-driver.py.txt) record a new nested directory,
canonical path/readback and `created: true`, followed by deliberate repetition
returning `created: false`. Pairing generation produced the current
`/auth/<escaped-token>` format; the driver compared its decoded token privately
to the fixture token. Every private output had mode `0600` and stdout contained
only the documented metadata. The credential-bearing pairing output was removed
immediately after verification. This proves generation, not phone import,
reachability, DNS or LAN connectivity.

The [maintenance receipt](assets/sdk-maintenance-receipt.json) records packaged
CLI ping, `evener/auth/test` and `evener/plugin/checkNow`, using the earlier
`89bfc744…` tarball on a separate owned hub. Root verified all retained private
output hashes and modes. Auth testing succeeded through the scripted
`auth = none` model-list endpoint; it does not qualify real account credentials.
Plugin checking began and ended with an empty marketplace registry and returned
an empty result; no installation or upgrade is claimed. The archived
[reproduction helper](assets/sdk-maintenance-helper.mjs.txt) was prepared after
the run; the exact original CLI invocation script was not retained.

### Cleanup and reproduction limits

The successful fixtures exited zero. Root verified their listener ports and
owned executables were gone, and removed fixture bearer-token files and private
hub logs. An orphaned daemon from the superseded worker lineage run was identified
by its owned executable path, terminated, and checked again before its credential
was removed. This is cleanup evidence, not workflow qualification. The original
hub on port 54211 and all seven native drafts remain preserved.

Archived drivers retain the original owned paths and installed-package imports.
To reproduce, prepare a fresh private fixture directory with copied `evener` and
`evener-dev`, isolated config/state/cache/data/run/workspace directories, empty
credentials and marketplace registry, and the receipt's SDK package. Copy the
launcher/driver there, supply new input metadata and update their package/fixture
paths. Start the launcher, wait for its runtime metadata, then run the driver;
request its loopback `/shutdown` afterward and independently check all owned
processes and listeners. These are source/evidence helpers, not a self-contained
release harness. Never repoint them at a production hub. The original sensitive
outputs are private local evidence and are not included in these assets.

The setup and saved-item implementation at `a229b0c82` passed 18 focused
contracts, the canonical frontend gate and outside-checkout package qualification.
The full canonical gate and separate vet last exited zero at `ccdde59b9`.
The added runtime receipts do not imply a newer full-gate or iOS release pass.
