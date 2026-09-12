# Doctor tooling study: what's wrong, and a `doctor_evener` first-party tool proposal

Date: 2026-08-29
Branch: `doctor-study` (worktree; no code changes — this is a study and proposal)
Evidence: measured on this machine's real state root (13 project buckets,
~2,400 sessions, 293 GB of `sessions/` data; the evener bucket alone holds
1,927 transcripts / 215 GB) and on a real doctor-agent run
(`034DQSByTLQZnq5JktrjSn`, a `doctor` delegate spawned 2026-08-25).

---

## Part 1 — What is actually wrong with the doctoring-evener skill

### Finding 1 (blocker): the skill's core instruction fails on the first try

`SKILL.md` line 49: *"Run them via the shell tool"* — i.e. invoke
`evener doctor <cmd>` through the session's shell.

On this machine (the machine evener is developed on), that instruction
**fails immediately**:

- `evener` is **not on PATH** for login shells (`bash -lc 'command -v evener'`
  → not found). `make install` is not used; the daemon runs from a repo-local
  build (`/Users/jesse/git/prime-radiant/evener/evener serve ...`).
- The real doctor-agent run proves it. `034DQSByTLQZnq5JktrjSn` (a `doctor`
  delegate) issued `evener doctor locate 034DNvJI7eTobkfNyyi4Dq` as its very
  first command and got:

  ```
  /bin/bash: evener: command not found
  [exit 127]
  ```

  It then recovered by discovering `./evener` in the working directory
  (`file ./evener && ls -l ./evener && ./evener doctor locate ...`) and used
  `./evener doctor ...` for the rest of the run.

That recovery is pure luck, not design:

- It only works when the session's cwd happens to be the repo that built the
  daemon. A doctor delegate spawned with a different cwd (any user project,
  a worktree, `/tmp`) has no `./evener`.
- The skill never mentions `./evener`, `$EVENER_STATE_DIR`, or any fallback.
  Every doctor session starts with one wasted failing call plus a
  discovery detour.
- Worse: a doctor agent running in a *sandboxed* delegate (read-only, for
  example) may not even be able to run the shell at all in some deployments,
  while the data plane itself (`agent/doctor`) is pure read-only Go.

**Root cause:** the skill treats a **CLI-process invocation** as the only
interface to the doctor data plane. There is no first-party tool, so the
skill has no way to say "call the doctor" that doesn't depend on PATH, cwd,
and process spawning.

### Finding 2 (perf, high): `sessions`/`audit --since` re-fold the same delegates journal once per child session

`ListSessions` (`agent/doctor/sessions.go`) builds a row per session, and
`sessionRow` calls `stableDoctorDelegates(paths)` **per session**
(`sessions.go:144`). `stableDoctorDelegates` resolves the session's
`JobTreeRootSessionID` and then **reads and folds that root's
`delegates.jsonl` from scratch** — with no cache.

Sessions cluster: in a 50-session sample from the real evener bucket, the
`job_tree_root_session_id` distribution was:

| root | children |
|---|---|
| `0349htymuWmnKr5Smg4wcu` (1.7 MB delegates.jsonl) | 18 |
| `0349dAO2cXSWZfraFM8YIU` (386 KB) | 18 |
| `0349d3S3xFOClTGpsq4zyO` (253 KB) | 10 |
| (self / none) | 4 |

Measured on that fixture (cold-ish page cache):

- one fold of the 1.7 MB root: **~559 ms**
- `ListSessions` over the 50 sessions: **10.66 s**
  (same 50 with header-only transcripts: 0.028 s; jobs folds: ~0)
- 50 back-to-back `stableDoctorDelegates` calls (the exact per-session
  pattern): **5.52 s** — with a warm cache.

Extrapolated to the full evener bucket (1,927 sessions; roots with
multi-MB delegate journals and 20 GB API logs on disk), a single
`evener doctor sessions --since 48h` runs **5–30+ minutes** and pins a CPU
core. I observed >4 minutes with zero output before killing it; the
projected all-buckets sweep is ~42 hours of mostly redundant re-folding.

The same refold pattern sits under `audit --since` (which calls
`ListSessions` first) and `tree` (which re-reads per node).

**This is the "something wrong" symptom you can feel:** the skill's batch
entry points (`sessions`, `audit --since`) are effectively unusable on a
real state root, so every fleet study times out or gets killed, and the
agent falls back to per-session commands — or to hand-parsing.

