# Serf Sandboxing — M5: `--sandbox` Goes Live on Linux (the review gate)

> **For agentic workers:** Implement with superpowers:subagent-driven-development,
> task-by-task, red→green→adversarial-verify→commit. Follow the SDD protocol in
> `2026-07-08-sandboxing-m0-master.md`. Design source:
> `docs/superpowers/specs/2026-07-08-sandboxing-design.md` (v4) — the **whole
> Validation section** is M5's acceptance gate.

**Goal:** Flip `--sandbox`/`--sandbox-net` from *wired-but-inert* (registered in
M1, enforced-internally in M2/M3/M4, never reaching a real user) to
**user-visible and live on Linux**, and prove the promise holds by running the
spec's **adversarial escape suite** green end-to-end against the real host
(bwrap 0.9.0, kernel 6.8) and the Landlock path. M5 ships **no new enforcement
mechanism** — every escape defense already exists from M2/M3/M4. M5 is exposure
(help text + docs), the *single* live-flip, full-system validation, and the
human review checkpoint that turns a reviewable branch into a shipped feature.

**Why last — the precondition gate (state this loudly):** M5 ships **only after
M3 AND M4 are both merged into `wip/sandboxing` and green.** The moment
`--sandbox` is user-visible, a live sandboxed session can `delegate` — so
subagent/worktree inheritance (M4: re-rooting, descriptor persistence, resume,
cross-lane isolation) MUST already hold, or the first delegate punches a hole in
the boundary the user opted into. This is also a **human review checkpoint**:
Jesse signs off before the flag is exposed. Do not open the flag on a tree where
either M3 or M4 is absent, red, or unreviewed. If you cannot confirm both are
merged and green, **stop and report** — this milestone has not started.

**What changes (and what emphatically does not):** No new subsystem. The edits
are: (1) finalize the two flags' help/usage strings + env-var docs so they read
as a supported feature; (2) the **one** behavioral flip that lets a CLI-set mode
actually engage the M2/M3/M4 enforcement on the live run/serve path; (3) a
real-host-gated escape suite that runs every spec vector to green; (4)
verification (and bugfix-only correction) of the startup enforcement line across
host-capability tiers; (5) live denial-UX feel-testing; (6) user-facing docs.
`off` (the default) stays **byte-identical to today** — the flip must be a
provable no-op for `off`.

**Tech Stack:** Go 1.25, existing deps only. Validation runs against the real
host: bwrap 0.9.0 + kernel 6.8 (this box is bwrap-capable → exercises *all*
modes and `net=off`) and the Landlock-only path (exercises `restricted` in a
linked worktree, net=on — the sole cell Landlock serves). Live feel-testing uses
the **`verify`** and **`e2e-scenario-testing`** skills. Escape-suite tests
follow the repo's integration-gate convention (skip under `-short`; skip with a
legible message when bwrap/kernel prerequisites are absent) — see
`cmd/serf-hub/e2e_test.go:21-33`.

**Anchors** (re-verify against the **post-M4 tree** before editing — M1–M4 have
already added the flags, the config carrier, and the enforcement reads; these
lines *will* have drifted):
- `cmd/serf/main.go:168-198` `newRunFlagSet` (the `--sandbox`/`--sandbox-net`
  registrations land among these); `:200-204` `fs.Usage`; `:206` `printRunUsage`;
  `:235` `printLongFlagDefaults` — **auto-lists every registered flag via
  `fs.VisitAll`, so the flags already render in `serf --help`; M5 finalizes their
  usage strings, it does not add them to a curated list**; `:248`
  `printRunEnvVars` (add any `SERF_SANDBOX*` env var here if one exists).
- `cmd/serf/serve.go:64-103` serve flag registrations; `:105-110` `fs.Usage` →
  `fs.PrintDefaults()` (same auto-list); `:528` `printServeEnvVars`.
- `cmd/serf/run.go:177` `env := execenv.NewLocalExecutionEnvironment(cfg.workDir)`
  and `cmd/serf/serve.go:203` `env := execenv.NewLocalExecutionEnvironment(wd)` —
  the CLI→enforcement seam. Whatever guard M1–M4 used to keep the resolved policy
  from engaging on this live path is the single point M5 flips.
