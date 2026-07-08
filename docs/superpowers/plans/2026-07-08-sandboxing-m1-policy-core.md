# Serf Sandboxing — M1: Policy Core + Cross-Backend Contract Tests

> **For agentic workers:** Implement with superpowers:subagent-driven-development,
> task-by-task, red→green→adversarial-verify→commit. Follow the SDD protocol in
> `2026-07-08-sandboxing-m0-master.md`. Design source:
> `docs/superpowers/specs/2026-07-08-sandboxing-design.md` (v4).

**Goal:** Build the backend-independent policy model and the contract-test
harness that every backend (bwrap, Landlock, Seatbelt) is later held to — plus
the flag/config plumbing — **with zero live enforcement**. After M1, a
`SandboxPolicy` can be parsed from flags/config, resolved against host facts into
an exact set of grants/denials/refusals, and asserted by a table-driven contract
suite. No file tool or command behaves any differently yet: the policy is
carried but not consulted by any enforcement path (that's M2/M3).

**Why first:** M2 (file tools) and M3 (kernel) both consume `SandboxPolicy` and
must satisfy the same contract suite. M6 (macOS) is written against this suite so
it can't drift from Linux semantics. Getting the *types and the contract* right
here is the whole point of M1.

**Architecture:** A new `agent/sandbox` package owns the policy model, mode/flag
parsing, host-capability probing (as an injectable interface so tests are
hermetic — no real bwrap/kernel calls in unit tests), root/gitdir/denylist
resolution, and the contract-test harness. `SandboxPolicy` is threaded into
`LocalExecutionEnvironment` and `SessionConfig` as an inert field. The contract
harness is the deliverable other milestones build against: given `(mode, net,
HostFacts)` it produces a `ResolvedPolicy` (writable roots, read roots, masked
paths, net decision) or a typed refusal, and a golden table asserts the exact
output for every (mode × host-capability) cell of the spec's floor matrix.

**Tech Stack:** Go 1.25, stdlib + `golang.org/x/sys/unix` (already a dep; used
here only for capability *probing* constants, not enforcement). No new external
dep in M1. Table-driven `testing`. Fuzz targets for the resolver (this repo
fuzzes heavily — see `docs/fuzzing.md`).

**Anchors** (re-verify against the live file before editing):
- `agent/execenv/local.go:60` `LocalExecutionEnvironment` struct;
  `:92` `NewLocalExecutionEnvironment`; `:123` `WithWorkingDirectory`.
- `agent/session_config.go:20` `SessionConfig`; `:31-41` timeout/depth fields
  (add `Sandbox`/`SandboxNet` beside them); `:365-445` the normalize/copy
  methods that must carry the new fields.
- `agent/subagents.go:226`/`:243` `localEnvPolicyName`/`localEnvPolicyFromName`
  (mirror this name↔enum round-trip for the sandbox mode).
- `cmd/serf/run.go:177` env construction + `:180` config build;
  `cmd/serf/serve.go:203`/`:204` same.

## Global Constraints

- **Zero enforcement in M1.** No call site consults the policy to allow/deny yet.
  If you find yourself editing `resolve`/`shellCommand`, you've left M1 — stop.
  The only edits to existing files are: add inert carrier fields + wire
  flags/config + a name↔enum helper.
- **Capability probing is injectable.** `HostFacts` is a plain struct; the thing
  that *produces* it (`Prober` interface) has a real implementation and a fake.
  Unit tests never shell out to `bwrap` or touch the kernel. A single
  build-tagged or explicitly-opt-in integration test may probe the real host.
- **Contract table is the deliverable.** It encodes the spec's floor matrix and
  per-mode grants exactly. M2/M3/M6 import and satisfy it. Treat its correctness
  as the milestone's primary output.
- **snake_case** for any JSON/config/flag key that hits the wire or a config
  file. `make lint` (serf-namingcheck) is a gate.
- Never `git add -A` without a prior `git status`. Stage exact paths.

## File Structure

- `agent/sandbox/policy.go` (new) — `Mode` enum (`Off`/`ReadOnly`/
  `WorkspaceWrite`/`Restricted`) + name↔enum; `SandboxPolicy` (mode, net bool,
  user denylist add/remove, extra writable/read roots); `ResolvedPolicy`
  (writable roots, read roots, masked paths incl. pseudo-fs, net decision,
  cache-strategy, backend-required). Default secrets denylist + pseudo-fs
  constants live here.
- `agent/sandbox/resolve.go` (new) — `Resolve(policy, HostFacts, cwd) (ResolvedPolicy, error)`:
  computes roots, resolves `gitdir:` (incl. linked-worktree main-`.git` read
  grant + submodule/worktree config protection), applies denylist add/remove,
  picks cache strategy, and returns a typed `*RefusalError` when host facts can't
  satisfy the mode+net (the fail-closed floor).
- `agent/sandbox/probe.go` (new) — `HostFacts` (OS, bwrapPath+capabilities,
  overlaySupported, landlockABI, kernelVersion); `Prober` interface;
  `realProber` (probes bwrap/kernel/landlock) behind an integration build tag or
  explicit opt-in; `FakeProber` for tests.
- `agent/sandbox/gitdir.go` (new) — resolve a worktree's `gitdir:` pointer, the
  set of config surfaces to protect (`.git/config`, `config.worktree`,
  `.git/modules/*/config`), and the main-repo read-grant for linked worktrees.
- `agent/sandbox/contract.go` (new) — the exported contract harness:
  `ContractCase{Mode, Net, Host, WantResolved, WantRefusal}` and
  `AssertResolve(t, Resolve)` running the golden table. Exported so M2/M3/M6 test
  packages import it.
- `agent/sandbox/policy_test.go`, `resolve_test.go`, `gitdir_test.go`,
  `contract_test.go` (new) — unit + the golden contract table + a fuzz target on
  `Resolve` (never panics, never returns a resolved policy that grants a
  denylisted/pseudo-fs path, refusals are typed).
- `agent/execenv/local.go` (modify) — add `Sandbox *sandbox.ResolvedPolicy`
  (nil = `off`, today's behavior) to the struct (`:60`), constructor (`:92`),
  and carry it in `WithWorkingDirectory` (`:123`). **Inert** — no method reads it
  in M1.
- `agent/session_config.go` (modify) — add `Sandbox string` (mode name) +
  `SandboxNet *bool` beside the timeout fields (`:31-41`); carry them through the
  normalize/copy methods (`:365-445`).
- `agent/subagents.go` (modify, small) — add `sandboxModeName`/`sandboxModeFromName`
  mirroring the env-policy helpers (`:226`/`:243`).
- `cmd/serf/run.go` + `cmd/serf/serve.go` (modify) — add `--sandbox <mode>`
  (default `off`) and `--sandbox-net <on|off>` flags; parse into `SessionConfig`;
  resolve to a `ResolvedPolicy` via `sandbox.Resolve` at env construction
  (`run.go:177`, `serve.go:203`) and store it inert on the env. On a
  `*RefusalError`, **fail session start** with the error's message (this is the
  one behavior M1 ships: refusing to start an unenforceable request — but since
  nothing enforces yet, guard it so `off` is unaffected and the refusal only
  fires for a non-off mode; that keeps M1 safe to merge while making the
  fail-closed floor real from day one).

## Task 1 — Mode enum + SandboxPolicy + defaults

**Files:** `agent/sandbox/policy.go` (new), `policy_test.go` (new).

- [ ] **Failing test** (`policy_test.go`): `TestModeRoundTrip` (every mode ↔ name,
  unknown name errors), `TestDefaultDenylistIncludesPseudoFS` (the default set
  contains `/proc`, `/sys`, `/dev/fd`, `/dev/mem`, `/run/user` and the credential
  dirs from the spec), `TestPolicyDenylistAddRemove` (user add extends, user
  remove punches a hole, model can't be represented as a mutator — the type is
  value-immutable once resolved).
- [ ] Implement `Mode`, name↔enum, `SandboxPolicy`, default constants.
- [ ] Adversarial verify (does the denylist match the spec's list exactly? is
  `off` the zero value so a nil/absent policy = today's behavior?). Fix, commit.

## Task 2 — HostFacts + Prober (fake + real, injectable)

**Files:** `agent/sandbox/probe.go` (new), `probe_test.go` (new).

- [ ] **Failing test:** `FakeProber` returns canned `HostFacts`; assert the three
  spec rows are representable — (a) bwrap-capable, (b) Landlock-only, (c)
  neither/Windows. `TestRealProberOptIn` is guarded (build tag or env) so CI
  unit runs never shell out.
- [ ] Implement `HostFacts`, `Prober`, `FakeProber`, and `realProber` (probe
  `bwrap --version`/caps, overlay support, Landlock ABI via
  `unix.LandlockCreateRuleset` availability, `runtime.GOOS`). Real prober behind
  opt-in.
- [ ] Adversarial verify (no real syscalls on the unit path; Windows/other-OS
  facts representable). Fix, commit.

## Task 3 — gitdir resolution + config-surface protection set

**Files:** `agent/sandbox/gitdir.go` (new), `gitdir_test.go` (new).

- [ ] **Failing test** with real temp git repos (no mocks): a **main checkout**
  (protect `.git/config`+`.git/hooks`, writable objects/refs/index), a **linked
  worktree** (`.git` is a `gitdir:` file → protect main `.git/config` write but
  **grant it read**, grant `worktrees/<id>/` + shared objects/refs writes, deny
  `config.worktree`), and a **submodule** (protect `.git/modules/*/config`).
  Assert the exact protected/writable/read-grant sets.
- [ ] Implement gitdir pointer resolution + the surface classifier.
- [ ] Adversarial verify against the spec's git-metadata section (can a
  `core.hooksPath` redirect persist? — assert every config file git would read is
  in the protected-write set). Fix, commit.

## Task 4 — Resolve() + fail-closed floor

**Files:** `agent/sandbox/resolve.go` (new), `resolve_test.go` (new).

- [ ] **Failing test:** for each (mode × host row) drive `Resolve` and assert the
  resolved roots/masked-paths/net/cache-strategy **or** a typed `*RefusalError`
  with the right reason, exactly per the spec floor matrix:
  bwrap-capable → all modes; Landlock-only → only `restricted` in a linked
  worktree net=on, everything else refuses naming bwrap; neither/Windows → every
  sandboxed mode refuses. Include: `workspace-write` cache-strategy = overlay
  when `HostFacts.overlaySupported` else session-private; `restricted` = always
  session-private; net=off requires bwrap.
- [ ] Implement `Resolve` composing Tasks 1–3 + host facts.
- [ ] **Fuzz target** `FuzzResolve` (arbitrary mode/net/roots/host): never
  panics; a returned `ResolvedPolicy` never lists a denylisted or pseudo-fs path
  as readable/writable; refusals are always typed.
- [ ] Adversarial verify. Fix, commit.

## Task 5 — Contract harness (the exported deliverable)

**Files:** `agent/sandbox/contract.go` (new), `contract_test.go` (new).

- [ ] **Failing test:** `contract_test.go` builds the golden `[]ContractCase`
  table covering every (mode × host row × net) cell and calls
  `AssertResolve(t, sandbox.Resolve)`. This is the table M2/M3/M6 will re-run
  against their backends.
- [ ] Implement `ContractCase` + `AssertResolve` (exported). Keep the golden
  table data-only so backends import the *cases*, not just the assert.
- [ ] Adversarial verify (is every floor-matrix cell present? are the expected
  values copied from the spec, not from the implementation — i.e. would a wrong
  `Resolve` be caught?). Fix, commit.

## Task 6 — Inert carrier plumbing (execenv + config + flags)

**Files:** `agent/execenv/local.go`, `agent/session_config.go`,
`agent/subagents.go`, `cmd/serf/run.go`, `cmd/serf/serve.go` (all modify);
`agent/sandbox/plumbing_test.go` or extend existing config tests.

- [ ] **Failing test:** `--sandbox restricted --sandbox-net off` parses into
  `SessionConfig`; a non-off mode on a `FakeProber` that can't satisfy it makes
  session construction return the typed refusal; `--sandbox off` (default)
  constructs an env whose `Sandbox` is nil and behaves exactly as today
  (regression: an existing execenv test still passes unchanged). Assert
  `WithWorkingDirectory` copies the field.
- [ ] Implement the carrier field (inert), config fields + carry-through, the
  name↔enum helper, the two flags, and the resolve-or-refuse at construction
  (guarded so `off` is untouched).
- [ ] Adversarial verify (is the policy truly inert — grep that no
  enforcement path reads it yet? does `off` produce byte-identical behavior?).
  Fix, commit.

## Done criteria

- `cd <worktree> && make test-short && make vet && make lint` clean.
- `go test ./agent/sandbox/...` green incl. the fuzz targets' seed corpus.
- The contract table exists and is exported for downstream import.
- `--sandbox`/`--sandbox-net` parse; a non-off mode the host can't satisfy
  refuses to start; `off` is a verified no-op.
- Merge `wip/sandbox-m1` → `wip/sandboxing`; update the M0 status ledger; report.
