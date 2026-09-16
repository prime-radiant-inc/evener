export type {
  ActivityBranchState,
  ActivityCounts,
  ActivityDelegate,
  ActivityDelegateBranch,
  ActivityDelegateEntry,
  ActivityDisclosureState,
  ActivityEntry,
  ActivityJob,
  ActivityNodeLike,
  ActivitySessionNode,
  ActivityShellEntry,
  ActivityTree,
  ActivityUsage,
  ActivityWorktree,
} from "./activityData";
export {
  activityDelegateBranch,
  activityDelegateDiagnostics,
  activityNodeID,
  defaultExpandedIDs,
  delegateHasActiveWork,
  isActivityFailure,
  isFailedDelegateOutcome,
  isFailedJobOutcome,
  isTurnContainer,
  parseActivityTree,
  reconcileActivityState,
} from "./activityData";
export type { ActivityBranch, ActivityClient, ActivityState } from "./activityList";
export { ActivityList } from "./activityList";
export { fenceRootSession, graftContinuationTree } from "./activityMerge";
export type {
  ActivityDelegateRow,
  ActivityDelegateState,
  ActivityFoldRow,
  ActivityJobRow,
  ActivityRow,
  ActivityRowBase,
  ActivityWatchRow,
} from "./activityRows";
export {
  activityDelegateState,
  buildActivityRows,
  buildWatchRows,
  delegateRowFields,
  foldRowID,
  indexActivityEntities,
  jobIsFailed,
  jobRowFields,
  watchDeliveryInstants,
  watchFacts,
  watchIsScheduled,
  watchMeta,
  watchName,
  watchRowID,
} from "./activityRows";
export type { AskAnswerItem, AskResolution } from "./askAnswers";
export { composeAskAnswers } from "./askAnswers";
export type { AskUserOption, AskUserQuestion } from "./askShared";
export { answeredAskUserSuffix, parseAskUserQuestions } from "./askShared";
export type { RejectableFile } from "./attachmentLimits";
export { MAX_ATTACHMENT_BYTES, MAX_ATTACHMENTS, rejectionReason } from "./attachmentLimits";
export type { MarkerAttachment } from "./attachmentMarkers";
export { translateAttachmentMarkers } from "./attachmentMarkers";
export type { BuiltinMatch } from "./builtinInvocation";
export { findBuiltinArgument, matchBuiltinInvocation } from "./builtinInvocation";
export { slashCommandInvocation, visibleCatalogCommands } from "./catalogCommands";
export type { AnyNotification, AppwireClientOptions, ConnectionState, TerminalReason } from "./client";
export { APPWIRE_PROTOCOL_VERSION, AppwireClient } from "./client";
export type { AppwireClientLike } from "./clientLike";
export type { InputAttachment } from "./composerInput";
export { buildComposerInput, buildInput, canonicalSkillNames } from "./composerInput";
export type { CredentialLayerView, InstanceProviderGroup } from "./credentialLabels";
export {
  activeSourceLabel,
  credentialLayers,
  ENDPOINT_CHANGED_TEST_MESSAGE,
  FINGERPRINT_UNAVAILABLE_ERROR,
  FINGERPRINT_UNAVAILABLE_TEST_MESSAGE,
  fingerprintUnavailable,
  groupByProvider,
  isEndpointConflict,
  keylessByDesign,
  safeCredentialTestMessage,
  safeCredentialTestResult,
  styleInfoText,
  unconfiguredLabel,
} from "./credentialLabels";
export type { AskQuestionRef } from "./deriveAskQuestions";
export { liveAskQuestions } from "./deriveAskQuestions";
export {
  firstLine,
  formatCharCount,
  formatClockTime,
  formatClockTimeSeconds,
  formatDurationMs,
  formatElapsed,
  formatTokenCount,
  plainQuoteLine,
  splitMandate,
} from "./displayFormat";
export type { DocFileContent, DocFileErrorKind } from "./docContent";
// readDocFile is deliberately absent from the root: it needs a DocPort from the
// host, and a consumer that supplies one (or spies on the module) wants the
// module itself, so it is published at the "./docContent" subpath instead.
export { DOC_FILE_MAX_BYTES, DocFileError, docFileRawURL, docImageURL } from "./docContent";
export type { EntityIdMatch, EntityKind } from "./entityIds";
export { entityKindOf, findEntityIds, jobOwnerSessionId } from "./entityIds";
export type { DelegateEntityView, EntityView, JobEntityView, OpenTarget, WatchEntityView } from "./entityView";
export { buildEntityView, entityOpenTarget, watchFoldKey, watchItems } from "./entityView";
export {
  ClientNotReadyError,
  ConnectionClosedError,
  errorKind,
  errorText,
  friendlyErrorMessage,
  friendlyLaunchErrorMessage,
  GENERIC_ERROR_MESSAGE,
  HUB_UNREACHABLE_MESSAGE,
  isHubLaunchError,
  isStaleCursorError,
  mutationErrorData,
  RequestTimeoutError,
  sessionActionError,
  sessionActionHeadline,
  WireError,
} from "./errors";
export type { ItemFailureSignals } from "./itemFailure";
export { hasErrorText, hasFailureStatus, hasItemFailure, isInProgressStatus, isNonZeroExit } from "./itemFailure";
export type { JobLogTail } from "./jobOutput";
export { parseJobLogTail } from "./jobOutput";
export { asEnvEntries, asEnvObjects, asMcpList, asStringList, inheritedItems } from "./launchInherited";
export type { PathListAddOutcome, PathValidation } from "./launchPathListAdd";
export { validatePathListAdd } from "./launchPathListAdd";
export type { LaunchConfigLayerName, LaunchFormState, OptionGroup, PromptCompositeSpec } from "./launchSchema";
export {
  buildFormState,
  collectConfig,
  emptyChoiceLabel,
  globalDefaultHint,
  groupOptions,
  inactivePromptDependent,
  isCollectionKind,
  isPromptCompositeWireField,
  listSupportsExplicitEmpty,
  matchesEnvCredentialError,
  optionSupportsLayer,
  PROMPT_COMPOSITE_SPECS,
  PROMPT_DEPENDENT_WIRE_FIELDS,
  resolvedDefaultLabel,
  resolvedEmptyChoice,
  schemaPathKind,
} from "./launchSchema";
export { marketplaceSourceLabel } from "./marketplaceSourceLabel";
export type {
  CapabilitySource,
  ItemImage,
  ItemModel,
  ModelRetryState,
  ThreadDiagnostics,
  ThreadModel,
  TurnModel,
} from "./model";
export { SYSTEM_PRELUDE_TURN_ID } from "./model";
export type { PickerModelRow, PickerRow } from "./modelCatalogPickerRows";
export { buildPickerRows, pickableModelRows, rowMeta, unavailableLine } from "./modelCatalogPickerRows";
export type { ModelCatalog, ModelCatalogDiagnostic, ModelCatalogEntry } from "./modelCatalogTypes";
export type { CatalogOption } from "./modelCatalogView";
export {
  capabilityLabels,
  contextWindowLabel,
  filterCatalog,
  formatCost,
  toCatalogOptions,
  withGroupHeads,
} from "./modelCatalogView";
export type { PathPickableRow, PathRow } from "./pathRows";
export { basename, buildPathRows, childrenPrefix, isDirEntry, parentOf, pickableRows } from "./pathRows";
export { humanizeState } from "./railSessionState";
export { effortLabel, effortOptionLevels, sessionEffortLevels } from "./reasoningEffort";
export type { AskBatch } from "./reconcileBatches";
export { reconcileBatches } from "./reconcileBatches";
export type { NotificationRoutingKey } from "./reducer";
// chunkViewBackingForTests is deliberately absent: it reports the reducer's
// internal chunk storage so a test can assert the view never copies it, which
// is a test hook rather than protocol API. It belongs with the package's test
// support, not the entry point.
export {
  applyNotification,
  collectAuthoritativeMutationIds,
  hydrateThread,
  imageSessionRouteForSession,
  mergeOlderItemPage,
  notificationRoutingKey,
  notificationTargetsThread,
  pendingTextJoined,
  prependOlderTurns,
  resolvePendingEscalation,
} from "./reducer";
export type { SendQueueAvailability, SendQueueAvailabilityInput } from "./sendQueueAvailability";
export { deriveSendQueueAvailability } from "./sendQueueAvailability";
export { isActionUnavailable, isThreadNotFound } from "./sessionErrors";
export { canReadSharedNotes } from "./sharedNotesAvailability";
export type {
  SlashEmbedding,
  SlashMatchEvaluation,
  SlashMenuItem,
  SlashSpliceResult,
  SlashToken,
} from "./slashCompletion";
export {
  evaluateSlashLabel,
  filterSlashMenuItems,
  mergeSlashCommands,
  parseSlashToken,
  spliceSlashCommand,
} from "./slashCompletion";
export type { StableDelegateState } from "./stableDelegate";
export { stableDelegateDisplayStatus } from "./stableDelegate";
export type { SteerRoute, SubmitRoute } from "./submitRouting";
export { decideSteerRoute, decideSubmitRoute, isTurnActive } from "./submitRouting";
export type { TaskCounts, TaskRow, TaskStatus } from "./taskListData";
export { parseTaskListData, taskAggregateLabel } from "./taskListData";
export type { TaskGroups } from "./taskListGroups";
export { groupTasks } from "./taskListGroups";
export { absoluteTime, relativeTime } from "./taskListTime";
export type { TextEdit, TextEditWithUnknownCursor } from "./textareaMarkers";
export { insertMarker, markerText, stripMarker } from "./textareaMarkers";
export {
  clip,
  clipJobID,
  formatByteCount,
  formatToolDuration,
  lineCount,
  parseArgs,
  parseJSONObject,
  str,
  tailFold,
  tailSlice,
  trailingBracketFooter,
} from "./toolCallText";
// TranscriptDisplayConfig (an unused alias of TranscriptDisplayConfigV1) and
// TranscriptDisplayAdvanced (the local advanced block, not yet V1-suffixed)
// are deliberately absent: the root publishes the wire types of those names
// from types.gen, which they would shadow.
export type {
  ContentLevel,
  ContentSelection,
  ContentVector,
  EffectiveConfigSources,
  HookExitDetail,
  HubTranscriptDisplayDefault,
  LegacyPreferenceKey,
  LegacyPreferenceValues,
  LegacyPreferenceWrites,
  TranscriptDisplayCategory,
  TranscriptDisplayConfigV1,
  TranscriptHookExitDetail,
  TranscriptLevel,
  TranscriptViewportClass,
  ViewportClass,
  VisibleCategoryInventory,
} from "./transcriptDisplayConfig";
export {
  accessibleConfigSummary,
  advancedEnabledCount,
  CONTENT_LEVELS,
  categoryInventory,
  configFingerprint,
  configFromLegacyPrefs,
  configSummary,
  configToWire,
  contentSummary,
  decodeConfig,
  decodeLocal,
  decodeLocalConfig,
  defaultsToWire,
  defaultToWire,
  dualWriteLegacyPreferences,
  encodeConfig,
  encodeLocal,
  encodeLocalConfig,
  fingerprintConfig,
  fromWireConfig,
  fromWireDefault,
  fromWireDefaults,
  fromWireTranscriptDisplayConfig,
  HOOK_EXIT_DETAILS,
  LEGACY_PREF_KEYS,
  legacyConfigFromValues,
  legacyPrefsFromConfig,
  legacyWritesFromConfig,
  makeTranscriptDisplayConfig,
  migrateLegacyConfig,
  normalizeConfig,
  normalizeContent,
  parseLocalConfig,
  presetContent,
  resolveEffectiveConfig,
  SHIPPED_DEFAULTS,
  SHIPPED_DESKTOP_CONFIG,
  SHIPPED_MOBILE_CONFIG,
  shippedConfig,
  shippedDefault,
  shippedDefaults,
  shippedDesktopConfig,
  shippedMobileConfig,
  toWireConfig,
  toWireDefault,
  toWireDefaults,
  toWireTranscriptDisplayConfig,
  visibleCategoryInventory,
  wireToConfig,
  wireToDefault,
  wireToDefaults,
} from "./transcriptDisplayConfig";
export type { WebSocketLike } from "./transport";
export { rpcURLFromLocation } from "./transport";
export type * from "./types.gen";
export { METHOD_NAMES, NOTIFICATION_NAMES, STEERING_KINDS, THREAD_ITEM_EVENT_KINDS } from "./types.gen";
export type { ConditionSpec, JsonObject, WatchDisplayState, WatchRow, WatchSummary } from "./watchRows";
export {
  asJsonObject,
  boolField,
  conditionSpec,
  foldWatchSummaries,
  humanizeInterval,
  humanizeSeconds,
  normalizeRow,
  numField,
  parseConditionText,
  sourceLabel,
  strArrayField,
  strField,
  watchDisplayState,
} from "./watchRows";
export {
  watchArmedLabel,
  watchCadenceLabel,
  watchDurationLabel,
  watchGloss,
  watchNextFireLabel,
  watchTitle,
} from "./watchText";
