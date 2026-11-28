import type { UpdateCheckResponse } from "@evener/appwire-client";
import { useEffect, useRef, useState } from "react";
import { usePaneDocument } from "../../../shell/paneDocument";
import { useVisibleInterval } from "../../../shell/useVisibleInterval";
import { useConnectionStore } from "../../../stores/connection";
import { hubUpdateStore, RESTART_TIMEOUT_MS, type UpdateChannel, useHubUpdateStore } from "../../../stores/hubUpdate";
import { useSettingsOverviewStore } from "../../../stores/settingsOverview";
import surfaceStyles from "../../../styles/surface.module.css";
import {
  Button,
  Card,
  Chip,
  ConfirmDialog,
  InlineNotice,
  Loader,
  SegmentedControl,
  Timestamp,
  useToasts,
} from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { formatHubStamp } from "./hubStamp";
import styles from "./hubUpdates.module.css";

const CLASS = {
  root: requireClass(styles.root, "hubUpdates.module.css", "root"),
  header: requireClass(styles.header, "hubUpdates.module.css", "header"),
  channelControl: requireClass(styles.channelControl, "hubUpdates.module.css", "channelControl"),
  channelLabel: requireClass(styles.channelLabel, "hubUpdates.module.css", "channelLabel"),
  ledger: requireClass(styles.ledger, "hubUpdates.module.css", "ledger"),
  row: requireClass(styles.row, "hubUpdates.module.css", "row"),
  rowLabel: requireClass(styles.rowLabel, "hubUpdates.module.css", "rowLabel"),
  rowValue: requireClass(styles.rowValue, "hubUpdates.module.css", "rowValue"),
  rowDate: requireClass(styles.rowDate, "hubUpdates.module.css", "rowDate"),
  link: requireClass(styles.link, "hubUpdates.module.css", "link"),
  linkMono: requireClass(styles.linkMono, "hubUpdates.module.css", "linkMono"),
  linkQuiet: requireClass(styles.linkQuiet, "hubUpdates.module.css", "linkQuiet"),
  linkIcon: requireClass(styles.linkIcon, "hubUpdates.module.css", "linkIcon"),
  mono: requireClass(styles.mono, "hubUpdates.module.css", "mono"),
  muted: requireClass(styles.muted, "hubUpdates.module.css", "muted"),
  structuralNotice: requireClass(styles.structuralNotice, "hubUpdates.module.css", "structuralNotice"),
  footer: requireClass(styles.footer, "hubUpdates.module.css", "footer"),
  actions: requireClass(styles.actions, "hubUpdates.module.css", "actions"),
  activity: requireClass(styles.activity, "hubUpdates.module.css", "activity"),
};

const HEADING = `${surfaceStyles.settingsSectionHeading} ${surfaceStyles.settingsSectionHeadingBody}`;

const CHANNEL_OPTIONS = [
  { value: "release" as const, label: "Release" },
  { value: "snapshot" as const, label: "Snapshot" },
];

function isChannel(value: string | undefined): value is UpdateChannel {
  return value === "release" || value === "snapshot";
}

function defaultChannel(buildChannel: string | undefined): UpdateChannel | null {
  if (isChannel(buildChannel)) return buildChannel;
  // A local build has no channel of its own; Release matches the hub's own
  // default for an empty channel request.
  if (buildChannel === "dev") return "release";
  return null;
}

function displayBuild(version: string | undefined): string {
  if (version === undefined || version === "") return "unknown";
  const dirty = version.endsWith("-dirty");
  const sha = dirty ? version.slice(0, -"-dirty".length) : version;
  return sha.slice(0, 7) + (dirty ? "-dirty" : "");
}

function isDirtyVersion(version: string | undefined): boolean {
  return version?.endsWith("-dirty") ?? false;
}

function isLinkableCommit(commit: string | undefined): commit is string {
  return commit !== undefined && /^[0-9a-f]{7,40}$/i.test(commit);
}

function safeRepoBase(repoUrl: string | undefined): string | null {
  if (!repoUrl) return null;
  try {
    const parsed = new URL(repoUrl);
    if (parsed.protocol !== "https:" && parsed.protocol !== "http:") return null;
    return `${parsed.origin}${parsed.pathname}`.replace(/\/+$/, "");
  } catch {
    return null;
  }
}