- `cmd/serf-hub/sandbox_test.go:22-25` — the containment-invariant **tripwire
  pattern** (`sandboxOutOfRootSecret` planted above the allowed root; finding it
  in any output = a path-escape defect). The escape suite extends this idea.
- Startup enforcement line: emitted by M3e (grep the post-M4 tree for the "one
  startup line states backend + exact enforcement set" site before editing).

## Global Constraints

- **No new enforcement mechanism.** If you find yourself writing a masking rule,
  a bwrap arg, an `openat2` call, or a re-rooting path, you've left M5 — stop.
  The escape defenses are M2/M3/M4's; M5 exposes and validates them. The only
  production-code edits are: flag usage strings, env-var help, the single
  live-flip, and any *bugfix* the startup line / escape suite proves is needed.
- **The flip is the one behavioral change, and `off` is a proven no-op.** Locate
  the single guard M1–M4 used to keep the resolved policy inert on the live
  run/serve path (the env constructed with a nil sandbox, or a package-level
  enforcement gate). Removing it must be the *only* thing that changes runtime
  behavior for a non-`off` mode, and must change **nothing** for `off`. Prove the
  no-op with a before/after regression, not by inspection.
- **Human gate before exposure.** Tasks 2–4 (validation) run first and must be
  green; Jesse reviews; only then does Task 1's exposure + flip land on a branch
  cut for merge. Do not expose an unproven flag.
- **Fail-closed floor is honest at the CLI.** A mode the host can't satisfy still
  refuses to *start* (M1 behavior) — M5 changes none of that; it only makes the
  now-refusable/now-enforceable request reachable by a real user.
- Test output **pristine**; captured-and-asserted denial errors are fine, stray
  logs are not. snake_case for any new wire/config/flag key (`make lint` gate).
- Never `git add -A` without a prior `git status`. Stage exact paths.

## Branch

Cut **`wip/sandbox-m5`** from `wip/sandboxing` **after M3 and M4 have merged**.
Merge back to `wip/sandboxing` when done. Nothing touches `main` — Jesse merges
to main after his sign-off.

## File Structure

- `cmd/serf/main.go` (modify) — finalize `--sandbox`/`--sandbox-net` usage
  strings in `newRunFlagSet`; add any sandbox env var to `printRunEnvVars`.
- `cmd/serf/serve.go` (modify) — same for the serve flag set + `printServeEnvVars`.
- `cmd/serf/run.go` + `cmd/serf/serve.go` (modify, tiny) — the single live-flip
  at the env-construction seam (`run.go:177` / `serve.go:203`).
- `agent/sandbox/escape_test.go` (new, real-host-gated) — **the named
  adversarial escape deliverable**: drives a real sandboxed session end-to-end
  through the live flag and asserts every spec vector. Extends the
  `sandbox_test.go` tripwire pattern (plant a secret outside every allowed root;
  its appearance in any tool output — file tool, shell, subagent — is a defect).
  Where M2/M3/M4 already unit-test a vector, this adds the *integrated* live-flag
  assertion; it does not duplicate the unit test.
- `docs/sandboxing.md` (new) — the user-facing guide (modes, flags, floor,
  per-mode guarantees, host tiers, denial UX, hooks/MCP-under-sandbox caveats).
  Use the **maintaining-documentation** skill for placement/terminology.
- `docs/superpowers/plans/2026-07-08-sandboxing-m0-master.md` (modify) — tick the
  M5 box in the status ledger when done.

## Task 0 — Precondition gate (no code; do not skip)

- [ ] Confirm on `wip/sandboxing`: M3 **and** M4 are merged, `make test`/`vet`/
  `lint` clean, and both were reviewed. Confirm this host is bwrap-capable
  (`bwrap --version` resolves; kernel ≥ the spec floor) so the suite can exercise
  all modes.
- [ ] **Post the review checkpoint to Jesse** and get explicit go/no-go before
  exposing the flag. If M3 or M4 is missing/red/unreviewed, **stop and report** —
  M5 has not started.

## Task 1 — Expose the flags + the single live-flip

**Files:** `cmd/serf/main.go`, `cmd/serf/serve.go`, `cmd/serf/run.go`.
(Land this **after** Tasks 2–4 are green and Jesse has signed off — exposure is
the last thing to move.)

- [ ] **Failing test:** (a) `serf --help` and `serf serve --help` render
  `--sandbox <mode>` and `--sandbox-net <on|off>` with the final, documented
  usage text (assert the strings, incl. the mode list and the default). (b) A
  regression proving the **live-flip is a no-op for `off`**: an existing execenv
  behavior test passes byte-identically with the flip in place. (c) A non-`off`
  mode set purely via the CLI now *engages* enforcement on the live run/serve
  path (a write outside the worktree is denied) where before the flip it was
  inert — this is the observable difference the flip creates.
- [ ] Finalize the two usage strings; add any `SERF_SANDBOX*` env var to the two
  env-var help sections; remove any "experimental / not-enforced" marker M1–M4
  left. Perform the single live-flip at the env-construction seam so a CLI-set
  mode attaches the enforced `ResolvedPolicy` (and rides to subagents via M4's
  `WithWorkingDirectory` carry). Re-verify the guard's exact shape against the
  post-M4 tree first.
- [ ] Adversarial verify (is the flip truly the *only* behavioral change? Does
  `off` produce byte-identical behavior — grep that the `off`/nil path is
  untouched? Do help strings match the docs written in Task 5?). Fix, commit.

## Task 2 — Adversarial escape suite green end-to-end (the acceptance gate)

**Files:** `agent/sandbox/escape_test.go` (new), real-host-gated.

Assemble the spec's full named suite and run it to green against **the real
host** (bwrap 0.9.0, kernel 6.8) **and** the Landlock path. Each vector below is
a `t.Run` that drives a real sandboxed session (no mocks — Jesse's e2e rule) and
asserts denial + a legible typed error, using the out-of-root-secret tripwire.
Where M2/M3/M4 already cover a vector at unit level, add the integrated live-flag
assertion; where the suite exposes a gap or a genuine defect, fix the defect in
the owning layer (that's an allowed bugfix), do **not** paper over it in the test.

- [ ] **Failing test** — the suite skeleton with every vector present, red until
  driven against a live sandboxed session:
  - **symlink-out** via file tools (`read_file`/`write_file`/`edit_file`) **and**
    shell — expect confinement denial.
  - **TOCTOU symlink-swap race** during read / write / rename / **apply_patch**
    with a concurrent model-spawned job flipping a path component.
  - **`read_file("/proc/<serf-pid>/environ")`** (must not leak serf's provider
    API key) + **`/proc/1/root`** + **`/proc/<pid>/root`** aliasing — denied on
    **both** layers (file tool + spawned proc).
  - inherited-**fd / agent-socket** egress — spawned command sees no serf fds
    beyond stdio, no ssh/gpg-agent socket.
  - **`ld-linux.so <denied-binary>`** indirect exec — still confined.
  - **`.git/hooks` write + `core.hooksPath` redirect** persist attempt;
    `config.worktree` / submodule-config tamper — all config/hook surfaces
    write-denied, redirect can't persist.
  - **`~/.bashrc` / `~/.gitconfig` write** — write-confinement denial (`$HOME`
    never writable).
  - **denylist read via every file tool incl. `apply_patch`** — refused
    uniformly (apply_patch now routes through the race-safe layer).
  - **hardlink create-then-use vs. masked secret** — blocked by masking; the
    **pre-existing-hardlink** read/write-through residual asserted as a
    *documented, known-open* residual (not claimed closed).
  - **cache-poison-then-consume** — overlay (warm) or session-private (cold)
    isolation; a later build never consumes the sandbox's cache writes.
  - **net=off egress** — TCP **and** UDP/DNS **and** provider-native web (the
    provider-capability registry fails closed on unknown capabilities).
  - **worktree-lane cross-reads** — a delegate in lane A cannot read lane B
    (M4 inheritance, validated live end-to-end here).
  - **Edge cases pinned** (from the spec): nested worktrees, submodules, a
    resumed session re-applying policy, deleted-then-recreated roots, delegate
    resume after a serf restart, and overlay-unavailable degradation.
- [ ] Drive each vector to green on bwrap; re-run the Landlock-servable subset on
  the Landlock path. Fix any real defect at its source layer.
- [ ] Adversarial verify (does every spec Validation vector have a case? does
  each case fail loudly if the defense were removed — i.e. would a neutered
  policy be *caught*, not silently pass? is the tripwire actually planted outside
  **every** allowed root?). Fix, commit.

## Task 3 — Startup enforcement line correct across host tiers

**Files:** the M3e startup-line site (verify only; bugfix if wrong).

- [ ] **Failing test:** assert the single startup line names the **backend** +
  the **exact enforcement set** + **warm-overlay vs cold-cache** for each host
  tier — bwrap-capable warm-overlay, bwrap-capable cold-cache (overlay
  unavailable), Landlock-only (`restricted`/linked-worktree/net=on), and
  neither → the fail-closed refusal message. Drive with the M1 `FakeProber` rows
  plus one real-host assertion on this box.
- [ ] If the line misreports any tier, fix it (bugfix, not new mechanism).
- [ ] Adversarial verify (does the line ever *overstate* enforcement — claim
  warm-overlay when it degraded to cold, or claim a mode the backend can't
  serve? Overstatement is the dangerous failure). Fix, commit.

## Task 4 — Live denial-UX feel-testing (verify + e2e-scenario-testing)

- [ ] Use the **`verify`** and **`e2e-scenario-testing`** skills to run a **real
  sandboxed serf session** (real model, real bwrap) and drive it into denials:
  a write outside the worktree, a denylisted read, a `net=off` fetch, a
  `git config --local` (the legible-denial case the spec calls out).
- [ ] Confirm the model receives a **legible, typed denial** it can reason about
  and **does not spin in a retry loop** (the failure mode the spec's Validation
  section flags). Capture the transcript; if the model loops or the error is
  opaque, that's a denial-legibility defect to fix in the owning layer.
- [ ] Record the scenario cards + observations; commit any fixes red→green.

## Task 5 — User-facing documentation

**Files:** `docs/sandboxing.md` (new). Use the **maintaining-documentation** skill.

- [ ] Write the user guide: the four modes and exactly what each guarantees
  (the spec's mode table, in user language); `--sandbox` / `--sandbox-net`
  flags + defaults (net **on** when sandboxed); the fail-closed **floor** and the
  per-host capability tiers (bwrap = all modes; Landlock = `restricted` in a
  linked worktree, net=on; neither/Windows = refuse); the secrets+pseudo-fs
  denylist and its user-extensibility (both directions, never model-changeable);
  git-config/hooks read-only consequence (`git config --local` fails by design);
  cache containment (no poisoning in any mode); the denial UX; and the
  hooks/MCP-under-sandbox caveats (broader-access hooks are incompatible with
  sandboxed sessions — documented).
- [ ] Cross-link from the top-level docs index / help where sibling docs
  (`docs/worktrees.md`, `docs/environment.md`) are referenced.
- [ ] Adversarial verify the doc against the shipped behavior (does every claim
  match Task 2's observed results? no invented flags/vars — verify each against
  the code). Fix, commit.

## Done criteria

- Task 0 gate satisfied: M3 + M4 merged and green; Jesse's go/no-go on record.
- `cd <worktree> && make test-short && make vet && make lint` clean.
- The named escape suite (`agent/sandbox/escape_test.go`) is **green
  end-to-end** on the real host (bwrap 0.9.0, kernel 6.8) and on the Landlock
  path; every spec Validation vector + pinned edge case is present and would fail
  if its defense were removed.
- `serf --help` / `serf serve --help` show `--sandbox` and `--sandbox-net` with
  final documented text; the live-flip engages enforcement for a non-`off` mode
  and is a **proven byte-identical no-op for `off`**.
- The startup enforcement line is correct (backend + exact set + overlay state)
  across every host tier and never overstates.
- Live feel-test transcript on record: legible denials, **no model retry loops**.
- `docs/sandboxing.md` written, verified against shipped behavior, cross-linked.
- Merge `wip/sandbox-m5` → `wip/sandboxing`; tick the M5 box in the M0 ledger;
  report. (Jesse merges to `main`.)
