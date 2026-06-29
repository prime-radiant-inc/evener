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

# This is the SINGLE source of truth for every fuzz surface, consumed verbatim
# (via `--list`) by scripts/fuzz-coverage.sh, scripts/fuzz-triage.sh, and the
# static gap gate (cmd/serf-fuzzcov -gap-only). Each entry is
# "tag:module:package-relpath:name[:coverpkg[:focus]]":
#   tag       "native" — a testing.F target driven by `go test -fuzz`; or
#             "rapid"  — a Test* func driven by rapid.Check during ordinary
#             `go test -run`. Both kinds share this one registry so the consumers
#             have a single list instead of a parallel hardcoded rapid set.
#   module    the go.work module dir ("." or "agent"/"llm"/"auth").
#   package-relpath  the package relative to that module.
#   name      the FuzzXxx (native) or TestXxx (rapid) function name.
# The two optional trailing fields are consumed only by the coverage tooling
# (scripts/fuzz-coverage.sh + cmd/serf-fuzzcov), never by the campaign run below:
#   coverpkg  go test -coverpkg value for the coverage replay; defaults to the
#             package-relpath. Only FuzzToolArgsValidate overrides it, because its
#             real SUT is agent/internal/tool, not the agent root package.
#   focus     ";"-separated focus set — the decode/parse seam whose coverage % is
#             the surface's primary, drivable-to-100 metric. Each spec is a file
#             ("sse.go", relative to the SUT package dir) or a function
#             ("adapter.go#decodeStream"). Empty means "the whole SUT package".
TARGETS=(
	"native:llm:.:FuzzParseSSE::sse.go"
	"native:.:./appwire:FuzzMessageDecode::jsonrpc.go"
	"native:.:./appwire:FuzzMessageDecodeStructured::jsonrpc.go"
	"native:.:./appwire:FuzzMethodParams::"
	"native:.:./appwire:FuzzWireTypes::"
	"native:agent:.:FuzzToolArgsValidate:./internal/tool,.:internal/tool/definitions.go"
	"native:agent:./schema:FuzzSessionMetaRoundTrip::snapshot.go"
	"native:agent:./internal/jobstore:FuzzJobEventLogReplay::fold.go"
	"native:agent:.:FuzzTranscriptReplay::transcript_read.go"
	"native:.:./cmd/serf-hub:FuzzHubReplayCarryThrough::app_threadread.go#replayTurnToAgentTurn"
	"native:.:./cmd/serf-hub:FuzzHubReplayLiveVsReload::app_threadread.go#replayTurnToAgentTurn"
	"native:llm:./providers/openai:FuzzOpenAIResponsesMetamorphic::responses.go#decodeResponsesStream"
	"native:llm:./providers/openai:FuzzOpenAIChatCompletionsMetamorphic::chatcompletions.go#decodeChatCompletionsStream"
	"native:llm:./providers/anthropic:FuzzAnthropicStreamMetamorphic::adapter.go#decodeStream"
	"native:llm:./providers/google:FuzzGeminiStreamMetamorphic::adapter.go#decodeStream"
	"native:llm:./providers/openaicompat:FuzzOpenAICompatStreamMetamorphic::adapter.go#decodeStream"
	"native:.:./cmd/serf-hub:FuzzWebHandler::web.go"
	# 8.2 — codex-compat item + config decode targets.
	"native:.:./appwire:FuzzCodexItemDecode::"
	"native:llm:./providercfg:FuzzProvidersTOMLLoad::load.go"
	"native:.:./cmd/serf-hub/internal/launchconfig:FuzzLaunchConfigDecode::io.go"
	"native:agent:./plugin:FuzzPluginManifestParse::plugin.go#ParseManifest"
	"native:.:./internal/credentials:FuzzCredentialsStoreDecode::store.go"
	"native:agent:./plugin:FuzzPluginLoad::plugin.go#Load"
	# Phase 7 Wave 1 — a decode/parse target for every remaining package.
	# Lane A1 (agent module)
	"native:agent:./transcript:FuzzTranscriptWriterRoundTrip::transcript.go"
	"native:agent:./task:FuzzTaskStoreLoad::task_store.go#Load"
	"native:agent:./doctor:FuzzDoctorLoadTranscript::transcript.go#loadTranscript"
	"native:agent:./mcpconfig:FuzzMCPConfigLoad::config.go"
	"native:agent:./provider:FuzzResolveProfileFromConfig::resolve.go"
	"native:agent:./internal/atif:FuzzATIFConvert::atif.go"
	"native:agent:./internal/sessionlog:FuzzSessionLogLoad::sessionlog.go"
	"native:agent:./internal/contextmgr:FuzzCheckpointExtract::checkpoint_format.go"
	"native:agent:./internal/hooks:FuzzParseHookOutput::hooks.go#parseHookOutput"
	"native:agent:./internal/mcp:FuzzMCPSchemaToParams::manager.go#mcpSchemaToParams"
	"native:agent:./internal/frontmatter:FuzzFrontmatterParse::frontmatter.go"
	# Lane A2 (root module: protocol/server/hub-internal)
	"native:.:./frontmatter:FuzzParse::frontmatter.go"
	"native:.:./hubapi:FuzzParseRef::refs.go"
	"native:.:./rendezvous:FuzzList::rendezvous.go#List"
	"native:.:./server:FuzzAppTurnsFromNotifications::appwire_turns.go#appTurnsFromNotifications"
	"native:.:./internal/appserver:FuzzHandleMessage::server.go#HandleMessage"
	"native:.:./internal/appprojector:FuzzProject::appwire_projection.go#Project"
	"native:.:./internal/apptranscript:FuzzProjectTurn::apptranscript.go#ProjectTurn"
	"native:.:./cmd/serf-hub/internal/appsource:FuzzMapCodexTurn::codex_mapping.go#mapCodexTurn"
	"native:.:./cmd/serf-hub/internal/codexlaunch:FuzzParseCodexEndpoint::codex_launch.go#ParseCodexEndpoint"
	"native:.:./cmd/serf-hub/internal/hubcore:FuzzBuildTree::tree.go#BuildTreeAt"
	# Lane A3 (root module: CLI/TUI glue)
	"native:.:./cmd/serf:FuzzRunFlagParse::main.go#newRunFlagSet"
	"native:.:./cmd/llmcall:FuzzLLMCallParsers::main.go#parseMetadata"
	"native:.:./cmdutil:FuzzCmdutilParsers::cmdutil.go#ParseModelRef"
	"native:.:./cmd/serf-tui:FuzzApplyHubNotification::hub_notifications.go#applyHubNotification"
	"native:.:./cmd/serf-tui/internal/clipboard:FuzzNormalizePastedPath::clipboard_paste.go#NormalizePastedPath"
	"native:.:./cmd/serf-tui/internal/hubstart:FuzzParseStartup::hub_start.go#ParseTUIStartupOptions"
	"native:.:./cmd/serf-tui/internal/launchconfig:FuzzApplyEdit::launch_settings_panel.go#applyEdit"
	"native:.:./cmd/serf-tui/internal/msgrender:FuzzRenderToolCall::tool_renderers.go#toolArgsFromJSON"
	"native:.:./cmd/serf-tui/internal/toolsummary:FuzzSummarizeTool::tool_summary.go"
	"native:.:./cmd/serf-tui/internal/transcript:FuzzApplyThreadItem::reducer.go#subagentRunFromToolItem"
	"native:.:./cmd/serf-tui/internal/tuitheme:FuzzColorParsing::terminal_bg.go#relativeLuminanceHex"
	# Lane A4 (llm + auth modules)
	"native:llm:./providers/internal/openaichat:FuzzToolArgumentsString::openaichat.go#ToolArgumentsString"
	"native:llm:./providers/internal/openaichat:FuzzParseChatUsage::openaichat.go#ParseChatUsage"
	"native:llm:./providers/kimi:FuzzCountInputTokensResponse::adapter.go#CountInputTokens"
	"native:auth:./openai:FuzzParseIDTokenClaims::claims.go#ParseIDTokenClaims"
	"native:auth:./openai:FuzzTokenEndpointResponse::tokens.go"
	# Phase 7 Wave 3 — behavioral API fuzzing (under the B0 sandbox) + tool execution.
	# B1/B2 (hub)
	"native:.:./cmd/serf-hub:FuzzAppWireDispatch::app_rpc.go#newHubAppServer"
	"native:.:./cmd/serf-hub:FuzzWebMutatingHandler::web_spawn.go#handleApiSpawn"
	# B3 (tool execution via DenyEnv)
	"native:agent:.:FuzzToolExecution:./internal/tool,.:internal/tool/registry.go#ExecuteCall"
	# B4 (provider request-build / non-stream Complete / error mapping)
	"native:llm:./providers/anthropic:FuzzAnthropicRequestBuild::request.go#buildRequestBody"
	"native:llm:./providers/anthropic:FuzzAnthropicComplete::adapter.go#Complete"
	"native:llm:./providers/google:FuzzGoogleRequestBuild::request.go#buildRequestBody"
	"native:llm:./providers/google:FuzzGoogleComplete::adapter.go#Complete"
	"native:llm:./providers/openaicompat:FuzzOpenAICompatRequestBuild::request.go#buildRequestBody"
	"native:llm:./providers/openaicompat:FuzzOpenAICompatComplete::adapter.go#completeViaChatCompletions"
	"native:llm:./providers/openai:FuzzOpenAIResponsesRequestBuild::responses.go#buildRequestBody"
	"native:llm:./providers/openai:FuzzOpenAIChatCompletionsRequestBuild::chatcompletions.go#buildChatCompletionsBody"
	"native:llm:./providers/openai:FuzzOpenAIResponsesDecode::responses.go#fromResponses"
	"native:llm:.:FuzzErrorFromHTTPStatus::errors.go#errorFromHTTPStatus"
	# Rapid promoter surfaces — Test* funcs driven by rapid.Check during ordinary
	# `go test -run` (not `go test -fuzz`). They share this registry so the triage
	# tool no longer needs a parallel hardcoded list.
	# 8.2b cross-provider differential — one canonical logical response encoded to
	# each provider's wire format, decoded via the real adapters, asserted
	# equivalent. coverpkg spans the 4 real decoders it exercises; no single focus.
	"native:llm:./providers/difftest:FuzzCrossProviderDifferential:./providers/anthropic,./providers/google,./providers/openai,./providers/openaicompat:"
	"rapid:agent:.:TestToolArgsSchemaFuzz"
	"rapid:agent:.:TestLifecycleSeqFuzz"
	"rapid:agent:./internal/jobstore:TestJobstoreSeqFuzz"
	"rapid:.:./internal/appserver:TestRouterSeqFuzz"
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
	IFS=: read -r tag module pkg name cover focus <<<"$t"
	want "$module:$name" || continue
	case "$tag" in
		native)
			echo "=== fuzzing $module:$name for $duration ==="
			# -tags serffuzz makes the internal/invariant assertions live so a
			# tripped invariant is found as a crasher (see docs/fuzzing.md).
			# run-capped.sh gives each target its own memory ceiling so a leaky
			# search OOMs that one target's scope, never the host. The targets run
			# sequentially, so a per-target cap is the tightest safe bound.
			( cd "$repo_root/$module" && "$repo_root/scripts/run-capped.sh" go test -tags serffuzz -run '^$' -fuzz "^${name}\$" -fuzztime "$duration" "$pkg" ) || fail=1
			;;
		rapid)
			# rapid surfaces are property checks driven by `go test -run`; the
			# search depth is governed by -rapid.checks, not -fuzztime, so the
			# --time budget does not apply to them.
			echo "=== rapid $module:$name ==="
			( cd "$repo_root/$module" && "$repo_root/scripts/run-capped.sh" go test -tags serffuzz -run "^${name}\$" -count=1 "$pkg" ) || fail=1
			;;
		*)
			echo "run-fuzz: unknown tag '$tag' in entry '$t'" >&2; fail=1
			;;
	esac
done

exit "$fail"
