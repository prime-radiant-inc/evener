/**
 * Root navigation shell — three-tab bottom bar (Sessions/New/Settings),
 * full-screen onboarding when no profiles exist, and a conversation
 * push/pop stack that hides the tab bar.
 *
 * The Sessions top bar shows the active server button, which opens the server
 * switcher sheet. The shell applies the resolved theme, Dynamic Type category,
 * and reduced-motion attribute to the root element. No desktop cards, rail,
 * panes, or hover behavior.
 */
import { type JSX, useEffect, useState } from "react";
import type { RootTab } from "../state/navigation";
import { BottomBar, type BottomTab } from "../ui/BottomBar";
import { Sheet } from "../ui/Sheet";
import type { ShellServiceBundle } from "./fixture-services";
import { NewSessionScreen } from "./NewSessionScreen";
import { OnboardingScreen } from "./OnboardingScreen";
import type {
  ConnectionStore,
  NavigationStore,
  PreferencesStore,
} from "./root-types";
import { ServerSwitcherSheet } from "./ServerSwitcherSheet";
import { SessionsScreen } from "./SessionsScreen";
import { SettingsScreen } from "./SettingsScreen";

export interface ShellServices extends ShellServiceBundle {}

export interface RootShellProps {
  readonly services: ShellServiceBundle;
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

export function RootShell({ services, stores }: RootShellProps): JSX.Element {
  const { connection, navigation, preferences } = stores;
  const profiles = connection((s) => s.profiles);
  const activeProfileId = connection((s) => s.activeProfileId);
  const _status = connection((s) => s.status);
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

  const hasProfiles = profiles.length > 0 && activeProfileId !== null;
  const inConversation = conversationStack.length > 0;

  // Resolve theme to a data attribute.
  const resolvedTheme = theme === "system" ? undefined : theme;

  // If onboarding or adding a server with no active profile, show full-screen onboarding.
  if (!hasProfiles || addingServer) {
    return (
      <div
        className="evener-shell"
        data-theme={resolvedTheme}
        data-content-size={contentSize}
        data-reduced-motion={reducedMotion ? "true" : undefined}
      >
        <OnboardingScreen
          services={services}
          onConnected={() => {
            setAddingServer(false);
            void connection.getState().refresh();
          }}
        />
      </div>
    );
  }

  // Conversation push above the tab bar (placeholder — Task 8 implements content).
  if (inConversation) {
    const activeConv = navigation.getState().activeConversation;
    return (
      <div
        className="evener-shell"
        data-theme={resolvedTheme}
        data-content-size={contentSize}
        data-reduced-motion={reducedMotion ? "true" : undefined}
      >
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
    <div
      className="evener-shell"
      data-theme={resolvedTheme}
      data-content-size={contentSize}
      data-reduced-motion={reducedMotion ? "true" : undefined}
    >
      <main className="evener-screen-scroll">
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
      />
      <Sheet
        open={switcherOpen}
        onClose={() => setSwitcherOpen(false)}
        title="Servers"
      >
        <ServerSwitcherSheet
          connection={connection}
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
