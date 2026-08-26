# Remote delegates: threat model and protocol boundary

**Status:** Design-only proposal; no runtime networking or transport credentials are implemented.
**Date:** 2026-08-26
**Issue:** Refs #321

> This document does not implement remote delegates, claim delivery, or close #321. It records the smallest protocol and security decisions required before a later runtime slice. The feature remains open.

## 1. Scope and design posture

Today a stable `dlg_...` is owned by one local delegate-tree controller; shell work uses an owner-scoped `job_...`; conversation evidence is a `transcript_ref`; watches deliver bounded frames to their creating session. Creation, model clients, state, worktrees, and callbacks are local. This proposal adds a future remote boundary without changing those local meanings.

The first implementation slice MUST be a leaf (delegation allowance 0), non-delegating, read-only delegate on an operator-preconfigured trusted worker, with a repository already present at the requested commit and a terminal result only. It MUST NOT transfer provider credentials, accept a model-selected host, mutate a workspace, or promise general event streaming.

## 2. Trust zones and identities

| Zone | Authority | Must not trust |
|---|---|---|
| Controller | Root session, delegate-tree controller, local stores, policy and user intent | Worker claims, model text, peer-supplied host selection |
| Transport | Authenticated connection carrying framed protocol messages | Network location, reconnect continuity without proof |
| Worker | Approved daemon, its local sandbox, worker-side delegate runtime and artifact store | Controller payloads outside the granted capability |
| Workspace | Named repository/worktree and commit identity | A path string, dirty state, or repository supplied by the peer |
| Evidence | Controller/worker transcript and artifact records | Unauthenticated output, hashes without bytes, claimed delivery |
| Secrets | Provider credential resolution and local secret stores | Any wire payload, task, transcript, artifact, environment dump |

Every request has a controller instance identity, stable delegate ID, generation, and request/message ID. A worker has a configured peer identity and key/certificate identity. A session ID or transcript reference is evidence identity, never authentication or control authority. `job_...`, `dlg_...`, and `watch_...` remain typed, purpose-specific handles; remote transport IDs must not replace them.

Peers are explicitly configured by an operator (stable identity, endpoint, allowed repository/workspace classes, protocol versions, and capability ceiling). Discovery, DNS, a model string, a URL in task text, or a worker advertisement cannot enroll a peer or choose a host.

Authentication is mutually authenticated transport using the deployment's approved standard (for example, TLS with pinned operator-managed peer identity). This spec intentionally chooses no credential format, token placement, or key distribution. Credentials MUST be provisioned out-of-band, scoped to the peer, rotated/revoked, and unavailable to model-authored tools. Authentication proves peer identity and channel integrity; authorization remains a separate decision.

## 3. Authorization and attenuation

The controller is the authority for host selection, user intent, repository identity, sandbox mode, network policy, delegation allowance, time/resource limits, and result visibility. The worker MUST NOT admit or start work unless it has first verified an immutable **authorization grant** issued by the controller for that configured worker over the mutually authenticated channel. Authentication alone never authorizes execution. The grant is not a lease: it is the controller's signed/MACed authorization decision and contains `(grant_id, audience worker, controller authorization epoch, delegate_id, generation, operation, repository/worktree/commit, attenuated roots and limits, sandbox floor, network policy, result visibility, replay horizon, issued_at, not_after, nonce, capability digest)`.

Before launch, the worker validates the grant's audience, controller authorization epoch, expiry, nonce, protocol version, repository identity, attenuation chain, and configured peer policy. It then atomically binds the grant digest and all immutable grant fields to a newly issued lease. A lease can renew only its already-bound grant, generation, fence, workspace, and safety limits; renewal may shorten expiry or narrow capabilities, never widen them or change the worker, operation, repository, sandbox, or result visibility. The controller may revoke a grant by durably advancing its authorization epoch or recording its grant ID as revoked; the worker rejects admission and renewal for revoked/expired grants, and reconnect rechecks revocation. Grant revocation does not by itself claim that already-running work stopped; fencing and cancellation govern that.

