# transcript-subagent-audit-children-of: children_of enumerates a spawned subagent, then read audits its claim

**What this covers**: the lineage filter and the subagent-audit loop
(`docs/tools/transcripts.md` §"The navigation loop" step 5, and
`agent/prompts/sections/delegation.md` — "you must inspect the
subagent's report before you rely on it"). A parent session spawns a
subagent that does real work and reports back. A later run uses
`find_session_transcripts({children_of:"<parent ref>"})` to enumerate
the child, then `read_session_transcript({transcript_ref:child,
format:"outline"})` + a range read to **judge whether the child actually
did what it claims** — did it run the commands / read the files it says
it did? This is the trust-but-verify path delegation depends on.

## Pre-state

- Built serf binary (`go build -o /tmp/serf ./cmd/serf` if absent).
- Creds exported into the child env:
  ```bash
  set -a; . "$PWD/.env"; set +a
  ```
- A live model (`oai-work/gpt-5.5`).
- Subagent nesting allowed: pass `--max-subagent-depth 1` (the default is
  1; pass it explicitly so the parent can spawn one child).
- State persistence on (default for the CLI).

## Steps

1. Shared project dir (same `--dir` ⇒ same bucket ⇒ the parent and child
   are both discoverable in `current_project`):
   ```bash
   proj=$(mktemp -d -t serf-e2e-subagent-XXXXX)
   ```

2. **Parent session — spawn a subagent to do a checkable task.** The
   parent delegates a small, verifiable job and relays the child's
   report:
   ```bash
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" --max-subagent-depth 1 \
     "Use spawn_agent (blocking=true) to delegate this task to a subagent: 'Create a file inventory.txt in the current directory listing exactly these three words, one per line: apples, oranges, pears. Then run wc -l inventory.txt to confirm it has 3 lines, and report the line count.' After the subagent returns, relay its reported line count and confirm the file exists. Report the subagent's result."
   ```
   Wait for exit 0. Note the parent's session id from the run (or find it
   below). Sanity-check the child's artifact actually exists:
   ```bash
   cat "$proj/inventory.txt"   # apples / oranges / pears, 3 lines
   ```

3. **Auditor session — enumerate the child, then read it.** A fresh run
   audits the delegation. It must (a) find the parent, (b) list the
   parent's children, (c) read the child and judge its honesty:
   ```bash
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" \
     "Audit a recent delegation in this project. Steps: (1) with find_session_transcripts, locate the parent session that spawned a subagent to build an inventory.txt (no query needed if you can spot it in the catalog by title; otherwise query 'inventory'); report its transcript_ref. (2) Call find_session_transcripts with children_of set to that parent's transcript_ref; report the child's transcript_ref and kind (it should be kind 'subagent', parent_ref pointing at the parent). (3) Call read_session_transcript on the child with format outline; from the outline, report which turns ran a write_file/exec_command and whether the wc -l command is actually present. (4) Read a small range around those turns and judge: did the subagent REALLY create inventory.txt and run wc -l, or did it merely claim to? State your verdict (trustworthy / not) with the evidence (the actual tool calls you saw in the child's transcript)."
   ```

## Expected

- After step 2: `$proj/inventory.txt` exists with 3 lines; the parent's
  report relays line count `3`.
- After step 3, the auditor reports:
  - The parent's `transcript_ref` (a `local:<id>`).
  - From `children_of`, the child's `transcript_ref`, with `kind:
    "subagent"` and `parent_ref` equal to the parent's ref. The
    `children_of` lookup is resolved from the **ref alone** — no
    transcript is opened to find the children, not even the parent's.
  - From the child's outline, the Turn(s) that ran `write_file` (the
    inventory.txt creation) and `exec_command` (`wc -l inventory.txt`).
    The lifecycle bracket is not relevant here (the *child* didn't spawn
    anyone); ordinary tool lines are.
  - A verdict of **trustworthy**, justified by the actual tool calls seen
    in the child's transcript (it did create the file and did run
    `wc -l`), not by the child's self-report alone.
- Falsification:
  - `children_of:"<parent ref>"` returns nothing although the parent
    demonstrably spawned a child in this bucket → the lineage filter
    regressed.
  - The child comes back with `kind` other than `subagent`, or
    `parent_ref` not pointing at the parent → kind/lineage derivation
    regressed.
  - The auditor renders a verdict from the child's *self-reported* output
    without reading the child's actual tool calls → it's trusting the
    claim, exactly what the audit loop exists to prevent. (The scenario's
    value is that the auditor's verdict cites the tool calls it SAW.)
  - `children_of` only works when also given a `query`, or requires the
    parent's transcript to be opened → the "no transcript opened, ref
    alone" invariant regressed.

## Cleanup

```bash
rm -rf "$proj"
```

The parent and child both leave transcripts under
`~/.local/state/serf/projects/<bucket>/sessions/`; delete that bucket's
`sessions/` entries for a hermetic rerun.

## Sharp edges

- **`children_of` takes a `transcript_ref`, not a session id alone** (a
  bare id is accepted wherever a ref is, but the documented vocabulary is
  the ref). The auditor must obtain the parent ref from step 1's `find`
  first; it cannot invent it. The child is looked up in the **parent's
  own bucket**, so a `proj:` parent would find its children in that
  sibling project — here everything is `local:`.
- The parent–child handle is also visible directly in the **parent's**
  outline: a `wait`/`spawn_agent` lifecycle turn renders an audit-pivot
  bracket like `… · wait[success=true status=completed child=local:01KT…]`,
  carrying the child ref right in the map. `children_of` is the
  enumerate-from-the-parent view of the same lineage; either route
  reaches the child. A bonus assertion: read the parent's outline and
  confirm the child ref in the bracket matches the `children_of` result.
- Use `blocking=true` on `spawn_agent` so the parent gets the child's
  result JSON in one call (the result carries the child's
  `transcript_ref`). With `blocking=false` the parent must `wait()` —
  more turns, same lineage; either is fine for the audit, but blocking
  keeps the parent transcript simpler.
- `--max-subagent-depth` defaults to 1. If a future change makes the
  default 0, the parent silently can't spawn and step 2 produces no
  child — pass `--max-subagent-depth 1` explicitly (as above) so the
  scenario isn't at the mercy of the default.
- Subagent transcripts are full sessions in their own right; the auditor
  reads the child exactly like any other session (outline → range). There
  is no special "subagent read" mode — one `read` tool, one ref.
