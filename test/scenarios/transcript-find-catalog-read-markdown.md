# transcript-find-catalog-read-markdown: catalog lists a past session, markdown read reconstructs it

**What this covers**: the baseline navigation loop for the two new
session-transcript tools (`docs/tools/transcripts.md`):
`find_session_transcripts({})` returns the **catalog** (recent sessions,
newest-first, metadata-only — no content scan), and
`read_session_transcript({transcript_ref})` renders the **markdown**
default (last 40 turns) with a self-announcing window header. A second
serf run, sharing the first run's project bucket, picks the earlier
session out of the catalog by its title / `approx_turns` and confirms
from the rendered conversation what that session actually did. If this
scenario fails, either `find` stopped returning the catalog or the
default markdown read stopped announcing its window.

## Pre-state

- A built serf binary. Build if absent:
  ```bash
  cd /Users/jesse/prime-radiant/toil-suite/serf
  go build -o /tmp/serf ./cmd/serf
  ```
- Repo creds exported into the child's environment. The `.env` is bare
  `KEY=value` (no `export`), so plain `. .env` sets shell vars but does
  NOT pass them to the serf child — you MUST use `set -a`:
  ```bash
  set -a; . "$PWD/.env"; set +a
  ```
- A live model. `oai-work/gpt-5.5` (real API key) is the reliable path;
  `openai/gpt-5.5` (ChatGPT/Codex OAuth) also works for non-codex models.
  Codex models are rejected on the OAuth `openai` instance.
- State persistence enabled (the default for the CLI): the transcript
  tools are only wired in when state persistence is on.

## Steps

1. Pick a single, shared project directory. Both serf runs MUST use the
   **same** `--dir` so they land in the same state-dir bucket (a hash of
   the working dir / git origin) — that bucket-sharing is exactly what
   makes session A visible to session B's `current_project` catalog:
   ```bash
   proj=$(mktemp -d -t serf-e2e-catalog-XXXXX)
   ```

2. **Session A — do a small, identifiable piece of work.** Give it a
   distinctive artifact so the catalog title / content is recognizable:
   ```bash
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" \
     "Create a file named tide_table.py in the current directory that prints the single line 'high tide at noon' when run with python3. Run it to confirm the output, then report what you did."
   ```
   Wait for it to exit 0. Confirm the artifact landed:
   ```bash
   test -f "$proj/tide_table.py" && python3 "$proj/tide_table.py"   # -> high tide at noon
   ```

3. **Session B — list the catalog and read A.** In the SAME project dir,
   ask a fresh serf run to use the transcript tools (it has no prior
   knowledge of A's ref — it must discover it):
   ```bash
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" \
     "Use find_session_transcripts with no arguments to list recent sessions in this project. Identify the earlier session that created a tide table script (by its title and approx_turns). Then call read_session_transcript on that session's transcript_ref using the default markdown format. From the rendered conversation, report: (a) the exact filename that session created, (b) the exact line the script prints, and (c) quote the window header line that read_session_transcript printed at the top of its output."
   ```

## Expected

- After step 2: `$proj/tide_table.py` exists and prints exactly
  `high tide at noon`.
- After step 3, session B's final report states:
  - It called `find_session_transcripts({})` and got back a list of
    records, each carrying a `transcript_ref` (a `local:<id>` ref —
    same-project sessions are `local:`), `kind`, `title`, `updated_at`,
    and `approx_turns`. The catalog is metadata-only: **no `snippets`,
    no `scanned`, no `scan_truncated`** (those only appear with a query).
  - It picked A's ref from that list — NOT a ref it was given in the
    prompt. A's ref must be discovered.
  - From the markdown read it correctly reports filename `tide_table.py`
    and printed line `high tide at noon`.
  - It quotes a window header that names the session and the window,
    e.g. of the form *"Showing turns N–M of T (the last 40)…"*. A
    default read is REQUIRED to announce that it is a window.
- Falsification:
  - `find_session_transcripts({})` returns nothing for a bucket that
    demonstrably contains session A → catalog discovery regressed (most
    likely the bucket key changed so the two same-`--dir` runs no longer
    share a bucket).
  - The catalog records carry `snippets`/`scanned` with no query set →
    the cheap-by-default invariant regressed.
  - The markdown read returns the conversation with **no** window header
    → a default read is masquerading as the whole session (the headline
    honesty invariant).
  - Session B reports the wrong filename / printed line → markdown
    rendering dropped or garbled assistant/tool content.

## Cleanup

```bash
rm -rf "$proj"
```

Session metadata under `~/.local/state/serf/projects/<bucketHash>/`
lingers but is harmless. To purge: find the bucket for `$proj` and
delete its `sessions/` entries.

## Sharp edges

- `approx_turns` is the metadata turn count and is deliberately
  *approximate* — it can differ from the exact `turns_total` an outline
  read reports. Don't assert they're equal; assert the catalog entry is
  recognizably session A (title + rough size), then confirm specifics by
  reading it.
- The live session sorts **last** and is flagged `is_current` so B does
  not audit itself by mistake. If B reports it "found only its own
  session," it likely skipped the `is_current` one and saw an otherwise
  empty bucket — check that A actually wrote a transcript.
- Idle/exit: the CLI run is non-interactive; it exits after the
  `communicate` result. There is no daemon to wait on — just wait for
  the process to return.
- Bucket key: the bucket is derived from the working dir / git origin.
  `$proj` is a fresh `mktemp -d` with no git origin, so both runs hash to
  the same flat-dir bucket. Don't `git init` inside `$proj` between the
  two runs — that would change the bucket and break the precondition.
