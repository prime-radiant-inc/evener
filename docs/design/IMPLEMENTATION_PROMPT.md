# Implementation prompt — fuzzing toolkit (hand to a new agent)

You are implementing serf's API fuzzing + failure-to-regression toolkit. Work in the dedicated worktree, not the main checkout.

## Where
- Worktree: `/home/jesse/git/prime-radiant/serf/.worktrees/fuzzing-toolkit`, branch `wip/fuzzing-toolkit` (already created, off main). Do all work here. Commit frequently, phased; do NOT push and do NOT merge to main — Jesse does that.
- Read first, in order: `docs/design/fuzzing-toolkit-design.md` (the build spec — package layout, signatures, phases, acceptance) and `docs/research/api-fuzzing-toolkit.md` (prior art, the four API surfaces, why serf is fuzz-ready, the soft-spot oracles). The design doc is authoritative for *how*; raise the §7 open decisions with Jesse rather than guessing on them.

## What to build, in this order (design §6)
**Phase 0 first** (free Go-native wins, ~250–450 LoC): the four `testing.F` targets in design §2 — SSE metamorphic (`llm/sse_fuzz_test.go`), appwire frame decode (`appwire/jsonrpc_fuzz_test.go`), appwire per-method param decode over `appwire.Methods` (`appwire/params_fuzz_test.go`), and tool-arg decode+validate over a real `registerCoreTools` registry (`agent/internal/tool/registry_fuzz_test.go`). Add the `make fuzz` / `make fuzz-nightly` targets and seed corpora; wire `make fuzz` (seed-corpus + saved crashers via `go test -run '^Fuzz'`) into the gate. Targets must be deterministic and fast; seed with `f.Add`; the oracle is never just "no panic" — implement the metamorphic/semantic oracles the design calls out.

**Then Phase 3** (the promoter, ~400–700 LoC) in `fuzz/promoter/` per design §3: the `Failure`/`Signature`/`Adapter` types, the bucket store, the Go emitter, and — most important — the **flake-guard-before-promote** loop. This is generic; nothing in `fuzz/promoter` or `fuzz/schemagen` may import serf (that's the portability test).

Phases 1/2/4/5 are scoped in the design but **stop after Phase 0 + 3 and check in with Jesse** before starting them — they involve a new dependency (`pgregory.net/rapid`) and the stateful model, which are design decisions worth confirming with real Phase-0/3 code in hand.

## Key seams (verify the line numbers — code may have moved)
- SSE: `llm.ParseSSE` (`llm/sse.go`), with its two code paths (blocking vs timeout goroutine) — the metamorphic oracle.
- appwire frame: `appwire.Message.UnmarshalJSON` / `ID` (`appwire/jsonrpc.go`); method catalog `appwire.Methods` (`appwire/protocol.go`); typed dispatch `HandleTyped[P,R]` / `Router.Dispatch` (`internal/appserver/router.go`).
- tools: `Registry.ExecuteCall` (`agent/internal/tool/registry.go`); per-tool JSON Schemas in `agent/internal/tool/definitions.go`. Note `Schema.Validate(args)` is NOT recover-wrapped at runtime — a live panic-hunt. Use a temp-dir/read-only exec env so fuzzing can't touch the real FS.

## Rules
- TDD and the project conventions in CLAUDE.md. Run `gofmt`, `go test` for the touched module, and `make lint` before each commit; keep the gate green. go.work does NOT span modules — fuzz targets are per-module; add any new module (`fuzz/`) to `GO_MODULES` in the Makefile (mind the `envvars`-not-gated caveat, research §10).
- Determinism is non-negotiable: a fuzz target that flakes, or a promoter that could promote a non-deterministic failure, is a defect. Prove the promoter's discipline (see acceptance).
- Don't invent serf internals — read the seam before driving it.

## Acceptance (design §6)
- **Phase 0:** `make fuzz` green. Demonstrate the free loop end-to-end on ONE target: inject a deliberate parser bug, show `go test -fuzz` finds it, show the saved `testdata/fuzz/` crasher then keeps `make fuzz` red until the bug is fixed, then remove the injected bug.
- **Phase 3 (the most important tests in the toolkit):** promoter unit tests proving a deterministic synthetic failure **promotes once**, the same failure on a second sighting **dedups** (no second test emitted), and a synthetic **flaky** failure is **quarantined — never emitted**.

Start by reading the two docs, then sketch Phase 0's four targets and confirm the seams compile against current code before filling in oracles.
