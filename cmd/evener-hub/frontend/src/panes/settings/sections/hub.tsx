import { friendlyErrorMessage } from "@evener/appwire-client";
import { useEffect } from "react";
import { settingsOverviewStore, useSettingsOverviewStore } from "../../../stores/settingsOverview";
import { Button, InlineNotice, PropertyList, Skeleton } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./hub.module.css";
import { HubResidents } from "./hubResidents";
import { HubUpdates } from "./hubUpdates";
import { Code, FieldValue } from "./settingsField";

const CLASS = {
  root: requireClass(styles.root, "hub.module.css", "root"),
};

/**
 * Settings -> Hub (parity-m7-settings.md §16): 3 read-only daemon-runtime
 * fields, a strict subset of General's own field set (§2) with matching
 * copy - both sections read the same settingsOverview.hub, this one just
 * shows fewer rows and skips General's intro paragraph (the legacy
 * hub.html has none either). No local heading: Settings.tsx's outer
 * PaneScaffold already titles the pane "Hub" from the active section id.
 */
export function HubSection() {
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
        {/* friendlyErrorMessage, not the raw store error - see general.tsx's
            matching comment for why (settingsOverview.ts stores errorText(err)
            verbatim, including AppwireClient's own internal-wiring text). */}
        {friendlyErrorMessage(error)}
      </InlineNotice>
    );
  }

  if (!data) return null; // not yet fetched, not loading, no error: effect hasn't run yet

  const hub = data.hub;

  return (
    <div className={CLASS.root}>
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
            label: "Run dir",
            value: <FieldValue field="run-dir">{hub?.runDir ?? ""}</FieldValue>,
            help: "Per-PID rendezvous files; the hub watches this directory to discover live daemons.",
          },
          {
            label: "Spawn timeout",
            value: <FieldValue field="spawn-timeout">{hub?.spawnTimeout ?? ""}</FieldValue>,
            help: "How long the hub waits for a daemon to report ready after spawn before treating it as failed.",
          },
        ]}
      />
      <HubUpdates />
      <HubResidents />
    </div>
  );
}
