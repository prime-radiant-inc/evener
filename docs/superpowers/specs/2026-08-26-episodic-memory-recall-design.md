# Episodic memory recall: security and retrieval contract

**Status:** design proposal only. This document does not implement retrieval, embeddings, authorization, delivery, or storage changes. It is a follow-up design for [#334](https://github.com/prime-radiant-inc/evener/issues/334); implementation requires approval and separate child issues.

## 1. Boundary and vocabulary

Episodic recall is a user- or agent-requested query over archived session evidence. It is not project memory, transcript discovery, or unsolicited context injection:

- **Project memory (#34):** curated, durable project knowledge with its own authoring and lifecycle. It is not an input corpus for this contract by implication.
- **Transcript discovery (#4 / `find_session_transcripts` and `read_transcript`):** the existing lexical archive/audit surface. It remains the baseline and is not silently replaced or broadened.
- **Observer/live injection (#49):** unsolicited delivery. This proposal does not authorize or design it.
- **Recall:** a bounded, explicit request that returns evidence and provenance, not instructions to execute.

The initial implementation should be local-first. No transcript text, metadata, or embedding may leave the configured trust boundary without an explicit, separately reviewed user opt-in and transport policy. The default is no external embedding service.

## 2. Retrieval contract (proposed)

### Request

A request contains:

- `query`: non-empty UTF-8 text, with an implementation-defined maximum input size;
- `scope`: explicit default `current_project`; supported scopes must be enumerated, never inferred from query text;
- optional filters: project identity, session/ref, agent kind, branch/lineage, author/role, inclusive time range, and retention/privacy class;
- `limit`: bounded, with a server hard maximum;
- `mode`: lexical, semantic, or hybrid (hybrid is the default only after semantic indexing exists);
- `include_quotes`: explicit, default true for reviewable evidence.

A request has a caller identity and capability context. Scope filters are authorization constraints, not merely ranking hints. An omitted filter must not mean “all projects” or “all sessions.” Invalid or contradictory filters fail closed.

### Retrieval unit and ranking

The durable unit is a **turn chunk**: a contiguous, bounded slice of one transcript turn, never an unanchored embedding. Each chunk carries its session, turn sequence, byte/rune offsets when available, content kind, and index version. Chunking must preserve enough boundaries to quote exactly and must not join unrelated sessions or projects.

The first design target is deterministic hybrid retrieval:

1. apply authorization, scope, deletion, retention, and redaction filters;
2. retrieve lexical candidates (the current substring discovery remains a separate baseline);
3. retrieve local semantic candidates, if an approved local embedding backend exists;
4. fuse scores with a documented deterministic tie-break (`session ref`, sequence, and chunk offset);
5. apply diversity limits so one transcript cannot consume every result;
6. return at most the requested limit and hard maximum.

Scores are relevance signals, not truth or permission. The contract must expose score components or calibrated bands rather than imply that a numeric score is a probability. Exact-match lexical results must remain possible even when semantic indexing is unavailable.

### Response

Each result contains:

- a bounded quote copied from the source after current redaction policy;
- `transcript_ref`, session/project identity, turn sequence, content kind, and chunk offset/range;
- source updated time and an immutable source/content digest (or equivalent version token);
- retrieval mode, index/embedding version, relevance score/band, and rank;
- `provenance_status`: `exact`, `redacted`, `stale`, or `unavailable`;
- a link/handle usable by the existing transcript reader, subject to the same authorization.

A response also reports applied scope/filter summary, index coverage, whether results were truncated, and failure/degradation reasons. A result must never be rendered as an executable command or authoritative instruction; callers receive it as untrusted quoted evidence.

## 3. Trust and threat model

### Assets and principals

Assets include transcript text, tool arguments/results, thinking, credentials and personal data, source metadata/lineage, deletion intent, index contents, embeddings, and retrieval audit records. Principals are the human user, ordinary agents, the dedicated recall agent, the host process, local storage/index operators, malicious transcript authors or tool outputs, and optional embedding infrastructure.

The proposed capability boundary is a dedicated **memory-recall agent**. Only that agent may invoke the recall backend/tool. Ordinary agents may request a bounded recall operation through an allowlisted interface, but cannot select a backing store, widen scope, bypass redaction, or read raw index records. The capability must be checked server-side, not by prompt text or tool naming.

### Threats and controls

| Threat | Required control / acceptance evidence |
| --- | --- |
| Cross-project or cross-user disclosure | Authenticate caller; authorize every request and result; default to current project; enforce lineage/privacy filters before ranking; negative isolation tests. |
| Secrets in arguments/results or thinking | Classify before indexing and before quoting; redact by policy; never embed raw excluded fields; local-only default; secret-scan fixtures and no-secrets audit. |
| Deleted/redacted source remains retrievable | Tombstone and remove from lexical/semantic indexes; reject stale handles; verify after restart and migration; deletion is fail-closed if synchronization is incomplete. |
| Prompt injection or malicious recalled instructions | Treat all recalled content as untrusted data; delimit and label quotes; never execute tools or change policy from a quote; adversarial corpus test. |
| Memory poisoning / repeated false claims | Preserve source and time/lineage; do not promote recall to curated memory; diversity and duplicate suppression; expose corroboration count and conflict status; no automatic write-back. |
| Query or result exfiltration through embeddings | No remote embedding by default; explicit endpoint allowlist, consent, redaction, encryption, and retention policy before any remote mode. |
| Index tampering or confused provenance | Integrity-protected records, source digest/version, authenticated local ownership, and validation on read; mismatch yields `stale`/`unavailable`, never a guessed quote. |
| Resource exhaustion | Hard limits on query, filters, candidates, chunks, quote bytes, ranking work, index growth, and wall-clock deadline; return partial/degraded status honestly. |
| Timing/coverage leakage | Do not reveal existence of unauthorized sessions; normalize authorization failures and avoid metadata side channels where practical. |
| Capability confused deputy | Recall agent receives only caller-approved scope; backend rechecks caller and project; no “all projects” escalation via nested agent or ref. |

The host and local state directory are trusted only to the extent of the existing process/file permissions; this design does not claim protection from a fully compromised host. Embedding-model correctness, semantic truth, and user intent are not security properties.

## 4. Data boundaries, privacy, and redaction

Indexable fields must be an explicit allowlist. Proposed default: user/assistant visible text and selected structured summaries, excluding thinking, raw tool arguments, raw tool results, credentials, environment dumps, filesystem contents, and hidden/system policy. The existing discovery API may continue its current public lexical behavior; changing its searchable fields is out of this design’s scope and requires a separate compatibility decision.

Redaction is applied before persistence, embedding, ranking output, and logs. It must be deterministic and versioned, with placeholders that cannot be mistaken for source text. A quote is never reconstructed from an unredacted backing copy. Users need per-session opt-out, project policy, and deletion/redaction controls; policy changes affect future indexing and trigger reprocessing/tombstoning according to the lifecycle plan.

Privacy classes must distinguish at least: private-to-caller, project-shared, and explicitly shareable. Cross-project retrieval requires an explicit capability and policy; “all projects” is not a privacy grant. Index metadata itself (titles, timestamps, existence, counts) is sensitive and is filtered by the same authorization boundary.

## 5. Lifecycle, versioning, and failure behavior

Indexing is asynchronous and idempotent. A committed transcript change emits/records an index job keyed by source ref plus content digest. Updates create a new source version; deletion creates a durable tombstone before physical reclamation. Rebuilds are restartable and must not expose a partially migrated index as current.

Every record names schema version, chunker version, redaction-policy version, and embedding model/version. Version changes use a shadow index, deterministic backfill, integrity/coverage checks, then an atomic active-version switch. Keep the prior version until rollback and retention rules permit removal. A missing model or corrupt index degrades to lexical retrieval or an explicit unavailable result; it must not silently use a different model, return unproven provenance, or claim complete coverage.

Retention expiry, opt-out, deletion, redaction, source corruption, authorization denial, quota exhaustion, timeout, and index lag each have distinct machine-readable statuses. Partial results carry `coverage=partial` and cause; empty results distinguish “no authorized match” from “index unavailable” without disclosing unauthorized existence.

## 6. Evaluation and acceptance checklist

The implementation plan must add deterministic fixtures/tests (not prose tests) for:

- ranking/filtering and stable ties across lexical/semantic/hybrid modes;
- exact quote fidelity, offsets, ref links, and redacted quote behavior;
- project, session, branch, agent, time-range, and privacy isolation;
- deletion, redaction, opt-out, restart, and stale-index synchronization;
- malicious recalled instructions and poisoning/repetition/conflict handling;
- index schema/model/redaction migration and rollback;
- bounded latency, candidate work, quote size, and storage growth;
- a 201-session corpus where the current 200-session scan bound misses a known older match, proving the new index is not the old scan with a larger constant;
- index unavailable/lagging/corrupt and authorization failure modes.

Measure recall@k, precision@k, MRR or nDCG on a versioned labeled corpus, quote fidelity, unauthorized-disclosure rate (target zero), deletion leakage (target zero), poisoning/adversarial success rate, p50/p95 latency, index lag, and bytes per source byte. Evaluation must report corpus and policy versions; semantic quality must not be inferred from a handful of hand-picked examples.

## 7. Non-goals and unresolved decisions

Non-goals: runtime retrieval in this PR; embeddings or a vector database; changes to `find_session_transcripts`/`read_transcript`; automatic transcript summarization; automatic writes to project memory (#34); unsolicited live injection (#49); cross-user tenancy; remote model/service selection; claims that retrieved text is true; or closing #334.

Unresolved and requiring architecture review: exact local index technology and encryption-at-rest posture; embedding model and licensing; chunk size/overlap; supported privacy classes and redaction detector; capability/token representation; remote embedding policy; retention defaults; whether thinking/tool content can ever be explicitly opted in; score calibration and fusion weights; source digest format; and operational rebuild/compaction limits. These are intentionally not guessed here.

## 8. Traceability to current baseline

The current [`agent/session_tools_find.go`](../../../agent/session_tools_find.go) implementation provides metadata filtering, current/all-project scope (where available), refs/snippets, and a bounded scan of 200 opened transcripts. It searches full thinking, tool arguments, and tool-result bodies. [`read_transcript`](../../tools/transcripts.md) remains the provenance/audit reader. The future recall design must layer behind a new capability and preserve those APIs as the lexical baseline until a separately reviewed compatibility change lands.
