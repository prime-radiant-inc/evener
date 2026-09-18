# Shared interactive artifacts: qualification and implementation contract

Date: 2026-09-18. Checkout base: `0a53ef91f517c8d323a2381a493233b51ab59e22`.
Branch: `codex/shared-artifacts-qualification`.

This is the contract record for implementing the commissioned v1.0 design.
It distinguishes observations of existing code and dependency experiments from
requirements that production implementation must still satisfy. No A01–A22
production acceptance case has passed at this stage. Raw execution stays off.

## Source material and precedence

The unchanged source documents supplied outside the repository are:

| Document | SHA-256 |
| --- | --- |
| `evener-shared-artifacts-spec-2026-09-18.md` | `b41e6254d0f4efa577acc15db6fe00e677b2b084d446ef21395919121f4a16e3` |
| `01-handoff-supplement.md` | `40a50dbb2e552515e2c1831e1f03646cf29f7eb65707a3d007e0772ba2f7a4b7` |

The commissioning request selects the architecture. The original specification
defines its requirements. The supplement proposes clarifications; the decisions
below explicitly adopt them as implementation contracts, not as claims of
separate product approvals. Existing repository instructions and testing policy
apply throughout.

The complete handoff was subsequently supplied as
`evener-artifacts-implementer-handoff-2026-09-18.zip`, SHA-256
`f2cbc9427f05ed720d820f6cf0173a0f09e2c5b9bb8da073ab75a0fa44da7493`.
Its sequence, fixtures and examples README have been read. The included checker
verified all 15 manifest entries, 22 domain vectors, four authoring scenarios,
and five HTML scripts using Python 3.14.7 and Node 26.0.0. This is document and
syntax validation only. The original spec and supplement hashes match exactly.
The sequence's G0–G5 gates govern execution. G1 is the durable domain, G2 shared
runtime, G3 the complete comparison interaction, G4 recovery, and G5 release
qualification. Map fixture driver vocabulary to actual SDK/AppWire codecs.

## Dependency qualification

| Component | Resolved version / profile | Evidence and limits |
| --- | --- | --- |
| Go | 1.27.0, darwin/arm64 | Actual `go version`; matches `go.work` |
| SQLite | `modernc.org/sqlite` v1.50.1 | Actual workspace module resolution; production store not implemented yet |
| Go MCP SDK | v1.6.1 | Workspace resolution and real SDK client/server loopback experiment |
| Root module outside workspace | MCP SDK v1.3.0 | `GOWORK=off go list -m`; align explicitly to v1.6.1 when adding service dependency |
| MCP transport profile | 2025-11-25 | SDK initializes this version; JSON response/stateless mode exercised |
| Node / npm | 26.0.0 / 11.12.1 | Actual local tools; build-time tools only |
| MCP-UI candidate | `@mcp-ui/client` 7.1.1 | Exact scratch installation; use `AppFrame` behind an Evener adapter |
| MCP Apps candidate | `@modelcontextprotocol/ext-apps` 1.2.0 | Coherent with JS SDK family below; Apps profile 2026-01-26 |
| JS MCP candidate | `@modelcontextprotocol/sdk` 1.27.1, `zod` 3.25.76 | Scratch bundle and actual codecs; not yet added to production lockfile |
| Browser | Chrome 153.0.8010.36, Darwin arm64 | Actual browser experiment; no Safari, Firefox, Linux or remote HTTPS qualification yet |

Do not upgrade to the incompatible ext-apps 2.x / split JS SDK 2.x family as an
incidental renderer change. No runtime npm installation or backend JavaScript.
The selected MCP transport version and Apps version are separate contracts.

The Go SDK experiment uses its real `StreamableHTTPHandler` with `Stateless` and
`JSONResponse`, a real SDK client, bearer middleware, and loopback HTTP. It proves:

- JSON response mode preserves tool, result, content-block and resource metadata,
  structured success, and structured domain errors when the raw handler returns
  `CallToolResult{IsError: true, ...}, nil`.
- Generic typed `AddTool` with an ordinary Go error discards returned structured
  fields. Domain failures must use the explicit result envelope.
- Stateless mode still issues a protocol session ID. That ID is neither durable
  artifact identity nor authorization; independently authenticate every request.
- An app-only visibility annotation does not prevent direct SDK tool calls.
  Enforce visibility at the adapter, proxy, and service authorization boundaries.
- Raw `CallToolParamsRaw.Arguments` preserves number tokens and duplicate keys.
  The application must reject duplicates. Generic schema/map decoding is lossy.
