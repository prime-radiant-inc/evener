# Task 156 — PR #1269 review round 5

Worktree `activity-generations`. `origin/main` already merged (no movement). One commit: **`39eabb199`**
`fix(agent): decide a cached-fold hit from the handle the probe read through`.

## 1. Measurement — in-place, not rename

Every mutation of both journals is an append or an in-place `Truncate` on the writer's own open handle:

| file:line | site |
| --- | --- |
| `agent/internal/jobstore/store.go:660` | `recoverTrailingJSONLineLocked` — drops an incomplete trailing line at open |
| `agent/internal/jobstore/store.go:757` | `rollbackAppendLocked` — rolls a failed append back |
| `agent/internal/delegatestore/store.go:159` | truncate to 0 for a torn version header at open |
| `agent/internal/delegatestore/store.go:175` | truncate an unterminated trailing batch at open |
| `agent/internal/delegatestore/store.go:213` | `rollbackLocked` — truncate on a failed append |

`grep` for `os.Rename` finds nothing over either path, and neither package has any compaction. So there is no inode
swap to make a handle a snapshot of — branch 3 of the brief applies, not branch 2. A rewrite here always shrinks
the file before it regrows.

## 2. The change

No new probes, no locking. `probeTailFromHandle` opens once and answers both questions from that handle — length
and mtime via `f.Stat()`, the recorded bytes via `ReadAt`. `tailProbeMatches` is now a thin wrapper over it, so
there is one reader rather than two.

Both places that can return a folded value **without reading anything** now require the handle's own length and
mtime to match the recorded state: `tryFastHit`, and `refresh`'s same-size true-hit branch. The second was
necessary — fixing only the fast hit changed no outcome, because `refresh` would then serve the same stale fold
from the same stale `info`.

RED for `TestCache_AppendRacingTheStatIsNotServedFromTheCachedFold`, with the handle checks disabled:

```
value = {sum:1180 lines:40}, want the appended lines read too (sum 1725)
 -- the probe's handle reports a longer file than the stat did
```

An append landing between `Get`'s stat and the probe leaves the recorded tail exactly where it was, so the probe
matched, the stale length said nothing had grown, and the caller got a fold missing the whole append. That is a
wrong answer, not just a wrong path.

## 3. The tolerated window, stated on `Cache`

One paragraph naming the writers and what they do, one naming what slips through: a truncate and regrow to the
recorded length with the recorded trailing bytes, inside a single handle's fstat-to-ReadAt window. Why no more
probes: every check is itself two reads, so each new one opens a window of its own. Why no locking: the writers are
separate processes holding nothing a reader could take. And what it costs when it happens — a generation that does
not move, so a continuation is accepted and its position applied to changed content.

The torn-stat and same-size-rewrite tests are untouched and still green.

## Gates

Toolchain gofmt clean; `go vet` host + `-tags evenerfuzz` + `GOOS=windows -tags evenerfuzz`, 0; `golangci-lint`
`0 issues.`; `cmd/evener-tui` and `internal/appprojector` clean; agent suite clean;
`go test -race -count=3 -timeout 150m ./internal/foldcache/... .` clean.
