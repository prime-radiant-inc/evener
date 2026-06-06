# transcript-find-scope-all-projects: scope=all_projects finds a session in a different project bucket

**What this covers**: the discovery-**scope** parameter and the
sibling-bucket ref form (`docs/tools/transcripts.md` §"Buckets and
scope"). By default `find_session_transcripts` searches only the
**current_project** bucket. `scope:"all_projects"` widens to sibling
buckets under the same state root, and a match in another project comes
back as a `proj:<bucketHash>:<sessionID>` ref (vs the `local:<id>` form
for same-bucket hits). This scenario creates a session in project dir X,
then from a serf run in a **different** project dir Y (sharing the same
`--state-dir`) shows that the default scope does NOT see X but
`scope:"all_projects"` does — and that X's ref is a `proj:` ref.

## Pre-state

- Built serf binary (`go build -o /tmp/serf ./cmd/serf` if absent).
- Creds exported into the child env:
  ```bash
  set -a; . "$PWD/.env"; set +a
  ```
- A live model (`oai-work/gpt-5.5`).
- State persistence on (default for the CLI).

## Bucket/scope precondition

`current_project` = the bucket for *this* run's working dir;
`all_projects` = sibling buckets under the same state root. To get TWO
distinct buckets that still share one state root (so `all_projects` can
reach across), use two **different** `--dir`s with the **same**
`--state-dir`:

```bash
state=$(mktemp -d -t serf-e2e-state-XXXXX)   # shared state root
dirX=$(mktemp -d -t serf-e2e-projX-XXXXX)    # project bucket X
dirY=$(mktemp -d -t serf-e2e-projY-XXXXX)    # project bucket Y (different)
```

Different working dirs ⇒ different bucket hashes ⇒ Y's `current_project`
search will NOT see X. Same `--state-dir` ⇒ both buckets live under one
root ⇒ `all_projects` can reach X from Y.

## Steps

1. **Session in project X** — a distinctive marker, written under the
   shared state root:
   ```bash
   marker="cross-proj-$(date +%s)"
   /tmp/serf --model oai-work/gpt-5.5 --dir "$dirX" --state-dir "$state" \
     "Create a file flag.txt containing exactly: ${marker}. Read it back with cat and report the marker."
   ```
   Wait for exit 0. Confirm two distinct buckets exist under the shared
   state root after the next step.

2. **Session in project Y — default scope must MISS X.** Same state
   root, different dir:
   ```bash
   /tmp/serf --model oai-work/gpt-5.5 --dir "$dirY" --state-dir "$state" \
     "Call find_session_transcripts with query '${marker}' and the DEFAULT scope (do not set scope). Report how many matches came back. Then explain in one line whether a session from a different project directory would be visible at the default scope."
   ```

3. **Session in project Y — all_projects must FIND X.** Another Y run:
   ```bash
   /tmp/serf --model oai-work/gpt-5.5 --dir "$dirY" --state-dir "$state" \
     "Call find_session_transcripts with query '${marker}' AND scope set to 'all_projects'. Report: (a) the number of matches, (b) the transcript_ref of the match and whether it is a local: ref or a proj: ref, (c) the scope_applied value on the response. Then call read_session_transcript on that ref and report the marker the matched session wrote."
   ```

## Expected

- After step 1: `$dirX/flag.txt` contains the marker; X's transcript is
  under `$state/projects/<bucketX>/sessions/`.
- After step 2 (default scope from Y): **zero** matches for the marker —
  X is in a different bucket and the default `current_project` scope does
  not cross buckets. The agent states a different-project session is not
  visible by default.
- After step 3 (`all_projects` from Y):
  - At least one match for the marker.
  - The match's `transcript_ref` is a **`proj:<bucketHash>:<id>`** ref —
    NOT `local:` — because it lives in a sibling bucket.
  - `scope_applied` is `"all_projects"`.
  - The follow-up `read_session_transcript` on that `proj:` ref returns
    X's session and the agent reports the correct marker — proving a
    `proj:` ref is read-able cross-bucket.
- Falsification:
  - Step 2 returns the X match at the **default** scope → scope isolation
    regressed (the default is leaking into sibling buckets).
  - Step 3 returns zero matches → `all_projects` widening regressed, or
    the two runs didn't actually share a `--state-dir`.
  - The step-3 match comes back as a `local:` ref → the sibling-bucket
    ref form regressed (a cross-bucket hit must be `proj:`).
  - `scope_applied` is not `"all_projects"` when `all_projects` was
    requested and a project root exists → scope reporting regressed.
  - `read_session_transcript` on the `proj:` ref fails to open X → the
    `proj:<bucketHash>:<id>` ref isn't traversing to the sibling bucket.

## Cleanup

```bash
rm -rf "$state" "$dirX" "$dirY"
```

## Sharp edges

- **The shared `--state-dir` is what makes this work.** Two different
  `--dir`s alone, under each user's default XDG state root, would still
  be siblings there — but pinning an explicit shared `$state` makes the
  precondition hermetic and obvious, and keeps the test from polluting
  the real state root. Don't omit `--state-dir`.
- Under a **flat** state dir with no project root, `all_projects`
  degrades to `current_project` and says so via `scope_applied`. This
  scenario uses real per-project buckets (distinct `--dir`s), so
  `scope_applied` should faithfully echo `all_projects`. If you instead
  ran with a degenerate flat layout, expect `scope_applied:
  "current_project"` even though you asked for `all_projects` — that's
  the documented degradation, not a bug.
- `proj:` refs are opaque and traversal-safe; the agent never needs a
  filesystem path. If a step tries to convert a `proj:` ref into a path
  to read it, that's a smell — `read_session_transcript` consumes the ref
  directly.
- Steps 2 and 3 are separate runs so each starts a clean session; you
  could also ask both questions in one Y run, but separating them keeps
  the default-miss and all_projects-hit assertions independent.
- The marker is in X's content (file written + echoed) and its
  prompt/title, so either the metadata match or the content scan can
  surface it under `all_projects`. Either is a valid cross-project
  discovery.
