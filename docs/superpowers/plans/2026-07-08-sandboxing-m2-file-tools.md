# Serf Sandboxing — M2: File-Tool Race-Safe In-Process Layer

> **For agentic workers:** Implement with superpowers:subagent-driven-development,
> task-by-task, red→green→adversarial-verify→commit. Follow the SDD protocol in
> `2026-07-08-sandboxing-m0-master.md`. Design source:
> `docs/superpowers/specs/2026-07-08-sandboxing-design.md` (v4) — M2 draws on the
> "Race-safe path enforcement" and "Denylist by masking; pseudo-filesystems"
> threat-model subsections, the file-tool rows of the Tool-surface inventory, and
> the M2 milestone bullet.

**Goal:** Make serf's own file tools enforce a resolved sandbox policy in a way
that is both **path-correct and race-safe (TOCTOU-proof)**, entirely in-process —
no kernel/bwrap wrapping (that is M3). When a `LocalExecutionEnvironment` carries
a resolved policy (`e.Sandbox != nil`, the inert field M1 added), every file
tool — `read_file`, `write_file`, `edit_file`, `apply_patch`, `glob`, `grep`,
`list_dir` — resolves paths through fd-anchored, symlink-refusing operations
beneath an allowed root, refuses the secrets+pseudo-fs denylist (so
`read_file("/proc/<serf-pid>/environ")` can't read serf's own provider key), and
denies writes outside the writable-root set. When `e.Sandbox == nil` (mode
`off`, the shipped default), behavior is **byte-identical to today** — no new
code path is taken.

**Why this shape:** The in-process path is the *privileged* layer — a file tool
that follows an attacker-planted symlink writes with serf's full authority
(§Threat model layer 2). Resolve-then-open is a TOCTOU hole: a model-spawned
background job can swap a path component for an out-of-worktree symlink between
serf's check and its `open`/`write`/`rename`. The fix is to never re-open by
path after checking: resolve once beneath a base-root fd with symlinks refused,
then do all I/O on the returned fd; writes are atomic temp-file-plus-`renameat`
beneath the checked directory fd. `apply_patch` is **refactored** to route
through this layer (Jesse's resolved decision #9 — disabling a core edit tool to
pass a milestone is rejected), not left on its direct `os.*` calls.

**Architecture:** A new fd-based secure-path layer in `agent/execenv`. Each
`LocalExecutionEnvironment` lazily builds a `sandboxFS` from `e.Sandbox` (the M1
`*sandbox.ResolvedPolicy`); it owns cached `O_PATH|O_DIRECTORY|O_CLOEXEC`
base-root fds (worktree root, session-tmp, writable gitdir/cache roots, read
roots) opened once so a later swap of the root dir itself can't redirect
resolution. Resolution has two shapes, chosen by the resolved policy:

1. **Root-confined** (all writes; `restricted` reads): `openat2(rootfd, rel,
   RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS)` on Linux; an
   `openat(O_NOFOLLOW | O_DIRECTORY)` component walk beneath `rootfd` on macOS.
   The path must resolve beneath one of the allowed root fds or it is refused.
2. **Anywhere-minus-denylist** (`read-only` / `workspace-write` reads):
   resolve the absolute path race-safely with `RESOLVE_NO_SYMLINKS` (no symlink
   is traversed, so the cleaned textual path is canonical and a mid-flight
   symlink swap makes the open fail rather than redirect), then refuse if the
   result lies under any masked path (secrets + pseudo-fs) from the resolved
   policy.

All reads/writes operate on the returned fd. Writes are temp-file +
`renameat` beneath the checked directory fd. When `e.Sandbox == nil`, none of
this runs: `read_file`/`write_file`/`edit_file` keep their `afero` path and
`glob`/`grep`/`list_dir` keep their raw-`os` path exactly as today.

