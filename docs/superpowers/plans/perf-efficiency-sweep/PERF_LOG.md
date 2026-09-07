# Perf Efficiency Sweep — Log

Branch: `perf-efficiency-sweep` (base `be7002918`, 2026-09-06).
Goal: CPU + memory efficiency wins with zero product/usability regression.
Method: baseline → parallel scout (muse-spark-1.3-contributor xhigh) → implement safe wins one-by-one → gates → PR.
Rule: small frequent commits; every claim backed by measurement.

## Baseline (2026-09-06, darwin/arm64, go1.27.0)

- `go build ./cmd/evener`: 8.04s real, 17.26s user, 4.66s sys; max RSS 594,100,224 bytes (~566 MiB peak compiler RSS); binary 54,141,570 bytes (~51.6 MiB).
- Repo checkout: ~106M; frontend dir: ~12M (`cmd/evener-hub/frontend`).
- Go module deps (`go list -m all`): 121.
- `go vet ./...`: exit 0 (clean).
- `go test -count=1 ./...`: exit 0, 73 packages ok, zero FAIL. Baseline GREEN.

## Scout reports

### Backend hot paths (dlg_034KXqrm9JIrWml9trkebb, DONE 2026-09-06)

9 ranked opportunities (all savings unmeasured; each names its proof benchmark):

1. Repeated `json.MarshalIndent` of full envelopes to probe size — `agent/session_tools_transcript.go:1235,1288-1310,1325,1371,1275`, `agent/apilog_read.go:168-188,270-278`, `agent/internal/modelavailability/modelavailability.go:378`. Risk low-med.
2. `rawLinesForRange` double-decode per line + rune-length whole-string + quadratic cap loop — `agent/transcript_render.go:343,348,352,383,387,390,399,418,424`. Risk med.
3. Retained search serializes all prior matches per candidate (O(matches²)) — `agent/retained_output_read.go:227,235,244,253-264`. Risk low.
4. Image bytes deep-copied on every mutation-queue transit — `agent/session_client_mutation_queue.go:424,439`, `agent/session_client_mutation.go:1363-1374`. Risk med.
5. Job-output grep uses default 4KB bufio — `agent/internal/jobstore/output.go:526,563`. Risk low.
6. Retained line scanner copies every line before regex test — `agent/retained_output_read.go:337-377,394,209,210,224,249`. Risk low.
7. Dashboard filter rebuilds lowercase haystack per row, 2× per keystroke — `cmd/evener-tui/hub_dashboard.go:491-502,440-471`. Risk low.
8. Per-notification full `json.Unmarshal` on TUI hot path incl. streaming deltas — `cmd/evener-tui/hub_notifications.go` (many). Risk med.
9. `Page.SerializedBytes` pays indented serialize for a length — `agent/internal/modelavailability/modelavailability.go:369-378`. Risk low.
- Non-findings: regexes already package-level; reconnect backoff capped; transcript builders already use strings.Builder.

### Daemon/session/state overhead (dlg_034KXssYf5SNQu2pLvUjHI, DONE 2026-09-06)

