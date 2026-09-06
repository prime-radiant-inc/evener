# AppWire Item-Only Flag-Day Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Remove legacy transcript turn paging and ship a single item-paging contract.

**Architecture:** AppWire v4 rejects old peers. Live, saved, relay, and browser paths use native bounded item pages; logical turns remain grouping containers. Three isolated Luna lanes implement disjoint ownership sets, then the controller integrates and verifies the complete contract.

**Tech Stack:** Go, TypeScript, scripted AppWire transports, Vitest, existing generators.

**Spec:** `docs/superpowers/specs/2026-09-05-appwire-item-only-flag-day-design.md`

## Global Constraints

- Protocol: `evener-appwire-v4`; no negotiation or legacy retry.
- Remove all `PageUnit` fields and `TranscriptPageUnit`, read `TurnLimit`, transcript-list `Limit`. Retain separate per-turn item-list `Limit` and unrelated limits.
- Keep `ItemLimit`, default/maximum 40 with existing normalization. Transcript-list cursor is required and opaque. Retired paging fields must be rejected through the existing decoder boundary.
- Preserve current item response-validator function names and method names. Make their validation unconditional.
- Keep grouped turns, positions, keys, empty-turn ordinals, accounting, identity/append/rewrite/cancellation guarantees, enrichment and atomic relay handoff.
- No full local repository tests or races. Focused Go commands use `GOMAXPROCS=2 go test -p 1`; full CI remains the broad gate. At most three isolated writing lanes; no nested delegates.
- Read `docs/developing-evener/testing.md` before test changes. RED must be a real behavioral failure, not a compile error from absent cross-lane changes.
- Stage explicit owned paths only. Do not push or amend. Report test command/output, commit range, scope and incomplete gates.

## Shared interface

Keep `ThreadReadParams` with existing identity/subscription fields and `ItemLimit`; remove only `PageUnit`/`TurnLimit`. Keep `ThreadTurnsListParams` with existing identity/cursor/items-view fields and `ItemLimit`; remove only `PageUnit`/`Limit`. Responses retain existing fields except `PageUnit`. Source interface signatures and item validator names remain unchanged unless a genuinely dead optional interface is proved; coordinate any signature change before committing it.

The core lane publishes a small shared wire-contract commit early. Other lanes may cherry-pick that exact commit into their isolated lanes (not duplicate-edit the contract) to compile their owned consumers. Report this ancestry; integration must avoid duplicate commits. Cross-lane compile failures remain incomplete until combined verification, not reasons for compatibility stubs.

### Task 1: Wire contract and daemon

**Ownership:** `appwire/`, `internal/appitempaging/`, `internal/appserver/`, `internal/appwiredoc/`, `internal/appwirets/`, `server/`; generated `docs/appwire-protocol.md` and `cmd/evener-hub/frontend/src/protocol/types.gen.ts`. No other frontend/hub/apptranscript edits.

**Produces:** the shared contract above and daemon item-only read/list behavior. Existing grouped projection APIs remain inputs from Task 2.

Additional owned caller fixture found by controller: `cmd/evener/internal/launchcheck/launchcheck_test.go` asserts the exact successful protocol string. Migrate it to v4 and run `GOMAXPROCS=2 go test -p 1 ./cmd/evener/internal/launchcheck`.

- [ ] Add failing assertions before production changes. At minimum a protocol assertion can use:
  ```go
  if appwire.ProtocolVersion != "evener-appwire-v4" { t.Fatalf("protocol = %q", appwire.ProtocolVersion) }
  ```
  Add a real public-handler default read with more than 40 items; assert no more than 40 returned, positions/keys exist and older cursor is nonempty without selecting a mode. Add decoder cases for retired `pageUnit`, `turnLimit`, and transcript-list `limit`; preserve version mismatch tests.
- [ ] Run focused new tests on the unchanged implementation with `GOMAXPROCS=2 go test -p 1 ./appwire ./internal/appserver ./server -run 'FlagDay|ItemOnly' -count=1 -v`; record the exact assertion failures.
- [ ] Implement types/version/decoder changes and unconditional validators. Publish the minimal contract commit early; notify controller with exact SHA and removed symbols.
- [ ] Remove `WindowTurns`, `PageTurns`, `DefaultTurnPageSize` and daemon turn-mode snapshot/runtime paths. Migrate tests/fuzz references to the new contract, preserving surviving invariants. Keep native adapter semantics separate from AppWire peer versioning.
- [ ] Run affected packages (one command at a time): `GOMAXPROCS=2 go test -p 1 ./appwire ./internal/appitempaging ./internal/appserver ./server`; focused new regressions under race; `go generate ./appwire`; format/diff checks. Report blockers from cross-lane dependencies honestly.
- [ ] Self-review and commit explicit owned files; write report with RED/GREEN, commits and remaining cross-lane compile dependencies.

- [ ] Remove saved turn-window wrappers and unused legacy projection/cache branch; preserve grouped full internal projections, shared indexes, accounting and empty-group `NextEntry`. Trace callers before deleting similarly named APIs. Do not delete unrelated agent/doctor transcript semantics.
### Task 4: Integrate, verify, review and publish

**Ownership:** controller coordinates, delegates fixes to owning lane. Cross-cutting compile-only callers outside lane ownership receive an explicit ownership grant before editing.

- [ ] Inspect all reports/diffs and contract commit ancestry. Recheck clean target branch/ref immediately before each explicit integration; preserve rollback refs and unrelated files. Do not duplicate cherry-picked contract commits.
- [ ] Run the combined affected Go packages serially with low parallelism. Run new focused regressions under race and package vet; `make test-web`, browser guard if available, generation and relevant lint gates. No full local `make test`, race, or merge-approval gate.
- [ ] Inventory `PageUnit`, `TranscriptPageUnit`, turn-window symbols, numeric transcript paging, `materializeLocalDaemonTurns`, v3 declarations and compatibility documentation. Classify residual matches as intentional rejection fixtures, unrelated APIs, or defects; fix defects.
- [ ] Dispatch independent spec/code review over each ownership range and combined end-to-end change. Preserve regression assertions during fixes and run covering tests again. Run the requested simplification workflow over the correction after correctness review.
- [ ] Once focused gates and review pass, ordinary fast-forward push to PR #822 branch `atomic-transcript-item-paging` after exact remote/PR/ancestry preflight. Verify PR head and await CI/RoboRev, reporting findings rather than equating review completion with approval.
