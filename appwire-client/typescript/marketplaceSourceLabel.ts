// marketplaceSourceLabel formats a MarketplaceSourceInput into the human
// summary the web settings pane and the native marketplace browser show
// beside each registered marketplace's name - ported verbatim from
// templates/partials/settings/plugins-manager.html's own sourceLabel()
// (parity-m7-settings.md §12b). The marketplace prefix keeps it apart from
// watchRows' sourceLabel, which names a watch's source ("this session").
import type { MarketplaceSourceInput } from "./types.gen";

/** Appends the commit pin, when there is one, in git's own `url@ref` form.
 * A sha wins over a ref: it is the commit the clone actually holds. */
function pinned(label: string, source: MarketplaceSourceInput): string {
  const pin = source.sha || source.ref;
  return pin ? `${label}@${pin}` : label;
}

export function marketplaceSourceLabel(source: MarketplaceSourceInput): string {
  switch (source.kind) {
    case "github":
      return pinned(`github: ${source.repo ?? ""}`, source);
    case "url":
      return pinned(source.url ?? "", source);
    case "directory":
      return source.path ?? "";
    case "git-subdir":
      return `${pinned(source.url ?? "", source)} (${source.path ?? ""})`;
    default:
      return source.kind;
  }
}
