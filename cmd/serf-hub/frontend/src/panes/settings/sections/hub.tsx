import { useEffect } from "react";
import { settingsOverviewStore, useSettingsOverviewStore } from "../../../stores/settingsOverview";
import { Button, EmptyState, Skeleton } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./hub.module.css";
import { Code, SettingsField } from "./settingsField";

const CLASS = {
  root: requireClass(styles.root, "hub.module.css", "root"),
  list: requireClass(styles.list, "hub.module.css", "list"),
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
      <EmptyState
        title="Couldn't load hub settings"
        hint={error}
        action={
          <Button size="sm" onClick={() => void settingsOverviewStore.getState().refresh()}>
            Retry
          </Button>
        }
      />
    );
  }

  if (!data) return null; // not yet fetched, not loading, no error: effect hasn't run yet

  const hub = data.hub;

  return (
    <div className={CLASS.root}>
      <dl className={CLASS.list}>
        <SettingsField
          label="Listen address"
          value={hub?.listenAddr ?? ""}
          help={
            <>
              Address and port the hub HTTP server binds to. Change via <Code>addr</Code> in{" "}
              <Code>~/.serf/hub.toml</Code> or <Code>--addr</Code>.
            </>
          }
        />
        <SettingsField
          label="Run dir"
          value={hub?.runDir ?? ""}
          help="Per-PID rendezvous files; the hub watches this directory to discover live daemons."
        />
        <SettingsField
          label="Spawn timeout"
          value={hub?.spawnTimeout ?? ""}
          help="How long the hub waits for a daemon to report ready after spawn before treating it as failed."
        />
      </dl>
    </div>
  );
}
