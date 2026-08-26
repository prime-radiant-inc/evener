import { type ReactNode, useId } from "react";
import { Icon } from "./Icon";

export interface DisclosureProps {
  summary: ReactNode;
  expanded: boolean;
  onToggle: (expanded: boolean) => void;
  children: ReactNode;
}

export function Disclosure({
  summary,
  expanded,
  onToggle,
  children,
}: DisclosureProps) {
  const generatedId = useId();
  const buttonId = `${generatedId}-button`;
  const regionId = `${generatedId}-region`;

  return (
    <div data-disclosure-state={expanded ? "expanded" : "collapsed"}>
      <button
        aria-controls={regionId}
        aria-expanded={expanded}
        id={buttonId}
        type="button"
        onClick={() => onToggle(!expanded)}
      >
        {summary}
        <Icon name="chevron" decorative />
      </button>
      {expanded ? (
        <section aria-labelledby={buttonId} id={regionId}>
          {children}
        </section>
      ) : null}
    </div>
  );
}
