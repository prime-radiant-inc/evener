// hubResidents.tsx — "Resident daemons" sub-section of Settings -> Hub.
// Polls the Hub's resident-daemon inventory every two seconds (pausing while
// hidden or while a hub restart is in progress), renders compact ListRows with
// a right/bottom detail Sheet, and keeps Retire and Force stop identity-fenced.

import type { DaemonBlocker, DaemonIdentity, DaemonResident, DaemonRetireResponse } from "@evener/appwire-client";
import { friendlyErrorMessage } from "@evener/appwire-client";
import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { usePaneDocument } from "../../../shell/paneDocument";
import { useIsMobile } from "../../../shell/useIsMobile";
import { useVisibleInterval } from "../../../shell/useVisibleInterval";
import { useConnectionStore } from "../../../stores/connection";
import { daemonResidentsStore, residentRowKey, useDaemonResidentsStore } from "../../../stores/daemonResidents";
import { useHubUpdateStore } from "../../../stores/hubUpdate";
import surfaceStyles from "../../../styles/surface.module.css";
import {
  Badge,
  Button,
  type CadenceState,
  Card,
  Chevron,
  Chip,
  ConfirmDialog,
  EmptyState,
  ListRow,
  PropertyList,
  Sheet,
  Skeleton,
  StatusDot,
  useFocusRehome,
  useToasts,
} from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./hubResidents.module.css";
import { formatHubStamp } from "./hubStamp";

const POLL_INTERVAL_MS = 2000;

// PRE_RETIRE_PHASE is the only lifecycle phase from which the Hub accepts a
// retire (agent/retirement.go's TryClaim requires phase "resident"). A fresh
// roster snapshot that still reports it after an accepted retire is the Hub's
// cached pre-retire view — not evidence the retire was undone — so it must not
// supersede the accepted result.
const PRE_RETIRE_PHASE = "resident";

const CLASS = {
  root: requireClass(styles.root, "hubResidents.module.css", "root"),
  header: requireClass(styles.header, "hubResidents.module.css", "header"),
  titleGroup: requireClass(styles.titleGroup, "hubResidents.module.css", "titleGroup"),
  timeout: requireClass(styles.timeout, "hubResidents.module.css", "timeout"),
  list: requireClass(styles.list, "hubResidents.module.css", "list"),
  listRestarting: requireClass(styles.listRestarting, "hubResidents.module.css", "listRestarting"),
  rowItem: requireClass(styles.rowItem, "hubResidents.module.css", "rowItem"),
  daemonName: requireClass(styles.daemonName, "hubResidents.module.css", "daemonName"),
  metaLine: requireClass(styles.metaLine, "hubResidents.module.css", "metaLine"),
  pid: requireClass(styles.pid, "hubResidents.module.css", "pid"),
  metaBlocker: requireClass(styles.metaBlocker, "hubResidents.module.css", "metaBlocker"),
  rowTrailing: requireClass(styles.rowTrailing, "hubResidents.module.css", "rowTrailing"),
  chevron: requireClass(styles.chevron, "hubResidents.module.css", "chevron"),
  sheetBody: requireClass(styles.sheetBody, "hubResidents.module.css", "sheetBody"),
  statusRow: requireClass(styles.statusRow, "hubResidents.module.css", "statusRow"),
  blockerBox: requireClass(styles.blockerBox, "hubResidents.module.css", "blockerBox"),
  blockerHeading: requireClass(styles.blockerHeading, "hubResidents.module.css", "blockerHeading"),
  blockerItem: requireClass(styles.blockerItem, "hubResidents.module.css", "blockerItem"),
  sheetFooter: requireClass(styles.sheetFooter, "hubResidents.module.css", "sheetFooter"),
  sheetFooterActions: requireClass(styles.sheetFooterActions, "hubResidents.module.css", "sheetFooterActions"),
  sheetFooterNote: requireClass(styles.sheetFooterNote, "hubResidents.module.css", "sheetFooterNote"),
};

