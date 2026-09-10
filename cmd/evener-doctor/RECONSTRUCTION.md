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
mistaking an interrupt receipt for the original input.

The output contains:

- `sessions/SESSION_ID.transcript.jsonl`, validated through Evener's strict native
  decoder, including a clearly identified reconstruction notice.
- `sessions/SESSION_ID.meta.json`, an unchanged copy of the supplied metadata.
- `source-snapshot.json`, the selected archive records used in reconstruction.
- `source-mutations.json`, when a mutation journal was supplied.
- `report.json`, with the coverage cutoff, tool-output omissions, limitations,
  and SHA-256 hashes of the transcript and source snapshot.

Only root sessions with surviving metadata are supported. The command refuses
forked or delegated histories, incomplete message ordinals, malformed tool
arguments, and tool events that cannot be paired with their calls and transcript
timestamps. Source schema mismatches fail explicitly.

## Fidelity and installation

This reconstructs normalized history. It cannot recreate original transcript
bytes, media, provider signatures, all private runtime fields, or conversation
after the archive's last synchronization. AgentsView can deliberately omit tool
result bodies. Those are represented by explicit unavailable-content notices;
recorded call IDs and result error status remain attached. Archived reasoning
remains readable text, without invented provider signatures.

Attention resolutions retain their readable evidence as historical system
messages. The archive does not store the originating steering delivery IDs, so
replaying the resolution alone as live attention bookkeeping would be invalid.
The report counts these historical records explicitly.

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
