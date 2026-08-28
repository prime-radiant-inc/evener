import {
  type ReactElement,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useSyncExternalStore,
} from "react";
import type { StoreApi } from "zustand";
import type { UseBoundStore } from "zustand/react";
import type { LiveConversationService } from "../services/conversation";
import type { RosterEntry } from "../services/roster";
import type { LiveConversationState } from "../state/conversation";
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
  const conversationError = runtime.conversationStore((state) => state.error);
  const activityView = runtime.activityStore((state) => state.view);

  const rosterProjection = useMemo<ProjectedRoster>(() => {
    if (runtime.rosterStore === null) {
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
  ]);

  const conversationResult = useMemo<{
    projection: {
      view: LiveConversationView;
      operational: ConversationOperationalMap;
    } | null;
    failed: boolean;
  }>(() => {
    if (mobileConversation === null || rawRef === null) {
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
  ]);

  const activityResult = useMemo<{
    view: LiveActivityView | null;
    operational: ActivityOperationalMap | null;
  }>(() => {
    if (activityView === null || conversationResult.projection === null) {
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
  }, [activityProjector, activityView, conversationResult.projection]);

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

  const previousProfileId = useRef(runtime.profileId);
  useEffect(() => {
    if (previousProfileId.current === runtime.profileId) return;
    previousProfileId.current = runtime.profileId;
    conversationProjector.reset();
    activityProjector.reset();
    rosterRefs.current = EMPTY_REFS;
    currentConversationProjection.current = null;
    currentActivityOperational.current = null;
    uiStore.getState().resetProfileScope();
  }, [activityProjector, conversationProjector, runtime.profileId, uiStore]);

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
    draft,
    canSend: mobileConversation?.capabilities.send ?? false,
    canSteer: mobileConversation?.capabilities.steer ?? false,
    canQueue: mobileConversation?.capabilities.queue ?? false,
    canInterrupt: mobileConversation?.capabilities.interrupt ?? false,
    pending: projectPendingMutation(pendingMutation),
    error: conversationResult.failed
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
    connection: projectLiveConnection(connectionStatus, activeReachability),
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
  return <Renderer state={state} dispatch={dispatch} />;
}
