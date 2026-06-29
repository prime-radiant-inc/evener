# Phase 7 — complete coverage: fuzz every package and every API

**Status:** plan, executing. **Goal:** TRUE 100% — every package has a fuzz target
driving its REAL seam (not a copy), every API surface fuzzed *behaviorally* (real
handlers, not just codecs), the deferred 8.3 harness pieces completed, and the
gap floor flipped to BLOCKING. **No skips. No ignore-list except the fuzz toolkit's
own packages** (`cmd/serf-fuzzcov`, `fuzz/promoter`, `fuzz/schemagen`, `fuzz/typegen`,
`cmd/serf-fuzz-harvest`), each justified in writing.

Definition of done:
1. The 8.6 gap map is empty (every decode/parse package has a target driving its real seam).
2. Every API is fuzzed through its real handlers under a sandbox: appwire (all 46 methods end-to-end), hub HTTP (incl. mutating routes), tool execution (not just validate), provider Complete/request paths.
3. The deferred 8.3 pieces are built: delegate/subagent spawning + background shell jobs.
4. Focus-set coverage is ratcheted upward toward 100% per target; the gap floor is BLOCKING in `ci.yml`.

Lesson carried in (the credentials bug): a target MUST drive the package's real
exported/used seam, never a test-local copy. The 8.6 coverage map is the check —
every new target must show non-zero focus-set coverage of its SUT.

---

## Workstream A — a decode/parse target for every remaining package (all 36)

One `testing.F` per package, Phase-0 pattern: drive the package's REAL
Unmarshal/Parse/Decode seam, no-panic floor + round-trip/structured oracle where
the type re-serializes, `t.TempDir` for any FS, register in `run-fuzz.sh`, confirm
non-zero focus coverage. Grouped into parallel module lanes:

**Lane A1 — agent module (11):** `agent/transcript`, `agent/task`, `agent/doctor`,
`agent/mcpconfig`, `agent/provider`, `agent/internal/atif`, `agent/internal/sessionlog`,
`agent/internal/contextmgr`, `agent/internal/hooks`, `agent/internal/mcp`,
`agent/internal/frontmatter`.

**Lane A2 — root module, protocol/server/hub-internal (10):** `frontmatter`, `hubapi`,
`rendezvous`, `server`, `internal/appserver`, `internal/appprojector`,
`internal/apptranscript`, `cmd/serf-hub/internal/appsource`,
`cmd/serf-hub/internal/codexlaunch`, `cmd/serf-hub/internal/hubcore`.

**Lane A3 — root module, CLI/TUI glue (12) — INCLUDED, no skips:** `cmd/serf`,
`cmd/llmcall`, `cmdutil`, `cmd/serf-tui`, `cmd/serf-tui/internal/clipboard`,
`cmd/serf-tui/internal/hubstart`, `cmd/serf-tui/internal/launchconfig`,
`cmd/serf-tui/internal/msgrender`, `cmd/serf-tui/internal/toolsummary`,
`cmd/serf-tui/internal/transcript`, `cmd/serf-tui/internal/tuitheme`. For CLI
packages whose "parse" is flag/arg handling, fuzz the arg parser; for TUI render
packages, fuzz the data-decode/render-input function the gap map flagged.

**Lane A4 — llm + auth modules (3):** `llm/providers/internal/openaichat`,
`llm/providers/kimi`, `auth/openai` (OAuth token/response parsing).

Lanes run in parallel: each writes ONLY its `*_fuzz_test.go` files (disjoint),
runs a short search per target to hunt bugs, and reports the `run-fuzz.sh` TARGETS
lines + any bug — the parent aggregates `run-fuzz.sh`, runs the gate, commits.

## Workstream B — sandbox infra + behavioral API fuzzing

**B0 (prerequisite) — sandboxed hub/exec env.** Extend 8.3's `agenttest` deny-env
and add a temp-FS / no-spawn / no-network hub construction so a fuzzer can drive
mutating handlers without touching the real machine. This is the highest-value
missing infra; B1–B3 depend on it.

