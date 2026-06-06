# transcript-find-by-query-content-search: find({query}) returns records with snippets + scan stats

**What this covers**: the **content-search** filter on the new
`find_session_transcripts` tool (`docs/tools/transcripts.md`) — the
new-shape complement to the existing `⌘K` overlay scenario
(`search-finds-content-across-sessions.md`, which tests the web/server
search, NOT this agent tool). With a `query`, `find` matches session
metadata first, then opens transcripts for a **bounded** raw-text scan
(200 newest), returning per-record `snippets`, plus `scanned` and
`scan_truncated` reporting the scan's coverage. With no query it scans
nothing; this scenario asserts the query path turns the scan ON and
reports it honestly.

## Pre-state

- Built serf binary (`go build -o /tmp/serf ./cmd/serf` if absent).
- Creds exported into the child env (bare `.env`, so `set -a` is
  mandatory):
  ```bash
  set -a; . "$PWD/.env"; set +a
  ```
- A live model (`oai-work/gpt-5.5`).
- State persistence on (default for the CLI) — transcript tools are only
  wired in when it is.

## Steps

1. One shared project dir for all runs (same `--dir` ⇒ same bucket ⇒
   `current_project` search sees them all):
   ```bash
   proj=$(mktemp -d -t serf-e2e-query-XXXXX)
   ```

2. Seed the bucket with a session containing a **rare, distinctive
   token** unlikely to collide with any other session's content. Use a
   nonce so reruns don't match each other:
   ```bash
   nonce="zarquon$(date +%s)"
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" \
     "Create a file named notes.txt whose only contents are the single word ${nonce}. Then read it back with cat and report the word you wrote."
   ```
   Wait for exit 0. Sanity: `grep -q "$nonce" "$proj/notes.txt"`.

3. Add a couple of decoy sessions in the same bucket that do NOT mention
   the nonce, so a match proves content discrimination rather than
   "returns everything":
   ```bash
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" \
     "Print the current date with the date command and report it."
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" \
     "List the files in the current directory with ls and report the count."
   ```

4. A final serf run searches by the nonce. It is NOT told which session
   contains it — it must find the match by content:
   ```bash
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" \
     "Call find_session_transcripts with query set to '${nonce}'. Report: (a) how many matches came back and each match's transcript_ref, (b) the snippet text on the matching record, (c) the values of scanned and scan_truncated on the response. Do NOT read any session — just report the find result."
   ```

## Expected

- After step 4, the final report shows:
  - At least one match, whose `transcript_ref` is the `local:<id>` of the
    session from step 2 (the only one mentioning the nonce). The decoy
    sessions from step 3 do NOT appear in the matches.
  - A `snippets` field on the matching record carrying an excerpt that
    contains the nonce — `snippets` appear on **search results only**.
  - A `scanned` integer (number of transcripts opened for the raw scan)
    and a `scan_truncated` boolean. With only a handful of sessions in
    the bucket, `scanned` is small and `scan_truncated` is `false`
    (the bounded scan caps at 200 newest; we are well under that).
- Falsification:
  - The decoys appear among the matches → the query is not actually
    filtering by content (returning the catalog regardless of `query`).
  - No `snippets` on the matching record → the content scan didn't run,
    or its excerpts were dropped.
  - `scanned`/`scan_truncated` absent when a `query` was supplied → the
    scan-coverage reporting regressed (these are required with a query
    and forbidden without one).
  - Zero matches for a nonce that is provably in a transcript in this
    bucket → content search broke, or the bucket isn't shared (re-check
    that every run used the same `--dir`).

## Cleanup

```bash
rm -rf "$proj"
```

## Sharp edges

- `query` is a **case-insensitive substring** match — no regex, no
  boolean operators. Don't write a query with `|`/`*`/quotes expecting
  operator semantics; pass a plain literal token. The nonce avoids any
  casing or tokenization ambiguity.
- Metadata matches first, then content. The nonce appears in the
  session's *content* (the file it wrote and read back), not just its
  title, so this exercises the raw-text scan, not only the metadata
  match. If you want to also exercise the metadata path, put the nonce
  in the prompt's first sentence (it becomes the session title/prompt
  metadata).
- The bounded scan is **200 newest** sessions. To observe
  `scan_truncated:true` you'd need a bucket with >200 sessions where the
  match is older than the 200th-newest — out of scope here; this
  scenario asserts the *false* case and the presence of the field.
- This is the agent-tool path (`find_session_transcripts`), distinct
  from the `/api/search` + `⌘K` overlay covered by
  `search-finds-content-across-sessions.md`. They share intent (content
  search) but are different surfaces; a regression in one need not break
  the other.