A capability is a bounded intersection, never an addition:

- operation: first slice `run_leaf_readonly` only;
- peer and repository identity plus exact base commit;
- workspace read roots and artifact/result size limits;
- sandbox and network policy at least as restrictive as the controller's request;
- no provider/API secret, host selection, delegation, credential forwarding, or arbitrary socket;
- explicit expiry, cancellation authority, and generation.

A child can narrow the parent's tools, delegation allowance, roots, duration, and output visibility, but cannot restore or expand them. Unknown capabilities, requested downgrade, missing authorization, policy disagreement, commit mismatch, dirty/ineligible workspace, and peer identity mismatch fail closed before execution. Worker-reported capability is a fact to audit, not permission to grant.

The sandbox comparison is a canonical partial order, evaluated only after both
policies are normalized. A worker policy `W` satisfies requested controller
policy `C` (`W ⪯ C`) only when every dimension below is no broader than `C`:

| Dimension | `W ⪯ C` rule |
|---|---|
| Filesystem roots | Canonical, symlink-resolved read/write root set of `W` is a subset of `C`; path aliases are removed before comparison. |
| Writes | `deny` is narrower than `allow`; `W=deny` satisfies `C=allow`, while `W=allow` does not satisfy `C=deny`. |
| Network | `deny` is narrower than an allowlist, which is narrower than unrestricted; an allowlist must be a subset of `C`'s allowlist. |
| Secret masks | `W` masks a superset of `C`'s masked paths/patterns, after canonicalization. |
| Tools/capabilities | The callable tool and capability set of `W` is a subset of `C`; unknown names are not ignored. |
| Numeric limits | Duration, CPU, memory, process, output, artifact, and concurrency limits in `W` are less than or equal to `C`. |

Missing, unknown, malformed, incomparable, or non-canonical values fail closed;
there is no default ordering for an omitted dimension. The worker sends the
normalized policy identity and facts over the authenticated channel; the
controller records the same comparison result before grant issuance, and the
worker repeats it before admission. A policy identity is evidence of the
canonical facts, not permission to widen them.

## 4. Envelope and protocol

The wire protocol is a versioned, length-bounded, authenticated envelope. An implementation must define canonical serialization before shipping; the conceptual shape is:

```json
{
  "protocol": "evener.remote-delegate/1",
  "kind": "request|response|event|ack|cancel|heartbeat|error",
  "request_id": "opaque unique ID",
  "session_id": "controller session identity",
  "delegate_id": "dlg_...",
  "generation": 7,
  "sequence": 42,
  "lease_id": "opaque lease identity",
  "capability_digest": "hash of effective attenuated capability",
  "workspace": {"repo_id": "...", "commit": "...", "worktree_id": "..."},
  "body": "typed payload",
  "body_digest": "hash",
  "trace": {"parent_event_id": "...", "causal_id": "..."}
}
```

This is a shape, not an executable schema. Authenticity and integrity are claimed only for bytes while they are inside the live mutually authenticated channel; this design does not claim that a body digest authenticates a stored file or a relay after the channel ends. The controller and worker instead preserve the authenticated peer identity and verified digest in their own durable audit records. A future stored-envelope/signature design is a prerequisite for any untrusted relay or independently readable store and is not silently implied here. Monotonic sequence numbers are replay checks within the authenticated lease. Bodies are typed, bounded, and reject unknown required fields, invalid lengths, duplicate fields, malformed UTF-8, and unsupported kinds. Errors are typed and non-secret. No envelope may contain provider credentials, secret values, ambient environment, arbitrary host/path directives, or unbounded transcript/output.

Handshake exchanges protocol range, supported message kinds, worker identity, and an effective capability set. Negotiation is intersection plus policy: unknown required features and any downgrade that changes the authorization or safety floor cause refusal. Version selection and capability digest are recorded with the lease.

## 5. Lease, heartbeat, and ownership

