#!/usr/bin/env bash
# e2e-cover.sh — measure END-TO-END coverage of the real serf/serf-tui binaries.
#
# WHY: unit tests can't reach main(), the CLI dispatchers, flag parsing, help/
# error exits, or the full run/serve wiring — that code only executes in the
# shipping binary. This harness builds instrumented binaries (`go build -cover`,
# Go 1.20+), drives them through a battery of real invocations, and collects the
# coverage the binary emits via GOCOVERDIR. It is the measured complement to the
# `go test` unit coverage: together they show what ALL tests + real e2e reach.
#
# WHAT IT RUNS: a no-network, no-credential battery covering every serf/serf-tui
# subcommand's help/dispatch/error path (the bulk of the cmd/* surface). With
# SERF_E2E_LIVE=1 it also runs the live scenario scripts in test/ (which need
# real provider credentials) under the same GOCOVERDIR.
#
# USAGE:
#   scripts/e2e-cover.sh                 # CLI battery, print cmd/* coverage
#   scripts/e2e-cover.sh --merge-unit    # also run unit tests, print COMBINED %
#   scripts/e2e-cover.sh --html OUT.html # write an HTML coverage report
#   SERF_E2E_LIVE=1 scripts/e2e-cover.sh # additionally run live provider scripts
#
# OUTPUT: a merged textfmt profile (path printed) + a per-package cmd/* summary.
# The profile is combinable with the unit profile (union) via --merge-unit.
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

merge_unit=false
html_out=""
while [ $# -gt 0 ]; do
	case "$1" in
		--merge-unit) merge_unit=true; shift ;;
		--html) html_out="$2"; shift 2 ;;
		-h|--help) sed -n '2,28p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
		*) echo "unknown flag: $1" >&2; exit 2 ;;
	esac
done

workdir="$(mktemp -d -t serf-e2e-cover.XXXXXX)"
covdir="$workdir/gocov"; mkdir -p "$covdir"
serf="$workdir/serf"; tui="$workdir/serf-tui"

echo "==> building instrumented binaries (go build -cover)"
go build -cover -o "$serf" ./cmd/serf || { echo "build serf failed" >&2; exit 1; }
go build -cover -o "$tui" ./cmd/serf-tui || { echo "build serf-tui failed" >&2; exit 1; }

# run CMD under GOCOVERDIR, ignoring its exit code (error paths are on purpose).
run() { GOCOVERDIR="$covdir" "$@" >/dev/null 2>&1 || true; }

echo "==> driving the no-credential CLI battery"
# serf: version, help, every dispatcher arm's --help, and error paths.
run "$serf" --version
run "$serf" --help
run "$serf" -h
run "$serf" serve --help
run "$serf" launch-check --help
run "$serf" launch-check
run "$serf" upgrade --help
run "$serf" openai --help
run "$serf" openai login --help
run "$serf" openai logout --help
run "$serf" openai status --help
run "$serf" openai status
run "$serf" openai bogus-subcommand
run "$serf" --bogus-flag
run "$serf" totally-unknown-command
# serf-tui: help + error path.
run "$tui" --help
run "$tui" -h
run "$tui" --bogus

if [ "${SERF_E2E_LIVE:-0}" = "1" ]; then
	echo "==> SERF_E2E_LIVE=1: running live provider scripts under GOCOVERDIR"
	export GOCOVERDIR="$covdir" SERF_BIN="$serf"
	for s in test/*.sh; do
		[ -f "$s" ] || continue
		echo "    $s"; bash "$s" >/dev/null 2>&1 || true
	done
fi

e2e_prof="$workdir/e2e.prof"
go tool covdata textfmt -i="$covdir" -o="$e2e_prof" || { echo "covdata textfmt failed" >&2; exit 1; }
echo
echo "==> e2e binary coverage by cmd/* package:"
go tool covdata percent -i="$covdir" 2>/dev/null | grep -E 'cmd/serf|cmdutil|hubapi|rendezvous' | sort

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
