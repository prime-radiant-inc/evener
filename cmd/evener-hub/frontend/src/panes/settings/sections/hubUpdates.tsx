import { useEffect, useState } from "react";
import { useConnectionStore } from "../../../stores/connection";
import { hubUpdateStore, RESTART_TIMEOUT_MS, type UpdateChannel, useHubUpdateStore } from "../../../stores/hubUpdate";
import { useSettingsOverviewStore } from "../../../stores/settingsOverview";
import { Button, ConfirmDialog, InlineNotice, Loader, RadioGroup } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./hubUpdates.module.css";
import { Code, FieldDim } from "./settingsField";

const CLASS = {
  root: requireClass(styles.root, "hubUpdates.module.css", "root"),
  heading: requireClass(styles.heading, "hubUpdates.module.css", "heading"),
  status: requireClass(styles.status, "hubUpdates.module.css", "status"),
  actions: requireClass(styles.actions, "hubUpdates.module.css", "actions"),
};

const CHANNEL_OPTIONS = [
  { value: "release", label: "Release" },
  { value: "snapshot", label: "Snapshot" },
];

function isChannel(value: string | undefined): value is UpdateChannel {
  return value === "release" || value === "snapshot";
}

function displayBuild(version: string | undefined): string {
  if (version === undefined || version === "") return "unknown";
  const dirty = version.endsWith("-dirty");
  const sha = dirty ? version.slice(0, -"-dirty".length) : version;
  return sha.slice(0, 7) + (dirty ? "-dirty" : "");
}

/**
 * Settings -> Hub -> Updates. Install and Restart are separate actions:
 * Install writes a verified artifact without touching the running process;
 * Restart execs the binary already on disk. A dev build has no Install
 * control, because its binary comes from an explicit local build.
 *
 * The check carries the on-disk build, the process incarnation, and whether
 * this hub supports restart. The host confirms a restart by comparing the
 * incarnation, not the version, so a rebuild at the same commit still reloads.
 */