function commitsMatch(a: string | undefined, b: string | undefined): boolean {
  if (!a || !b || isDirtyVersion(a) || isDirtyVersion(b)) return false;
  if (a.length < 7 || b.length < 7) return false;
  return a.startsWith(b) || b.startsWith(a);
}

function deriveUpdateState(input: {
  restarting: boolean;
  restartTimedOut: boolean;
  checking: boolean;
  hasError: boolean;
  check: UpdateCheckResponse | null;
}): string | undefined {
  if (input.restarting) return "restarting";
  if (input.restartTimedOut) return "restart-timed-out";
  if (input.checking) return "checking";
  if (input.hasError) return "check-failed";
  if (input.check === null) return undefined;
  return input.check.updateAvailable ? "update-available" : "up-to-date";
}

function runningChipLabelFor(
  localBuild: boolean,
  buildChannel: string | undefined,
  runningReleaseTag: string | undefined,
): string | null {
  if (localBuild) return "local build";
  if (buildChannel === "snapshot") return "snapshot";
  if (buildChannel === "release" && !runningReleaseTag) return "release";
  return null;
}

function ExternalArrow() {
  return (
    <svg
      className={CLASS.linkIcon}
      width="11"
      height="11"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M6 3h7v7M13 3L5.5 10.5" />
    </svg>
  );
}

function BuildReference({
  version,
  commit,
  releaseTag,
  repoBase,
}: {
  version: string | undefined;
  commit?: string | undefined;
  releaseTag?: string | undefined;
  repoBase: string | null;
}) {
  const dirty = isDirtyVersion(version);
  const label = displayBuild(version ?? commit);
  const commitForLink = dirty ? undefined : commit && isLinkableCommit(commit) ? commit : version;
  const canLinkCommit = repoBase !== null && !dirty && isLinkableCommit(commitForLink);
  const canLinkTag = repoBase !== null && releaseTag !== undefined && releaseTag !== "";

  return (
    <>
      {canLinkTag && (
        <a
          className={CLASS.link}
          href={`${repoBase}/releases/tag/${encodeURIComponent(releaseTag)}`}
          target="_blank"
          rel="noreferrer"
        >
          {releaseTag}
          <ExternalArrow />
        </a>
      )}
      {canLinkCommit ? (
        <a
          className={CLASS.linkMono}
          href={`${repoBase}/commit/${encodeURIComponent(commitForLink)}`}
          target="_blank"
          rel="noreferrer"
        >
          {label}
          <ExternalArrow />
        </a>
      ) : (
        <span className={CLASS.mono}>{label}</span>
      )}
    </>
  );
}

function BuildTimestamp({ iso }: { iso: string | undefined }) {
  const formatted = formatHubStamp(iso, { omitCurrentYear: true });
  if (!iso || !formatted) return <span className={CLASS.rowDate} />;
  return (
    <time className={CLASS.rowDate} dateTime={iso} title={iso}>
      {formatted}
    </time>
  );
}

function LastCheckedStamp({ value }: { value: number }) {
  const [nowMs, setNowMs] = useState(() => Date.now());
  const ownerDocument = usePaneDocument();
  useVisibleInterval(() => setNowMs(Date.now()), 30_000, true, ownerDocument);
  return (
    <span className={CLASS.muted}>
      Checked <Timestamp value={value} now={Math.max(nowMs, value)} />
    </span>
  );
}

