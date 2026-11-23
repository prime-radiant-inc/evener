// ReasoningEffortControl: the "the value IS the control" effort picker shared
// by the session status row and the spawn card. Select's quiet variant owns the
// mechanics - a readout, a chevron, and a transparent native <select> laid over
// them at zero opacity, with the :focus-within ring on the visible face - so
// this component owns only what both callers repeated: the separator, the test
// hooks, and the option/readout derivation.
//
// An unset effort ("") means "the caller's default applies" and is a real
// leading option, never the first ladder level a bare value-"" select would
// display. defaultLevel is the tier an unset effort resolves to: the closed
// readout shows that tier (the effort actually used) and the open list labels
// the empty option "<tier> (default)", keeping "follow the default" distinct
// from pinning the same tier. "none" is an explicit off; effortLabel names it
// by whether the model's ladder can express it.
//
// Callers keep their incidental differences: the testid prefix (the wrapper
// keeps it, the readout gets `${testId}-value`), their native select id, their
// spoken name, and whether an explicit "none" is always offered. Disabled
// renders on the visible wrapper as well as the native select, so the quiet
// trigger drops its hover face and pointer cursor.

import { effortDefaultLabel, effortLabel, effortOptionLevels, effortReadout } from "@evener/appwire-client";
import { Select } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./reasoningeffortcontrol.module.css";

const CLASS = {
  control: requireClass(styles.control, "reasoningeffortcontrol.module.css", "control"),
  separator: requireClass(styles.separator, "reasoningeffortcontrol.module.css", "separator"),
};

export interface ReasoningEffortControlProps {
  /** The model's own effort ladder, as named on the wire. It drives both the
   * offered options and the "can this model express none" label. */
  levels: string[];
  /** The current effort; "" means the caller's default applies. */
  current: string;
  /** The tier an unset current resolves to, when the caller knows it. */
  defaultLevel: string | undefined;
  onChange: (level: string) => void;
  /** Testid prefix: the wrapper keeps it and the readout gets `${testId}-value`. */
  testId: string;
  disabled?: boolean;
  /** The caller's accessible name. Defaults to "Reasoning effort". */
  ariaLabel?: string;
  /** The caller's existing DOM id for the native control. */
  id?: string;
  /** Offer an explicit "none" even when the ladder does not name it: the spawn
   * card does, so an off is always one pick away. */
  offerNone?: boolean;
}

export function ReasoningEffortControl({
  levels,
  current,
  defaultLevel,
  onChange,
  testId,
  disabled = false,
  ariaLabel = "Reasoning effort",
  id,
  offerNone = false,
}: ReasoningEffortControlProps) {
  const offered = effortOptionLevels(offerNone ? levels.filter((level) => level !== "none") : levels, current);
  const options = offered.map((level) => ({
    value: level,
    label: level === "" ? effortDefaultLabel(defaultLevel, levels) : effortLabel(level, levels),
  }));
  if (offerNone && !offered.includes("none")) {
    options.push({ value: "none", label: effortLabel("none", levels) });
  }

  return (
    <span
      className={CLASS.control}
      data-effort-control=""
      data-testid={testId}
      data-disabled={disabled ? "true" : undefined}
    >
      <span className={CLASS.separator} aria-hidden="true">
        ·
      </span>
      <Select
        variant="quiet"
        id={id}
        ariaLabel={ariaLabel}
        value={current}
        options={options}
        readout={effortReadout(current, levels, defaultLevel)}
        onChange={(event) => onChange(event.target.value)}
        disabled={disabled}
        valueTestId={`${testId}-value`}
      />
    </span>
  );
}