- Notifications return 202, stateless GET returns 405, unsupported profile headers
  fail, and an unauthenticated call receives 401. The HTTP Accept header must still
  include both `application/json` and `text/event-stream`.

These are dependency experiments, not supervision, storage, or Evener acceptance
tests. Scratch sources, logs and reports are under
`.superpowers/shared-artifacts-qualification/{backend,browser}` in the worktree.

## C01–C08 decisions

| Clarification | Decision | Frozen behavior |
| --- | --- | --- |
| C01 | Adopt | Stable realm/session or realm/human principal; persist immutable logical operation and dispatch bytes before first dispatch; renewed authority may resolve the same receipt without reviving an old view |
| C02 | Adopt | One service writer orders grant revocation, namespace tombstones and write commits; an acknowledged revocation fences every later commit using that authority |
| C03 | Adopt | Host captures delivered source revision/hash and proposal data at ingress; approval rechecks authority and destination without relabeling historical provenance; rejected IDs remain terminal |
| C04 | Adopt | Private idempotent `ensureNamespace` and `tombstoneNamespace`; Hub association persists before ensure; durable pending deletion and access embargo precede service cleanup; startup applies pending deletion before grants |
| C05 | Adopt; clarify numeric domain | Durable terminal version conflicts; source conflict takes precedence; service-owned fingerprint v1 sorts object keys, preserves array order and JSON number tokens, rejects duplicate keys, and bounds state numbers for browser round trips |
| C06 | Adopt | Validate shape, UTF-8, supported format/version, required fields and byte limits; preserve authored bytes/hash; publication does not certify JavaScript behavior |
| C07 | Adopt | State-only changes offer explicit refresh, never overwrite active inputs or silently rebase; reconcile pending writes before remount |
| C08 | Adopt | Process ownership, runtime grants, MCP connections and view generations have separate lifetimes; qualify both direct clients and actual agent/child construction paths |

C05 deliberately keeps the supplement's token-preserving rule: `1`, `1.0`, and
`1e0` are distinct submitted representations. `-0` differs from `0`. Object key
order is insignificant; optional absence differs from explicit null. The service
fingerprints a strictly parsed, deterministically serialized JSON tree using raw
number tokens, not a float64 map. JSON must be valid and bounded, with duplicate
keys rejected at every nesting level. The same parser validates state and semantic
requests. A fingerprint is versioned and must not change under existing receipts.
Fingerprint v1 includes the resolved namespace plus semantic tool arguments,
excluding `mutationId`; realm, principal and operation are the receipt key. This
prevents the same creation request/ID from retrieving an artifact in a different
namespace after a grant changes. Credentials, service runs and lease generations
are excluded. The namespace is trusted context, never a public tool argument.

State/proposal numeric values use the finite binary64 domain that the browser
helper can represent. Reject overflow, nonzero underflow to zero, and integer
values outside the safe range `[-9007199254740991, 9007199254740991]` before lossy
decoding. Decimal fractions have ordinary binary64 value semantics; exact decimal
or arbitrary-precision quantities must be strings. Versions are safe positive
integers. Numeric tokens remain significant to the service fingerprint even when
two accepted tokens denote the same browser number. A new explicit browser save
can serialize `1.0` as `1`; it is a new mutation, not an exact retry. Recovery
reuses the frozen submitted JSON representation. Mount and source updates do not
rewrite stored state.

The alternative of normalizing numeric tokens for fingerprint identity was
considered because the current agent registry decodes to maps. A narrow
raw-argument path preserves the chosen distinction without changing ordinary
tools' executor behavior. It does not remove the browser numeric-domain limit:
an actual Node probe rounds `9007199254740993` and serializes parsed `1e400` as
null, so both must fail state validation. Reject host-field injection and
duplicate keys before generic argument repair can discard evidence; bundled
mutations are not silently repaired through a lossy map.

## Normalized public tool contract

This section describes MCP tool arguments and structured results, not new
JSON-RPC method names. Use SDK `tools/call`, `tools/list`, and `resources/read`
codecs. Supplied fixture operation labels must be translated by their test driver.
All argument objects reject unknown properties. Identifiers are opaque strings;
versions are positive integers. Namespace, realm, principal and runtime generation
never come from tool arguments.

