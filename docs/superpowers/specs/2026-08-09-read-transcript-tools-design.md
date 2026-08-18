# Recoverable Tool Output — Design

Date: 2026-08-09
Status: Approved final design
Source: `read-transcript-feedback-2026-08-08.md`

## Purpose

Make every truncated text tool result recoverable without rerunning the tool. During the current root session tree, an agent must be able to retrieve any targeted omitted line in one additional `read_transcript` search call and reconstruct every omitted byte by paging.

The first implementation stays small. It reuses the full output and job-log readers that Evener already has. It adds one temporary output store, one truncation flag, one capture hook, and two read operations.

## Current behavior

The registry already returns two forms of most text results:

- `tool.ExecResult.Output` is the bounded result sent to the model.
- `tool.ExecResult.FullOutput` is the unbounded event-facing result. For `TextResult`, a tool may override it with text that differs from the pre-limit model-facing result.

The registry does not preserve the exact pre-limit model-facing result in a distinct field. `Session.execTool` emits `FullOutput` through `TOOL_CALL_END`, but the model cannot query that event stream. The truncation warning therefore points to data without giving the model an address.

`read_transcript` also limits `job:` refs to one rendered tail. The job store already implements head reads, backward windows, and regex scans over the retained log. The missing work is chiefly an API bridge.

Job output and generic tool output have different retention contracts:

- A `job:` log persists across restarts but retains at most the existing 8 MiB tail. Earlier bytes may be gone.
- An `artifact:` result retains every byte but expires when its root session tree closes.

The reader must state this difference plainly.

## Design principles

1. **Every generic truncation returns a handle.** A warning without an address is a defect.
2. **One reader handles retained evidence.** Keep `read_transcript`; dispatch by ref kind.
3. **Store exact bytes once.** Carry the pre-limit model-facing bytes through `ExecResult.RecoverableOutput`; do not rerun or reserialize a tool.
4. **Keep the first API narrow.** Add fixed byte paging and regex search with line context. Defer convenience grammars.
5. **Report loss honestly.** `artifact:` means complete output. `job:` means complete retained output and may report a dropped prefix.

## Reference kinds

`read_transcript` accepts three kinds of reference:

| Ref | Data | Lifetime | Completeness |
| --- | --- | --- | --- |
| Session ref | Semantic transcript | Durable | Existing transcript contract |
| `job:<job_id>` | Shell or delegate job output | Durable | Existing retained 8 MiB tail |
| `artifact:<id>` | Generic truncated tool result | Current root session tree | Exact full result |

The tool keeps its current name for compatibility. Its description changes from “read transcript” to “read retained evidence by reference.”

## Minimal artifact store

Each root session owns one concurrency-safe artifact store. Every descendant inherits the same pointer through private spawn configuration. Root shutdown closes the store only after descendant shutdown finishes, then removes its temporary directory. Independent root sessions never share stores.

The store uses ordinary temporary files. It needs only these operations:

```go
type ArtifactStore interface {
    Put([]byte) (string, error)
    Open(ref string) (io.ReadSeekCloser, error)
    Close() error
}
```

The concrete store may use narrower internal methods, but it must not grow a metadata database, retention scheduler, or per-tool adapter. The file length supplies `total_bytes`. The ref contains an opaque, unguessable identifier and no path or tool argument.

The store has no size cap. This is an explicit product decision: generic tool output remains complete until root-tree cleanup. A later disk policy requires a separate design because any cap weakens the invariant.

## Capture point

Add `Truncated bool` and `RecoverableOutput string` to `tool.ExecResult`. `truncateResult` sets `RecoverableOutput` to its exact input before applying limits and sets `Truncated` when a configured line or character limit removes bytes.

The separate fields matter. `TextResult` may intentionally use different model-facing `Output` and event-facing `FullOutput` strings. `RecoverableOutput` always preserves the pre-limit model-facing bytes; later `FullOutput` overrides do not change it.

After common tool dispatch and before `TOOL_CALL_END`, `Session.execTool` handles a truncated result:

1. Write `[]byte(res.RecoverableOutput)` to the shared artifact store.
2. Append a short recovery footer to `res.Output`:

   ```text
   Full output: artifact:<id>
   Read with: read_transcript(transcript_ref="artifact:<id>")
   ```

