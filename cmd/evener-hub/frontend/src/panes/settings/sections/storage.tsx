import { useEffect, useState } from "react";
import { settingsOverviewStore, useSettingsOverviewStore } from "../../../stores/settingsOverview";
import { Disclosure, PropertyList, type StackedPropertyListRow } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { SectionGate } from "./sectionGate";
import { SESSION_CACHE_HELP, SESSION_CACHE_LABEL, SessionCacheStatus } from "./sessionCacheRow";
import { Code, FieldDim } from "./settingsField";
import styles from "./storage.module.css";

const CLASS = {
  root: requireClass(styles.root, "storage.module.css", "root"),
  help: requireClass(styles.help, "storage.module.css", "help"),
};

// pastCountCopy mirrors storage.html's own Go template conditional exactly
// ({{if ne .PastCount 1}}s{{end}}) - plural for every count except exactly 1.
function pastCountCopy(count: number): string {
  return `Currently tracking ${count} session${count !== 1 ? "s" : ""}.`;
}

/** Runtime paths, optional past-session index and browser session cache. */
export function StorageSection() {
  const [detailsOpen, setDetailsOpen] = useState(false);
  const data = useSettingsOverviewStore((s) => s.data);
  const loading = useSettingsOverviewStore((s) => s.loading);
  const error = useSettingsOverviewStore((s) => s.error);

  useEffect(() => {
    void settingsOverviewStore.getState().fetch();
  }, []);

  if (!data) {
    return (
      <SectionGate
        loading={loading}
        error={error}
        title="Couldn't load storage settings"
        onRetry={() => void settingsOverviewStore.getState().refresh()}
        skeletonLines={4}
      />
    );
  }

  const hub = data.hub;
  const pastIndex = hub?.pastIndex;

  return (
    <div className={CLASS.root}>
      <PropertyList
        layout="stacked"
        rows={[
          {
            label: SESSION_CACHE_LABEL,
            value: <SessionCacheStatus />,
            help: SESSION_CACHE_HELP,
            rowProps: { "data-field": "session-cache" },
          },
        ]}
      />
      <Disclosure summary="Technical details" open={detailsOpen} onOpenChange={setDetailsOpen}>
        <p className={CLASS.help}>Paths and file locations used by the hub at runtime.</p>
        <PropertyList
          layout="stacked"
          rows={
            [
              {
                label: "State dir",
                value: data.storage?.stateDir ?? "",
                help: "Root directory for hub state: auth token, credentials, and project sub-directories.",
                rowProps: { "data-field": "state-dir" },
              },
              {
                label: "Run dir",
                value: hub?.runDir ?? "",
                help: "Per-PID rendezvous files; the hub watches this directory to discover live daemons.",
                rowProps: { "data-field": "run-dir" },
              },
              {
                label: "Hub config",
                value: <Code>~/.config/evener/hub.toml</Code>,
                help: "Main configuration file. Edit it to change addresses, providers, and spawn defaults.",
                rowProps: { "data-field": "hub-config" },
              },
              ...(pastIndex !== undefined
                ? [
                    {
                      label: "Past index",
                      value: (
                        <>
                          {pastIndex.path} {pastIndex.size !== undefined && <FieldDim>{pastIndex.size}</FieldDim>}
                        </>
                      ),
                      help: (
                        <>
                          SQLite database of past session metadata. Search results in <Code>⌘K</Code> come from here.{" "}
                          {pastCountCopy(pastIndex.count ?? 0)}
                        </>
                      ),
                      rowProps: { "data-field": "past-index" },
                    },
                    {
                      label: "Past results per page",
                      value: pastIndex.perPage ?? 0,
                      rowProps: { "data-field": "past-results-per-page" },
                    },
                  ]
                : []),
            ] satisfies StackedPropertyListRow[]
          }
        />
      </Disclosure>
    </div>
  );
}
