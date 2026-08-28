import type { ReactNode } from "react";
import { Chevron } from "../chevron";
import { requireClass } from "../internal/requireClass";
import styles from "./disclosure.module.css";
import { isDisclosureOpen, toggleDisclosure } from "./disclosureStore";

const CLASS = {
  details: requireClass(styles.details, "disclosure.module.css", "details"),
  summary: requireClass(styles.summary, "disclosure.module.css", "summary"),
  chevron: requireClass(styles.chevron, "disclosure.module.css", "chevron"),
  body: requireClass(styles.body, "disclosure.module.css", "body"),
};

interface DisclosureCommonProps {
  summary: ReactNode;
  children: ReactNode;
  disabled?: boolean;
  "data-testid"?: string;
}

type DisclosureStateProps =
  | { id: string; defaultOpen?: boolean; open?: never; onOpenChange?: never }
  | { open: boolean; onOpenChange(open: boolean): void; id?: never; defaultOpen?: never };

export type DisclosureProps = DisclosureCommonProps & DisclosureStateProps;

type StoreBackedDisclosureProps = DisclosureCommonProps & {
  id: string;
  defaultOpen?: boolean;
};

type ControlledDisclosureProps = DisclosureCommonProps & {
  open: boolean;
  onOpenChange(open: boolean): void;
};

interface DisclosureViewProps extends DisclosureCommonProps {
  open: boolean;
  requestToggle(): void;
}

function DisclosureView({
  summary,
  children,
  disabled = false,
  open,
  requestToggle,
  "data-testid": testId,
}: DisclosureViewProps) {
  return (
    <details className={CLASS.details} open={open} data-testid={testId}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; see ToolCallItem.tsx */}
      <summary
        className={CLASS.summary}
        aria-disabled={disabled || undefined}
        tabIndex={disabled ? -1 : undefined}
        onClick={(event) => {
          event.preventDefault();
          if (!disabled) requestToggle();
        }}
      >
        <span className={CLASS.chevron} aria-hidden="true" data-open={open ? "true" : "false"}>
          <Chevron />
        </span>
        {summary}
      </summary>
      {open && <div className={CLASS.body}>{children}</div>}
    </details>
  );
}

function StoreBackedDisclosure(props: StoreBackedDisclosureProps) {
  const fallback = props.defaultOpen ?? false;
  const open = isDisclosureOpen(props.id, fallback);
  return <DisclosureView {...props} open={open} requestToggle={() => toggleDisclosure(props.id, fallback)} />;
}

function ControlledDisclosure(props: ControlledDisclosureProps) {
  return <DisclosureView {...props} open={props.open} requestToggle={() => props.onOpenChange(!props.open)} />;
}

export function Disclosure(props: DisclosureProps) {
  if ("open" in props) {
    return <ControlledDisclosure {...(props as ControlledDisclosureProps)} />;
  }
  return <StoreBackedDisclosure {...(props as StoreBackedDisclosureProps)} />;
}
