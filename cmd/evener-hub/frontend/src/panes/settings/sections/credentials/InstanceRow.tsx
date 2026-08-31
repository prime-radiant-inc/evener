// InstanceRow.tsx: one provider instance's tappable list row for the
// detail-sheet redesign - a single full-width button carrying identity and
// status only (heading dot, name, ★ default / from environment chips, one
// meta line, chevron).
// Every per-instance ACTION (test, set key, sign in, edit, clear, remove,
// make default) and the layered credential display moved into
// InstanceDetailSheet, so the list stays one-target-per-row on desktop and
// touch alike. Pure presentational: the section owns selection.
import type { InstanceEntry } from "../../../../protocol/types.gen";
import { Chevron, Chip, StatusDot } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { credentialLayers, keylessByDesign, styleInfoText, unconfiguredLabel } from "./credentialLabels";
import styles from "./InstanceRow.module.css";

const CLASS = {
  rowButton: requireClass(styles.rowButton, "InstanceRow.module.css", "rowButton"),
  rowMain: requireClass(styles.rowMain, "InstanceRow.module.css", "rowMain"),
  heading: requireClass(styles.heading, "InstanceRow.module.css", "heading"),
  name: requireClass(styles.name, "InstanceRow.module.css", "name"),
  meta: requireClass(styles.meta, "InstanceRow.module.css", "meta"),
  chevron: requireClass(styles.chevron, "InstanceRow.module.css", "chevron"),
};

// The one meta line: the unconfigured label is the more important signal and
// leads; style info (a gateway's base URL is the interesting part of "No key
// set · optional") follows it.
function metaText(instance: InstanceEntry): string {
  const unconfigured = unconfiguredLabel(instance);
  const styleInfo = styleInfoText(instance);
  return unconfigured === null ? styleInfo : `${unconfigured} · ${styleInfo}`;
}

export interface InstanceRowProps {
  instance: InstanceEntry;
  onSelect: () => void;
}

export function InstanceRow({ instance, onSelect }: InstanceRowProps) {
  const meta = metaText(instance);
  return (
    <li>
      <button type="button" className={CLASS.rowButton} onClick={onSelect}>
        <div className={CLASS.rowMain}>
          <div className={CLASS.heading}>
            <StatusDot state={credentialLayers(instance).length > 0 || keylessByDesign(instance) ? "idle" : "ended"} />
            <span className={CLASS.name}>{instance.name}</span>
            {instance.isDefault && <Chip>★ default</Chip>}
            {instance.implicit && <Chip>from environment</Chip>}
          </div>
          <div className={CLASS.meta}>{meta}</div>
        </div>
        <span className={CLASS.chevron} aria-hidden="true">
          <Chevron direction="right" />
        </span>
      </button>
    </li>
  );
}
