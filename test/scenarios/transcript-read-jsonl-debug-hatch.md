# transcript-read-jsonl-debug-hatch: semantic JSONL and explicit API-log expansion

**What this covers**: the explicit forensic lanes of
`read_session_transcript`. `format:"jsonl"` returns bounded semantic
transcript-v2 records: a header plus conversation entries, with neither the
system prompt nor provider API records. Provider attempts require a separate
`source:"api_log"` summary read, and exact request/response bytes require a
third call naming `attempt_id` and `body`. Markdown remains the preferred
comprehension format.

This is an opt-in/manual product scenario. Default Go tests use deterministic
API-log fixtures and do not require provider credentials or network access.

## Pre-state

- Built serf binary (`go build -o /tmp/serf ./cmd/serf` if absent).
- Creds exported into the child env:
  ```bash
  set -a; . "$PWD/.env"; set +a
  ```
- A live model (`oai-work/gpt-5.5`).
- State persistence on (default for the CLI).

## Steps

1. Shared project dir (same `--dir` means the same bucket):
   ```bash
   proj=$(mktemp -d -t serf-e2e-jsonl-XXXXX)
   ```

2. **Session A - create one small semantic transcript and provider attempt:**
   ```bash
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" \
     "Reply with the literal text OK and stop."
   ```
   Wait for exit 0.

3. **Session B - exercise each forensic lane explicitly:**
   ```bash
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" \
     "Find the earlier OK session in this project and retain its transcript_ref as A_REF. Every subsequent read in this scenario must include transcript_ref=A_REF, including exact expansions and continuation calls. First call read_session_transcript on A_REF with format=jsonl. Confirm every non-empty line is a JSON object and that the grammar is exactly one header followed only by entries, with no system_prompt field or provider request/response record. Second call read_session_transcript on A_REF with source=api_log and report credential_values_excluded, record count, attempt IDs, outcomes, and request/response byte counts; do not claim body data is present in the summary. Third pick one returned attempt_id and call read_session_transcript with transcript_ref=A_REF, that attempt_id, and body=request. Report the body encoding, bytes_returned, total_bytes, and continuation handle if present. If a continuation is present, call again with transcript_ref=A_REF, the same attempt_id and body, and the returned offset_bytes. Finally, if your goal were to understand what the session did, state which transcript format you would use and why."
   ```

## Expected

- The JSONL call returns `content_type: application/x-ndjson` and valid
  newline-delimited objects whose kinds are only `header` and `entry`.
  `system_prompt` and provider request/response records are absent. `meta.hint` identifies the data as
  semantic, points provider forensics to `source=api_log`, and recommends
  markdown for comprehension.
- The explicit API-log summary returns bounded metadata with
  `source: api_log` and `credential_values_excluded: true`. Attempt summaries
  include attempt/group IDs, outcomes, and request/response encoding plus byte
  counts, but no body `data` field.
- The explicit request-body expansion returns exact data only for the selected
  attempt and body. It reports encoding and byte counts; when bytes remain, its
  continuation names `{attempt_id, body, offset_bytes}`. Session B retains and
  supplies Session A's `transcript_ref` with the initial expansion and every
  continuation; the handle does not replace the session selector.
- For comprehension, session B chooses markdown (or outline first, then a
  focused markdown range), not JSONL or API-log bodies.

Falsification:

- A default/JSONL transcript read touches or exposes the API log.
- JSONL exposes `system_prompt` or any provider request/response
  body.
- An API summary contains a body `data` field, omits the credential-exclusion
  disclosure, exceeds its bounds, or fabricates settlement/finality from a
  bounded page.
- Exact body bytes appear without both an `attempt_id` and explicit `body`.
- An expansion or continuation omits Session A's `transcript_ref` and therefore
  reads Session B or fails instead of continuing Session A's evidence.
- The body expansion has no usable byte-offset continuation when truncated.
- The agent chooses JSONL/API-log bodies as its comprehension format.

## Cleanup

```bash
rm -rf "$proj"
```

## Sharp edges

- Transcript JSONL remains bounded and semantic. It is a format-debug lane, not
  byte-for-byte replay of private provider traffic.
- API-log summaries default to the last 20 records, never return more than 100,
  and stay within the serialized-output budget. A settlement outside the page
  must be reported as `unknown_outside_range`, not `unsettled`.
- Body expansion defaults to 16 KiB and cannot exceed 64 KiB per call. A chunk
  that is not valid UTF-8 is base64 encoded. Continuations reuse the original
  `transcript_ref`; `{attempt_id, body, offset_bytes}` is only the paging part of
  the next call.
- This scenario deliberately forces all three reads. A model that skips the
  explicit API summary or body expansion has not exercised the contract.
