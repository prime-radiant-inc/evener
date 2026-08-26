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
import { type JSX, useEffect, useRef, useState } from "react";
import { createAttachmentStore } from "../state/attachments";
import type { Reachability } from "../state/connection";
import { createConversationStore } from "../state/conversation";
import type { RootTab } from "../state/navigation";
import { createVoiceStore } from "../state/voice";
import { BottomBar, type BottomTab } from "../ui/BottomBar";
import {
  usePlatformPresentation,
  useViewportCoordinator,
} from "../ui/platformPresentation";
import { Sheet } from "../ui/Sheet";
import { Loading } from "../ui/States";
import { ConversationScreen } from "./ConversationScreen";
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
import { SessionsScreen } from "./SessionsScreen";
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

  const [switcherOpen, setSwitcherOpen] = useState(false);
  const [addingServer, setAddingServer] = useState(false);
  const [showVoice, setShowVoice] = useState(false);

  // Conversation store created once and reused across re-renders. The store is
  // always created (hook order must be stable); it stays idle until a
  // conversation is pushed.
  const conversationStoreRef = useRef(createConversationStore());
  const voiceStoreRef = useRef(createVoiceStore());
  const attachmentStoreRef = useRef(createAttachmentStore());
  const liveServicesRef = useRef<ProfileScopedServices | null>(null);
  const [liveServices, setLiveServices] =
    useState<ProfileScopedServices | null>(null);

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
  // profile. The native side selects the profile's credential by ID; JS only
  // sees the redacted profile summary and never receives the token.
  useEffect(() => {
    const previous = liveServicesRef.current;
    if (previous !== null) {
      previous.client.close();
      liveServicesRef.current = null;
    }
    setLiveServices(null);

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

    let disposed = false;
    const scoped = createScoped(profile);
    liveServicesRef.current = scoped;

    const setReachability = (state: string): void => {
      const reachability = mapClientState(state);
      if (reachability !== null && !disposed) {
        connection.getState().setReachability(profile.id, reachability);
      }
    };
    const unsubscribe = scoped.client.onStateChange(setReachability);
    setReachability("connecting");

    void scoped.client
      .connect()
      .then(() => {
        if (disposed) return;
        connection.getState().setReachability(profile.id, "reachable");
        setLiveServices(scoped);
      })
      .catch(() => {
        if (!disposed) {
          connection.getState().setReachability(profile.id, "unreachable");
        }
      });

    return () => {
      disposed = true;
      unsubscribe();
      scoped.client.close();
      if (liveServicesRef.current === scoped) {
        liveServicesRef.current = null;
      }
    };
  }, [
    activeProfileId,
    activeProfileOrigin,
    activeProfileGeneration,
    connection,
    services.createProfileScopedServices,
  ]);

  // Open the selected conversation only after the profile-scoped AppWire
  // client exists. ConversationStore owns generation checks for late frames.
  useEffect(() => {
    if (activeConversation === null || liveServices === null) {
      if (activeConversation === null) {
        conversationStoreRef.current.getState().reset();
      }
      return;
    }
    void conversationStoreRef.current
      .getState()
      .open(liveServices.conversationService, activeConversation.sessionId);
  }, [activeConversation, liveServices]);

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

  // Conversation push above the tab bar. ConversationScreen owns the top bar,
  // virtualized timeline, and composer.
  if (inConversation) {
    if (showVoice) {
      return (
        <div {...shellAttrs}>
          <VoiceScreen
            conversationStore={conversationStoreRef.current}
            voiceStore={voiceStoreRef.current}
            bridge={services.native}
            composerFocusRef={{ current: null }}
            onEnd={() => setShowVoice(false)}
            onKeyboard={() => setShowVoice(false)}
            connectionStatus="unknown"
            reducedMotion={reducedMotion}
          />
        </div>
      );
    }
    return (
      <div {...shellAttrs}>
        <ConversationScreen
          conversationStore={conversationStoreRef.current}
          navigationStore={navigation}
          conversationService={liveServices?.conversationService}
          attachmentStore={attachmentStoreRef.current}
          onShowVoice={() => setShowVoice(true)}
        />
      </div>
    );
  }

  return (
    <div {...shellAttrs}>
      <main
        className="evener-screen-scroll"
        role="tabpanel"
        id={PANEL_ID}
        aria-labelledby={`${PANEL_ID}-tab-${tab}`}
      >
        {tab === "sessions" ? (
          <SessionsScreen
            connection={connection}
            onOpenSwitcher={() => setSwitcherOpen(true)}
            rosterService={liveServices?.rosterService}
            rosterStore={liveServices?.rosterStore}
            navigation={navigation}
          />
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
            onOpenSwitcher={() => setSwitcherOpen(true)}
          />
        ) : null}
      </main>
      <BottomBar
        tabs={TABS}
        activeId={tab}
        onSelect={(id) => navigation.getState().setTab(id as RootTab)}
        panelId={PANEL_ID}
      />
      <Sheet
        open={switcherOpen}
        onClose={() => setSwitcherOpen(false)}
        title="Servers"
      >
        <ServerSwitcherSheet
          connection={connection}
          navigation={navigation}
          onSwitch={() => setSwitcherOpen(false)}
          onAdd={() => {
            setSwitcherOpen(false);
            setAddingServer(true);
          }}
          onClose={() => setSwitcherOpen(false)}
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