**Tech Stack:** Go 1.25, stdlib + `golang.org/x/sys/unix` v0.42 (already a direct
dep). Verified available: `unix.Openat2` + `unix.OpenHow` + `RESOLVE_BENEATH`
(0x8) + `RESOLVE_NO_SYMLINKS` (0x4) on Linux; `unix.Openat`, `unix.Renameat`,
`unix.Mkdirat`, `unix.Unlinkat`, `unix.Fstatat`, `unix.Fstat` on both Linux and
Darwin. No new external dep in M2 (`go-landlock` is M3). Table-driven `testing` +
fuzz targets (this repo fuzzes heavily — see `docs/fuzzing.md`); real temp dirs
and real concurrent symlink swaps in race tests, no mocks.

**Anchors** (re-verify against the live file before editing — verified
2026-07-08, they will drift as milestones land):
- `agent/execenv/local.go:1121` `resolve` (read; passes absolute paths through
  today), `:1136` `resolveWrite` → `:1158` `ensureUnderRoot`, `:1174`
  `resolveSymlinksBestEffort` — the string-based checks M2 replaces **for the
  sandbox path only**; they stay verbatim for `off` and for the exec working-dir
  check.
- File-tool methods to route through the layer: `:243` `ReadFile`
  (`afero.ReadFile`), `:291` `WriteFile` (`afero.WriteFile`), `:310` `EditFile`
  (`afero`), `:492` `FileExists` (`os.Stat`), `:501` `ListDirectory`
  (`os.ReadDir`), `:553` `Glob` (`os.DirFS`), `:627` `Grep` / `:657` `grepNative`
  (`os.ReadFile` + `filepath.WalkDir`).
- `:60` struct (M1 added `Sandbox *sandbox.ResolvedPolicy`), `:92` constructor,
  `:114` `filesystem()` helper, `:123` `WithWorkingDirectory` (carries the field).
- **Do NOT touch** the exec-path `ensureUnderRoot` call sites: `:779`
  (`execPreparedCommand`) and `:877` (`StreamCommand`) — command working-dir
  confinement + its kernel wrapping is M3, not M2.
- `agent/internal/tool/apply_patch.go:15` `ApplyPatch(rootDir, patch)`, `:237`
  `safeJoin` (string-based traversal check to be deleted), and its direct `os.*`
  calls (`:48` MkdirAll, `:55`/`:139` WriteFile, `:70` Remove, `:85` ReadFile,
  `:151` Rename).
- `agent/session_tools_shell.go:232-233` apply_patch `Exec` →
  `tool.ApplyPatch(env.WorkingDirectory(), patch)` — the call site to rewire.
- Import direction is safe: `agent/internal/tool` **already** imports
  `agent/execenv` (`registry.go:18`), so `apply_patch.go` can depend on execenv
  with no cycle (execenv does not import tool).

## Global Constraints

- **`off` is byte-identical.** `e.Sandbox == nil` ⇒ the existing afero/os path
  runs unchanged. Every existing execenv test must still pass without edits.
  Guard the sandbox branch on `e.Sandbox != nil` at the top of each tool method;
  if you find yourself changing the `nil`/off path, stop.
- **Never re-open by path after checking.** The load-bearing property: resolve
  once beneath a base-root fd, do all I/O on the returned fd, write via temp +
  `renameat` beneath the checked dir fd. A test that opens by path after the
  check is not race-safe and does not count.
- **Sandbox modes refuse symlink traversal.** `RESOLVE_NO_SYMLINKS` /
  `O_NOFOLLOW` means a symlinked component anywhere in a sandboxed path is a
  legible denial, not a follow. This is a deliberate behavior change **under
  sandbox modes only**; `off` still follows symlinks as today.
