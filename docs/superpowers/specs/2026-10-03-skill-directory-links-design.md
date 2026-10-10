# Directory-linked skill discovery

## Status and authority

Issue: https://github.com/prime-radiant-inc/evener/issues/3689.
Base: `8ebea57624e8ea4b01f0f9ce00537523d58c6fd2`.

Jesse approved the consequential choice: **“Support across skill roots.”**
The approved question includes readable immediate-child directory links to targets
outside the root, existing permissions and source identity, broken-link
diagnostics, restored-target recovery, and an unchanged private builtin cache.
This written specification awaits Jesse's approval before implementation.

## Problem and scope

`agent/skill/discovery.go` and `agent/skill/skills.go` enumerate root children using
`DirEntry.IsDir()`. A symlink to a readable directory is not itself a directory,
so a valid `SKILL.md` behind it is silently absent. This breaks the normal
`merge-children .config/evener/skills` dotfiles layout.

Support immediate-child directory symlinks in both `Discover` and `ScanSkillsDir`.
The shared discovery policy covers home `.agents/skills`, Evener user skills,
project `.agents/skills` and `skills` from git root through cwd, explicit
`skills_dirs`, and plugin `skills`. Existing readable symlinked roots and linked
`SKILL.md` files remain supported. The private embedded-cache verification and
publication policy is unchanged.

Affected surfaces consume the runtime catalog: model advertisement (including
read-file fallback), user completion, metadata inspection, and activation/loading
in live sessions and cold catalog reads. This change belongs to skill discovery;
it adds no client-specific enumeration or renderer.

## Required behavior

1. A real child directory or an immediate child symlink whose resolved target is
   a directory is a candidate. Read its `SKILL.md` through the configured child
   path and apply the same parser, controls, qualification and precedence as a
   real directory.
2. Accept absolute and relative links, including targets outside the discovery
   root. Follow ordinary filesystem link resolution without recursive discovery
   or a new allowlist/trust grant. Do not traverse nested skill collections.
3. Retain the lexical absolute `SKILL.md` path and child directory as the catalog
   source and `base_directory`, rather than replacing them with the target's
   canonical path. Existing declared-name validation and current-byte digests
   continue to govern loading. Normal sandbox/tool boundaries still govern later
   collateral reads and actions; skill bodies remain inert text.
4. For a child symlink that cannot resolve (missing target, loop or unreadable
   target), `Discover` records `unreadable_source` at the configured child link
   path. Keep other healthy skills discoverable and usable. Do not invent a
   declared name or unavailable catalog entry when the source cannot be read.
5. Non-directory children, links to regular files, and directories without
   `SKILL.md` remain non-candidates. A readable directory link whose `SKILL.md`
   cannot be read follows the existing source diagnostic behavior. Invalid
   metadata follows existing unavailable-winner and diagnostic rules.
6. `ScanSkillsDir` retains its map-only API and existing quiet-skip policy for
   unreadable/invalid candidates; it accepts the same readable directory links.
   It does not gain a separate diagnostic API.
7. Existing precedence, collision diagnostics, model/user invocation controls,
   path-free user/inspection entries, plugin qualification, and exact-name
   resolution are preserved. Reading the layout changes no installation files.

## Recovery and preservation

Discovery is a snapshot, not a filesystem watcher. When a missing or unreadable
linked target becomes readable again, the next existing discovery operation
(e.g. a fresh catalog/session) sees the skill without a migration or repair
button. No new live-session catalog refresh promise is introduced.

A descriptor discovered while the target was readable keeps its source path.
Loading after target disappearance reports the existing `unreadable_source`
failure. Restoring the target allows a subsequent load of that descriptor to
succeed if the declared identity still matches. Changing the declared name still
fails existing identity validation. A same-name replacement remains governed by
existing current-byte load policy; this change adds no target-inode pinning.

Broken candidates do not disable healthy siblings or modify targets, links,
activation history, unrelated files, or the user's dotfiles deployment. Preserve
real-directory and file-linked layouts alongside the newly supported layout.

## Non-goals

- Embedded-cache issue1414 or relaxed private-cache validation.
- Changes to dotfiles, homedir-manager, implementing-features, or plugin install.
- Recursive discovery, filesystem watchers, catalog refresh redesign, inode
  identity pinning, or source-race transactions.
- Changes to source trust, invocation permission, template execution or sandbox
  policy.
- Model/provider behavior claims, UI changes, live provider requests or deployment.

## Implementation boundaries

Own discovery changes and necessary tests in `agent/skill`, with integration
coverage in `agent/session_skills_test.go` or a dedicated session skill test file
if needed. Update `docs/skills.md` and S18 in `docs/product/subsystems.md` in the
same change. Record approved design and evidence in this worktree only. Prefer a
small shared candidate-classification helper if that avoids drift between the
modern and older scanner; do not refactor unrelated discovery.

## Whole-journey acceptance and checks

Use fixture-owned real files/symlinks, actual `Discover`, catalog views and `Load`,
not mocked scanner internals or provider APIs. No test depends on developer HOME,
provider credentials, network, or permissions denied only for non-root hosts.

Before the fix, add and run a focused regression demonstrating that a personal
root containing `root/name -> readable outside-root directory` cannot resolve the
valid skill. Report the full failing output. Identical bytes in a real directory
with linked `SKILL.md` are the supported-layout control.

After the fix, verify:

- Both absolute and relative directory links across each public root category
  resolve exact canonical identity, appear in permitted model/user views, and
  load the exact fixture bytes with an independently computed SHA-256 digest.
  Rendered structured context retains the linked source and base directory, and
  collateral is reachable through that base using ordinary filesystem reads.
- The older scanner discovers and loads those same valid linked directories.
- A real session startup using an isolated HOME/XDG layout obtains the linked
  personal skill through production discovery; typed prompt skill inputs and
  `DetailedStatus` completion/inspection obey controls and omit sensitive paths.
  Exercise real activation preparation/loading without a live provider request.
- Broken and looping links give source-specific catalog diagnostics, coexist
  with healthy real-directory/file-link/directory-link skills, and recover after
  fixture target restoration on rediscovery. A descriptor can fail load while
  its target is absent, then load again when it returns. Identity-changed source
  continues to fail. Verify original fixture bytes/links are preserved.
- File links, missing `SKILL.md`, malformed metadata and collisions retain the
  agreed ignore/diagnostic/precedence behavior. No recursive scan occurs.
- Existing private builtin-cache tests and supported layouts still pass.

Inspect test harnesses and any scripts used before running them. Start with the
focused regression and `go test ./agent/skill -count=1`; add a targeted
`go test ./agent -run '<directory-link and affected skill tests>' -count=1` for
production wiring. Run relevant race/format checks as warranted. Follow
`AGENTS.md`: targeted local checks, full deterministic repository gates in CI,
not a long local full-suite blocker. Real GitHub check state and review bodies
must support delivery claims.

## Review and delivery

After Jesse approves this document, freeze its inputs for installed `/par` (or
its required two-independent-reviewer adaptation), resolve evidenced findings,
and bring consequential amendments back for approval before implementation.
Use TDD, implementation PAR, required simplify-code and shepherd-pr stages.
Follow repository merge authority: current-head checks green, matching review
bodies and own per-commit findings settled before admin squash with the exact
head SHA. Verify GitHub merged state and intended squash content before removing
owned worktree/branch. Preserve unrelated primary-checkout files and unknown or
retained scratch. Deployment requires separate authorization.
