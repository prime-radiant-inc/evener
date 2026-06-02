# Agent decomposition — Chunk 1 implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.
> Steps use checkbox (`- [ ]`) syntax. Execute tasks IN ORDER — each builds on the
> committed state of the previous. Do NOT parallelize (the carves touch the same
> package).

**Goal:** carve the two genuinely-clean foundation packages out of the flat
`package agent` — `agent/execenv` and `agent/schema` (`Turn`+`TurnKind` only) —
proving the cross-module-breaking, compiler-driven carve pattern at the lowest risk.
Both were independently verified clean by two adversarial reviewers.

**Architecture:** layered packages, deps point down (see
`docs/superpowers/specs/2026-06-01-agent-decomposition-design.md`). These two are
Layer-0 foundation: they import only `llm` + stdlib (+`doublestar` for execenv) and
nothing from `package agent`.

**Tech stack:** Go 1.25, go.work multi-module (`.` `agent` `llm` `auth`). Gates:
`go build ./...`, `make vet`, `make test-race`, `make lint-golangci` (all loop the 4
modules). `goimports` available; **never** `gofmt -r` on type names.

**Hard constraints (every task):**
- Compiler-driven moves: move decls → `go build ./...` enumerates every broken ref →
  qualify each → `goimports -w` fixes imports. The build is the completeness net.
- Behavior-preserving: zero logic change. Prove with the full `-race` suite.
- Commit per package with trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- **NEVER push.** **NEVER `git add -A`** (stage named files after `git status`).
- Stay in the main checkout (sequential carve = one commit line). No worktrees.
- Gate MUST be green before committing: build + vet + test-race + lint-golangci, all
  4 modules. If a gate fails, fix it; do not commit red.

---

## File structure

- Create `agent/execenv/execenv.go` (from `agent/env.go`), `agent/execenv/local.go`
  (from `agent/env_local.go`), package `execenv`. Move `env_local_test.go` →
  `agent/execenv/local_test.go`.
- Create `agent/schema/turn.go` (from `agent/turns.go`), package `schema`.
- `package agent` and the root-module consumers gain imports of
  `primeradiant.com/serf/agent/execenv` and `primeradiant.com/serf/agent/schema`
  and reference `execenv.X` / `schema.Turn`.

---

## Task 1: Carve `agent/execenv`

**Files:**
- Create: `agent/execenv/execenv.go`, `agent/execenv/local.go`, `agent/execenv/local_test.go`
- Delete: `agent/env.go`, `agent/env_local.go` (contents moved)
- Modify: every file referencing `ExecutionEnvironment`, `LocalExecutionEnvironment`,
  `NewLocalExecutionEnvironment`, `ExecResult`, `DirEntry`, `EnvVarPolicy`,
  `EnvPolicy*` (compiler enumerates; spans `package agent` + root-module
  `cmd/serf/run.go`, `cmd/serf/serve.go`, and others).

**The full type closure to move** (verified — env.go imports only `context`;
env_local.go imports only stdlib + `github.com/bmatcuk/doublestar`): the
`ExecutionEnvironment` interface, `ExecResult`, `DirEntry`, `EnvVarPolicy` + its
`EnvPolicy*` consts (env.go); `LocalExecutionEnvironment`, `NewLocalExecutionEnvironment`
+ impl (env_local.go). `EnvironmentInfo` does **NOT** move (it lives in profile.go,
embeds `WorkspaceInfo` — out of scope).

- [ ] **Step 1: Create the package.** `mkdir agent/execenv`. Move `agent/env.go` →
  `agent/execenv/execenv.go` and `agent/env_local.go` → `agent/execenv/local.go`;
  change `package agent` → `package execenv` in both. Move `agent/env_local_test.go`
  → `agent/execenv/local_test.go` (change package to `execenv` if white-box, else
  `execenv_test`). Remove now-empty `agent/env.go`/`agent/env_local.go`.
- [ ] **Step 2: Enumerate breakage.** `cd agent && go build ./...` — every `undefined:
  ExecutionEnvironment` (etc.) is a site to fix. Then `go build ./...` from repo root
  for the root-module consumers.
- [ ] **Step 3: Qualify references.** For each broken site, `ExecutionEnvironment` →
  `execenv.ExecutionEnvironment`, `NewLocalExecutionEnvironment` →
  `execenv.NewLocalExecutionEnvironment`, etc. Run `goimports -w` on touched files to
  add `primeradiant.com/serf/agent/execenv` imports and drop unused ones. Repeat
  build until green. Do NOT use `gofmt -r`.
- [ ] **Step 4: Resolve test placement.** Tests that exercise ONLY the env types move
  to `agent/execenv/`. White-box `package agent` tests that USE env types stay but
  qualify `execenv.X`. Build the test binaries: `go vet ./...` per module.
- [ ] **Step 5: Gate.** From repo root: `go build ./...`; `make vet`; `make test-race`;
  `make lint-golangci`. ALL must be green (rc 0). Investigate any failure; fix; re-run.
