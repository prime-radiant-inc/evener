// formatProviderLabel names a base provider for both the grouped instance list
// and InstanceSheet's base selector. When the display name differs from the
// registry id (beyond case), both appear so an error naming the registry id is
// directly findable in Settings.
export function formatProviderLabel(id: string, name?: string): string {
  const trimmed = name?.trim() ?? "";
  if (trimmed === "" || trimmed.toLowerCase() === id.toLowerCase()) {
    return trimmed || id;
  }
  return `${trimmed} · ${id}`;
}
