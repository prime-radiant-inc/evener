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
import { type JSX, useEffect, useState } from "react";
import type { RootTab } from "../state/navigation";
import { BottomBar, type BottomTab } from "../ui/BottomBar";
import { Sheet } from "../ui/Sheet";
import { Loading } from "../ui/States";
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

  const [switcherOpen, setSwitcherOpen] = useState(false);
  const [addingServer, setAddingServer] = useState(false);

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
            void connection.getState().refresh();
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

  // Conversation push above the tab bar (placeholder — Task 8 implements content).
  if (inConversation) {
    const activeConv = navigation.getState().activeConversation;
    return (
      <div {...shellAttrs}>
        <main className="evener-conversation">
          <div className="evener-topbar">
            <button
              type="button"
              className="evener-icon-button"
              aria-label="Back"
              onClick={() => navigation.getState().popConversation()}
            >
              ‹
            </button>
            <span className="evener-topbar__title">
              {activeConv?.title ?? "Conversation"}
            </span>
          </div>
          <div className="evener-screen-scroll">
            <p style={{ padding: "32px", color: "var(--secondary)" }}>
              Conversation content arrives in Task 8.
            </p>
          </div>
        </main>
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
