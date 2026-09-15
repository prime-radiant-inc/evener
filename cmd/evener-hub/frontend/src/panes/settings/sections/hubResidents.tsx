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
import { friendlyErrorMessage } from "@evener/appwire-client";
import type { DaemonBlocker, DaemonIdentity, DaemonResident, DaemonRetireResponse } from "@evener/appwire-client";
import { daemonResidentsStore, residentRowKey, useDaemonResidentsStore } from "../../../stores/daemonResidents";
import { Button, ConfirmDialog, EmptyState, Skeleton } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./hubResidents.module.css";

const POLL_INTERVAL_MS = 2000;

// PRE_RETIRE_PHASE is the only lifecycle phase from which the Hub accepts a
// retire (agent/retirement.go's TryClaim requires phase "resident"). A fresh
// roster snapshot that still reports it after an accepted retire is the Hub's
// cached pre-retire view — not evidence the retire was undone — so it must not
// supersede the accepted result.
const PRE_RETIRE_PHASE = "resident";

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
  // A positive timeout below one second must not floor into the "disabled"
  // sentinel. daemon_idle_timeout is a duration, so hub.toml can express a
  // sub-second deadline, and the daemon reads timeoutMillis === 0 as
  // "automatic retirement disabled" — rendering 500ms as "0s" would tell an
  // operator retirement is off while it is actually armed.
  if (ms < 1000) return "<1s";
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

// formatBlocker formats a DaemonBlocker for display. Its sessionId and/or
// delegateId is included alongside the category so the operator can identify
// the blocking entity (e.g. "turn (session-abc)" or "delegate (dlg-xyz)").
// A delegate blocker carries both its root session id and its delegate id, so
// both are rendered as "(session/delegate)"; dropping either would leave the
// operator unable to tell which delegate blocks retirement. The single-id arm
// uses logical OR, not ??, so an empty-string sessionId falls through to the
// delegate id instead of being treated as present and dropping it. (The Go
// producer's `omitempty` tag normally omits the field entirely, but the TS type
// still admits sessionId: "", and the both-ids arm above is already
// truthiness-based; ?? was the inconsistent arm.)
function formatBlocker(b: DaemonBlocker): string {
  if (b.sessionId && b.delegateId) return `${b.category} (${b.sessionId}/${b.delegateId})`;
  const id = b.sessionId || b.delegateId;
  return id ? `${b.category} (${id})` : b.category;
}

/**
 * The phase to show for one row. A retire response the Hub accepted wins: the
 * daemon is retiring and the server has not yet removed it from the list.
 * Otherwise fall back to the current probe, which still renders "—" while the
 * probe is stale or unknown.
 */
