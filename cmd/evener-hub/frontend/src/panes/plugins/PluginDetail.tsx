import type { ReactNode } from "react";
import { Button, PropertyList, type PropertyListRow } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./pluginDetail.module.css";
import { handleDetailEscape } from "./pluginRowFocus";

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
  /** The expanded region's DOM id, so the row's opener can point here with
   * aria-controls. */
  id?: string;
  mode: "installed" | "catalog" | "launcher";
  description?: string;
  chips?: ReactNode;
  contents?: ReactNode;
  metadata: PluginMetadataRow[];
  children?: ReactNode;
  /** Renders the body's own way out below the controls: the row above
   * collapses it too, but a long body should not make the keyboard scroll
   * back up - and the focus rehome needs a control that no selection or
   * mutation state can disable. */
  onCollapse?: () => void;
}

/** Every plugin detail mode shares presentation: the inline body a row's
 * expansion mounts under that row. The caller owns reads and contextual
 * actions, never a second body. */
export function PluginDetail({
  id,
  mode,
  description,
  chips,
  contents,
  metadata,
  children,
  onCollapse,
}: PluginDetailProps) {
  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: Escape is the keyboard dismissal path for the expanded body, the same contract OverlayPanel gives its own panels; every pointer action inside stays on real controls
    <div
      id={id}
      data-kind="plugin-detail"
      data-mode={mode}
      onKeyDown={(event) => handleDetailEscape(event, onCollapse)}
    >
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
      {(children !== undefined || onCollapse !== undefined) && (
        <div className={CLASS.controls}>
          {children}
          {onCollapse !== undefined && (
            <Button variant="link" size="xs" type="button" onClick={onCollapse}>
              Hide details
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
