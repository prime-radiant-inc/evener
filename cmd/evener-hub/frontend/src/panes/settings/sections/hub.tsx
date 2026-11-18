import type { SettingsOperationStoreQuarantine } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import { settingsOverviewStore, useSettingsOverviewStore } from "../../../stores/settingsOverview";
import { Button, Disclosure, InlineNotice, PropertyList, Skeleton } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./hub.module.css";
import { HubResidents } from "./hubResidents";
import { HubUpdates } from "./hubUpdates";
import { Code, FieldDim, FieldValue } from "./settingsField";

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
    void settingsOverviewStore.getState().fetch();
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
              value: <FieldValue field="listen-address">{hub?.listenAddr ?? ""}</FieldValue>,
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
                <FieldValue field="bearer-token">
                  {BEARER_TOKEN_MASK} {hub?.bearerTokenAge !== undefined && <FieldDim>{hub.bearerTokenAge}</FieldDim>}
                </FieldValue>
              ),
              help: (
                <>
                  Used to authenticate the TUI or a remote browser. Stored as <Code>auth-token</Code> in the hub state
                  directory and reused across restarts.
                </>
              ),
            },
            {
              label: "Spawn timeout",
              value: <FieldValue field="spawn-timeout">{hub?.spawnTimeout ?? ""}</FieldValue>,
              help: "How long the hub waits for a daemon to report ready after spawn before treating it as failed.",
            },
          ]}
        />
      </Disclosure>
    </div>
  );
}

// OperationStoreQuarantineNotice reports the operation store's quarantine
// signal: a corrupt store file was renamed aside under a custody file, and the
// hub now serves the replacement. It is a health fact, not a failure the reader
// can act on here — the hub already recovered — so it is a neutral status
// notice that names the set-aside file and its custody for whoever has to go
// look at them. Nothing renders when the hub reports no quarantine, and an
// older hub sends no field at all.
function OperationStoreQuarantineNotice({ quarantine }: { quarantine: SettingsOperationStoreQuarantine | undefined }) {
  if (!quarantine) return null;
  return (
    <InlineNotice
      tone="neutral"
      live="status"
      data-reason="operation-store-quarantined"
      title="Host operation store was quarantined"
    >
      <p>A corrupt store file was moved aside and the hub is serving a replacement. Its contents were kept here:</p>
      <PropertyList
        layout="stacked"
        rows={[
          {
            label: "Set aside",
            value: <FieldValue field="quarantined-file">{quarantine.quarantinedFile ?? ""}</FieldValue>,
          },
          { label: "Custody", value: <FieldValue field="custody-file">{quarantine.custodyFile ?? ""}</FieldValue> },
          {
            label: "Quarantine epoch",
            value: <FieldValue field="quarantine-epoch">{quarantine.quarantineEpoch ?? 0}</FieldValue>,
          },
        ]}
      />
    </InlineNotice>
  );
}
