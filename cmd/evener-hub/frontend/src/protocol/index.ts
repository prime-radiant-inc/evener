export type {
  ActivityBranchState,
  ActivityCounts,
  ActivityDelegate,
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
} from "./activityRows";
export { activityDelegateState, buildActivityRows, foldRowID, jobIsFailed } from "./activityRows";
export type { AskAnswerItem, AskResolution } from "./askAnswers";
export { composeAskAnswers } from "./askAnswers";
export type { MarkerAttachment } from "./attachmentMarkers";
export { translateAttachmentMarkers } from "./attachmentMarkers";
export type { AnyNotification, AppwireClientOptions, ConnectionState, TerminalReason } from "./client";
export { APPWIRE_PROTOCOL_VERSION, AppwireClient } from "./client";
export type { AppwireClientLike } from "./clientLike";
export type { InputAttachment } from "./composerInput";
export { buildComposerInput, buildInput } from "./composerInput";
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
export type { StableDelegateState } from "./stableDelegate";
export { stableDelegateDisplayStatus } from "./stableDelegate";
export type { SteerRoute, SubmitRoute } from "./submitRouting";
export { decideSteerRoute, decideSubmitRoute, isTurnActive } from "./submitRouting";
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
