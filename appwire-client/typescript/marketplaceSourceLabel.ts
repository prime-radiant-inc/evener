// marketplaceSourceLabel formats a MarketplaceSourceInput into the human
// summary the web settings pane and the native marketplace browser show
// beside each registered marketplace's name - ported verbatim from
// templates/partials/settings/plugins-manager.html's own sourceLabel()
// (parity-m7-settings.md §12b). The marketplace prefix keeps it apart from
// watchRows' sourceLabel, which names a watch's source ("this session").
import type { MarketplaceSourceInput } from "./types.gen";

export function marketplaceSourceLabel(source: MarketplaceSourceInput): string {
  switch (source.kind) {
    case "github":
      return `github: ${source.repo ?? ""}`;
    case "url":
      return source.url ?? "";
    case "directory":
      return source.path ?? "";
    case "git-subdir":
      return `${source.url ?? ""} (${source.path ?? ""})`;
    default:
      return source.kind;
  }
}
