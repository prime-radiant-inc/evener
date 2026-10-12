import { friendlyErrorMessage } from "@evener/appwire-client";
import { useEffect } from "react";
import { settingsOverviewStore, useSettingsOverviewStore } from "../../../stores/settingsOverview";
import { Button, InlineNotice, PropertyList, Skeleton, type StackedPropertyListRow } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./general.module.css";
import { Code, FieldDim, FieldValue } from "./settingsField";

const CLASS = {
  root: requireClass(styles.root, "general.module.css", "root"),
  help: requireClass(styles.help, "general.module.css", "help"),
};

// The bearer token's value is a hard invariant (credential never-echo) -
// this fixed mask is the ONLY thing ever rendered for it, matching the
// legacy's own literal general.html markup exactly (never derived from any
// wire field - there is no token value on SettingsHubOverview at all).
const BEARER_TOKEN_MASK = "••••••••••••";

/**
 * Settings -> General (parity-m7-settings.md §2): the fullest of the three
 * overview-fed sections - every field Hub (§16) and Storage (§17) show,
 * plus Bearer token/Past results per page/Hub config/Hub version, which
 * only this section renders. State dir reads settingsOverview.storage
 * (not hub) - see SettingsHubOverview's own doc comment for the
 * cross-reference. Past index and Past results per page are both omitted
 * when hub.pastIndex is absent (no past-session index configured).
 */
export function GeneralSection() {
  const data = useSettingsOverviewStore((s) => s.data);
  const loading = useSettingsOverviewStore((s) => s.loading);
  const error = useSettingsOverviewStore((s) => s.error);

  useEffect(() => {
    void settingsOverviewStore.getState().fetch();
  }, []);

  if (loading && !data) return <Skeleton lines={9} />;

  if (!data && error) {
    return (
      <InlineNotice
        tone="danger"
        live="status"
        data-reason="unavailable"
        title="Couldn't load general settings"
        action={
          <Button size="sm" onClick={() => void settingsOverviewStore.getState().refresh()}>
            Retry
          </Button>
        }
      >
        {/* friendlyErrorMessage, not the raw store error: settingsOverview.ts
            stores errorText(err) as-is, which is AppwireClient's own message
            verbatim for a client-unreachable rejection - internal wiring
            detail, never something to show here. */}
        {friendlyErrorMessage(error)}
      </InlineNotice>
    );
  }

  if (!data) return null; // not yet fetched, not loading, no error: effect hasn't run yet

  const hub = data.hub;
  const pastIndex = hub?.pastIndex;

  return (
    <div className={CLASS.root}>
      <p className={CLASS.help}>
        Read-only summary of the hub's runtime configuration. Edit <Code>~/.config/evener/hub.toml</Code> to change
        these values.
      </p>
      <PropertyList
        layout="stacked"
        rows={
          [
            { label: "Hub address", value: <FieldValue field="hub-address">{hub?.listenAddr ?? ""}</FieldValue> },
            {
              label: "Bearer token",
              value: (
                <FieldValue field="bearer-token">
                  {BEARER_TOKEN_MASK} {hub?.bearerTokenAge !== undefined && <FieldDim>{hub.bearerTokenAge}</FieldDim>}
                </FieldValue>
              ),
              help: (
                <>
                  Long-lived; copy when authenticating the TUI or a remote browser. Persisted at <Code>auth-token</Code>{" "}
                  in the hub state dir and reused across restarts; delete that file to invalidate existing sessions.
                </>
              ),
            },
            {
              label: "Run dir",
              value: <FieldValue field="run-dir">{hub?.runDir ?? ""}</FieldValue>,
              help: "Per-PID rendezvous files; the hub watches this directory to discover live daemons.",
            },
            { label: "State dir", value: <FieldValue field="state-dir">{data.storage?.stateDir ?? ""}</FieldValue> },
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
                        SQLite database of past session metadata. Search results in <Code>⌘K</Code> come from here.
                      </>
                    ),
                  },
                ]
              : []),
            {
              label: "Spawn timeout",
              value: <FieldValue field="spawn-timeout">{hub?.spawnTimeout ?? ""}</FieldValue>,
              help: "How long the hub waits for a daemon to report ready after spawn before treating it as failed.",
            },
            ...(pastIndex !== undefined
              ? [
                  {
                    label: "Past results per page",
                    value: <FieldValue field="past-results-per-page">{pastIndex.perPage ?? 0}</FieldValue>,
                  },
                ]
              : []),
            {
              label: "Hub config",
              value: (
                <FieldValue field="hub-config">
                  <Code>~/.config/evener/hub.toml</Code> <FieldDim>edit to change</FieldDim>
                </FieldValue>
              ),
            },
            {
              label: "Hub version",
              value: (
                <FieldValue field="hub-version">
                  {hub?.version ?? ""} {hub?.commit !== undefined && <FieldDim>({hub.commit})</FieldDim>}
                </FieldValue>
              ),
            },
          ] satisfies StackedPropertyListRow[]
        }
      />
    </div>
  );
}