### Finding 3 (drift, medium): SKILL.md's command table is missing two subcommands

The CLI (`evener doctor help`) exposes `plugins` and `turnids`. Neither
appears in `SKILL.md`'s command table, the persona
(`internal/bundled/agents/doctor.md`), or any `references/` file. The skill's
own HARD GATE says the references are the map — but two real surfaces are
unmapped. A doctor agent asked about plugin-store drift or reserved turn-id
collisions has no pointer and will improvise.

### Finding 4 (contract, medium): the doctor persona carries write tools while its own rules say propose-only

`doctor.md` gives the doctor agent `tools: [shell, read_file, glob, grep,
write_file, apply_patch, task_list]`, while the skill's repair-guardrails
tier for "core skill / doctor-tool repair" is **propose-only, never silently
applied**. The enforcement is purely prompt-level. A first-party read-only
doctor tool (see Part 2) makes the *data plane* structurally read-only; the
*repair* tier still needs write tools, but the diagnosis loop stops needing
`shell` at all.

### Finding 5 (minor): duplicated, slightly divergent doc surfaces

`SKILL.md` carries a full command table; `cmd/evener-doctor/main.go` has a
usage comment block; `cmd/evener-doctor/README.md` exists too. The
`--recompute`/`--validate`/`--health` caveats are explained in SKILL.md but
the `plugins`/`turnids` rows exist only in the binary. Any surface change
requires three synchronized edits — the skill's table is already behind.

---

## Part 2 — Proposal: expose `doctor_evener` as a first-party tool

### Goals

1. The doctor loop runs **in-process**, in the daemon that owns the state —
   no PATH dependency, no cwd dependency, no process spawn per inspection.
2. The data plane is **structurally read-only** — same property the CLI has
   today (`stable_delegate_readonly_test.go` pins it), but enforced by the
   tool registry (`ReadOnly: true`), not by prompt discipline.
3. Shell-timeout policy, output truncation, and the transcript-tool output
   limits apply uniformly; no 30-minute `sessions` sweep can wedge a shell
   job.
4. The skill's instruction collapses from "run the shell tool and hope
   `evener` is on PATH" to "call `doctor_evener`" — one surface, one schema,
   testable with the existing agenttest harness.

### Design

**One tool, one argument surface.** A single `doctor_evener` tool whose
parameters mirror the CLI's subcommand+flags as typed JSON:

```
doctor_evener(
  command: enum("locate","transcript","apilog","jobs","mutations",
                "watches","tree","turnids","sessions","audit","plugins"),
  selector?: string,          // local:|proj:<hash>:|bare id; rejected for
                              // selector-less commands, same dialect as CLI
  state_dir?: string,        // default: this session's state root (the
                              // daemon's own), not ~/.local/state guessing
  json?: boolean = true,      // default true: structured result envelope
  ...command-specific options as typed fields:
  transcript: { count?, health?, format?, range?, text_max?, full_text? }
  apilog:     { empty?, errors?, cache_spikes?, threshold?, summary?,
                validate?, recompute?, health? }
  jobs:       { job_id? }
  watches:    { watch_id?, self_loops? }
  tree:       { depth?, observers? }
  sessions:   { since?, bucket? }
  audit:      { runbook, sessions? | since? }
)
```

Implementation notes:

- The handler is a thin adapter over `agent/doctor`'s exported functions —
  exactly what `cmd/evener-doctor/main.go`'s `run()` already dispatches to.
  `doctor.Run(args, ...)` itself is not reused verbatim (it parses CLI flags);
  instead the tool maps typed args to the same `doctor.Locate`,
  `doctor.Transcript`, `doctor.Jobs`, ... calls. One data plane, two fronts.
- **`state_dir` defaults to the session's own state root** (`toolDeps.stateDir`),
  falling back to `doctor.ResolveStateBase("")` only when empty. This kills
  the PATH/env/cwd failure class entirely: a doctor delegate inherits the
  daemon's state root by construction.
- Registered `ReadOnly: true`, with `schema.ToolOutputLimit` like the
  transcript tools (the `sessions` table and `audit` findings are the big
  ones; cap and page).
