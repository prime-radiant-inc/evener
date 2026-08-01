# Open Local Job Transcript Reads

Date: 2026-08-01

## Summary

Serf will treat the local operating-system account, represented by one Serf
state home, as the read trust boundary. Any session in that state home may read
any locally persisted session transcript or job output when given an exact
reference. Read access does not grant discovery or control.

Jobs will use a flag-day, session-scoped identifier. The owner session encoded
in the identifier makes job-output lookup direct enough to remove observer read
grants, their durable event log, cross-session callback wiring, and the hub's
historical grant inversion.

There is no migration or compatibility path for existing job identifiers or
persisted job state.

## Goals

- Make local transcript and job-output read policy consistent.
- Keep job reads exact-reference-only and read-only.
- Locate a job's owner without enumerating session directories or maintaining
  a global index.
- Preserve the existing job-output evidence envelope for both owner and foreign
  reads, including running-job snapshots.
- Preserve observer auto-open as UI relationship metadata, independent of read
  permission.
- Delete substantially more grant infrastructure than the replacement adds.

## Non-goals

- Reading remote-backed sessions or state outside the caller's Serf state home.
- Global job listing, search, status, stopping, steering, watching, or delegate
  discovery.
- Waiting for a running job to emit output or finish.
- Migrating, recognizing, or explaining pre-flag-day job identifiers.
- Adding a persistent locator, cache, database, schema-version gate, or
  compatibility parser.

## Trust and capability model

The local OS account is already the effective confidentiality boundary: session
transcript references resolve across sibling project buckets without a separate
authorization check, and the same account can read the underlying files. Job
output will follow that model.

An exact `read_transcript(transcript_ref="job:<job-id>")` call may read the
named local job's evidence. It does not make that job visible to any other tool.
The following surfaces retain their current ownership and descendant rules:

- `job_list`
- `job_status`
- `job_stop`
- `delegate_send`
- `job_watch`

No new job enumeration or search surface is added. Existing session-transcript
lookup behavior remains unchanged.

## Job identifier

The sole valid job identifier format is:

```text
job_<owner-session-id>_<random-suffix>
```

The components are fixed:

- `job_`: four-character domain prefix.
- `owner-session-id`: the complete 22-character Serf session ID of the session
  whose jobstore owns the job.
- `_`: one separator.
- `random-suffix`: twelve base62 characters generated from a cryptographically
  random source.

The canonical identifier is therefore 39 characters. The owner session remains
fully recoverable without an index. The roughly 71 bits in the suffix need only
be unique within that owner session; no durable counter or allocator is needed.

Generation and validation share one definition. Validation requires the exact
length, a valid owner session ID, and a base62 suffix. There is no legacy shape.
Job creation must never overwrite an existing record or output artifact.

UI summaries must abbreviate the identifier so the random suffix remains
visible. Prefix-only clipping is invalid because every job owned by one session
shares that prefix. Copyable/detail surfaces retain the complete identifier.

## Owner and project lookup

Given a valid job ID, lookup extracts the owner session ID and checks only exact
paths for that owner.

1. Check the current project first:
   `<current-state-dir>/sessions/<owner-session-id>/jobs.jsonl`.
2. If that store contains the exact job record, it wins immediately.
3. Otherwise enumerate sibling project directories under the same state home,
   examining at most 256 sibling project entries.
4. For each valid sibling project, check only its exact owner-session path.
   Do not enumerate the project's sessions.
5. Zero matching records returns job-not-found. More than one sibling match is
   ambiguous. Reaching the project bound before establishing the answer returns
   `lookup_limit_exceeded`, even if a partial search found one candidate.

Sibling enumeration must itself be bounded; an unbounded glob that first loads
every project entry does not satisfy this contract. A flat or scratch state
directory with no state-home relationship can resolve only its current bucket.

The job record, not the mere presence of an output filename, establishes that a
job exists. Once the record is found, the reader derives the output path from
the validated project, owner session ID, and job ID. It does not trust a
persisted absolute `OutputPath` for a cross-session read.

Remote projects and remote transcript providers are outside this lookup.

## Read behavior

Any session may read a matching local job whether the job is running or
terminal. Owner and foreign reads return the same evidence envelope:

- job ID and type;
- current durable status and reason;
- retained output content;
- total, dropped, and truncation metadata;
- delegate structured result and its validity metadata, when present.

The foreign path is a filesystem snapshot, not a call into the owner session:

1. Read `jobs.jsonl` with the existing non-mutating event reader. It tolerates
   an incomplete final line caused by a concurrent append and never opens the
   store for creation, append, repair, or truncation.
