# Task 127 — PR #1145, round 26 Medium (per-member identity)

Status: done and pushed; folded into the round-26 section of the PR body.

- `16127c1fb` ci: stop a recorded group unless its number has been handed on
- Pushed head: `49a4a9416` (merge of `origin/main` @ `eeff54b70`)

Reproductions (real processes, each job in a group of its own, group id checked
against the shell's before any signal, cleanup by that group id only):
```
(a) leader dead, orphan alive  -> status 0 ; members left: [] ; record removed
(b) number reused after record -> status 1 ; stranger alive: yes ; record removed
(c) listing will not run       -> status 2 ; record kept
```

Time field: `ps -o etime=`. Its [[dd-]hh:]mm:ss format is fixed on macOS and
Linux and needs only arithmetic, where `lstart=` is a locale-formatted date whose
parsing differs between BSD `date -j -f` and GNU `date -d`. Start epoch is
`now - elapsed`, compared with the record's mtime (`stat -f %m`, falling back to
`stat -c %Y`), with two seconds of slack erring towards "ours".

Deleted `pgroup_owned_by` and `pid_leads_pgroup` (dead, and advertising a
guarantee the platform cannot give). Kept `pid_owned_by` for `pid:` records. The
header now states the residual — a number reused after our group emptied whose
new leader has since exited, leaving descendants, reads as ours — and what
`pgroup_signalable` actually enforces (0, 1, non-number, own group; its session
check is a no-op on macOS).

Gates: `bash -n` on all three before and after the merge; envvars PASS 0.59s,
agent PASS 17.33s; `make tools-golangci` real network plus the slow-curl hand-run
(5 members before the trap, 0 after); `make lint-generated` PASS.

Concerns:
- Fixture-building fact worth keeping: in this sandbox a session-detached process
  (`setsid`) does not survive, and `ps -p N` sees nothing from a tty-less shell.
  Case (b) therefore uses a same-session group leader started after the record —
  which is what the check actually tests. The `ps -E`/`/proc` facts stand.
- Linux remains unverified by me (no Linux host available); the etime and stat
  fallbacks are written for it but only macOS was exercised.
