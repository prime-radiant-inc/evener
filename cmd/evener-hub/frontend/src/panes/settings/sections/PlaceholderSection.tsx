import { EmptyState } from "../../../widgets";
import { settingsSectionLabel } from "../sections";

export interface PlaceholderSectionProps {
  sectionId: string;
}

/**
 * Stand-in content for every settings section until its own stream (T2-T4)
 * lands a real one - keeps nav-and-routing genuinely navigable end to end
 * ahead of that, without hand-authoring 16 near-identical placeholder
 * files that would just be thrown away one by one. Settings.tsx currently
 * renders this unconditionally for every section id; a later task swaps
 * specific ids over to their real components as those land (a lookup
 * mechanism this task deliberately doesn't guess at - see the wave-7
 * report).
 */
export function PlaceholderSection({ sectionId }: PlaceholderSectionProps) {
  return <EmptyState title={settingsSectionLabel(sectionId)} hint="This section hasn't been built yet." />;
}
