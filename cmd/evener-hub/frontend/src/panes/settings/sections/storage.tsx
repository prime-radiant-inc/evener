import { useEffect, useState } from "react";
import { settingsOverviewStore, useSettingsOverviewStore } from "../../../stores/settingsOverview";
import {
  Button,
  Disclosure,
  InlineNotice,
  PropertyList,
  Skeleton,
  type StackedPropertyListRow,
} from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { SESSION_CACHE_HELP, SESSION_CACHE_LABEL, SessionCacheStatus } from "./sessionCacheRow";
import { Code, FieldDim, FieldValue } from "./settingsField";
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

  if (loading && !data) return <Skeleton lines={4} />;

  if (!data && error) {
    return (
      <InlineNotice
        tone="danger"
        live="status"
        data-reason="unavailable"
        title="Couldn't load storage settings"
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
  const pastIndex = hub?.pastIndex;

  return (
    <div className={CLASS.root}>
      <PropertyList
        layout="stacked"
        rows={[
          {
            label: SESSION_CACHE_LABEL,
            value: (
              <FieldValue field="session-cache">
                <SessionCacheStatus />
              </FieldValue>
            ),
            help: SESSION_CACHE_HELP,
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
                value: <FieldValue field="state-dir">{data.storage?.stateDir ?? ""}</FieldValue>,
                help: "Root directory for hub state: auth token, credentials, and project sub-directories.",
              },
              {
                label: "Run dir",
                value: <FieldValue field="run-dir">{hub?.runDir ?? ""}</FieldValue>,
                help: "Per-PID rendezvous files; the hub watches this directory to discover live daemons.",
              },
              {
                label: "Hub config",
                value: (
                  <FieldValue field="hub-config">
                    <Code>~/.config/evener/hub.toml</Code>
                  </FieldValue>
                ),
                help: "Main configuration file. Edit it to change addresses, providers, and spawn defaults.",
              },
              ...(pastIndex !== undefined
                ? [
                    {
                      label: "Past index",
                      value: (
                        <FieldValue field="past-index">
                          {pastIndex.path} {pastIndex.size !== undefined && <FieldDim>{pastIndex.size}</FieldDim>}
                        </FieldValue>
                      ),
                      help: (
                        <>
                          SQLite database of past session metadata. Search results in <Code>⌘K</Code> come from here.{" "}
                          {pastCountCopy(pastIndex.count ?? 0)}
                        </>
                      ),
                    },
                  ]
                : []),
              ...(pastIndex?.perPage !== undefined
                ? [
                    {
                      label: "Past results per page",
                      value: <FieldValue field="past-results-per-page">{pastIndex.perPage}</FieldValue>,
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