Remote execution is a leased generation, not an owned process claim. One controller-side durable CAS authority allocates a strictly increasing fence per `(delegate_id, generation)`; allocation succeeds only from the currently persisted controller instance/epoch and records the new owner before any request is sent. A lease binds `(controller instance, controller epoch, delegate_id, generation, worker identity, verified grant ID/digest, capability digest, workspace identity, expiry, fencing generation)`. The worker durably stores the highest accepted fence for that delegate/generation and atomically compares-and-sets it when accepting a lease or renewal. Lower fences, equal fences from a different lease/connection, old controller epochs, and conflicting owners are rejected before launch.

The lease uses an authenticated TTL/deadline protocol, not a shared clock. The
grant carries controller wall-time validity (`issued_at`, `not_after`) and a
maximum duration `Dmax`; the controller rejects a grant whose validity interval
is absent, expired, exceeds `Dmax`, or cannot be established from its own
trusted wall clock. At acceptance, the worker checks the authenticated grant
wall-time interval against its own trusted wall clock with a configured maximum
clock-error bound `E`; if the interval is expired, outside `E`, or the worker
cannot establish that bound, it fails closed. The worker computes the
authenticated remaining grant validity conservatively as
`grant_remainder = not_after - (worker_wall_now + E)`. It fails closed when
`grant_remainder <= 0` or when that subtraction is uncertain; otherwise the
initial local monotonic deadline is
`monotonic_now + min(requested_ttl, Dmax, grant_remainder)`. This clamps the
initial lease to all three limits, so a disconnected worker cannot run past
the grant's `not_after`. The worker persists that deadline together with the
fence and enforces it locally. The controller also
enforces its own absolute deadline and sends renewal before both parties'
deadlines; renewal carries a fresh bounded TTL and may only shorten the grant's
remaining validity. Neither side treats the other's clock as its own.

The worker records a boot/monotonic epoch with the lease. On reboot, monotonic
reset, suspend/resume event that invalidates the configured bound, or inability
to prove the persisted deadline belongs to the current boot epoch, every
pre-reboot lease is expired and cannot renew. The controller must allocate a
strictly higher fence and perform a new grant acceptance; old connections and
old leases are rejected. A controller restart similarly reloads its CAS record,
advances controller epoch, and cannot renew a pre-restart lease without that
higher fence. Heartbeats prove liveness of the authenticated peer and lease;
they do not prove task progress or successful delivery. A late packet, stale
heartbeat, or old generation cannot mutate current state.

There is exactly one lifecycle authority: the controller's stable delegate aggregate remains authoritative for public `dlg_...` status, ownership, stop, and transcript reference. A worker's local record is subordinate evidence. Ownership transfer is not implied by reconnect, and no second worker may run the same generation unless a future protocol explicitly performs an atomic fence/claim.

## 6. Reconnect, idempotency, and result delivery

Every mutating request is idempotent by `(lease_id, request_id)` and every ordered stream item by `(lease_id, generation, sequence)`. The controller and worker have separate state machines; `sent` is controller-only and never a worker state or T4 oracle.

The controller state machine is `prepared → sent → accepted → running → terminal|cancelled|indeterminate`. `prepared` has a durable grant/lease/fence and may send; `sent` records that the request was emitted but has no worker admission proof; `accepted` records the worker receipt; `running` records a live generation; terminal and cancelled are final; indeterminate is final for that request ID and means prior execution cannot be excluded. On controller restart, unresolved `sent` becomes `indeterminate/runtime_lost` unless a durable worker acceptance receipt is already present; it is never blindly resent as a new request.

