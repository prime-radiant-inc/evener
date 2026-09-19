# Task 158 — PR #1269 review round 6

Worktree `activity-generations`. Merged up to `origin/main` @ `33b553278` (the standing instruction to stop at
`1d5e12100` arrived after I had already merged `c09997369`; it was lifted before the push, so the branch is current).

| SHA | Item | Message |
| --- | --- | --- |
| `89bb878d0` | 1 (Medium) | `fix(agent): treat a deletion as one event, and report the generation it is judged by` |
| `6d6403e43` | 2 (Medium) | `fix(agent): bump the continuation format version` |

## Item 1 — the two halves are inseparable

The livelock does not reproduce on the old code, because `Result.Epoch` was 0 for an absent read: the runaway
tombstone was invisible. It becomes real the moment absence reports the number it is judged by, which the finding
also asks for. So the RED is at the fold-cache level — `TestCache_DeletedPathReportsOneStableGeneration`,
`generation 0 after a delete, want it past 0` — and the end-to-end
`TestLoadSessionJobActivityTree_PaginatesAfterTheJobJournalIsDeleted` is the regression guard: with `drop`
reporting but not idempotent it dies on `page 2: activity continuation is stale: the underlying journal changed`.
Checked by hand in both directions.

`epochState` gained a tombstone marker, `drop` advances once and then returns the same number, `Get` returns it.
Both journals share it because they share the cache. A recreate deliberately does not advance again: the deletion
was the discard, and presence — already in the token — separates "absent at 1" from "present at 1".

## Item 2

`activityContinuationVersion = 2`; every other version refused as stale; no legacy path. The four negative fixtures
that used version 1 to test *other* validation failures now mint the current version, or they would have passed for
the wrong reason. The three stale rejections share one constructor.

## The agent suite on this machine

28 failures, all skills-lifecycle tests from #1168, all `envelope source = "/private/var/..." want "/var/..."`.
`t.TempDir()` returns `/var/folders/...`; macOS resolves that symlink; the envelope carries the resolved spelling
and the expectation does not. Linux CI is green because the two spellings are identical there. **Filed as #1297.**
None of them touch the fold cache or the activity tree.

Trap for future briefs: `-run 'Continuation'` matches `TestSkillDelivery_ResponsesContinuationPlanning`, so a
filtered activity race run picks up a #1168 failure that has nothing to do with continuations.

## Gates

Toolchain gofmt clean; `go vet` host + `-tags evenerfuzz` + `GOOS=windows -tags evenerfuzz`, 0; `golangci-lint`
`0 issues.`; `cmd/evener-tui` and `internal/appprojector` clean; `-race -count=3 -timeout 150m` on foldcache clean;
the activity and foldcache area green under `-count=1`. Full agent-root race gate classified in the PR comment.
