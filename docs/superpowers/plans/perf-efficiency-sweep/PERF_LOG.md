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

### Frontend / daemon / test-infra / docs (PENDING)

## Scout assignments (all muse-spark-1.3-contributor, xhigh reasoning)

1. Backend hot paths (Go hot loops, allocs, cloning, polling, retries).
2. Frontend bundle + runtime (import cost, re-render, bundle size).
3. Daemon/session/state overhead (goroutines, timers, file IO, stores).
4. Test/build infra (slow tests, redundant coverage, build caching).
5. Docs/audit (verify claims, kill dead weight that costs CI time).

Each scout: read-only, report ranked opportunities with file:line evidence + expected saving + risk. No code changes.

## Implemented wins

(none yet — fill per commit: what / measured before→after / test proof / commit hash)

## Gates before PR

- `go vet ./...` clean
- `go test -count=1 ./...` green (or documented pre-existing failures, verified on base)
- `make lint` if touched areas require it
- No behavior change without a test proving parity
