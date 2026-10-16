import { Button, InlineNotice, Skeleton } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { PluginsDirsSection } from "../pluginsDirs";
import { useExtensionsHostState, useExtensionsHostStore } from "./hostStore";
import { MarketplacesSection } from "./MarketplacesSection";
import styles from "./marketplacesPlugins.module.css";

const CLASS = { page: requireClass(styles.page, "marketplacesPlugins.module.css", "page") };

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
  return (
    <div className={CLASS.page}>
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
      <PluginsDirsSection host={host} />
    </div>
  );
}
