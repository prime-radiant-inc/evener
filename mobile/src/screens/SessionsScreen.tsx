/**
 * Sessions screen — responsive server header (Foundation 7C lane C) plus the
 * live session roster grouped by attention.
 *
 * One full-width server disclosure control replaces the redundant competing
 * title row. It shows the active profile name, full origin (scheme/host/port),
 * an honest status glyph+text, and a chevron — all in a robust grid layout
 * that wraps long names and origins without ellipsis or horizontal overflow.
 *
 * Below the header, the roster lists sessions grouped by attention: Needs
 * You, Running, Recent. Each row shows the title, project, concise state, and
 * relative update time. A search input filters locally. Tapping a row opens
 * the conversation via navigation.pushConversation.
 *
 * Reachability is honest: without real health data it shows "Not checked"
 * (unknown), never fabricated "Connected" or "Reconnecting". The status state
 * logic is owned by 7B; this screen only reads it.
 *
 * The roster props (rosterService, rosterStore, navigation) are optional so the
 * server-header tests can render the screen without a roster. When the roster
 * store is provided, the screen shows the roster list; otherwise it falls back
 * to the honest empty state.
 */
import { type JSX, useEffect, useState } from "react";
import { Empty, ErrorState, Loading } from "../ui/States";
import { type StatusKind, StatusMark } from "../ui/StatusMark";
import "./SessionsScreen.css";
import type { RosterEntry, RosterService } from "../services/roster";
import { createRosterStore } from "../state/roster";
import type {
  ConnectionStore,
  NavigationStore,
  RosterStore,
} from "./root-types";

export interface SessionsScreenProps {
  readonly connection: ConnectionStore;
  readonly onOpenSwitcher: () => void;
  /** Roster service for loading sessions. Optional for header-only rendering. */
  readonly rosterService?: RosterService;
  /** Roster store (Zustand). Optional for header-only rendering. */
  readonly rosterStore?: RosterStore;
  /** Navigation store for pushing a conversation. Optional for header-only. */
  readonly navigation?: NavigationStore;
}

// Group labels for the attention sections.
const GROUP_LABELS: { needsYou: string; running: string; recent: string } = {
  needsYou: "Needs You",
  running: "Running",
  recent: "Recent",
};

// Format a timestamp (ms) as a concise relative time string.
function relativeTime(updatedAt: number): string {
  const now = Date.now();
  const diff = now - updatedAt;
  if (diff < 60_000) return "just now";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
  return `${Math.floor(diff / 86_400_000)}d ago`;
}

