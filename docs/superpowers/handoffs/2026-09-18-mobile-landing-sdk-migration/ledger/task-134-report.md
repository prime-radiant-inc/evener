# Task 134 — PR #1145 RoboRev round 29

Status: done. Four commits (132b's included), pushed.

- `3755982c6` ci: carry the caller's arguments as an array, not a joined string
- `f2e994fd9` ci: identify a job from an untruncated line, and never drop a live unknown
- `6c6a5426c` ci: drop --short under ROOT_FULL too
- `cadaf2840` fix(evener-dev): build the shard binary with the caller's build flags
- Pushed head: `cadaf2840` (merge already up to date at `eeff54b70`)

Item 2 reproduction (decoy file named `-tags` in the repo root, ROOT_FULL=1,
`--short -count=1 -run '*'`):
```
list argv: list ./...                      (the decoy changed nothing)
test argv: test -count=1 -run * -p 5 …     (no -short: --short was dropped)
```
Item 4 reproduction:
```
before  live job, wrong marker → 1 (not ours: the record would be dropped)
after   live job, wrong marker → 2 (unknown: the record is kept)
after   live job, right marker → 0
```
The truncation itself does not reproduce here: `ps` truncates only to a terminal
and this harness always captures, so `COLUMNS=40` changed nothing measurable.
`-ww` plus the spawn program moved into a file beside the record (so the marker
is near the front of a short command line) is what makes it safe at a terminal.

Item 3: build flags now go to `go test -c` and test flags to the shards, both
spellings normalised through one `goFlag`; pinned by a table test
(`TestSplitFlagsSendsBuildFlagsToTheBuild`). `go test ./cmd/evener-dev/...` ok.

Gates: `bash -n` all three scripts; envvars PASS 0.45s, `.` PASS 189.14s, agent
PASS 35.46s; `go test ./cmd/evener-dev/...` ok 24.9s; `make lint-generated` PASS.

Concerns:
- The spawn now writes a wrapper file per attempt (`<record>.wrapper.pl`). It is
  cleared with the record, but it is one more file the spawn can fail on — and it
  does fail the spawn loudly when it cannot be written.
- `splitFlags` carries its own build-flag table in Go beside the shell one in
  `package_list_build_flags`. Two encodings of the same list in two languages;
  I could not see a way to share them without generating one from the other.