| Tool | Arguments | Structured success / visibility |
| --- | --- | --- |
| `artifact_publish` create | Required `mutationId`, `title`, `summary`, `html`; optional `initialState` object (default `{}`), `format` (default `html`), `formatVersion` (default `1`) | Compact committed receipt; model only |
| `artifact_publish` update | Required `mutationId`, `title`, `summary`, `html`, `artifactId`, `expectedSourceRevision`, `expectedStateVersion`; optional `format` (default `html`), `formatVersion` (default `1`); `initialState` forbidden | Compact committed receipt; model only |
| `artifact_read` | Required `artifactId`; optional `include` array of `source`, `state`, `diagnostics`, default none; optional `sourceStartLine`/`sourceEndLine` only with source | Metadata and requested bounded bodies, both versions; model only |
| `artifact_list` | Optional `cursor`, `limit` (default 20, max 100) | `artifacts` metadata array and optional opaque `nextCursor`; model only |
| `artifact_open` | Required `artifactId` | Metadata and bounded launch data; model only; sole tool declaring viewer resource |
| `artifact_get_view` | Required `artifactId`; optional `knownSourceRevision`, `knownStateVersion` | Metadata and versions always; source/state bodies omitted when corresponding version unchanged; app only |
| `artifact_save_state` | Required `artifactId`, `mutationId`, `expectedSourceRevision`, `expectedStateVersion`, `state` object | Compact committed receipt; app only |
| `artifact_report_diagnostic` | Required `artifactId`, `sourceRevision`, `message`; optional `kind` (`runtime` or `validation`) | Acknowledged bounded report; app only, no model wake |

Freeze public schemas once in the implementation and derive the model projection
and generated shared types from that authority. Evener's model schema omits
`mutationId`; the trusted execution adapter inserts its durable operation ID.
Public MCP retains the field for other authorized hosts. The service also verifies
the authenticated method grant; tool visibility is not a grant.

The sole resource is `ui://evener-artifacts/viewer-v1.html`, MIME
`text/html;profile=mcp-app`. Its bytes are immutable for resource contract v1.
Only `artifact_open` declares `_meta.ui.resourceUri`; every tool declares the
appropriate `_meta.ui.visibility` array. No arbitrary resource or URL fetching.

Example `structuredContent` for a successful checkpoint (initial publication
creates source revision 1 and state version 1):

```json
{"status":"committed","mutationId":"mut_example","artifactId":"art_example","sourceRevision":1,"stateVersion":2}
```

Return this original receipt unchanged on exact retry, even after a later head.
Supply a concise, deliberately constructed text block too; do not let typed SDK
fallback stringify private source/state or the host envelope into model context.

Domain failure `structuredContent`, with MCP `isError: true` and a concise text
block:

```json
{"status":"rejected","error":{"code":"SOURCE_CONFLICT","retryable":false,"sourceRevision":3,"stateVersion":4}}
```

Only authorized conflicts include current versions. Lookup denial and absence
share `NOT_FOUND_OR_FORBIDDEN` without private details. `MUTATION_ID_REUSED` never
modifies the original receipt. `BUSY` has `retryable: true` and no accepted durable
outcome; retry retains the same ID and payload. `SERVICE_UNAVAILABLE` describes
transport/service availability, not proof of rollback. Remaining required codes:
`STATE_CONFLICT`, `UNSUPPORTED_FORMAT`, `INVALID_SOURCE`, `INVALID_STATE`,
`TOO_LARGE`, `QUOTA_EXCEEDED`, and `DELETED`. JSON-RPC errors remain reserved for
protocol/request failures. An uncertain connection outcome is not a domain receipt.

## Durable mutation and namespace authority

The service owns a private local SQLite database with WAL, foreign keys and
`synchronous=FULL`, one serialized writer, bounded readers, explicit schema
version, and no destructive downgrade. Artifact, first revision, state v1 and
creation receipt commit together. Publish increments only source revision; save
increments only state version. Both version preconditions apply to every update.
Source history and lifetime compact receipts are retained; quota failure cannot
evict deduplication records. Content deletion retains content-free tombstones.

Authorization precedes receipt lookup; matching receipt precedes current-head
preconditions. The same writer orders current grant fences, namespace tombstones
and mutation acceptance. Test both revocation orderings using explicit barriers.
Commit failure must never become a successful receipt; lost acknowledgment is
resolved from the same durable request and current authority.

Hub currently has a single authenticated owner capability, not a per-user account
system. Mint a durable random realm ID and owner principal in private Hub state;
do not derive them from the rotating token, cookie, browser client ID or PID.
Agent principal binds that realm to the authenticated durable session ID. Runtime
and view generations narrow authority, but do not enter receipt identity.