- Result envelope: the CLI's `--json` struct shapes, unchanged. The Finding
  wire contract (`finding-contract.md`) stays byte-identical.

**Who gets it.** Register in `registerCoreTools` for the `doctor` bundled
agent role (add to `doctor.md`'s tool list, replacing `shell` for the
diagnosis loop) and make it available to the root session (it's read-only;
the skill-catalog approach can gate discoverability). Keep the CLI exactly as
is — humans and scripts keep `evener doctor`.

**Sizing.** `definitions.go` +~120 lines, one new
`agent/session_tools_doctor.go` (~250 lines: arg mapping, per-command option
structs, result envelopes), registration + tests. No changes to
`agent/doctor`'s public API.

### Rollout sequence (small, independently shippable)

1. **Fix the refold (Finding 2) first — it's a bug, not a feature.**
   Add a per-invocation cache in `ListSessions`/`RunAudit`/`Tree` keyed by
   resolved root-session id: `map[string]delegatestore.State` (plus the
   diagnostics), populated lazily by `stableDoctorDelegates`. Reads stay
   pure; the cache lives for one `ListSessions` call, not across calls.
   Expected effect on the 50-session fixture: 10.66 s → well under 1 s
   (18 folds of the big root collapse to 1). Add a benchmark test that
   sweeps a synthetic multi-root state and fails on regression.
2. **Add the `doctor_evener` tool** per the design above, with tests:
   arg mapping round-trip per subcommand, `ReadOnly` enforcement,
   default-`state_dir` inheritance, output limits on the `sessions` table.
3. **Update the skill once, at the new seam.** `SKILL.md` line 49 changes
   from "Run them via the shell tool" to "Call `doctor_evener`". Add the
   `plugins` and `turnids` rows to the table (Finding 3). Drop `shell` from
   the doctor persona's tool list if nothing else in its loop needs it
   (keep `write_file`/`apply_patch` for the propose-only repair tier — that
   tier's authority is review-gated, not tool-gated).
4. **Keep `evener doctor` CLI as-is** for humans, scripts, and the tool-fluency
   probes (`docs/skills/tool-fluency/SKILL.md` references it).

### What I am deliberately not proposing

- **Not** removing the CLI or rewriting runbooks — the runbook `audit:`
  blocks, Finding contract, and repair guardrails all stay.
- **Not** a hub/RPC surface yet. The hub frontend has no doctor surface
  today; that's a separate product decision (and the same read-only adapter
  could serve it later).
- **Not** streaming/paging for `sessions` output in v1 — the refold fix
  (step 1) removes the pathological latency; a page/cursor can be added to
  the tool later if real audits still overflow the output limit.

### Risks and mitigations

| Risk | Mitigation |
|---|---|
| Tool schema grows wide (many optional fields) | Group per-command options into one `options` object with `additionalProperties:false`; validate per-command in the handler, mirroring the CLI's own mutual-exclusion errors (`--sessions` xor `--since`) |
| Big `sessions` result overflows context | Same `ToolOutputLimit` + render cap as transcript tools; the CLI table renderer is reused for the human shape |
| Two fronts drift (CLI flags vs tool fields) | One table in `SKILL.md` + a round-trip test asserting every CLI flag has a tool field (the existing `usage_flags_test.go` pattern extends to this) |
| Read-only guarantee regressions | `stable_delegate_readonly_test.go` pattern extended: tool handler over a fixture asserts zero mutation |

---

## Verification already done in this study

- Reproduced the `sessions` hang and attributed it (delegate-journal refold,
  measured 5.52 s warm / 10.66 s cold on 50 sessions; 559 ms per fold of the
  1.7 MB root; 18 redundant folds).
- Confirmed the skill's shell instruction fails on this machine and found a
  real doctor-agent transcript with the `command not found` + `./evener`
  recovery (`034DQSByTLQZnq5JktrjSn`, first exec_command, exit 127).
- Audited the skill/CLI/reference surfaces for drift (`plugins`, `turnids`
  missing from SKILL.md and all references).
- Confirmed `evener doctor` is already an in-process subcommand of the main
  binary (`cmd/evener/main.go` `dispatchCLICommandWith`) — the CLI is not a
  shell-out today; the *skill's instruction* is the shell-out.
- No code was changed on this branch; scratch harnesses and fixtures were
  removed (`git status` clean, `go build ./...` green).
