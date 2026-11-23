// Catalog rows search name and description; installed entries search the
// plugin and marketplace pair, so the entry's shape selects its convention.
type CatalogQueryFields = { name: string; description?: string };
type InstalledQueryFields = { plugin: string; marketplace: string };

export function pluginMatchesQuery(plugin: CatalogQueryFields | InstalledQueryFields, query: string): boolean {
  const q = query.toLowerCase();
  return "plugin" in plugin
    ? plugin.plugin.toLowerCase().includes(q) || plugin.marketplace.toLowerCase().includes(q)
    : plugin.name.toLowerCase().includes(q) || (plugin.description ?? "").toLowerCase().includes(q);
}

/** A marketplace row is addressed by its name. */
export function marketplaceMatchesQuery(name: string, query: string): boolean {
  return name.toLowerCase().includes(query.toLowerCase());
}