const HEADING = `${surfaceStyles.settingsSectionHeading} ${surfaceStyles.settingsSectionHeadingBody}`;

// formatTimeout converts a millisecond timeout value to a human-readable
// string. Unknown (no lifecycle) differs from disabled (explicit zero).
function formatTimeout(ms: number | null | undefined): string {
  if (ms == null) return "unknown";
  if (ms === 0) return "disabled";
  // A positive timeout below one second must not floor into the "disabled"
  // sentinel (0 disables automatic retirement).
  if (ms < 1000) return "<1s";
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const remainingMins = minutes % 60;
  return remainingMins === 0 ? `${hours}h` : `${hours}h ${remainingMins}m`;
}

function formatDurationShort(ms: number): string {
  const clamped = Math.max(0, ms);
  const seconds = Math.floor(clamped / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.floor(hours / 24);
  return `${days}d`;
}

function formatUptime(startedAt: string, nowMs: number): string | null {
  const parsed = Date.parse(startedAt);
  if (Number.isNaN(parsed)) return null;
  return `up ${formatDurationShort(nowMs - parsed)}`;
}

function formatDeadlineCountdown(deadline: string | undefined, nowMs: number): string | null {
  if (!deadline) return null;
  const parsed = Date.parse(deadline);
  if (Number.isNaN(parsed)) return null;
  const remaining = parsed - nowMs;
  if (remaining <= 1000) return "retiring soon";
  return `retires in ${formatDurationShort(remaining)}`;
}

function formatStamp(iso: string | undefined): string | null {
  if (!iso) return null;
  return formatHubStamp(iso) ?? iso;
}

function effectiveTimeout(daemon: DaemonResident): string {
  if (!daemon.lifecycle) return "unknown";
  return formatTimeout(daemon.lifecycle.timeoutMillis);
}

// formatBlocker includes sessionId and/or delegateId alongside the category so
// the operator can identify the blocking entity.
function formatBlocker(b: DaemonBlocker): string {
  if (b.sessionId && b.delegateId) return `${b.category} (${b.sessionId}/${b.delegateId})`;
  const id = b.sessionId || b.delegateId;
  return id ? `${b.category} (${id})` : b.category;
}

function summarizeBlockers(blockers: readonly DaemonBlocker[]): string {
  if (blockers.length === 0) return "";
  let turns = 0;
  let delegates = 0;
  let others = 0;
  let firstOtherCategory: string | undefined;
  for (const b of blockers) {
    if (b.category === "turn") turns += 1;
    else if (b.category === "delegate") delegates += 1;
    else {
      others += 1;
      if (firstOtherCategory === undefined) firstOtherCategory = b.category;
    }
  }
  const parts: string[] = [];
  if (turns > 0) parts.push(turns === 1 ? "active turn" : `${turns} active turns`);
  if (delegates > 0) parts.push(delegates === 1 ? "1 delegate" : `${delegates} delegates`);
  if (others > 0) parts.push(others === 1 ? (firstOtherCategory ?? "1 blocker") : `${others} blockers`);
  return parts.join(", ");
}

function shortProtocol(protocol: string): string {
  return protocol.replace(/^evener-appwire-/, "");
}

function displayPhaseFor(daemon: DaemonResident, retireResult: DaemonRetireResponse | undefined): string {
  if (retireResult?.accepted === true) return retireResult.lifecycle.phase;
  return daemon.lifecycle?.phase ?? "—";
}

function effectiveBlockersFor(daemon: DaemonResident, retireResult: DaemonRetireResponse | undefined): DaemonBlocker[] {
  if (retireResult !== undefined && !retireResult.accepted) {
    return retireResult.lifecycle.blockers;
  }
  return daemon.lifecycle?.blockers ?? [];
}

function dotStateFor(daemon: DaemonResident, displayPhase: string, blockers: readonly DaemonBlocker[]): CadenceState {
  if (daemon.compatibility === "incompatible") return "failed";
  if (displayPhase === "retiring" || daemon.probeState === "stale") return "needs-you";
  if (blockers.length > 0) return "working";
  return "idle";
}

function rowChips(daemon: DaemonResident, displayPhase: string) {
  const showIncompatible = daemon.compatibility === "incompatible";
  const showStale = !showIncompatible && daemon.probeState === "stale";
  const showArchived = daemon.archived;
  const showRetiring = displayPhase === "retiring";
  if (!showIncompatible && !showStale && !showArchived && !showRetiring) return undefined;
  return (
    <>
      {showIncompatible && <Chip tone="danger">{`Incompatible · ${shortProtocol(daemon.protocol)}`}</Chip>}
      {showStale && <Chip tone="attention">Stale probe</Chip>}
      {showArchived && (
        <Chip tone="neutral" variant="quiet">
          Archived
        </Chip>
      )}
      {showRetiring && <Chip tone="attention">Retiring</Chip>}
    </>
  );
}

/**
 * Settings -> Hub -> Resident daemons.
 *
 * Shows the Hub's resident-daemon inventory as compact ListRows that drill
 * into a detail Sheet. Pauses polling while the hub is restarting so an
 * expected reconnect never flashes red errors, and routes genuine refresh or
 * action failures through system toasts.
 */
export function HubResidents() {
  const data = useDaemonResidentsStore((s) => s.data);
  const loading = useDaemonResidentsStore((s) => s.loading);
  const error = useDaemonResidentsStore((s) => s.error);
  const pending = useDaemonResidentsStore((s) => s.pending);
  const hubRestarting = useHubUpdateStore((s) => s.restarting || s.applying);
  const connectionState = useConnectionStore((s) => s.state);
  const isMobile = useIsMobile();
  const toasts = useToasts();
  const rootRef = useRef<HTMLElement>(null);
  useFocusRehome(rootRef);

  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  // Capture exact DaemonIdentity at click time so a background list refresh
  // cannot silently retarget Force stop while the dialog is open.
  const [confirmForceStop, setConfirmForceStop] = useState<DaemonIdentity | null>(null);
  // Per-generation retire responses (accepted "retiring" or refused blockers).
  const [retireResults, setRetireResults] = useState<Map<string, DaemonRetireResponse>>(() => new Map());
  const lastRefreshErrorRef = useRef<string | null>(null);

  useEffect(() => {
    if (!hubRestarting && connectionState === "ready") {
      void daemonResidentsStore.getState().refresh();
    }
  }, [hubRestarting, connectionState]);

  const ownerDocument = usePaneDocument();
  useVisibleInterval(
    () => void daemonResidentsStore.getState().refresh(),
    POLL_INTERVAL_MS,
    !hubRestarting && connectionState === "ready",
    ownerDocument,
  );

  // Surface refresh errors via deduplicated system toasts, suppressed during
  // expected hub restarts. Only clear the dedupe slot once a refresh actually
  // settles without error (runRefresh resets error to null while loading).
  useEffect(() => {
    if (hubRestarting) return;
    if (error !== null) {
      if (error !== lastRefreshErrorRef.current) {
        lastRefreshErrorRef.current = error;
        toasts.push("error", `Couldn't refresh resident daemons: ${error}`);
      }
    } else if (!loading) {
      lastRefreshErrorRef.current = null;
    }
  }, [error, loading, hubRestarting, toasts]);

  // Prune per-generation retire results and auto-close the detail sheet when
  // its daemon leaves the roster.
  useEffect(() => {
    if (!data) return;
    const daemonByKey = new Map(data.daemons.map((d) => [residentRowKey(d.identity), d]));
    setRetireResults((prev) => {
      if (prev.size === 0) return prev;
      let changed = false;
      const next = new Map(prev);
      for (const [key, result] of prev) {
        const daemon = daemonByKey.get(key);
        const fresh = daemon !== undefined && daemon.probeState === "current";
        const phase = daemon?.lifecycle?.phase;
        const phaseChanged = fresh && phase !== result.lifecycle.phase;
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
    setSelectedKey((prev) => (prev !== null && !daemonByKey.has(prev) ? null : prev));
  }, [data]);

  const clearRetireResult = useCallback((key: string) => {
    setRetireResults((prev) => {
      if (!prev.has(key)) return prev;
      const next = new Map(prev);
      next.delete(key);
      return next;
    });
  }, []);

  const handleRetire = useCallback(
    async (daemon: DaemonResident) => {
      const key = residentRowKey(daemon.identity);
      clearRetireResult(key);
      try {
        const result = await daemonResidentsStore.getState().retire(daemon.identity);
        setRetireResults((prev) => {
          const next = new Map(prev);
          next.set(key, result);
          return next;
        });
        if (!result.accepted) {
          const summary = summarizeBlockers(result.lifecycle.blockers);
          toasts.push(
            "warning",
            summary
              ? `Couldn't retire ${daemon.name || daemon.identity.ref}: blocked by ${summary}.`
              : `Couldn't retire ${daemon.name || daemon.identity.ref}.`,
          );
        }
      } catch (err: unknown) {
        toasts.push("error", `Retire failed: ${friendlyErrorMessage(err)}`);
      }
    },
    [clearRetireResult, toasts],
  );

  const handleForceStopConfirm = useCallback(async () => {
    const identity = confirmForceStop;
    if (!identity) return;
    setConfirmForceStop(null);
    clearRetireResult(residentRowKey(identity));
    try {
      await daemonResidentsStore.getState().forceStop(identity);
    } catch (err: unknown) {
      toasts.push("error", `Force stop failed: ${friendlyErrorMessage(err)}`);
    }
  }, [clearRetireResult, confirmForceStop, toasts]);

  const headingId = "hub-residents-heading";
  const nowMs = Date.now();

  const nameCounts = useMemo(() => {
    const counts = new Map<string, number>();
    if (!data) return counts;
    for (const d of data.daemons) {
      const label = d.name || d.identity.ref;
      counts.set(label, (counts.get(label) ?? 0) + 1);
    }
    return counts;
  }, [data]);

  const selectedDaemon =
    data !== null && selectedKey !== null
      ? (data.daemons.find((d) => residentRowKey(d.identity) === selectedKey) ?? null)
      : null;
  const selectedRetireResult = selectedKey !== null ? retireResults.get(selectedKey) : undefined;
  const selectedPending = selectedKey !== null && pending.has(selectedKey);
  const selectedDisplayPhase = selectedDaemon ? displayPhaseFor(selectedDaemon, selectedRetireResult) : "—";
  const selectedBlockers = selectedDaemon ? effectiveBlockersFor(selectedDaemon, selectedRetireResult) : [];
  const selectedRetireDisabled =
    selectedDaemon === null ||
    !selectedDaemon.canRetire ||
    selectedPending ||
    selectedRetireResult?.accepted === true ||
    selectedDisplayPhase === "retiring" ||
    hubRestarting;

  let selectedRetireReason: string | null = null;
  if (selectedDaemon !== null) {
    if (selectedDisplayPhase === "retiring") selectedRetireReason = "Daemon is already retiring.";
    else if (selectedDaemon.compatibility === "incompatible")
      selectedRetireReason = "Incompatible protocol. Use Force stop if it needs to exit.";
    else if (selectedDaemon.archived)
      selectedRetireReason = "Archived sessions retire automatically on their shortened timeout.";
    else if (selectedBlockers.length > 0) selectedRetireReason = "Active work is holding this daemon open.";
    else if (selectedDaemon.probeState !== "current")
      selectedRetireReason = "Daemon probe is not current, so safe retire is unavailable.";
  }

  return (
    <section ref={rootRef} className={CLASS.root} aria-labelledby={headingId}>
      <div className={CLASS.header}>
        <div className={CLASS.titleGroup}>
          <h3 id={headingId} className={HEADING}>
            Resident daemons
          </h3>
          {data !== null && <Badge count={data.daemons.length} />}
        </div>

        {data !== null && (
          <span
            className={CLASS.timeout}
            data-testid="hub-default-timeout"
            title="Default idle timeout for future spawns and resumes. Running daemons keep their own effective timeout."
          >
            {hubRestarting ? "Reconnecting…" : `Idle timeout ${formatTimeout(data.defaultTimeoutMillis)}`}
          </span>
        )}
      </div>

      {loading && data === null && <Skeleton lines={3} />}

      {data !== null && data.daemons.length === 0 && (
        <Card variant="outlined" padding="none">
          <EmptyState
            size="compact"
            reason="empty"
            title="No resident daemons"
            hint="Active session processes appear here while running."
          />
        </Card>
      )}

      {data !== null && data.daemons.length > 0 && (
        <Card variant="outlined" padding="none">
          <ul className={`${CLASS.list}${hubRestarting ? ` ${CLASS.listRestarting}` : ""}`}>
            {data.daemons.map((daemon) => {
              const rowKey = residentRowKey(daemon.identity);
              const retireResult = retireResults.get(rowKey);
              const isPending = pending.has(rowKey);
              const displayPhase = displayPhaseFor(daemon, retireResult);
              const blockers = effectiveBlockersFor(daemon, retireResult);
              const displayName = daemon.name || daemon.identity.ref;
              const duplicateName = (nameCounts.get(displayName) ?? 0) > 1;
              const uptime = formatUptime(daemon.identity.startedAt, nowMs);

              let statusText: string | null = null;
              let statusIsBlocker = false;
              if (daemon.compatibility === "incompatible") {
                statusText = `probe ${daemon.probeState}`;
              } else if (displayPhase === "retiring") {
                statusText = "draining";
              } else if (blockers.length > 0) {
                statusText = summarizeBlockers(blockers);
                statusIsBlocker = true;
              } else {
                const countdown = formatDeadlineCountdown(daemon.lifecycle?.deadline, nowMs);
                if (countdown) {
                  statusText = countdown;
                } else if (daemon.lifecycle?.timeoutMillis === 0) {
                  statusText = "auto-retire off";
                } else if (
                  daemon.lifecycle !== undefined &&
                  daemon.lifecycle.timeoutMillis !== data.defaultTimeoutMillis
                ) {
                  statusText = `timeout ${formatTimeout(daemon.lifecycle.timeoutMillis)}`;
                }
              }

              let rowAction: ReactNode;
              if (
                daemon.canRetire &&
                displayPhase === "resident" &&
                retireResult?.accepted !== true &&
                blockers.length === 0
              ) {
                rowAction = (
                  <Button
                    variant="secondary"
                    size="xs"
                    disabled={isPending || hubRestarting}
                    onClick={() => void handleRetire(daemon)}
                  >
                    Retire
                  </Button>
                );
              } else if (daemon.compatibility === "incompatible" && daemon.canForceStop) {
                rowAction = (
                  <Button
                    variant="dangerQuiet"
                    size="xs"
                    disabled={isPending || hubRestarting}
                    onClick={() => setConfirmForceStop(daemon.identity)}
                  >
                    Force stop
                  </Button>
                );
              }

              return (
                <li key={rowKey} className={CLASS.rowItem}>
                  <ListRow
                    variant="plain"
                    leading={<StatusDot state={dotStateFor(daemon, displayPhase, blockers)} decorative />}
                    label={<span className={CLASS.daemonName}>{displayName}</span>}
                    chips={rowChips(daemon, displayPhase)}
                    meta={
                      <span className={CLASS.metaLine}>
                        <span className={CLASS.pid}>{`PID ${daemon.identity.pid}`}</span>
                        {uptime !== null && <span>{uptime}</span>}
                        {duplicateName && <span className={CLASS.pid}>{daemon.identity.ref}</span>}
                        {statusText !== null && (
                          <span className={statusIsBlocker ? CLASS.metaBlocker : undefined}>{statusText}</span>
                        )}
                      </span>
                    }
                    aria-label={`${displayName} ${daemon.identity.ref}`}
                    onSelect={() => setSelectedKey(rowKey)}
                    trailingAction={
                      <span className={CLASS.rowTrailing}>
                        {rowAction}
                        <span className={CLASS.chevron} aria-hidden="true">
                          <Chevron direction="right" />
                        </span>
                      </span>
                    }
                  />
                </li>
              );
            })}
          </ul>
        </Card>
      )}

      <Sheet
        side={isMobile ? "bottom" : "right"}
        open={selectedDaemon !== null}
        onClose={() => setSelectedKey(null)}
        title={selectedDaemon ? selectedDaemon.name || selectedDaemon.identity.ref : ""}
        footer={
          selectedDaemon ? (
            <div className={CLASS.sheetFooter}>
              <div className={CLASS.sheetFooterActions}>
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={selectedRetireDisabled}
                  onClick={() => void handleRetire(selectedDaemon)}
                >
                  Retire now
                </Button>
                <Button
                  variant="dangerQuiet"
                  size="sm"
                  disabled={!selectedDaemon.canForceStop || selectedPending || hubRestarting}
                  onClick={() => setConfirmForceStop(selectedDaemon.identity)}
                >
                  Force stop…
                </Button>
              </div>
              {selectedRetireReason !== null && <p className={CLASS.sheetFooterNote}>{selectedRetireReason}</p>}
            </div>
          ) : undefined
        }
      >
        {selectedDaemon && (
          <div className={CLASS.sheetBody}>
            <div className={CLASS.statusRow}>
              {rowChips(selectedDaemon, selectedDisplayPhase) ??
                (selectedBlockers.length > 0 ? (
                  <Chip tone="alive">Active</Chip>
                ) : (
                  <Chip tone="neutral" variant="quiet">
                    Idle
                  </Chip>
                ))}
            </div>

            {selectedBlockers.length > 0 && (
              <div className={CLASS.blockerBox} data-testid="resident-blockers">
                <span className={CLASS.blockerHeading}>Blocking retirement</span>
                {selectedBlockers.map((blocker) => {
                  const formatted = formatBlocker(blocker);
                  return (
                    <span
                      key={`${blocker.category}:${blocker.sessionId ?? ""}:${blocker.delegateId ?? ""}`}
                      className={CLASS.blockerItem}
                    >
                      {formatted}
                    </span>
                  );
                })}
              </div>
            )}

            <PropertyList
              rows={[
                { label: "Session ref", value: selectedDaemon.identity.ref, mono: true, wrap: true },
                { label: "PID", value: String(selectedDaemon.identity.pid), mono: true },
                {
                  label: "Started",
                  value: formatStamp(selectedDaemon.identity.startedAt) ?? selectedDaemon.identity.startedAt,
                },
                { label: "Phase", value: selectedDisplayPhase },
                {
                  label: "Idle timeout",
                  value:
                    data !== null &&
                    selectedDaemon.lifecycle !== undefined &&
                    selectedDaemon.lifecycle.timeoutMillis === data.defaultTimeoutMillis
                      ? `${effectiveTimeout(selectedDaemon)} (hub default)`
                      : effectiveTimeout(selectedDaemon),
                },
                {
                  label: "Retire deadline",
                  value: selectedDaemon.lifecycle?.deadline
                    ? (formatStamp(selectedDaemon.lifecycle.deadline) ?? selectedDaemon.lifecycle.deadline)
                    : "—",
                },
                {
                  label: "Protocol",
                  value: `${selectedDaemon.protocol} · ${selectedDaemon.compatibility}`,
                  mono: true,
                  wrap: true,
                },
                { label: "Probe", value: selectedDaemon.probeState },
              ]}
            />
          </div>
        )}
      </Sheet>

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
