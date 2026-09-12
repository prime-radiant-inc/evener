import { useEffect, useState } from "react";
import { hubUpdateStore, RESTART_TIMEOUT_MS, type UpdateChannel, useHubUpdateStore } from "../../../stores/hubUpdate";
import { useSettingsOverviewStore } from "../../../stores/settingsOverview";
import { Button, ConfirmDialog, Loader, RadioGroup } from "../../../widgets";
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

function shortCommit(sha: string): string {
  return sha.slice(0, 7);
}

/**
 * Settings -> Hub -> Updates. Seeds the channel from the overview's
 * buildChannel (release/snapshot builds only), checks on mount and on
 * channel change, and gates "Update and restart" behind a confirm dialog.
 * Dev builds get a note instead of controls: a worktree build must never be
 * silently replaced by a release binary. If buildChannel hasn't arrived yet
 * (channel still null - older overview responses, or a value this UI
 * doesn't recognize), the block shows only the running-build line and no
 * controls: there is nothing to check or select yet.
 */
export function HubUpdates() {
  const hub = useSettingsOverviewStore((s) => s.data?.hub);
  const channel = useHubUpdateStore((s) => s.channel);
  const check = useHubUpdateStore((s) => s.check);
  const checking = useHubUpdateStore((s) => s.checking);
  const checkError = useHubUpdateStore((s) => s.checkError);
  const applying = useHubUpdateStore((s) => s.applying);
  const applyError = useHubUpdateStore((s) => s.applyError);
  const restarting = useHubUpdateStore((s) => s.restarting);
  const restartTimedOut = useHubUpdateStore((s) => s.restartTimedOut);
  const [confirming, setConfirming] = useState(false);

  const buildChannel = hub?.buildChannel;
  const isDev = buildChannel === "dev";

  // Seed the selector from the running binary once the overview is here.
  useEffect(() => {
    if (channel === null && isChannel(buildChannel)) {
      hubUpdateStore.getState().setChannel(buildChannel);
    }
  }, [channel, buildChannel]);

  // Check whenever a channel is selected (mount and every change).
  useEffect(() => {
    if (channel !== null) void hubUpdateStore.getState().runCheck();
  }, [channel]);

  if (!hub) return null;

  return (
    <section className={CLASS.root} aria-labelledby="hub-updates-heading">
      <h3 id="hub-updates-heading" className={CLASS.heading}>
        Updates
      </h3>
      <p className={CLASS.status}>
        Running {hub.version ?? "unknown"}
        {hub.commit !== undefined && <FieldDim> ({hub.commit})</FieldDim>}
        {buildChannel !== undefined && <FieldDim> · {buildChannel}</FieldDim>}
      </p>

      {isDev && (
        <p className={CLASS.status}>
          <FieldDim>
            Dev build. Rebuild with <Code>make build-hub</Code> to update.
          </FieldDim>
        </p>
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
            disabled={applying || restarting}
          />

          <p className={CLASS.status} role="status">
            {restarting && <Loader label="Restarting hub" />}
            {!restarting &&
              restartTimedOut &&
              `The hub didn't come back within ${RESTART_TIMEOUT_MS / 1000}s. Check its logs.`}
            {!restarting && !restartTimedOut && checking && "Checking…"}
            {!restarting && !restartTimedOut && !checking && checkError !== null && (
              <>Couldn't check for updates: {checkError}</>
            )}
            {!restarting &&
              !restartTimedOut &&
              !checking &&
              checkError === null &&
              check !== null &&
              (check.updateAvailable
                ? `Update available: ${check.channel} ${shortCommit(check.latestCommit ?? "")}, running ${check.currentVersion}`
                : `Up to date on ${check.channel} (${shortCommit(check.latestCommit ?? "")})`)}
          </p>
          {applyError !== null && <p className={CLASS.status}>Update failed: {applyError}</p>}

          <div className={CLASS.actions}>
            <Button
              size="sm"
              onClick={() => void hubUpdateStore.getState().runCheck()}
              disabled={checking || applying || restarting}
            >
              Check for updates
            </Button>
            <Button
              size="sm"
              onClick={() => setConfirming(true)}
              disabled={!(check?.updateAvailable ?? false) || checking || applying || restarting}
            >
              Update and restart
            </Button>
          </div>

          <ConfirmDialog
            open={confirming}
            title="Update and restart the hub?"
            confirmLabel="Yes, update and restart"
            busy={applying}
            onConfirm={() => {
              setConfirming(false);
              void hubUpdateStore.getState().apply();
            }}
            onCancel={() => setConfirming(false)}
          >
            The hub goes offline for a few seconds while the new binary starts. Running sessions keep going.
          </ConfirmDialog>
        </>
      )}
    </section>
  );
}
