# Reconstruct a missing session transcript

`evener doctor reconstruct` stages an Evener transcript from a local AgentsView
archive and surviving session metadata. It opens the archive read-only and never
installs files into live session state or starts a daemon. Every run needs a new
output directory; existing output is never overwritten.

```sh
evener doctor reconstruct SESSION_ID \
  --agentsview-db /path/to/agentsview/sessions.db \
  --meta /path/to/SESSION_ID.meta.json \
  --mutations /path/to/SESSION_ID.json \
  --output-dir /path/to/new-recovery-directory \
  --json
```

The mutation journal is optional. When supplied, client input identities are
restored by matching its stable turn IDs. This preserves retry identity without
mistaking an interrupt receipt for the original input. Recorded turn failures
also retain their matching mutation identity so native restore recognizes them
instead of appending the same failure again.
Stable IDs are restored only on user inputs, steering, and failures; steering
provenance is restored only on steering turns. The source snapshot keeps the
original archived fields for every record.
Supplied journals pass the same strict decoding and validation as native session
restore; incomplete containers, malformed records, and mismatched record IDs
are refused before staging any files.

The output contains:

- `sessions/SESSION_ID.transcript.jsonl`, validated through Evener's strict native
  decoder, including a clearly identified reconstruction notice.
- `sessions/SESSION_ID.meta.json`, an unchanged copy of the supplied metadata.
- `source-snapshot.json`, the selected archive records used in reconstruction.
- `source-mutations.json`, when a mutation journal was supplied.
- `report.json`, with the coverage cutoff, tool-output omissions, limitations,
  and SHA-256 hashes of the transcript and source snapshot.

Only root sessions with surviving metadata are supported. The command refuses
forked or delegated histories, incomplete message ordinals, duplicate call IDs
or positions, gaps in a message's call indices, non-object tool arguments, and
tool events that cannot be paired with
their calls and transcript timestamps. Each call must have exactly one result
event within its native tool round. Timestamps are compared as instants;
multiple tool-result messages at the same instant are ambiguous and refused.
Missing arguments become an empty object. Source schema mismatches fail explicitly.
Duplicate header fields and unknown tool-result statuses are refused. Successful
`delegate`, `delegate_send`, and `job_status` results may carry the delegate's
recorded lifecycle status, including a failed delegate; this does not make the
status-query tool call itself an error.
Generated records also pass native bounded framing: an encoded header or entry
over the runtime's record-size limit is refused before staging any files.

The transcript header preserves the surviving metadata's profile and model,
falling back to the first archived assistant response only for missing fields.
Each assistant turn retains its own archived response provider and model.

## Fidelity and installation

This reconstructs normalized history. It cannot recreate original transcript
bytes, media, provider signatures, all private runtime fields, or conversation
after the archive's last synchronization. AgentsView can deliberately omit tool
result bodies. Those are represented by explicit unavailable-content notices;
recorded call IDs and result error status remain attached. Result status determines
whether output is an error; successful output keeps literal error-like prefixes.
Text matching archive placeholders is retained because the archive cannot
distinguish it from literal conversation text. Archived reasoning
remains readable text, without invented provider signatures.
Assistant tool markers are removed only when the entire content is the
canonical marker-only block for its recorded calls; mixed text is retained.
JSON-only failures and completed hooks regain the readable diagnostic used by
native transcript rendering. Cache-write accounting prefers a supplied duration
breakdown, falling back to the aggregate count when no breakdown is present.

Attention resolutions retain their readable evidence in `source-snapshot.json`
and are omitted from the runtime transcript. The archive lacks their originating
delivery IDs, so native attention replay would be invalid. Omitting these private
records also keeps them out of model prompts and preserves pending tool rounds.
The report counts them in `historical_attention_records`.

Review `report.json` and inspect the staged history before installation:

```sh
evener doctor transcript SESSION_ID \
  --state-dir /path/to/new-recovery-directory \
  --range last:10
```

Installing is a separate recovery operation. Preserve the staging bundle and
original metadata and journals first. Verify there is no live owner of the
session, then publish a separate copy of the staged transcript atomically to the
original project's `sessions` directory, refusing an existing destination.
Preserve the original metadata and other surviving state. Resume explicitly and
verify the hub can read the restored history under the expected session identity.

Jobs, delegates, task stores, queues, and process state need independent review.
Reconstruction does not mean that old processes or claims of passing tests are
current. The transcript notice makes that boundary visible when work resumes.
