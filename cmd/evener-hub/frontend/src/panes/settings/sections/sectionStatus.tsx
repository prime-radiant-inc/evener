// Local to the settings sections: the quiet status line is section chrome,
// not a widget.
import type { ReactNode } from "react";
import surfaceStyles from "../../../styles/surface.module.css";
import { requireClass } from "../../../widgets/internal/requireClass";

const STATUS = requireClass(surfaceStyles.settingsSectionStatus, "surface.module.css", "settingsSectionStatus");

export function SectionStatus({ reason, children }: { reason?: string; children: ReactNode }) {
  return (
    <p className={STATUS} role="status" data-reason={reason}>
      {children}
    </p>
  );
}
