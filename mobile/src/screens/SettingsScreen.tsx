/**
 * Settings screen — mobile-only server management, appearance, and
 * diagnostics/permission placeholders.
 *
 * No web settings panels. Mobile-only: saved servers, appearance (theme),
 * and permission/connection diagnostics placeholders for Task 8.
 */
import type { JSX } from "react";
import type { ThemeChoice } from "../state/preferences";
import { Button } from "../ui/Button";
import { ListRow } from "../ui/ListRow";
import { TopBar } from "../ui/TopBar";
import type { ConnectionStore, PreferencesStore } from "./root-types";

export interface SettingsScreenProps {
  readonly connection: ConnectionStore;
  readonly preferences: PreferencesStore;
  readonly onOpenSwitcher: () => void;
}

export function SettingsScreen({
  connection,
  preferences,
  onOpenSwitcher,
}: SettingsScreenProps): JSX.Element {
  const profiles = connection((s) => s.profiles);
  const activeProfileId = connection((s) => s.activeProfileId);
  const theme = preferences((s) => s.theme);
  const active = profiles.find((p) => p.id === activeProfileId) ?? null;

  return (
    <main className="evener-screen-scroll">
      <TopBar title="Settings" />
      <div className="evener-list-group">
        <div className="evener-list-group__header">Connection</div>
        <ListRow
          title={active?.name ?? "No server"}
          subtitle={active?.origin ?? "Not connected"}
          ariaLabel="active server"
          onClick={onOpenSwitcher}
        />
        <ListRow
          title={`${profiles.length} saved server${profiles.length === 1 ? "" : "s"}`}
          subtitle="Switch, edit, or remove"
          ariaLabel="manage servers"
          onClick={onOpenSwitcher}
        />
      </div>
      <div className="evener-list-group">
        <div className="evener-list-group__header">Appearance</div>
        <ThemeRow
          label="System"
          value="system"
          current={theme}
          onSelect={preferences.getState().setTheme}
        />
        <ThemeRow
          label="Light"
          value="light"
          current={theme}
          onSelect={preferences.getState().setTheme}
        />
        <ThemeRow
          label="Dark"
          value="dark"
          current={theme}
          onSelect={preferences.getState().setTheme}
        />
      </div>
      <div className="evener-list-group">
        <div className="evener-list-group__header">Diagnostics</div>
        <ListRow
          title="Permissions"
          subtitle="Camera, microphone, speech"
          ariaLabel="permissions"
        />
        <ListRow
          title="Connection"
          subtitle="Hub reachability and transport"
          ariaLabel="connection diagnostics"
        />
      </div>
      <div style={{ padding: "16px" }}>
        <Button variant="tertiary" disabled>
          Diagnostics arrive in Task 8
        </Button>
      </div>
    </main>
  );
}

function ThemeRow(props: {
  label: string;
  value: ThemeChoice;
  current: ThemeChoice;
  onSelect: (t: ThemeChoice) => void;
}): JSX.Element {
  const selected = props.current === props.value;
  return (
    <button
      type="button"
      className="evener-list-row"
      aria-label={`theme ${props.label.toLowerCase()}`}
      onClick={() => props.onSelect(props.value)}
    >
      <span className="evener-list-row__main">
        <span className="evener-list-row__title">{props.label}</span>
      </span>
      {selected ? <span aria-hidden="true">✓</span> : null}
    </button>
  );
}
