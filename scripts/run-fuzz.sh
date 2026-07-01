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
	"native:.:./appwire:FuzzTurnPagingEquivalence::paging.go"
	"native:.:./appwire:FuzzMethodParams::"
	"native:.:./appwire:FuzzWireTypes::"
	"native:agent:.:FuzzToolArgsValidate:./internal/tool,.:internal/tool/definitions.go"
	"native:agent:./schema:FuzzSessionMetaRoundTrip::snapshot.go"
	"native:agent:./internal/jobstore:FuzzJobEventLogReplay::fold.go"
	"native:agent:.:FuzzTranscriptReplay::transcript_read.go"
	"native:agent:.:FuzzTranscriptReplayStructured::transcript_read.go"
	"native:agent:.:FuzzTranscriptReadersAgree::transcript_read.go"
	"native:agent:.:FuzzLineWindowExtractors::job_output_digest.go"
	"native:.:./cmd/serf-hub:FuzzHubReplayCarryThrough::app_threadread.go#replayTurnToAgentTurn"
	"native:.:./cmd/serf-hub:FuzzHubReplayLiveVsReload::app_threadread.go#replayTurnToAgentTurn"
	"native:.:./cmd/serf-hub:FuzzHubReplayLiveVsReloadStructured::app_threadread.go#replayTurnToAgentTurn"
	"native:llm:./providers/openai:FuzzOpenAIResponsesMetamorphic::responses.go#decodeResponsesStream"
	"native:llm:./providers/openai:FuzzOpenAIResponsesStructured::responses.go#decodeResponsesStream"
	"native:llm:./providers/openai:FuzzOpenAIChatCompletionsMetamorphic::chatcompletions.go#decodeChatCompletionsStream"
	"native:llm:./providers/anthropic:FuzzAnthropicStreamMetamorphic::adapter.go#decodeStream"
	"native:llm:./providers/anthropic:FuzzAnthropicStreamStructured::adapter.go#decodeStream"
	"native:llm:./providers/google:FuzzGeminiStreamMetamorphic::adapter.go#decodeStream"
	"native:llm:./providers/google:FuzzGeminiStreamStructured::adapter.go#decodeStream"
	"native:llm:./providers/openaicompat:FuzzOpenAICompatStreamMetamorphic::adapter.go#decodeStream"
	"native:llm:./providers/openaicompat:FuzzOpenAICompatStreamStructured::adapter.go#decodeStream"
	"native:.:./cmd/serf-hub:FuzzWebHandler::web.go"
	# 8.2 — codex-compat item + config decode targets.
	"native:.:./appwire:FuzzCodexItemDecode::"
	"native:llm:./providercfg:FuzzProvidersTOMLLoad::load.go"
	"native:.:./cmd/serf-hub/internal/launchconfig:FuzzLaunchConfigDecode::io.go"
	"native:.:./cmd/serf-hub/internal/launchconfig:FuzzLaunchConfigResolve::resolver.go#Resolve"
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
	"native:agent:./provider:FuzzApOpenRouterAnthropicResolve::profile.go#resolveOpenRouterAnthropicWebSearch"
	"native:agent:./skill:FuzzApSkillFileParse::skills.go#parseSkillFile"
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
	"native:auth:./openai:FuzzResolveRuntimeCredentials::service.go#ResolveRuntimeCredentials"
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
	"native:llm:.:FuzzClassify::classify.go#Classify"
	"native:llm:.:FuzzAPILogBuilders::apilog.go#BuildAPILogRequest"
	"native:llm:.:FuzzClientDispatch::client.go#Complete"
	"native:llm:./providers/openai:FuzzOpenAICompleteRoundTrip::adapter.go#Complete"
	"native:llm:./providers/openai:FuzzOpenAIStreamRoundTrip::adapter.go#Stream"
	"native:llm:./providers/anthropic:FuzzAnthropicCompleteRoundTrip::adapter.go#Complete"
	"native:llm:./providers/anthropic:FuzzAnthropicStreamRoundTrip::adapter.go#Stream"
	"native:llm:./providers/google:FuzzGoogleCompleteRoundTrip::adapter.go#Complete"
	"native:llm:./providers/google:FuzzGoogleStreamRoundTrip::adapter.go#Stream"
	"native:llm:./providers/openaicompat:FuzzOpenaicompatCompleteRoundTrip::adapter.go#Complete"
	"native:llm:./providers/openaicompat:FuzzOpenaicompatStreamRoundTrip::adapter.go#Stream"
	"native:agent:./internal/jobstore:FuzzStorePersistence::store.go#openFs"
	"native:agent:./internal/jobstore:FuzzStoreFaultTolerance::store.go#rollbackAppendLocked"
	"native:agent:.:FuzzDgfzSendDelegateMessage::job_delegate.go#sendDelegateMessage"
	"native:agent:.:FuzzMsfzCallModelWithFallback::session_model_call.go#callModelWithFallback"
	"native:agent:.:FuzzMsfzConsumeModelStream::session_stream.go#consumeModelStream"
	"native:agent:.:FuzzMsfzContinuationAnchorPlanning::session_model_call.go#applyResponsesContinuationAnchorPlanning"
	"native:agent:.:FuzzSafz_DriveNotificationTurn::subagents.go#driveSubagentNotificationTurn"
	"native:agent:.:FuzzSafz_PrepareSubagentRun::subagents.go#prepareSubagentRun"
	"native:agent:.:FuzzRfzForkSession::fork.go#ForkSession"
	"native:agent:.:FuzzRfzRestoreSessionFromMeta::session_init.go#RestoreSessionFromMetaWithConfig"
	"native:agent:.:FuzzShfz_RunShellForeground::job_shell.go#runShell"
	"native:agent:.:FuzzShfz_RunShellModes::job_shell.go#runShell"
	"native:agent:./internal/tool:FuzzApatchApplyPatch::apply_patch.go#apply"
	"native:agent:./internal/tool:FuzzApatchParseV4APatchLines::apply_patch.go#parseV4APatchLines"
	"native:agent:./internal/contextmgr:FuzzCtxmgrCheckpointData::context_manager.go#collectCheckpointData"
	"native:agent:./internal/contextmgr:FuzzCtxmgrCheckpointPredManageContext::strategy_checkpoint_pred.go#ManageContext"
	"native:agent:./internal/contextmgr:FuzzCtxmgrSessionLogManageContext::strategy_session_log.go#ManageContext"
	"native:agent:./internal/contextmgr:FuzzCtxmgrSummarizeSteered::context_manager.go#summarizeWithLLMSteered"
	"native:agent:./doctor:FuzzDoctorRenderAPILog::apilog.go#RenderAPILog"
	"native:agent:./doctor:FuzzDoctorRenderWatches::watches.go#RenderWatches"
	"native:agent:./execenv:FuzzEgrepGrepNative::local.go#grepNative"
	"native:agent:.:FuzzJobtoolsExec::session_tools_jobs.go#jobReadOutputTool"
	"native:agent:.:FuzzJobtoolsFormat::session_tools_jobs.go#formatJobList"
	"native:agent:.:FuzzRawLinesForRange::transcript_render.go#rawLinesForRange"
	"native:agent:.:FuzzResolveTranscript::transcript_lookup.go#resolveTranscript"
	"native:agent:.:FuzzToolInputSummary::transcript_render.go#toolInputSummary"
	"native:agent:.:FuzzWatchdelDelegateResume::job_delegate.go#assessDelegateResumability"
	"native:agent:.:FuzzWatchdelWatchOps::job_watch.go#configureWatch"
	"native:llm:.:FuzzGenerateCore::generate.go#Generate"
	"native:llm:.:FuzzTryParsePartialJSON::generate_object.go#tryParsePartialJSON"
	"native:llm:./providers/openaicompat:FuzzRescueClaudeXMLArgs::rescue.go#rescueClaudeXMLArgs"
	"native:llm:./providers/openai:FuzzOpenaiListModels::models.go#ListModels"
	"native:llm:./providers/openaicompat:FuzzOpenaicompatListModels::models.go#ListModels"
	"native:llm:./providers/google:FuzzGoogleListModels::models.go#ListModels"
	"native:llm:./providers/anthropic:FuzzAnthropicListModels::models.go#ListModels"
	"native:llm:./providers/ollama:FuzzOllamaListModels::adapter.go#ListModels"
	"native:llm:.:FuzzCountInputTokensDispatch::token_count.go#CountInputTokens"
	"native:llm:./providers/openai:FuzzOpenAICountInputTokensRoundTrip::token_count.go#CountInputTokens"
	"native:llm:./providers/anthropic:FuzzAnthropicCountInputTokensRoundTrip::adapter.go#CountInputTokens"
	"native:llm:./providers/google:FuzzGoogleCountInputTokensRoundTrip::adapter.go#CountInputTokens"
	"native:llm:.:FuzzRetryDelay::retry_util.go#retryDelay"
	"native:llm:.:FuzzRetry::retry_util.go#Retry"
	"native:llm:.:FuzzRetryStream::stream_retry.go#RetryStream"
	"native:llm:./providers/anthropic:FuzzAreqClampEffort::response.go#clampEffort"
	"native:llm:./providers/anthropic:FuzzAreqDecodeStreamGrammar::adapter.go#decodeStream"
	"native:llm:./providers/anthropic:FuzzAreqParseUsage::response.go#parseUsage"
	"native:llm:./providers/anthropic:FuzzAreqToAnthropicMessages::request.go#toAnthropicMessages"
	"native:llm:.:Fuzz_lcfg_BuildAPILogRequest::apilog.go#BuildAPILogRequest"
	"native:llm:.:Fuzz_lcfg_ContinuationSecret::continuation_secret.go#LoadOrCreateContinuationSecret"
	"native:llm:.:Fuzz_lcfg_GetPrice::pricing.go#GetPrice"
	"native:llm:.:Fuzz_lcfg_Kind::errorkind.go#Kind"
	"native:llm:.:Fuzz_lcfg_NewFromEnv::env_registry.go#NewFromEnv"
	"native:llm:.:Fuzz_lcfg_NewFromProviders::providers_config.go#newFromProviders"
	"native:llm:./providers/google:FuzzMiscGoogleStreamRoundTrip::adapter.go#Stream"
	"native:llm:./providers/ollama:FuzzMiscOllamaNormalizeHost::adapter.go#normalizeHost"
	"native:llm:./providers/openaicompat:FuzzMiscOpenAICompatBuilder::request.go#buildRequestBody"
	"native:llm:./providers/openaicompat:FuzzMiscOpenAICompatMessages::request.go#toChatMessages"
	"native:llm:./providercfg:FuzzMiscProviderCfgUpsert::mutate.go#Upsert"
	"native:llm:./providers/openai:FuzzOresp_Builders::responses.go#buildRequestBody"
	"native:llm:./providers/openai:FuzzOresp_CompleteFallback::adapter.go#Complete"
	"native:llm:./providers/openai:FuzzOresp_NewForInstance::adapter.go#NewForInstance"
	"native:llm:./providers/openai:FuzzNewForInstanceOAuth::adapter.go#NewForInstance"
	"native:llm:./providers/openai:FuzzOresp_StreamChat::chatcompletions.go#streamViaChatCompletions"
	"native:llm:./providers/openai:FuzzOresp_StreamResponses::responses.go#streamResponses"
	"native:agent:./internal/sessionlog:FuzzSessionLogPersistence::sessionlog.go#appendToDisk"
	"native:llm:./providercfg:FuzzProvidersTOMLPersistence::mutate.go#WriteFile"
	"native:.:./internal/credentials:FuzzCredentialsStorePersistence::store.go#save"
	"native:.:./cmd/serf-hub/internal/launchconfig:FuzzLaunchConfigPersistence::io.go#saveLayerFS"
	"native:agent:./transcript:FuzzTranscriptWriterPersistence::transcript.go#newWriterFS"
	"native:agent:./schema:FuzzSessionMetaPersistence::snapshot.go#saveSessionMetaFS"
	"native:.:./rendezvous:FuzzRendezvousPersistence::rendezvous.go#writeFS"
	"native:.:./cmd/serf-hub/internal/hubcore:FuzzHubcorePersistFS::past.go#chmodSQLiteIndexFilesFS"
	"native:agent:./execenv:FuzzEditFile::local.go#EditFile"
	"native:agent:./execenv:FuzzEditFileDifferential::local.go#EditFile"
	"native:agent:./execenv:FuzzReadFileWindow::local.go#ReadFile"
	"native:agent:./execenv:FuzzDetectImageFormat::local.go#detectImageFormat"
	"native:agent:./task:FuzzTaskStorePersistence::task_store.go#save;task_store.go#Load"
	"native:llm:.:FuzzAPILogWrite::apilog.go#write"
	"native:llm:.:FuzzClientCapabilities::client.go#BehaviorTagOf"
	"native:llm:.:FuzzTokenEstimators::token_count.go#estimateImageTokens"
	"native:llm:.:FuzzEstimateInputTokens::token_count.go#estimateMessagesInputTokens"
	"native:llm:.:FuzzNormalizeFinishReason::types.go#normalizeFinish"
	"native:llm:.:FuzzReasoningEffort::types.go#ClampReasoningEffort"
	"native:llm:.:FuzzUsageAdd::types.go#Add"
	"native:llm:.:FuzzParseRateLimitHeaders::ratelimit.go#ParseRateLimitHeaders"
	"native:llm:.:FuzzParseLiteLLMCatalog::model_catalog.go#parseLiteLLMCatalog"
	"native:llm:.:FuzzStreamAccumulator::stream_accumulator.go#Process"
	"native:llm:.:FuzzResponsesContinuationDecision::responses_continuation.go#DecideResponsesContinuation"
	"native:llm:./providers/internal/openaichat:FuzzToChatResponseFormat::openaichat.go#ToChatResponseFormat"
	"native:llm:./providers/internal/openaichat:FuzzToChatTools::openaichat.go#ToChatTools"
	"native:llm:./providers/openai:FuzzClassifyResponsesError::adapter.go#ClassifyResponsesError"
	"native:llm:./providers/openai:FuzzResponsesRequestFingerprint::responses_continuation_fingerprint.go#requestFingerprintForResponsesBody"
	"native:llm:./providers/openai:FuzzReasoningSummaryRoundTrip::responses.go#reasoningSummaryInput"
	"native:llm:./providers/openai:FuzzOpenAIChatMultimodalParts::chatcompletions.go#buildChatMultimodalParts"
	"native:llm:./providers/openaicompat:FuzzFromChatCompletionResponse::response.go#fromChatCompletionResponse"
	"native:llm:./providers/openaicompat:FuzzOpenAICompatMultimodalParts::request.go#buildMultimodalParts"
	"native:llm:./providers/anthropic:FuzzAnthropicImageRequestBuild::request.go#anthropicImageBlock"
	"native:llm:./providers/google:FuzzGeminiImageRequestBuild::request.go#geminiImagePart"
	"native:agent:./internal/jobstore:FuzzOutputMatcher::watch.go#OutputMatcher"
	"native:agent:./internal/contextmgr:FuzzSummarizeToolResult::context_manager.go#summarizeToolResult"
	"native:agent:./internal/mcp:FuzzMergeEnv::manager.go#mergeEnv"
	"native:agent:./internal/mcp:FuzzMCPResultToString::manager.go#mcpResultToString"
	"native:agent:./internal/hooks:FuzzMatchTarget::matcher.go#matchTarget"
	"native:agent:./task:FuzzTaskStore::task_store.go#Append"
	"native:agent:./provenance:FuzzProvenance::provenance.go#Union"
	"native:agent:./mcpconfig:FuzzMCPInlineMerge::config.go#ParseInline"
	"native:agent:./provider:FuzzProfileOverrides::profile_overrides.go#addDecisionToSchema"
	"native:agent:./plugin:FuzzParsePluginHooks::hooks.go#parsePluginHooksDiagWithSource"
	"native:agent:./plugin:FuzzParseAgent::agents.go#ParseAgent"
	# Rapid promoter surfaces — Test* funcs driven by rapid.Check during ordinary
	# `go test -run` (not `go test -fuzz`). They share this registry so the triage
	# tool no longer needs a parallel hardcoded list.
	# 8.2b cross-provider differential — one canonical logical response encoded to
	# each provider's wire format, decoded via the real adapters, asserted
	# equivalent. coverpkg spans the 4 real decoders it exercises; no single focus.
	"native:llm:./providers/difftest:FuzzCrossProviderDifferential:./providers/anthropic,./providers/google,./providers/openai,./providers/openaicompat:"
	"native:llm:./providers/difftest:FuzzStreamVsNonStreamDifferential:./providers/anthropic,./providers/google,./providers/openai,./providers/openaicompat:"
	"rapid:agent:.:TestToolArgsSchemaFuzz"
	"rapid:agent:.:TestLifecycleSeqFuzz"
	"native:agent:.:FuzzLifecycleSeq::"
	"rapid:agent:./internal/jobstore:TestJobstoreSeqFuzz"
	"rapid:agent:./internal/contextmgr:TestCompactionSeqFuzz"
	"rapid:.:./internal/appserver:TestRouterSeqFuzz"
	"rapid:.:./internal/appserver:TestHubMultiSessionSeqFuzz"
	# Wave 2: next-tier agent-package surface — doctor rendering, ctx/jobstore
	# output, execenv fs, watch/observer, session-tool dispatch, lifecycle slots.
	"native:agent:./doctor:FuzzDr2BuildWatchReport::watches.go#buildWatchReport"
	"native:agent:./doctor:FuzzDr2APILog::apilog.go#APILog"
	"native:agent:./internal/contextmgr:FuzzCxjsObsMaskManageContext::strategy_obs_mask.go#ManageContext"
	"native:agent:./internal/jobstore:FuzzCxjsGrepReaderLimit::output.go#grepReaderLimit"
	"native:agent:./execenv:FuzzExfsListDirectory::local.go#ListDirectory"
	"native:agent:./execenv:FuzzExfsInjectLocalVenvPath::local.go#injectLocalVenvPath"
	"native:agent:.:FuzzWobsClassifyRestoredTarget::job_watch.go#classifyRestoredWatchSendTarget"
	"native:agent:.:FuzzWobsBuildWatchFrame::job_watch.go#buildWatchFrame"
	"native:agent:.:FuzzWobsRestoreWatchSendPending::job_watch.go#restoreWatchSendPending"
	"native:agent:.:FuzzWobsConfigureWatchSession::job_watch.go#configureWatch"
	"native:agent:.:FuzzWobsLoadObserverGrants::observer_grants.go#LoadSessionObserverGrants"
	"native:agent:.:FuzzStoolDispatch::session_tools.go#execTool"
	"native:agent:.:FuzzStoolWebFetch::tool_web_fetch.go#webFetch"
	"native:agent:.:FuzzLcyc_DrainAsSteer::session_queue.go#DrainAsSteerWithInput"
	"native:agent:.:FuzzLcyc_DiscardRestoredCandidate::session_lifecycle.go#discardRestoredCandidate"
	"native:agent:.:FuzzLcyc_ReserveSlot::subagent_manager.go#reserveSlot"
	"native:agent:.:FuzzLcyc_InitPlugins::session_init.go#initPlugins"
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
