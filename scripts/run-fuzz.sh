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

# Each entry is "module:package-relpath:FuzzName[:coverpkg[:focus]]". module is
# the go.work module dir; package-relpath is relative to that module. The two
# optional trailing fields are consumed only by the coverage tooling
# (scripts/fuzz-coverage.sh + cmd/serf-fuzzcov), never by the campaign run below:
#   coverpkg  go test -coverpkg value for the coverage replay; defaults to the
#             package-relpath. Only FuzzToolArgsValidate overrides it, because its
#             real SUT is agent/internal/tool, not the agent root package.
#   focus     ";"-separated focus set — the decode/parse seam whose coverage % is
#             the surface's primary, drivable-to-100 metric. Each spec is a file
#             ("sse.go", relative to the SUT package dir) or a function
#             ("adapter.go#decodeStream"). Empty means "the whole SUT package".
TARGETS=(
	"llm:.:FuzzParseSSE::sse.go"
	".:./appwire:FuzzMessageDecode::jsonrpc.go"
	".:./appwire:FuzzMethodParams::protocol.go"
	".:./appwire:FuzzWireTypes::"
	"agent:.:FuzzToolArgsValidate:./internal/tool,.:internal/tool/definitions.go"
	"agent:./schema:FuzzSessionMetaRoundTrip::snapshot.go"
	"agent:./internal/jobstore:FuzzJobEventLogReplay::fold.go"
	"agent:.:FuzzTranscriptReplay::transcript_read.go"
	".:./cmd/serf-hub:FuzzHubReplayCarryThrough::app_threadread.go#replayTurnToAgentTurn"
	".:./cmd/serf-hub:FuzzHubReplayLiveVsReload::app_threadread.go#replayTurnToAgentTurn"
	"llm:./providers/openai:FuzzOpenAIResponsesMetamorphic::responses.go#decodeResponsesStream"
	"llm:./providers/openai:FuzzOpenAIChatCompletionsMetamorphic::chatcompletions.go#decodeChatCompletionsStream"
	"llm:./providers/anthropic:FuzzAnthropicStreamMetamorphic::adapter.go#decodeStream"
	"llm:./providers/google:FuzzGeminiStreamMetamorphic::adapter.go#decodeStream"
	"llm:./providers/openaicompat:FuzzOpenAICompatStreamMetamorphic::adapter.go#decodeStream"
	".:./cmd/serf-hub:FuzzWebHandler::web.go"
	# 8.2 — codex-compat item + config decode targets.
	".:./appwire:FuzzCodexItemDecode::types.go"
	"llm:./providercfg:FuzzProvidersTOMLLoad::load.go"
	".:./cmd/serf-hub/internal/launchconfig:FuzzLaunchConfigDecode::io.go"
	"agent:./plugin:FuzzPluginManifestParse::plugin.go#ParseManifest"
	".:./internal/credentials:FuzzCredentialsStoreDecode::store.go"
	"agent:./plugin:FuzzPluginLoad::plugin.go#Load"
)

duration="60s"
declare -a only=()
while [ $# -gt 0 ]; do
	case "$1" in
		--time) duration="$2"; shift 2 ;;
		--time=*) duration="${1#*=}"; shift ;;
		--list) printf '%s\n' "${TARGETS[@]}"; exit 0 ;;
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
	IFS=: read -r module pkg name cover focus <<<"$t"
	want "$module:$name" || continue
	echo "=== fuzzing $module:$name for $duration ==="
	( cd "$repo_root/$module" && go test -run '^$' -fuzz "^${name}\$" -fuzztime "$duration" "$pkg" ) || fail=1
done

exit "$fail"
