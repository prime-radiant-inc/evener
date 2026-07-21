// sourceLabel formats a MarketplaceSourceInput into the human summary
// MarketplacesSection shows beside each registered marketplace's name -
// ported verbatim from templates/partials/settings/plugins-manager.html's
// own sourceLabel() (parity-m7-settings.md §12b).
import type { MarketplaceSourceInput } from "../../../../protocol/types.gen";

export function sourceLabel(source: MarketplaceSourceInput): string {
  switch (source.kind) {
    case "github":
      return `github: ${source.repo}`;
    case "url":
      return source.url ?? "";
    case "directory":
      return source.path ?? "";
    case "git-subdir":
      return `${source.url} (${source.path})`;
    default:
      return source.kind;
  }
}