- [ ] **Step 6: Verify the move is real.** `go doc primeradiant.com/serf/agent | grep -E
  'ExecutionEnvironment|LocalExecutionEnvironment|ExecResult'` returns NOTHING (they
  left `agent`); `go doc primeradiant.com/serf/agent/execenv` lists them. Grep confirms
  no `agent.ExecutionEnvironment` refs remain anywhere.
- [ ] **Step 7: Commit.** `git status`; `git add` the specific moved/modified files;
  commit: `refactor(agent): carve agent/execenv foundation package` with a body
  describing the move + the trailer.

---

## Task 2: Carve `agent/schema` (`Turn` + `TurnKind`)

**Files:**
- Create: `agent/schema/turn.go` (from `agent/turns.go`)
- Delete: `agent/turns.go` (contents moved)
- Modify: every file referencing `Turn`, `TurnKind`, the `Turn*` kind consts, or
  `NewTurn` (compiler enumerates; ~53 in-module + ~9 cross-module consumer files —
  `cmd/serf-hub`, `cmd/serf-tui`, `internal/apptranscript`, `server`).

**Move** (verified — turns.go imports only `time` + `llm`): `Turn`, `TurnKind`, the
`Turn*` kind constants, `NewTurn`, and any other decl in `turns.go`. Nothing else
moves in this task (`TranscriptEntry` embeds `Turn` but stays — it carves with
transcript later; it will reference `schema.Turn`).

- [ ] **Step 1: Create the package.** `mkdir -p agent/schema`. Move `agent/turns.go` →
  `agent/schema/turn.go`; change `package agent` → `package schema`. Keep its
  `import ("time"; "primeradiant.com/serf/llm")`.
- [ ] **Step 2: Enumerate breakage.** `cd agent && go build ./...` then root
  `go build ./...`. Every `undefined: Turn`/`TurnKind`/`NewTurn`/`TurnUserInput`… is a
  site.
- [ ] **Step 3: Qualify references — compiler-driven.** `Turn` → `schema.Turn`,
  `TurnKind` → `schema.TurnKind`, `NewTurn` → `schema.NewTurn`, each kind const →
  `schema.TurnX`. **Watch the field-name collision** (`events.ForkSummaryData{Turn:
  ...}` — a FIELD named Turn, NOT the type — must stay `.Turn`); the compiler distinguishes
  them (a field access won't be `undefined`), which is exactly why we qualify only the
  build-flagged sites and never `gofmt -r`. `goimports -w` per touched file. Repeat
  until both builds green.
- [ ] **Step 4: Test placement.** `Turn` is used pervasively in `package agent` tests
  (white-box) — they stay in `package agent` and qualify `schema.Turn`. Cross-module
  consumer tests (`server`, `cmd/...`) qualify `schema.Turn`. Build all test binaries.
- [ ] **Step 5: Gate.** Root: `go build ./...`; `make vet`; `make test-race`;
  `make lint-golangci`. ALL green.
- [ ] **Step 6: Verify.** `go doc primeradiant.com/serf/agent | grep -E '^(type Turn|func
  NewTurn)'` returns NOTHING; `go doc primeradiant.com/serf/agent/schema` lists `Turn`,
  `TurnKind`, `NewTurn`. No `agent.Turn`/`agent.NewTurn` refs remain (grep).
- [ ] **Step 7: Commit.** `git status`; `git add` named files; commit:
  `refactor(agent): carve agent/schema with the Turn history atom` + body + trailer.

---

## Task 3: Reflect on Chunk 1 (orchestrator)

- [ ] Write a short reflection capturing: how clean the compiler-driven carve actually
  was; the real cross-module blast radius (file counts) vs. the spec's estimate; any
  surprises (test placement, goimports behavior, lint, CI-list edits needed); how long
  each carve took; whether the gates caught anything. This calibrates the estimates and
  procedure for every later carve.

## Task 4: Write the plan for the rest (orchestrator)

- [ ] Using the chunk-1 reflection + the corrected topological order from the spec
  (`skill · task · transcript → tool → mcp · provider · plugin → context`; persistence
  + subagent late), write the next implementation plan. Decide the next chunk (likely
  `agent/schema` expansion to the remaining pure types, or the `skill`/`task` leaves).
  Re-validate the next chunk's cleanliness before planning its tasks.

---

## Self-review notes
- Spec coverage: covers chunk 1 (execenv + schema/Turn) from the design §6; later
  chunks deferred to Task 4 by design.
- No placeholders: each step has the exact command/transformation.
- Type consistency: `execenv.*` and `schema.Turn`/`schema.TurnKind`/`schema.NewTurn`
  used consistently.
- Risk: the only non-mechanical judgment is the `Turn` field-vs-type collision (Step
  2.3) — handled by compiler-driven qualification (never `gofmt -r`).
