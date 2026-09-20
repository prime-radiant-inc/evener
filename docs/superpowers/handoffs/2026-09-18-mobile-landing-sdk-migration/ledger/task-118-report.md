# Task 118 — follow-up PR for the continuation generations

**Status: DONE. PR #1231** (not draft), base `main`, head `claude/activity-continuation-epochs`,
head SHA **`2ed87fa98`**. New worktree at `.claude/worktrees/activity-continuation-epochs`, branched
from `origin/main` (b9a98151c). The old `activity-oversized-entry` worktree is untouched.

## Cherry-picks
Both applied **clean — no conflicts, no resolution needed**: `e1de98893` → `d864c23e8` (round-8:
per-session generations, exemption deleted) and `6f7230659` → `2ed87fa98` (depth placeholder names
its child's generations). The squash on main (`ce1c8ed01`) carries exactly the branch state these
two were built on (`20cdd8a3b`), so the diffs landed unchanged; the earlier merge conflict I hit in
Task 116 was `git merge` matching squashed against unsquashed history, not a content divergence.
Verified on the result: `root.live == nil &&` is gone, and `collectActivitySessionEpochs`,
`activityPlaceholderEpochs` and `foldcache.Cache.Epoch` are all present.

## Gates (on 2ed87fa98)
- toolchain gofmt (`$(go env GOROOT)/bin/gofmt -l`) clean on all five touched files
- `go vet ./...`, `-tags evenerfuzz`, `GOOS=windows -tags evenerfuzz`: 0 in the agent and root modules
- `go test -count=1 ./...` agent: zero non-ok lines; `./internal/appprojector/...` and
  `./cmd/evener-hub/...` (server): zero non-ok lines
- `-race -count=3 -timeout 30m` over the activity tests: **`ok primeradiant.com/evener/agent 659.537s`**
- Task 116's race run on the pre-cherry-pick tree also finished green: `ok ... 763.926s`
- `GOGC=50 golangci-lint run --concurrency=2 ./...`: `0 issues.`
- The six tests (four RED→GREEN plus two unit pins) pass individually in the new worktree.

## One thing to file
`internal/selfupdate` fails on this machine independently of any of my work — five
`TestInstallDirs*` cases compare `/private/var/...` against `/var/...` (macOS temp-dir symlink), and
they fail identically on the merged `activity-oversized-entry` worktree. Not in any gate this task
required (agent / appprojector / server), not caused here, and worth its own issue.