export function SessionsScreen({
  connection,
  onOpenSwitcher,
  rosterService,
  rosterStore,
  navigation,
}: SessionsScreenProps): JSX.Element {
  // Keep the Zustand selector hook unconditional. Production service wiring is
  // asynchronous, so rosterStore changes from undefined to a live store after
  // the first render; conditional selectors would violate React's hook order.
  const [fallbackRosterStore] = useState(createRosterStore);
  const status = connection((s) => s.status);
  const profiles = connection((s) => s.profiles);
  const activeProfileId = connection((s) => s.activeProfileId);
  const reachability = connection((s) => s.reachability);

  const active = profiles.find((p) => p.id === activeProfileId) ?? null;
  const activeName = active?.name ?? "No server";
  const activeOrigin = active?.origin ?? "";
  const reachState: StatusKind =
    active && reachability[active.id] === "unreachable"
      ? "offline"
      : active && reachability[active.id] === "reachable"
        ? "reachable"
        : active && reachability[active.id] === "reconnecting"
          ? "reconnecting"
          : "unknown";

  useEffect(() => {
    if (status === "initial") {
      void connection.getState().refresh();
    }
  }, [status, connection]);

  // Load the roster on mount when a roster service and store are provided.
  const hasRoster = rosterService !== undefined && rosterStore !== undefined;
  const activeRosterStore = rosterStore ?? fallbackRosterStore;
  const rosterLoading = activeRosterStore((s) => s.loading);
  const rosterError = activeRosterStore((s) => s.error);
  const searchTerm = activeRosterStore((s) => s.searchTerm);
  const groupedEntries = activeRosterStore((s) => s.groupedEntries);
  const entries = activeRosterStore((s) => s.entries);

  useEffect(() => {
    if (hasRoster && rosterService !== undefined && rosterStore !== undefined) {
      void rosterStore.getState().refresh(rosterService);
    }
  }, [hasRoster, rosterService, rosterStore]);

  function handleEntryClick(entry: RosterEntry): void {
    if (navigation !== undefined) {
      navigation.getState().pushConversation({
        sessionId: entry.ref,
        title: entry.title,
      });
    }
  }

  function renderRosterSection(): JSX.Element {
    if (rosterLoading && entries.length === 0) {
      return <Loading label="Loading sessions…" />;
    }
    if (rosterError !== null && entries.length === 0) {
      return (
        <ErrorState
          message={rosterError}
          retry={() => {
            if (rosterService !== undefined && rosterStore !== undefined) {
              void rosterStore.getState().refresh(rosterService);
            }
          }}
        />
      );
    }
    if (entries.length === 0) {
      return (
        <Empty
          title="No sessions yet"
          hint="Start a new session from the New tab."
        />
      );
    }

    const groups: { key: keyof typeof GROUP_LABELS; entries: RosterEntry[] }[] =
      [
        { key: "needsYou", entries: groupedEntries.needsYou },
        { key: "running", entries: groupedEntries.running },
        { key: "recent", entries: groupedEntries.recent },
      ];

    return (
      <div>
        {hasRoster && rosterStore !== undefined ? (
          <div style={{ padding: "8px 16px" }}>
            <input
              type="search"
              placeholder="Search sessions"
              value={searchTerm}
              onChange={(e) => rosterStore.getState().setSearch(e.target.value)}
              className="evener-input"
              aria-label="Search sessions"
            />
          </div>
        ) : null}
        {groups.map((group) => {
          if (group.entries.length === 0) return null;
          return (
            <div key={group.key} className="evener-list-group">
              <div
                style={{
                  padding: "8px 16px",
                  fontSize: "0.85em",
                  fontWeight: 600,
                  color: "var(--secondary)",
                }}
              >
                {GROUP_LABELS[group.key]}
              </div>
              {group.entries.map((entry) => (
                <button
                  key={entry.ref}
                  type="button"
                  className="evener-list-row"
                  onClick={() => handleEntryClick(entry)}
                  aria-label={entry.title}
                >
                  <span className="evener-list-row__main">
                    <span className="evener-list-row__title">
                      {entry.title}
                    </span>
                    <span className="evener-list-row__subtitle">
                      <span className="evener-roster-project">
                        {entry.project}
                      </span>
                      {" · "}
                      <span className="evener-roster-status">
                        {entry.status}
                      </span>
                      {" · "}
                      <span className="evener-roster-time">
                        {relativeTime(entry.updatedAt)}
                      </span>
                    </span>
                  </span>
                </button>
              ))}
            </div>
          );
        })}
      </div>
    );
  }

  return (
    <div>
      <button
        type="button"
        className="evener-sessions-header"
        onClick={onOpenSwitcher}
      >
        <span className="evener-sessions-header__text">
          <span className="evener-sessions-header__name">{activeName}</span>{" "}
          {activeOrigin ? (
            <>
              <span className="evener-sessions-header__origin">
                {activeOrigin}
              </span>{" "}
            </>
          ) : null}
          <span className="evener-sessions-header__label">active server</span>{" "}
          <StatusMark status={reachState} />
        </span>
        <span className="evener-sessions-header__chevron" aria-hidden="true">
          ›
        </span>
      </button>
      {hasRoster ? (
        renderRosterSection()
      ) : status === "loading" ? (
        <Loading label="Loading sessions…" />
      ) : status === "error" ? (
        <ErrorState
          message="Could not load sessions."
          retry={() => void connection.getState().refresh()}
        />
      ) : (
        <Empty
          title="No sessions yet"
          hint="Start a new session from the New tab."
        />
      )}
    </div>
  );
}
