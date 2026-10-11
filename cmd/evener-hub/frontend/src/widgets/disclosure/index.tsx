import type { ReactNode } from "react";
import { Chevron } from "../chevron";
import { requireClass } from "../internal/requireClass";
import styles from "./disclosure.module.css";
import { isDisclosureOpen, setDisclosureOpen } from "./disclosureStore";

const CLASS = {
  details: requireClass(styles.details, "disclosure.module.css", "details"),
  summary: requireClass(styles.summary, "disclosure.module.css", "summary"),
  chevron: requireClass(styles.chevron, "disclosure.module.css", "chevron"),
  body: requireClass(styles.body, "disclosure.module.css", "body"),
};

interface DisclosureCommonProps {
  summary: ReactNode;
  children: ReactNode;
  /** "quiet" drops the hover fill, the open-state wash, and the summary's
   * inline inset: chevron and summary text in the content column, the body
   * below. For rows inside a reading surface such as the transcript. */
  variant?: "default" | "quiet";
  disabled?: boolean;
  "data-testid"?: string;
}

type DisclosureStateProps =
  | { id: string; defaultOpen?: boolean; ignoreBaseline?: boolean; open?: never; onOpenChange?: never }
  | {
      open: boolean;
      onOpenChange(open: boolean): void;
      id?: never;
      defaultOpen?: never;
      ignoreBaseline?: never;
    };

export type DisclosureProps = DisclosureCommonProps & DisclosureStateProps;

type StoreBackedDisclosureProps = DisclosureCommonProps & {
  id: string;
  defaultOpen?: boolean;
  /** Keep the row on `defaultOpen` through the transcript's expand-all and
   * collapse-all baselines; a reader's own click still wins. */
  ignoreBaseline?: boolean;
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
  variant = "default",
  disabled = false,
  open,
  requestToggle,
  "data-testid": testId,
}: DisclosureViewProps) {
  return (
    <details className={CLASS.details} open={open} data-testid={testId} data-variant={variant}>
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
        {summary}
        <span className={CLASS.chevron} aria-hidden="true" data-open={open ? "true" : "false"}>
          <Chevron />
        </span>
      </summary>
      {open && <div className={CLASS.body}>{children}</div>}
    </details>
  );
}

function StoreBackedDisclosure({ ignoreBaseline, ...props }: StoreBackedDisclosureProps) {
  const open = isDisclosureOpen(props.id, props.defaultOpen ?? false, ignoreBaseline ? { ignoreBaseline } : undefined);
  // Flip what the reader sees: an expand-all baseline can hold the row open
  // with no explicit choice recorded, and the click must still close it.
  return <DisclosureView {...props} open={open} requestToggle={() => setDisclosureOpen(props.id, !open)} />;
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
