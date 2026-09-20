# Task 160 — PR #1269 round 7, and PR #1301 (#1297 + #1300)

## #1301 — the macOS fixture fix, opened first

`test: compare skill paths by resolved path on macOS (#1297, #1300)`, one commit `e4e569ad1`, branch
`claude/fix-1297-tempdir-symlink`, not a draft. Closes both issues.

Cause: `skill.projectSkillDirs` resolves the working directory and `plugin.SkillSources` resolves each plugin
directory (so a symlinked plugin and its target collide under one name rather than loading twice), so every path
the daemon reports is the resolved spelling. macOS `t.TempDir()`/`os.MkdirTemp` hand back `/var/folders/...`, a
symlink to `/private/var/folders/...`. The fixtures were comparing two names for one file.

Fix is in the fixtures — a documented `skillFixtureRoot(t)` helper in `agent`, plus the same resolution on the
browser guard's `os.MkdirTemp` root. No production change; `EvalSymlinks` is a no-op for these paths on Linux.

Proof: `./agent` green (was 28 failures); `go test -tags browserguard -run '^TestSkillComposerBrowser$'` green in
16s at load average 25 against a freshly built frontend, and red on `skill-context source`/`base_directory` when
only that resolution is reverted.

## #1269 round 7

| SHA | Item | Message |
| --- | --- | --- |
| `fd08ce28b` | 1 (Medium) | `fix(agent): do not compare a recreated file against a tombstone` |
| `4cdf6fcc4` | 2 + 3 (Low) | `docs(agent): put the path-limit rationale on the function that enforces it` |

RED before the fix, on the empty recreate:
`generation 2 after an empty file appeared, want the absence's 1 -- the deletion was the discard; a tombstone has
no content to compare this against`. `describesContent := st != nil && !st.absent` guards both the comparison
branches and `wasCached`; the test also asserts `FullRescans` counts 0 for the first fold after a tombstone and 1
for the later append onto the cached empty fold.

The Low items: the `activityContinuationPathFits` paragraph is back on its own function (the dedup paragraph stays
put), and the path bound's rationale is restated around this build's own arithmetic — a page resumed one hop down
names an absolute path of exactly `activityMaxNewDepth+1`, so a decoder capped at `activityMaxNewDepth` would
refuse a token this service just handed out. The earlier-build-slack story was stale: version 2 refuses those on
version before the length is read.

Merged `origin/main` @ `f7816dd8d` (carries #1301), so the agent suite is clean here again.

## Gates

Toolchain gofmt; three vets on `agent`; `golangci-lint` `0 issues.`; `cmd/evener-tui` and `internal/appprojector`
clean; agent suite clean; `-race -count=3 -timeout 150m ./internal/foldcache/... .` — result in the PR comment.
