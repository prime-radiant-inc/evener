# sandbox-edge-cases: the per-delegate + launch-config refusal matrix (unit-backed)

**What this covers**: the agent- and user-visible refusals guarding the
per-delegate sandbox surface and the launch-config sandbox fields — the cases that
are pure/decode-level and fully unit-tested, so no live model run is needed to
prove them. This card documents the EXACT string the agent (or the settings user)
sees for each, and pins each to its test. If any refusal string drifts or a guard
is dropped, the cited test fails; this card is the human-readable index of them.

The two agentic refusals that a live model CAN drive (the mode floor under a
restricted parent) are live-validated in `2026-07-09-sandbox-delegate-live-e2e.md`
(Scenario B). This card is the exhaustive backstop for the rest.

## Pre-state
None — every case here is a pure or decode-path unit. Run the whole set:
```
$ go test ./agent/ -run 'DelegateSandbox|SandboxFloor|SandboxNet|DecodeDelegateArgs|AtLeastAsConfining'
$ go test ./cmd/serf-hub/internal/launchconfig/ -run 'Sandbox'
```
All PASS on `wip/sandbox-config-delegate` (2026-07-09).

## The refusal matrix

Each row: the request the agent/user makes, the exact refusal string, and the
test. All delegate-side refusals are `invalid_request:` errors surfaced as the
`delegate` tool's error result (the model reads them and adapts, as in the live
Scenario B).

### 1. `sandbox="off"` paired with an explicit `sandbox_net`
- **Why refused**: `off` applies NO network confinement (`Resolve` hard-codes net
  on for `ModeOff`; `EnableSandbox` treats an off policy as a no-op), so an
  explicit `sandbox_net` would silently run with full network while the caller
  believes egress is off. Refused loudly rather than silently accepted.
- **String**: `invalid_request: sandbox_net has no effect with sandbox="off" (off applies no network confinement); pass a non-off sandbox mode or omit sandbox_net`
- **Test**: `agent/sandbox_delegate_floor_test.go` `TestBuildDelegateSandboxPolicy_OffWithNetRefused` (covers both `sandbox_net=true` and `false`, and the route through `resolveDelegateSandboxRequest`).

### 2. `sandbox_net` set WITHOUT a `sandbox` mode, under an UNSANDBOXED parent
- **Why refused**: network confinement is meaningless without a sandbox; silently
  dropping the flag would be a surprising no-op. (Under a *sandboxed* parent the
  same request is NOT an error — it inherits the parent's mode and applies the
  tighter network. The refusal is specific to an unsandboxed/off parent.)
- **String**: `invalid_request: sandbox_net requires a sandbox mode; your session is not sandboxed, so pass sandbox=... as well`
- **Tests**: `agent/sandbox_delegate_floor_test.go` `TestResolveDelegateSandboxRequest_NetOnlyInheritsMode` (both the sandboxed-inherit and unsandboxed-refuse branches); `agent/sandbox_delegate_create_test.go` `TestCreateDelegate_SandboxNetWithoutModeRefusedEarly` (refused EARLY — no delegate id minted).

### 3. Malformed (non-boolean) `sandbox_net`
- **Why refused**: a present-but-non-boolean value (e.g. the string `"false"` from
  a non-strict provider) is refused rather than silently decoded as inherit — the
  same silent no-op this surface refuses elsewhere. An ABSENT key stays nil
  (inherit); it is NOT read as false.
- **String**: `invalid_request: sandbox_net must be a JSON boolean (true or false, not a quoted string)`
- **Test**: `agent/job_delegate_decode_test.go` `TestDecodeDelegateArgs_SandboxNetMalformed` (+ `TestDecodeDelegateArgs_Sandbox` for the nil-stays-nil / bool-carries cases).

### 4. Unknown `sandbox` mode on the delegate tool
- **Why refused**: a mistyped mode fails loudly rather than silently disabling the
  box.
- **String**: `invalid_request: unknown sandbox mode "bogus" (want one of: off, read-only, workspace-write, restricted)`
- **Test**: `agent/sandbox_delegate_floor_test.go` `TestBuildDelegateSandboxPolicy_UnknownMode`.

