# Managed plugin publication: private Git metadata, same content paths

Status: design for delivery. Date: 2026-10-20. Issue:
[#4259](https://github.com/prime-radiant-inc/evener/issues/4259).

Settled scope: managed installs keep their content paths, Git metadata stays
private, and the per-spawn `.git` walk stops for managed content after a
verified whole-store migration. User-maintained skills and in-place directory
plugins are unchanged. Updated hub/CLI writers are required before migration;
older session readers keep their existing content paths and non-metadata bytes.
Old published revisions are retained rather than reclaimed.

This design replaces the paused draft on `wip/perf-plugin-scan-investigation`.
That draft's move rule is rejected: it moved ordinary symlink targets and
ancestor-pointer directories. This version moves only entries named `.git` and
never moves a pointer's target.

## Publication

Managed sources are prepared in a private directory under the store root,
`<root>/publishing/<marketplace>/<plugin>/.staging`, which the existing config
mask keeps out of every sandbox. Bundled payloads stage in
`<root>/publishing/bundled/`. The final content root is
`<root>/cache/<marketplace>/<plugin>/<revision>`; bundled content stays
`<root>/bundled/<name>-<digest>`.

1. Fetch into private staging.
   - Before copying or cloning, a selected source is refused when it names Git
     administration: Git's own `rev-parse --is-inside-git-dir` verdict when Git
     is available, or a strong HEAD/config/objects signature when it is not.
     A repository-relative or git-subdir path containing `.git` is refused.
     In-place directory-marketplace sources are not copied and remain the
     user's own directories. Plugin Git commands strip ambient
     repository-selection variables (`GIT_DIR`, `GIT_WORK_TREE`, and friends)
     so an unrelated environment cannot authorize a metadata selection.
   - Direct clone: clone into the staging payload, resolve `HEAD`, then remove
     the payload's `.git` entry (link, file, or directory) before any audit or
     rename. A cloned work tree is never published with a `.git` pointer.
   - Sparse clone: clone into a private sibling, copy only the requested
     subtree, then remove the private clone.
   - Relative and copied directory sources: copy the tree, skipping every entry
     named `.git` (any case) without descending into it. A symlink whose
     lexical or resolved target has a `.git` component is refused with a
     diagnostic naming the source-relative link. Ordinary symlinks keep their
     current dereference behavior. This closes the #4768 copy aliasing path for
     new publications; bytes already copied before this change are not
     retroactively identifiable.
2. Run manifest fallback and validation on staging.
3. Audit the fully prepared tree: walk it without following symlinks and refuse
   any `.git` name at any depth or any symlink whose lexical or resolved target
   has a `.git` component.
4. Publish with the existing immutable-revision rule: an absent SHA directory
   may take its SHA name; any occupied name, and every empty-SHA source, gets
   a unique `revision-<random>` sibling. An existing revision is never reused
   for a new materialization and is never removed. Copied directory installs
   therefore get a new revision on reinstall and never freeze.
5. Bundled publication copies only the embedded payload and refuses a payload
   containing a `.git` name before its rename. The embedded tree is covered by
   a build-time test.

## Store verification

`<root>/store-format.json` holds `{"version":1,"managed_content":"clean"}` or
`"unverified"`. Missing/malformed means unverified. The state is written under
the store lock, then the bundled lock (never the reverse), and is written
`clean` only after a full re-audit of both entire roots.

Migration runs once from `lockStore`, so every updated hub RPC and CLI plugin
mutation migrates before its own write. It is best effort: if the bundled lock
cannot be taken, or a read/rename fails, the state stays `unverified` and the
operation continues with the old scan. The next mutation retries. It never
rewrites the registry or a saved path.

Inventory covers every entry under `cache` and `bundled`, including legacy
`.staging`, `.staging.clone`, orphan revisions, bundled `.stage-*`,
`.conflict`, and `.previous`. It does not follow symlinks and does not descend
into a `.git` entry.

Move rule (bounded metadata only):

- An entry named `.git` (any case), directory or file or symlink, moves whole
  to `<root>/managed-git-archive/<cache|bundled>/<relative-path>`, with a
  numeric suffix if that slot is taken.
- A `.git` entry in a cache marketplace or plugin **namespace** position is
  preserved and the store stays unverified: a marketplace or plugin may
  legitimately be named `.git`, and archiving it would remove published
  content a registry entry and saved session still name. Only revision
  payloads and known legacy scratch trees yield metadata.
- A `.git` symlink moves the link itself when its target is outside the
  managed roots or is itself Git metadata. A link whose target lies inside a
  managed root without a `.git` component is left in place and the store stays
  unverified. Its target is never moved or followed.
- A `.git` file moves the file itself. If its `gitdir:` target lies inside a
  managed root and the target path has no `.git` component, the entry is left
  in place and the store stays unverified. This conservatively refuses the
  ancestor/mixed-content pointer the rejected draft would have moved. A target
  outside the managed roots is never touched; the pointer file still moves.
- No other path is moved. Ordinary symlink targets, revision directories, and
  bundled conflict directories keep their bytes.
- Hardlink aliases to metadata are not scanned; bytes already aliased into
  ordinary files are outside what migration can identify.

A store with any remaining `.git` name, an unsafe pointer, or a read error is
not clean. Migration decisions are logged with store-relative paths; a raw
filesystem error keeps its own local log text, and none of this reaches an RPC
payload.

## Scan policy

At session policy resolution the probe reads the default plugin store's
`store-format.json` and records the managed roots and whether they are clean.
The fact is sampled at resolution, like other host facts; it is not re-read per
command and is not a dynamically revocable flag.

- Clean: the managed roots (`plugins/cache`, `plugins/bundled`) are granted
  read-only with no `gitDirsUnder` traversal on either backend. Seatbelt keeps
  its path-free `.git` name rule. File tools keep name-based `.git` denial.
  Match is by exact managed root in its lexical, resolved, or recorded on-disk
  spelling; the on-disk spelling covers APFS case and Unicode normalization and
  is recorded once at policy resolution, not probed per command.
- Unverified/absent: both backends keep today's walk and masking.
- User skills are always walked as today. In-place user directories remain
  outside the fixed roots and unchanged.

Authority for a clean managed root is the publisher plus the completed audit,
not the filesystem. A same-user edit adding `.git` there is outside the
sandbox threat model and is still refused by name by the file tools.

## Preservation, GC, Doctor

The reused sibling commits make publication immutable and prevalidated, and
retain unreferenced cache revisions because saved and resumable sessions may
name them. Doctor no longer calls a retained revision an orphan or promises GC
reclaims it; genuine unreadable, corrupt, and staging diagnostics remain.
Migration archives live under the store root and are never collected by GC.

## Limitations

- Older executables still walk; older readers still read the same paths. A new
  executable resuming an old path reads the clean state and skips.
- A migration failure does not prevent sessions or publications. The old scan
  continues until a later mutation completes the audit.
- Arbitrary malformed or external legacy layouts are not promised to migrate.
  An unclassifiable pointer keeps its protection and is reported.
- Existing targets are compared to managed roots by file identity as well as
  spelling, so APFS case or Unicode spellings cannot make a mixed-content
  pointer look external. A target created after resolution under a different
  spelling than configured keeps the old walk (a performance cost, not an
  unsafe exemption).
- Native APFS case/Unicode regressions require a case-insensitive, normalizing
  volume and skip on Linux CI. The no-Git source fallback is signature-based:
  a corrupt or nonstandard Git-admin layout without those markers may not be
  detected, and an unverifiable admin-looking directory is refused.
- The separate marketplace-name migration defect is outside this change.

## Tests

Focused, no network or live provider: publication strips clone metadata and
refuses copied aliases; empty-SHA reinstall publishes a distinct revision;
existing revision bytes survive install/upgrade/migration; whole-root
inventory covers legacy staging and bundled slots; ordinary symlink targets and
ancestor pointers are preserved; interrupted migration retries while service
continues; GC/Doctor retention; both backends skip managed walks when clean
while user skills still walk, including a symlinked config/store root. A real
saved session is resumed after an ordinary GC-triggered migration, resolves
the old revision, and runs its hook twice; the policy resolved for that store
names only the managed roots as walk-exempt. Backend argv/policy tests cover
repeated spawns with zero managed traversal.
