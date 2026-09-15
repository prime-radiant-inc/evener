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
export type { MarkerAttachment } from "./attachmentMarkers";
export { translateAttachmentMarkers } from "./attachmentMarkers";
export { slashCommandInvocation, visibleCatalogCommands } from "./catalogCommands";
export type { AnyNotification, AppwireClientOptions, ConnectionState, TerminalReason } from "./client";
export { APPWIRE_PROTOCOL_VERSION, AppwireClient } from "./client";
export type { AppwireClientLike } from "./clientLike";
export type { InputAttachment } from "./composerInput";
export { buildComposerInput, buildInput, canonicalSkillNames } from "./composerInput";
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
export {
  canDrainQueue,
  canSteer,
  decideSteerRoute,
  decideSubmitRoute,
  isTurnActive,
  NO_ACTIVE_TURN,
  QUEUE_UNAVAILABLE,
  SEND_UNAVAILABLE,
  type SessionControlName,
  type SessionControls,
  STEER_UNAVAILABLE,
  STOP_UNAVAILABLE,
  sessionControls,
  TURN_RUNNING,
} from "./submitRouting";
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
