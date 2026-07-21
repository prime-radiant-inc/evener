import { useEffect } from "react";
import { settingsOverviewStore, useSettingsOverviewStore } from "../../../stores/settingsOverview";
import { Button, EmptyState, Skeleton } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { Code, FieldDim, SettingsField } from "./settingsField";
import styles from "./storage.module.css";

const CLASS = {
  root: requireClass(styles.root, "storage.module.css", "root"),
  help: requireClass(styles.help, "storage.module.css", "help"),
  list: requireClass(styles.list, "storage.module.css", "list"),
};

// pastCountCopy mirrors storage.html's own Go template conditional exactly
// ({{if ne .PastCount 1}}s{{end}}) - plural for every count except exactly 1.
function pastCountCopy(count: number): string {
  return `Currently tracking ${count} session${count !== 1 ? "s" : ""}.`;
}

/**
 * Settings -> Storage (parity-m7-settings.md §17): 4 read-only fields.
 * State dir comes from settingsOverview.storage (its own field), but Run
 * dir and the whole Past index row come from settingsOverview.hub - the
 * legacy storage.html renders them too, but appwire.SettingsStorageOverview
 * deliberately doesn't duplicate them (see that type's own doc comment);
 * General (§2) and this section both read the one shared hub.pastIndex
 * instead. Past index is omitted entirely when hub.pastIndex is absent (no
 * past-session index configured), matching General's own conditional.
 */
export function StorageSection() {
  const data = useSettingsOverviewStore((s) => s.data);
  const loading = useSettingsOverviewStore((s) => s.loading);
  const error = useSettingsOverviewStore((s) => s.error);

  useEffect(() => {
    void settingsOverviewStore.getState().fetch();
  }, []);

  if (loading && !data) return <Skeleton lines={4} />;

  if (!data && error) {
    return (
      <EmptyState
        title="Couldn't load storage settings"
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
  const pastIndex = hub?.pastIndex;

  return (
    <div className={CLASS.root}>
      <p className={CLASS.help}>Paths and file locations used by the hub at runtime.</p>
      <dl className={CLASS.list}>
        <SettingsField
          label="State dir"
          value={data.storage?.stateDir ?? ""}
          help="Root directory for hub state: auth token, credentials, and project sub-directories."
        />
        <SettingsField
          label="Run dir"
          value={hub?.runDir ?? ""}
          help="Per-PID rendezvous files; the hub watches this directory to discover live daemons."
        />
        <SettingsField
          label="Hub config"
          value={<Code>~/.serf/hub.toml</Code>}
          help="Main configuration file. Edit it to change addresses, providers, and spawn defaults."
        />
        {pastIndex !== undefined && (
          <SettingsField
            label="Past index"
            value={
              <>
                {pastIndex.path} {pastIndex.size !== undefined && <FieldDim>{pastIndex.size}</FieldDim>}
              </>
            }
            help={
              <>
                SQLite database of past session metadata. Search results in <Code>⌘K</Code> come from here.{" "}
                {pastCountCopy(pastIndex.count ?? 0)}
              </>
            }
          />
        )}
      </dl>
    </div>
  );
}
