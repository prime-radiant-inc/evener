# Agent handoff notes — serf UX overhaul (written 2026-07-05, for sessions on any host)

Context: WS1 (attention), WS2 (working state/metrics), WS3 (sidebar rebuild),
WS5 (MCP resilience) are merged and pushed. Remaining: WS4
(`specs/2026-07-05-ws4-quick-wins-inventory.md`) and WS6
(`specs/2026-07-04-ws6-consistency-sweep-inventory.md`). Specs/plans for the
shipped workstreams live beside them and are the house style to imitate.

## Process that worked (Jesse-endorsed)

- Brainstorm → spec → adversarial review rounds (two competing reviewers, every
  decisive claim verified against code before folding; review log in the spec)
  → plan (bite-sized TDD tasks) → subagent-driven development in a worktree.
- Plan audit before build: check every spec section has a task (fold-coverage
  vs the review log is NOT enough), and every task title against its steps
  (two plans promised things their steps lacked).
- SDD: fresh implementer + fresh reviewer per task, synchronous dispatch;
  append-only ledger file per build; Critical/Important findings get a fix
  subagent + re-review. Verify any "pre-existing failure" claim at the base
  commit. Model tiering: sonnet workers/orchestrators, opus only where
  judgment-dense, haiku for trivial tasks.
- E2e scenario cards (test/scenarios/ has the format): run fully live with an
  isolated fake $HOME (real ~/.serf untouched), dedicated Chrome profile;
  evidence over assertion. Cards found real bugs every workstream.
- Merge: `--no-ff` merge commits (repo sets merge.ff=only; explicit flag
  overrides), full gates on merged main before push. After any conflict
  resolution: re-grep the whole repo for conflict markers and `go vet` the
  package — `go build` does not compile test files.

## Repo operational facts

- Modules: `GO_MODULES` in Makefile (root, agent, llm, auth, envvars, fuzz,
  invariant); run tests per module (`cd agent && go test ./...`).
- `make lint` = golangci across modules + serf-namingcheck + internalcheck +
  docscheck + codegen check. namingcheck runs ONLY here — per-task golangci
  runs miss naming violations. JSON/TOML keys are snake_case; deliberate
  camelCase (e.g. Claude Code plugin schema interop in internal/plugins/)
  takes a `// serf:naming-ignore: <reason>` line above the field.
- jstest (cmd/serf-hub/jstest) is agent-run, not in CI:
  `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` (one-time
  jsdom setup per its README).
- agent/snapshot_golden_test.go pins meta.json byte-exactly; adding
  SessionMeta fields means regenerating goldenMeta()/goldenMetaJSON (omitzero
  keeps legacy round-trip).
- Live runs: `--model openai/gpt-5.5` works standalone via the ChatGPT OAuth
  token (partial provider availability is tolerated; no .env needed unless
  targeting oai-work). Cheap e2e model: openai/gpt-5.4-mini. Never print .env
  contents.
- appwire: new METHODS need catalog + BOTH routers (server + serf-hub) in ONE
  commit (bidirectional cross-check tests); new struct fields don't.
  docs/appwire-protocol.md is generated (`make generate`).
- Known flake: TestReconnect_FailedReconnect_BackoffSuppressesImmediateRetry
  is load-sensitive under full-suite parallelism (WS6 item). The env-var audit
  test skips gitignored inspo/.

## Multi-agent lessons (hard-won)

- Demand verbatim quotes + transcript evidence for any subagent incident/
  security claim before propagating it; adversarially-primed reviewers can
  confabulate detailed "attacks" (one did — twice, via brief contamination).
  Never re-describe a suspected attack pattern in downstream dispatch briefs.
- All agent commits carry Jesse's git author; provenance comes from dispatch
  records, not `git log`.
- Orchestrators must verify child work from worktree state, not awaited
  replies (nested SendMessage replies don't route back).
