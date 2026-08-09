# Recoverable Tool Output — Design

Date: 2026-08-09
Status: Approved
Source: `read-transcript-feedback-2026-08-08.md`

## Purpose

Make every truncated text tool result recoverable without rerunning the tool. An agent must be able to inspect any omitted byte in one additional `read_transcript` call during the current Serf process.

The first implementation stays small. It reuses the full output and job-log readers that Serf already has. It adds one temporary output store, one truncation flag, one capture hook, and two read operations.

## Current behavior

The registry already returns both forms of a text result:

- `tool.ExecResult.Output` is the bounded result sent to the model.
- `tool.ExecResult.FullOutput` is the exact result before generic truncation.

`Session.execTool` emits `FullOutput` through `TOOL_CALL_END`, but the model cannot query that event stream. The truncation warning therefore points to data without giving the model an address.

`read_transcript` also limits `job:` refs to one rendered tail. The job store already implements head reads, backward windows, and regex scans over the retained log. The missing work is chiefly an API bridge.

Job output and generic tool output have different retention contracts:

- A `job:` log persists across restarts but retains at most the existing 8 MiB tail. Earlier bytes may be gone.
- An `artifact:` result retains every byte but expires when the current Serf process ends.

The reader must state this difference plainly.

## Design principles

1. **Every generic truncation returns a handle.** A warning without an address is a defect.
2. **One reader handles retained evidence.** Keep `read_transcript`; dispatch by ref kind.
3. **Store exact bytes once.** Reuse `ExecResult.FullOutput`; do not rerun or reserialize a tool.
4. **Keep the first API narrow.** Add fixed byte paging and regex search with line context. Defer convenience grammars.
5. **Report loss honestly.** `artifact:` means complete output. `job:` means complete retained output and may report a dropped prefix.

## Reference kinds

`read_transcript` accepts three kinds of reference:

| Ref | Data | Lifetime | Completeness |
| --- | --- | --- | --- |
| Session ref | Semantic transcript | Durable | Existing transcript contract |
| `job:<job_id>` | Shell or delegate job output | Durable | Existing retained 8 MiB tail |
| `artifact:<id>` | Generic truncated tool result | Current process | Exact full result |

The tool keeps its current name for compatibility. Its description changes from “read transcript” to “read retained evidence by reference.”

## Minimal artifact store

The Serf process owns one concurrency-safe artifact store and shares it with all root and child sessions in that process. The top-level process creates the store; child sessions inherit the same pointer. Process shutdown closes the store and removes its temporary directory.

The store uses ordinary temporary files. It needs only these operations:

```go
type ArtifactStore interface {
    Put([]byte) (string, error)
    Open(ref string) (io.ReadSeekCloser, error)
    Close() error
}
```

The concrete store may use narrower internal methods, but it must not grow a metadata database, retention scheduler, or per-tool adapter. The file length supplies `total_bytes`. The ref contains an opaque, unguessable identifier and no path or tool argument.

The store has no size cap. This is an explicit product decision: generic tool output remains complete until process cleanup. A later disk policy requires a separate design because any cap weakens the invariant.

## Capture point

Add `Truncated bool` to `tool.ExecResult`. `truncateResult` sets it when its bounded `Output` differs because a configured line or character limit removed bytes.

The explicit flag matters. `TextResult` may intentionally use different `Output` and `FullOutput` strings even when no truncation occurred. Comparing those fields would create false artifacts.

After common tool dispatch and before `TOOL_CALL_END`, `Session.execTool` handles a truncated result:

1. Write `[]byte(res.FullOutput)` to the shared artifact store.
2. Append a short recovery footer to `res.Output`:

   ```text
   Full output: artifact:<id>
   Read with: read_transcript(transcript_ref="artifact:<id>")
   ```

3. Add the same ref to `ToolCallEndData` as an optional `output_ref` field.
4. Return the augmented `res.Output` to the model.

This one hook covers grep, glob, web fetch, file reads, plugin tools, and future registered text tools. Individual executors remain unaware of artifacts.

The store captures only results that generic limits truncated. Untruncated results create no file and gain no footer.

