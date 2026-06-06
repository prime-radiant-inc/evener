# Session Transcript Tools

Two read-only agent tools, split along one clean seam — *which session?* vs *show me
this session*:

- **`find_session_transcripts`** — discover sessions across the corpus. Returns refs.
- **`read_session_transcript`** — view one session at the verbosity you ask for.
  Consumes a ref.

The mental model is exactly that: **find returns refs, read consumes a ref.** `find`
takes filters and hands back session refs; it is never given a session to read — you
hand that to `read`. `read` always takes one ref. They replace the old `recall` tool.
This document is the canonical design (workshopped with agent consumers for the
simplest, easiest surface). The historical decision record is
`docs/specs/session-transcript-tools.md`; the agent-facing prompt steer is
`agent/prompts/sections/transcripts.md`.

## The one rule worth memorizing

**Turn numbers are universal.** Every turn has a single `Turn N` number, shown at the
start of each outline line and in every markdown `## Turn N` heading. Those are exactly
the numbers `range` and `expand_turn` accept. There is no second numbering and no
translation step: see `Turn 58` in the outline → read `range:"55-60"` → `expand_turn:58`.

## Why these tools exist

A serf session produces an immutable `.transcript.jsonl`. After compaction, when
auditing a subagent, or when resuming prior work, the agent needs to read that record
back — selectively, because a transcript can dwarf the context window. `find` locates
the session; `read` shows it, escalating from a one-line-per-turn **outline** to
**markdown** to raw **jsonl** as needed. Everything is read-only; transcripts are never
mutated.

## Core concepts

**Transcript.** One append-only `.transcript.jsonl` per session: a header line, then one
line per *entry* (a turn), with `api_call` log lines interleaved. Immutable once written.

**Turn number.** The addressing unit across both tools. A 0-based index over *entry*
lines only (header and `api_call` lines do not advance it). It is what every tool emits
and accepts — outline lines, markdown headings, `range`, `expand_turn` — so they compose
without translation. (Internally this is a derived index, not the on-disk line counter;
that counter is never shown to the agent.)

**Session kind.** Derived, not stored. One of exactly: `root` (top-level), `subagent`
(spawned by another session), or `fork` (branched from another).

**Transcript refs.** Sessions are named by opaque, traversal-safe refs:

- `local:<sessionID>` — a session in the **current** project bucket.
- `proj:<bucketHash>:<sessionID>` — a session in a **sibling** bucket.

Refs are the only session handle the model is given; raw filesystem paths and bare
session IDs are never required. A bare session ID and `"current"`/omitted (the live
session) are also accepted wherever a ref is. The ref parameter is `transcript_ref`
wherever a session is read; ref-valued *filters* on `find` (`children_of`) are documented
as taking a `transcript_ref` too, for one consistent ref vocabulary.

**Buckets and scope.** Each project gets a state-dir *bucket* keyed by a hash of its git
origin. Discovery scope is `current_project` (this bucket; the default) or `all_projects`
(sibling buckets). Under a flat state dir with no project root, `all_projects` degrades to
`current_project` and says so via `scope_applied`.

**Result tool.** The `communicate`-style tool whose call carries a session's final answer.
Its name comes from session metadata (default `communicate`); the renderer shows that
call's `message` as assistant text rather than as a tool card.

## `find_session_transcripts` — which session?

Cross-session discovery, and nothing else. Composable filters, **one** response shape.

**Parameters** (all optional):

- `query` — text to match. Case-insensitive **substring** (no regex, no boolean
  operators); matches session metadata (title, prompt, model, …) and a bounded scan of
  transcript content. Omit for the plain catalog.
- `children_of` — a `transcript_ref`; restrict results to the sessions that ref spawned
  (its subagents and forks). This is the "what did this session spawn" view — a filter,
  not a separate mode or a thing that gets read.
- `scope` — `current_project` (default) or `all_projects`.
- `limit` — max matches (default 10, hard max 50).

Registered `strict:false`, so the model omits the filters it isn't using. There is no
session-selector parameter and no mode switch: catalog, content search, and
children-of-a-parent are just which filters you set, and all return the same records.

**Response:**

```
{ "matches": [ {
    "transcript_ref", "kind", "title", "updated_at", "approx_turns",
    "parent_ref"?, "project"?, "is_current"?, "snippets"? } ... ],
  "scope_applied": "current_project" | "all_projects",
  "scanned"?: int, "scan_truncated"?: bool }
```

- With **no `query`**, `find` runs **no content scan** — metadata only (cheap), scaling
  to thousands of sessions. `snippets`/`scanned`/`scan_truncated` are omitted (meaningless
  without a scan).
