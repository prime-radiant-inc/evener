/**
 * Sessions screen — responsive server header (Foundation 7C lane C).
 *
 * One full-width server disclosure control replaces the redundant competing
 * title row. It shows the active profile name, full origin (scheme/host/port),
 * an honest status glyph+text, and a chevron — all in a robust grid layout
 * that wraps long names and origins without ellipsis or horizontal overflow.
 *
 * The accessible name is the natural text content: name, full origin, "active
 * server" (visually hidden), and the unchanged StatusMark label. No duplicate
 * status-label table — the StatusMark component owns the label text. The
 * chevron and status glyph are aria-hidden.
 *
 * Reachability is honest: without real health data it shows "Not checked"
 * (unknown), never fabricated "Connected" or "Reconnecting". The status state
 * logic is owned by 7B; this screen only reads it.
 */
import { type JSX, useEffect } from "react";
import { Empty, ErrorState, Loading } from "../ui/States";
import { type StatusKind, StatusMark } from "../ui/StatusMark";
import "./SessionsScreen.css";
import type { ConnectionStore } from "./root-types";

export interface SessionsScreenProps {
  readonly connection: ConnectionStore;
  readonly onOpenSwitcher: () => void;
}

export function SessionsScreen({
  connection,
  onOpenSwitcher,
}: SessionsScreenProps): JSX.Element {
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
      {status === "loading" ? (
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
