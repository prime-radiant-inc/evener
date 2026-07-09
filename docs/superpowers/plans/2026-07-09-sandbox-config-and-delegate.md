# Sandbox: launch-config defaults + per-delegate sandbox

Follow-on to the M1–M6 sandbox campaign. Two independent workstreams. Branch
`wip/sandbox-config-delegate` off `wip/sandboxing`. **Nothing is pushed or merged
to main; nothing is merged to `wip/sandboxing` until Jesse signs off.** TDD:
every behavior gets a failing test first, then the code, red→green.

## Decisions (settled — do not relitigate)

- **Worktree delegates auto-sandbox only when the session is already sandboxed.**
  This is *today's* inherit-and-re-root behavior (a sandboxed parent's worktree
  delegate already re-roots its policy onto the lane; an `off` parent stays
  unconfined). So there is **no change to the automatic path**. The only new
  capability is an explicit per-delegate `sandbox` param (Workstream B).
- **System default is Web/TUI-only.** It lives in launch config (global
  `~/.serf/launch.toml`), which the hub renders into the spawned `serf serve`.
  The bare `serf`/`serf serve` CLI keeps its own `--sandbox` flag (default off);
  it does NOT read launch config. No new env var.
- **No-escalation floor (security invariant).** A delegate may never request a
  *looser* box than its parent (see the lattice below). Enforced regardless of
  the two decisions above.

## The mode confinement lattice (Workstream B floor)

Modes are NOT a total order (`read-only` and `restricted` are incomparable).
From `agent/sandbox/policy.go` doc comments, model each mode on two axes; lower =
more confined:

| mode            | readConfinement | writeConfinement | meaning |
|-----------------|-----------------|------------------|---------|
| off             | 2               | 1                | reads anywhere (NO denylist — sees secrets), writes working root |
| read-only       | 1               | 0                | reads anywhere−denylist, NO writes (tmp only) |
| workspace-write | 1               | 1                | reads anywhere−denylist, writes worktree |
| restricted      | 0               | 1                | reads worktree−denylist, writes worktree |

`child.AtLeastAsConfining(parent)` ⇔ `child.read ≤ parent.read AND child.write ≤ parent.write`.

Full expected matrix (child rows × parent cols → allowed?):

|                 | off | read-only | workspace-write | restricted |
|-----------------|-----|-----------|-----------------|------------|
| off             | yes | no        | no              | no         |
| read-only       | yes | yes       | yes             | no         |
| workspace-write | yes | no        | yes             | no         |
| restricted      | yes | no        | yes             | yes        |

Note the incomparable pair: `read-only` vs `restricted` is refused **both**
directions (read-only reads outside the worktree restricted forbids; restricted
writes the worktree read-only forbids). Under an `off` parent every child mode is
allowed (off is loosest on both axes). This matrix IS the core test.

Network floor: a delegate may turn network OFF (tighter) but not ON. Concretely,
the requested policy's `Network` defaults to the **parent's effective network**
when the delegate omits it (so a delegate under a net-off parent stays net-off);
an *explicit* delegate `sandbox_net=true` under a net-off parent is refused.

---

## Workstream A — launch-config sandbox default (all 4 UI surfaces at once)

The web and TUI both render the shared `launchconfig` schema; the global layer is
the system default, the launch layer is the per-spawn override. One schema entry
surfaces all four surfaces. The one load-bearing gap: `ToArgs` does not emit the
sandbox flags today, so a launch-config choice never reaches the spawned session.

Files (all under `cmd/serf-hub/internal/launchconfig/` unless noted):

1. **`types.go`** `Layer`: add `Sandbox string \`toml:"sandbox,omitempty"\`` and
   `SandboxNet *bool \`toml:"sandbox_net,omitempty"\`` (string uses ""-means-unset
   like `Model`; `*bool` tri-state like `NoProjectPrompts`).
2. **`merge.go`** `mergeLayers`: merge `Sandbox` (non-empty wins, `prov["sandbox"]`)
   and `SandboxNet` (non-nil wins, `prov["sandbox_net"]`), following the existing
   per-field pattern; set `nonEmpty = true`.