- **No kernel/bwrap wrapping in M2.** The `rg` subprocess and shell stay
  unconfined here; M2 only makes their *in-process base resolution* policy-aware.
  Session-tmp provisioning and the cache overlay/redirect (the "no cache
  poisoning" containment) are M3d — M2 confines writes to the writable-root
  **set** the resolved policy provides and does not itself provision tmp or
  overlays.
- **Denials are typed + audit-logged with redaction.** A file-tool denial is a
  typed error legible to the model; the audit log records mode + tool + a
  **redacted** path (basename, or a `<denied>` token for denylisted/secret
  paths) + never the file contents or a full secret path (v4 redaction
  contract).
- **snake_case** for any audit-log JSON field or config key that hits the wire.
  `make lint` (serf-namingcheck) is a gate.
- Never `git add -A` without a prior `git status`. Stage exact paths.

## File Structure

- `agent/execenv/securepath.go` (new) — the cross-platform `sandboxFS`: built
  from `*sandbox.ResolvedPolicy`, caches base-root fds, orchestrates the two
  resolution shapes, applies the secrets+pseudo-fs denylist, and exposes the
  fd-based primitives (`readFile`, `atomicWrite`, `remove`, `rename`, `mkdirAll`,
  `stat`, `readDir`, `globBase`) the tool methods call. Also the typed
  `*DeniedError` and the redaction helper.
- `agent/execenv/securepath_linux.go` (new, `//go:build linux`) —
  `openBeneath(rootFd int, rel string, write bool) (fd int, err error)` via
  `unix.Openat2` with `RESOLVE_BENEATH | RESOLVE_NO_SYMLINKS`, and the
  no-BENEATH `RESOLVE_NO_SYMLINKS` variant for anywhere-minus-denylist reads.
- `agent/execenv/securepath_darwin.go` (new, `//go:build darwin`) — the same
  `openBeneath` contract via an `openat(O_NOFOLLOW | O_DIRECTORY)` component walk
  beneath `rootFd` (macOS has no `openat2`).
- `agent/execenv/securepath_other.go` (new, `//go:build !linux && !darwin`) —
  `openBeneath` returns a fail-closed "sandbox file enforcement unsupported on
  <GOOS>" error. Unreachable in practice (M1 refuses sandboxed modes off
  Linux/Darwin) but keeps the build green and the floor honest.
- `agent/internal/tool/apply_patch.go` (modify) — `patchOp.apply` and
  `ApplyPatch` take an injected `execenv` file-mutator instead of a `rootDir`
  string; delete `safeJoin` (its containment role moves into `sandboxFS`); the
  `os.*` calls become policy-aware execenv calls.
- `agent/execenv/execenv.go` (modify) — add a `FileMutator` capability
  interface (raw read / atomic write / remove / rename / mkdir), mirroring the
  existing optional `StreamingExecutor`. `apply_patch` type-asserts for it.
- `agent/session_tools_shell.go` (modify, one line) — call site becomes
  `tool.ApplyPatch(env, patch)`.
- `agent/execenv/securepath_test.go`, `securepath_race_test.go`,
  `securepath_fuzz_test.go` (new) — unit + the concurrent-swap TOCTOU race suite
  + a fuzz target on the resolver (never escapes a root, never returns a fd for a
  denylisted/pseudo-fs path, refusals are typed).
- `agent/execenv/sandbox_tools_test.go` (new) — the per-tool acceptance matrix
  (`read_file`/`write_file`/`edit_file`/`apply_patch`/`glob`/`grep`/`list_dir`)
  built on M1's `sandbox.ContractCase` cases and `sandbox.Resolve`.

## Task 1 — Secure-path resolver core (fd-anchored, symlink-refusing)

**Files:** `agent/execenv/securepath.go`, `securepath_linux.go`,
`securepath_darwin.go`, `securepath_other.go` (all new);
`securepath_test.go`, `securepath_race_test.go`, `securepath_fuzz_test.go` (new).

- [ ] **Failing test** (`securepath_test.go`): build a `sandboxFS` from a
  `sandbox.ResolvedPolicy` over real temp dirs and assert —
  `TestOpenBeneathRefusesSymlinkComponent` (a symlink component in the path
  errors, never follows), `TestOpenBeneathRefusesDotDotEscape` (`../` out of the
  root is refused), `TestConfinedResolvesUnderRoot` (a plain file beneath the
  worktree root resolves and its bytes read back through the fd),
  `TestDenylistRefusesPseudoFS` (a policy whose read shape is
  anywhere-minus-denylist refuses `/proc`, `/sys`, `/dev/fd`, `/dev/mem`,
  `/run/user/<n>` and the secrets dirs), `TestOffModeUnused` (a `sandboxFS`
  built from a nil policy is never constructed — the caller stays on the afero
  path).
