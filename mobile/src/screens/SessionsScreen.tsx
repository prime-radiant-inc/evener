/**
 * Sessions screen — honest placeholder for Task 8.
 *
 * Shows the active server in the top bar (name + full origin + status), an
 * honest empty state when no sessions exist, and a loading state while the
 * roster loads. No session implementation yet — Task 8 replaces the
 * placeholder with the live roster.
 *
 * Reachability is honest: without real health data it shows "Reconnecting"
 * Reachability is honest: without real health data it shows "Not checked"
 * (unknown), never fabricated "Connected" or "Reconnecting".
 */
import { type JSX, useEffect } from "react";
import { Empty, ErrorState, Loading } from "../ui/States";
import { type StatusKind, StatusMark } from "../ui/StatusMark";
import { TopBar } from "../ui/TopBar";
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
      <TopBar
        title="Sessions"
        trailing={
          <button
            type="button"
            className="evener-button secondary"
            aria-label={`${activeName} active server`}
            onClick={onOpenSwitcher}
            style={{ minHeight: "var(--tap-target)" }}
          >
            {activeName} {activeOrigin ? `· ${activeOrigin}` : ""}
          </button>
        }
      />
      <div style={{ padding: "16px" }}>
        <StatusMark status={reachState} />
      </div>
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
