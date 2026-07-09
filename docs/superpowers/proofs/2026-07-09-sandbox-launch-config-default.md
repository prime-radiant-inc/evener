# sandbox-launch-config: a launch-config sandbox default reaches a hub-spawned session

**What this covers**: Workstream A — the global/per-launch sandbox default set in
launch config (web Settings or TUI launch panel) is merged into the effective
launch layer and rendered into the spawned `serf serve`'s argv, so a hub-spawned
session enforces the configured box. Exercises the `Sandbox`/`SandboxNet` fields
threaded through `cmd/serf-hub/internal/launchconfig/` (`types.go`, `merge.go`,
`wire.go`, `args.go`, `schema.go`) plus the TUI schema/apply switches. The one
load-bearing gap this closes: before Workstream A, `ToArgs` did NOT emit the
sandbox flags, so a launch-config choice never reached the spawned session.

**Status: card + Go backstop (live hub not stood up).** Standing up the full hub
(server + web/TUI + a spawned `serf serve`) live is heavy relative to what it
proves here — the model-facing surface is a *settings form*, not an agent action,
and the enforcement itself is already live-validated by
`2026-07-09-sandbox-flag-live-e2e.md` (a real `serf --sandbox restricted` session).
The remaining risk is purely the *plumbing* — that a form choice becomes the right
argv — which the focused `TestToArgs_Sandbox` backstop covers deterministically.
The card below documents the real UI path so a future live pass has an exact
script.

## The real UI path (what a user does)
1. Open the hub's **Settings** (web) or the **launch settings panel** (TUI). Both
   render the shared `launchconfig` schema; the schema entry surfaces on all four
   surfaces at once (`schema.go` `LaunchOptionSchema()`).
2. Under the **Sandbox** group (`LaunchGroupSandbox`), set **Sandbox** to
   `restricted` on the **global** layer (the system default) — a
   `LaunchControlSelect` with choices `(inherit)` (empty value), `off`,
   `read-only`, `workspace-write`, `restricted`. The empty value inherits the lower
   layer (only the global layer treats absent as off); the explicit `off` entry is
   how a project/launch layer clears a global default. Optionally set **Sandbox
   network egress** (`sandbox_net`, a `LaunchControlBoolean`, default on).
   - Global layer file: `<stateRoot>/launch.toml` (e.g. `~/.serf/launch.toml`),
     `[global] sandbox = "restricted"`. The sandbox options are `DefaultableLayers
     = {global, project}` and `PerLaunch: true`, so a per-launch pick can override
     the global default (including back to `off`).
3. Start a new session from the hub. The hub resolves the layer stack
   (`resolver.go`), merges to an effective layer (`merge.go`), and renders it into
   the spawned `serf serve` argv via `ToArgs` (`args.go`).
4. The spawned `serf serve` parses `--sandbox restricted` (`cmd/serf/main.go:203`,
   `flags.sandbox`) and provisions the enforced env.

## Expected + Falsification (live, when a hub is stood up)
- **Enforcement line** in the spawned session's startup output:
  `sandbox: bwrap enforcing restricted (network on, secrets masked, cache private)`
  (Linux/bwrap host) — the same line proven live in
  `2026-07-09-sandbox-flag-live-e2e.md`.
- **A blocked op**: an out-of-worktree `read_file /etc/hostname` returns
  `sandbox: read_file denied (hostname): outside the sandbox's readable roots; this sandbox policy is fixed for the session [sandbox mode: restricted]`.
- **Falsify**: if the spawned session prints NO enforcement line (ran unsandboxed)
  or the out-of-worktree read SUCCEEDS, the launch-config default did not reach the
  session — FAIL. If a global `restricted` + per-launch `off` still enforces
  restricted, the per-launch override is broken — FAIL.

## Backstop (Go, run 2026-07-09 — PASS)
The plumbing gap this workstream closes is asserted by
`cmd/serf-hub/internal/launchconfig/args_test.go` `TestToArgs_Sandbox`:

| effective layer | emitted argv |
|---|---|
| `{Sandbox:"restricted"}` | `--sandbox restricted` |
| `{Sandbox:"off"}` (explicit) | `--sandbox off` (so a launch layer can override a global default back to off) |
| `{SandboxNet:true}` no mode | *(nothing — flag is a no-op without a mode)* |
| `{Sandbox:"workspace-write", SandboxNet:false}` | `--sandbox workspace-write --sandbox-net off` |
| `{Sandbox:"restricted", SandboxNet:true}` | `--sandbox restricted --sandbox-net on` |

Companion backstops (all PASS):
- `merge_test.go` `TestMerge_Sandbox` — global `restricted` + launch `""` inherits
  restricted; global `restricted` + launch `off` resolves to off (explicit
  override); provenance points to the contributing layer.
- `wire_test.go` `TestWire_SandboxRoundTrips` — `FromWire∘ToWire` round-trips
  `Sandbox` and the `SandboxNet` tri-state (nil / false / true).
- `schema_test.go` `TestLaunchOptionSchema_Sandbox` — the two options exist with
  `WireField` `sandbox`/`sandboxNet`, `Group` `Sandbox`, `Kind`
  `select`/`boolean`, `PerLaunch:true`, and the expected choice set.

```
$ go test ./cmd/serf-hub/internal/launchconfig/ -run 'TestToArgs_Sandbox|TestMerge_Sandbox|TestWire_SandboxRoundTrips|TestLaunchOptionSchema_Sandbox'
ok  primeradiant.com/serf/cmd/serf-hub/internal/launchconfig
```

## Sharp edges
- The bare `serf` / `serf serve` CLI keeps its own `--sandbox` flag (default off)
  and does NOT read launch config — launch config only governs HUB-spawned
  sessions. Testing the CLI flag proves the flag, not this workstream.
- `sandbox_net` without a non-off mode is deliberately suppressed by `ToArgs`
  (serf ignores the flag without a sandbox), and `merge.go` emits a diagnostic for
  the same combination — see `2026-07-09-sandbox-delegate-edge-cases.md`.
- A live hub pass must assert on the SPAWNED session's output (the enforcement
  line / a blocked op), not on the hub's own process — the hub itself is not
  sandboxed.
