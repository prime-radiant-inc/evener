// Settings -> Agents (#8): server-rendered, read-only agent roster, overview-
// fed (evener/settings/overview -> data.agents). No add/remove/edit affordance
// in this view - editing happens externally in the linked editor
// (parity-m7-settings.md §8). The overview hook is injectable (`useOverview`)
// so tests can supply a fixture; it defaults to the real
// stores/settingsOverview adapter.
import { friendlyErrorMessage } from "@evener/appwire-client";
import { type SettingsOverviewStoreState, useSettingsOverviewStore } from "../../../stores/settingsOverview";
import { EmptyState, OpenButton, Skeleton } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./agents.module.css";
import { useConnectedEffect } from "./useConnectedEffect";

const CLASS = {
  root: requireClass(styles.root, "agents.module.css", "root"),
  help: requireClass(styles.help, "agents.module.css", "help"),
  error: requireClass(styles.error, "agents.module.css", "error"),
  list: requireClass(styles.list, "agents.module.css", "list"),
  row: requireClass(styles.row, "agents.module.css", "row"),
  name: requireClass(styles.name, "agents.module.css", "name"),
  builtin: requireClass(styles.builtin, "agents.module.css", "builtin"),
};

export interface AgentsSectionProps {
  /** Unused - kept so this component's signature matches every other
   * dispatched settings section (see Settings.tsx's SECTION_COMPONENTS map). */
  sectionId: string;
  useOverview?: () => SettingsOverviewStoreState;
}

export function AgentsSection({ useOverview = useSettingsOverviewStore }: AgentsSectionProps) {
  const { data, loading, error, fetch } = useOverview();

  // fetch caches internally (settingsOverview.ts's own contract), so it's safe
  // to depend on it honestly - a re-mount or an unstable fetch reference
  // re-runs this harmlessly. useConnectedEffect (not a bare useEffect) because
  // stores/settingsOverview.ts requires a connected client the same way
  // credentialsStore/launchConfigStore do - see that hook's own doc comment
  // for the direct-deep-link race this guards against.
  useConnectedEffect(fetch, [fetch]);

  const agents = data?.agents ?? [];

  return (
    <div className={CLASS.root}>
      <h2>Agents</h2>
      <p className={CLASS.help}>
        Agents discovered from plugin directories and built-in defaults. Open an agent file to view or edit its
        definition.
      </p>
      {loading && <Skeleton />}
      {error && <p className={CLASS.error}>Failed to load: {friendlyErrorMessage(error)}</p>}
      {!loading &&
        !error &&
        (agents.length === 0 ? (
          <EmptyState title="No agents discovered." />
        ) : (
          <ul className={CLASS.list}>
            {agents.map((agent) => (
              <li key={agent.name} className={CLASS.row}>
                <span className={CLASS.name}>{agent.name}</span>
                {agent.editPath ? (
                  <OpenButton href={agent.editPath} word="open in editor" />
                ) : (
                  <span className={CLASS.builtin}>built-in</span>
                )}
              </li>
            ))}
          </ul>
        ))}
    </div>
  );
}
