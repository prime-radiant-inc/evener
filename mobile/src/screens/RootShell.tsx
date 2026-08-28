/**
 * Root navigation shell — three-tab bottom bar (Sessions/New/Settings),
 * full-screen onboarding when no profiles exist, and a conversation
 * push/pop stack that hides the tab bar.
 *
 * The Sessions top bar shows the active server button, which opens the server
 * switcher sheet. The shell applies the resolved theme, Dynamic Type category,
 * and reduced-motion attribute to the root element. No desktop cards, rail,
 * panes, or hover behavior.
 *
 * While profiles are loading (status "initial"), the shell shows a loading
 * indicator — not onboarding — to prevent an onboarding flash when the store
 * has saved profiles that haven't been read yet.
 */
import {
  type JSX,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { ConceptSwitcher } from "../live-concepts/ConceptSwitcher";
import { LiveConceptHost } from "../live-concepts/LiveConceptHost";
import {
  type ConceptStorage,
  createLiveConceptUiStore,
} from "../live-concepts/live-ui-store";
import { createActivityStore } from "../state/activity";
import { createAttachmentStore } from "../state/attachments";
import type { Reachability } from "../state/connection";
import { createConversationStore } from "../state/conversation";
import type { RootTab } from "../state/navigation";
import { connectRosterNotifications } from "../state/roster";
import { createVoiceStore } from "../state/voice";
import { BottomBar, type BottomTab } from "../ui/BottomBar";
import {
  usePlatformPresentation,
  useViewportCoordinator,
} from "../ui/platformPresentation";
import { Sheet } from "../ui/Sheet";
import { Loading } from "../ui/States";
import { type StatusKind, StatusMark } from "../ui/StatusMark";
import { NewSessionScreen } from "./NewSessionScreen";
import { OnboardingScreen } from "./OnboardingScreen";
import type { ProfileScopedServices } from "./production-services";
import type {
  ConnectionStore,
  NavigationStore,
  PreferencesStore,
  ShellServices,
} from "./root-types";
import { ServerSwitcherSheet } from "./ServerSwitcherSheet";
import { SettingsScreen } from "./SettingsScreen";
import { VoiceScreen } from "./VoiceScreen";

export interface RootShellProps {
  readonly services: ShellServices;
  readonly stores: {
    readonly connection: ConnectionStore;
    readonly navigation: NavigationStore;
    readonly preferences: PreferencesStore;
  };
}

const TABS: readonly BottomTab[] = [
  { id: "sessions", label: "Sessions", glyph: "▣" },
  { id: "new", label: "New", glyph: "＋" },
  { id: "settings", label: "Settings", glyph: "⚙" },
];

const PANEL_ID = "evener-tab-panel";

interface OwnedProfileScope {
  readonly profileId: string | null;
  readonly origin: string | null;
  readonly generation: number;
  readonly epoch: number;
}

interface ActiveProfileGraph {
  readonly scope: Pick<
    OwnedProfileScope,
    "profileId" | "origin" | "generation"
  >;
  readonly scoped: ProfileScopedServices;
  active: boolean;
  disposed: boolean;
  sourcesInvalidated: boolean;
  invalidateSources: () => void;
  unsubscribeRoster: () => void;
  unsubscribeState: () => void;
}

interface PendingProfileGraphDisposal {
  readonly graph: ActiveProfileGraph;
  cancelled: boolean;
}

export function RootShell({ services, stores }: RootShellProps): JSX.Element {
  const { connection, navigation, preferences } = stores;
  const profiles = connection((s) => s.profiles);
  const activeProfileId = connection((s) => s.activeProfileId);
  const activeProfileGeneration = connection((s) => s.generation);
  const activeProfileOrigin = connection((s) => {
    const active = s.profiles.find(
      (profile) => profile.id === s.activeProfileId,
    );
    return active?.origin ?? null;
  });
  const activeProfileReachability = connection((s) =>
    s.activeProfileId === null
      ? "unknown"
      : (s.reachability[s.activeProfileId] ?? "unknown"),
  );
  const status = connection((s) => s.status);
  const tab = navigation((s) => s.tab);
  const conversationStack = navigation((s) => s.conversationStack);
  const activeConversation = navigation((s) => s.activeConversation);
  const theme = preferences((s) => s.theme);
  const reducedMotion = preferences((s) => s.reducedMotion);
  const contentSize = preferences((s) => s.contentSize);

  // Apply presentation preferences (theme, content-size, reduced-motion) to
  // document.documentElement so body-level portals inherit the same tokens.
  usePlatformPresentation({ theme, contentSize, reducedMotion });
  // Coordinate the visualViewport once: writes --viewport-height and
  // --keyboard-inset onto the document root and tears down on unmount.
  useViewportCoordinator();

  const [serverSwitcherOpen, setServerSwitcherOpen] = useState(false);
  const [conceptSwitcherOpen, setConceptSwitcherOpen] = useState(false);
  const [addingServer, setAddingServer] = useState(false);
  const [showVoice, setShowVoice] = useState(false);

  // RootShell owns exactly one identity for every cross-concept production
  // store. They stay alive while renderers change and are reset together when
  // the profile owner advances to a fresh scope.
  const [conversationStore] = useState(createConversationStore);
  const [activityStore] = useState(createActivityStore);
  const [voiceStore] = useState(createVoiceStore);
  // Metadata-only live attachment presentation is deferred, but RootShell
  // retains the canonical attachment store identity for its existing viewer.
  const [_attachmentStore] = useState(createAttachmentStore);
  const [conceptUiStore] = useState(() =>
    createLiveConceptUiStore(
      safeConceptStorage(
        services.conceptStorage ?? createMemoryConceptStorage(),
      ),
    ),
  );
  const selectedConcept = conceptUiStore((state) => state.concept);
  const workOpen = conceptUiStore((state) => state.workOpen);

  const [liveServices, setLiveServices] =
    useState<ProfileScopedServices | null>(null);
  const activeProfileGraphRef = useRef<ActiveProfileGraph | null>(null);
  const pendingProfileGraphDisposalRef =
    useRef<PendingProfileGraphDisposal | null>(null);
  const profileScopeEpochRef = useRef(0);
  const initialProfileScope: OwnedProfileScope = {
    profileId: activeProfileId,
    origin: activeProfileOrigin,
    generation: activeProfileGeneration,
    epoch: profileScopeEpochRef.current,
  };
  const ownedProfileScopeRef = useRef<OwnedProfileScope>(initialProfileScope);
  const [ownedProfileScope, setOwnedProfileScope] =
    useState<OwnedProfileScope>(initialProfileScope);

  const scheduleProfileGraphDisposal = useCallback(
    (graph: ActiveProfileGraph) => {
      const pending: PendingProfileGraphDisposal = {
        graph,
        cancelled: false,
      };
      pendingProfileGraphDisposalRef.current = pending;
      queueMicrotask(() => {
        if (
          pending.cancelled ||
          pendingProfileGraphDisposalRef.current !== pending
        ) {
          return;
        }
        pendingProfileGraphDisposalRef.current = null;
        disposeProfileGraph(graph);
        if (activeProfileGraphRef.current === graph) {
          activeProfileGraphRef.current = null;
        }
      });
    },
    [],
  );

  const conceptSwitcherOpenerRef = useRef<HTMLElement | null>(null);
  const conceptSwitcherWasOpenRef = useRef(false);
  const openConceptSwitcher = useCallback(() => {
    const activeElement = document.activeElement;
    conceptSwitcherOpenerRef.current =
      activeElement instanceof HTMLElement ? activeElement : null;
    setConceptSwitcherOpen(true);
  }, []);

  // A concept selection replaces the trigger element. Update the restoration
  // target after that DOM replacement but before ConceptSwitcher's passive
  // close effect focuses it.
  useLayoutEffect(() => {
    if (!conceptSwitcherOpen && conceptSwitcherWasOpenRef.current) {
      const replacementTrigger = document.querySelector<HTMLElement>(
        '[data-concept-switch-trigger="true"]',
      );
      if (replacementTrigger !== null) {
        conceptSwitcherOpenerRef.current = replacementTrigger;
      }
    }
    conceptSwitcherWasOpenRef.current = conceptSwitcherOpen;
  }, [conceptSwitcherOpen]);

  // Load profiles on mount.
  useEffect(() => {
    void connection.getState().refresh();
  }, [connection]);

  // Content-size consumption and lifecycle refresh under one monotonic guard.
  // A generation counter prevents unmount and an older/slower read from
  // overwriting a newer foreground result.
  useEffect(() => {
    let generation = 0;
    let cancelled = false;

    function refreshContentSize(gen: number): void {
      void services.native
        .getContentSize()
        .then((category) => {
          // Only commit if this is the latest generation and not cancelled.
          if (!cancelled && gen === generation) {
            preferences.getState().setContentSize(category);
          }
        })
        .catch(() => {
          // contentSize is best-effort; keep the store default.
        });
    }

    // Initial mount read.
    refreshContentSize(generation);

    // Lifecycle subscription: foreground refreshes both profiles and content size.
    const unsubscribe = services.native.onLifecycle((state) => {
      if (state === "foreground") {
        generation += 1;
        void connection.getState().refresh();
        refreshContentSize(generation);
      }
    });

    return () => {
      cancelled = true;
      unsubscribe();
    };
  }, [services.native, connection, preferences]);

  // Build one AppWire client and all server-scoped wrappers for the active
  // profile. A changed profile tuple renders no concept until this owner effect
  // has synchronously reset/replaced roster, conversation, activity, and UI
  // scope, then advanced its own monotonic epoch. The epoch is deliberately
  // independent of profile IDs and reusable connection/store generations.
  useEffect(() => {
    const targetScope = {
      profileId: activeProfileId,
      origin: activeProfileOrigin,
      generation: activeProfileGeneration,
    };
    const currentOwner = ownedProfileScopeRef.current;
    const scopeChanged = !sameProfileScope(currentOwner, targetScope);
    const currentGraph = activeProfileGraphRef.current;
    const pendingDisposal = pendingProfileGraphDisposalRef.current;

    if (
      currentGraph !== null &&
      !currentGraph.disposed &&
      sameProfileScope(currentGraph.scope, targetScope) &&
      !scopeChanged
    ) {
      if (pendingDisposal?.graph === currentGraph) {
        pendingDisposal.cancelled = true;
        pendingProfileGraphDisposalRef.current = null;
      }
      currentGraph.active = true;
      currentGraph.scoped.client.setActive(true);
      return () => {
        deactivateProfileGraph(currentGraph);
        scheduleProfileGraphDisposal(currentGraph);
      };
    }

    if (pendingDisposal !== null) {
      pendingDisposal.cancelled = true;
      pendingProfileGraphDisposalRef.current = null;
    }
    if (currentGraph !== null) {
      invalidateProfileGraphSources(currentGraph);
      disposeProfileGraph(currentGraph);
      activeProfileGraphRef.current = null;
    }

    if (scopeChanged) {
      setLiveServices(null);
      if (currentGraph === null) {
        conversationStore.getState().reset();
        activityStore.getState().reset();
      }
      conceptUiStore.getState().resetProfileScope();
      navigation.getState().clearConversations();
      setShowVoice(false);
      setConceptSwitcherOpen(false);

      profileScopeEpochRef.current += 1;
      const nextOwner: OwnedProfileScope = {
        ...targetScope,
        epoch: profileScopeEpochRef.current,
      };
      // These four resets/replacements above are the complete owner
      // transaction. Only publish the fresh epoch after all of them finish.
      ownedProfileScopeRef.current = nextOwner;
      setOwnedProfileScope(nextOwner);
    }

    const profile = connection
      .getState()
      .profiles.find((candidate) => candidate.id === activeProfileId);
    const profileGeneration = connection.getState().generation;
    const createScoped = services.createProfileScopedServices;
    if (
      profile === undefined ||
      profile.origin !== activeProfileOrigin ||
      profileGeneration !== activeProfileGeneration ||
      createScoped === undefined
    ) {
      return;
    }

    const scoped = createScoped(profile);
    const graph: ActiveProfileGraph = {
      scope: targetScope,
      scoped,
      active: true,
      disposed: false,
      sourcesInvalidated: false,
      invalidateSources: () => {
        scoped.rosterStore.getState().reset();
        conversationStore.getState().reset();
        activityStore.getState().reset();
      },
      unsubscribeRoster: () => {},
      unsubscribeState: () => {},
    };
    activeProfileGraphRef.current = graph;
    graph.unsubscribeRoster = connectRosterNotifications(
      scoped.rosterStore,
      scoped.rosterService,
      (handler) => scoped.client.onNotification(handler),
    );

    const setReachability = (state: string): void => {
      const reachability = mapClientState(state);
      if (reachability !== null && graph.active && !graph.disposed) {
        connection.getState().setReachability(profile.id, reachability);
      }
    };
    graph.unsubscribeState = scoped.client.onStateChange(setReachability);
    setReachability("connecting");

    void scoped.client
      .connect()
      .then(() => {
        if (
          graph.disposed ||
          !graph.active ||
          activeProfileGraphRef.current !== graph ||
          !sameProfileScope(ownedProfileScopeRef.current, graph.scope)
        ) {
          return;
        }
        connection.getState().setReachability(profile.id, "reachable");
        setLiveServices(scoped);
        void scoped.rosterStore.getState().refresh(scoped.rosterService);
      })
      .catch(() => {
        if (
          !graph.disposed &&
          graph.active &&
          activeProfileGraphRef.current === graph &&
          sameProfileScope(ownedProfileScopeRef.current, graph.scope)
        ) {
          connection.getState().setReachability(profile.id, "unreachable");
        }
      });

    return () => {
      deactivateProfileGraph(graph);
      scheduleProfileGraphDisposal(graph);
    };
  }, [
    activeProfileId,
    activeProfileOrigin,
    activeProfileGeneration,
    activityStore,
    conceptUiStore,
    connection,
    conversationStore,
    navigation,
    scheduleProfileGraphDisposal,
    services.createProfileScopedServices,
  ]);

  // Keep roster event refresh active only while the production Sessions
  // surface is visible.
  useEffect(() => {
    if (liveServices === null) return;
    const roster = liveServices.rosterStore.getState();
    roster.setSessionsVisible(
      tab === "sessions" && activeConversation === null,
    );
    return () => roster.setSessionsVisible(false);
  }, [activeConversation, liveServices, tab]);

  // Open the selected conversation and its sanitized activity projection only
  // after the profile-scoped AppWire client exists. The production stores own
  // exact binding/generation checks for late frames and reads.
  useEffect(() => {
    if (activeConversation === null || liveServices === null) {
      if (activeConversation === null) {
        conversationStore.getState().reset();
        activityStore.getState().reset();
      }
      return;
    }
    void conversationStore
      .getState()
      .openProjected(
        liveServices.conversationService,
        activityStore.getState(),
        activeConversation.sessionId,
      );
  }, [activeConversation, activityStore, conversationStore, liveServices]);

  const isLoading = status === "initial" || status === "loading";
  // Auto-select the first profile if profiles exist but none is active.
  // This handles the case where a profile was paired before auto-select
  // was added, or where the active profile was deleted.
  useEffect(() => {
    if (profiles.length > 0 && activeProfileId === null) {
      void services.profile
        .select({ profileId: profiles[0]?.id ?? "" })
        .then((result) => {
          if (result.profileId !== null) {
            connection.setState({
              activeProfileId: result.profileId,
              generation: result.generation,
            });
          }
        })
        .catch(() => {});
    }
  }, [profiles, activeProfileId, services.profile, connection]);
  const hasProfiles = profiles.length > 0 && activeProfileId !== null;
  const inConversation = conversationStack.length > 0;
  const activeProfile =
    profiles.find((profile) => profile.id === activeProfileId) ?? null;
  const activeProfileStatus: StatusKind =
    activeProfileReachability === "reachable"
      ? "reachable"
      : activeProfileReachability === "reconnecting"
        ? "reconnecting"
        : activeProfileReachability === "unreachable"
          ? "offline"
          : "unknown";
  const profileScopeReady = sameProfileScope(ownedProfileScope, {
    profileId: activeProfileId,
    origin: activeProfileOrigin,
    generation: activeProfileGeneration,
  });

  const runtime = useMemo(
    () => ({
      connection,
      navigation,
      preferences,
      rosterStore: liveServices?.rosterStore ?? null,
      rosterService: liveServices?.rosterService ?? null,
      conversationStore,
      conversationService: liveServices?.conversationService ?? null,
      activityStore,
      native: services.native,
      profileId: ownedProfileScope.profileId,
    }),
    [
      activityStore,
      connection,
      conversationStore,
      liveServices,
      navigation,
      ownedProfileScope.profileId,
      preferences,
      services.native,
    ],
  );

  const openConversation = useCallback(
    (ref: string) => {
      const entry = liveServices?.rosterStore
        .getState()
        .entries.find((candidate) => candidate.ref === ref);
      navigation.getState().pushConversation({
        sessionId: ref,
        title: entry?.title ?? "Conversation",
      });
    },
    [liveServices, navigation],
  );
  const openRootTab = useCallback(
    (nextTab: Extract<RootTab, "new" | "settings">) => {
      setShowVoice(false);
      conceptUiStore.getState().setWorkOpen(false);
      navigation.getState().popAllConversations();
      navigation.getState().setTab(nextTab);
    },
    [conceptUiStore, navigation],
  );

  const resolvedTheme = theme === "system" ? undefined : theme;

  const shellAttrs = {
    className: "evener-shell",
    "data-theme": resolvedTheme,
    "data-content-size": contentSize,
    "data-reduced-motion": reducedMotion ? "true" : undefined,
  };

  // Loading state — prevents onboarding flash while profiles load.
  if (isLoading && !addingServer && !hasProfiles) {
    return (
      <div {...shellAttrs}>
        <Loading label="Loading…" />
      </div>
    );
  }

  // A profile change is visible to connection state before the RootShell owner
  // effect can complete its reset transaction. Suppress the renderer during
  // that gap; the fresh scope appears only with its newly published epoch.
  if (hasProfiles && !addingServer && !profileScopeReady) {
    return (
      <div {...shellAttrs}>
        <Loading label="Loading…" />
      </div>
    );
  }

  // Onboarding or adding a server.
  if (!hasProfiles || addingServer) {
    return (
      <div {...shellAttrs}>
        <OnboardingScreen
          services={services}
          onConnected={() => {
            setAddingServer(false);
            // Refresh the shared store to pick up the new profile. Don't set
            // loading state — that would flash the loading screen. The store's
            // refresh() sets status to "loading" then "ready"/"error". Instead,
            // call health() and update profiles/activeProfileId directly.
            void services.profile
              .health()
              .then((health) => {
                connection.setState({
                  profiles: health.profiles,
                  activeProfileId: health.activeProfileId,
                  generation: health.generation,
                  status: "ready",
                });
              })
              .catch(() => {
                // If health fails, keep current state — onboarding will re-show.
              });
          }}
          onCancel={
            addingServer
              ? () => {
                  setAddingServer(false);
                }
              : undefined
          }
        />
      </div>
    );
  }

  return (
    <div {...shellAttrs}>
      {inConversation ? (
        showVoice ? (
          <VoiceScreen
            conversationStore={conversationStore}
            voiceStore={voiceStore}
            bridge={services.native}
            composerFocusRef={{ current: null }}
            onEnd={() => setShowVoice(false)}
            onKeyboard={() => setShowVoice(false)}
            connectionStatus="unknown"
            reducedMotion={reducedMotion}
          />
        ) : (
          <LiveConceptHost
            runtime={runtime}
            uiStore={conceptUiStore}
            platform="ios"
            profileScopeEpoch={ownedProfileScope.epoch}
            surface={workOpen ? "work" : "conversation"}
            onOpenConceptSwitcher={openConceptSwitcher}
            onOpenConversation={openConversation}
            onBack={() => navigation.getState().popConversation()}
            onOpenNew={() => openRootTab("new")}
            onOpenSettings={() => openRootTab("settings")}
            onOpenVoice={() => setShowVoice(true)}
          />
        )
      ) : (
        <>
          <div
            className="evener-screen-scroll"
            role="tabpanel"
            id={PANEL_ID}
            aria-labelledby={`${PANEL_ID}-tab-${tab}`}
          >
            {tab === "sessions" ? (
              <>
                <button
                  type="button"
                  className="evener-sessions-header"
                  onClick={() => setServerSwitcherOpen(true)}
                >
                  <span className="evener-sessions-header__text">
                    <span className="evener-sessions-header__name">
                      {activeProfile?.name ?? "No server"}
                    </span>{" "}
                    {activeProfile?.origin ? (
                      <>
                        <span className="evener-sessions-header__origin">
                          {activeProfile.origin}
                        </span>{" "}
                      </>
                    ) : null}
                    <span className="evener-sessions-header__label">
                      active server
                    </span>{" "}
                    <StatusMark status={activeProfileStatus} />
                  </span>
                  <span
                    className="evener-sessions-header__chevron"
                    aria-hidden="true"
                  >
                    ›
                  </span>
                </button>
                <LiveConceptHost
                  runtime={runtime}
                  uiStore={conceptUiStore}
                  platform="ios"
                  profileScopeEpoch={ownedProfileScope.epoch}
                  surface="sessions"
                  onOpenConceptSwitcher={openConceptSwitcher}
                  onOpenConversation={openConversation}
                  onBack={() => navigation.getState().popConversation()}
                  onOpenNew={() => openRootTab("new")}
                  onOpenSettings={() => openRootTab("settings")}
                  onOpenVoice={() => setShowVoice(true)}
                />
              </>
            ) : null}
            {tab === "new" ? (
              <NewSessionScreen
                service={liveServices?.newSessionService}
                navigation={navigation}
              />
            ) : null}
            {tab === "settings" ? (
              <SettingsScreen
                connection={connection}
                preferences={preferences}
                onOpenSwitcher={() => setServerSwitcherOpen(true)}
              />
            ) : null}
          </div>
          <BottomBar
            tabs={TABS}
            activeId={tab}
            onSelect={(id) => navigation.getState().setTab(id as RootTab)}
            panelId={PANEL_ID}
          />
        </>
      )}
      <ConceptSwitcher
        open={conceptSwitcherOpen}
        selectedConcept={selectedConcept}
        onSelect={(concept) => conceptUiStore.getState().setConcept(concept)}
        onClose={() => setConceptSwitcherOpen(false)}
        openerRef={conceptSwitcherOpenerRef}
      />
      <Sheet
        open={serverSwitcherOpen}
        onClose={() => setServerSwitcherOpen(false)}
        title="Servers"
      >
        <ServerSwitcherSheet
          connection={connection}
          navigation={navigation}
          onSwitch={() => setServerSwitcherOpen(false)}
          onAdd={() => {
            setServerSwitcherOpen(false);
            setAddingServer(true);
          }}
          onClose={() => setServerSwitcherOpen(false)}
        />
      </Sheet>
    </div>
  );
}

function mapClientState(state: string): Reachability | null {
  switch (state) {
    case "ready":
      return "reachable";
    case "connecting":
    case "reconnecting":
      return "reconnecting";
    case "closed":
      return "unreachable";
    default:
      return null;
  }
}

function sameProfileScope(
  owned: Pick<OwnedProfileScope, "profileId" | "origin" | "generation">,
  target: Pick<OwnedProfileScope, "profileId" | "origin" | "generation">,
): boolean {
  return (
    owned.profileId === target.profileId &&
    owned.origin === target.origin &&
    owned.generation === target.generation
  );
}

function disposeProfileGraph(graph: ActiveProfileGraph): void {
  if (graph.disposed) return;
  invalidateProfileGraphSources(graph);
  deactivateProfileGraph(graph);
  graph.disposed = true;
  graph.unsubscribeRoster();
  graph.unsubscribeState();
  graph.scoped.client.close();
}

function invalidateProfileGraphSources(graph: ActiveProfileGraph): void {
  if (graph.sourcesInvalidated) return;
  graph.sourcesInvalidated = true;
  graph.invalidateSources();
}

function deactivateProfileGraph(graph: ActiveProfileGraph): void {
  graph.active = false;
  graph.scoped.client.setActive(false);
}

function safeConceptStorage(storage: ConceptStorage): ConceptStorage {
  return {
    read: () => {
      try {
        return storage.read();
      } catch {
        return null;
      }
    },
    write: (conceptId) => {
      try {
        storage.write(conceptId);
      } catch {
        // Persistence is best-effort; the live UI store still updates in memory.
      }
    },
    remove: () => {
      try {
        storage.remove();
      } catch {
        // Treat an unavailable persistence backend as already empty.
      }
    },
  };
}

function createMemoryConceptStorage(): ConceptStorage {
  let value: string | null = null;
  return {
    read: () => value,
    write: (conceptId) => {
      value = conceptId;
    },
    remove: () => {
      value = null;
    },
  };
}