- With a **`query`**, it matches metadata first, then opens transcripts for a **bounded**
  raw-text scan (200 newest). When the scan stops early, `scan_truncated:true` reports the
  partial coverage; `snippets` carries the matching excerpts (search results only).
- With **`children_of`**, results are restricted to sessions whose parent is that ref's
  session. The parent's bucket and ID come from the ref alone — **no transcript is opened,
  not even the parent's** — and children are looked up in the parent's own bucket, so a
  `proj:` parent finds its children in that sibling project.
- Only sessions that are actually readable (have a transcript on disk) are returned, so a
  match is always a `read`-able ref.
- `kind` is one of `root` / `subagent` / `fork`. `parent_ref` (a `transcript_ref`, present
  only for non-root sessions) is the lineage handle — pass it back to `read` or as a
  `children_of` filter. `approx_turns` is the metadata turn count and is deliberately
  *approximate*; it can differ from a `read` outline's exact `turns_total`. `is_current`
  flags the live session (which also sorts last), so you don't audit yourself by mistake.

## `read_session_transcript` — show me this session

Views one session at one of three verbosities, selected by `format`.

**Parameters:**

| Param | Applies to | Meaning |
|-------|-----------|---------|
| `transcript_ref` | all | ref / bare id / omitted = current session |
| `format` | — | `outline` \| `markdown` (default) \| `jsonl` |
| `range` | all formats | turn-number window (below); omit for the default last 40 |
| `expand_turn` | markdown | a `Turn N` whose tool results to render in full; omit for none |

Registered `strict:false`, so every parameter is optional. The three formats are one
escalating ladder — outline to see the shape, markdown to read it, jsonl to replay it —
and each returns the same envelope skeleton (`transcript_ref`, `format`, `content`,
format-specific `meta`).

**`range` grammar** (over `Turn` numbers, length = `turns_total`):

- *omitted* → the **smart default**: the last 40 turns.
- `last:N` → the last N turns.
- `start:N` → the first N turns.
- `N-M` → Turn N..M inclusive (N>M clamps to empty).

A **malformed** spec does not error: it falls back to the default, sets
`meta.range_warning`, and surfaces a one-line warning in the content, so the model cannot
silently reason from the wrong window.

### `format: "outline"` — the cheap map

One line per turn: far more scannable than the body, and the right first look at the
*shape* of a session.

```
{ "transcript_ref", "format":"outline", "turns_total",
  "content": "<one line per turn>", "truncated", "elided_turns", "hint" }
```

Each line is dot-separated, empty segments dropped, **starting with its Turn number** (the
number `range`/`expand_turn` take):

```
58 · Assistant · exec_command · "run tests" · ok · 18 lines [truncated]
```

A subagent-lifecycle turn (`spawn_agent`/`wait`/`resume_agent`/`close_agent`) replaces the
size note with one **audit-pivot bracket per lifecycle call**, so the parent→child handle
is right in the map:

```
27 · Assistant · wait · wait[success=true status=completed child=local:01KT…]
```

`range` applies to outline too: a windowed outline (`range:"last:200"`) is how you map a
huge session without dumping all of it. The outline is **always bounded** — over the
conversation budget it keeps a head and tail of lines and drops the middle under an honest
`… N turns elided …` marker — so it is never an unbounded wall even with no range.

### `format: "markdown"` (default) — read it

Condensed conversation: assistant text and thinking in full, tool results truncated.

```
{ "transcript_ref", "format":"markdown", "content_type":"text/markdown",
  "content": "<markdown>",
  "meta": { "turns_total", "range", "turns_rendered", "truncated", "elided_turns",
            "skipped_corrupt_lines"?, "range_warning"? } }
```

The first thing in `content` is a header that **says what you're looking at**: the
session, and — crucially — the window. A default read states it plainly, e.g.
*"Showing turns 84–123 of 124 (the last 40). For the whole shape use format=outline; for
earlier turns set range."* So a default read never silently masquerades as the whole
session.

Rendering rules:

- **Conversation grouping** — `## Turn N — User/Assistant/Steering/Summary/Checkpoint`
  headings (the same `Turn N`). `SYSTEM`/deprecated `TOOL` turns render as a one-line
  omitted note; unknown kinds get a labeled note. Nothing is silently dropped.
- **Reasoning shown** — assistant thinking and text render in full, in recorded order.
- **Tool-call condensation** — calls render as condensed cards under a **Tools** block:
  `- [status] \`name\` — purpose: <X> — input: <summary>`. Purpose comes only from an
  explicit `purpose`/`intent`/`description` arg, never inferred. Input summaries are
  per-tool and never dump file contents or full edit strings.
- **Tool-result pairing** — results pair to calls by **call ID**, never by adjacency. A
  result whose call is not in the rendered slice collects under a **"Tool results without a
  shown call"** section with an honest note — the call is outside the range, or absent after
  a historical repair; not corruption.

