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

- `query`: non-empty UTF-8 text, at most 4,096 UTF-8 bytes (hard ceiling; the
  default request budget is 1,024 bytes);
- `scope`: explicit default `current_project`; supported scopes must be enumerated, never inferred from query text;
- optional filters: project identity, session/ref, agent kind, branch/lineage, author/role, inclusive time range, and retention/privacy class;
- `limit`: default 10, hard maximum 50;
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
- `transcript_ref` as provenance only (it grants no access), session/project identity,
  turn sequence, content kind, and chunk offset/range;
- source updated time and an immutable source/content digest (or equivalent version token);
- retrieval mode, index/embedding version, relevance score/band, and rank;
- `quote_fidelity`: `exact` or `redacted`;
- `source_verification`: `current`, `stale`, or `unavailable`;
- an opaque, separately scoped `recall_ref` read capability. It is server-checked
  for the original caller, project/privacy scope, and redaction policy before any
  detail is returned. `transcript_ref` remains a provenance label only; the
  existing reader retains its broader, independently authorized semantics.

`quote_fidelity=exact` requires a source-copied quote and a matching digest;
`quote_fidelity=redacted` requires a policy-versioned redacted quote and digest.
Both are legal with `source_verification=current` or `stale`. A stale result is
display-only evidence and its capability must not return newer or unredacted
content. `source_verification=unavailable` may contain no quote, offset, or
`recall_ref`; it must contain the indexed digest/version and a reason. A digest
mismatch is fail-closed: return no quote/capability and `unavailable`, never a
best-effort reconstruction. Thus every result variant has an unambiguous safe
consumer action.

A response also reports applied scope/filter summary, index coverage, whether results were truncated, and failure/degradation reasons. A result must never be rendered as an executable command or authoritative instruction; callers receive it as untrusted quoted evidence.

### Resource-bounds contract

These defaults and ceilings are normative and discoverable through the future
recall capability's read-only `recall_limits` metadata. Deployment configuration
may lower defaults but may never raise ceilings:

| Dimension | Safe default | Mandatory hard ceiling |
| --- | ---: | ---: |
| query bytes | 1,024 | 4,096 |
| result count | 10 | 50 |
| quote bytes per result | 2,048 | 8,192 |
| response bytes (quotes plus metadata) | 32 KiB | 128 KiB |
| candidate chunks examined | 1,000 | 10,000 |
| chunks ranked | 500 | 5,000 |
| results per session | 3 | 10 |
| wall-clock deadline | 500 ms | 2 s |
| index bytes per source byte | 2x | 5x |

The index also has a configured storage quota; its safe default is 1 GiB and
its mandatory ceiling is 10 GiB per project. A request exceeding query bytes or
`limit` is rejected before work with `invalid_request` and the effective limit.
Candidate, ranking, response, diversity, and deadline limits stop further work
and return available results with `coverage=partial`, `tripped_limits`, and
effective limits. If no authorized result is complete, return an empty partial
response rather than an unbounded retry. A storage-quota or index-growth breach
stops indexing, records `index_quota_exceeded`, and leaves the prior active index
usable; it never evicts data silently. Every response includes effective limits,
which limits tripped, and measured candidate/chunk/response work.

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
| Capability confused deputy | Recall agent receives only caller-approved scope; backend rechecks caller and project; no “all projects” escalation via nested agent or ref. Negative access-expansion test proves a `recall_ref` cannot expose fields or sessions unavailable to its caller. |

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

The corpus and expected outcomes are versioned (for example, `episodic-v1`
plus policy/index versions). The following are pass/fail acceptance criteria,
not merely measurements:

- On the labeled `episodic-v1` corpus, each supported mode (lexical, semantic,
  and hybrid) must achieve recall@5 >= 0.80 and nDCG@5 >= 0.70; hybrid must not
  score below lexical by more than 0.05 on either metric. Ties use the specified
  stable key, and the deterministic fixture rankings must match their checked-in
  expected rows exactly.
- Quote fidelity is 100% for `exact` fixtures and every `redacted` fixture must
  contain no forbidden token while retaining its expected digest/policy version.
  Unauthorized-disclosure and deletion-leakage rates must both be exactly zero.
- A poisoning/adversarial success is a malicious quote causing an evaluator to
  follow an instruction, change policy, or promote it as trusted memory without
  independent corroboration. The injection fixture must produce zero such
  successes; duplicate/repetition fixtures must not increase rank or corroboration
  beyond one source lineage, and conflict fixtures must report conflict.
- The 201-session fixture must find its known old match through the index while
  the legacy 200-session scan reports truncation; no result may cross its project
  boundary. Deletion, digest mismatch, and capability-expansion fixtures must
  return the exact fail-closed shapes defined above.
- At least 99% of bounded requests must finish within the 500 ms default deadline
  and 100% within the 2 s ceiling on the reference corpus; index lag must be <=
  60 seconds at p95. Candidate/chunk/response measurements may never exceed hard
  ceilings, and indexed bytes per source byte must be <= 2x default and never
  exceed the 5x ceiling. A limit breach is accepted only when its partial/reject
  status and `tripped_limits` report are exact.

Measure recall@k, precision@k, MRR or nDCG on the versioned labeled corpus,
quote fidelity, unauthorized-disclosure rate, deletion leakage,
poisoning/adversarial success rate, p50/p95 latency, index lag, and bytes per
source byte. Evaluation must report corpus, policy, and index versions; semantic
quality must not be inferred from a handful of hand-picked examples.

## 7. Non-goals and unresolved decisions

Non-goals: runtime retrieval in this PR; embeddings or a vector database; changes
to `find_session_transcripts` or its lexical behavior; automatic transcript
summarization; automatic writes to project memory (#34); unsolicited live
injection (#49); cross-user tenancy; remote model/service selection; claims that
retrieved text is true; or closing #334. The existing `read_transcript` contract
is not changed here, but recall cannot ship until the separately scoped,
server-checked `recall_ref` capability is implemented and tested.

Unresolved and requiring architecture review: exact local index technology and encryption-at-rest posture; embedding model and licensing; chunk size/overlap; supported privacy classes and redaction detector; capability token encoding (but not its server-checked scope); remote embedding policy; retention defaults; whether thinking/tool content can ever be explicitly opted in; score calibration and fusion weights; source digest format; and operational rebuild/compaction scheduling. The normative resource ceilings and response behavior above are not unresolved. These are intentionally not guessed here.

## 8. Traceability to current baseline

The current [`agent/session_tools_find.go`](../../../agent/session_tools_find.go) implementation provides metadata filtering, current/all-project scope (where available), refs/snippets, and a bounded scan of 200 opened transcripts. It searches full thinking, tool arguments, and tool-result bodies. [`read_transcript`](../../tools/transcripts.md) remains the provenance/audit reader with its existing broader semantics; a recall result's provenance ref must not expand that access. The future recall design must layer behind a server-checked scoped capability and preserve the existing APIs as the lexical baseline until a separately reviewed compatibility change lands.