function displayPhaseFor(daemon: DaemonResident, retireResult: DaemonRetireResponse | undefined): string {
  if (retireResult?.accepted === true) return retireResult.lifecycle.phase;
  return daemon.lifecycle?.phase ?? "—";
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

  // Action error tracking: per-row friendly error message from a failed
  // retire or force-stop RPC.
  const [actionErrors, setActionErrors] = useState<Map<string, string>>(() => new Map());

  // Polling lifetime: owned by this component, not the store.
  useEffect(() => {
    void daemonResidentsStore.getState().refresh();
    const id = setInterval(() => {
      void daemonResidentsStore.getState().refresh();
    }, POLL_INTERVAL_MS);
    return () => clearInterval(id);
  }, []);

  // Prune per-row retire results and action errors against the latest list.
  // A refusal is NOT superseded by a fresh snapshot: the list's lifecycle
  // reports only in-flight leases, never the offline obligation behind an
  // accepted:false retire, so its empty blocker set is not evidence the refusal
  // resolved. A refusal is dropped when the row leaves the roster or its
  // lifecycle phase changes; a newly initiated action clears it in the handler
  // below. An accepted retire is dropped when the row leaves the roster or the
  // daemon reaches a genuinely different, concrete phase; the pre-retire phase
  // reported by the Hub's cached roster does not supersede it. actionErrors for
  // rows absent from the list are dropped so a departed daemon cannot leave a
  // stale error behind a later row that reuses its ref. Keyed by residentRowKey
  // — the row's generation, falling back to ref, the same key the rows and the
  // store's pending set use — so the prune consults the daemon that actually
  // owns each entry. A replaced generation at the same ref is absent under its
  // own key, which is exactly the "row left the roster" case, so its entry is
  // cleared rather than misapplied to its replacement. Uses the
  // functional-update form so the maps are not dependencies.
  useEffect(() => {
    if (!data) return;
    const daemonByKey = new Map(data.daemons.map((d) => [residentRowKey(d.identity), d]));
    setRetireResults((prev) => {
      if (prev.size === 0) return prev;
      let changed = false;
      const next = new Map(prev);
      for (const [key, result] of prev) {
        const daemon = daemonByKey.get(key);
        // A stale or unknown probe carries no lifecycle (the server only sets it
        // while the probe is fresh), so its absent phase must not read as a
        // phase change — that would discard a retire the Hub already accepted
        // while the daemon is exiting.
        const fresh = daemon !== undefined && daemon.probeState === "current";
        const phase = daemon?.lifecycle?.phase;
        const phaseChanged = fresh && phase !== result.lifecycle.phase;
        // A fresh snapshot does not supersede a refusal: the list's lifecycle
        // reports only in-flight leases, so an empty blocker set cannot prove
        // the offline obligation behind an accepted:false retire is gone. The
        // refusal survives until the phase changes or the row leaves the roster.
        //
        // An accepted retire is pruned on a phase change too, but NOT when the
        // differing phase is the pre-retire phase: the Hub's cached roster can
        // still report phase "resident" with a current probe while the daemon is
        // already retiring, and treating that as a change would revert the row
        // from "retiring" to "resident" and re-enable the Retire button. Only a
        // move to a genuinely different, concrete phase supersedes an accepted
        // result; a refusal keeps its exact prior rule.
        const superseded = result.accepted
          ? phaseChanged && phase !== undefined && phase !== PRE_RETIRE_PHASE
          : phaseChanged;
        if (!daemon || superseded) {
          next.delete(key);
          changed = true;
        }
      }
      return changed ? next : prev;
    });
    setActionErrors((prev) => {
      if (prev.size === 0) return prev;
      let changed = false;
      const next = new Map(prev);
      for (const key of prev.keys()) {
        if (!daemonByKey.has(key)) {
          next.delete(key);
          changed = true;
        }
      }
      return changed ? next : prev;
    });
  }, [data]); // retireResults intentionally omitted — read via functional-update prev

  const handleRetire = useCallback(async (identity: DaemonIdentity) => {
    const key = residentRowKey(identity);
    // Clear any previous action error and stored refusal for this row before
    // starting: a new action supersedes the older attempt's refusal (a fresh
    // refusal replaces it when this RPC resolves).
    setActionErrors((prev) => {
      const next = new Map(prev);
      next.delete(key);
      return next;
    });
    setRetireResults((prev) => {
      if (!prev.has(key)) return prev;
      const next = new Map(prev);
      next.delete(key);
      return next;
    });
    try {
      const result = await daemonResidentsStore.getState().retire(identity);
      setRetireResults((prev) => {
        const next = new Map(prev);
        next.set(key, result);
        return next;
      });
    } catch (err: unknown) {
      // Retire RPCs propagate errors directly; surface via friendly message in
      // the row. The store's error field tracks only refresh failures.
      setActionErrors((prev) => {
        const next = new Map(prev);
        next.set(key, friendlyErrorMessage(err));
        return next;
      });
    }
  }, []);

  const handleForceStopConfirm = useCallback(async () => {
    const identity = confirmForceStop;
    if (!identity) return;
    setConfirmForceStop(null);
    const key = residentRowKey(identity);
    // Clear any previous action error and stored retire refusal for this row
    // before starting: a new action supersedes the older attempt's outcome.
    setActionErrors((prev) => {
      const next = new Map(prev);
      next.delete(key);
      return next;
    });
    setRetireResults((prev) => {
      if (!prev.has(key)) return prev;
      const next = new Map(prev);
      next.delete(key);
      return next;
    });
    try {
      await daemonResidentsStore.getState().forceStop(identity);
    } catch (err: unknown) {
      // A CodeConflict (stale identity fenced by the Hub) or network failure
      // is surfaced via friendly message in the row. The Hub-side fence
      // prevents any unsafe action even on a conflict.
      setActionErrors((prev) => {
        const next = new Map(prev);
        next.set(key, friendlyErrorMessage(err));
        return next;
      });
    }
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

      {/* Hub default idle timeout with future-launch scope note — shown when
          data is available. The timeout governs future spawns and resumes;
          currently-running daemons keep their per-row effective timeout. */}
      {data !== null && (
        <p className={CLASS.meta}>
          Hub default idle timeout: {formatTimeout(data.defaultTimeoutMillis)}. Applies to future spawns and resumes;
          running daemons keep their effective timeout.
        </p>
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
      {/* Deliberately NOT gated on !loading, matching the table path below:
          the store sets loading on every 2s poll, and with zero daemons there
          is no skeleton to fall back to, so the gate blanked the panel. */}
      {data !== null && data.daemons.length === 0 && (
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
                // Per-row state is keyed the same way the row itself is: by the
                // row's own generation (see residentRowKey), never the ref alone,
                // so two live daemons sharing a ref keep separate state.
                const rowKey = residentRowKey(daemon.identity);
                const retireResult = retireResults.get(rowKey);
                const isPending = pending.has(rowKey);
                const actionError = actionErrors.get(rowKey);

                // Display phase: prefer the retire-response lifecycle when the
                // Hub accepted it (see displayPhaseFor).
                const displayPhase = displayPhaseFor(daemon, retireResult);

                // List-snapshot blockers from the current probe result.
                const snapshotBlockers = daemon.lifecycle?.blockers ?? [];

                // React keys must be unique among siblings, and residentRowKey is
                // the same generation-first key the per-row state above uses, so a
                // row's key and its state always agree. listDaemons dedups by
                // identity.generation, not ref, so two rows can legitimately share
                // a ref (an unconfirmed rendezvous alongside a confirmed one, or a
                // replacement in flight); the ref is the fallback for a row that
                // reports no generation. aria-label includes identity.ref so rows
                // sharing a display name remain distinguishable.
                return (
                  <tr key={rowKey} aria-label={`${daemon.name} ${daemon.identity.ref}`}>
                    <td>
                      <div>{daemon.name}</div>
                      {/* Root reference — shown under the name per spec. Two
                          daemons sharing a name are distinguished by their ref. */}
                      <div className={CLASS.meta}>{daemon.identity.ref}</div>
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
                          disabled={
                            !daemon.canRetire ||
                            isPending ||
                            retireResult?.accepted === true ||
                            displayPhase === "retiring"
                          }
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
                        {/* List-snapshot blockers — visible before any retire attempt
                            so the operator can see why a row is not retirable. Once a
                            retire result is displayed, the refusal's own blockers
                            supersede this snapshot: the claim snapshot carries the same
                            in-flight leases, so rendering both duplicates the line. */}
                        {(retireResult === undefined || retireResult.accepted) && snapshotBlockers.length > 0 && (
                          <div className={CLASS.blockers}>
                            {"Blocked by: "}
                            {snapshotBlockers.map(formatBlocker).join(", ")}
                          </div>
                        )}
                        {/* Retire refusal blockers — shown when the most-recent
                            retire returned Accepted:false. Cleared only when the
                            row's lifecycle phase changes, the daemon leaves the
                            roster, or a new action is initiated. */}
                        {retireResult !== undefined && !retireResult.accepted && (
                          <div className={CLASS.blockers}>
                            {"Blocked by: "}
                            {retireResult.lifecycle.blockers.map(formatBlocker).join(", ") || "unknown reason"}
                          </div>
                        )}
                        {/* Per-row action error — shown when a retire or force-stop
                            RPC fails. role="alert" for immediate screen-reader
                            announcement. */}
                        {actionError !== undefined && (
                          <div className={CLASS.errorBanner} role="alert">
                            {actionError}
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
        busy={confirmForceStop !== null && pending.has(residentRowKey(confirmForceStop))}
        onConfirm={() => void handleForceStopConfirm()}
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
