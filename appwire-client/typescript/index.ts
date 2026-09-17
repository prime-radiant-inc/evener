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
export type {
  AskAnswerSender,
  AskAnswerState,
  AskDockPorts,
  AskDockRefState,
  AskDockState,
  AskDockStore,
  AskDockThreads,
  AskDockThreadsSnapshot,
  SendBatchOutcome,
} from "./askDock";
export { createAskDockStore, nextUnansweredKey } from "./askDock";
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
export type {
  CommandCatalog,
  CommandCatalogClient,
  CommandCatalogState,
  SessionCommandCatalog,
  SessionCommandCatalogState,
} from "./commandCatalog";
export { createCommandCatalog, createSessionCommandCatalog, sessionPluginNames } from "./commandCatalog";
export type { InputAttachment } from "./composerInput";
export { buildComposerInput, buildInput, canonicalSkillNames } from "./composerInput";
export type { CredentialLayerView, InstanceProviderGroup } from "./credentialLabels";
export {
  activeSourceLabel,
  CONNECTION_REPLACED_ERROR,
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
export type { DelegateModelFields, DelegateTiming, DelegateTimingFields } from "./delegateDetails";
export { delegateModel, delegatePacket, delegateTiming } from "./delegateDetails";
export type { AskQuestionRef } from "./deriveAskQuestions";
export { liveAskQuestions } from "./deriveAskQuestions";
export type { DisclosureState, DisclosureStore } from "./disclosure";
export { createDisclosureStore, isDisclosureOpenIn, scopedDisclosureId } from "./disclosure";
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
export type { FrameworkFreeStore, StoreListener } from "./frameworkFreeStore";
export { createFrameworkFreeStore } from "./frameworkFreeStore";
export type { HubOverviewClient, HubOverviewListener, HubOverviewState, HubOverviewStore } from "./hubOverview";
export { createHubOverviewStore } from "./hubOverview";
export type { ItemFailureSignals } from "./itemFailure";
export { hasErrorText, hasFailureStatus, hasItemFailure, isInProgressStatus, isNonZeroExit } from "./itemFailure";
export type { JobLogTail } from "./jobOutput";
export { parseJobLogTail } from "./jobOutput";
export type { ActionId } from "./keybindingActions";
export { ACTIONS } from "./keybindingActions";
export type { Chord, KeybindingParser, KeybindingPress, KeySequence } from "./keybindingChord";
export {
  chordDisplayKeys,
  chordsOverlap,
  formatChord,
  formatSequence,
  keyComparisonIdentity,
  modifierDisplayKey,
  parseChord,
  regexMatchesKeyValue,
  serializeChord,
  withOptionalModifier,
} from "./keybindingChord";
export type { DefaultBindingShape, DefaultChordInfo } from "./keybindingDefaults";
export {
  CHARACTER_KEY_TRIGGER_BINDING_ID,
  CHEATSHEET_SCOPE,
  DEFAULT_BINDINGS,
  defaultBindingChordsForAction,
  defaultBindingShapesForAction,
  registerDefaultBindings,
  registerDefaultBindingsForAction,
  SETTINGS_SCOPE,
} from "./keybindingDefaults";
export type { ActionDisplayRow } from "./keybindingDisplay";
export { ACTION_DISPLAY_ROWS, displayBindingFor, displayBindingsFor, isActionCustomized } from "./keybindingDisplay";
export { rebindAction, removeActionBindings, restoreDefaultBinding } from "./keybindingOverrides";
export type {
  ActionRunner,
  Binding,
  BindingInput,
  KeybindingsListener,
  KeybindingsRegistry,
  KeybindingsState,
  WhenClause,
} from "./keybindingRegistry";
export { createKeybindingsRegistry, GLOBAL_SCOPE } from "./keybindingRegistry";
export type {
  KeybindingDraftCheckpoint,
  KeybindingDraftStorage,
  KeybindingsClient,
  KeybindingsStore,
  KeybindingsStoreActions,
  KeybindingsStoreDeps,
  KeybindingsStoreFields,
  KeybindingsStoreState,
  KeybindingsSupport,
} from "./keybindingsStore";
export {
  createKeybindingsStore,
  discardStoredDraft as discardStoredKeybindingDraft,
  fromWireOverrides,
  keybindingsSupport,
} from "./keybindingsStore";
export type {
  KeybindingsPlatform,
  OverrideRule,
  ValidatedOverrides,
  ValidatedRule,
  ValidationWarning,
  ValidationWarningReason,
} from "./keybindingValidation";
export { actionDisplayLabel, currentKeybindingsPlatform, validateOverrideRules } from "./keybindingValidation";
export type {
  LaunchConfigClient,
  LaunchConfigListener,
  LaunchConfigStore,
  LaunchConfigStoreState,
  LaunchSettingsState,
} from "./launchConfig";
export { createLaunchConfigStore, LaunchSettings } from "./launchConfig";
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
export type { ReadyGenerationFence } from "./readyGenerationFence";
export { createReadyGenerationFence } from "./readyGenerationFence";
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
export { harnessSupportsPluginSelection, harnessUsesEvenerModels } from "./spawnHarnessModels";
export type { PluginSelectionState } from "./spawnPluginSelectionState";
export {
  pluginSelectionIssues,
  reconcilePluginSelection,
  selectAllPlugins,
  selectedPluginNames,
  selectNoPlugins,
  setPluginSelected,
  withPluginSelection,
} from "./spawnPluginSelectionState";
export type { AdvancedFieldValue, AdvancedValues, ChipScalars } from "./spawnSchema";
export { collectAdvancedOverrides, perLaunchEvenerOptions, resolveScalars } from "./spawnSchema";
export type { StableDelegateState } from "./stableDelegate";
export { stableDelegateDisplayStatus } from "./stableDelegate";
export type { SteerRoute, SubmitRoute } from "./submitRouting";
export {
  canDrainQueue,
  canSteer,
  decideSteerRoute,
  decideSubmitRoute,
  isTurnActive,
  NO_ACTIVE_TURN,
  QUEUE_EMPTY,
  QUEUE_UNAVAILABLE,
  SEND_UNAVAILABLE,
  type SessionControlName,
  type SessionControls,
  STEER_UNAVAILABLE,
  STOP_UNAVAILABLE,
  sessionControls,
  TURN_RUNNING,
} from "./submitRouting";
export type { TaskCounts, TaskRow, TaskStatus } from "./taskListData";
export { parseTaskListData, taskAggregateLabel } from "./taskListData";
export type { TaskGroups } from "./taskListGroups";
export { groupTasks } from "./taskListGroups";
export { absoluteTime, relativeTime } from "./taskListTime";
export type {
  PanelLoadFailure,
  TasksFetchResult,
  TasksListRead,
  TasksPanelEntry,
  TasksPanelListener,
  TasksPanelNotifications,
  TasksPanelState,
  TasksPanelStore,
} from "./taskPanelState";
export {
  applyTasksFetchResult,
  classifyTasksRejection,
  classifyTasksResponse,
  createTasksPanelStore,
  EMPTY_TASKS_PANEL_ENTRY,
  panelLoadFailure,
} from "./taskPanelState";
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
export type {
  HubDefaultsByLayout,
  TranscriptDisplayChange,
  TranscriptDisplayClient,
  TranscriptDisplayStore,
  TranscriptDisplayStoreActions,
  TranscriptDisplayStoreDeps,
  TranscriptDisplayStoreFields,
  TranscriptDisplayStoreState,
  TranscriptDisplaySupport,
  TranscriptDraft,
  TranscriptDraftCheckpoint,
  TranscriptDraftStorage,
} from "./transcriptDisplayStore";
export {
  createTranscriptDisplayStore,
  discardStoredDraft as discardStoredTranscriptDraft,
  fromWireChange,
  InvalidPatchResponseError,
  isViewportClass,
  transcriptDisplaySupport,
} from "./transcriptDisplayStore";
export type {
  ProjectedAnchor,
  ProjectedEntry,
  ProjectedTurn,
  TranscriptMetadataVisibility,
  TranscriptProjection,
} from "./transcriptProjector";
export { ACTION_SUMMARY_UNAVAILABLE, projectThread } from "./transcriptProjector";
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
