import type { ComponentProps, ReactNode } from "react";
import { useIsMobile } from "../../shell/useIsMobile";
import { PropertyList, type PropertyListRow, Sheet } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./pluginSheet.module.css";

const CLASS = {
  description: requireClass(styles.description, "pluginSheet.module.css", "description"),
  chips: requireClass(styles.chips, "pluginSheet.module.css", "chips"),
  metadata: requireClass(styles.metadata, "pluginSheet.module.css", "metadata"),
  controls: requireClass(styles.controls, "pluginSheet.module.css", "controls"),
};

export interface PluginMetadataRow extends PropertyListRow {
  kind: "version" | "marketplace" | "source" | "author";
}

type PluginSheetProps = Omit<ComponentProps<typeof Sheet>, "side" | "children"> & {
  mode: "installed" | "catalog" | "launcher";
  description?: string;
  chips?: ReactNode;
  contents?: ReactNode;
  metadata: PluginMetadataRow[];
  children?: ReactNode;
};

/** Every plugin detail mode shares presentation and viewport geometry. The
 * caller owns reads and contextual actions, never a second sheet body. */
export function PluginSheet({ mode, description, chips, contents, metadata, children, ...sheet }: PluginSheetProps) {
  const isMobile = useIsMobile();
  return (
    <Sheet {...sheet} side={isMobile ? "bottom" : "right"}>
      {sheet.open && (
        <div data-kind="plugin-detail" data-mode={mode}>
          {chips && <div className={CLASS.chips}>{chips}</div>}
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
          {children && <div className={CLASS.controls}>{children}</div>}
        </div>
      )}
    </Sheet>
  );
}
