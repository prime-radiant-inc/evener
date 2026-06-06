# transcript-read-jsonl-debug-hatch: format=jsonl returns raw NDJSON; markdown is the steer for comprehension

**What this covers**: the bottom rung of the `read_session_transcript`
ladder (`docs/tools/transcripts.md` §"format: jsonl — replay it (debug
hatch)"). `format:"jsonl"` returns the **verbatim** transcript lines for
the range — the header plus interleaved `api_call` lines, including the
**system prompt** and raw API records — as valid NDJSON. It is "noisy
and rarely what you want": the tool description and the agent prompt
steer the model toward markdown for comprehension and reserve jsonl for
byte-exact replay / debugging the transcript format. This scenario
asserts (a) jsonl really is raw NDJSON carrying the system prompt +
api_call lines, and (b) the agent, asked to *understand* a session,
reaches for markdown, not jsonl.

## Pre-state

- Built serf binary (`go build -o /tmp/serf ./cmd/serf` if absent).
- Creds exported into the child env:
  ```bash
  set -a; . "$PWD/.env"; set +a
  ```
- A live model (`oai-work/gpt-5.5`).
- State persistence on (default for the CLI).

## Steps

1. Shared project dir (same `--dir` ⇒ same bucket):
   ```bash
   proj=$(mktemp -d -t serf-e2e-jsonl-XXXXX)
   ```

2. **Session A — any small session** so there is a transcript with at
   least one real API call to dump:
   ```bash
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" \
     "Reply with the literal text OK and stop."
   ```
   Wait for exit 0.

3. **Session B — exercise jsonl explicitly, then exercise the steer.**
   Two questions in one run:
   ```bash
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" \
     "Part 1 (explicit debug): find the earlier 'OK' session in this project, then call read_session_transcript on it with format set to jsonl. Report: (a) the content_type of the response, (b) whether the content is raw newline-delimited JSON where each non-empty line parses as a JSON object, (c) whether you can see the system prompt and api_call lines in it, and (d) the hint field's text. Part 2 (comprehension): forget Part 1's format — if your actual goal were to UNDERSTAND what that session did, which format would you use and why? State the format you'd pick."
   ```

## Expected

- After step 3, session B reports:
  - **Part 1:** `content_type` is `application/x-ndjson`. The content is
    valid NDJSON — each non-empty line is a standalone JSON object. The
    dump includes the transcript **header** line, the **system prompt**,
    and interleaved **`api_call`** records (raw API request/response
    logs) — the verbatim bytes, not the condensed conversation. The
    `meta.hint` reads to the effect of *"raw NDJSON; for comprehension,
    re-read with format=markdown."*
  - **Part 2:** B says it would use **markdown** (the default) to
    understand the session — the condensed conversation with assistant
    reasoning shown — and explains jsonl is for replay/debugging, not
    comprehension. (Outline as a first map is also acceptable; the
    falsifying answer is "jsonl".)
- Falsification:
  - jsonl content is NOT raw NDJSON (e.g. it returns the markdown
    rendering, or a JSON array, or pretty-printed multi-line objects
    where lines don't individually parse) → the raw-replay format
    regressed.
  - The jsonl dump omits the system prompt / api_call lines (i.e. it's
    been "cleaned up" to just turns) → jsonl stopped being the verbatim
    debug hatch.
  - For Part 2 the agent picks **jsonl** to understand a session → the
    tool/prompt steer toward markdown isn't landing (jsonl is being
    treated as a comprehension format).
  - `meta.hint` is missing or doesn't redirect to markdown → the steer
    string regressed.

## Cleanup

```bash
rm -rf "$proj"
```

## Sharp edges

- jsonl is bounded only by the 200k hard cap (head-only, valid NDJSON) —
  it does NOT apply the conversation budget or the per-result clamp. For
  a tiny "OK" session the whole thing fits; don't assert truncation here.
  On a large session, jsonl is head-truncated and `meta.truncated` is
  set — that's expected, not a bug.
- The system prompt is large; seeing it dominate the jsonl output is
  exactly why the tool steers to markdown. If the agent complains the
  jsonl is "mostly system prompt and API noise," that's the intended
  lesson, not a failure.
- This scenario deliberately *forces* a jsonl read in Part 1 to verify
  the format, then asks the unforced question in Part 2 to verify the
  steer. A model that refuses Part 1 ("I'd use markdown instead") is
  being over-helpful — the instruction is explicit; re-prompt to make it
  call jsonl so the format itself gets exercised.
- `format` is `strict:false`/optional and enumerated `outline | markdown
  | jsonl`. A typo'd format is not part of this scenario; the relevant
  malformed-input behavior (range fallback) is covered by the
  outline/range scenario.
