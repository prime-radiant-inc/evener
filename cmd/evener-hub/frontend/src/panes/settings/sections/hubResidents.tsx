// hubResidents.tsx — "Discovered resident daemons" sub-section of
// Settings → Hub. Mounts below HubUpdates. Polls the Hub's resident-daemon
// inventory every two seconds, renders one accessible table row per daemon,
// and exposes Retire and Force stop actions.
//
// Polling contract: this component's useEffect owns the interval lifetime
// (start on mount, cancel on unmount); the daemonResidents store starts no
// permanent timer of its own.
//
// Force-stop safety: the DaemonIdentity the row was rendered from is
// captured in component state when the user clicks "Force stop". The
// confirmation dialog uses that captured identity — never the current list
// state — so a background refresh that changes the row cannot silently
// retarget the action.
//
// Stale-probe safety: CanRetire from the server already encodes "do not
// allow retire when probe is stale"; this component does not carry eligibility
// hints forward from a previous render.

import { useCallback, useEffect, useState } from "react";
import { friendlyErrorMessage } from "../../../protocol/errors";
import type { DaemonIdentity, DaemonResident, DaemonRetireResponse } from "../../../protocol/types.gen";
import { daemonResidentsStore, useDaemonResidentsStore } from "../../../stores/daemonResidents";
import { Button, ConfirmDialog, EmptyState, Skeleton } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./hubResidents.module.css";

const POLL_INTERVAL_MS = 2000;

const CLASS = {
  root: requireClass(styles.root, "hubResidents.module.css", "root"),
  heading: requireClass(styles.heading, "hubResidents.module.css", "heading"),
  meta: requireClass(styles.meta, "hubResidents.module.css", "meta"),
  errorBanner: requireClass(styles.errorBanner, "hubResidents.module.css", "errorBanner"),
  tableWrapper: requireClass(styles.tableWrapper, "hubResidents.module.css", "tableWrapper"),
  table: requireClass(styles.table, "hubResidents.module.css", "table"),
  actions: requireClass(styles.actions, "hubResidents.module.css", "actions"),
  blockers: requireClass(styles.blockers, "hubResidents.module.css", "blockers"),
  phase: requireClass(styles.phase, "hubResidents.module.css", "phase"),
  compatibility: requireClass(styles.compatibility, "hubResidents.module.css", "compatibility"),
  archived: requireClass(styles.archived, "hubResidents.module.css", "archived"),
};

// formatTimeout converts a millisecond timeout value to a human-readable
// string. Unknown (no lifecycle) differs from disabled (explicit zero).
function formatTimeout(ms: number | null | undefined): string {
  if (ms == null) return "unknown";
  if (ms === 0) return "disabled";
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const remainingMins = minutes % 60;
  return remainingMins === 0 ? `${hours}h` : `${hours}h ${remainingMins}m`;
}

// effectiveTimeout returns a daemon row's effective timeout string.
// When lifecycle is absent (probe stale or incompatible) the timeout is
// unknown — distinct from a configured zero (automatic retirement disabled).
function effectiveTimeout(daemon: DaemonResident): string {
  if (!daemon.lifecycle) return "unknown";
  return formatTimeout(daemon.lifecycle.timeoutMillis);
}

/**
 * Settings → Hub → Discovered resident daemons.
 *
 * Shows the Hub's current resident-daemon inventory. Refreshes every
 * POLL_INTERVAL_MS milliseconds; on failure keeps old rows visible and marks
 * the data as stale. Retire and Force stop actions are identity-fenced:
 * the Hub will refuse a stale identity with CodeConflict before touching
 * any process.
 */