### `format: "jsonl"` — replay it (debug hatch)

The verbatim JSONL lines for the range — header plus interleaved `api_call` lines,
including the system prompt and raw API records. It is **noisy and rarely what you want**:
reserved for byte-exact replay or for debugging the transcript format itself. For
comprehension, use markdown. Bounded only by the 200k hard cap (head-only, valid NDJSON).

```
{ "transcript_ref", "format":"jsonl", "content_type":"application/x-ndjson",
  "content": "<raw lines>",
  "meta": { "lines_returned", "truncated", "skipped_corrupt_lines",
            "hint":"raw NDJSON; for comprehension, re-read with format=markdown.",
            "range_warning"? } }
```

## Truncation and size budgets (markdown)

The registry never truncates these tools' output: each format bounds its own output in the
render/outline layer, and the registry's limit (`transcriptToolMaxChars`, 600k) exists only
so it never re-truncates a bounded envelope into invalid JSON — a backstop. The markdown
render layers two bounds under a final cap:

1. **Per-result line clamp.** In the condensed (non-`full`) view, each rendered result line
   is clamped to `resultLineMaxRunes` (300) characters. This bounds a result with very
   *wide* verbatim lines — a base64 payload, a one-line log dump, or a `read_file` of
   another transcript. Keeping one fat result from dominating a card is what stops a single
   turn from crowding out the rest of a range. A width-clamped result is marked `[truncated]`
   in the outline too.

2. **Conversation budget** (`convBudgetChars`, 24k). When the selected range renders larger,
   turns are dropped to fit — **the drop direction follows the range's anchor:**
   - **Tail-anchored** (default, `last:N`): drop the **oldest** turns, keeping the most
     recent. A top marker points at the outline for the dropped span.
   - **Front-anchored** (`start:N`, `N-M`): drop the **newest** turns, keeping the front you
     asked for. A bottom marker gives the exact continue call:
     `… showing turns A–K of your requested A–M; continue with range="K+1-M". …`

3. **Hard cap** (`hardCapChars`, 200k). A rune-safe, head-keeping last-resort cap, for the
   budget-exempt `expand_turn` below.

`meta` reports the bounds honestly: `turns_rendered + elided_turns == turns_total` for the
contiguous in-range window.

**`expand_turn`** names a `Turn N` whose tool results render **in full** — exempt from the
per-result clamp and the conversation budget (but not the 200k hard cap). It is how you see
one big result whole without dropping to `jsonl`. If the named turn falls outside the
rendered range, it is appended as a labeled supplemental section naming its real Turn number.

## The navigation loop

1. **Locate** — `find_session_transcripts({})` (catalog) or `{query}` to pick a session.
2. **Read** — `read_session_transcript({transcript_ref})`: markdown by default, landing on
   the most recent turns (the outcome), with a header naming the window.
3. **Map** — `read_session_transcript({transcript_ref, format:"outline"})` for the shape and
   the turn numbers worth reading.
4. **Detail** — `read_session_transcript({transcript_ref, range})` for a span;
   `{…, expand_turn: N}` for one big result whole.
5. **Descend** — `find_session_transcripts({children_of: transcript_ref})` to enumerate what
   a session spawned, then audit each child the same way.

## Design invariants

- **Read-only.** No tool mutates a transcript. Both are `ReadOnly:true` and wired in only
  when state persistence is enabled.
- **One job per tool.** `find` does corpus discovery and never reads a session; `read` views
  one session and always takes a ref.
- **One turn numbering.** What the outline and markdown show is what `range` and `expand_turn`
  take. No second index is ever exposed.
- **The registry never truncates.** Each format bounds its own output, always rune-safe and
  reported in `meta`.
- **Honest counts and windows.** `turns_rendered + elided_turns == turns_total`; a default
  read announces it is a window; markers never let the model reason from a wrong window.
- **Opaque refs, no paths, traversal-safe.**
- **Cheap by default.** The catalog and the `children_of` filter enumerate metadata only;
  content scans are bounded and flagged when partial.

## Non-goals

- **No mutation** — no write/edit/delete/redact.
- **No within-session content search.** Navigating one session is outline → range; finding
  *which* session mentions something is `find({query})`. If a keyword index of one huge
  session ever earns its place, the clean shape is a query-filtered outline, not a `find` mode.
- **No `find` modes / no `default_read`.** Discovery is filters on one shape; the agent
  constructs the `read` call itself rather than parsing a pre-baked one (consumers found a
  baked next-call more likely to lure them to the wrong match than to help).
- **No exact catalog turn count.** Kept approximate to preserve the metadata-only cost of
  discovery; the exact count is one `read` away.
- **No retroactive ref rewriting.** Transcripts are immutable; new sessions emit refs.
