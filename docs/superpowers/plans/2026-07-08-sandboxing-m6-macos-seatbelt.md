# Serf Sandboxing — M6: macOS Seatbelt Backend

> **For agentic workers:** Implement with superpowers:subagent-driven-development,
> task-by-task, red→green→adversarial-verify→commit. Follow the SDD protocol in
> `2026-07-08-sandboxing-m0-master.md`. Design source:
> `docs/superpowers/specs/2026-07-08-sandboxing-design.md` (v4), section
> **"Backend (macOS)"** + the shared **Modes**/**Backends** policy model.

**Goal:** Give serf a third enforcement backend — Apple's `sandbox-exec`
(Seatbelt) — that turns M1's `ResolvedPolicy` into an SBPL policy and runs
spawned commands under `/usr/bin/sandbox-exec -p <policy> -- <cmd>`. Seatbelt is
**deny-capable** (`(deny default)`), so — unlike Landlock — **every mode is
expressible**: `read-only`, `workspace-write`, and `restricted` on a main
checkout all map to concrete SBPL, net on *and* off. M6 plugs into the same
backend-selection seam M3 builds at the command-wrap sites, gated on
`runtime.GOOS == "darwin"`, and re-runs M1's exported cross-backend contract
suite so macOS semantics can't drift from Linux — plus explicit live parity
tests (network denial, pseudo-fs/process exposure, path-race) on **paradise-park**.

**Why this shape:** The SBPL *generation* is pure string/param assembly from
`ResolvedPolicy` — it compiles and runs on any host, so the generator's unit
tests, golden `.sbpl` snapshots, and the M1 contract suite all run on the Linux
CI host. Only *invocation* (the hard-coded `/usr/bin/sandbox-exec` exec) and the
in-process fd-walk are darwin-specific, behind `//go:build darwin`. This keeps
the bulk of M6 testable off-Mac and confines the paradise-park round to live
enforcement smoke + the three parity suites.

**Architecture:** New Seatbelt files in the existing **`agent/sandbox`** package
(from M1). A build-tag-free `SeatbeltPolicy(rp *ResolvedPolicy, cwd) (text
string, params []DirParam)` emits SBPL in layered sections — a static embedded
base, then read / write / deny / network sections derived from the resolved
grants — with every root passed **out-of-band** as a `-D KEY=path` definition and
referenced inside the policy via `(param "KEY")` (never string-interpolated —
SBPL-injection defense). A darwin-only `seatbeltWrap` prepends
`/usr/bin/sandbox-exec -p <text> -D…= -- <argv…>`; a non-darwin stub of the same
signature reports the backend unavailable so `local.go` stays build-tag-free. The
resolver gains a **darwin/seatbelt-capable** floor row (all modes; cache-strategy
= session-private; net on/off both fine), and M1's contract table gains the
matching cases.

**Tech Stack:** Go 1.25, stdlib. Static base policies embedded via `//go:embed`
(mirrors codex's `include_str!`). `//go:build darwin` / `//go:build !darwin`
split for invocation + fd-walk. No new external dep. Table-driven `testing` +
golden `.sbpl` snapshot files (Linux) + a fuzz target on the SBPL escaper +
`CGO_ENABLED=0 GOOS=darwin go build ./...` cross-build gate (Linux). Live smoke
via real `/usr/bin/sandbox-exec` on paradise-park.

**Anchors** (re-verify against the live file before editing — M1/M3 will have
moved these):
- M1 deliverables (must be landed on `wip/sandboxing` first): `agent/sandbox/policy.go`
  `Mode` / `ResolvedPolicy` (writable roots, read roots, masked paths incl.
  pseudo-fs, net decision, cache-strategy, backend-required);
  `agent/sandbox/resolve.go` `Resolve(policy, HostFacts, cwd)`;
  `agent/sandbox/probe.go` `HostFacts` (`OS` field) / `Prober` / `FakeProber`;
  `agent/sandbox/gitdir.go` config-surface protection set;
  `agent/sandbox/contract.go` `ContractCase` / `AssertResolve`.
- The command-wrap seam (M3 builds it; M6 adds the darwin arm):
  `agent/execenv/local.go:1110` `shellCommand`; `:768` `execPreparedCommand`;
  `:866` `StreamCommand` (`cmd := shellCommand(command)` at `:889`); `:756`
  `ExecCommand`. Empty `ExtraFiles` + `O_CLOEXEC` fd hygiene rides the same seam.
- `WithWorkingDirectory` `:123` (carries the policy for re-rooting — M4's concern,
  noted so M6 doesn't regress it).
- **Reference to study, not copy:** `inspo/codex/codex-rs/sandboxing/src/seatbelt.rs`
  (`create_seatbelt_command_args`, `build_seatbelt_access_policy`,
  `seatbelt_protected_metadata_name_regex`) + the three `.sbpl` includes
  (`seatbelt_base_policy.sbpl`, `seatbelt_network_policy.sbpl`,
  `restricted_read_only_platform_defaults.sbpl`) + `manager.rs:195-214` (the
  `/usr/bin/sandbox-exec` argv assembly).

## Global Constraints

- **Deny-default, additive allows.** Every section is an `(allow …)` on top of a
  static `(deny default)` base. **net=off is the *absence* of a network allow
  section, not an explicit deny** — fail-closed by construction. Never emit an
  allow that isn't backed by a `ResolvedPolicy` grant.
- **Roots are params, never interpolated.** Each root/excluded-subpath is passed
  as `-D KEY=path` and referenced via `(param "KEY")`. Path *text* never enters
  the policy string. The only place a path becomes SBPL text is the protected-
  metadata anchored regex (`.git`), where it MUST go through the SBPL escaper.
  This defeats a crafted worktree path (`"` `)` newline) from breaking out of a
  literal or silently widening the policy.
- **`/usr/bin/sandbox-exec` is hard-coded.** Never resolve via `PATH` or
  `exec.LookPath` (PATH-injection defense — the whole point of the fixed path).
- **Satisfy M1's contract; don't weaken Linux.** M6 adds the darwin/seatbelt-capable
  floor row and imports M1's `ContractCase` set unchanged. If a change to
  `resolve.go` shifts any Linux cell, you've broken M1 — stop.
- **Build-tag discipline.** Pure SBPL generation is build-tag-free (compiles on
  Linux). Only `seatbelt_darwin.go` (invocation, fd-walk) and its `!darwin` stub
  carry tags, and both provide identical signatures so `local.go` never sees a
  tag. `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...` must pass on Linux.
- **macOS path canonicalization.** Seatbelt matches on *canonical* paths:
  `/tmp`→`/private/tmp`, `/var`→`/private/var`, and `$HOME` under `/Users` may be
  firmlinked. Roots are canonicalized (symlink-resolved) before becoming `-D`
  params, via an injectable canonicalizer (identity in unit tests, real
  `filepath.EvalSymlinks` on-host). A root that fails to canonicalize is passed
  through, never dropped.
- **Do NOT run macOS tests on this Linux host.** Linux runs the generator/golden/
  contract tests + the cross-build gate. **paradise-park** runs `make test` and
  the live `sandbox-exec` smoke + parity suites.
- **snake_case** for any JSON/config/flag key that hits the wire (`make lint` /
  serf-namingcheck gate). Never `git add -A` without a prior `git status`.

## File Structure

- `agent/sandbox/seatbelt.go` (new, no build tag) — `SeatbeltPolicy(rp
  *ResolvedPolicy, cwd string) (string, []DirParam)`; `DirParam{Key, Path}`;
  section builders (`readSection`, `writeSection`, `denySection`, `netSection`);
  the `-D` param allocator (`WRITABLE_ROOT_n`, `READABLE_ROOT_n`,
  `…_EXCLUDED_m`); the SBPL string/regex escaper. Pure — unit + golden tested on
  Linux.
- `agent/sandbox/seatbelt_base.sbpl` (new, embedded) — `(version 1)` +
  `(deny default)` + `process-exec`/`process-fork`/`signal (target same-sandbox)`
  + `process-info* (target same-sandbox)` + the minimal sysctl/pty/`/dev/null`
  floor. Trimmed from codex's base (drop CFPrefs/IOKit lines serf doesn't need;
  keep exec+pty+process-info-same-sandbox — the last is the macOS analog of
  Linux's private-`/proc`).
- `agent/sandbox/seatbelt_platform_defaults.sbpl` (new, embedded) — the macOS
  system read-roots (`/usr/lib`, `/System/Library/Frameworks`, dyld shared cache
  paths, `/bin`, `/dev/random`, `/etc`, …) required for *any* binary to exec/dyld-
  load. Appended only for `restricted` (the macOS realization of the spec's
  "system read roots"). Distilled from `restricted_read_only_platform_defaults.sbpl`.
- `agent/sandbox/seatbelt_network.sbpl` (new, embedded) — the net=on service
  allow block (DNS/SecurityServer/configd mach-lookups). Appended only when the
  resolved net decision is on.
- `agent/sandbox/seatbelt_test.go` (new) — section unit tests + golden `.sbpl`
  snapshots per (mode × net) + the SBPL-escaper fuzz target. Linux-runnable.
- `agent/sandbox/testdata/seatbelt/*.sbpl` (new) — golden snapshots (params
  redacted to stable `KEY=<root>` placeholders so they're host-independent).
- `agent/sandbox/seatbelt_darwin.go` (new, `//go:build darwin`) —
  `pathToSeatbelt = "/usr/bin/sandbox-exec"`; `seatbeltWrap(argv []string, rp
  *ResolvedPolicy, cwd string) []string` → `[pathToSeatbelt, "-p", text,
  "-DKEY=path"…, "--", argv…]`; the real canonicalizer.
- `agent/sandbox/seatbelt_stub.go` (new, `//go:build !darwin`) — same
  `seatbeltWrap` signature returning `(nil, errSeatbeltUnavailable)` so the
  package builds everywhere; never reached off-darwin (floor refuses first).
- `agent/sandbox/resolve.go` (modify, small) — add the **darwin/seatbelt-capable**
  branch: `HostFacts.OS == "darwin"` (with `sandbox-exec` present) → all modes
  expressible, `backend = seatbelt`, cache-strategy = **session-private always**
  (no overlay on macOS), net on/off both satisfiable.
- `agent/sandbox/contract.go` (modify, small) — extend the exported case set
  (or its host-row constructor) with the darwin/seatbelt-capable row so
  `AssertResolve` covers it; keep it data-only.
- `agent/execenv/local.go` (modify, minimal) — add the `runtime.GOOS == "darwin"`
  arm to M3's backend dispatch at the wrap sites, calling `sandbox.seatbeltWrap`
  (platform-split). If M3 hasn't landed, M6 introduces the seam here guarded so
  `off`/nil-policy is byte-identical to today. fd hygiene (`ExtraFiles` empty,
  cloexec) is M3's; M6 only asserts it survives the seatbelt wrap.

## Task 1 — SBPL assembly skeleton + `-D` param model + escaper

**Files:** `agent/sandbox/seatbelt.go`, `seatbelt_base.sbpl`,
`seatbelt_test.go` (new).

- [ ] **Failing test** (`seatbelt_test.go`): `TestSeatbeltEnvelope` — for a
  trivial `ResolvedPolicy` (one writable root, no denylist, net off),
  `SeatbeltPolicy` returns a policy that (a) starts with the embedded base
  (`(version 1)`…`(deny default)`), (b) references the root only via `(param
  "WRITABLE_ROOT_0")`, never by literal text, and (c) yields exactly one
  `DirParam{Key:"WRITABLE_ROOT_0", Path:<root>}`. `TestSbplEscaper` — a root
  containing `"`, `)`, backslash, and a newline round-trips through the escaper
  such that the emitted regex/param can't terminate a literal or open a new form
  (assert the escaped bytes; feed it a fuzz seed).
- [ ] Implement the section-assembly envelope (base → read → write → deny → net,
  joined by `\n`), the `-D` param allocator, and the SBPL escaper (escape `"` and
  `\` for strings; `regexp.QuoteMeta`-style escape for the anchored-regex path
  segments). Sections are stubs returning "" for now.
- [ ] `FuzzSbplEscape` — arbitrary path bytes: escaper output never contains an
  unescaped policy-terminating character; never panics.
- [ ] Adversarial verify (can any path value reach the policy *text* except
  through the escaper? is the base embedded verbatim, not re-generated?). Commit.

## Task 2 — read / write / net sections from ResolvedPolicy (the mode matrix)

**Files:** `agent/sandbox/seatbelt.go`, `seatbelt_platform_defaults.sbpl`,
`seatbelt_network.sbpl`, `seatbelt_test.go`, `testdata/seatbelt/*.sbpl` (new).

- [ ] **Failing test:** golden snapshots for each spec mode against a fixed
  fixture `ResolvedPolicy`:
  - `read-only` net=off → `(allow file-read* (require-all (subpath (param "/"))
    (require-not …denylist…)))`, **no** `file-write*` allow (tmp-only writes
    come from the session-tmp writable root), no network section.
  - `workspace-write` net=on → read = full-disk-minus-denylist; write = worktree
    + session-tmp + cache-redirect roots (each an `(allow file-write* …)`);
    `+ seatbelt_network.sbpl`.
  - `restricted` net=on → read = worktree + session-tmp **+ the embedded
    platform-defaults** (system read roots); write = worktree + session-tmp;
    network section present. Assert platform-defaults is appended for
    `restricted` and **absent** for the other modes.
  - Net decision drives the network section: on → append `(allow
    network-outbound)`/`(allow network-inbound)` + `seatbelt_network.sbpl`; off →
    section is empty (deny-default blocks).
- [ ] Implement `readSection`/`writeSection`/`netSection` mapping
  `ResolvedPolicy.{ReadRoots,WritableRoots,Net,CacheStrategy}` onto `(subpath
  (param …))` allows; full-disk-minus-denylist as `(require-all (subpath (param
  ROOT_/)) (require-not …))`; platform-defaults gated on `restricted`.
- [ ] Adversarial verify against the **Modes** table (does `read-only` truly emit
  no persistent write allow? does `workspace-write` never grant a cache root as
  persistent-writable — only session-tmp redirect? is net=off free of *any*
  network allow across all sections?). Commit.

## Task 3 — git-metadata protection + excluded subpaths + macOS canonicalization

**Files:** `agent/sandbox/seatbelt.go`, `seatbelt_test.go` (modify).

- [ ] **Failing test** (uses M1's gitdir config-surface set on a real temp repo +
  a temp linked worktree): a writable worktree root that contains git metadata
  emits, *inside* its `file-write*` allow, `require-not` guards for every
  protected surface — `.git/config`, `config.worktree`, `.git/modules/*/config`,
  `.git/hooks` — as **both** `(require-not (literal (param EXCLUDED_n)))` **and**
  `(require-not (subpath (param EXCLUDED_n)))` (the codex two-form guard that
  closes first-time-create of the protected dir), plus the anchored
  `(require-not (regex #"^<root>/\.git(/.*)?$"))` for the metadata name. A linked
  worktree's **main** `.git/config` appears in a `file-read*` allow but never in
  `file-write*` (read-granted, write-denied — the v4 linked-worktree fix).
  `TestCanonicalizeRoots` — with a fake canonicalizer mapping `/tmp`→`/private/tmp`,
  the emitted `-D` param carries the canonical path.
- [ ] Implement the excluded-subpath / protected-metadata `require-not` emission
  (port the shape of `build_seatbelt_access_policy` + `seatbelt_protected_metadata_name_regex`)
  and the injectable canonicalizer applied to every root before it becomes a param.
- [ ] Adversarial verify against the spec's **Git-metadata protection** section:
  can a `core.hooksPath` redirect land? — assert every config file git reads is
  in a write-`require-not`; assert the `.git` regex is anchored (`^…$`) and
  escaped so a sibling like `.gitfoo` isn't caught and `.git/objects` **is** still
  writable. Commit.

## Task 4 — resolver darwin floor row + contract-suite parity (Linux-runnable)

**Files:** `agent/sandbox/resolve.go`, `agent/sandbox/contract.go`,
`resolve_test.go` / `contract_test.go` (modify).

- [ ] **Failing test:** extend the golden contract table with a
  **seatbelt-capable** host row (`HostFacts.OS == "darwin"`, sandbox-exec present)
  and assert `Resolve` yields, for every mode × net cell: the right roots/
  masked-paths/net, `backend = seatbelt`, and **cache-strategy = session-private
  in all modes** (no overlay on macOS — including `workspace-write`, which
  overlays on Linux). Assert a darwin host with sandbox-exec *absent* still
  refuses (fail-closed). Confirm no Linux cell changed (M1 regression).
- [ ] Implement the darwin branch in `Resolve` (all modes expressible; backend
  seatbelt; cache session-private; net on/off both satisfiable) and add the cases
  to the exported set so `AssertResolve` covers macOS.
- [ ] Adversarial verify (is the darwin row genuinely "all modes" like bwrap —
  including `restricted` on a *main* checkout, which Landlock refused? are the
  expected values copied from the spec's floor/Modes tables, not from the new
  code?). This whole task runs on the Linux host. Commit.

## Task 5 — backend wiring into the command-wrap seam (`//go:build darwin`)

**Files:** `agent/sandbox/seatbelt_darwin.go`, `seatbelt_stub.go` (new);
`agent/execenv/local.go` (modify, minimal); a darwin-tagged wrap test.

- [ ] **Failing test** (`//go:build darwin`, runs on paradise-park; a matching
  `!darwin` test asserts the stub path): `seatbeltWrap(["/bin/echo","hi"], rp,
  cwd)` returns argv beginning `["/usr/bin/sandbox-exec","-p",<policy>,"-DWRITABLE_ROOT_0=…","--","/bin/echo","hi"]`
  with the hard-coded path and `--` separator. A Linux `TestSeatbeltStubRefuses`
  asserts the `!darwin` stub returns the unavailable error. A regression test:
  with a nil policy (`off`), the seam leaves argv **byte-identical** to today's
  `shellCommand` output.
- [ ] Implement `seatbeltWrap` (darwin) + the stub (`!darwin`), the hard-coded
  `pathToSeatbelt`, and the `runtime.GOOS == "darwin"` arm in M3's dispatch at
  `shellCommand`/`execPreparedCommand`/`StreamCommand`. If M3's seam is absent,
  introduce it minimally, guarded so nil/`off` is untouched. Verify `ExtraFiles`
  stays empty and serf fds stay `O_CLOEXEC` through the wrap.
- [ ] Cross-build gate on **this Linux host**: `CGO_ENABLED=0 GOOS=darwin
  GOARCH=arm64 go build ./...` must pass (proves the darwin files compile without
  a Mac).
- [ ] Adversarial verify (grep that `sandbox-exec` is never `LookPath`'d; that
  `off` produces identical argv; that the darwin arm is unreachable on Linux).
  Commit.

## Task 6 — paradise-park: live enforcement smoke + the three parity suites

**Files:** a darwin-tagged `agent/sandbox/seatbelt_live_test.go` (guarded by an
opt-in env, e.g. `SERF_SEATBELT_LIVE=1`, so it never runs in a non-macOS/CI unit
pass); a small smoke script under `scripts/`.

Run on the Mac:

```
ssh paradise-park
git -C ~/serf fetch && git -C ~/serf worktree add ~/serf-m6 wip/sandbox-m6   # or clone
cd ~/serf-m6 && make test && make test-race
SERF_SEATBELT_LIVE=1 go test ./agent/sandbox/... -run Seatbelt
scripts/seatbelt-smoke.sh    # live /usr/bin/sandbox-exec allow/deny assertions
```

- [ ] **Failing test / live smoke:** for a real `ResolvedPolicy`, generate the
  policy and actually invoke `/usr/bin/sandbox-exec -p <policy> -D…= -- /bin/sh
  -c '<probe>'`, asserting the real kernel verdict — not just the emitted text:
  - **Network denial** (net=off): a spawned `curl`/`nc`/`dig`/`/dev/tcp` probe
    fails (no TCP, no UDP, no DNS); net=on the same probe succeeds. Matches the
    Linux `--unshare-net` observable.
  - **Pseudo-fs / process exposure:** a spawned proc cannot enumerate or read
    another (non-same-sandbox) process's info (`ps`/`sysctl kern.proc`/reading a
    host proc), and the file-tool denylist (secrets: `~/.ssh`, `~/.aws`, …) is
    refused — the macOS analog of Linux private-`/proc` + `/proc/<pid>/environ`
    denial (base policy's `process-info* (target same-sandbox)` is the mechanism).
  - **Path-race (TOCTOU):** the in-process file-tool race test (concurrent
    symlink/component swap during read/write/rename/apply_patch) passes on macOS
    via the `openat(O_NOFOLLOW|O_DIRECTORY)` fd-walk. **If M2 shipped Linux-only
    (`openat2`)**, add the darwin fd-walk sibling here (`//go:build darwin`,
    beneath the base-root fd) — coordinate with M2; do not re-implement M2's
    layer, only its macOS variant needed for parity.
- [ ] **Re-run M1's contract suite on-host** (`AssertResolve`) against real macOS
  `HostFacts` from the live prober — confirms the resolver agrees with the Mac it
  actually runs on, not just the fixture.
- [ ] Adversarial verify (do the *live* verdicts match what `ResolvedPolicy`
  claims for every mode? does net=off actually block UDP/DNS, not just TCP? does a
  `restricted` main-checkout session run at all — platform-defaults sufficient for
  `/bin/sh` to dyld-load?). Fix, commit. Capture any expected denial output and
  assert it (pristine-output rule).

## Done criteria

- **Linux gate** (this host): `go test ./agent/sandbox/...` green incl. golden
  snapshots + the escaper fuzz seed; `make vet && make lint` clean;
  `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...` passes.
- **paradise-park gate:** `make test && make test-race` clean;
  `SERF_SEATBELT_LIVE=1` suite + `scripts/seatbelt-smoke.sh` green; the three
  parity suites (network, pseudo-fs/process, path-race) pass; M1's `AssertResolve`
  passes against real macOS host facts.
- The exported contract table carries the darwin/seatbelt-capable row; **no Linux
  cell changed**. Seatbelt is selected only when `runtime.GOOS == "darwin"`; every
  other OS still refuses per M1's floor.
- Paths reach the policy only as `-D`/`(param …)` or via the escaper; `sandbox-exec`
  is the hard-coded `/usr/bin` path; net=off emits no network allow.
- Merge `wip/sandbox-m6` → `wip/sandboxing`; update the M0 status ledger (M6);
  report.
