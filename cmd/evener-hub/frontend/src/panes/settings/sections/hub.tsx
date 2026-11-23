import type { SettingsOperationStoreQuarantine } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import { connectionStore, readyConnectionTransition } from "../../../stores/connection";
import {
  loadSettingsOverview,
  markSettingsOverviewStale,
  settingsOverviewStore,
  useSettingsOverviewStore,
} from "../../../stores/settingsOverview";
import { Button, Disclosure, InlineNotice, PropertyList, Skeleton } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./hub.module.css";
import { HubResidents } from "./hubResidents";
import { HubUpdates } from "./hubUpdates";
import { Code, FieldDim } from "./settingsField";

const CLASS = {
  root: requireClass(styles.root, "hub.module.css", "root"),
};

// Never derive the mask from a credential. The overview carries only its age.
const BEARER_TOKEN_MASK = "••••••••••••";

/** Hub runtime, authentication status, updates and resident daemons. */
export function HubSection() {
  const [detailsOpen, setDetailsOpen] = useState(false);
  const data = useSettingsOverviewStore((s) => s.data);
  const loading = useSettingsOverviewStore((s) => s.loading);
  const error = useSettingsOverviewStore((s) => s.error);

  useEffect(() => {
    // Reads once, and re-reads if a reconnect was missed while this section was
    // unmounted, so a quarantine the hub performed in that window still shows.
    loadSettingsOverview();
    // While mounted, a transition INTO a ready connection is this section's own
    // trigger: the reader can open Hub settings in the middle of a reconnect, and
    // the replacement/recovery hook deliberately tracks its own view from module
    // load, so a subscription created here would miss the ready that ends a flap
    // that started before it. readyConnectionTransition has no such window - it
    // compares each publish to the one before it - and it also covers the first
    // connect, where the mount read ran ahead of the client.
    return connectionStore.subscribe((state, previous) => {
      if (!readyConnectionTransition(state, previous)) return;
      markSettingsOverviewStale();
      loadSettingsOverview();
    });
  }, []);

  if (loading && !data) return <Skeleton lines={3} />;

  if (!data && error) {
    return (
      <InlineNotice
        tone="danger"
        live="status"
        data-reason="unavailable"
        title="Couldn't load hub settings"
        action={
          <Button size="sm" onClick={() => void settingsOverviewStore.getState().refresh()}>
            Retry
          </Button>
        }
      >
        {error}
      </InlineNotice>
    );
  }

  if (!data) return null; // not yet fetched, not loading, no error: effect hasn't run yet

  const hub = data.hub;

  return (
    <div className={CLASS.root}>
      <HubUpdates />
      <HubResidents />
      <OperationStoreQuarantineNotice quarantine={hub?.operationStoreQuarantine} />
      <Disclosure summary="Technical details" open={detailsOpen} onOpenChange={setDetailsOpen}>
        <PropertyList
          layout="stacked"
          rows={[
            {
              label: "Listen address",
              value: hub?.listenAddr ?? "",
              rowProps: { "data-field": "listen-address" },
              help: (
                <>
                  Address and port the hub HTTP server binds to. Change via <Code>addr</Code> in{" "}
                  <Code>~/.config/evener/hub.toml</Code> or <Code>--addr</Code>.
                </>
              ),
            },
            {
              label: "Bearer token",
              value: (
                <>
                  {BEARER_TOKEN_MASK} {hub?.bearerTokenAge !== undefined && <FieldDim>{hub.bearerTokenAge}</FieldDim>}
                </>
              ),
              rowProps: { "data-field": "bearer-token" },
              help: (
                <>
                  Used to authenticate the TUI or a remote browser. Stored as <Code>auth-token</Code> in the hub state
                  directory and reused across restarts.
                </>
              ),
            },
            {
              label: "Spawn timeout",
              value: hub?.spawnTimeout ?? "",
              rowProps: { "data-field": "spawn-timeout" },
              help: "How long the hub waits for a daemon to report ready after spawn before treating it as failed.",
            },
          ]}
        />
      </Disclosure>
    </div>
  );
}

// OperationStoreQuarantineNotice reports the operation store's quarantine
// signal. The signal's QuarantinedFile is the store path the quarantine
// replaced — the corrupt bytes were renamed aside BESIDE it and the replacement
// now occupies that path — so the notice names it as the store path and never
// as a file holding the corrupt bytes. The custody file is NOT a copy of the
// store: it holds the recovery records the replacement was rebuilt from. It is a
// health fact, not a failure the reader can act on here: the hub already
// recovered. Nothing renders when the hub reports no quarantine, and an older
// hub sends no field at all.
function OperationStoreQuarantineNotice({ quarantine }: { quarantine: SettingsOperationStoreQuarantine | undefined }) {
  if (!quarantine) return null;
  return (
    <InlineNotice
      tone="neutral"
      live="status"
      data-reason="operation-store-quarantined"
      title="Host operation store was quarantined"
    >
      <p>
        A corrupt host operation store file was quarantined and the hub serves a replacement at the same path. The
        custody file holds the recovery records the replacement was built from, not a copy of the corrupt store.
      </p>
      {quarantine.asideFile ? null : <p>The file holding the corrupt bytes is no longer on disk.</p>}
      <PropertyList
        layout="stacked"
        rows={[
          // Only when the aside file is still there: a row for a path the hub
          // does not have would read as a file to go and look at.
          ...(quarantine.asideFile
            ? [
                {
                  label: "Set aside",
                  value: quarantine.asideFile,
                  rowProps: { "data-field": "aside-file" },
                },
              ]
            : []),
          {
            label: "Store path",
            value: quarantine.quarantinedFile ?? "",
            rowProps: { "data-field": "quarantined-file" },
          },
          {
            label: "Custody file",
            value: quarantine.custodyFile ?? "",
            rowProps: { "data-field": "custody-file" },
          },
          {
            label: "Quarantine epoch",
            value: quarantine.quarantineEpoch ?? 0,
            rowProps: { "data-field": "quarantine-epoch" },
          },
        ]}
      />
    </InlineNotice>
  );
}
