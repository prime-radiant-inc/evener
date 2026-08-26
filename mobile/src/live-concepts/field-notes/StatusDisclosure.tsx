import { type ReactNode, useId } from "react";
import { Icon } from "../shared/Icon";
import { StatusLabel, type StatusLabelInput } from "../shared/StatusLabel";

// Adapted from the concept-lab StatusDisclosure: a disclosure button that
// summarises a work/tool node, carries a non-color status marker, and
// reveals a labelled region. Uses live shared primitives only.

export interface StatusDisclosureProps {
  label: string;
  status: StatusLabelInput;
  expanded: boolean;
  onToggle(expanded: boolean): void;
  children: ReactNode;
}

export function StatusDisclosure({
  label,
  status,
  expanded,
  onToggle,
  children,
}: StatusDisclosureProps) {
  const generatedId = useId();
  const buttonId = `${generatedId}-button`;
  const regionId = `${generatedId}-region`;
  const statusId = `${generatedId}-status`;

  return (
    <div data-disclosure-state={expanded ? "expanded" : "collapsed"}>
      <button
        aria-controls={regionId}
        aria-describedby={statusId}
        aria-expanded={expanded}
        aria-label={label}
        id={buttonId}
        type="button"
        onClick={() => onToggle(!expanded)}
      >
        <span className="fn-disclosure-summary">
          <span>{label}</span>
          <span data-state-label aria-hidden="true">
            <StatusLabel state={status} />
          </span>
        </span>
        <Icon name="chevron" decorative />
      </button>
      <span className="fn-visually-hidden" id={statusId}>
        <StatusLabel state={status} />
      </span>
      {expanded ? (
        <section
          aria-describedby={statusId}
          aria-labelledby={buttonId}
          id={regionId}
        >
          {children}
        </section>
      ) : null}
    </div>
  );
}