Hub records namespace ID, realm and owning root conversation before private
ensure. Repeating the same association succeeds; changing owner/realm fails.
Grant issuance requires acknowledged provisioning and no deletion embargo.
Descendants inherit only verified, capability-narrowed root namespace authority;
fork ancestry alone does not grant it. Clear gets a new association. Root deletion
durably records pending tombstone and embargo before cleanup; child deletion
revokes the child without deleting the shared namespace. Service restart starts
with no grants and applies pending tombstones before new issuance.

Agent publication requires a session-owned managed-operation journal containing
stable invocation identity and the exact immutable service request before the
first network call. The placement is after preparation/PreToolUse acceptance and
before registry dispatch in `agent/session_tools.go`. Reconcile pending records
on restore before ordinary orphan-tool repair can label an unknown commit as a
mere interruption. Current transcript `AppendDurable` can retain an unsynced
record and return nil; it is not sufficient to authorize dispatch. Use a real sync
barrier with explicit failure, following existing durable journal patterns.

Checkpoint requests use the existing browser outbox. They retain durable reference
and principal association, never credentials. Closing a frame revokes future
proposals but does not erase an already-persisted request. Recovery obtains fresh
narrow authority, settles the original request, and only then hydrates a new view.

## Existing integration points and required extensions

| Existing production path | Verified behavior / required extension |
| --- | --- |
| `agent/internal/mcp/manager.go` | Constructor eagerly connects/lists; metadata and structured results are discarded. Add fixed bundled catalog and lazy managed acquisition without changing unknown stdio ownership |
| `agent/session_init.go`, `session_config.go` | Inject optional application-owned runtime provider in new/restore config, never serialized credentials or Hub implementation imports |
| `agent/subagents.go`, `delegate_runtime.go` | Current narrowing follows MCP initialization. Register bundled tools first, acquire only after surviving capability checks; verify descendant scopes and restored delegates |
| `agent/internal/tool/registry.go`, `session_tool_repair.go` | Map decode loses number tokens/duplicates and error path loses side channels. Add narrow raw validation/execution and explicit typed host result through success and domain error |
| `agent/session_tools.go`, `session_tool_round.go`, `llm/types.go` | Carry explicit host envelope separately from model output and sync managed invocation/reference state at the right durability boundary |
| `internal/appprojector/appwire_projection.go`, `internal/apptranscript/apptranscript.go` | Preserve envelope and stable reference through live and persisted projection |
| `appwire/types.go`, `protocol.go`, shared TypeScript model/reducer | Generate plain-data reference/status/proxy types; keep browser/React/SQLite imports outside shared core |
| `cmd/evener-hub/spawn.go`, `cmd/evener/serve.go` | Add private inherited bootstrap and daemon-only broker identity; current daemon environment/rendezvous token is not new management authority |
| `cmd/evener-hub/internal/hubcore/deletion_store.go` | Reuse durable pending-deletion workflow; ensure artifact namespace embargo survives Hub/service restart |
| `appwire-client/typescript/state/mutation/dispatcher.ts` | Extend existing allowed method and receipt/recovery handling; do not add a competing retry loop |
| `cmd/evener-hub/frontend/src/stores/mutationOutboxIndexedDB.ts` | Already persists request/ID before dispatch; extend artifact record/result settlement |
| `cmd/evener-hub/frontend/src/stores/threads.ts` | Send resolves on local enqueue. Artifact `ui/message` must wait for authoritative normal-input acceptance, not this promise alone |
| `cmd/evener-hub/frontend/src/panes/session/transcript/toolRenderers.ts` | Render a durable reference card and useful fallback, without replaying the original tool |
| `cmd/evener-hub/frontend/src/shell/paneRegistry.ts`, `workspace.ts` | Add a pane type using existing layout machinery; one live renderer per artifact/page and stable reference across pin/unpin |

The exported agent runtime seam must avoid imports from the root application into
the reusable agent module. Hub owns one real child process and private control;
lease Release closes client resources/grants only. Actual process ownership must
remain protected by a service-held lock through shutdown. Browser/agent MCP
clients carry narrow temporary bearer grants. No process discovery adoption.

Surviving daemons cannot inherit a fresh FD after Hub restart. Rebootstrap needs a
separate narrowly authenticated operation over the existing trusted Hub-to-daemon
route, bound to current daemon incarnation and durable ownership; `ClientInfo`
strings are not authority. This path requires process-test qualification before
claiming A20. Until qualified, show unavailable rather than use secret-file or
browser-auth substitutes.

## Browser delivery and restricted host

Use `AppFrame` with an Evener-owned `AppBridge`. The candidate `AppRenderer`
registers a successful default `ui/request-display-mode` handler without an
exposed override, so a fallback handler alone cannot enforce the deny policy.
Explicitly deny unsupported registered methods as well as unknown ones. The
candidate `SandboxConfig.permissions` is typed but ignored by its implementation;
do not mistake that option for a permissions boundary.