3. **`wire.go`** `FromWire`/`ToWire`: map `Sandbox` (plain string) and `SandboxNet`
   (`copyBoolPtr`).
4. **`args.go`** `ToArgs`: `if e.Sandbox != "" { add("--sandbox", e.Sandbox) }`
   and `if e.SandboxNet != nil { add("--sandbox-net", onOff(*e.SandboxNet)) }`
   where `onOff(true)="on"`, `onOff(false)="off"`. (An explicit `off` mode must be
   emitted so a launch layer can override a global default back to off.)
5. **`appwire/types.go`** `LaunchConfigLayer` (~line 1015): add `Sandbox string`
   + `SandboxNet *bool` (JSON `sandbox` / `sandboxNet`).
6. **`schema.go`**: add `LaunchGroupSandbox LaunchGroup = "Sandbox"` and two
   `LaunchOption`s in `LaunchOptionSchema()`:
   - `sandbox` → `LaunchControlSelect`, `Group: LaunchGroupSandbox`,
     `DefaultableLayers: defaultLayers`, `PerLaunch: true`, `DriverSupport: serfOnly`,
     `Choices: sandboxChoices()` = `{"", "(default: off)"}, {"off","off"},
     {"read-only","read-only"}, {"workspace-write","workspace-write"},
     {"restricted","restricted"}`. Description: what each mode confines + "reads
     outside the sandbox are denied; writes are confined to the working tree."
   - `sandbox_net` → `LaunchControlBoolean`, same group/layers/PerLaunch,
     WireField `sandboxNet`. Description: "Allow network egress when sandboxed.
     Default on."
7. **TUI** `cmd/serf-tui/internal/launchconfig/launch_schema.go` `launchOptionValue`
   (~line 101 switch): add `case "sandbox"` (display the string, like
   `context_strategy`) and `case "sandbox_net"` (display via `ptrBoolStr`, like
   `no_project_prompts`).
8. **TUI** `cmd/serf-tui/internal/launchconfig/launch_settings_panel.go` `applyEdit`
   (~line 324 switch): `case "sandbox"` sets `layer.Sandbox = strings.TrimSpace(value)`
   (like `context_strategy`); `case "sandbox_net"` sets `layer.SandboxNet =
   parseOptionalBool(value)` (like `no_project_prompts`). Without these two cases
   the TUI shows "(unsupported)" / errors "editing … not yet supported".
9. Web needs NO change — the schema-driven renderer already handles select +
   boolean.

### Workstream A tests

- `args_test.go`: `Sandbox` set → emits `--sandbox <mode>`; unset → no flag;
  `SandboxNet` true/false → `--sandbox-net on|off`; nil → no flag.
- `merge_test.go`: global `restricted` + launch `""` → effective `restricted`
  (inherit); global `restricted` + launch `off` → effective `off` (explicit
  override); provenance points to the right layer; `SandboxNet` precedence.
- `wire_test.go`: FromWire∘ToWire round-trips both fields (incl. nil vs false vs
  true for `SandboxNet`).
- A schema test asserting the two new options exist with the right kind/choices
  (mirror any existing schema coverage).

---

## Workstream B — per-delegate sandbox

The persistence + resume + enforcement machinery already exists
(`DelegateRestoreDescriptor.Sandbox`, `resolveRestoredDelegateSandbox`,
unconditional `EnableSandbox` on restore). Missing: the input surface + a
create-path branch + the floor.

1. **`agent/sandbox/policy.go`**: add `func (m Mode) AtLeastAsConfining(other Mode) bool`
   plus unexported `readConfinement()`/`writeConfinement()` per the lattice. Pure,
   fully table-tested with the matrix above.
