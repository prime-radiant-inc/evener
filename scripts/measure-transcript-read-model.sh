#!/usr/bin/env bash
# measure-transcript-read-model.sh — the transcript read model's acceptance
# measurements (docs/superpowers/specs/2026-09-25-transcript-read-model-design.md,
# "Acceptance criteria"; docs/superpowers/plans/2026-09-25-transcript-read-model-phase3.md,
# Task 19), run against real transcripts and a real session.
#
# Runs two opt-in server-package tests:
#   - TestRealSessionRetainedHistoryMemory: replays every *.transcript.jsonl in
#     session-dir into its own thread history on one daemon, then reports
#     retained heap, the index cache's open handles, the notice budget's
#     bytes and the largest per-thread notice ring — once for the whole
#     directory, once for its 20 smallest files, so growth with history size
#     is visible. Use a copy of a real, long-lived session directory (root
#     plus delegates), never a live one.
#   - TestRealTranscriptHistoryReadLatency: for each transcript given, 200
#     samples of the full thread/read handler path (capture, latest, regroup,
#     JSON encoding) at the default page size, idle and while a goroutine
#     appends one entry every 100 ms. Reports p50/p99 against the spec's
#     latency gate.
#
# Usage:
#   scripts/measure-transcript-read-model.sh <session-dir> <transcript.jsonl>...
#
# session-dir is a directory of *.transcript.jsonl files (a real session
# copy: the root and every delegate) for the memory measurement.
# Each transcript.jsonl is a real transcript file for the latency
# measurement (the spec measures the ~95 MB root and ~134 MB coordinator
# transcripts; pass as many as you like).
#
# Copy the session directory and the transcripts out of a live state
# directory first (cp -r is fine): both tests copy what they are given again
# before writing to it, and never touch the paths given here. Output is the
# reported numbers and PASS/FAIL only; full logs land in the directory
# printed at the end.
set -euo pipefail

if [[ $# -eq 0 || $1 == -h || $1 == --help ]]; then
	sed -n '2,29p' "$0" | sed 's/^# \{0,1\}//'
	exit $(($# == 0))
fi

session_dir=$1
shift
if [[ ! -d $session_dir ]]; then
	echo "not a directory: $session_dir" >&2
	exit 1
fi
if [[ $# -eq 0 ]]; then
	echo "give at least one transcript.jsonl for the latency measurement" >&2
	exit 1
fi
for path in "$@"; do
	[[ -f $path ]] || {
		echo "not a file: $path" >&2
		exit 1
	}
done

repo=$(cd "$(dirname "$0")/.." && pwd)
logs=$(mktemp -d "${TMPDIR:-/tmp}/transcript-read-model.XXXXXX")
transcripts=$(
	IFS=:
	echo "$*"
)
status=0

run() {
	local name=$1 pattern=$2
	shift 2
	if ! (cd "$repo" && env "$@" go test ./server -run "$pattern" -count=1 -v -timeout 60m) >"$logs/$name.log" 2>&1; then
		echo "FAIL: $name (see $logs/$name.log)"
		status=1
	fi
	grep -E 'replaying |memory |latency |is not under' "$logs/$name.log" | sed 's/^ *[a-z_]*\.go:[0-9]*: //' || true
}

run memory '^TestRealSessionRetainedHistoryMemory$' "EVENER_TRM_SESSION_DIR=$session_dir"
run latency '^TestRealTranscriptHistoryReadLatency$' "EVENER_TRANSCRIPT_INDEX_REAL=$transcripts"

if [[ $status -eq 0 ]]; then
	echo PASS
else
	echo FAIL
fi
echo "full logs: $logs"
exit $status
