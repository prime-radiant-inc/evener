# Evener → Evener Rename: Execution Plan

## Decisions (confirmed)
- Module: `primeradiant.com/evener` → `primeradiant.com/evener` (7 sub-modules: agent, auth, envvars, fuzz, identifier, invariant, llm)
- Binaries: all 14 mirrored to `evener-*`
- User data: all paths → evener (`~/.evener`, `~/.config/evener`, `~/.local/state/evener`, `<gitroot>/.evener`, `<prefix>/share/evener/bin`, `$XDG_CACHE_HOME/evener`)
- Env vars: all `EVENER_*` → `EVENER_*`
- AppWire protocol: version bump + wire JSON keys renamed (hard flag day, no wire compat)
- Generated files: regenerated via `make generate`
- Prose: all docs, comments, strings rewritten
- Migration tool: `cmd/evener-migrate`
- Commits: ~20-30 small thematic commits
- DO NOT rename: `ProtocolVersion`, continuation secret domain strings, `serffuzz` tag, `serf-apptranscript-prefix`, `SERF_STATE_HOME` (doesn't exist)

## Catalog Summary
| Domain | Files | Occurrences |
|---|---|---|
| Go module path | 1653 go files | 4793 |
| Go symbols (Evener/evener) | ~785 files | 219 cap + 2492 lower |
| EVENER_ env vars (Go) | ~100 files | 551 |
| EVENER_ env vars (non-Go) | ~60 files | 684 |
| User data paths (Go) | ~20 key files | ~50 |
| Markdown | 800 files | 17,422 |
| Frontend (ts/tsx/html/css) | 277 files | 2,160 |
| Go comments | 785 files | 1,756 |
| Shell scripts | 55 files | — |
| Config (yaml/toml/json/sbpl/tmpl) | ~30 files | — |
| GitHub workflows | 3 files | — |

## Commit Plan (25 commits)

### Phase 1: Go Module & Binary Foundation (sequential, critical path)

**Commit 1: Module path rename**
- sed `primeradiant.com/evener` → `primeradiant.com/evener` in: go.mod, go.work, all 7 sub-module go.mod files, all .go files, testing-budget.json, scripts/fuzzcov-ignore.txt, scripts/run-module-tests.sh, scripts/run-module-tests-selftest.sh, Makefile LDFLAGS
- Verify: `go build ./...`

**Commit 2: cmd/ directory renames**
- `git mv cmd/evener cmd/evener`, `git mv cmd/evener-hub cmd/evener-hub`, ... (all 14)
- sed remaining `cmd/evener` → `cmd/evener` in import paths
- Verify: `go build ./...`

**Commit 3: Binary names in Makefile**
- EVENER_INSTALL_BINS, build targets, dist targets, install targets, clean, temp dir names
- Verify: `make build`

**Commit 4: Self-update & install paths**
- installBinaries list, `share/evener/bin` → `share/evener/bin`, error messages, binresolve comments
- Verify: `go build ./...`

### Phase 2: Go Code (sequential, depends on Phase 1)

**Commit 5: Env var registry definitions**
- envvars.go: rename 23 registered EVENER_* Var identifiers + Name strings → EVENER_*
- Verify: `go build ./...`

**Commit 6: Env var usage in Go**
- All EVENER_ references in Go → EVENER_ (2 non-registry consts: EVENER_LIVE_TESTS, EVENER_FUZZ_PERSIST + test/build vars)
- Verify: `go test ./...`

**Commit 7: Go package clause**
- `evener_test` → `evener_test` (root external test package)
- Verify: `go build ./...`

**Commit 8: Go type/func/var/const identifiers**
- 21 Evener* types, ~25 funcs, 3 vars, 2 consts → Evener*
- Verify: `go build ./... && go test ./...`

**Commit 9: User data paths in Go**
- cmdutil/userdirs.go, cmdutil/statedir.go, appwire/frame_recorder.go, internal/credentials/store.go, agent/transcript_lookup.go, agent/mcpconfig/config.go, agent/runtime_dir.go, agent/sandbox_infra.go, cmd/evener-hub/config.go, etc.
- `~/.evener` → `~/.evener`, `.evener` → `.evener`, `evener/projects` → `evener/projects`, `share/evener` → `share/evener`, `cache/evener` → `cache/evener`
- Verify: `go test ./...`

**Commit 10: String literals (harness, originator, etc.)**
- `spawnHarness = "evener"` → `"evener"`, `defaultOriginator = "evener"` → `"evener"`, other string constants
- Verify: `go test ./...`

**Commit 11: AppWire protocol version bump + wire JSON keys**
- Bump protocol version
- Rename wire-stable JSON tags: `evener` → `evener`, `evenerErrorInfo` → `evenerErrorInfo`
- Rename wire method strings: `evener/jobs/list` → `evener/jobs/list`, etc.
- Verify: `go test ./...`

**Commit 12: Generated files**
- `make generate` (types.gen.ts, appwire-protocol.md)
- Verify: `git diff --exit-code` after `make generate`

### Phase 3: Scripts & Config (can parallelize after Phase 1)

**Commit 13: Shell scripts**
- 55 .sh files: EVENER_* → EVENER_*, evener → evener
- Verify: `make test-short`

**Commit 14: Config files (yaml/toml/json/sbpl/tmpl)**
- 7 yaml, 2 toml, 5 json, 9 sbpl, 3 tmpl, webmanifest
- Verify: `make test-short`

**Commit 15: GitHub workflows & install.sh**
- .github/ workflows, install.sh
- Verify: visual inspection

### Phase 4: Frontend (can parallelize after Phase 11/12)

**Commit 16: Frontend package.json + manifest**
- `evener-hub-frontend` → `evener-hub-frontend`, manifest names, package-lock.json
- Verify: `make test-web`

**Commit 17: Frontend TS/TSX/HTML/CSS**
- 277 files: evener → evener in UI strings, wire method refs, fixtures
- Verify: `make test-web` + `make test-web-browser`

### Phase 5: Docs & Prose (can parallelize anytime)

**Commit 18: README.md**
- Verify: `make lint-docs`

**Commit 19: docs/ markdown batch 1** (~270 files)
- Verify: `make lint-docs`

**Commit 20: docs/ markdown batch 2** (~530 files)
- Verify: `make lint-docs`

**Commit 21: Go comments**
- 785 files, 1756 occurrences: Evener → Evener, evener → evener in comments
- Verify: `go build ./...`

**Commit 22: AGENTS.md + ABOUT.md**
- Verify: `make lint-docs`

### Phase 6: Migration Tool

**Commit 23: cmd/evener-migrate implementation**
- New binary: moves ~/.evener→~/.evener, ~/.config/evener→~/.config/evener, ~/.local/state/evener→~/.local/state/evener, per-project .evener→.evener
- Idempotent, safe (refuse overwrite), prints report
- Verify: `go build ./cmd/evener-migrate/`

**Commit 24: cmd/evener-migrate tests**
- Test migration logic, idempotency, safety checks
- Verify: `go test ./cmd/evener-migrate/`

### Phase 7: Final Verification

**Commit 25: Final cleanup & verification**
- Full grep for remaining `evener`/`Evener`/`EVENER_` references
- Run: `make test`, `make lint`, `make fuzz-seeds`
- Any stragglers fixed
- Verify: all gates green

## Execution Strategy

**Phase 1-2 (commits 1-12):** Sequential, critical path. Done by me + 1 subagent. These touch Go code and have ordering dependencies. ~12 commits.

**Phase 3-6 (commits 13-24):** Parallelized with up to 6 subagents in worktrees:
- Subagent A: Shell scripts (commit 13)
- Subagent B: Config files (commit 14) + GitHub/install (commit 15)
- Subagent C: Frontend (commits 16-17) — depends on Phase 2 wire changes
- Subagent D: Docs markdown (commits 18-20) + AGENTS.md/ABOUT.md (commit 22)
- Subagent E: Go comments (commit 21)
- Subagent F: Migration tool (commits 23-24)

Each subagent branches from the Phase 2 head, does its work, commits, and I merge back sequentially.

**Phase 7 (commit 25):** Final verification by me.