3. Add the same ref to `ToolCallEndData` as an optional `output_ref` field.
4. Return the augmented `res.Output` to the model.

This one hook covers grep, glob, web fetch, file reads, plugin tools, and future registered text tools. Individual executors remain unaware of artifacts. Any session policy that exposes a generically truncatable text tool must also expose `read_transcript`; tool-policy construction enforces this recovery dependency for built-in and plugin-defined agent allowlists.

The store captures only results that generic limits truncated. Untruncated results create no file and gain no footer.

### Retention failure

Generic truncation markers describe only what was removed; they make no availability claim. If `Put` fails, Evener returns the bounded tool result and appends:

```text
[retention_failed: full output could not be retained]
```

Evener must not print “full output is available,” mention the event stream, or invent a ref after a failed write. The tool call itself keeps its original success or error status; retention failure is a separate warning.

## `read_transcript` API

Add two public arguments:

- `output_match` — an RE2 expression for `job:` and `artifact:` refs.
- `context_lines` — lines before and after each match, from 0 through 10; default 0. It requires `output_match`.

Broaden `offset_bytes`:

- Session refs: unchanged; it still requires `expand_turn`.
- `job:` and `artifact:` refs without `output_match`: the zero-based raw byte offset of a fixed 16 KiB page.
- `job:` and `artifact:` refs with `output_match`: the byte offset where a bounded search begins. Omit it to begin at the first available byte.

An `artifact:` read with no operation returns its first page at offset 0. A `job:` read with an explicit `offset_bytes` returns a raw page; the caller uses `offset_bytes: 0` to request the lifetime head. A `job:` read with neither `offset_bytes` nor `output_match` keeps the existing rendered markdown view. This rule preserves delegate structured results and all existing callers while adding an exact paging path.

Do not add these features in the first version:

- `head:` or `tail:` range grammar;
- configurable page size;
- content-type negotiation;
- artifact listing;
- persistent artifact metadata.

Byte offsets already express head reads and continuation. Search results include offsets, so a caller can page around a match.

## Page response

A `job:` page requested with `offset_bytes`, or an `artifact:` page requested with or without it, returns a small JSON envelope:

