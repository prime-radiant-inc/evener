import { useState } from "react";
import { Chevron, EmptyState, Eyebrow, Input, ListRow } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { matchesSettingsSection, SETTINGS_CLUSTERS, SETTINGS_SECTIONS, type SettingsSection } from "./sections";
import styles from "./settings.module.css";

export interface SettingsNavProps {
  /** null on the mobile list view - no row is "current" when no content
   * sits beside the list for a highlight to refer to. */
  activeId: string | null;
  onNavigate: (sectionId: string) => void;
}

const CLASS = {
  nav: requireClass(styles.nav, "settings.module.css", "nav"),
  filterRow: requireClass(styles.filterRow, "settings.module.css", "filterRow"),
  clusterHeader: requireClass(styles.clusterHeader, "settings.module.css", "clusterHeader"),
  link: requireClass(styles.link, "settings.module.css", "link"),
  linkActive: requireClass(styles.linkActive, "settings.module.css", "linkActive"),
  linkChevron: requireClass(styles.linkChevron, "settings.module.css", "linkChevron"),
};

function NavLink({
  section,
  active,
  onNavigate,
}: {
  section: SettingsSection;
  active: boolean;
  onNavigate: (id: string) => void;
}) {
  return (
    <div className={active ? CLASS.linkActive : undefined}>
      <ListRow
        variant="plain"
        aria-current={active ? "page" : undefined}
        onSelect={() => onNavigate(section.id)}
        label={
          <span className={CLASS.link}>
            <span>{section.label}</span>
            <span className={CLASS.linkChevron} aria-hidden="true" data-testid="settings-nav-chevron">
              <Chevron direction="right" size={16} />
            </span>
          </span>
        }
      />
    </div>
  );
}

/**
 * Task groups in metadata order. Filter state stays here so mobile
 * drill-in and Back preserve it. Empty groups disappear from results.
 */
export function SettingsNav({ activeId, onNavigate }: SettingsNavProps) {
  const [filter, setFilter] = useState("");

  const matching = SETTINGS_SECTIONS.filter((section) => matchesSettingsSection(section, filter));

  return (
    <nav aria-label="Settings sections" className={CLASS.nav}>
      <div className={CLASS.filterRow}>
        <Input
          type="search"
          aria-label="Filter settings"
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
          placeholder="Filter settings…"
        />
      </div>
      {matching.length === 0 && (
        <div role="status">
          <EmptyState
            size="compact"
            reason="empty"
            title="No matching settings"
            hint="Try a control name, such as font or marketplace."
          />
        </div>
      )}
      {SETTINGS_CLUSTERS.map((cluster) => {
        const sections = matching.filter((section) => section.cluster === cluster.id);
        if (sections.length === 0) return null;
        return (
          <div key={cluster.id}>
            <div className={CLASS.clusterHeader} data-section={cluster.id}>
              <Eyebrow>{cluster.label}</Eyebrow>
            </div>
            {sections.map((section) => (
              <NavLink key={section.id} section={section} active={section.id === activeId} onNavigate={onNavigate} />
            ))}
          </div>
        );
      })}
    </nav>
  );
}
