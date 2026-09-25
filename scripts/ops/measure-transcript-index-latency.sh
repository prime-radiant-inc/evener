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
echo "full logs: $logs"
exit $status
