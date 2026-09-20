# Task 148 — PR #1269 review round 1

All four findings addressed. Pushed head **`bbe186e19`**, PR #1269 open, body appended with "## Review round 1".

## Commits

| SHA | Finding | Message |
| --- | --- | --- |
| `8f497cef6` | 1 (Medium, foldcache) | `fix(agent): stat again before trusting a same-size file that kept its tail` |
| `c78ac6a61` | 3 (Medium, fixture) | `test(agent): move the nested-walk generations by removing the journals, not restamping them` |
| `6055b93b5` | 2 + 4 (docs) | `docs(agent): point the depth bound at the frontend issue and drop references to deleted code` |
| `74dd98971` | — | `Merge origin/main into claude/activity-continuation-generations` (main had moved to `468ee7950`) |
| `bbe186e19` | — | `test(agent): write the rewrite fixture loop as a range over int` (golangci-lint modernize) |

## Finding 1 — RED → GREEN

New test `TestCache_SameSizeRewriteKeepingItsTailBumpsTheGeneration`: folds 40 three-byte lines, rewrites the first
18 in place so the size is identical and the last 66 bytes — more than the 64 the probe reads — are byte-identical,
then stamps a distinct mtime.

RED at `f206969c0`:
```
generation = 0 after a same-size rewrite that kept its trailing bytes, want 1 -- the fold this cache held is gone,
so a continuation keyed to it must not be accepted
```
GREEN after `8f497cef6`. `TestCache_TornAppendStatKeepsTheGeneration` (the other half) stays green unchanged.

The implementation restructures the same-size branch into three cases: tail gone → bump; tail intact and mtime
agrees → true hit or a zero-offset reread with the generation held; tail intact and mtime moved → `os.Stat` again
and bump unless the file has grown.

**One deviation from the reviewer's wording**, deliberate: the growth test compares the fresh size against the
recorded **size**, not the recorded **offset**. A fold that stops short of a torn trailing line records an offset
below the file's size, so `fresh.Size() > st.offset` is already true with no growth at all and would re-admit
exactly the false acceptance this fixes. `fresh.Size() > st.size` is the growth test.

A stat error in that branch is returned, matching how the tail probe's own error is handled; a file deleted between
the two stats is a real condition the caller has to see.

## Finding 3 — RED → GREEN

`TestLoadSessionJobActivityTree_NestedContinuationSurvivesNonzeroFoldEpochs` now uses `bumpFoldGeneration` for both
journals — the root's `delegates.jsonl` observed by a tree load, the child's `jobs.jsonl` observed directly at
`historicalJobFoldCache` because a session load stats a missing jobs journal and skips it without asking the cache.

The fixture had no assertion that it worked, which is why it rotted silently, so the walk now checks the minted
continuations carry both a nonzero jobs generation and a nonzero delegates generation. Verified load-bearing by
hand twice: deleting only the delegates bump gives

```
minted continuations carried a nonzero jobs generation: true, delegates generation: false -- both must be nonzero
or the fixture stopped moving a generation and this walk proves nothing about nonzero ones
```

and deleting both gives the same failure with both false. Restored and green.

## Finding 2 — agent behaviour unchanged, issue cited

Filed issue is **#1270**, "Activity panel: offer to open a depth-truncated child session directly when the delegate
carries no continuation". Cited in the depth-boundary block's comment in `agent/jobs_activity.go` and in the PR
body's depth-truncation guarantee. No agent change: the token that would drive a "load more" control is the
predictor this branch deletes.

## Finding 4 — stale comments

`agent/jobs_activity.go:134` (budget `revision` named the deleted `markActivityDelegateTruncated`),
`agent/jobs_activity.go:191` and `agent/jobs_activity_past_test.go:720` (both explained the extra continuation-path
hop as one a depth-boundary mint needs, and the test named a deleted test). All three now give the rule that
remains: the extra hop is slack the decoder still accepts. `grep` confirms no reference to any deleted symbol or
test survives in `agent/`.

## Gates (all green at `bbe186e19`)

- Toolchain gofmt (`$(go env GOROOT)/bin/gofmt -l agent/`) clean.
- `go vet` host, `-tags evenerfuzz`, `GOOS=windows -tags evenerfuzz` — both modules, 0.
- `go test -count=1 -timeout 30m ./...` in `agent`: zero non-ok lines.
- `go test -count=1 ./internal/appprojector/... ./cmd/evener-hub/... ./cmd/evener-tui/...`: zero non-ok lines.
  (Note for future briefs: appprojector lives at `internal/appprojector`, not `./appprojector`.)
- `-race -count=3 -timeout 30m` over the activity tests: zero non-ok lines.
- `-race -count=10` over foldcache: `ok 45.726s`, re-run after the lint fix so the binary matches the pushed tree.
- `GOGC=50 golangci-lint run --concurrency=2 ./...`: `0 issues.` in both modules.
