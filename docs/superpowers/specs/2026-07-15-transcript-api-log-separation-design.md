# Transcript and API-Log Separation Design

Date: 2026-07-15
Status: Approved
Tracker: #20

## Purpose

Serf currently mixes conversation history and API-call diagnostics in transcript
JSONL. Exact provider bodies are optional and written to a separate raw log.
This makes transcripts enormous, encourages agents to ingest wire payloads during
ordinary audits, and still does not guarantee that the forensic log contains the
exact request Serf sent.

The transcript becomes a semantic interaction record. The per-session API log
becomes the complete wire-forensics record.

## Hard-Break Decision

This is an intentional hard format break:

- new transcripts never write `api_call` lines;
- readers do not accept or translate legacy transcript `api_call` lines;
- there is no dual writer, compatibility alias, migration, or fallback reader;
- old sessions that require the old mixed format must be inspected with an old
  Serf binary.

The implementation must delete obsolete compatibility paths rather than retain
them behind flags.

## Transcript Contract

A transcript records the ordered interaction and durable state visible to the
agent or its supervisor:

- user, steering, and assistant turns;
- reasoning/message items that Serf already persists;
- tool calls and tool results;
- lifecycle and state-transition events;
- task, delegate, compaction, and notification events needed to explain the
  session;
- compact provider provenance on model-produced turns.

A transcript does not contain:

- provider request bodies;
- provider response bodies;
- system-prompt or tool-schema copies made solely for API diagnostics;
- `api_call` summary records;
- API-log lines embedded under another transcript event type.

Each model-produced turn may carry the stable `attempt_group_id` that produced
it. Provider errors may carry the same join reference. Attempts themselves are
not transcript entries.

## API-Log Contract

Each actual provider attempt appends one record to the session's canonical API
log. Every record contains:

- stable, globally unique `attempt_id`;
- stable `attempt_group_id` shared by retries and explicit fallbacks for one
  logical model call;
- one-based attempt index and terminal/final-attempt information;
- timestamp, latency, provider instance, requested model, and history mode;
- HTTP method and resolved endpoint;
- all non-secret request headers that reached the transport;
- the exact serialized request body bytes;
- the raw provider response body bytes, including provider error bodies;
- parsed outcome metadata such as status/error class, response model, finish
  reason, and token usage when available.

Attempt outcome classes distinguish provider rejection, transport failure,
provider timeout, caller cancellation, response decoding failure, and success.
Daemon/session-state faults, sandbox denials, job-notification failures, and Hub
failures are not rewritten as provider errors. Those remain lifecycle/job events
in their owning stores and may reference the attempt group when causally related.

Authentication tokens, API keys, cookies, and equivalent credential values are
never persisted. The record may identify the selected configured credential or
provider instance without exposing its value.

Bodies must be lossless. Valid UTF-8 bodies may be stored as JSON strings;
otherwise the record uses an explicit binary encoding such as base64. The log
schema records the encoding.

The API logger captures bodies at the adapter/transport boundary. It must not
reconstruct a request from Serf's higher-level `llm.Request` after the call.

## Attempt Identity

Serf assigns an `attempt_group_id` before the first provider call for a logical
model request. Every actual provider invocation receives a new `attempt_id`.

Retries, endpoint fallbacks, provider fallbacks, and continuation fallbacks:

- retain the logical group ID when they are attempts to satisfy the same model
  request;
- receive distinct attempt IDs and monotonically increasing attempt indexes;
- write a record whether they succeed or fail;
- identify the final attempt without rewriting earlier append-only records.

If final group information is not known until the last attempt, the terminal
record carries the final count. Readers derive the group summary from the
append-only records.

## Storage and Permissions

- The per-session API log is created with mode `0600`.
- Parent directories used exclusively for session-private forensic data are not
  world-readable.
- Existing `0644` creation sites are removed.
- The ordinary API summary and optional raw-body split are replaced by the one
  canonical per-attempt record. Exact request and raw response logging are not
  opt-in.
- Write/fsync behavior retains the existing crash-safety contract.

## Transcript Reader

`read_session_transcript` remains the canonical forensic tool.

Default behavior:

- source is `transcript`;
- format is semantic markdown, with outline and bounded exact-turn expansion;
- reads are limited by both turns and bytes;
- oversized individual entries return bounded evidence plus an expansion handle;
- raw transcript JSONL requires explicit `format=jsonl`.

API-log behavior is explicit and rare:

- callers select `source=api_log` or request an `attempt_id` expansion;
- default transcript reads never touch the API-log file;
- API-log reads are bounded by records and bytes;
- exact bodies require an explicit record/attempt expansion;
- results clearly identify that credential values were excluded.

The Hub/browser may expose the same explicit expansion, including exact request
and response bodies, but never preloads them with a transcript.

## Related Surface Updates

- `serf-doctor apilog` reads the canonical API-log records.
- Transcript find/outline/tree operations do not scan API bodies.
- Browser transcript request/result disclosures remain separate from provider
  API-log disclosures.
- Documentation must stop describing transcript JSONL as containing API logs.

## Testing

Use scripted provider transports that capture exact request bytes and return
known raw response/error bodies.

Cover:

- exact request and raw response round-trip, including escaped UTF-8 and binary
  fallback encoding;
- secret headers absent and non-secret headers exact;
- unique attempt IDs and correct retry/fallback grouping;
- failed attempts recorded;
- provider/transport attempt failures remain distinguishable from harness,
  session, sandbox, notification, and Hub failures;
- `0600` file creation;
- transcript files contain no API bodies or `api_call` lines;
- transcript turn provenance joins to an API-log group;
- normal transcript reads do not open the API log;
- explicit API-log reads and attempt expansion obey record/byte limits;
- old mixed transcripts fail with a clear unsupported-format error;
- restart/crash leaves only complete append-only records or a clearly detected
  partial tail.

## Scope Lock

This spec does not:

- preserve compatibility with legacy mixed transcripts;
- migrate existing session files;
- log credential values;
- put raw provider data in transcript, Hub cold-load, or normal agent context;
- redesign transcript typography;
- add a general blob/content-addressed storage system;
- change provider retry or fallback policy.
