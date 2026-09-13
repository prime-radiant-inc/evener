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
export type { AskAnswerItem, AskResolution } from "./askAnswers";
export { composeAskAnswers } from "./askAnswers";
export type { AnyNotification, AppwireClientOptions, ConnectionState, TerminalReason } from "./client";
export { APPWIRE_PROTOCOL_VERSION, AppwireClient } from "./client";
export type { AppwireClientLike } from "./clientLike";
export type { DocFileContent, DocFileErrorKind } from "./docContent";
// readDocFile is deliberately absent: it hardcodes a relative URL and the
// browser fetch global, so no Node or native consumer of this package can call
// it. It joins the entry point when it takes a base-URL and fetch port.
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
export type { WebSocketLike } from "./transport";
export { rpcURLFromLocation } from "./transport";
export type * from "./types.gen";
export { METHOD_NAMES, NOTIFICATION_NAMES, STEERING_KINDS, THREAD_ITEM_EVENT_KINDS } from "./types.gen";
