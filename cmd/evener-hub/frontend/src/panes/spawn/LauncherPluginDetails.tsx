// LauncherPluginDetails: the launcher mode of the shared plugin detail, the
// inline body a row's Details action expands under that row (the sheet it
// used to open inside the selection sheet is gone). One launch candidate
// arrives as a prop together with the selection state its row shows;
// launcher mode owns no editor - no install, remove or enable - so the body
// is the Contents block, the candidate's metadata, and the one switch that
// mirrors the row. The candidate names its own resolved directory, so the
// components read starts when the body mounts; the panel mounts the body
// only while a candidate's details are expanded, and a read that fails
// degrades quietly where Contents would be rather than blocking the
// selection work around it.

import type { AppwireClientLike, PluginComponentsReadArgs, PluginLaunchCandidate } from "@evener/appwire-client";
import type { PluginComponentsEntry } from "@evener/appwire-client/state/extensions";
import { type ReactElement, useRef } from "react";
import { Button, Switch, useFocusRehome } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { PluginContentsBlock } from "../plugins/PluginContents";
import { PluginDetail, type PluginMetadataRow } from "../plugins/PluginDetail";
import styles from "./pluginSelection.module.css";
import { usePluginComponents } from "./usePluginComponents";

const CLASS = {
  detailBody: requireClass(styles.detailBody, "pluginSelection.module.css", "detailBody"),
};

export interface LauncherPluginDetailsProps {
  candidate: PluginLaunchCandidate;
  selected: boolean;
  selectionDisabled?: boolean;
  onCollapse: () => void;
  onToggleSelected: (next: boolean) => void;
  /** The client the components read goes through: the spawn form's own. */
  client: Pick<AppwireClientLike, "request">;
  /** The launch source whose hub resolved the preview; the read follows it so
   * the Contents describe the plugins that host would load. */
  host?: string;
}

function contentsEntry(state: ReturnType<typeof usePluginComponents>["state"]): PluginComponentsEntry | undefined {
  if (state.status === "error") return { status: "error", source: "request", error: state.message };
  if (state.status !== "ready") return undefined;
  if (state.response.error !== undefined) {
    return { status: "error", source: "target", error: state.response.error };
  }
  return { status: "loaded", components: state.response.components ?? {} };
}

export function LauncherPluginDetails({
  candidate,
  selected,
  selectionDisabled = false,
  onCollapse,
  onToggleSelected,
  client,
  host,
}: LauncherPluginDetailsProps): ReactElement {
  // The preview's path identifies the winner. Its manifest name need not
  // match the catalog name used in installed registry keys.
  const target: PluginComponentsReadArgs["target"] = { path: candidate.path ?? "" };
  const read = usePluginComponents({ client, host, target }, JSON.stringify(candidate));
  // Remove-only selection disables its own switch after deselection. Cover
  // all body controls, not just the shared contents block's Retry.
  const focusRoot = useRef<HTMLDivElement>(null);
  useFocusRehome(focusRoot);

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
  // nothing, the same rule the settings detail body follows).
  const contentsBlock = <PluginContentsBlock entry={contentsEntry(read.state)} onRetry={read.retry} />;

  return (
    <div ref={focusRoot} className={CLASS.detailBody}>
      <PluginDetail mode="launcher" description={candidate.description} contents={contentsBlock} metadata={metaRows}>
        <Switch
          checked={selected}
          disabled={selectionDisabled}
          onChange={onToggleSelected}
          label="Included in this session"
          description="Turn off to start this session without this plugin. Only this launch's plugin selection changes."
        />
        {/* The body's own way out: the row above collapses it too, but the
         * body needs a control no remove-only selection state can disable -
         * both for the keyboard and as the focus rehome's target. */}
        <Button variant="link" size="xs" type="button" onClick={onCollapse}>
          Hide details
        </Button>
      </PluginDetail>
    </div>
  );
}
