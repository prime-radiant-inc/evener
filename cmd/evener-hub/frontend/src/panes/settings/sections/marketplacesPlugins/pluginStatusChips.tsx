// The status chips a plugin carries, shared by the installed list's rows and
// the plugin detail sheet so a new flag is added in one place.

import type { PluginEntry } from "@evener/appwire-client";
import type { ReactElement } from "react";
import { Chip, type ChipTone } from "../../../../widgets";

const STATUS_CHIPS: { label: string; tone: ChipTone; shows: (plugin: PluginEntry) => boolean }[] = [
  { label: "broken", tone: "danger", shows: (p) => p.broken },
  { label: "off by default", tone: "neutral", shows: (p) => !p.enabled },
  { label: "auto-upgrade", tone: "neutral", shows: (p) => p.autoUpgrade },
  { label: "update available", tone: "attention", shows: (p) => p.updateAvailable === true },
];

/** The plugin's status chips, empty when it has none. */
export function pluginStatusChips(plugin: PluginEntry): ReactElement[] {
  return STATUS_CHIPS.filter((chip) => chip.shows(plugin)).map((chip) => (
    <Chip key={chip.label} tone={chip.tone}>
      {chip.label}
    </Chip>
  ));
}
