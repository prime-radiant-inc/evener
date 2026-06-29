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
	# Phase 7 Wave 1 — a decode/parse target for every remaining package.
	# Lane A1 (agent module)
	"agent:./transcript:FuzzTranscriptWriterRoundTrip::transcript.go"
	"agent:./task:FuzzTaskStoreLoad::task_store.go#Load"
	"agent:./doctor:FuzzDoctorLoadTranscript::transcript.go#loadTranscript"
	"agent:./mcpconfig:FuzzMCPConfigLoad::config.go"
	"agent:./provider:FuzzResolveProfileFromConfig::resolve.go"
	"agent:./internal/atif:FuzzATIFConvert::atif.go"
	"agent:./internal/sessionlog:FuzzSessionLogLoad::sessionlog.go"
	"agent:./internal/contextmgr:FuzzCheckpointExtract::checkpoint_format.go"
	"agent:./internal/hooks:FuzzParseHookOutput::hooks.go#parseHookOutput"
	"agent:./internal/mcp:FuzzMCPSchemaToParams::manager.go#mcpSchemaToParams"
	"agent:./internal/frontmatter:FuzzFrontmatterParse::frontmatter.go"
	# Lane A2 (root module: protocol/server/hub-internal)
	".:./frontmatter:FuzzParse::frontmatter.go"
	".:./hubapi:FuzzParseRef::refs.go"
	".:./rendezvous:FuzzList::rendezvous.go#List"
	".:./server:FuzzAppTurnsFromNotifications::appwire_turns.go#appTurnsFromNotifications"
	".:./internal/appserver:FuzzHandleMessage::server.go#HandleMessage"
	".:./internal/appprojector:FuzzProject::appwire_projection.go#Project"
	".:./internal/apptranscript:FuzzProjectTurn::apptranscript.go#ProjectTurn"
	".:./cmd/serf-hub/internal/appsource:FuzzMapCodexTurn::codex_mapping.go#mapCodexTurn"
	".:./cmd/serf-hub/internal/codexlaunch:FuzzParseCodexEndpoint::codex_launch.go#ParseCodexEndpoint"
	".:./cmd/serf-hub/internal/hubcore:FuzzBuildTree::tree.go#BuildTreeAt"
	# Lane A3 (root module: CLI/TUI glue)
	".:./cmd/serf:FuzzRunFlagParse::main.go#newRunFlagSet"
	".:./cmd/llmcall:FuzzLLMCallParsers::main.go#parseMetadata"
	".:./cmdutil:FuzzCmdutilParsers::cmdutil.go#ParseModelRef"
	".:./cmd/serf-tui:FuzzApplyHubNotification::hub_notifications.go#applyHubNotification"
	".:./cmd/serf-tui/internal/clipboard:FuzzNormalizePastedPath::clipboard_paste.go#NormalizePastedPath"
	".:./cmd/serf-tui/internal/hubstart:FuzzParseStartup::hub_start.go#ParseTUIStartupOptions"
	".:./cmd/serf-tui/internal/launchconfig:FuzzApplyEdit::launch_settings_panel.go#applyEdit"
	".:./cmd/serf-tui/internal/msgrender:FuzzRenderToolCall::tool_renderers.go#toolArgsFromJSON"
	".:./cmd/serf-tui/internal/toolsummary:FuzzSummarizeTool::tool_summary.go"
	".:./cmd/serf-tui/internal/transcript:FuzzApplyThreadItem::reducer.go#subagentRunFromToolItem"
	".:./cmd/serf-tui/internal/tuitheme:FuzzColorParsing::terminal_bg.go#relativeLuminanceHex"
	# Lane A4 (llm + auth modules)
	"llm:./providers/internal/openaichat:FuzzToolArgumentsString::openaichat.go#ToolArgumentsString"
	"llm:./providers/internal/openaichat:FuzzParseChatUsage::openaichat.go#ParseChatUsage"
	"llm:./providers/kimi:FuzzCountInputTokensResponse::adapter.go#CountInputTokens"
	"auth:./openai:FuzzParseIDTokenClaims::claims.go#ParseIDTokenClaims"
	"auth:./openai:FuzzTokenEndpointResponse::tokens.go"
	# Phase 7 Wave 3 — behavioral API fuzzing (under the B0 sandbox) + tool execution.
	# B1/B2 (hub)
	".:./cmd/serf-hub:FuzzAppWireDispatch::app_rpc.go#newHubAppServer"
	".:./cmd/serf-hub:FuzzWebMutatingHandler::web.go#handleApiSpawn"
	# B3 (tool execution via DenyEnv)
	"agent:.:FuzzToolExecution:./internal/tool,.:internal/tool/registry.go#ExecuteCall"
	# B4 (provider request-build / non-stream Complete / error mapping)
	"llm:./providers/anthropic:FuzzAnthropicRequestBuild::request.go#buildRequestBody"
	"llm:./providers/anthropic:FuzzAnthropicComplete::adapter.go#Complete"
	"llm:./providers/google:FuzzGoogleRequestBuild::request.go#buildRequestBody"
	"llm:./providers/google:FuzzGoogleComplete::adapter.go#Complete"
	"llm:./providers/openaicompat:FuzzOpenAICompatRequestBuild::request.go#buildRequestBody"
	"llm:./providers/openaicompat:FuzzOpenAICompatComplete::adapter.go#completeViaChatCompletions"
	"llm:./providers/openai:FuzzOpenAIResponsesRequestBuild::responses.go#buildRequestBody"
	"llm:./providers/openai:FuzzOpenAIChatCompletionsRequestBuild::chatcompletions.go#buildChatCompletionsBody"
	"llm:./providers/openai:FuzzOpenAIResponsesDecode::responses.go#fromResponses"
	"llm:.:FuzzErrorFromHTTPStatus::errors.go#errorFromHTTPStatus"
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
