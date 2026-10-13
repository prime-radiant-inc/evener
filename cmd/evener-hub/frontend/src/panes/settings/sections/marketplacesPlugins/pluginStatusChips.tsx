// The status chips a plugin carries, shared by the installed list's rows and
// the plugin detail sheet so a new flag is added in one place.

import type { PluginEntry } from "@evener/appwire-client";
import type { ReactElement } from "react";
import { Chip, type ChipTone } from "../../../../widgets";

const STATUS_CHIPS: { kind: string; label: string; tone: ChipTone; shows: (plugin: PluginEntry) => boolean }[] = [
  { kind: "broken", label: "broken", tone: "danger", shows: (p) => p.broken },
  { kind: "disabled", label: "off by default", tone: "neutral", shows: (p) => !p.enabled },
  { kind: "auto-upgrade", label: "auto-upgrade", tone: "neutral", shows: (p) => p.autoUpgrade },
  { kind: "update-available", label: "update available", tone: "attention", shows: (p) => p.updateAvailable === true },
];

/** The plugin's status chips, empty when it has none. */
export function pluginStatusChips(plugin: PluginEntry): ReactElement[] {
  return STATUS_CHIPS.filter((chip) => chip.shows(plugin)).map((chip) => (
    <Chip key={chip.kind} tone={chip.tone} kind={chip.kind}>
      {chip.label}
    </Chip>
  ));
}
