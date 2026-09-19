# Task 136 — PR #1145 RoboRev round 30

Status: done. Two commits, main merged, pushed, PR body appended.

- `8c5a93c72` fix(evener-dev): read every spelling of a build flag, and of -short
- `7353de559` ci: drop the record when nothing was spawned
- Pushed head: `b482831eb`

Item 2, measured on `/bin/bash` 3.2.57 (what `#!/usr/bin/env bash` resolves to
here), `x.go`/`y.go` in the directory, array = `-run`, `*`, `two words`:
```
  [-run]
  [*]
  [two words]
empty array through the idiom yields 0 element(s)
```
Each element stays one word and `*` is not expanded — the idiom's alternative
value is the quoted `"${arr[@]}"` — so the finding is refuted and no change was
made. No bash 5 is installed here; that half is unverified, the same Linux gap
carried since round 26.

Item 4 reproduction:
```
before  record kept → cleanup status 2 after 5s: "the job never named itself…"
after   record removed → cleanup status 0 after 0s
```

Gates: `bash -n` all three scripts; envvars PASS 0.62s, `.` PASS 83.26s, agent
PASS 109.25s; `go test ./cmd/evener-dev/...` ok; `make lint-generated` PASS.

Concerns:
- The Go and shell build-flag tables are still two encodings of one list (#1247).
  This round's High was exactly a drift between them, and the Go table test now
  enumerates the shell's set by hand — which is a test that has to be edited
  whenever the shell table changes, not a guarantee that it was.
- `hasShortFlag` reads `-short=false`/`-short=0` as not-short. Go's flag package
  accepts other falsey spellings (`-short=F`, `-short=FALSE`); those would be
  read as short here. Narrow, and the survey only gets slower, never wrong.
