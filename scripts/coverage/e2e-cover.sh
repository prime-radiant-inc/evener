#!/usr/bin/env bash
# e2e-cover.sh — measure END-TO-END coverage of the real evener binary.
#
# WHY: unit tests can't reach main(), the CLI dispatchers, flag parsing, help/
# error exits, or the full run/serve wiring — that code only executes in the
# shipping binary. This harness builds an instrumented binary (`go build -cover`,
# Go 1.20+), drives it through a battery of real invocations, and collects the
# coverage the binary emits via GOCOVERDIR. It is the measured complement to the
# `go test` unit coverage: together they show what ALL tests + real e2e reach.
#
# WHAT IT RUNS: a no-network, no-credential battery covering every evener
# subcommand's help/dispatch/error path (the bulk of the cmd/* surface). With
# EVENER_E2E_LIVE=1 it also runs the live scenario scripts in test/ (which need
# real provider credentials) under the same GOCOVERDIR.
#
# USAGE:
#   scripts/coverage/e2e-cover.sh                 # CLI battery, print cmd/* coverage
#   scripts/coverage/e2e-cover.sh --merge-unit    # also run unit tests, print COMBINED %
#   scripts/coverage/e2e-cover.sh --tui           # also run the tmux TUI battery (slow)
#   scripts/coverage/e2e-cover.sh --html OUT.html # write an HTML coverage report
#   EVENER_E2E_LIVE=1 scripts/coverage/e2e-cover.sh # additionally run live provider scripts
#
# OUTPUT: a merged textfmt profile (path printed) + a per-package cmd/* summary.
# The profile is combinable with the unit profile (union) via --merge-unit.
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

merge_unit=false
run_tui=false
html_out=""
while [ $# -gt 0 ]; do
	case "$1" in
		--merge-unit) merge_unit=true; shift ;;
		--tui) run_tui=true; shift ;;
		--html) html_out="$2"; shift 2 ;;
		-h|--help) sed -n '2,28p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) echo "unknown flag: $1" >&2; exit 2 ;;
	esac
done

workdir="$(mktemp -d -t evener-e2e-cover.XXXXXX)"
covdir="$workdir/gocov"; mkdir -p "$covdir"
evener="$workdir/evener"

echo "==> building web UI (frontend/dist with Vite hashed assets)"
make build-web >/dev/null 2>&1 || { echo "build-web failed" >&2; exit 1; }

echo "==> building instrumented binary (go build -cover)"
go build -cover -o "$evener" ./cmd/evener || { echo "build evener failed" >&2; exit 1; }

# run CMD under GOCOVERDIR, ignoring its exit code (error paths are on purpose).
run() { GOCOVERDIR="$covdir" "$@" >/dev/null 2>&1 || true; }

echo "==> driving the no-credential CLI battery"
# evener: version, help, every dispatcher arm's --help, and error paths.
run "$evener" --version
run "$evener" --help
run "$evener" -h
run "$evener" serve --help
run "$evener" launch-check --help
run "$evener" launch-check
run "$evener" upgrade --help
run "$evener" openai --help
run "$evener" openai login --help
run "$evener" openai logout --help
run "$evener" openai status --help
run "$evener" openai status
run "$evener" openai bogus-subcommand
run "$evener" --bogus-flag
run "$evener" totally-unknown-command
# evener tui: help + error path.
run "$evener" tui --help
run "$evener" tui -h
run "$evener" tui --bogus

