#!/usr/bin/env bash
# measure-transcript-index-latency.sh — the transcript read model's latency
# gate (docs/superpowers/specs/2026-09-25-transcript-read-model-design.md,
# "Acceptance criteria"), measured on real transcripts.
#
# For each transcript it runs, against a private copy:
#   - internal/transcriptindex TestRealTranscriptWindowsEqualTheReference:
#     every page of the index equals the whole-file projection;
#   - internal/transcriptindex TestRealTranscriptLatency: the latest window
#     (40 items), 200 samples idle and 200 while appending one entry every
#     100 ms, at the source and at the page (regroup + JSON encode) level;
#   - server TestRealTranscriptInMemoryReadLatency: today's in-memory read of
#     the same window, the baseline.
#
# Usage:
#   scripts/ops/measure-transcript-index-latency.sh <transcript.jsonl>...
#
# Copy transcripts out of a live state directory first (cp is fine); the
# tests copy them again before appending, and never write the paths given.
# Output is the build, equality and latency lines only; full logs stay in the
# directory printed at the end.
set -euo pipefail

if [[ $# -eq 0 || $1 == -h || $1 == --help ]]; then
	sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
	exit $(($# == 0))
fi
for path in "$@"; do
	[[ -f $path ]] || {
		echo "not a file: $path" >&2
		exit 1
	}
done

repo=$(cd "$(dirname "$0")/../.." && pwd)
logs=$(mktemp -d "${TMPDIR:-/tmp}/transcript-index-latency.XXXXXX")
paths=$(
	IFS=:
	echo "$*"
)
status=0

run() {
	local name=$1 package=$2 pattern=$3
	if ! (cd "$repo" && EVENER_TRANSCRIPT_INDEX_REAL=$paths go test "$package" -run "$pattern" -count=1 -v -timeout 60m) >"$logs/$name.log" 2>&1; then
		echo "FAIL: $name (see $logs/$name.log)"
		status=1
	fi
	grep -E 'build file=|project file=|items, every|latency |is not under' "$logs/$name.log" | sed 's/^ *[a-z_]*\.go:[0-9]*: //' || true
}

run equality ./internal/transcriptindex '^TestRealTranscriptWindowsEqualTheReference$'
run index ./internal/transcriptindex '^TestRealTranscriptLatency$'
run baseline ./server '^TestRealTranscriptInMemoryReadLatency$'

# durationNS converts a Go Duration.String() value (e.g. "12.345ms", "900µs",
# "1.5s") to nanoseconds. The p99s this gate compares are read latencies well
# under a second, so it only handles the single-unit form Duration.String()
# prints below a minute; a chained value ("1m2s") is unexpected here and fails
# loudly rather than silently parsing wrong.
durationNS() {
	local d=$1 value unit scale
	if [[ $d =~ ^([0-9.]+)(ns|µs|us|ms|s)$ ]]; then
		value=${BASH_REMATCH[1]}
		unit=${BASH_REMATCH[2]}
	else
		echo "unparsed-duration:$d"
		return 1
	fi
	case $unit in
	ns) scale=1 ;;
	us | µs) scale=1000 ;;
	ms) scale=1000000 ;;
	s) scale=1000000000 ;;
	esac
	awk -v v="$value" -v s="$scale" 'BEGIN { printf "%.0f\n", v * s }'
}

# relativeGate enforces the spec's relative criterion (docs/superpowers/specs/
# 2026-09-25-transcript-read-model-design.md, "Acceptance criteria"): an idle
# writer's index read is no worse than 2x today's full-page read right after a
# change notification (the "invalidated" baseline, not the warm "cached" one —
# a notified client re-reads once, it does not have a warm cache). indexLabel's
# p99 (from index.log) must be under 2x baselineLabel's p99 (from baseline.log),
# per transcript file.
relativeGate() {
	local indexLabel=$1 baselineLabel=$2
	while read -r file indexP99; do
		[ -n "$file" ] || continue
		baselineP99=$(awk -v f="$file" -v l="$baselineLabel" '$0 ~ ("latency " l " file=" f " ") { for (i=1;i<=NF;i++) if ($i ~ /^p99=/) { sub(/^p99=/, "", $i); print $i } }' "$logs/baseline.log" | tail -1)
		if [ -z "$baselineP99" ]; then
			echo "FAIL: relative gate $indexLabel/$baselineLabel: no $baselineLabel sample for file=$file"
			status=1
			continue
		fi
		indexNS=$(durationNS "$indexP99") || { echo "FAIL: relative gate: $indexNS"; status=1; continue; }
		baselineNS=$(durationNS "$baselineP99") || { echo "FAIL: relative gate: $baselineNS"; status=1; continue; }
		limitNS=$((baselineNS * 2))
		echo "relative $indexLabel/$baselineLabel file=$file index_p99=$indexP99 baseline_p99=$baselineP99 limit=2x"
		if [ "$indexNS" -ge "$limitNS" ]; then
			echo "FAIL: relative gate: $indexLabel p99 $indexP99 (file=$file) is not under 2x $baselineLabel p99 $baselineP99"
			status=1
		fi
	done < <(awk -v l="$indexLabel" '$0 ~ ("latency " l " file=") { file=""; p99=""; for (i=1;i<=NF;i++) { if ($i ~ /^file=/) { file=$i; sub(/^file=/, "", file) } if ($i ~ /^p99=/) { p99=$i; sub(/^p99=/, "", p99) } } if (file != "" && p99 != "") print file, p99 }' "$logs/index.log")
}

relativeGate index-idle baseline-invalidated
relativeGate index-page-idle baseline-page-invalidated

echo "full logs: $logs"
exit $status
