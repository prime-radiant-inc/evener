import { useEffect } from "react";
import { settingsOverviewStore, useSettingsOverviewStore } from "../../../stores/settingsOverview";
import { Button, EmptyState, Skeleton } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./general.module.css";
import { Code, FieldDim, SettingsField } from "./settingsField";

const CLASS = {
  root: requireClass(styles.root, "general.module.css", "root"),
  help: requireClass(styles.help, "general.module.css", "help"),
  list: requireClass(styles.list, "general.module.css", "list"),
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
      <EmptyState
        title="Couldn't load general settings"
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
      <p className={CLASS.help}>
        Read-only summary of the hub's runtime configuration. Edit <Code>~/.serf/hub.toml</Code> to change these values.
      </p>
      <dl className={CLASS.list}>
        <SettingsField label="Hub address" value={hub?.listenAddr ?? ""} />
        <SettingsField
          label="Bearer token"
          value={
            <>
              {BEARER_TOKEN_MASK} {hub?.bearerTokenAge !== undefined && <FieldDim>{hub.bearerTokenAge}</FieldDim>}
            </>
          }
          help={
            <>
              Long-lived; copy when authenticating the TUI or a remote browser. Persisted at <Code>auth-token</Code> in
              the hub state dir and reused across restarts; delete that file to invalidate existing sessions.
            </>
          }
        />
        <SettingsField
          label="Run dir"
          value={hub?.runDir ?? ""}
          help="Per-PID rendezvous files; the hub watches this directory to discover live daemons."
        />
        <SettingsField label="State dir" value={data.storage?.stateDir ?? ""} />
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
                SQLite database of past session metadata. Search results in <Code>⌘K</Code> come from here.
              </>
            }
          />
        )}
        <SettingsField
          label="Spawn timeout"
          value={hub?.spawnTimeout ?? ""}
          help="How long the hub waits for a daemon to report ready after spawn before treating it as failed."
        />
        {pastIndex !== undefined && <SettingsField label="Past results per page" value={pastIndex.perPage ?? 0} />}
        <SettingsField
          label="Hub config"
          value={
            <>
              <Code>~/.serf/hub.toml</Code> <FieldDim>edit to change</FieldDim>
            </>
          }
        />
        <SettingsField
          label="Hub version"
          value={
            <>
              {hub?.version ?? ""} {hub?.commit !== undefined && <FieldDim>({hub.commit})</FieldDim>}
            </>
          }
        />
      </dl>
    </div>
  );
}
