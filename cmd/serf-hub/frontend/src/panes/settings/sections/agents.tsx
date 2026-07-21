// Settings -> Agents (#8): server-rendered, read-only agent roster, overview-
// fed (serf/settings/overview -> data.agents). No add/remove/edit affordance
// in this view - editing happens externally in the linked editor
// (parity-m7-settings.md §8). See overviewSeam.ts's own top comment for why
// `useOverview` is injected rather than importing stores/settingsOverview.ts
// directly.
import { useEffect } from "react";
import { EmptyState, Skeleton } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./agents.module.css";
import { useSettingsOverview } from "./overviewSeam";

const CLASS = {
  root: requireClass(styles.root, "agents.module.css", "root"),
  help: requireClass(styles.help, "agents.module.css", "help"),
  error: requireClass(styles.error, "agents.module.css", "error"),
  list: requireClass(styles.list, "agents.module.css", "list"),
  row: requireClass(styles.row, "agents.module.css", "row"),
  name: requireClass(styles.name, "agents.module.css", "name"),
  builtin: requireClass(styles.builtin, "agents.module.css", "builtin"),
  link: requireClass(styles.link, "agents.module.css", "link"),
};

export interface AgentsSectionProps {
  /** Unused - kept so this component's signature matches every other
   * dispatched settings section (see Settings.tsx's SECTION_COMPONENTS map). */
  sectionId: string;
  useOverview?: typeof useSettingsOverview;
}

export function AgentsSection({ useOverview = useSettingsOverview }: AgentsSectionProps) {
  const { data, loading, error, fetch } = useOverview();

  useEffect(() => {
    // fetch caches internally (overviewSeam.ts's own contract), so it's
    // safe to depend on it honestly - a re-mount or an unstable fetch
    // reference from a future real store re-runs this harmlessly rather
    // than needing an exhaustive-deps suppression.
    void fetch();
  }, [fetch]);

  const agents = data?.agents ?? [];

  return (
    <div className={CLASS.root}>
      <h2>Agents</h2>
      <p className={CLASS.help}>
        Agents discovered from plugin directories and built-in defaults. Open an agent file to view or edit its
        definition.
      </p>
      {loading && <Skeleton />}
      {error && <p className={CLASS.error}>Failed to load: {error}</p>}
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
                  <a className={CLASS.link} href={agent.editPath} target="_blank" rel="noopener">
                    open in editor ↗
                  </a>
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