export function HubResidents() {
  const data = useDaemonResidentsStore((s) => s.data);
  const loading = useDaemonResidentsStore((s) => s.loading);
  const error = useDaemonResidentsStore((s) => s.error);
  const pending = useDaemonResidentsStore((s) => s.pending);

  // Force-stop safety: capture the identity at click time so a background
  // list refresh cannot silently change the target while the dialog is open.
  const [confirmForceStop, setConfirmForceStop] = useState<DaemonIdentity | null>(null);

  // Retire response tracking: needed to display "retiring" or blockers text
  // immediately after a retire RPC resolves, before the next list refresh
  // reflects the updated phase.
  const [retireResults, setRetireResults] = useState<Map<string, DaemonRetireResponse>>(() => new Map());

  // Polling lifetime: owned by this component, not the store.
  useEffect(() => {
    void daemonResidentsStore.getState().refresh();
    const id = setInterval(() => {
      void daemonResidentsStore.getState().refresh();
    }, POLL_INTERVAL_MS);
    return () => clearInterval(id);
  }, []);

  const handleRetire = useCallback(async (identity: DaemonIdentity) => {
    try {
      const result = await daemonResidentsStore.getState().retire(identity);
      setRetireResults((prev) => {
        const next = new Map(prev);
        next.set(identity.generation, result);
        return next;
      });
    } catch {
      // The store records loading/error state; nothing additional to surface here.
    }
  }, []);

  const handleForceStopConfirm = useCallback(() => {
    const identity = confirmForceStop;
    if (!identity) return;
    setConfirmForceStop(null);
    void daemonResidentsStore.getState().forceStop(identity);
  }, [confirmForceStop]);

  const headingId = "hub-residents-heading";

  return (
    <section className={CLASS.root} aria-labelledby={headingId}>
      <h3 id={headingId} className={CLASS.heading}>
        Discovered resident daemons
      </h3>

      <p className={CLASS.meta}>
        Daemons in the hub's rendezvous directory, including archived and incompatible sessions. Retire safely exits an
        idle resident; force stop signals the process immediately.
      </p>

      {/* Hub default idle timeout — always shown when data is available. */}
      {data !== null && (
        <p className={CLASS.meta}>Hub default idle timeout: {formatTimeout(data.defaultTimeoutMillis)}</p>
      )}

      {/* Stale-data indicator: shown when the last refresh failed but old
          rows are retained. Uses role="status" so screen readers announce it. */}
      {error !== null && (
        <p className={CLASS.errorBanner} role="status">
          Unable to refresh resident list (showing last known data): {friendlyErrorMessage(error)}
        </p>
      )}

      {/* Initial-load skeleton */}
      {loading && data === null && <Skeleton lines={3} />}

      {/* Empty state when the list loaded but no daemons were found */}
      {data !== null && data.daemons.length === 0 && !loading && (
        <EmptyState title="No resident daemons" hint="No daemons found in the hub's rendezvous directory." />
      )}

      {/* Resident table — horizontally scrollable on narrow viewports */}
      {data !== null && data.daemons.length > 0 && (
        <div className={CLASS.tableWrapper}>
          <table className={CLASS.table}>
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col">PID / Started</th>
                <th scope="col">Protocol / Compatibility</th>
                <th scope="col">Effective timeout</th>
                <th scope="col">Phase</th>
                <th scope="col">Actions</th>
              </tr>
            </thead>
            <tbody>
              {data.daemons.map((daemon) => {
                const retireResult = retireResults.get(daemon.identity.generation);
                const isPending = pending.has(daemon.identity.generation);

                // Display phase: prefer the retire-response lifecycle when
                // Accepted:true — the daemon is retiring and the server has
                // not yet removed it from the list.
                const displayPhase =
                  retireResult?.accepted === true ? retireResult.lifecycle.phase : (daemon.lifecycle?.phase ?? "—");

                return (
                  // aria-label is the row's accessible name, used by the
                  // rendered-row test to find it by daemon name.
                  <tr key={daemon.identity.generation} aria-label={daemon.name}>
                    <td>
                      <div>{daemon.name}</div>
                      {daemon.archived && <div className={CLASS.archived}>archived</div>}
                    </td>
                    <td>
                      <div>PID {daemon.identity.pid}</div>
                      <div className={CLASS.meta}>{daemon.identity.startedAt}</div>
                    </td>
                    <td>
                      <div>{daemon.protocol}</div>
                      <div className={CLASS.compatibility}>{daemon.compatibility}</div>
                    </td>
                    <td>{effectiveTimeout(daemon)}</td>
                    <td className={CLASS.phase}>
                      <div>{displayPhase}</div>
                      {/* Deadline if known */}
                      {daemon.lifecycle?.deadline && (
                        <div className={CLASS.meta}>until {daemon.lifecycle.deadline}</div>
                      )}
                    </td>
                    <td>
                      <div className={CLASS.actions}>
                        <Button
                          size="sm"
                          disabled={!daemon.canRetire || isPending}
                          onClick={() => void handleRetire(daemon.identity)}
                        >
                          Retire now
                        </Button>
                        <Button
                          size="sm"
                          disabled={!daemon.canForceStop || isPending}
                          onClick={() => setConfirmForceStop(daemon.identity)}
                        >
                          Force stop
                        </Button>
                        {/* Retire refusal blockers — shown when the most-recent
                            retire returned Accepted:false */}
                        {retireResult !== undefined && !retireResult.accepted && (
                          <div className={CLASS.blockers}>
                            Blocked by:{" "}
                            {retireResult.lifecycle.blockers.map((b) => b.category).join(", ") || "unknown reason"}
                          </div>
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {/* Force-stop confirmation dialog. confirmForceStop holds the identity
          that was rendered when the user clicked the button — never re-read
          from the current list — so a background refresh cannot retarget the
          action while this dialog is open. */}
      <ConfirmDialog
        open={confirmForceStop !== null}
        title="Force stop daemon?"
        confirmLabel="Force stop"
        busy={confirmForceStop !== null && pending.has(confirmForceStop.generation)}
        onConfirm={handleForceStopConfirm}
        onCancel={() => setConfirmForceStop(null)}
      >
        {confirmForceStop !== null && (
          <>
            Force stop PID {confirmForceStop.pid} ({confirmForceStop.ref}). Running work or watches may be interrupted.
            This cannot be undone without a Resume action.
          </>
        )}
      </ConfirmDialog>
    </section>
  );
}
