import {
  type ReactElement,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useReducer,
  useRef,
  useSyncExternalStore,
} from "react";
import type { StoreApi } from "zustand";
import type { UseBoundStore } from "zustand/react";
import type { LiveConversationService } from "../services/conversation";
import type { RosterEntry } from "../services/roster";
import type { LiveConversationState } from "../state/conversation";
import { connectionStatusAxLabel } from "./accessibility-semantics";
import type {
  LiveComposerView,
  LiveConceptRuntime,
  LiveConceptState,
} from "./contract";
import {
  createLiveIntentDispatcher,
  type LiveIntentDispatcherRuntime,
} from "./dispatch-live-intent";
import type { LiveConceptUiStore } from "./live-ui-store";
import type {
  LiveActivityView,
  LiveConversationView,
  LiveRosterView,
  Platform,
} from "./model";
import {
  type ActivityOperationalMap,
  createLiveActivityProjector,
} from "./project-activity";
import { projectLiveConnection } from "./project-connection";
import {
  type ConversationOperationalMap,
  createLiveConversationProjector,
} from "./project-conversation";
import {
  createRosterProjector,
  type ProjectedRoster,
  type RosterProjector,
} from "./project-roster";
import { liveConceptRegistry } from "./registry";

type BoundStore<State> = UseBoundStore<StoreApi<State>>;

export interface LiveConceptHostRuntime
  extends Omit<
    LiveConceptRuntime,
    "conversationStore" | "conversationService"
  > {
  conversationStore: BoundStore<LiveConversationState>;
  conversationService: LiveConversationService | null;
}

export interface LiveConceptHostProps {
  runtime: LiveConceptHostRuntime;
  uiStore: BoundStore<LiveConceptUiStore>;
  platform: Platform;
  /**
   * RootShell-owned monotonic profile-scope epoch. RootShell increments this
   * only after atomically resetting/replacing roster, conversation, activity,
   * and profile-scoped UI sources for the incoming scope.
   */
  profileScopeEpoch: number;
  surface: "sessions" | "conversation" | "work";
  onOpenConceptSwitcher(): void;
  onOpenConversation(ref: string): void;
  onBack(): void;
  onOpenNew(): void;
  onOpenSettings(): void;
  onOpenVoice(): void;
}

const EMPTY_ENTRIES: readonly RosterEntry[] = Object.freeze([]);
const EMPTY_REFS: ReadonlyMap<string, string> = new Map();
const EMPTY_ROSTER: LiveRosterView = {
  status: "idle",
  query: "",
  groups: [],
  hasMore: false,
  error: null,
};
const FAILED_ROSTER: LiveRosterView = {
  status: "error",
  query: "",
  groups: [],
  hasMore: false,
  error: "Unable to display sessions",
};

function useNullableStoreValue<State, Value>(
  store: BoundStore<State> | null,
  selector: (state: State) => Value,
  fallback: Value,
): Value {
  const subscribe = useCallback(
    (notify: () => void) =>
      store === null ? () => undefined : store.subscribe(notify),
    [store],
  );
  const getSnapshot = useCallback(
    () => (store === null ? fallback : selector(store.getState())),
    [fallback, selector, store],
  );
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}

function projectPendingMutation(
  pending: LiveConversationState["pendingMutation"],
): LiveComposerView["pending"] {
  if (pending === null || pending === undefined) return null;
  return {
    kind: pending.kind,
    status: pending.status,
    draftSnapshot: pending.draftSnapshot,
    generation: pending.generation,
  };
}

function projectAcceptedMutation(
  accepted: LiveConversationState["lastAcceptedMutation"],
): NonNullable<LiveComposerView["accepted"]> | null {
  if (accepted === null || accepted === undefined) return null;
  const disposition = accepted.receipt.disposition;
  if (disposition !== "applied" && disposition !== "replayed") return null;
  return { kind: accepted.kind, disposition };
}

