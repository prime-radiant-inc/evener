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
import { Button, Chevron, Chip, StatusDot } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { VisuallyHidden } from "../../../../widgets/internal/VisuallyHidden";
import styles from "./InstanceRow.module.css";

const CLASS = {
  row: requireClass(styles.row, "InstanceRow.module.css", "row"),
  rowButton: requireClass(styles.rowButton, "InstanceRow.module.css", "rowButton"),
  rowMain: requireClass(styles.rowMain, "InstanceRow.module.css", "rowMain"),
  heading: requireClass(styles.heading, "InstanceRow.module.css", "heading"),
  name: requireClass(styles.name, "InstanceRow.module.css", "name"),
  meta: requireClass(styles.meta, "InstanceRow.module.css", "meta"),
  chevron: requireClass(styles.chevron, "InstanceRow.module.css", "chevron"),
  action: requireClass(styles.action, "InstanceRow.module.css", "action"),
};

// The one meta line: the unconfigured label is the more important signal and
// leads; style info (a gateway's base URL is the interesting part of "No key
// set · optional") follows it.
// A credential the hub found an error with leads the line the same way:
// "Error" is the most important thing the row can say (#3539).
function metaText(instance: InstanceEntry, authError: string | undefined): string {
  const lead = authError ? "Error" : unconfiguredLabel(instance);
  const styleInfo = styleInfoText(instance);
  return lead === null ? styleInfo : `${lead} · ${styleInfo}`;
}

/** The two variants, as a type rather than a comment: an interactive row MUST
 * carry onSelect (or its button renders with no handler), and a read-only row
 * has nothing to select. */
export type InstanceRowProps = {
  instance: InstanceEntry;
  /** The hub's error for this instance's credential (AuthStatusResponse.error):
   * a credential the provider rejected, or one it could not read. */
  authError?: string;
} & (
  | {
      readOnly: true;
      /** Remote scope only: begins a device-code sign-in ON the host that owns
       * this instance (component 07d's "Sign in on host"). Absent leaves the row
       * exactly the read-only row it was. */
      onHostSignIn?: (name: string) => void;
    }
  | { readOnly?: false; onSelect: () => void }
);

export function InstanceRow(props: InstanceRowProps) {
  const { instance, authError } = props;
  const meta = metaText(instance, authError);
  const dot = authError
    ? "failed"
    : credentialLayers(instance).length > 0 || keylessByDesign(instance)
      ? "idle"
      : "ended";
  // Both variants render this node, so a read-only row states exactly what the
  // tappable one does.
  const body = (
    <div className={CLASS.rowMain}>
      <div className={CLASS.heading}>
        <StatusDot state={dot} />
        <span className={CLASS.name}>{instance.name}</span>
        {instance.isDefault && <Chip>★ default</Chip>}
        {fromEnvironment(instance) && <Chip>from environment</Chip>}
      </div>
      <div className={CLASS.meta}>{meta}</div>
    </div>
  );
  if (props.readOnly) {
    return (
      <li className={CLASS.row}>
        {body}
        {/* "Sign in on host" is offered only where the caller supplies the
            action: the remote scope's Codex rows (ProviderInstanceGroups gates
            on supportsHostDeviceSignIn), never a local row and never a
            provider whose sign-in needs a browser the host does not have. */}
        {props.onHostSignIn !== undefined && (
          <div className={CLASS.action}>
            {/* The visible text is the same for every Codex row, so the
                accessible name must also name the instance this control signs
                in on: with two Codex-capable instances on one host, a
                screen-reader or voice-control user is otherwise offered
                identically named controls and cannot tell which one each acts
                on (issue #2285). The instance is APPENDED to the visible label
                as a visually-hidden suffix rather than set as an aria-label,
                which would REPLACE the name: WCAG 2.5.3 (Label in Name) needs
                the accessible name to contain the words on the button, or a
                voice-control user who says "Sign in on host" has no name to
                hit. The space before the suffix is load-bearing: the name is the
                button's children concatenated, so without it the two halves run
                together (see ModelSwitchTrigger). */}
            <Button variant="quiet" onClick={() => props.onHostSignIn?.(instance.name)}>
              Sign in on host <VisuallyHidden>to {instance.name}</VisuallyHidden>
            </Button>
          </div>
        )}
      </li>
    );
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
