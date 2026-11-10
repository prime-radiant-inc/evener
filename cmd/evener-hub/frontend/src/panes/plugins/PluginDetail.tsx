import type { ReactNode } from "react";
import { PropertyList, type PropertyListRow } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./pluginDetail.module.css";

const CLASS = {
  description: requireClass(styles.description, "pluginDetail.module.css", "description"),
  chips: requireClass(styles.chips, "pluginDetail.module.css", "chips"),
  metadata: requireClass(styles.metadata, "pluginDetail.module.css", "metadata"),
  controls: requireClass(styles.controls, "pluginDetail.module.css", "controls"),
};

export interface PluginMetadataRow extends PropertyListRow {
  kind: "version" | "marketplace" | "source" | "author";
}

export interface PluginDetailProps {
  mode: "installed" | "catalog" | "launcher";
  description?: string;
  chips?: ReactNode;
  contents?: ReactNode;
  metadata: PluginMetadataRow[];
  children?: ReactNode;
}

/** Every plugin detail mode shares presentation: the inline body a row's
 * expansion mounts under that row (the slide-out sheet is gone). The caller
 * owns reads and contextual actions, never a second body. */
export function PluginDetail({ mode, description, chips, contents, metadata, children }: PluginDetailProps) {
  return (
    <div data-kind="plugin-detail" data-mode={mode}>
      {chips !== undefined && <div className={CLASS.chips}>{chips}</div>}
      {description !== undefined && <p className={CLASS.description}>{description}</p>}
      {contents}
      <div className={CLASS.metadata} data-kind="plugin-metadata">
        <PropertyList
          rows={metadata.map(({ kind, ...row }) => ({
            ...row,
            value: <span data-kind={`plugin-${kind}`}>{row.value}</span>,
          }))}
        />
      </div>
      {children !== undefined && <div className={CLASS.controls}>{children}</div>}
    </div>
  );
}