- [ ] **Race test** (`securepath_race_test.go`):
  `TestResolveVsSymlinkSwap` — a goroutine repeatedly swaps a directory
  component between a real subdir and an out-of-root symlink while the resolver
  opens+reads in a loop; assert every successful read is of an in-root inode and
  every failure is a typed refusal — **never** an out-of-root read. Use
  `renameat` for the swap so it's atomic.
- [ ] Implement `sandboxFS` (base-root fd cache, the two resolution shapes, the
  denylist gate reading masked paths from the resolved policy), `openBeneath`
  per platform (Linux `Openat2`; Darwin `O_NOFOLLOW|O_DIRECTORY` walk; other
  fail-closed), and the typed `*DeniedError`.
- [ ] **Fuzz target** (`securepath_fuzz_test.go`) `FuzzSecurePathResolve`:
  arbitrary path + policy shape → never panics; a returned fd's real path is
  always beneath an allowed root and never under a masked path; every refusal is
  a `*DeniedError`.
- [ ] Adversarial verify (Opus): does any code path re-open by path after the
  check? Is the base-root fd captured once (immune to root-dir swap)? Does the
  Linux path actually pass `RESOLVE_NO_SYMLINKS` (not just `RESOLVE_BENEATH`)?
  Does `GOOS=darwin go build ./agent/execenv/...` compile? Fix, commit.

## Task 2 — Read surface: read_file, list_dir, file_exists

**Files:** `agent/execenv/local.go` (modify `ReadFile` `:243`, `ListDirectory`
`:501`, `FileExists` `:492`); `agent/execenv/sandbox_tools_test.go` (new).

- [ ] **Failing test:** with `e.Sandbox` set to each mode's resolved policy —
  `read_file("/proc/<serf-pid>/environ")` is refused (the named containment
  hole); `restricted` confines reads to the worktree (a sibling-dir read is
  refused) while `read-only`/`workspace-write` allow an out-of-worktree,
  non-denylisted read; a symlink-out target is refused in every sandboxed mode;
  `list_dir` cannot escape the worktree under `restricted` and cannot list a
  denylisted dir; `file_exists` on a denylisted/secret path returns false
  without leaking existence. Plus `TestReadFileOffModeIdentical` — nil policy
  reproduces today's afero bytes exactly.