The worker state machine is exact and durable: `absent → admitted_before_launch → launching → running → terminal|cancel_requested → cancelled|indeterminate`. The worker atomically writes `admitted_before_launch` with `(lease_id, request_id, generation, fence, grant digest, boot epoch, local monotonic deadline)` before spawning. It atomically changes that record to `launching` immediately before the spawn operation, then `running` only after a runtime identity/heartbeat proves the process is attached. A crash while `admitted_before_launch` or `launching` leaves the state recoverable but does not prove that no process escaped: on restart the worker must inspect durable runtime identity; if survival cannot be proved, it changes the record to `indeterminate/runtime_lost` and refuses execution. A proven live runtime transitions to `running`; a proven terminal runtime transitions to `terminal`; a proven stopped runtime transitions to `cancelled` only when cancellation was committed, otherwise `indeterminate`. `running` is never returned merely because a stale record says running.

Duplicate/restart responses are deterministic for every worker state: `absent` rejects as `not_admitted`; `admitted_before_launch` or `launching` returns `indeterminate/runtime_lost` unless a live runtime identity is proven; `running` returns `running` only with a fresh runtime proof and current fence/deadline; `terminal` returns the recorded terminal result; `cancel_requested` returns the recorded cancellation phase and does not launch; `cancelled` returns the cancellation receipt; and `indeterminate` returns `runtime_lost` and never reexecutes. Records are retained at least through `not_after + replay_horizon`, not in an in-memory cache. Gaps are detected; the controller requests bounded replay or records indeterminate.

Reconnect authenticates again and revalidates peer identity, lease, expiry, generation, capability digest, and workspace commit. It may resume protocol delivery, not silently resume execution. A terminal result is accepted only once, after integrity and authorization checks; transport acknowledgement means durable receipt, not user/model delivery. If receipt cannot be proven, status is `failed` or `stopped` with an explicit `runtime_lost`/`delivery_unknown` reason, never `completed` by timeout.

## 7. Cancellation and drain

`job_stop(target="dlg_...")` remains the controller operation and fences the delegate subtree before sending cancellation. A cancel carries the lease, generation, monotonic cancel sequence, and idempotency key. The worker atomically transitions its durable outcome from `running` to `cancel_requested`, then either commits `cancelled` after the leaf is stopped or commits a terminal result if a terminal transition won the same CAS. A terminal disposition durably committed before the cancel CAS wins even if its packet arrives afterward; a cancel CAS committed first wins and later execution output is rejected. A cancellation receipt proves only that the worker committed `cancelled` and will not execute under that lease; it does not prove controller notification or user/model delivery. No evidence drain is promised after the execution capability is fenced: evidence not durably accepted before the fence is discarded or classified `delivery_unknown`.

Graceful close is bounded control reconciliation: fence, cancel, wait for the terminal/cancellation receipt, persist the controller outcome, then release transport resources. Disconnect, worker crash, or timeout is not proof of cancellation. No late packet may reopen a fenced generation. A cancelled or expired capability cannot be used for execution, artifact upload, or transcript append. If a future product requires post-fence evidence drain, it must define a separate bounded drain authorization with its own ID, expiry, provenance, read-only evidence scope, and no-execution/no-new-output rule; this design does not authorize one.

## 8. Workspace, sandbox, secrets, and provenance

The first slice requires a worker-local repository/worktree identified by an operator-approved repository identity, immutable base commit, and worktree identity. It refuses missing, mismatched, or dirty state. It transfers no working directory as authority and performs no remote checkout based only on a peer path. Later workspace transfer requires a separate design covering content-addressing, size limits, symlink/hardlink behavior, and cleanup.

The worker applies a resolved, immutable sandbox policy before execution and reports policy identity/measurements, not a claim that can widen access. Read-only means no persistent workspace mutation; network and filesystem rules are fail-closed. Controller policy cannot grant access above the worker's configured safety floor. Worker processes receive no provider credential or controller transport private key. Provider/API credentials are resolved locally by the worker only if a future explicitly authorized worker policy permits it; they never cross the wire, enter prompts, transcripts, artifacts, diagnostics, or environment snapshots.