```json
{
  "transcript_ref": "artifact:…",
  "representation": "raw_bytes",
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

Use the existing UTF-8/base64 rule from transcript expansion: return UTF-8 when the page is valid UTF-8; otherwise return base64. A continuation always names the next raw byte offset. A `job:` envelope also carries `job_status`; an `artifact:` envelope does not, because an artifact is always complete.

For an `artifact:` ref, `retained_start_bytes` is always 0. For a pruned `job:` ref, it is the lifetime offset of the first retained byte. Job-page offsets are lifetime offsets, not offsets relative to the retained file. The first available job page therefore begins at `retained_start_bytes`.

If a caller requests a job offset before `retained_start_bytes`, including `offset_bytes: 0` after pruning, the read returns `output_unavailable` and names the first available offset. The caller can then start at `retained_start_bytes`. A raw job page never pretends that retained output begins at lifetime offset 0.

## Search response

`output_match` scans complete lines from `offset_bytes` and returns matching lines, byte offsets, and requested context:

```json
{
  "transcript_ref": "job:…",
  "output_match": "FAIL|panic",
  "context_lines": 3,
  "offset_bytes": 0,
  "retained_start_bytes": 0,
  "total_bytes": 42000,
  "search_complete": true,
  "skipped_partial_prefix": false,
  "matches": [
    {
      "line_start_byte": 18201,
      "before": ["…"],
      "line": "FAIL package/example",
      "after": ["…"]
    }
  ]
}
```

Search uses RE2 and the repository's existing bounded line-reading primitives. `line_start_byte` is the absolute offset of the returned complete line. `context_lines` follows the grep tool's existing 0–10 convention.

Each call stops before either 100 matches or 64 KiB of serialized match data. If unevaluated lines remain, `search_complete` is false and `continuation.offset_bytes` names the first line not yet evaluated for a match. Context lookahead may read that line, but it does not advance the continuation; the next call may repeat context but cannot skip a match. These pre-materialization bounds prevent regex or context amplification from building an unbounded in-memory response. The 64 KiB bound also keeps the envelope below `read_transcript`'s existing backstop, so search never relies on recursive generic truncation.

If the bounded scanner skips an oversized line, the response reports the skip and its byte interval. Search never silently claims that it scanned every retained line.

For a pruned job, search covers only retained bytes and reports nonzero `retained_start_bytes`. Retention persists whether the retained start follows a newline: only a suffix marked mid-line skips bytes through its first retained newline and sets `skipped_partial_prefix=true`; a line-aligned retained first line remains searchable. Raw paging always exposes the retained bytes. Legacy retained metadata that lacks this boundary marker is conservatively treated as mid-line when it has a pruned prefix. Search never labels a retained suffix as a complete line or describes the result as a scan of the full lifetime log.

Raw paging and search also apply to running jobs. Job offsets are lifetime offsets, so an append never invalidates a page or a continuation; it only extends EOF. Each call is a point-in-time snapshot taken under the store lock. Every job envelope reports `job_status` (`running` or `terminal`) so the caller knows whether more output may follow; `total_bytes` is the snapshot value at read time. If pruning outruns a reader between calls, the next call fails `output_unavailable` and names the first available offset, exactly as after any other pruning.

A running job's retained output may end in an incomplete line. Search never evaluates that trailing fragment — it may still be growing and could match differently once complete. Instead the continuation names the fragment's start, so a later call evaluates the completed line without a gap. On a terminal job the trailing unterminated line counts as complete and is evaluated. The existing bounded grep primitive evaluates the EOF fragment unconditionally, so the bridge adds an option to defer it while the job runs. Raw paging has no such rule; it returns whatever bytes exist. For a running job, `search_complete=true` means every complete retained line at snapshot time was evaluated.

The existing no-argument markdown snapshot remains available while a job runs.

## Validation and errors

The reader rejects unsupported combinations instead of ignoring them.

- `context_lines` without `output_match` → `invalid_request`.
- `context_lines < 0` or `context_lines > 10` → `invalid_request`.
- Invalid RE2 → `invalid_request` with the compile error.
- `output_match` on a session ref → `invalid_request`; session search is out of scope.
- `range` or `expand_turn` on `job:` or `artifact:` → `invalid_request`.
- Any explicit `format` on an `artifact:` ref → `invalid_request`; artifact reads use the raw page or search envelope.
- `format=outline` or `format=jsonl` on a `job:` ref → `invalid_request`.
- `offset_bytes` or `output_match` combined with any explicit `format` on a `job:` ref → `invalid_request`.
- Offset before a job's `retained_start_bytes` → `output_unavailable` with the first available offset.
- Offset beyond EOF → `invalid_request` with the valid byte interval and, for a running job, its status — the byte may simply not exist yet, and `job_watch` covers waiting for it.
- Malformed artifact ref → `invalid_request`.
- Well-formed but unknown artifact ref → `artifact_expired`.

A ref from a closed or different root session tree is expected to expire. Persisted transcripts may contain such refs; the explicit `artifact_expired` error is the contract.

## Compatibility

Existing session-ref reads do not change. Their formats, ranges, turn expansion, fixed page size, and continuation behavior remain intact.

Existing `job:` calls without `offset_bytes` or `output_match` keep the rendered markdown view. This preserves the current shell-log presentation and the delegate job's appended `structured_result`. Passing `offset_bytes`, including zero, selects raw paging. Passing `output_match` selects search.

Generic tools need no schema or executor changes. Their only visible change occurs on truncation: existing structural truncation markers, including `[Tool output was truncated.]`, remain intact; the result additionally gains an exact byte count, an artifact ref, and a concrete next call. The structural marker describes the inline truncation, while the separate artifact receipt describes recovery.

Replace vague text such as:

> The full output is available in the event stream.

with:

> Output truncated: showing 20,000 of 87,412 characters. Full output: `artifact:…`. Read with `read_transcript(transcript_ref="artifact:…")`.

## Security

Artifact refs must be unguessable. A reader may resolve only refs in its root session tree's artifact store. Never accept a path as a ref, and never derive a path from unchecked ref text.

The store writes exact tool output, which may contain secrets. Create its directory and files with owner-only permissions. Do not record tool arguments or duplicate output in metadata. Root-tree cleanup removes the whole directory after descendant shutdown.

Sandboxed tools do not gain filesystem access through artifact refs. The Evener host reads artifacts and returns bounded content through `read_transcript`, as it already does for durable job logs.

## Testing

Read `docs/testing.md` before changing tests. All tests use synthetic output and local temporary directories; none requires network access or provider credentials.

### Registry and capture

- A result below its limits sets `Truncated=false`, creates no artifact, and keeps its output unchanged.
- Character, line, head-tail, and tail truncation set `Truncated=true`.
- An intentional `TextResult{Output, FullOutput}` difference without generic truncation creates no artifact.
- A truncated `TextResult` whose `Output` and `FullOutput` differ stores bytes exactly equal to the pre-limit `Output` in `RecoverableOutput` and appends one valid handle.
- A store failure produces `retention_failed`, no false handle, no event-stream availability claim, and no change to the tool's original error status.
- Built-in and plugin agent policies cannot expose a generically truncatable text tool without also exposing `read_transcript`.

### Artifact reads

- Page a multi-page artifact from offset 0 to EOF and reconstruct the exact original bytes with no gaps or overlap.
- Preserve valid UTF-8 boundaries; use base64 for invalid UTF-8 pages.
- Search returns matching lines, absolute byte offsets, and 0, 1, and 10 lines of context.
- Search stops before 100 matches or 64 KiB, then returns a gap-free continuation at the next complete line.
- Invalid refs, stale refs, invalid RE2, invalid context, and invalid offsets return the specified errors.
- Concurrent puts and reads pass under the race detector.
- Root close waits for descendant shutdown, then closes the store and removes its temporary directory without affecting another root tree.

### Job reads

- Page an unpruned job from offset 0 to EOF without gaps or overlap.
- Page a pruned job from `retained_start_bytes` and report the dropped prefix.
- Reject an offset in a pruned prefix.
- Search retained job output with context and absolute lifetime offsets.
- Skip and report the first retained fragment when pruning starts mid-line.
- Page a running job, append more output, then continue from the returned offset without gaps or overlap; pass under the race detector with a concurrent writer.
- Search a running job whose retained output ends mid-line: the fragment is not evaluated, the continuation names its start, and a later call evaluates the completed line exactly once.
- Search a terminal job evaluates its trailing unterminated line.
- A running job's envelopes report `job_status`; the no-argument markdown snapshot remains available throughout.
- Keep the default delegate-job markdown view and its appended structured result unchanged. Raw paging and search cover the retained delegate report bytes only.

### Receipt replay

Run a real workspace grep whose broad pattern produces enough output to exceed the inline limit while its matching lines fit within one bounded search response. Assert that one additional call using the same broad pattern,

```text
read_transcript(
  transcript_ref="artifact:…",
  output_match="ORIGINAL_BROAD_PATTERN",
  context_lines=3
)
```

returns every expected matching line, including lines omitted from the preview, without rerunning grep or narrowing the original pattern. A separate stress test searches more than 100 matches and verifies bounded, gap-free continuation rather than one-call materialization.

## Acceptance criteria

The change is complete when all these statements hold:

1. Every generic text result truncated by Evener returns an `artifact:` handle or an honest `retention_failed` warning.
2. During the current root session tree, paging an artifact can reconstruct every original byte exactly.
3. `read_transcript` can page and search `artifact:` and retained `job:` output, including output of running jobs, with snapshot semantics and reported `job_status`.
4. Search on a running job never evaluates a growing partial line; continuation guarantees it is evaluated once complete.
5. Search supports 0–10 context lines and returns byte offsets.
6. A pruned job reports its unavailable prefix; it never claims complete lifetime output.
7. The grep failure from the source report is solvable in one additional search call using the original broad pattern, without rerunning or narrowing the search.
8. Existing session transcript behavior remains unchanged.
9. Every session that can receive an artifact handle can call `read_transcript`.

## Out of scope

- Persisting artifacts after the root session tree closes.
- Limiting artifact disk use or pruning artifacts before root-tree cleanup.
- Searching semantic session transcripts.
- Changing the 8 MiB job-retention cap.
- Adding head/tail line-range syntax.
- Adding per-tool artifact formats or metadata.
- Replacing `read_transcript` with a renamed reader.
