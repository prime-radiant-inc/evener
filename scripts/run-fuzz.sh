#!/usr/bin/env bash
# run-fuzz.sh — run each serf fuzz target's coverage-guided search for a bounded
# time. This is the NIGHTLY/manual campaign, not the gate: `make fuzz` (seed
# corpus only) is what runs in CI. A failing input found here is auto-saved by
# the Go toolchain to the target's testdata/fuzz/<FuzzName>/ directory, where it
# becomes a permanent regression seed that `make fuzz` replays forever.
#
# Usage:
#   scripts/run-fuzz.sh [--time DURATION] [target ...]
#     --time DURATION   per-target fuzz budget (default 60s; any go -fuzztime value)
#     target            one or more "module:FuzzName" to restrict the run;
#                       default is every known target below.
#
# Examples:
#   scripts/run-fuzz.sh                       # all targets, 60s each
#   scripts/run-fuzz.sh --time 5m            # all targets, 5 minutes each
#   scripts/run-fuzz.sh llm:FuzzParseSSE     # just the SSE target, 60s
set -uo pipefail

# Each entry is "module:package-relpath:FuzzName". module is the go.work module
# dir; package-relpath is relative to that module.
TARGETS=(
	"llm:.:FuzzParseSSE"
	".:./appwire:FuzzMessageDecode"
	".:./appwire:FuzzMethodParams"
	"agent:.:FuzzToolArgsValidate"
	"agent:./schema:FuzzSessionMetaRoundTrip"
	"agent:./internal/jobstore:FuzzJobEventLogReplay"
	"llm:./providers/openai:FuzzOpenAIResponsesMetamorphic"
	"llm:./providers/openai:FuzzOpenAIChatCompletionsMetamorphic"
	"llm:./providers/anthropic:FuzzAnthropicStreamMetamorphic"
	"llm:./providers/google:FuzzGeminiStreamMetamorphic"
	"llm:./providers/openaicompat:FuzzOpenAICompatStreamMetamorphic"
	".:./cmd/serf-hub:FuzzWebHandler"
)

duration="60s"
declare -a only=()
while [ $# -gt 0 ]; do
	case "$1" in
		--time) duration="$2"; shift 2 ;;
		--time=*) duration="${1#*=}"; shift ;;
		-h|--help) sed -n '2,20p' "$0"; exit 0 ;;
		*) only+=("$1"); shift ;;
	esac
done

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
fail=0

want() {
	[ ${#only[@]} -eq 0 ] && return 0
	local entry="$1" o
	for o in "${only[@]}"; do
		[ "$o" = "$entry" ] && return 0
	done
	return 1
}

for t in "${TARGETS[@]}"; do
	IFS=: read -r module pkg name <<<"$t"
	want "$module:$name" || continue
	echo "=== fuzzing $module:$name for $duration ==="
	( cd "$repo_root/$module" && go test -run '^$' -fuzz "^${name}\$" -fuzztime "$duration" "$pkg" ) || fail=1
done

exit "$fail"
