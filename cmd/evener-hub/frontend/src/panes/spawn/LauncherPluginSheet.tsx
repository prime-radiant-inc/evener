// LauncherPluginSheet: the launcher mode of the shared plugin detail sheet
// (the settings detail sheet is its installed/catalog mode). One launch
// candidate arrives as a prop together with the selection state its row
// shows; launcher mode owns no editor - no install, remove or enable - so
// the sheet is the Contents block, the candidate's metadata, and the one
// switch that mirrors the row. The candidate names its own directory (an
// installed plugin@marketplace pair, or the path every other source
// resolved), so the components read starts when the sheet mounts; the panel
// mounts the sheet only while a candidate's sheet is open, and a read that
// fails degrades quietly where Contents would be rather than blocking the
// selection work around it.

import type { AppwireClientLike, PluginComponentsReadArgs, PluginLaunchCandidate } from "@evener/appwire-client";
import type { ReactElement } from "react";
import { Button, InlineNotice, Switch } from "../../widgets";
import { PluginContents } from "../plugins/PluginContents";
import { type PluginMetadataRow, PluginSheet } from "../plugins/PluginSheet";
import { usePluginComponents } from "./usePluginComponents";

export interface LauncherPluginSheetProps {
  open: boolean;
  onClose: () => void;
  candidate: PluginLaunchCandidate;
  selected: boolean;
  selectionDisabled?: boolean;
  onToggleSelected: (next: boolean) => void;
  /** The client the components read goes through: the spawn form's own. */
  client: Pick<AppwireClientLike, "request">;
  /** The launch source whose hub resolved the preview; the read follows it so
   * the Contents describe the plugins that host would load. */
  host?: string;
}

export function LauncherPluginSheet({
  open,
  onClose,
  candidate,
  selected,
  selectionDisabled = false,
  onToggleSelected,
  client,
  host,
}: LauncherPluginSheetProps): ReactElement {
  // The pair is the wire address when the candidate carries one; every other
  // source reads by the path the preview already resolved. A hub always sets
  // the path for a non-installed candidate, so the fallback only satisfies
  // the type.
  const target: PluginComponentsReadArgs["target"] =
    candidate.marketplace !== undefined
      ? { plugin: candidate.name, marketplace: candidate.marketplace }
      : { path: candidate.path ?? "" };
  const read = usePluginComponents({ client, host, target }, JSON.stringify(candidate));

  const metaRows: PluginMetadataRow[] = [
    ...(candidate.version
      ? [{ kind: "version" as const, label: "Version", value: `v${candidate.version}`, mono: true }]
      : []),
    ...(candidate.marketplace !== undefined
      ? [{ kind: "marketplace" as const, label: "Marketplace", value: candidate.marketplace }]
      : []),
    { kind: "source", label: "Source", value: candidate.source, mono: true },
  ];

  // Where the Contents block goes, above the metadata. Only a settled read
  // renders: the block appears when the answer lands, and a failure - the
  // plugin's own load, or the read itself - takes its place, never "Manifest
  // only" (that reading stays reserved for a plugin that loads and ships
  // nothing, the same rule the settings detail sheet follows).
  const contentsBlock =
    read.state.status === "error" ? (
      // The read failed for the whole method, not for this plugin: a quiet
      // degrade notice where Contents would be, with the read's own retry.
      <InlineNotice
        tone="neutral"
        data-reason="components-degraded"
        action={
          <Button size="sm" type="button" onClick={read.retry}>
            Retry
          </Button>
        }
      >
        {read.state.message}
      </InlineNotice>
    ) : read.state.status === "ready" && read.state.response.error !== undefined ? (
      // The row carries the plugin's own load failure: the plugin is the
      // problem, not the read, so no retry and no Contents.
      <InlineNotice tone="danger" data-reason="plugin-broken">
        {read.state.response.error}
      </InlineNotice>
    ) : read.state.status === "ready" ? (
      <PluginContents components={read.state.response.components ?? {}} />
    ) : null;

  return (
    <PluginSheet
      open={open}
      onClose={onClose}
      title={candidate.name}
      mode="launcher"
      description={candidate.description}
      contents={contentsBlock}
      metadata={metaRows}
    >
      <Switch
        checked={selected}
        disabled={selectionDisabled}
        onChange={onToggleSelected}
        label="Included in this session"
        description="Turn off to start this session without this plugin. Only this launch's plugin selection changes."
      />
    </PluginSheet>
  );
}