9 ranked opportunities (all unmeasured; highest confidence × lowest risk: #1, #2-double-load, #5-coalesce):

1. jobstore single-event fsync per Append; AppendBatch exists but hot paths use single — `agent/internal/jobstore/store.go:146-173,179-214`, `agent/job_watch.go:499-520`. Risk low-med (crash-atomicity boundaries).
2. Full journal re-fold on hot paths, twice in a row — `store.go:249,263,277,217` + callers `agent/job_watch.go:1613,1682,3160+3164,3742`, `agent/session_jobtree_drain.go:1272`. Risk low (cursor infra exists) to med.
3. `rematerializeDurablePendings` full Load() on drain path, no dirty flag — `agent/session_jobtree_drain.go:1242,1271-1282`. Risk low (must preserve issue-#140 race window).
4. 250ms drain-recheck ticker polling where events exist — `agent/session_jobtree_drain.go:44-50,899,912,915-916`. Risk med (backstop for lost wakes; backoff, never delete).
5. One goroutine+ticker per progress watch + per delegate quiet-watchdog — `agent/job_watch.go:3304-3325,1973-1986,74-75`, `agent/delegate_runtime.go:152-173`. Risk low-med.
6. OutputMatcher re-scans + reallocates whole window per chunk — `agent/internal/jobstore/watch.go:155,231,257-269,205-222`. Risk med (watch_fuzz_test.go is oracle).
7. `deliveredWatchSendIDs` + `lastFedOffset` grow without visible eviction — `agent/jobs.go:88-102`, `agent/job_watch.go:511-513,524-527`. Risk low.
8. Transcript resume + doctor both pay full strict re-decode — `agent/transcript/transcript.go:590-642,136-171`, `agent/doctor/transcript.go:29-74`. Risk med (strictness load-bearing).
9. AppendDurable seeks every call; transcript default fsyncs every Append — `agent/transcript/transcript.go:230-236,436-442,453-466,497-513`. Risk low-med; `cmd/evener-hub/app_threadread_test.go:1089` pins no-fsync-per-Append for 200-turn case.

### Frontend bundle + runtime (dlg_034KXrwkPlP1rRW5sW3BbY, DONE 2026-09-06)

10 ranked opportunities (all unmeasured; no build/test run per scout constraints):

1. Remove unused `react-router` dep (zero imports; only a comment references it) — `cmd/evener-hub/frontend/package.json:35`, `src/shell/AppShell.tsx:125`. Risk negligible.
2. Lazy-load `qrcode.react` behind Mobile settings section — `src/panes/settings/sections/mobile.tsx:1,56`, `src/panes/settings/index.tsx:32`. Risk low.
3. Deduplicate `marked` instances (shared lexer) — `src/widgets/markdown/index.tsx:94`, `src/panes/session/transcript/messages/reasoningFormat.ts:9`. Risk low.
4. Throttle markdown re-parse during streaming (full parse+sanitize per token = O(n²)) — `src/widgets/markdown/index.tsx:167`. Risk med (settled path untouched → final render byte-identical).
5. `anser` per-render instantiation in codeblock ANSI path — `src/widgets/codeblock/ansi.ts:1,424`. Risk low if stateless.
6. Unify 1s `setInterval` clocks to 3s liveness tick — `ActivityTree.tsx:284`, `Spawn.tsx:476`, `liveness.ts:13,58-66`. Risk medium-low.
7. Scope `setNow` to leaf labels so ticks don't re-render whole panes — same sites. Risk low.
8. Eager full-size image `src` + base64 data-URIs in store — `ImageGallery.tsx:67+`, `reducer.ts:318`, `AttachmentTile.tsx:68`, `stores/threads.ts:52,72`. Risk med.
9. Barrel `src/widgets/index.ts` (120 lines, ~20 importers) defeats code-splitting — `Spawn.tsx:34` et al. Risk low-medium.
10. Split 1000+ line panes at lazy sub-boundaries (Rail 1604, Composer 1475, Spawn 1213, CommandPalette 1003). Risk med.
- Explicitly NOT recommended (already fine): dockview chunking, DEV-only routes, tooltip/popover policies, font loading, zustand selectors, mutationOutbox 2s scan.

### Test/build infra (dlg_034KXtmP3VL6XZCP5DRiPq, DONE 2026-09-06)

10 ranked opportunities (all unmeasured; static scan, nothing run):

1. `make fuzz` replays committed Fuzz seed corpus 2× plus rapid replay — `make/fuzzing.mk:86-92` (~144s native portion). Risk low-med (prove pass-set parity first).
2. FuzzToolArgsValidate corpus bloat: 2327 files/13MB of 16MB agent total, 4235 files repo-wide — coverage parity required before minimizing. Risk med.
3. setup-go has NO Go build/module cache in CI — `.github/actions/setup-toolchain/action.yml:8-16`, `ci.yml:59,76,93`. Risk low (cache-only).
4. Race lanes oversubscribed then throttled (AGENT_SHARDS=0 under -race) — `ci.yml:140-154`, `run-module-tests.sh:97-109`. Risk low (scheduling only).
5. `build-linux` wipes whole Go build cache (`go clean -cache`) — `make/building.mk:39-41`. Risk low.
6. lint-golangci double pass + serial LINT_TARGETS chain, each paying fresh `go run` compile — `make/linting.mk:119-120,207`. Risk low (prebuild evener-dev once).
7. vet runs ~2×+ (host + GOOS=windows + tagged vets, ~40 invocations) — `ci.yml:41-49`, `testing.mk:116-117`. Keep windows vet (issue #897); win via shared GOCACHE. Risk low-med.
8. Real-sleep abuse: `llm/provider_idle_test.go:37` (40s!), `agent/deadline_audit_test.go:240` (5s), `local_daemon_test.go:227` (2s), tmux_e2e 9 sleeps; budgets: evener-hub 67.09s, evener-tui 40.39s, plugins 31.94s. Risk med (convert to bounded poll, never delete).
9. coverage-floor.sh re-runs entire suite 2× per module — `scripts/coverage/coverage-floor.sh:130-142`. Risk med.
10. No artifact retention cap (default 90d) + duplicate goreleaser snapshot builds on main push — `binaries.yml:42-49`, `ci.yml:194-220`. Retention-days=7-14 is zero-risk; build-dedup med-high.
- NOT proposed: deleting coverage without parity; gating on timing budget; re-enabling node compile cache (needs owner sign-off).

### Docs + dead-weight audit (dlg_034KXujN2o0uGJCxPLZJx9, DONE 2026-09-06)

10 ranked opportunities (static read-only; no coverage deletion proposed except byte-identical seed dupes):

1. Deduplicate byte-identical fuzz seed files — `agent/testdata/fuzz/FuzzToolArgsValidate/` 13M/2327 files; pool 3364 files/~18M; repeated md5s (7×, 6×, 5×). Dedupe within a target dir only. Risk low-med.
2. Shrink/derive `llm/registry/testdata/models.dev.sample.json` (1.5M) from `data/models.dev.json.gz` (435K) at test setup — UNVERIFIED subset assumption, check first. Risk med.
3. Losslessly recompress `docs/web-ui` PNGs (~4.3M, one 1.1M file); docs total 27M. Risk low.
4. Gate per-PR goreleaser snapshot on packaging inputs — `.github/workflows/ci.yml:194-220` (pinned by branch protection, needs settings change). Risk med.
5. Merge two golangci-lint install steps — `ci.yml:63-69,80-86`. Risk low-med.
6. `lint-naming` pays a `go run` compile to check 5 TOML files — `make/linting.mk:26-27`. Keep check, cheapen invocation. Risk low.
7. secret-scan cost driver is the corpus itself — keep gate, shrink input via #1. Risk high if scoped instead; do NOT scope.
8. Double AST walk (lint-fuzz-registry + fuzz-gap-check) in different jobs — combine into one job. Risk low.
9. GOOS=windows vet duplication mostly must stay (issue #897, rotted-eval precedent); only valid trim: skip modules with zero tagged files. Risk med-high if deleted.
10. lint-generated runs codegen in lint lane — cache the build, keep the check. Risk low (caching) / high (deleting).
- Explicit non-proposals: 333 covtest + 2257 _test.go files (no dup evidence); plans/ history + test/scenarios (runner untraced).

## Implemented wins

- Frontend #1 (2026-09-06): drop unused `react-router` dep — `cmd/evener-hub/frontend/package.json` + lockfile (29 deletions). Proof: `npm ls react-router` empty, `tsc --noEmit` clean, 121 scoped vitest green, `vite build` green. Commits `82bb54aa7` + merge `74705b469`.
- Frontend #5 anser singleton: SKIPPED with evidence — Anser instances carry mutable fg/bg/decorations state across calls (behavioral leakage proven), singleton would corrupt output.
- Backend #5 (2026-09-06): 64KiB bufio for job-output grep — `agent/internal/jobstore/output.go:526,565` (matches store.go 64KiB convention). Unmeasured (buffer-size-only, parity by construction); jobstore tests green. Commit `821671373` + merge `d488e6af8`.
- Backend #3 (2026-09-06): O(1) retained-match size accounting — `agent/retained_output_read.go` running accumulator + `agent/retained_output_size_acc_test.go` (parity test vs wire bytes + kept benchmark). Measured 100-match search: 1,962,081 → 52,598 ns/op (~37×), 1,533,574 → 92,140 B/op (~16.6×), 15,469 → 615 allocs/op (~25×). Retained tests + parity test green. Commit `d4d457511` + merge `d488e6af8`.
- Backend #9 SerializedBytes: SKIPPED with evidence — callers use distinct per-iteration trial copies (nothing to cache); Marshal would move truncation boundary; loop bounded (≤128 × ≤4KB, rare model_list path).

## Gates before PR

- `go vet ./agent/... ./agent/internal/jobstore/ ./agent/internal/modelavailability/`: clean (post-merge).
- `gofmt -l` on touched Go files: clean.
- `go test -count=1 ./...` post-merge: exit 0, 73 packages ok, zero FAIL (full output `$EVENER_SCRATCH_DIR/perf-verify.txt`).
- Frontend `tsc`/`vite build`/scoped vitest: verified in lane before merge (see lane report).
- No behavior change without a test proving parity
