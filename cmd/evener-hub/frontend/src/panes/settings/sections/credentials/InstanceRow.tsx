// InstanceRow.tsx: one provider instance's list row for the detail-sheet
// redesign - a single full-width button carrying identity and status only
// (heading dot, name, ★ default / from environment chips, one meta line,
// chevron).
// Every per-instance ACTION (test, set key, sign in, clear, remove, make
// default) and the layered credential display moved into InstanceSheet,
// so the list stays one-target-per-row on desktop and touch alike. Pure
// presentational: the section owns selection.
//
// The read-only variant renders the same row - the same identity and meta, and
// the same chrome (CLASS.row) - with no button and no chevron: the host-scoped
// view of a remote host's own listing uses it, where nothing is actionable from
// this browser. One implementation keeps the two surfaces from drifting.
import type { InstanceEntry } from "@evener/appwire-client";
import {
  credentialLayers,
  fromEnvironment,
  keylessByDesign,
  styleInfoText,
  unconfiguredLabel,
} from "@evener/appwire-client";
import { Chevron, Chip, StatusDot } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import styles from "./InstanceRow.module.css";

const CLASS = {
  row: requireClass(styles.row, "InstanceRow.module.css", "row"),
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

/** The two variants, as a type rather than a comment: an interactive row MUST
 * carry onSelect (or its button renders with no handler), and a read-only row
 * has nothing to select. */
export type InstanceRowProps = { instance: InstanceEntry } & (
  | { readOnly: true }
  | { readOnly?: false; onSelect: () => void }
);

export function InstanceRow(props: InstanceRowProps) {
  const { instance } = props;
  const meta = metaText(instance);
  // Both variants render this node, so a read-only row states exactly what the
  // tappable one does.
  const body = (
    <div className={CLASS.rowMain}>
      <div className={CLASS.heading}>
        <StatusDot state={credentialLayers(instance).length > 0 || keylessByDesign(instance) ? "idle" : "ended"} />
        <span className={CLASS.name}>{instance.name}</span>
        {instance.isDefault && <Chip>★ default</Chip>}
        {fromEnvironment(instance) && <Chip>from environment</Chip>}
      </div>
      <div className={CLASS.meta}>{meta}</div>
    </div>
  );
  if (props.readOnly) {
    return <li className={CLASS.row}>{body}</li>;
  }
  return (
    <li>
      <button type="button" className={`${CLASS.row} ${CLASS.rowButton}`} onClick={props.onSelect}>
        {body}
        <span className={CLASS.chevron} aria-hidden="true">
          <Chevron direction="right" />
        </span>
      </button>
    </li>
  );
}