### 5. Mode floor — a delegate looser than its parent (the security invariant)
- **String** (e.g. `off` under a `restricted` parent): `invalid_request: sandbox "off" allows access on an axis your restricted sandbox forbids (it is not at least as confining on both reads and writes); modes allowed under your restricted sandbox: restricted` (partial-order-aware: names the failing axis and lists the recoverable modes — the modes at least as confining as the parent, in `AllModes` order)
- **Network floor** (net-on under a net-off parent): `invalid_request: sandbox_net on grants more network access than your own sandbox (network off); a delegate cannot be less restricted than you; omit sandbox_net or pass sandbox_net=false`
- **Tests**: `agent/sandbox/policy_test.go` (`AtLeastAsConfining`, the full 16-cell
  matrix incl. the incomparable `read-only`∥`restricted` refused BOTH directions);
  `agent/sandbox_delegate_floor_test.go` `TestBuildDelegateSandboxPolicy_ModeFloor`
  and `TestBuildDelegateSandboxPolicy_NetworkFloor`;
  `agent/sandbox_delegate_create_test.go` `TestCreateDelegate_SandboxFloorRefusedEarly`
  (refused BEFORE any id/worktree mint). Live-validated: `off` under `restricted`
  in `2026-07-09-sandbox-delegate-live-e2e.md` Scenario B.

### 6. Typo'd launch-config sandbox mode (settings-user visible)
- **Why**: a typo'd mode merges cleanly and would otherwise only fail at spawn
  (serf's `ParseMode` dies) with no launch-config pointer at the typo. The merge
  emits a diagnostic pointing at the offending layer; fail-loud-at-spawn remains
  the backstop.
- **Diagnostic** (`Field: "sandbox"`): `unknown sandbox mode "<value>" (want one of: off, read-only, workspace-write, restricted)`
- **Test**: `cmd/serf-hub/internal/launchconfig/merge_test.go` `TestMerge_UnknownSandboxModeDiagnostic`.

### 7. Launch-config `sandbox_net` set without a non-off mode (settings-user visible)
- **Why**: `sandbox_net` only takes effect alongside a non-off mode; a merged
  effective config with net set but no (or off) mode is a silent no-op at
  `serf serve`. `ToArgs` suppresses the flag AND `merge.go` warns (checked on the
  EFFECTIVE config, since mode and net may arrive from different layers).
- **Diagnostic** (`Field: "sandbox_net"`): `sandbox_net has no effect without a non-off sandbox mode`
- **Tests**: `cmd/serf-hub/internal/launchconfig/merge_test.go` `TestMerge_SandboxNetWithoutModeDiagnostic`; `args_test.go` `TestToArgs_Sandbox` (the `net on, no mode` / `net off, no mode` rows emit nothing).

## Sharp edges
- Cases 1–5 are DELEGATE-tool refusals (the model sees `invalid_request` and
  adapts); cases 6–7 are LAUNCH-CONFIG diagnostics (a settings user sees them at
  merge time, before spawn). Different surface, different reader — don't conflate.
- The distinction in case 2 is load-bearing: net-only is a legitimate *tightening*
  under a sandboxed parent (inherits the mode) and only an error under an
  unsandboxed one. A test that only checked the off-parent branch would miss the
  inherit path.
- These are intentionally unit-tested rather than live-driven: they are pure /
  decode-level and a live model adds no coverage over the exact-string assertions
  here. The one refusal worth a live run — the model actually reading a floor error
  and adapting without looping — is covered by the live Scenario B.
- Explicit `sandbox="off"` is NOT a distinct off-box: when it passes the floor (only
  under an off parent) `buildDelegateSandboxPolicy` returns `(nil, nil)`, i.e. the
  inherit path — the child inherits the (off) parent env rather than provisioning a
  separate EnableSandbox(off) round-trip. So `off` under an off parent is allowed and
  outcome-identical to omitting `sandbox` entirely.
- Strings re-verified against the branch after the consolidated review-fix pass
  (2026-07-09): the non-boolean `sandbox_net` message and the network-floor message
  both gained more directive phrasing (recorded above); the floor message became
  partial-order-aware (case 5). All cited tests re-run green.
