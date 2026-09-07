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

### Test/build infra + docs audit (PENDING — 2 scouts still running)

## Implemented wins

(none yet — fill per commit: what / measured before→after / test proof / commit hash)

## Gates before PR

- `go vet ./...` clean
- `go test -count=1 ./...` green (or documented pre-existing failures, verified on base)
- `make lint` if touched areas require it
- No behavior change without a test proving parity