The real Chrome experiment delivers the bundled viewer through the upstream
MCP-UI sandbox proxy on a credential-free hostname,
`artifact-sandbox.localhost`, separate from the Hub's `localhost`. A synthetic
host-only Hub cookie is absent on the sandbox origin. The viewer creates a nested
opaque-origin `srcdoc` runner with `sandbox="allow-scripts"`. The runner policy is
delivered before authored source and its network denials are measured in Chrome:

```text
default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline';
img-src data: blob:; font-src data:; media-src data: blob:; connect-src 'none';
worker-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none';
form-action 'none'
```

The viewer policy must permit this controlled nested runner; qualify inherited
policy, do not merely store a desired CSP string. Its runner handshake validates
actual window, one-time nonce and generation, then transfers a document-bound
MessagePort. Ordinary `WindowProxy` identity plus nonce alone is insufficient
during navigation before the load handler runs. The real pre-load navigation
experiment passed: a replacement document attempted checkpoint and reconnection
while its readyState was `loading`; a barrier held load-based revocation and the
authority was still live. Both attempts were denied and only the original genuine
checkpoint was accepted. This is evidence for the chosen bridge design, not a
production sandbox claim.
Frame self-navigation remains a possible egress path; `connect-src 'none'`
does not block every navigation. Spec 13.2 explicitly requires revoking host
capabilities after unexpected navigation, not a complete no-egress guarantee.
The restricted resource API's prohibition on arbitrary URL fetching describes
the host API, not all browser networking. Do not claim that a load handler or
an unsupported navigation CSP directive prevents the initial request. Production
qualification must exercise self-navigation, redirects and meta refresh, record
which requests leave the browser, and verify replacement documents cannot
acquire capabilities. The scratch srcdoc replacement test proves only its
stated capability boundary.
Remote use requires a separate credential-free HTTPS hostname and valid TLS;
otherwise present static fallback.

The actual Apps `ui/message` failure codec is `{ "isError": true }`.
`{ "success": false }` is accepted as an unrelated passthrough property and does
not signal rejection. Success is the qualified success result only after normal
input durable acceptance. Persist proposal identity, immutable payload, delivered
provenance and stable input mutation ID independently of the SDK request lifetime.
Send/Queue remain explicit human choices under existing session capabilities.

Mount, polling, save and diagnostics never send input or wake a model. Generated
source never receives credentials or arbitrary host capabilities. There is no
zero-egress or hard per-frame CPU/memory isolation claim. Closing a view revokes
authority and removes its frames; it does not kill the shared backend.

## Evidence and remaining gates

Recorded dependency command:
`go run .superpowers/shared-artifacts-qualification/backend/sdk_fixture.go` — exit 0;
real loopback SDK experiment, no provider calls. Resolved module evidence is
committed in [evidence/resolved-go-modules.json](evidence/resolved-go-modules.json). Browser commands, from the independent scratch
`browser/` directory: `npm ci`, `node codecs.mjs`, `node default-handlers.mjs`,
`node qualify.mjs` — passed. The host also passed TypeScript with the frontend's
bundler module resolution and `skipLibCheck`. Browser evidence is
[evidence/browser-results.json](evidence/browser-results.json), with package
integrity pins in [evidence/dependency-pins.json](evidence/dependency-pins.json).
The [evidence inventory](evidence/README.md) distinguishes committed observations
from the local-only harness and reports; it is not a standalone reproduction kit. OS: macOS 26.5.2 (25F84), Darwin 25.5.0 arm64;
installed headless Chrome 153.0.8010.36. The experiment measured actual denied
network/resource/form/popup/worker paths, opaque DOM/storage denial, ten forbidden
SDK methods, rejected `ui/message`, and eight forged/stale runner requests.

Baseline `make test` completed with exit 0: root, agent, llm, auth, envvars,
invariant, identifier and web all passed. Log: [evidence/baseline-test.log](evidence/baseline-test.log). New production
tests must observe a meaningful failing behavior before implementation. Use real
SQLite and real service processes, scripted providers only at the LLM boundary,
explicit fault barriers rather than timing sleeps, and the real included HTML
through the runner once supplied.

Release requires all A01–A22, both 100-client and 100-agent/child paths, existing
repository gates, named OS/browser evidence, and operational enable/disable,
coherent backup/restore instructions. Track unit, process, browser and opt-in
live-model evidence separately. This record enables implementation; it does not
authorize raw execution before security and lifecycle qualification.