2. **`agent/internal/tool/definitions.go`** `DefDelegate` (~line 125 properties):
   add `sandbox` (string, `enum` off/read-only/workspace-write/restricted) and
   `sandbox_net` (boolean). Descriptions (model-legible; this is the campaign's
   bar): sandbox = "Run this delegate under its own sandbox, independent of your
   session. Most useful with isolation=\"worktree\" (confines the delegate's writes
   to its lane). You may only pick a box at least as restrictive as your own — you
   cannot grant a delegate more access than you have. Omit to inherit your
   session's sandbox."; sandbox_net = "Whether the sandboxed delegate may use the
   network. Omit to inherit your session's setting. You cannot enable network for a
   delegate if your own session has it off."
3. **`agent/job_delegate.go`** `delegateArgs` (~line 80): add `Sandbox string` and
   `SandboxNet *bool` (nil = omitted).
4. **`agent/session_tools_jobs.go`** `delegateTool` (~line 337 decode block):
   decode `sandbox` via `stringArg` and `sandbox_net` via an optional-bool decode
   (nil when the key is absent — mirror how other optional bools are read; do NOT
   default a missing key to false).
5. **`agent/job_delegate.go`** `createDelegate`: **early** (next to the isolation
   validation at ~line 181, before minting IDs / creating a worktree):
   - If `args.Sandbox == ""`: unchanged inherit path (leave everything as today).
   - Else: `ParseMode(args.Sandbox)`; read the parent's effective policy from
     `s.currentEnv().(*execenv.LocalExecutionEnvironment).Sandbox` (mode = off when
     nil/not enforced); floor-check `requested.AtLeastAsConfining(parentMode)` and
     the network rule; on failure return `delegateStartFailed(fmt.Errorf(
     "invalid_request: sandbox %q grants more access than your own sandbox (%s); a
     delegate cannot be less restricted than you", …))`. Build a
     `sandbox.SandboxPolicy{Mode: requested, Network: <explicit net, else parent's
     effective net>}` and thread it into the run.
6. **`agent/subagents.go`** `prepareSubagentRun` (~lines 530–544 build `subEnv`,
   ~line 668 sets `sandboxSnapshot`): when a per-delegate policy is threaded in,
   after re-rooting `subEnv` to the lane, call `EnableSandbox` on it with the
   requested policy **resolved against the lane + host facts** (overriding the
   inherited re-rooted policy), and set `prepared.sandboxSnapshot =
   sandboxSnapshotFromInputs(&requestedPolicy)` so resume re-resolves the
   delegate's OWN box. Use the session's memoized host facts (`s.sandboxHostFacts()`)
   — do not fork a fresh probe per delegate. Choose the least-invasive threading
   consistent with surrounding code (a new param on `prepareSubagentRun`, or a ctx
   value like `ctxIsolation`); update all callers if adding a param.

### Workstream B tests

- `policy_test.go`: the full `AtLeastAsConfining` matrix (16 cells) + confirm
  `read-only`∥`restricted` refused both ways.
- Decode test: `sandbox`/`sandbox_net` parse into `delegateArgs`; absent →
  zero/nil (not false).
- Floor tests (unit, no real git needed where possible): parent `restricted` +
  delegate `off`/`workspace-write`/`read-only` → refused with the legible error;
  parent `off` + delegate any mode → allowed; parent net-off + explicit delegate
  net-on → refused; delegate net omitted under net-off parent → inherits off.
- Create-path test (isolated worktree delegate under an `off` parent with an
  explicit `sandbox=restricted`): the delegate's persisted
  `DelegateRestoreDescriptor.Sandbox` reflects the REQUESTED policy (not the
  parent's), and its env is enforced (Sandbox non-nil, mode restricted). Reuse the
  existing delegate-isolation test harness.
- Resume round-trip: a delegate created with an explicit box re-resolves that box
  on restore (the existing resume machinery should already cover this — add/extend
  a test asserting the requested mode survives, independent of the parent).

---

## Gates (both workstreams, before declaring done)

- `go build ./...`, `go vet ./...`, `go test ./...` (incl. `-race` on the delegate
  + escalation-adjacent packages), `serf-namingcheck`, golangci-lint on changed
  files. Test output must be PRISTINE.
- Commit incrementally (each red→green step). Update this plan's status as you go.
- Do NOT push, do NOT merge to `wip/sandboxing` or `main`.
