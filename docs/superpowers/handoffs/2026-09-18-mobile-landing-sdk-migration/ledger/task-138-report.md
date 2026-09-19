# Task 138 — the process-group library self-check

Status: done. One commit, pushed, PR body has a "## Self-check" section.

- `ce34e7423` ci: prove the process-group library against real processes
- Pushed head: `ce34e7423`
- Runtime: 7s (`make lint-process-group`); full `make lint` green.
- CI job: **lint-repository** (`.github/workflows/ci.yml`), whose step now runs
  `make lint-generated lint-naming lint-gofmt lint-internal lint-process-group
  secret-scan`. `make lint` includes it through LINT_TARGETS.

11 cases, one per row of the state table, all real processes; the only shim is a
failing `ps` for the unreadable-listing case. Jobs spawn in their own group and
are stopped by that group id; scratch comes from scratch-lib (no `rm -rf`).

RED mutations, each proved by hand before the commit (one line each, in
process-group-lib.sh):
```
live group stopped          stop_pgroup call removed
group already gone          empty-group branch keeps the record
pre-split pid stopped       stop_pid call removed
record of another script    prefix check disabled (if false)
pid record, unidentifiable  alive-unidentified returns 1 instead of 2
survivor record kept        survivor: arm renamed so it never matches
empty record waits          pgroup_record_value's wait loop disabled
number handed on            pgroup_number_reused call replaced by status=1
refusals                    the 0|1 refusal arm deleted
unreadable listing kept     blind path clears the record
TERM-ignoring child killed  stop_pgroup escalates TERM only, no KILL
```
Each turned its own case red and nothing else (the group-stop mutation also
reddened two dependent cases, which is honest coupling, not a gap).

Could not verify locally: the ubuntu runner. The library reaches for GNU `stat`
(`-c %Y`) and GNU `ps` there; both are exercised by the same code paths, but the
only measurement is macOS. First CI run on this branch is the proof.
