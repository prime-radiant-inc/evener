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