function ChannelStatusValue({
  channel,
  checking,
  hasError,
  check,
  onDiskMatchesLatest,
  localBuild,
  installAvailable,
  repoBase,
  runningCommit,
  showCompareLink,
}: {
  channel: UpdateChannel;
  checking: boolean;
  hasError: boolean;
  check: UpdateCheckResponse | null;
  onDiskMatchesLatest: boolean;
  localBuild: boolean;
  installAvailable: boolean;
  repoBase: string | null;
  runningCommit: string | undefined;
  showCompareLink: boolean;
}) {
  if (checking) return <Loader label={`Checking ${channel}…`} />;
  if (hasError) return <span className={CLASS.muted}>Unavailable</span>;
  if (check === null) return null;

  const showLatestBuild = onDiskMatchesLatest || check.updateAvailable || (localBuild && installAvailable);
  if (!showLatestBuild) {
    return (
      <Chip tone="alive" variant="quiet">
        Up to date
      </Chip>
    );
  }

  return (
    <>
      <BuildReference
        version={check.latestCommit}
        commit={check.latestCommit}
        releaseTag={check.channel === "release" ? check.latestTag : undefined}
        repoBase={repoBase}
      />
      {onDiskMatchesLatest ? (
        <Chip tone="attention" data-testid="hub-restart-pending">
          Ready on disk
        </Chip>
      ) : (
        <Chip tone="accent">{check.updateAvailable ? "Update available" : "Published build"}</Chip>
      )}
      {showCompareLink && repoBase !== null && runningCommit !== undefined && check.latestCommit !== undefined && (
        <a
          className={CLASS.linkQuiet}
          href={`${repoBase}/compare/${encodeURIComponent(runningCommit)}...${encodeURIComponent(check.latestCommit)}`}
          target="_blank"
          rel="noreferrer"
        >
          Compare
          <ExternalArrow />
        </a>
      )}
    </>
  );
}

function FooterActivity({
  installing,
  applying,
  restarting,
  restartTimedOut,
  lastCheckedAt,
}: {
  installing: boolean;
  applying: boolean;
  restarting: boolean;
  restartTimedOut: boolean;
  lastCheckedAt: number | null;
}) {
  if (installing || applying) return <Loader label="Installing update…" />;
  if (restarting) return <Loader label="Restarting hub…" />;
  if (restartTimedOut) return <span className={CLASS.muted}>Hub didn't come back</span>;
  if (lastCheckedAt !== null) return <LastCheckedStamp value={lastCheckedAt} />;
  return null;
}

