# transcript-multi-session-create-find-read: Session B discovers and reconstructs Session A by content

**What this covers**: THE headline capability of the new session-
transcript tools (`docs/tools/transcripts.md`) — one serf session
locating and reading **another** serf session it was never told about.
Session A (run in project dir X) performs a distinctive task. Session B
(a separate serf run in the SAME dir X) uses
`find_session_transcripts({query})` to locate A by content, gets A's
`transcript_ref`, and `read_session_transcript`s it to reconstruct what
A did. The non-negotiable assertion: **B had no a-priori handle on A —
it discovered A's ref from content, then consumed it.** This is the
cross-session find/read seam working end to end.

## Pre-state

- Built serf binary (`go build -o /tmp/serf ./cmd/serf` if absent).
- Creds exported into the child env (bare `.env`; `set -a` mandatory or
  `oai-work` drops out with `unknown provider: oai-work`):
  ```bash
  set -a; . "$PWD/.env"; set +a
  ```
- A live model (`oai-work/gpt-5.5`).
- State persistence on (default for the CLI) — the transcript tools are
  only wired in when state persistence is enabled.

## Bucket-sharing precondition (the load-bearing setup)

Sessions are stored in a per-project **bucket** under
`~/.local/state/serf/projects/<bucketHash>/sessions/`, where
`<bucketHash>` is derived from the working dir / git origin. **Two serf
runs invoked with the same `--dir` land in the same bucket.** That shared
bucket is the entire reason B's `current_project` search can see A.
This scenario makes that explicit: A and B use one `$proj`, and we assert
both transcripts live under the same bucket before B searches.

## Steps

1. Create ONE project dir; both runs use it:
   ```bash
   proj=$(mktemp -d -t serf-e2e-xsession-XXXXX)
   ```

2. **Session A — a distinctive, identifiable task.** Use a unique marker
   so B's content search is unambiguous:
   ```bash
   marker="lighthouse-$(date +%s)"
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" \
     "Create a file named beacon.txt containing exactly the line: project codename ${marker}. Then create a Python script summarize.py that reads beacon.txt and prints its contents in uppercase. Run summarize.py with python3 and let the output through. Finally report the codename and what the script printed."
   ```
   Wait for exit 0. Sanity-check A's work:
   ```bash
   grep -q "$marker" "$proj/beacon.txt" && python3 "$proj/summarize.py"
   ```

3. **Confirm the bucket is shared** before B runs. Both A's transcript
   and (after step 4) B's must sit under the same bucketHash. Capture the
   bucket that now contains A's transcript:
   ```bash
   bucket=$(find ~/.local/state/serf/projects -name "*.transcript.jsonl" \
     -newermt "@$(( $(date +%s) - 600 ))" -path "*/sessions/*" \
     -exec grep -l "$marker" {} \; 2>/dev/null | sed -E 's#.*/projects/([^/]+)/sessions/.*#\1#' | sort -u)
   echo "A is in bucket: $bucket"   # exactly one bucketHash expected
   ```

4. **Session B — discover and reconstruct A.** B is given the search
   term but NOT A's ref. It must find the ref, then read it:
   ```bash
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" \
     "Another serf session in this project recently worked on something with the codename ${marker}. You do NOT know its transcript ref. Steps: (1) call find_session_transcripts with query '${marker}' to locate that session and report the transcript_ref you got back; (2) call read_session_transcript on that ref and reconstruct, from the conversation, exactly: the filename it created for the codename, the name of the Python script it wrote, and the uppercase line that script printed. Report all three plus the transcript_ref. Do not guess — read the transcript."
   ```

5. Confirm B's own transcript also landed in the same bucket (proving
   "same dir ⇒ same bucket", not a coincidence):
   ```bash
   ls ~/.local/state/serf/projects/"$bucket"/sessions/*.transcript.jsonl | wc -l   # >= 2 now (A and B, plus any others)
   ```

## Expected

- After step 2: `$proj/beacon.txt` contains the marker line and
  `summarize.py` prints the uppercased codename.
- After step 3: exactly one `bucketHash` contains A's transcript.
- After step 4, B's report includes:
  - A `transcript_ref` it obtained from `find_session_transcripts`
    (a `local:<id>`), explicitly NOT supplied in B's prompt — the
    discovery is the point.
  - The correct filename (`beacon.txt`), script name (`summarize.py`),
    and the uppercased printed line (e.g. `PROJECT CODENAME LIGHTHOUSE-…`)
    — all reconstructed by reading A's transcript, not invented.
- After step 5: B's transcript is in the same `$bucket` as A's
  (count ≥ 2), confirming the bucket-sharing precondition held.
- Falsification:
  - B cannot find A despite the marker being in A's transcript and both
    sharing a bucket → cross-session `current_project` find regressed
    (or the bucket key changed so same-`--dir` runs no longer share).
  - B reports A's details correctly but its report shows it was *handed*
    A's ref (e.g. the ref was hard-coded in the prompt) → the scenario is
    not exercising discovery; rewrite so B must find it.
  - B fabricates the codename / filenames without a `read_session_transcript`
    call → it answered from the snippet or hallucinated instead of reading.
  - A and B end up in different buckets despite identical `--dir` → the
    bucket-derivation invariant regressed.

## Cleanup

```bash
rm -rf "$proj"
```

Bucket dir under `~/.local/state/serf/projects/$bucket/` lingers but is
harmless; delete its `sessions/` entries for a hermetic rerun.

## Sharp edges

- **Same `--dir` is the whole trick.** If you let either run default to a
  different cwd, or `git init` inside `$proj` between runs, the bucket
  hash changes and B's `current_project` search will not see A. Keep both
  runs pointed at the exact same `$proj` with no git origin introduced.
- B's `find` may surface A via a metadata match (the marker is in A's
  prompt/title) OR via the content scan (the marker is in the files A
  wrote and echoed). Either is a valid discovery; the assertion is that B
  obtained the ref from the tool, not the prompt.
- `is_current` marks B's own live session and sorts it last, so B won't
  mistake itself for A. If B reports finding "only itself," A likely
  didn't persist a transcript — re-check step 2 exited 0.
- The CLI runs are non-interactive and exit after `communicate`; there is
  no daemon to poll. Run them sequentially (A fully exits before B
  starts) so A's transcript is flushed to disk before B searches.
- For the cross-*project* (different directory) variant of discovery, see
  `transcript-find-scope-all-projects.md`, which exercises
  `scope:"all_projects"` and the `proj:<bucketHash>:<id>` ref form.
