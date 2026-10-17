import { useId } from "react";
import { Button, InlineNotice, Skeleton } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { PluginsDirsSection } from "../pluginsDirs";
import { useExtensionsHostState, useExtensionsHostStore } from "./hostStore";
import { MarketplacesSection } from "./MarketplacesSection";
import styles from "./marketplacesPlugins.module.css";

const CLASS = {
  page: requireClass(styles.page, "marketplacesPlugins.module.css", "page"),
  section: requireClass(styles.section, "marketplacesPlugins.module.css", "section"),
  heading: requireClass(styles.heading, "marketplacesPlugins.module.css", "heading"),
};

export function SourcesSection({
  host,
  onSelectMarketplace,
}: {
  host: string;
  onSelectMarketplace: (name: string) => void;
}) {
  const store = useExtensionsHostStore();
  const marketplaces = useExtensionsHostState((s) => s.marketplaces);
  const error = useExtensionsHostState((s) => s.marketplacesError);
  const headingId = useId();
  return (
    <div className={CLASS.page}>
      <section className={CLASS.section} aria-labelledby={headingId}>
        <h2 id={headingId} className={CLASS.heading}>
          Marketplaces
        </h2>
        {error !== null ? (
          <InlineNotice
            tone="danger"
            live="status"
            data-reason="unavailable"
            action={
              <Button size="sm" onClick={() => void store.getState().fetchMarketplaces()}>
                Retry
              </Button>
            }
          >
            {error}
          </InlineNotice>
        ) : marketplaces === null ? (
          <Skeleton />
        ) : (
          <MarketplacesSection onSelect={onSelectMarketplace} />
        )}
      </section>
      <PluginsDirsSection host={host} />
    </div>
  );
}