/**
 * Settings -> Hub -> Updates. Renders the running build, any differing staged
 * on-disk build, and the selected channel's latest build in one fixed-height
 * card with GitHub links and date stamps. Updating installs and restarts the
 * hub automatically when restart is supported; transient errors surface as
 * system toasts so action buttons never shift position.
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
  const [confirmingRestart, setConfirmingRestart] = useState(false);
  const [lastCheckedAt, setLastCheckedAt] = useState<number | null>(null);
  const toasts = useToasts();
  const manualCheckRef = useRef(false);
  const prevCheckTrigger = useRef<{ channel: UpdateChannel | null; ready: boolean; reconcile: boolean }>({
    channel: null,
    ready: false,
    reconcile: false,
  });
  const announcedErrors = useRef({
    check: null as string | null,
    install: null as string | null,
    apply: null as string | null,
    restart: null as string | null,
    timedOut: false,
  });

  const buildChannel = hub?.buildChannel;
  const localBuild = buildChannel === "dev";
  const connectionState = useConnectionStore((s) => s.state);
  const seededChannel = defaultChannel(buildChannel);

  // Seed the selector from the running binary once the overview is here.
  useEffect(() => {
    if (channel === null && seededChannel !== null) {
      hubUpdateStore.getState().setChannel(seededChannel);
    }
  }, [channel, seededChannel]);

  // Run one update check whenever the selected channel changes, the socket
  // transitions to ready, or a lost install asks to reconcile on a ready
  // socket — collapsing reconnect + reconcile into a single request.
  useEffect(() => {
    const ready = connectionState === "ready";
    const prev = prevCheckTrigger.current;
    const channelChanged = channel !== prev.channel;
    const becameReady = ready && !prev.ready;
    const needsReconcile = installReconcile && !prev.reconcile;
    prevCheckTrigger.current = { channel, ready, reconcile: installReconcile };
    const shouldCheck =
      ready &&
      ((channel !== null && (channelChanged || becameReady)) || (installReconcile && (becameReady || needsReconcile)));
    if (shouldCheck) void hubUpdateStore.getState().runCheck();
  }, [channel, connectionState, installReconcile]);

  // Track check settlement time and inform manual re-checks that find no update.
  useEffect(() => {
    if (checking) return;
    if (check !== null && checkError === null && !check.checkError) {
      setLastCheckedAt(Date.now());
      if (manualCheckRef.current) {
        manualCheckRef.current = false;
        if (!check.updateAvailable && !check.installAvailable && !check.restartPending) {
          toasts.push("info", `Hub is up to date on ${check.channel}.`);
        }
      }
    } else if (checkError !== null || check?.checkError) {
      manualCheckRef.current = false;
    }
  }, [checking, check, checkError, toasts]);

  // Route transient update errors to system toasts instead of inline danger
  // blocks above the buttons. Suppress check failures while a restart is
  // actively dropping the socket.
  const effectiveCheckError = restarting ? null : (checkError ?? check?.checkError ?? null);
  useEffect(() => {
    const prev = announcedErrors.current;
    const entries: Array<["check" | "install" | "apply" | "restart", string | null, string]> = [
      ["check", effectiveCheckError, "Couldn't check for updates"],
      ["install", installError, "Update failed"],
      ["apply", applyError, "Update failed"],
      ["restart", restartError, "Restart failed"],
    ];
    for (const [slot, value, prefix] of entries) {
      if (value !== prev[slot]) {
        prev[slot] = value;
        if (value !== null) toasts.push("error", `${prefix}: ${value}`);
      }
    }
    if (restartTimedOut !== prev.timedOut) {
      prev.timedOut = restartTimedOut;
      if (restartTimedOut) {
        toasts.push("error", `The hub didn't come back within ${RESTART_TIMEOUT_MS / 1000}s. Check its logs.`);
      }
    }
  }, [effectiveCheckError, installError, applyError, restartError, restartTimedOut, toasts]);

  if (!hub) return null;

  const running = check?.currentVersion ?? hub.commit;
  const runningCommit = check?.currentCommit ?? hub.commit;
  const runningBuildTime = check?.currentBuildTime ?? hub.buildTime;
  const runningReleaseTag = buildChannel === "release" ? (check?.currentReleaseTag ?? hub.releaseTag) : undefined;
  const onDisk = check?.onDiskCommit;
  const onDiskBuildTime = check?.onDiskBuildTime;
  const repoBase = safeRepoBase(check?.repoUrl);
  const channelError = check?.checkError ?? null;
  const hasCheckError = checkError !== null || channelError !== null;
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
  const restartPending = check?.restartPending === true;
  const onDiskMatchesLatest = restartPending && commitsMatch(onDisk, check?.latestCommit);
  const showSeparateOnDiskRow = restartPending && !onDiskMatchesLatest;
  const showCompareLink =
    repoBase !== null &&
    !isDirtyVersion(running) &&
    isLinkableCommit(runningCommit) &&
    isLinkableCommit(check?.latestCommit) &&
    !commitsMatch(runningCommit, check?.latestCommit);
  const promoteRestartPrimary = restartPending && !installAvailable && restartSupported && !legacyApply;
  const updateState = deriveUpdateState({
    restarting,
    restartTimedOut,
    checking,
    hasError: hasCheckError,
    check,
  });
  const runningChipLabel = runningChipLabelFor(localBuild, buildChannel, runningReleaseTag);

  let primaryAction: { label: string; onClick: () => void } | null = null;
  if (legacyApply) {
    primaryAction = {
      label: "Update and restart",
      onClick: () => void hubUpdateStore.getState().apply(),
    };
  } else if (promoteRestartPrimary) {
    primaryAction = {
      label: "Restart to apply",
      onClick: () => setConfirmingRestart(true),
    };
  } else if (installAvailable) {
    primaryAction = {
      label: restartSupported ? "Update and restart" : "Install update",
      onClick: () => void hubUpdateStore.getState().installAndRestart(),
    };
  }

  const restartDialog = (
    <ConfirmDialog
      open={confirmingRestart}
      title={promoteRestartPrimary ? "Restart hub to apply update?" : "Restart the hub?"}
      confirmLabel="Restart hub"
      destructive={false}
      busy={restarting}
      onConfirm={() => {
        setConfirmingRestart(false);
        void hubUpdateStore.getState().restart();
      }}
      onCancel={() => setConfirmingRestart(false)}
    >
      {`The hub restarts into ${displayBuild(onDisk ?? running)} (running ${displayBuild(running)}). Open panes reconnect automatically; running sessions keep going.`}
    </ConfirmDialog>
  );

  function handleManualCheck() {
    manualCheckRef.current = true;
    void hubUpdateStore.getState().runCheck();
  }

  return (
    <section className={CLASS.root} aria-labelledby="hub-updates-heading">
      <Card variant="outlined" padding="none">
        <div className={CLASS.header}>
          <h3 id="hub-updates-heading" className={HEADING}>
            Updates
          </h3>
          {channel !== null && (
            <div className={CLASS.channelControl}>
              <span className={CLASS.channelLabel} aria-hidden="true">
                Channel
              </span>
              <SegmentedControl
                label="Channel"
                hideLabel
                size="sm"
                value={channel}
                onChange={(value) => hubUpdateStore.getState().setChannel(value)}
                options={CHANNEL_OPTIONS}
                disabled={busy}
              />
            </div>
          )}
        </div>

        <div className={CLASS.ledger}>
          <div className={CLASS.row}>
            <span className={CLASS.rowLabel}>Running</span>
            <div className={CLASS.rowValue}>
              <BuildReference
                version={running}
                commit={runningCommit}
                releaseTag={runningReleaseTag}
                repoBase={repoBase}
              />
              {runningChipLabel !== null && (
                <Chip tone="neutral" variant="quiet" data-testid={localBuild ? "hub-update-dev-note" : undefined}>
                  {runningChipLabel}
                </Chip>
              )}
            </div>
            <BuildTimestamp iso={runningBuildTime} />
          </div>

          {showSeparateOnDiskRow && (
            <div className={CLASS.row} data-testid="hub-on-disk-build">
              <span className={CLASS.rowLabel}>On disk</span>
              <div className={CLASS.rowValue}>
                <BuildReference version={onDisk} commit={onDisk} repoBase={repoBase} />
                <Chip tone="attention" data-testid="hub-restart-pending">
                  Restart pending
                </Chip>
              </div>
              <BuildTimestamp iso={onDiskBuildTime} />
            </div>
          )}

          {channel !== null && (
            <div className={CLASS.row} data-testid="hub-update-status" data-state={updateState}>
              <span className={CLASS.rowLabel}>{channel === "release" ? "Release" : "Snapshot"}</span>
              <div className={CLASS.rowValue}>
                <ChannelStatusValue
                  channel={channel}
                  checking={checking}
                  hasError={hasCheckError}
                  check={check}
                  onDiskMatchesLatest={onDiskMatchesLatest}
                  localBuild={localBuild}
                  installAvailable={installAvailable}
                  repoBase={repoBase}
                  runningCommit={runningCommit}
                  showCompareLink={showCompareLink}
                />
              </div>
              <BuildTimestamp
                iso={
                  checking || hasCheckError
                    ? undefined
                    : (check?.latestPublishedAt ?? (onDiskMatchesLatest ? onDiskBuildTime : undefined))
                }
              />
            </div>
          )}
        </div>

        {check?.onDiskError !== undefined && (
          <div className={CLASS.structuralNotice}>
            <InlineNotice tone="danger">Can't read the on-disk build: {check.onDiskError}</InlineNotice>
          </div>
        )}

        {check?.applicable === true &&
          check.installTargetMatchesLaunch === false &&
          check.installTarget !== undefined && (
            <div className={CLASS.structuralNotice}>
              <InlineNotice tone="danger" data-testid="hub-install-target-mismatch">
                Install is unavailable: it would write {check.installTarget}, but this hub runs from a different binary.
                Use the managed installation.
              </InlineNotice>
            </div>
          )}

        {channel !== null && (
          <div className={CLASS.footer}>
            <div className={CLASS.actions}>
              {primaryAction !== null && (
                <Button variant="primary" size="sm" onClick={primaryAction.onClick} disabled={busy}>
                  {primaryAction.label}
                </Button>
              )}

              <Button variant="secondary" size="sm" onClick={handleManualCheck} disabled={busy}>
                Check for updates
              </Button>

              {!legacyApply && restartSupported && !promoteRestartPrimary && (
                <Button variant="quiet" size="sm" onClick={() => setConfirmingRestart(true)} disabled={busy}>
                  Restart hub
                </Button>
              )}
            </div>

            <div className={CLASS.activity}>
              <FooterActivity
                installing={installing}
                applying={applying}
                restarting={restarting}
                restartTimedOut={restartTimedOut}
                lastCheckedAt={lastCheckedAt}
              />
            </div>
          </div>
        )}
      </Card>

      {restartDialog}
    </section>
  );
}