Every transcript entry and artifact manifest records origin (`controller`/`worker`), authenticated peer identity, session/delegate/generation, sequence, parent/causal ID, workspace commit, capability digest, and content digest. Artifacts are content-addressed, size/type bounded, hash-verified before publication, and quarantined until verification. Unverified, partial, or worker-claimed paths are not readable through local transcript/artifact tools. Remote output is untrusted data, not user instruction or controller policy.

## 9. Auditability and failure semantics

Audit records cover peer enrollment/config changes, handshake and negotiated version, authorization decision and denial reason, grant issue/revocation, lease issue/renew/expiry/fence, workspace and sandbox measurements, request dedupe, reconnect, cancellation, artifact verification, terminal receipt, and delivery state. The following writes are mandatory and fail closed: controller authorization/grant and pre-launch admission before `start`; worker grant verification, fence acceptance, and dedupe admission before launch; controller/worker terminal or cancellation outcome before any receipt; and provenance/audit record before controller result acceptance. Best-effort diagnostic events are explicitly marked and cannot be used to satisfy those gates. If a mandatory append fails, the writer refuses the dependent transition, fences or stops work where possible, records `audit_unavailable` in the nearest durable authority, and exposes failure; it never reports successful admission, cancellation, completion, or delivery without the required record. Logs contain IDs, hashes, classifications, and bounded metadata only; never secrets, full prompts, arbitrary command output, or full paths where a redacted identifier suffices.

The public local vocabulary remains typed: synchronous protocol/authorization failures create no delegate generation; execution failures are `failed`; confirmed cancellation is `cancelled`; supervision or attribution loss is `stopped`; delegate public state returns to `idle` only after the controller has reconciled the generation. `completed` requires an authorized, integrity-checked terminal result. Unknown outcome is never silently converted to success or retried as a new generation.

## 10. Threat and requirement checklist

Each future implementation/test plan must provide evidence for every row; this is a compact traceability matrix, not executable TDD. Every row has setup, action, expected durable state, and externally observable result:

| ID/threat | Setup | Action | Expected durable state | External result |
|---|---|---|---|---|
| T1 impersonation/MITM | Configure one enrolled peer and one untrusted/expired/revoked identity. | Attempt handshake and grant use from each. | No grant/admission for the untrusted identity; denial audit records identity/classification. | Typed authentication/authorization refusal; trusted peer proceeds only after mutual auth. |
| T2 confused deputy/host injection | Configure a fixed peer registry; place host/URL text in task and worker advertisement. | Request start with injected host selection. | Controller persists only the configured peer decision; no grant for injected host. | Typed host-selection refusal; no network attempt to the injected host. |
| T3 capability escalation/downgrade | Prepare a child grant and worker policy that broadens one root, tool, limit, or secret/network dimension, or has an unknown value. | Negotiate and attempt admission. | No accepted grant/lease; mandatory denial audit; no worker launch. | Typed policy/capability mismatch. |
| T4 replay/duplication | Persist each controller state and each worker state around `admitted_before_launch`/`launching`; crash before and after spawn. | Replay identical `(lease_id, request_id)` after reconnect/restart. | Exactly one worker durable state through the replay horizon; uncertain pre-spawn state becomes `indeterminate/runtime_lost`; never two launches. | Deterministic state response; no blind reexecution. |
| T5 split brain | Persist fence `N`; restart controller/worker; allocate higher fence `N+1`. | Renew old connection `N` while submitting `N+1`. | Worker highest fence is `N+1`; old renewal and old packets are rejected. | Typed fencing/lease-expired error to stale owner; only `N+1` can execute. |
| T6 disconnect/restart | Run each controller/worker state, then disconnect or restart with missing, torn, or proven runtime evidence. | Reconnect and issue status/start/cancel. | Proven terminal remains terminal; proven live runtime may be running; unproven runtime becomes `indeterminate/runtime_lost`; no stale running state. | Deterministic response and explicit failure reason; no blind replay. |
| T7 cancellation race | Arrange terminal-before-cancel and cancel-before-terminal orderings, including delayed packets. | Apply terminal/cancel through the worker outcome CAS. | Exactly one final outcome; first successful CAS wins; no post-fence evidence is accepted. | Receipt distinguishes `completed`, `cancelled`, or `delivery_unknown`; never claims delivery. |
| T8 workspace confusion | Present missing, dirty, mismatched repo/commit, or mismatched worktree identity. | Attempt grant verification and start. | No admission; denial and workspace facts are durably audited; workspace unchanged. | Typed workspace refusal and no launch. |
| T9 secret exfiltration | Instrument wire, logs, transcript, artifact, environment, prompt, and child-process surfaces with sentinel secrets. | Execute the first read-only slice and capture all surfaces. | No secret-bearing record is accepted; any detected leak fails/quarantines the generation and audits the failure. | Secret-free captures and typed quarantine/failure. |
| T10 sandbox dishonesty | Supply canonical policy facts with missing, unknown, incomparable, or broader roots/write/network/mask/tool/limit dimensions. | Compare using the partial order and attempt a write. | No grant/admission; mandatory denial audit; workspace unchanged. | Typed fail-closed policy mismatch and no launch. |
| T11 malicious protocol/data | Prepare malformed/oversized/unknown/path-traversal/hash-mismatched/symlink/hardlink/output inputs. | Send them at handshake, admission, artifact, and transcript boundaries. | No state transition past the boundary; artifact is quarantined and rejection audit persists. | Typed bounded protocol/data error; no execution or publication. |
| T12 provenance/audit | Prepare a result/artifact with all provenance fields and inject each mandatory audit append failure. | Attempt acceptance, then retry after audit recovery. | Failed append leaves no accepted/completed result; `audit_unavailable` persists; recovery accepts exactly once with peer, lease/generation, sequence, grant, policy, workspace, and causal provenance. | Evidence is readable only after mandatory audit/provenance exists. |
| T13 version safety | Configure incompatible required versions/features and a safety-floor downgrade. | Negotiate and attempt admission. | No grant/lease; negotiated refusal and required feature are durably recorded. | Typed version/capability refusal; no launch. |
| T14 claim boundaries | Hold durable receipt, terminal result, notification, and user/model delivery at separate stages. | Drop or delay each stage and inspect every surface. | Only the actually committed stage is durable; no acknowledgement is recorded as delivery. | Distinct status/notification/read result; no false delivery claim. |

**Audit-failure acceptance:** Setup: inject failure into each mandatory-before-admission, mandatory-before-launch, mandatory-before-terminal-receipt, and mandatory-before-result-acceptance append. Action: perform the corresponding transition and restart each authority. Expected durable state: no dependent success transition is committed; work is fenced/stopped or remains explicitly indeterminate; recovery retries the audit transition without blind execution. External result: typed `audit_unavailable`/`runtime_lost`, not `completed`, `cancelled`, or delivered.

## 11. Compatibility, migration, and non-goals

Existing local delegates, IDs, transcript refs, watches, job status/reasons, restart reconciliation, and sandbox contracts remain unchanged. A future rollout is gated: protocol/peer configuration is opt-in; no configured peer means local behavior; remote records are additive and marked with peer/generation provenance. Persisted local state must not be rewritten into remote state. During migration, unsupported remote records fail closed with a visible diagnostic and remain inspectable as evidence.

Non-goals for this design: runtime networking; selecting a transport library or credential format; public peer discovery; arbitrary multi-tenant scheduling; shipping a remote daemon; workspace/file transfer; remote provider billing or credential brokering; remote nested delegation; live transcript/event streaming; exactly-once execution (the protocol provides idempotent convergence, not magic exactly-once); migration of an active generation between workers; claiming issue #321 closed.

## 12. Future acceptance gate

A runtime PR may proceed only when it links each implementation surface to this checklist, demonstrates the first-slice constraints, passes the required negative/security and recovery cases above, documents its concrete authenticated transport and credential provisioning separately, and preserves current local contracts. Approval of this document is approval of a boundary and threat model—not approval to ship remote execution.
