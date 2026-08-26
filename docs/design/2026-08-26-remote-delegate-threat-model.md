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

The controller is the authority for host selection, user intent, repository identity, sandbox mode, network policy, delegation allowance, time/resource limits, and result visibility. A worker MAY accept only a capability token/lease signed or MACed by the controller under the authenticated peer context. It MUST validate audience (worker), delegate/generation, expiry, nonce, protocol version, repository identity, and an attenuation chain before admission.

A capability is a bounded intersection, never an addition:

- operation: first slice `run_leaf_readonly` only;
- peer and repository identity plus exact base commit;
- workspace read roots and artifact/result size limits;
- sandbox and network policy at least as restrictive as the controller's request;
- no provider/API secret, host selection, delegation, credential forwarding, or arbitrary socket;
- explicit expiry, cancellation authority, and generation.

A child can narrow the parent's tools, delegation allowance, roots, duration, and output visibility, but cannot restore or expand them. Unknown capabilities, requested downgrade, missing authorization, policy disagreement, commit mismatch, dirty/ineligible workspace, and peer identity mismatch fail closed before execution. Worker-reported capability is a fact to audit, not permission to grant.

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

This is a shape, not an executable schema. The authenticated transport protects bytes in transit; envelope signatures/digests and monotonic sequence numbers protect storage, relay, and replay boundaries. Bodies are typed, bounded, and reject unknown required fields, invalid lengths, duplicate fields, malformed UTF-8, and unsupported kinds. Errors are typed and non-secret. No envelope may contain provider credentials, secret values, ambient environment, arbitrary host/path directives, or unbounded transcript/output.

Handshake exchanges protocol range, supported message kinds, worker identity, and an effective capability set. Negotiation is intersection plus policy: unknown required features and any downgrade that changes the authorization or safety floor cause refusal. Version selection and capability digest are recorded with the lease.

## 5. Lease, heartbeat, and ownership

Remote execution is a leased generation, not an owned process claim. The controller durably records admission before sending work. A lease binds `(controller instance, delegate_id, generation, worker identity, capability digest, workspace identity, expiry, fencing generation)`.

The worker may execute only the currently fenced lease and must persist its terminal disposition before acknowledging terminality. Heartbeats prove liveness of the authenticated peer and lease; they do not prove task progress or successful delivery. The controller renews explicitly; expiry fences the generation. A late worker packet, stale heartbeat, or old generation cannot mutate current state.

There is exactly one lifecycle authority: the controller's stable delegate aggregate remains authoritative for public `dlg_...` status, ownership, stop, and transcript reference. A worker's local record is subordinate evidence. Ownership transfer is not implied by reconnect, and no second worker may run the same generation unless a future protocol explicitly performs an atomic fence/claim.

## 6. Reconnect, idempotency, and result delivery

Every mutating request is idempotent by `(lease_id, request_id)` and every ordered stream item by `(lease_id, generation, sequence)`. The controller retries only after reconnect with the same identity and request ID; it never replays an unbounded task blindly. Duplicate start, cancel, ack, terminal, and artifact manifests are harmless and converge to one recorded outcome. Gaps are detected; the controller requests bounded replay or fails the generation as indeterminate.

Reconnect authenticates again and revalidates peer identity, lease, expiry, generation, capability digest, and workspace commit. It may resume protocol delivery, not silently resume execution. A terminal result is accepted only once, after integrity and authorization checks; transport acknowledgement means durable receipt, not user/model delivery. If receipt cannot be proven, status is `failed` or `stopped` with an explicit `runtime_lost`/`delivery_unknown` reason, never `completed` by timeout.

## 7. Cancellation and drain

`job_stop(target="dlg_...")` remains the controller operation and fences the delegate subtree before sending cancellation. A cancel carries the lease, generation, monotonic cancel sequence, and idempotency key. The worker stops admitting new work, interrupts the leaf, drains already-authorized terminal evidence, and returns a durable cancellation receipt. The controller records `cancelled` only when cancellation is confirmed; otherwise it records `stopped`/`runtime_lost` or `delivery_unknown` according to evidence.

Graceful close is a bounded drain: fence, cancel, wait for terminal/receipt, persist the controller outcome, then release transport resources. Disconnect, worker crash, or drain timeout is not proof of cancellation. No late packet may reopen a fenced generation. A cancelled or expired capability cannot be used for artifact upload or transcript append.

## 8. Workspace, sandbox, secrets, and provenance

The first slice requires a worker-local repository/worktree identified by an operator-approved repository identity, immutable base commit, and worktree identity. It refuses missing, mismatched, or dirty state. It transfers no working directory as authority and performs no remote checkout based only on a peer path. Later workspace transfer requires a separate design covering content-addressing, size limits, symlink/hardlink behavior, and cleanup.