function findRosterLabels(
  projector: RosterProjector,
  entries: readonly RosterEntry[],
  ref: string,
): { projectLabel: string; updatedLabel: string | null } {
  const matchingEntry = entries.find((entry) => entry.ref === ref);
  if (matchingEntry === undefined) {
    return { projectLabel: "", updatedLabel: null };
  }
  const privateProjection = projector.project({
    entries: [matchingEntry],
    loading: false,
    error: null,
    searchTerm: "",
    hasMore: false,
    sessionsVisible: true,
  });
  const row = privateProjection.view.groups[0]?.rows[0];
  return {
    projectLabel: matchingEntry.project,
    updatedLabel: row?.updatedLabel || null,
  };
}

export function LiveConceptHost({
  runtime,
  uiStore,
  platform,
  profileScopeEpoch,
  surface,
  onOpenConceptSwitcher,
  onOpenConversation,
  onBack,
  onOpenNew,
  onOpenSettings,
  onOpenVoice,
}: LiveConceptHostProps): ReactElement {
  const rosterProjector = useMemo(createRosterProjector, []);
  const conversationProjector = useMemo(createLiveConversationProjector, []);
  const activityProjector = useMemo(createLiveActivityProjector, []);

  const concept = uiStore((state) => state.concept);
  const workOpen = uiStore((state) => state.workOpen);
  const composerMode = uiStore((state) => state.composerMode);
  const expandedToolKeys = uiStore((state) => state.expandedToolKeys);
  const expandedWorkKeys = uiStore((state) => state.expandedWorkKeys);
  const questionDrafts = uiStore((state) => state.questionDrafts);
  const focusedItemKey = uiStore((state) => state.focusedItemKey);
  const scrollAnchors = uiStore((state) => state.scrollAnchors);

  const connectionStatus = runtime.connection((state) => state.status);
  const reachabilityByProfile = runtime.connection(
    (state) => state.reachability,
  );
  const appearance = runtime.preferences((state) => state.theme);
  const reducedMotion = runtime.preferences((state) => state.reducedMotion);
  const contentSize = runtime.preferences((state) => state.contentSize);

  const rosterEntries = useNullableStoreValue(
    runtime.rosterStore,
    (state): readonly RosterEntry[] => state.entries,
    EMPTY_ENTRIES,
  );
  const rosterLoading = useNullableStoreValue(
    runtime.rosterStore,
    (state) => state.loading,
    false,
  );
  const rosterError = useNullableStoreValue(
    runtime.rosterStore,
    (state) => state.error,
    null,
  );
  const rosterQuery = useNullableStoreValue(
    runtime.rosterStore,
    (state) => state.searchTerm,
    "",
  );
  const rosterHasMore = useNullableStoreValue(
    runtime.rosterStore,
    (state) => state.hasMore,
    false,
  );
  const sessionsVisible = useNullableStoreValue(
    runtime.rosterStore,
    (state) => state.sessionsVisible,
    false,
  );
  const rawRef = runtime.conversationStore((state) => state.ref);
  const mobileConversation = runtime.conversationStore(
    (state) => state.conversation,
  );
  const olderCursor = runtime.conversationStore((state) => state.olderCursor);
  const draft = runtime.conversationStore((state) => state.draft);
  const pendingMutation = runtime.conversationStore(
    (state) => state.pendingMutation,
  );
  const acceptedMutation = runtime.conversationStore(
    (state) => state.lastAcceptedMutation ?? null,
  );
  const conversationError = runtime.conversationStore((state) => state.error);
  const activityView = runtime.activityStore((state) => state.view);

  const acceptedProfileScope = useRef<{
    profileId: string | null;
    epoch: number;
  } | null>(null);
  if (acceptedProfileScope.current === null) {
    acceptedProfileScope.current = {
      profileId: runtime.profileId,
      epoch: profileScopeEpoch,
    };
  }
  const acceptedScope = acceptedProfileScope.current;
  const profileBlocked =
    acceptedScope.profileId !== runtime.profileId ||
    acceptedScope.epoch !== profileScopeEpoch;
  const projectionProfileGate = useMemo(
    () => ({
      profileId: runtime.profileId,
      epoch: profileScopeEpoch,
      blocked: profileBlocked,
    }),
    [profileBlocked, profileScopeEpoch, runtime.profileId],
  );

  const rosterProjection = useMemo<ProjectedRoster>(() => {
    if (projectionProfileGate.blocked || runtime.rosterStore === null) {
      return { view: EMPTY_ROSTER, refsByKey: EMPTY_REFS };
    }
    try {
      return rosterProjector.project({
        entries: rosterEntries,
        loading: rosterLoading,
        error: rosterError,
        searchTerm: rosterQuery,
        hasMore: rosterHasMore,
        sessionsVisible,
      });
    } catch {
      return { view: FAILED_ROSTER, refsByKey: EMPTY_REFS };
    }
  }, [
    rosterEntries,
    rosterError,
    rosterHasMore,
    rosterLoading,
    rosterProjector,
    rosterQuery,
    runtime.rosterStore,
    sessionsVisible,
    projectionProfileGate,
  ]);

  const conversationResult = useMemo<{
    projection: {
      view: LiveConversationView;
      operational: ConversationOperationalMap;
    } | null;
    failed: boolean;
  }>(() => {
    if (
      projectionProfileGate.blocked ||
      mobileConversation === null ||
      rawRef === null
    ) {
      return { projection: null, failed: false };
    }
    try {
      const labels = findRosterLabels(rosterProjector, rosterEntries, rawRef);
      return {
        projection: conversationProjector.project(mobileConversation, {
          ref: rawRef,
          olderCursor,
          truncatedItemIds: runtime.conversationStore
            .getState()
            .getTruncatedItemIds(),
          ...labels,
        }),
        failed: false,
      };
    } catch {
      return { projection: null, failed: true };
    }
  }, [
    conversationProjector,
    mobileConversation,
    olderCursor,
    rawRef,
    rosterEntries,
    rosterProjector,
    runtime.conversationStore,
    projectionProfileGate,
  ]);

  const activityResult = useMemo<{
    view: LiveActivityView | null;
    operational: ActivityOperationalMap | null;
  }>(() => {
    if (
      projectionProfileGate.blocked ||
      activityView === null ||
      conversationResult.projection === null
    ) {
      return { view: null, operational: null };
    }
    try {
      const projected = activityProjector.project(activityView, {
        scope: conversationResult.projection.view.threadKey,
      });
      return { view: projected.live, operational: projected.operational };
    } catch {
      return { view: null, operational: null };
    }
  }, [
    activityProjector,
    activityView,
    conversationResult.projection,
    projectionProfileGate,
  ]);

  const rosterRefs = useRef<ReadonlyMap<string, string>>(EMPTY_REFS);
  const currentConversationProjection = useRef<{
    view: LiveConversationView;
    operational: ConversationOperationalMap;
  } | null>(null);
  const currentActivityOperational = useRef<ActivityOperationalMap | null>(
    null,
  );
  rosterRefs.current = rosterProjection.refsByKey;
  currentConversationProjection.current = conversationResult.projection;
  currentActivityOperational.current = activityResult.operational;

  const [, forceAcceptedProfileRender] = useReducer(
    (revision: number) => revision + 1,
    0,
  );
  useLayoutEffect(() => {
    const accepted = acceptedProfileScope.current;
    if (accepted === null) return;
    if (
      accepted.profileId === runtime.profileId &&
      accepted.epoch === profileScopeEpoch
    ) {
      return;
    }
    if (profileScopeEpoch <= accepted.epoch) return;

    conversationProjector.reset();
    activityProjector.reset();
    rosterRefs.current = EMPTY_REFS;
    currentConversationProjection.current = null;
    currentActivityOperational.current = null;
    uiStore.getState().resetProfileScope();
    acceptedProfileScope.current = {
      profileId: runtime.profileId,
      epoch: profileScopeEpoch,
    };
    forceAcceptedProfileRender();
  }, [
    activityProjector,
    conversationProjector,
    profileScopeEpoch,
    runtime.profileId,
    uiStore,
  ]);

  useEffect(
    () => () => {
      conversationProjector.dispose();
      activityProjector.dispose();
      rosterRefs.current = EMPTY_REFS;
      currentConversationProjection.current = null;
      currentActivityOperational.current = null;
    },
    [activityProjector, conversationProjector],
  );

  const dispatchRuntime: LiveIntentDispatcherRuntime = runtime;
  const dispatch = useMemo(
    () =>
      createLiveIntentDispatcher(
        dispatchRuntime,
        {
          onOpenConceptSwitcher,
          resolveConversationRef: (key) => rosterRefs.current.get(key) ?? null,
          getCurrentProjection: () => currentConversationProjection.current,
          onOpenConversation,
          onBack,
          onOpenNew,
          onOpenSettings,
          onOpenVoice,
        },
        uiStore,
      ),
    [
      dispatchRuntime,
      onBack,
      onOpenConceptSwitcher,
      onOpenConversation,
      onOpenNew,
      onOpenSettings,
      onOpenVoice,
      uiStore,
    ],
  );

  const activeReachability =
    runtime.profileId === null
      ? "unknown"
      : (reachabilityByProfile[runtime.profileId] ?? "unknown");
  const conversation = conversationResult.projection?.view ?? null;
  const composer: LiveComposerView = {
    // The store clears its mutable draft at submit so failure recovery can use
    // revision ownership. Keep the exact submitted snapshot visible (disabled)
    // while that mutation is pending; an accepted receipt removes pending and
    // only then does the already-cleared draft become visible.
    draft: profileBlocked ? "" : (pendingMutation?.draftSnapshot ?? draft),
    canSend: profileBlocked
      ? false
      : (mobileConversation?.capabilities.send ?? false),
    canSteer: profileBlocked
      ? false
      : (mobileConversation?.capabilities.steer ?? false),
    canQueue: profileBlocked
      ? false
      : (mobileConversation?.capabilities.queue ?? false),
    canInterrupt: profileBlocked
      ? false
      : (mobileConversation?.capabilities.interrupt ?? false),
    pending: profileBlocked ? null : projectPendingMutation(pendingMutation),
    accepted:
      profileBlocked || acceptedMutation === null
        ? null
        : projectAcceptedMutation(acceptedMutation),
    error: profileBlocked
      ? null
      : conversationResult.failed
        ? "Unable to display conversation"
        : conversationError,
  };
  const state: LiveConceptState = {
    concept,
    platform,
    appearance,
    textScale: contentSize.startsWith("accessibility")
      ? "accessibility"
      : "standard",
    reducedMotion,
    surface,
    connection: {
      ...projectLiveConnection(connectionStatus, activeReachability),
      ...(runtime.connectionEvidence === undefined
        ? {}
        : { evidence: runtime.connectionEvidence }),
    },
    roster: rosterProjection.view,
    conversation,
    activity: activityResult.view,
    composer,
    ui: {
      concept,
      workOpen,
      composerMode,
      expandedToolKeys,
      expandedWorkKeys,
      questionDrafts,
      focusedItemKey,
      scrollAnchors,
    },
  };
  const Renderer = liveConceptRegistry[concept].Renderer;
  const connectionLabel = connectionStatusAxLabel(state.connection);
  const scrollIdentity = conversation?.threadKey ?? "roster";
  const scrollAnchorKey = `${concept}:${surface}:${scrollIdentity}`;
  useLayoutEffect(() => {
    const scroller = document.querySelector<HTMLElement>(
      "[data-live-concept-scroller='true']",
    );
    if (scroller === null) return;
    let restoring = true;
    let observedScroll = false;
    const saved = uiStore.getState().scrollAnchors[scrollAnchorKey];
    scroller.scrollTop = saved?.scrollTop ?? 0;
    restoring = false;
    const capture = (): void => {
      if (restoring) return;
      observedScroll = true;
      uiStore
        .getState()
        .setScrollAnchor(scrollAnchorKey, { scrollTop: scroller.scrollTop });
    };
    scroller.addEventListener("scroll", capture, { passive: true });
    return () => {
      scroller.removeEventListener("scroll", capture);
      if (observedScroll || saved !== undefined) capture();
    };
  }, [scrollAnchorKey, uiStore]);
  return (
    <>
      <p
        className="live-concept-connection-status"
        role="status"
        aria-label={connectionLabel}
      >
        {connectionLabel}
      </p>
      <Renderer state={state} dispatch={dispatch} />
    </>
  );
}