### Retention failure

If `Put` fails, Serf returns the bounded tool result and appends:

```text
[retention_failed: full output could not be retained: <concise error>]
```

Serf must not print “full output is available” or invent a ref after a failed write. The tool call itself keeps its original success or error status; retention failure is a separate warning.

## `read_transcript` API

Add two public arguments:

- `output_match` — an RE2 expression for `job:` and `artifact:` refs.
- `context_lines` — lines before and after each match, from 0 through 10; default 0. It requires `output_match`.

Broaden `offset_bytes`:

- Session refs: unchanged; it still requires `expand_turn`.
- `job:` and `artifact:` refs: the zero-based raw byte offset of a fixed 16 KiB page.

The default read for `job:` and `artifact:` starts at byte offset 0 of the available data. This removes the present tail-only surprise. The response includes a continuation when more bytes remain.

Do not add these features in the first version:

- `head:` or `tail:` range grammar;
- configurable page size;
- search result paging tokens;
- content-type negotiation;
- artifact listing;
- persistent artifact metadata.

Byte offsets already express head reads and continuation. Search results include offsets, so a caller can page around a match.

## Page response

A `job:` or `artifact:` page returns a small JSON envelope:

```json
{
  "transcript_ref": "artifact:…",
  "format": "raw",
  "content_type": "text/plain",
  "page": {
    "offset_bytes": 0,
    "bytes_returned": 16384,
    "total_bytes": 42000,
    "encoding": "utf8",
    "data": "…"
  },
  "retained_start_bytes": 0,
  "continuation": {
    "offset_bytes": 16384
  }
}
```

Use the existing UTF-8/base64 rule from transcript expansion: return UTF-8 when the page is valid UTF-8; otherwise return base64. A continuation always names the next raw byte offset.

For an `artifact:` ref, `retained_start_bytes` is always 0. For a pruned `job:` ref, it is the lifetime offset of the first retained byte. Job-page offsets are lifetime offsets, not offsets relative to the retained file. The first available job page therefore begins at `retained_start_bytes`.

If a caller omits `offset_bytes` for a pruned job, the read begins at `retained_start_bytes` and reports that value. It never pretends to begin at lifetime offset 0.

## Search response

`output_match` searches the available bytes and returns matching lines, byte offsets, and requested context:

```json
{
  "transcript_ref": "job:…",
  "output_match": "FAIL|panic",
  "context_lines": 3,
  "retained_start_bytes": 0,
  "total_bytes": 42000,
  "matches": [
    {
      "start_byte": 18201,
      "end_byte": 18234,
      "before": ["…"],
      "line": "FAIL package/example",
      "after": ["…"]
    }
  ]
}
```

Search uses RE2 and the repository's existing bounded line-reading primitives. `context_lines` follows the grep tool's existing 0–10 convention.

The search result remains subject to normal tool-output limits. If many matches make the search result itself too large, the generic invariant retains that exact result and returns another `artifact:` ref. This avoids a second search-specific paging system.

For a pruned job, search covers only retained bytes and reports nonzero `retained_start_bytes`. It must not describe the result as a scan of the full lifetime log.

## Validation and errors

The reader rejects unsupported combinations instead of ignoring them.

- `context_lines` without `output_match` → `invalid_request`.
- `context_lines < 0` or `context_lines > 10` → `invalid_request`.
- Invalid RE2 → `invalid_request` with the compile error.
- `output_match` on a session ref → `invalid_request`; session search is out of scope.
- `range` or `expand_turn` on `job:` or `artifact:` → `invalid_request`.
- `format=outline` or `format=jsonl` on `job:` or `artifact:` → `invalid_request`.
- Offset before a job's `retained_start_bytes` → `output_unavailable` with the first available offset.
- Offset beyond EOF → `invalid_request` with the valid byte interval.
- Malformed artifact ref → `invalid_request`.
- Well-formed but unknown artifact ref → `artifact_expired`.

A ref from an earlier process is expected to expire. Persisted transcripts may contain such refs; the explicit `artifact_expired` error is the contract.

## Compatibility

