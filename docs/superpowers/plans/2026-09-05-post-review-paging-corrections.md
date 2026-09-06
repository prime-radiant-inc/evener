# Post-review atomic paging corrections

> **Execution:** implement each step with focused RED → GREEN → refactor → commit verification. One scoped writer; no delegated edits.

**Goal:** Close I1–I4 and M1–M4 without changing the approved atomic-item protocol or restoring the removed external adapter.

**Architecture:** Authenticate complete-to-bounded native observations by following the actual backward continuation to exhaustion, rebuilding chronological contiguous history and comparing its anchored prefix to the retained digest. Publish source cache changes only on successful operations. Make indexed turn APIs consistently grouped, and stage transcript index/resolver/cache/sidecar/journal effects behind successful selected projection and a final context check.

**Tech stack:** Go, AppWire, JSONL transcript indexes and journals, deterministic scripted transports and projectors, Go race detector.

**Spec:** `docs/superpowers/specs/2026-09-01-atomic-transcript-item-paging-design.md`; existing implementation plan: `docs/superpowers/plans/2026-09-01-atomic-transcript-item-paging-plan.md`.

## Global constraints

Start at `267893d24376629ba2b8980347934f87df0b8b65`. Preserve typed stale errors, append and overlap behavior, public maximum 40 upstream items, legacy per-entry `TurnsFromFile`, provider/OAuth/model support, generic AppWire/Codex-shaped compatibility, `.codex`/`.codex-plugin`, and local daemon infrastructure. Never publish or touch LRU on cancellation; keep valid preexisting cache and persistence intact. No out-of-scope production files; no weakening tests or editing generated output manually.

## Step 2 — Logical coordinates and transcript transaction (M1–M3)

Files: `internal/apptranscript/apptranscript.go`, `turn_cache.go`, `turn_index.go`, `item_paging.go`, and scoped `*_test.go`.
Interfaces: grouped full projection and context-aware cache loading; `LatestFromFile[Context]`, `PageFromFile[Context]`, `LatestItemsFromFile`, `PageItemsFromFile`, item index loading/publication.

1. RED: compare unlimited and bounded grouped results against raw legacy per-entry APIs; exercise pre-canceled unlimited and bounded warm hits; cancel during selected cold item projection and assert no index/sidecar publication and unchanged valid warm state.
2. GREEN: introduce cancellable grouped scanning/projection and context cache loading, with lock-time checks. Move item index, resolver, LRU, base sidecar and journal publication behind one successful projection transaction. Preserve serialization and suffix-only append behavior.
3. Refactor and cover canceled append/warm-cache paths and logical numeric cursor consistency. Run named tests, complete package and focused race checks. Inspect staged diff and commit explicit transcript paths.

## Step 3 — Active guidance and final verification (M4)

Files: `internal/appwiredoc/protocol.md.tmpl`, generated `docs/appwire-protocol.md`, comments in `cmd/evener-hub/app_rpc.go` and `cmd/evener-hub/web_api_tree.go`, this plan.

1. Establish stale guidance with exact text search; replace removed external source/managed launcher/bridged-row claims with generic AppWire descriptions.
2. Run canonical `make generate`; verify protocol output is reproducible, inspect staged diff, and commit only allowed paths. Run `make lint-generated` after commit.
3. Run `gofmt -w` on touched Go files, confirm `gofmt -d` empty; run both full subsystem packages, hub package, and focused race gates. Check `git diff --check` from base, scope of changed paths, and final empty porcelain.
4. Write scratch report with exact RED/GREEN commands, failure evidence, exit statuses, commit hashes, architecture, self-review and limitations. Return integration/re-review packet to controller; controller owns rerunning original adversarial overlays and sending the same reviewer the packet after integration.

## Self-review coverage

I1: every complete→bounded overlap is authenticated. I2: traversal reaches the beginning across gaps, and next digest/count covers reconstructed history. I3: every verification page respects public 40 and validates progress/cycles. I4: source read/list/conversion/refresh publication and LRU are success-only. M1: grouped numeric coordinate system is limit-independent while raw API stays separate. M2: full and warm context paths honor scanning/lock cancellation without cache effects. M3: cold/advanced index persistence and resolver state commit only after selected projection success. M4: template and generated guidance agree and source comments remain generic. All eight groups have explicit tests or generation checks above.

3. **F3 GREEN:** in `internal/apptranscript/turn_index.go`, derive candidate trust from the index's embedded identity for memory and disk candidates alike. Do not invalidate indexes on raw/grouped full reads; preserve valid append reuse. No cache or lock redesign.
4. **F4/F5:** change only the misleading usage example in `cmd/evener/serve.go`. Report raw grep rows 188,193,194,3065,3068 as W (generic `CodexErrorInfo` wire clone/server code/tests), not C; leave all code and controller-owned ignored classification artifacts intact.
