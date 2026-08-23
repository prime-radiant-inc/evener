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
import { createConversationStore } from "../state/conversation";
import type { RootTab } from "../state/navigation";
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
import type {
  ConnectionStore,
  NavigationStore,
  PreferencesStore,
  ShellServices,
} from "./root-types";
import { ServerSwitcherSheet } from "./ServerSwitcherSheet";
import { SessionsScreen } from "./SessionsScreen";
import { SettingsScreen } from "./SettingsScreen";

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
  const status = connection((s) => s.status);
  const tab = navigation((s) => s.tab);
  const conversationStack = navigation((s) => s.conversationStack);
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

  // Conversation store created once and reused across re-renders. The store is
  // always created (hook order must be stable); it stays idle until a
  // conversation is pushed. A real ConversationService is wired in a later task.
  const conversationStoreRef = useRef(createConversationStore());

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
  // virtualized timeline, and composer placeholder. The conversation store is
  // created lazily on first use and reused across re-renders; a real
  // ConversationService is wired in a later task.
  if (inConversation) {
    return (
      <div {...shellAttrs}>
        <ConversationScreen
          conversationStore={conversationStoreRef.current}
          navigationStore={navigation}
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
          />
        ) : null}
        {tab === "new" ? <NewSessionScreen /> : null}
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