Existing session-ref reads do not change. Their formats, ranges, turn expansion, fixed page size, and continuation behavior remain intact.

Existing `job:` calls without new arguments change from a rendered markdown tail to a raw page from the first retained byte. This is an intentional behavior change. It fixes the tail-only default and makes continuation possible. The tool description and tests must call it out.

If preserving the old rendered job view proves necessary during implementation, keep it behind `format=markdown` and make raw paging the default only when `offset_bytes` or `output_match` is present. Prefer the simpler raw-page default unless a current caller test demonstrates a compatibility need.

Generic tools need no schema or executor changes. Their only visible change occurs on truncation: the warning gains an exact byte count, an artifact ref, and a concrete next call.

Replace vague text such as:

> The full output is available in the event stream.

with:

> Output truncated: showing 20,000 of 87,412 characters. Full output: `artifact:…`. Read with `read_transcript(transcript_ref="artifact:…")`.

## Security

Artifact refs must be unguessable. A reader may resolve only refs in the artifact store supplied to its process. Never accept a path as a ref, and never derive a path from unchecked ref text.

The store writes exact tool output, which may contain secrets. Create its directory and files with owner-only permissions. Do not record tool arguments or duplicate output in metadata. Cleanup removes the whole directory on normal process shutdown.

Sandboxed tools do not gain filesystem access through artifact refs. The Serf host reads artifacts and returns bounded content through `read_transcript`, as it already does for durable job logs.

## Testing

Read `docs/testing.md` before changing tests. All tests use synthetic output and local temporary directories; none requires network access or provider credentials.

### Registry and capture

- A result below its limits sets `Truncated=false`, creates no artifact, and keeps its output unchanged.
- Character, line, head-tail, and tail truncation set `Truncated=true`.
- An intentional `TextResult{Output, FullOutput}` difference without generic truncation creates no artifact.
- A truncated result stores bytes exactly equal to `FullOutput` and appends one valid handle.
- A store failure produces `retention_failed`, no false handle, and no change to the tool's original error status.

### Artifact reads

- Page a multi-page artifact from offset 0 to EOF and reconstruct the exact original bytes with no gaps or overlap.
- Preserve valid UTF-8 boundaries; use base64 for invalid UTF-8 pages.
- Search returns matching lines, absolute byte offsets, and 0, 1, and 10 lines of context.
- Invalid refs, stale refs, invalid RE2, invalid context, and invalid offsets return the specified errors.
- Concurrent puts and reads pass under the race detector.
- Closing the store removes its temporary directory.

### Job reads

- Page an unpruned job from offset 0 to EOF without gaps or overlap.
- Page a pruned job from `retained_start_bytes` and report the dropped prefix.
- Reject an offset in a pruned prefix.
- Search retained job output with context and absolute lifetime offsets.
- Keep delegate-job structured result coverage or expose it through a documented page representation.

### Receipt replay

Run a real workspace grep whose result exceeds the inline limit. Choose a known match omitted from the preview. Assert that one additional call,

```text
read_transcript(
  transcript_ref="artifact:…",
  output_match="KNOWN_OMITTED_MATCH",
  context_lines=3
)
```

returns that match without rerunning grep or narrowing the original pattern.

## Acceptance criteria

The change is complete when all these statements hold:

1. Every generic text result truncated by Serf returns an `artifact:` handle or an honest `retention_failed` warning.
2. During the current process, paging an artifact can reconstruct every original byte exactly.
3. `read_transcript` can page and search `artifact:` and retained `job:` output.
4. Search supports 0–10 context lines and returns byte offsets.
5. A pruned job reports its unavailable prefix; it never claims complete lifetime output.
6. The grep failure from the source report is solvable in one additional tool call without rerunning the search.
7. Existing session transcript behavior remains unchanged.

## Out of scope

- Persisting artifacts across process restart.
- Limiting artifact disk use or pruning artifacts before process exit.
- Searching semantic session transcripts.
- Changing the 8 MiB job-retention cap.
- Adding head/tail line-range syntax.
- Adding per-tool artifact formats or metadata.
- Replacing `read_transcript` with a renamed reader.