The worker applies a resolved, immutable sandbox policy before execution and reports policy identity/measurements, not a claim that can widen access. Read-only means no persistent workspace mutation; network and filesystem rules are fail-closed. Controller policy cannot grant access above the worker's configured safety floor. Worker processes receive no provider credential or controller transport private key. Provider/API credentials are resolved locally by the worker only if a future explicitly authorized worker policy permits it; they never cross the wire, enter prompts, transcripts, artifacts, diagnostics, or environment snapshots.

Every transcript entry and artifact manifest records origin (`controller`/`worker`), authenticated peer identity, session/delegate/generation, sequence, parent/causal ID, workspace commit, capability digest, and content digest. Artifacts are content-addressed, size/type bounded, hash-verified before publication, and quarantined until verification. Unverified, partial, or worker-claimed paths are not readable through local transcript/artifact tools. Remote output is untrusted data, not user instruction or controller policy.

## 9. Auditability and failure semantics

Audit records cover peer enrollment/config changes, handshake and negotiated version, authorization decision and denial reason, lease issue/renew/expiry/fence, workspace and sandbox measurements, request dedupe, reconnect, cancellation/drain, artifact verification, terminal receipt, and delivery state. Logs contain IDs, hashes, classifications, and bounded metadata only; never secrets, full prompts, arbitrary command output, or full paths where a redacted identifier suffices. Audit append failure is visible and must not be represented as successful delivery.

The public local vocabulary remains typed: synchronous protocol/authorization failures create no delegate generation; execution failures are `failed`; confirmed cancellation is `cancelled`; supervision or attribution loss is `stopped`; delegate public state returns to `idle` only after the controller has reconciled the generation. `completed` requires an authorized, integrity-checked terminal result. Unknown outcome is never silently converted to success or retried as a new generation.

## 10. Threat and requirement checklist

Each future implementation/test plan must provide evidence for every item; this is a traceability checklist, not executable TDD:

- **T1 impersonation/MITM:** mutual authentication rejects an untrusted, expired, revoked, or wrong-audience peer.
- **T2 confused deputy/host injection:** model text, task data, URL, DNS, and worker advertisements cannot select or enroll a host.
- **T3 capability escalation/downgrade:** attenuation is monotonic; worker and controller refuse a weaker sandbox, broader roots, delegation, secrets, or unknown capability.
- **T4 replay/duplication:** stale lease, generation, sequence, and duplicate mutating request tests converge without duplicate execution or terminal delivery.
- **T5 split brain:** fencing prevents two workers or a late worker from owning one generation.
- **T6 disconnect/restart:** reconnect, controller restart, worker restart, gaps, and unknown outcome preserve explicit failure semantics and no blind replay.
- **T7 cancellation race:** cancel/start/terminal/drain races leave one fenced outcome; no late packet reopens work.
- **T8 workspace confusion:** repository/commit/dirty/worktree mismatch is refused before execution.
- **T9 secret exfiltration:** wire capture, logs, transcripts, artifacts, env snapshots, prompts, and sandboxed child processes contain no provider/API credentials or transport private keys.
- **T10 sandbox dishonesty:** measured worker policy is enforced and cannot be widened by the peer; read-only first slice cannot mutate persistent workspace.
- **T11 malicious protocol/data:** malformed, oversized, unknown, hash-mismatched, path-traversal, symlink/hardlink, and untrusted-output inputs are rejected/quarantined.
- **T12 provenance/audit:** every accepted result and artifact can be traced to authenticated peer, lease/generation, sequence, capability, workspace commit, and causal parent.
- **T13 version safety:** incompatible required versions/features fail closed; negotiation is recorded and capability downgrade is refused.
- **T14 claim boundaries:** tests and UI distinguish durable receipt, terminal result, notification, and user/model delivery; no test calls an acknowledgement delivery.

## 11. Compatibility, migration, and non-goals

Existing local delegates, IDs, transcript refs, watches, job status/reasons, restart reconciliation, and sandbox contracts remain unchanged. A future rollout is gated: protocol/peer configuration is opt-in; no configured peer means local behavior; remote records are additive and marked with peer/generation provenance. Persisted local state must not be rewritten into remote state. During migration, unsupported remote records fail closed with a visible diagnostic and remain inspectable as evidence.

Non-goals for this design: runtime networking; selecting a transport library or credential format; public peer discovery; arbitrary multi-tenant scheduling; shipping a remote daemon; workspace/file transfer; remote provider billing or credential brokering; remote nested delegation; live transcript/event streaming; exactly-once execution (the protocol provides idempotent convergence, not magic exactly-once); migration of an active generation between workers; claiming issue #321 closed.

## 12. Future acceptance gate

A runtime PR may proceed only when it links each implementation surface to this checklist, demonstrates the first-slice constraints, passes the required negative/security and recovery cases above, documents its concrete authenticated transport and credential provisioning separately, and preserves current local contracts. Approval of this document is approval of a boundary and threat model—not approval to ship remote execution.
