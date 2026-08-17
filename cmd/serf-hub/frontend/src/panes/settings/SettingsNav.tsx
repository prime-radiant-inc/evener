import { useId, useState } from "react";
import { Chevron, Input } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { SETTINGS_CLUSTERS, SETTINGS_SECTIONS, type SettingsSection } from "./sections";
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
  visuallyHidden: requireClass(styles.visuallyHidden, "settings.module.css", "visuallyHidden"),
  clusterHeader: requireClass(styles.clusterHeader, "settings.module.css", "clusterHeader"),
  link: requireClass(styles.link, "settings.module.css", "link"),
  linkActive: requireClass(styles.linkActive, "settings.module.css", "linkActive"),
  linkChevron: requireClass(styles.linkChevron, "settings.module.css", "linkChevron"),
};

function matchesFilter(label: string, filter: string): boolean {
  return label.toLowerCase().includes(filter.trim().toLowerCase());
}

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
    <button
      type="button"
      className={active ? `${CLASS.link} ${CLASS.linkActive}` : CLASS.link}
      aria-current={active ? "page" : undefined}
      onClick={() => onNavigate(section.id)}
    >
      <span>{section.label}</span>
      <span className={CLASS.linkChevron} aria-hidden="true" data-testid="settings-nav-chevron">
        <Chevron direction="right" size={16} />
      </span>
    </button>
  );
}

/**
 * The settings left nav: the 5 ungrouped links, then the 3 labeled
 * clusters, per sections.ts's own fixed order. Owns its own filter text
 * (a purely nav-local concern, matching the legacy's own `.settings-nav-
 * filter` scoping) - case-insensitive substring match on each link's
 * visible label; a cluster's header hides once every link between it and
 * the next header is filtered out, matching test-settings-shell.js.
 */
export function SettingsNav({ activeId, onNavigate }: SettingsNavProps) {
  const [filter, setFilter] = useState("");
  const filterId = useId();

  const ungrouped = SETTINGS_SECTIONS.filter((s) => s.cluster === undefined && matchesFilter(s.label, filter));

  return (
    <nav aria-label="Settings sections" className={CLASS.nav}>
      <div className={CLASS.filterRow}>
        <label className={CLASS.visuallyHidden} htmlFor={filterId}>
          Filter settings
        </label>
        <Input
          id={filterId}
          type="search"
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
          placeholder="Filter settings…"
        />
      </div>
      {ungrouped.map((section) => (
        <NavLink key={section.id} section={section} active={section.id === activeId} onNavigate={onNavigate} />
      ))}
      {SETTINGS_CLUSTERS.map((cluster) => {
        const sections = SETTINGS_SECTIONS.filter((s) => s.cluster === cluster.id && matchesFilter(s.label, filter));
        if (sections.length === 0) return null;
        return (
          <div key={cluster.id}>
            <div className={CLASS.clusterHeader}>{cluster.label}</div>
            {sections.map((section) => (
              <NavLink key={section.id} section={section} active={section.id === activeId} onNavigate={onNavigate} />
            ))}
          </div>
        );
      })}
    </nav>
  );
}