- [ ] Implement the `e.Sandbox != nil` branch in each method routing through
  `sandboxFS` (open beneath the correct root fd / resolve-then-denylist per the
  policy's read shape, read via the fd, list via `readDir` on the checked fd);
  keep the nil/off branch untouched.
- [ ] Adversarial verify: is the image/PDF/binary-detection logic still applied
  after the sandboxed read (same output contract)? Does `read_file` on a
  directory fd behave? Does `list_dir`'s recursion re-resolve each subdir
  beneath the checked fd rather than re-`os.ReadDir`-ing by joined path? Fix,
  commit.

## Task 3 — Write surface: write_file, edit_file (atomic temp + renameat)

**Files:** `agent/execenv/local.go` (modify `WriteFile` `:291`, `EditFile`
`:310`); extend `sandbox_tools_test.go`.

- [ ] **Failing test:** `read-only` mode **denies** `write_file`/`edit_file` with
  a legible error (writes are tmp-only per the mode matrix); `workspace-write`
  and `restricted` confine writes to the writable-root set (a write outside the
  worktree/tmp set is refused); a concurrent symlink swap of the target's parent
  during the write never lands the write outside a writable root
  (`securepath_race_test.go` extended for the write path);
  `edit_file`'s read-modify-write stays beneath the checked fd; and
  `TestWriteOffModeIdentical` — nil policy reproduces today's afero write.
- [ ] Implement the sandbox branch: resolve the parent beneath a writable root
  fd, write to a temp file in that dir fd, `renameat` onto the leaf beneath the
  same dir fd; `read-only` short-circuits to a typed denial. Keep the nil/off
  afero branch untouched.
- [ ] Adversarial verify: can the temp-file name collide/leak? Is the rename
  target re-checked beneath the *same* dir fd (not a fresh path resolve)? Does
  `edit_file`'s fuzzy-match/uniqueness logic still run on the fd-read bytes? Does
  a partial write leave a stray temp file on failure? Fix, commit.

## Task 4 — Browse surface: glob, grep (base resolution + native denylist)

**Files:** `agent/execenv/local.go` (modify `Glob` `:553`, `Grep` `:627`,
`grepNative` `:657`); extend `sandbox_tools_test.go`.

- [ ] **Failing test:** under `restricted`, `glob`/`grep` with a base path
  outside the worktree are refused; a glob pattern that would traverse a
  symlink out of root yields no out-of-root match; the **native** grep fallback
  skips denylisted/pseudo-fs paths and never returns a line from one; under
  `read-only`/`workspace-write` a non-denylisted out-of-worktree base is allowed
  but a denylisted base (e.g. `/proc`) is refused. `TestGlobGrepOffIdentical` —
  nil policy reproduces today's `os.DirFS`/`rg` behavior.
- [ ] Implement policy-aware base-directory resolution (resolve the base beneath
  the correct root fd / denylist-check per read shape) for `Glob`, `Grep`, and
  `grepNative`; the native walk refuses to descend into masked paths.
- [ ] Adversarial verify: **note in the plan and the commit message** that the
  `rg` subprocess itself is still unconfined in M2 — only its base dir is
  policy-checked; the subprocess kernel-wrap is M3 (tool-surface inventory:
  "in-process base resolution; rg subprocess kernel-wrapped"). Confirm the
  native fallback can't be steered out of policy via a crafted `globFilter` or a
  symlinked entry mid-walk. Fix, commit.

## Task 5 — apply_patch refactored through the race-safe layer

**Files:** `agent/execenv/execenv.go` (add `FileMutator` capability interface);
`agent/execenv/local.go` (implement it on `LocalExecutionEnvironment`);
`agent/internal/tool/apply_patch.go` (modify: inject the mutator, delete
`safeJoin`); `agent/session_tools_shell.go:233` (rewire call site);
`apply_patch` tests (extend existing + add sandbox cases).

- [ ] **Failing test:** with a sandboxed env, every `apply_patch` op is
  confined — `*** Add File` / `*** Update File` / `*** Delete File` /
  `*** Move to` outside the worktree (via `../` or an absolute out-of-root path
  or a symlinked component) are all refused; a concurrent symlink swap of a
  patched file's parent during apply never writes/renames outside a writable
  root; a denylisted target (e.g. under `/proc`) is refused;
  `read-only` mode denies the mutating ops. Plus **regression**: the full
  existing `apply_patch` suite passes unchanged against a nil-policy env (off).
- [ ] Implement: add `FileMutator` (raw read / atomic write / remove / rename /
  mkdirAll — each fd-based+policy-checked when `Sandbox != nil`, os/afero when
  nil) as an optional execenv capability (mirroring `StreamingExecutor`); change
  `ApplyPatch(rootDir, patch)` → `ApplyPatch(fm FileMutator, patch)` and each
  `patchOp.apply(rootDir)` → `apply(fm)`; delete string-based `safeJoin` (the fd
  resolver now owns containment); rewire `session_tools_shell.go:233`.
- [ ] Adversarial verify: is the `*** Move to` rename target re-checked beneath a
  writable root fd (not string-joined)? Does the update op's read→match→write
  keep containment on every op even though content-atomicity is best-effort (an
  attacker racing the *content* is not an escape; racing the *destination* must
  fail closed)? Does the off path produce byte-identical results to the old
  `os.*` implementation? Fix, commit.

## Task 6 — Typed denials + redacted audit log

**Files:** `agent/execenv/securepath.go` (finalize `*DeniedError` + redaction);
a small audit-log sink (new `agent/execenv/audit.go` or extend an existing
logger — grep for the current denial/log seam first); tests in
`securepath_test.go`.

- [ ] **Failing test:** a denial from each tool surfaces a typed `*DeniedError`
  whose message names the tool + mode + a **redacted** path; a denylisted/secret
  path is redacted to `<denied>` (never the full path); file *contents* never
  appear in a log line; the audit record's JSON keys are snake_case. Assert the
  log is captured-and-tested (pristine-output rule — no stray denial spam).
- [ ] Implement the typed error + redaction helper (basename for in-tree paths,
  `<denied>` token for denylisted/secret paths) and emit one audit line per
  denial; thread it through Tasks 2–5's denial points.
- [ ] Adversarial verify: can any code path log a full secret path or file
  bytes? Is a redacted basename ever itself sensitive (then use `<denied>`)? Is
  the error legible enough that the model won't retry-loop (per Jesse's
  denial-UX rule)? Fix, commit.