export function HubUpdates() {
  const hub = useSettingsOverviewStore((s) => s.data?.hub);
  const channel = useHubUpdateStore((s) => s.channel);
  const check = useHubUpdateStore((s) => s.check);
  const checking = useHubUpdateStore((s) => s.checking);
  const checkError = useHubUpdateStore((s) => s.checkError);
  const installing = useHubUpdateStore((s) => s.installing);
  const installError = useHubUpdateStore((s) => s.installError);
  const installReconcile = useHubUpdateStore((s) => s.installReconcile);
  const applying = useHubUpdateStore((s) => s.applying);
  const applyError = useHubUpdateStore((s) => s.applyError);
  const restarting = useHubUpdateStore((s) => s.restarting);
  const restartError = useHubUpdateStore((s) => s.restartError);
  const restartTimedOut = useHubUpdateStore((s) => s.restartTimedOut);
  const [confirming, setConfirming] = useState(false);

  const buildChannel = hub?.buildChannel;
  const isDev = buildChannel === "dev";
  const connectionState = useConnectionStore((s) => s.state);

  // Seed the selector from the running binary once the overview is here.
  useEffect(() => {
    if (channel === null && isChannel(buildChannel)) {
      hubUpdateStore.getState().setChannel(buildChannel);
    }
  }, [channel, buildChannel]);

  // Check whenever a channel is selected (mount and every change), and when
  // the connection becomes ready again: the cached answer belongs to the
  // previous connection and may name a different hub's build.
  useEffect(() => {
    if (channel !== null && connectionState === "ready") void hubUpdateStore.getState().runCheck();
  }, [channel, connectionState]);

  // Dev builds have no channel, but the check is still what reports the
  // on-disk build and whether restart is available. Retry when the
  // connection becomes ready after a failed check: a check that died with
  // the socket leaves check null, and the dev UI would otherwise show
  // neither Restart nor the failure.
  useEffect(() => {
    if (hub && buildChannel === "dev" && connectionState === "ready") {
      void hubUpdateStore.getState().runCheck();
    }
  }, [hub, buildChannel, connectionState]);

  // A lost install response is reconciled once the socket is ready again.
  useEffect(() => {
    if (installReconcile && connectionState === "ready") void hubUpdateStore.getState().runCheck();
  }, [installReconcile, connectionState]);

  if (!hub) return null;

  const running = check?.currentVersion ?? hub.commit;
  const onDisk = check?.onDiskCommit;
  const channelError = check?.checkError ?? null;
  const restartSupported = check?.restartSupported === true;
  // An older hub omits the field entirely; a current hub that cannot restart
  // sends false. The generated type requires the field, so read it as optional
  // to tell those two cases apart. Only the absence means the deprecated
  // combined action.
  const restartCapabilityPresent =
    check !== null && (check as { restartSupported?: boolean }).restartSupported !== undefined;
  const installAvailable = check?.installAvailable ?? check?.updateAvailable ?? false;
  const legacyApply =
    check !== null && !restartCapabilityPresent && (check.applicable ?? false) && (check.updateAvailable ?? false);
  const busy = checking || installing || applying || restarting;

  const updateState = restarting
    ? "restarting"
    : restartTimedOut
      ? "restart-timed-out"
      : checking
        ? "checking"
        : checkError !== null
          ? "check-failed"
          : channelError !== null
            ? "check-failed"
            : check === null
              ? undefined
              : check.updateAvailable
                ? "update-available"
                : "up-to-date";

  const restartDialog = (
    <ConfirmDialog
      open={confirming}
      title={legacyApply ? "Update and restart the hub?" : "Restart the hub?"}
      confirmLabel={legacyApply ? "Yes, update and restart" : "Yes, restart"}
      busy={legacyApply ? applying : restarting}
      onConfirm={() => {
        setConfirming(false);
        if (legacyApply) void hubUpdateStore.getState().apply();
        else void hubUpdateStore.getState().restart();
      }}
      onCancel={() => setConfirming(false)}
    >
      {legacyApply
        ? "The hub installs the update and restarts. Open panes reconnect; running sessions keep going."
        : `The hub restarts into ${displayBuild(onDisk ?? running)} (running ${displayBuild(running)}). Open panes reconnect; running sessions keep going.`}
    </ConfirmDialog>
  );

  return (
    <section className={CLASS.root} aria-labelledby="hub-updates-heading">
      <h3 id="hub-updates-heading" className={CLASS.heading}>
        Updates
      </h3>
      <p className={CLASS.status}>
        Running {displayBuild(running)}
        {buildChannel !== undefined && <FieldDim> · {buildChannel}</FieldDim>}
      </p>
      {check !== null && (
        <p className={CLASS.status} data-testid="hub-on-disk-build">
          On disk {displayBuild(onDisk)}
          {check.restartPending && <FieldDim> · restart pending</FieldDim>}
        </p>
      )}
      {check?.onDiskError !== undefined && (
        <InlineNotice tone="danger">Can't read the on-disk build: {check.onDiskError}</InlineNotice>
      )}

      {check?.applicable === true &&
        check.installTargetMatchesLaunch === false &&
        check.installTarget !== undefined && (
          <InlineNotice tone="danger" data-testid="hub-install-target-mismatch">
            Install is unavailable: it would write {check.installTarget}, but this hub runs from a different binary. Use
            the managed installation.
          </InlineNotice>
        )}

      {check?.restartPending === true && (
        <InlineNotice data-testid="hub-restart-pending">
          Installed {displayBuild(onDisk)}, running {displayBuild(running)}. Restart to apply.
        </InlineNotice>
      )}

      {isDev && (
        <p className={CLASS.status} data-testid="hub-update-dev-note">
          <FieldDim>
            Dev build. Rebuild with <Code>make build-hub</Code>, then restart.
          </FieldDim>
        </p>
      )}
      {isDev && checkError !== null && (
        <InlineNotice tone="danger" data-testid="hub-dev-check-error">
          Couldn't read the hub's build: {checkError}
        </InlineNotice>
      )}

      {!isDev && channel !== null && (
        <>
          <RadioGroup
            label="Channel"
            value={channel}
            onChange={(value) => {
              if (isChannel(value)) hubUpdateStore.getState().setChannel(value);
            }}
            options={CHANNEL_OPTIONS}
            disabled={busy}
          />

          <InlineNotice data-testid="hub-update-status" data-state={updateState}>
            {restarting && <Loader label="Restarting hub" />}
            {!restarting &&
              restartTimedOut &&
              `The hub didn't come back within ${RESTART_TIMEOUT_MS / 1000}s. Check its logs.`}
            {!restarting && !restartTimedOut && checking && "Checking…"}
            {!restarting && !restartTimedOut && !checking && checkError !== null && (
              <>Couldn't check for updates: {checkError}</>
            )}
            {!restarting && !restartTimedOut && !checking && checkError === null && channelError !== null && (
              <>Couldn't check for updates: {channelError}</>
            )}
            {!restarting &&
              !restartTimedOut &&
              !checking &&
              checkError === null &&
              channelError === null &&
              check !== null &&
              (check.updateAvailable
                ? `Update available: ${check.channel} ${displayBuild(check.latestCommit ?? "")}, running ${displayBuild(check.currentVersion)}`
                : `Up to date on ${check.channel} (${displayBuild(check.latestCommit ?? "")})`)}
          </InlineNotice>
          {installing && <Loader label="Installing update" />}
          {installError !== null && <InlineNotice tone="danger">Install failed: {installError}</InlineNotice>}
          {applyError !== null && <InlineNotice tone="danger">Update failed: {applyError}</InlineNotice>}
          {restartError !== null && <InlineNotice tone="danger">Restart failed: {restartError}</InlineNotice>}

          <div className={CLASS.actions}>
            <Button size="sm" onClick={() => void hubUpdateStore.getState().runCheck()} disabled={busy}>
              Check for updates
            </Button>
            {legacyApply ? (
              <Button size="sm" onClick={() => setConfirming(true)} disabled={busy}>
                Update and restart
              </Button>
            ) : (
              <>
                <Button
                  size="sm"
                  onClick={() => void hubUpdateStore.getState().install()}
                  disabled={!installAvailable || busy}
                >
                  Install update
                </Button>
                {restartSupported && (
                  <Button size="sm" onClick={() => setConfirming(true)} disabled={busy}>
                    Restart hub
                  </Button>
                )}
              </>
            )}
          </div>
        </>
      )}

      {isDev && (
        <>
          <div className={CLASS.actions}>
            <Button size="sm" onClick={() => void hubUpdateStore.getState().runCheck()} disabled={busy}>
              Check again
            </Button>
            {restartSupported && (
              <Button size="sm" onClick={() => setConfirming(true)} disabled={busy}>
                Restart hub
              </Button>
            )}
          </div>
          {restarting && (
            <div data-testid="hub-dev-restarting">
              <Loader label="Restarting hub" />
            </div>
          )}
          {!restarting && restartTimedOut && (
            <InlineNotice tone="danger" data-testid="hub-dev-restart-timeout">
              The hub didn't come back within {RESTART_TIMEOUT_MS / 1000}s. Check its logs.
            </InlineNotice>
          )}
          {restartError !== null && (
            <InlineNotice tone="danger" data-testid="hub-dev-restart-error">
              Restart failed: {restartError}
            </InlineNotice>
          )}
        </>
      )}

      {restartDialog}
    </section>
  );
}