# Web-hub battery: start the real hub HTTP server (instrumented) on a loopback
# port, drive its routes with curl, then stop it. Captures the web handler /
# API / static-asset paths that only run inside the serving binary. Skipped if
# curl is unavailable. Routes are verified against the real built dist/ to
# ensure asset coverage exercises actual Vite hashed paths, not stale routes.
if command -v curl >/dev/null 2>&1; then
	echo "==> driving the web-hub HTTP battery"
	hub_run="$workdir/hubrun"; mkdir -p "$hub_run"
	port=$(( (RANDOM % 2000) + 9300 ))
	GOCOVERDIR="$covdir" HOME="$workdir" "$evener" hub --addr "127.0.0.1:$port" >"$workdir/hub.log" 2>&1 &
	hub_pid=$!
	# wait up to ~5s for the listener line.
	for _ in $(seq 1 50); do grep -q 'listening on' "$workdir/hub.log" 2>/dev/null && break; sleep 0.1; done
	base="http://127.0.0.1:$port"

	# Extract real asset paths from the built index.html to verify /webassets/ routes work.
	# Vite outputs hashed filenames like /webassets/index-<hash>.js; grep index.html to find them.
	if [ -f "cmd/evener-hub/frontend/dist/index.html" ]; then
		# Extract all /webassets/* paths from index.html (e.g., src="/webassets/index-CmdW429A.js")
		webasset_routes=$(grep -oE '"/webassets/[^"]+' "cmd/evener-hub/frontend/dist/index.html" | tr -d '"' | sort -u)
	else
		webasset_routes=""
	fi

	for route in / /api/health /api/models /api/tree \
		/credentials /auth \
		/api/sessions/nonexistent /doc/file /nonexistent-route; do
		curl -fsS --max-time 5 "$base$route" >/dev/null 2>&1 || true
	done

	# Test real /webassets/* routes (Vite-hashed assets, only available after build-web).
	# These are auth-gated so expect 401, not 404. A 404 would indicate stale coverage.
	if [ -n "$webasset_routes" ]; then
		for asset_route in $webasset_routes; do
			http_code=$(curl -s -w '%{http_code}' --max-time 5 "$base$asset_route" -o /dev/null 2>&1)
			case "$http_code" in
				401|200) ;; # auth-gated (401) or open (200) — both OK, route exists
				404) echo "    ERR: /webassets route returned 404 (stale/missing): $asset_route" >&2; exit 1 ;;
				*) echo "    WARN: /webassets route returned HTTP $http_code: $asset_route" >&2 ;;
			esac
		done
	fi

	# Stop the coverage hub.
	kill -TERM "$hub_pid" 2>/dev/null || true
	wait "$hub_pid" 2>/dev/null || true
fi

if [ "${EVENER_E2E_LIVE:-0}" = "1" ]; then
	echo "==> EVENER_E2E_LIVE=1: running live provider scripts under GOCOVERDIR"
	export GOCOVERDIR="$covdir" EVENER_BIN="$evener"
	for s in test/*.sh; do
		[ -f "$s" ] || continue
		echo "    $s"; bash "$s" >/dev/null 2>&1 || true
	done
fi

# TUI battery: drive the real terminal UI in tmux (slow; needs tmux). The
# tmux e2e tests build the evener binary's TUI subcommand with -cover and launch
# it with GOCOVERDIR set to our covdir when EVENER_E2E_COVER is exported, so the
# TUI subprocess's paint / interaction coverage — which units give 0% for —
# merges into this run.
if $run_tui; then
	if command -v tmux >/dev/null 2>&1; then
		echo "==> driving the TUI tmux battery under coverage (slow)"
		EVENER_E2E_COVER="$covdir" go test -run 'TmuxE2E' -count=1 -timeout 20m ./cmd/evener-tui/ >"$workdir/tui.log" 2>&1 \
			|| echo "    (some tmux tests failed; coverage still collected — see $workdir/tui.log)"
	else
		echo "==> --tui requested but tmux not installed; skipping"
	fi
fi

e2e_prof="$workdir/e2e.prof"
go tool covdata textfmt -i="$covdir" -o="$e2e_prof" || { echo "covdata textfmt failed" >&2; exit 1; }
echo
echo "==> e2e binary coverage by cmd/* package:"
go tool covdata percent -i="$covdir" 2>/dev/null | grep -E 'cmd/evener|cmdutil|hubapi|rendezvous' | sort

if $merge_unit; then
	echo
	echo "==> running unit tests for the merge (whole repo -coverpkg)"
	unit_prof="$workdir/unit.prof"
	go test -count=1 -coverpkg=./... -coverprofile="$unit_prof" ./... >/dev/null 2>&1 || true
	# union the two textfmt profiles: a block is covered if EITHER run hit it.
	python3 - "$unit_prof" "$e2e_prof" <<'PY'
import re, sys
seen = {}
for path in sys.argv[1:]:
	try:
		f = open(path)
	except OSError:
		continue
	for l in f:
		m = re.match(r'^(.+?):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)$', l)
		if not m:
			continue
		f_, sl, sc, el, ec, ns, cnt = m.groups()
		key = (f_, sl, sc, el, ec)
		seen[key] = (int(ns), seen.get(key, (0, False))[1] or int(cnt) > 0)
tot = sum(n for n, _ in seen.values())
cov = sum(n for n, c in seen.values() if c)
print(f"COMBINED unit+e2e union: covered={cov} total={tot} pct={100*cov/tot:.1f}%")
PY
fi

if [ -n "$html_out" ]; then
	go tool cover -html="$e2e_prof" -o "$html_out" && echo "==> HTML report: $html_out"
fi
echo
echo "e2e profile: $e2e_prof"
