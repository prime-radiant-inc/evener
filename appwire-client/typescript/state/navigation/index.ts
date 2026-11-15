// The navigation state layer, published together at
// `@evener/appwire-client/state/navigation`: the resource vocabulary (types),
// the snapshot and delta codec, host grouping (where a row, and a project on
// several hosts, sit when sessions are organized by host), the live attention
// model (liveAttention: Live's Needs you / Working / Idle bands, their order
// and each row's why line, shared by the phone Board and the web rail), the
// graph merge,
// the deep-freeze helpers the codec and merge share, the rule matching an
// invalidation target to a loaded resource (invalidation), the revalidator
// that re-reads loaded resources on the hub's invalidations, the store an
// app binds its view layer to (store) -
// one hub connection's navigation state, which schedules its own boot work
// and owns navigation's one deadline, the convergence wait - and the graph-
// shaped selectors the web app reads that state through, adoptable by
// native if it ever gains a store (selectors).
//
// Everything a host owns is a port: the client the store reads through and
// where it persists rail expansion. Nothing here touches a framework, the
// DOM or a storage API.
//
// Every re-export is explicit, never `export *`: a `.mts` script run by tsx
// loads this barrel as CommonJS (the package is `"type": "commonjs"`), and
// Node's static named-export analysis cannot see the names through a star
// re-export, so `import { relativeAge } from .../state/navigation` fails at
// load time under tsx while every Vite/Metro consumer stays green.
export type {
  DecodedNavigationResponse,
  NavigationGraph,
  NavigationGraphContainer,
  NavigationGraphEntity,
  NavigationPresence,
  NormalizedResource,
} from "./codec";
export {
  decodeArchivedListSessions,
  decodeNavigationResponse,
  materializeNavigationResource,
  materializeSnapshot,
  normalizedGraphFromSnapshot,
  snapshotResource,
  validateGraphForResource,
  validateSnapshotForResource,
} from "./codec";
export type { HostFacts, HostPlacedRow } from "./hostGrouping";
export {
  CONTROLLER_SOURCE_ID,
  canonicalHostId,
  orderedHosts,
  projectHostIds,
} from "./hostGrouping";
export { cloneAndDeepFreezeJSON, equalJSON } from "./immutable";
export { isSequenceGap, matchesTarget, matchingTargets, requiredRevision } from "./invalidation";
export type {
  Band,
  BoardState,
  ClassifiedRow,
  Hue,
  LastLine,
  LiveBands,
  LiveSummary,
  Usual,
  WhyKind,
  WhyLine,
} from "./liveAttention";
export {
  AGENT_QUIET_AFTER_MS,
  approvalRefs,
  bandOf,
  boardState,
  hostLabeler,
  hubTime,
  LIVE_BAND_ORDER,
  lastLine,
  liveBands,
  liveRows,
  liveSummary,
  plural,
  quietOrWorking,
  rowClassifier,
  sectionLabel,
  stateWord,
  subagentChipText,
  subagentQuietLine,
  summaryText,
  taskLine,
  usualPlace,
  waitingOnSubagents,
  whyLine,
  workingActivity,
} from "./liveAttention";
export { applyDelta, normalizeSnapshot, reconcileSnapshot } from "./merge";
export type { NavigationInvalidationWaiter } from "./revalidator";
export {
  applyNavigationInvalidation,
  isGenerationMismatch,
  isRevalidatorDisposed,
  NavigationRevalidator,
} from "./revalidator";
export type { NavigationPinSectionSummary } from "./selectors";
export {
  findSessionNode,
  quotaWaitRemainingMs,
  relativeAge,
  selectAttentionSummary,
  selectDisplaySources,
  selectExpanded,
  selectGlobalRows,
  selectLiveRows,
  selectLocation,
  selectNeedsYouCount,
  selectNeedsYouRows,
  selectNextSectionOffset,
  selectPinSectionSummaries,
  selectPinSections,
  selectProjectPage,
  selectProjectResource,
  selectProjectSummaries,
  selectSectionCanDemand,
  selectSectionRemaining,
  selectSessionSummary,
  selectSources,
  subagentTallyToShow,
  waitingForQuotaText,
} from "./selectors";
export type {
  BackgroundLivePages,
  NavigationClient,
  NavigationPersistence,
  NavigationStore,
  NavigationStoreDeps,
  NavigationStoreState,
} from "./store";
export {
  createNavigationStore,
  NAVIGATION_INVALIDATION_TIMEOUT_MS,
  projectNodeExpansionKey,
} from "./store";
export type {
  NavigationRequest,
  NavigationResponse,
  ResourceKey,
  ResourceListener,
  ResourceState,
} from "./types";
export {
  canonicalResourceKey,
  isNavigationUnavailable,
  isProjectResource,
  isSettledGone,
  keyID,
  NAVIGATION_CATALOG_LIMIT,
  NAVIGATION_SECTION_LIMIT,
  NavigationBaseInvalidError,
  navigationOwnedContainerKey,
  navigationParamsToResourceKey,
  navigationRootContainerKey,
  navigationViewScope,
  nextNavigationOffset,
  settledPresence,
} from "./types";