2. Fold the durable record for the exact job.
3. Read the derived output artifact through a read-only window helper.
4. Validate the output metadata against the retained bytes.

A concurrent append or prune may invalidate the first output snapshot. The
reader retries once immediately, without sleeping. If the second attempt also
races, it returns `output_changed_during_read`. The read never blocks waiting
for more output or completion and never polls.

The record and retained bytes are a bounded point-in-time observation; a
running job may advance immediately after the result is formed. That is normal
snapshot behavior.

Error classes remain explicit:

- malformed job identifier;
- job not found;
- ambiguous owner across sibling projects;
- project lookup limit exceeded;
- retained output unavailable or pruned;
- corrupt durable job or output metadata;
- output changed repeatedly during the snapshot.

There is no permission-denied, grant-missing, or grant-expired result.

## Watch delivery and observer relationships

Observer read grants are deleted completely:

- no `watch_read_grant` event kind or observer-session field for it;
- no grant folding or loading;
- no mint-at-install or mint-at-delivery behavior;
- no per-watch grant deduplication;
- no parent-to-child granted-read callback;
- no granted-read view or snapshot type;
- no grant-aware `job_status` error;
- no hub reconstruction of observer relationships from grant history.

A watch frame derived from a structured job notification always appends the
canonical read instruction for that job. Events without a structured job ID do
not advertise a job read. There is no special permission case for the observer's
own job because the annotation conveys no capability.

Observer auto-open remains a separate UI relationship. The worker session's
`SessionMeta.ObservedBy` contains observer session IDs and remains append-only
and deduplicated.

There are two ways to learn the worker-to-observer relationship:

1. When a concrete delegate job is the watch target, resolve its worker session
   and stamp `ObservedBy` at watch installation so the UI can open the observer
   before the job finishes.
2. When a session-level watch observes a future structured delegate-job
   notification, resolve that delivered job's worker transcript reference and
   stamp `ObservedBy` at delivery. Shell jobs, events without a concrete job,
   and relationships that would make an observer observe itself produce no
   stamp.

Metadata stamping is best-effort UI bookkeeping. Failure may cost auto-open but
must not fail, delay, or alter watch delivery. Clearing or expiring a watch does
not remove historical `ObservedBy` entries and therefore needs no watch
reference counting.

The hub reads only `SessionMeta.ObservedBy` for observer auto-open. It does not
interpret old grant events.

## Flag-day behavior

This change deliberately breaks existing job identifiers and job transcript
references. There is:

- no dual parser;
- no migration;
- no fallback scan for an old identifier;
- no jobstore schema-version marker added solely for this transition;
- no automatic deletion of local state.

Pre-change persisted job state and transcript references are outside the
supported contract and must be discarded by the operator when adopting the
flag day. Serf does not inspect or mutate the user's existing state to help with
that transition.

## Verification

Default deterministic tests must exercise real behavior rather than rendered
command or large-string matching:

- identifier generation, owner extraction, suffix validation, and malformed
  input rejection;
- distinct jobs in one session retaining visibly distinct abbreviated IDs;
- current-project lookup precedence;
- bounded sibling-project lookup, ambiguity, and explicit limit exhaustion;
- absence of session-directory enumeration;
- path derivation that ignores an injected persisted absolute output path;
- equivalent owner and foreign reads for running and terminal shell and
  delegate jobs;
- structured-result, truncation, dropped-byte, and pruned-output behavior;
- deterministic first-race retry and repeated-race failure without sleeps;
- proof that foreign reads do not broaden list, status, stop, steer, or watch;
- watch-frame read instructions only for structured job notifications;
- installation-time and delivery-time `ObservedBy` stamping, deduplication,
  self-link suppression, and best-effort failure behavior;
- hub auto-open driven only by `SessionMeta.ObservedBy`.

Grant-specific tests are deleted rather than retained as tests of removed
internals. Parser or lookup fuzzing belongs under `make fuzz` exclusively; it
must not be introduced into the default test targets.

After integration, the controller runs the repository gate stack required by
the fleet ledger. Test output must remain pristine.

## Complexity constraint

The implementation must be materially net-negative. New production code is
limited to:

- session-scoped job ID generation, validation, and owner extraction;
- bounded exact-project lookup;
- a non-mutating output snapshot helper;
- the remaining direct observer metadata stamps.

It must not introduce a locator database, migration layer, compatibility
branch, persistent counter, cache invalidation scheme, or cross-session runtime
callback. If removal of the grant subsystem does not substantially outweigh
the replacement, implementation stops for design reassessment.