**B1 — appwire API end-to-end.** Replace Phase-2's stub handlers with the REAL hub
app handlers (`cmd/serf-hub/app_*.go`) over all 46 `appwire.Methods` through
`Router.Dispatch`, against the B0 sandbox. Oracles: never panic/wedge, status
monotonicity, no orphaned state.

**B2 — hub HTTP API, mutating routes.** Fuzz the routes 8.4 excluded (`/api/spawn`,
`/api/dirs/create`, the action verbs, git) under the B0 sandbox. Oracles: never
5xx/panic, never escape the sandbox FS, never spawn a real process.

**B3 — tool API execution.** Fuzz tool handler EXECUTION (not just validate) via the
deny-env, for the tools that are sandboxable; assert clean-error contract + no
real side effects.

**B4 — provider adapters, non-decode paths.** Request-building, the non-streaming
`Complete` path, and error mapping for each provider.

## Workstream C — finish the deferred 8.3 harness pieces

**C1 — per-child adapter seam:** give each delegated/subagent session its own
Responder so a child's concurrent turn doesn't race the parent's draw sequence;
then add the delegate/subagent ops to the lifecycle harness.

**C2 — background shell jobs:** model async-finalize quiescence (advance-clock +
BlockUntil handshake) so background jobs are deterministic; add the op.

**C3 — background delegates (done):** drive a background delegate to terminal
deterministically. The delegate tool defaults to background (no `max_wait_ms`), so
`createDelegate` returns immediately and a fire-and-forget finalize bridge settles
the child off the op goroutine. `opBackgroundDelegate` spawns one, then quiesces it
by JOINING the delegate job's done channel (closed only after the child finishes
AND the bridge finalizes — the bridge enqueues the owner notification before
closing done) and DRAINING the notification rail (`drainJobNotificationTurns` runs
an `EntryNotification` turn so the owner notification the nil-`notifyFunc` root
would otherwise never consume is surfaced exactly once). Oracle 6 then asserts no
running job + every job terminal. The quiet-watchdog ticker runs on the injected
clock (`jm.clock.NewTicker`), so it never fires on wall time and is stopped at
finalize; its firing is exercised deterministically by
`TestLifecycleBackgroundDelegateWatchdogFires` (gated child + `BlockUntil`
handshake + advance past the quiet window). No production seam was needed — the
8.3 clock seam already threads `jm.clock` through the watchdog, finalize backoff,
and delegate timers.

## Workstream D — drive to 100 and lock it

**D1** — per target, use `serf-fuzzcov`'s uncovered-line output to add seeds/oracles
until focus-set ≈100% (or document genuinely-unreachable lines with a reason).
**D2** — flip the gap floor to BLOCKING in `ci.yml` once Workstream A lands; ratchet
focus-set floors upward (`BLESS=1`). **D3** — the only ignore-list entries are the
fuzz toolkit's own packages, each with a reason.

## Workstream E — supporting follow-ups (from EXECUTION-FOLLOWUPS.md)

Unified `native|rapid` target registry consumed by run-fuzz.sh/fuzz-coverage/
fuzz-triage; regenerate the jobs corpus with the fixed scrubber; document/install
gitleaks; corpus minimization for promotion.

## Execution waves
- **Wave 1:** Workstream A — 4 parallel lanes (A1–A4). Parent aggregates run-fuzz.sh + gate + commit.
- **Wave 2:** B0 sandbox (single focused build — it's prerequisite infra).
- **Wave 3:** B1–B4 + C — parallel where files are disjoint, after B0.
- **Wave 4:** D (drive-to-100 + flip gate) + E (follow-ups).

Every wave: parent runs the full gate (`make fuzz`/`test`/`lint` + `-race` on new
rapid targets) and the coverage map (confirm each new target shows non-zero focus
coverage of its real seam) before moving on.