## Task 7 — Adversarial escape suite + M1 contract consumption

**Files:** `agent/execenv/sandbox_tools_test.go` (extend into the escape suite);
import `primeradiant.com/serf/agent/sandbox`.

- [ ] **Failing test:** drive the spec's Validation escapes through **every**
  file tool, consuming M1's exported `sandbox.ContractCase` table + `sandbox.Resolve`
  to build the per-(mode × host) envs:
  - symlink-out (read + write + apply_patch) → denied.
  - `read_file("/proc/<serf-pid>/environ")` + `/proc/1/root` + `/proc/<pid>/root`
    aliasing → denied in every sandboxed mode.
  - TOCTOU symlink-swap race during read / write / rename / apply_patch
    (concurrent job) → never escapes.
  - denylist read via **every** file tool incl. `apply_patch` → denied.
  - `~/.bashrc` / `~/.gitconfig` write → write-confinement denial.
  - `.git/hooks` write + `config.worktree` / submodule-config tamper via file
    tools → denied (config/hook surfaces are read-only per the resolved policy;
    the git-metadata classification comes from M1's gitdir resolution).
  - pre-existing-hardlink read/write-through → **documented residual, asserted**
    (masking closes create-then-use; a pre-planted hardlink inside a
    readable/writable root is out of the running-amok model — assert the current
    behavior so a future inode-preflight change is a conscious one).
  - `off` mode → every escape "succeeds" exactly as today (proves the guard).
- [ ] Wire the acceptance matrix so each per-tool row's allow/deny matches the
  grants the resolved policy declares (the M1 contract is the oracle, not the
  implementation).
- [ ] Adversarial verify: is every file tool actually exercised for each escape
  (not just `read_file`)? Are the expected allow/deny values taken from the M1
  contract/spec, not copied from M2's own resolver (so a wrong resolver is
  caught)? Fix, commit.

## Done criteria

- `cd <worktree> && make test-short && make vet && make lint` clean;
  `make test-race` clean (the TOCTOU suite is the point — it must pass under
  `-race`).
- `go test ./agent/execenv/...` green incl. the fuzz targets' seed corpus.
- `GOOS=darwin go build ./... && GOOS=darwin go vet ./agent/execenv/...` clean
  (the macOS fd-walk compiles + vets; its live race/acceptance validation runs
  on **paradise-park** — fold it into M6's macOS validation window, which
  re-runs the M1 contract suite + parity tests anyway).
- Every file tool (`read_file`, `write_file`, `edit_file`, `apply_patch`,
  `glob`, `grep`, `list_dir`) enforces the resolved policy when `e.Sandbox != nil`
  and is byte-identical to today when nil; `apply_patch` no longer calls `os.*`
  directly and `safeJoin` is gone.
- The adversarial escape suite (Task 7) passes; the pre-existing-hardlink
  residual is asserted, not silently ignored.
- Merge `wip/sandbox-m2` → `wip/sandboxing`; update the M0 status ledger; report.
