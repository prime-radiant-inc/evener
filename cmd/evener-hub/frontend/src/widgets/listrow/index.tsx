import { forwardRef, type HTMLAttributes, type ReactNode, type Ref } from "react";
import { isAriaRefusal, refuseClick } from "../button";
import { Chevron } from "../chevron";
import { requireClass } from "../internal/requireClass";
import styles from "./listrow.module.css";

/** collection is design-system.md §10's collection row: a framed surface row
 * that opens the item's detail sheet, so an interactive one ends in the
 * drill-in chevron and its meta line is mono. plain is a row inside another
 * surface - a picker's choice, an activity-sidebar entry: no frame, no chevron, a denser box and a quiet sans meta
 * line. */
export type ListRowVariant = "collection" | "plain";

interface ListRowContentProps
  extends Omit<HTMLAttributes<HTMLElement>, "children" | "className" | "onClick" | "onSelect"> {
  /** The item's name; one line, truncating. */
  label: ReactNode;
  /** A glyph before the label (a StatusDot, an activity glyph). */
  leading?: ReactNode;
  /** State chips after the label. */
  chips?: ReactNode;
  /** One secondary line under the label; truncates. */
  meta?: ReactNode;
  variant?: ListRowVariant;
}

interface InteractiveListRowProps extends ListRowContentProps {
  /** Makes the whole row one <button>. */
  onSelect: () => void;
  disabled?: boolean;
  action?: never;
}

interface ReadOnlyListRowProps extends ListRowContentProps {
  onSelect?: undefined;
  disabled?: never;
  /** The read-only row's one trailing control. §10 caps a row at one: more
   * than that belongs in the item's detail sheet. */
  action?: ReactNode;
}

export type ListRowProps = InteractiveListRowProps | ReadOnlyListRowProps;

const CLASS = {
  row: requireClass(styles.row, "listrow.module.css", "row"),
  withLeading: requireClass(styles.withLeading, "listrow.module.css", "withLeading"),
  interactive: requireClass(styles.interactive, "listrow.module.css", "interactive"),
  leading: requireClass(styles.leading, "listrow.module.css", "leading"),
  heading: requireClass(styles.heading, "listrow.module.css", "heading"),
  label: requireClass(styles.label, "listrow.module.css", "label"),
  chips: requireClass(styles.chips, "listrow.module.css", "chips"),
  meta: requireClass(styles.meta, "listrow.module.css", "meta"),
  trailing: requireClass(styles.trailing, "listrow.module.css", "trailing"),
  chevron: requireClass(styles.chevron, "listrow.module.css", "chevron"),
};

const VARIANT_CLASS: Record<ListRowVariant, string> = {
  collection: requireClass(styles.collection, "listrow.module.css", "collection"),
  plain: requireClass(styles.plain, "listrow.module.css", "plain"),
};

/**
 * A full-width list row: leading glyph, label with state chips, one meta
 * line. With `onSelect` the whole row is one real <button> (§10: the row is a
 * single target on desktop and touch alike); without it the row is a passive
 * <div> that may carry one trailing `action`. Wrapping rows in <ul>/<li> and
 * spacing them is the caller's layout.
 *
 * Forwards its ref to the root (the button when interactive - a sheet that
 * restores focus to the row that opened it needs it) and spreads other native
 * attributes onto it, before its own type/className/onClick/disabled so those
 * stay the widget's, the same merge order as Button.
 */
export const ListRow = forwardRef<HTMLElement, ListRowProps>(function ListRow(
  { label, leading, chips, meta, variant = "collection", onSelect, disabled, action, ...rest },
  ref,
) {
  const interactive = onSelect !== undefined;
  const className = [
    CLASS.row,
    VARIANT_CLASS[variant],
    leading !== undefined ? CLASS.withLeading : "",
    interactive ? CLASS.interactive : "",
  ]
    .filter(Boolean)
    .join(" ");
  const content = (
    <>
      {leading !== undefined && <span className={CLASS.leading}>{leading}</span>}
      <span className={CLASS.heading}>
        <span className={CLASS.label}>{label}</span>
        {chips !== undefined && <span className={CLASS.chips}>{chips}</span>}
      </span>
      {meta !== undefined && <span className={CLASS.meta}>{meta}</span>}
    </>
  );
  if (interactive) {
    return (
      <button
        ref={ref as Ref<HTMLButtonElement>}
        {...rest}
        type="button"
        className={className}
        onClick={isAriaRefusal(rest["aria-disabled"]) ? refuseClick : onSelect}
        disabled={disabled}
      >
        {content}
        {variant === "collection" && (
          <span className={`${CLASS.trailing} ${CLASS.chevron}`} aria-hidden="true">
            <Chevron direction="right" />
          </span>
        )}
      </button>
    );
  }
  return (
    <div ref={ref as Ref<HTMLDivElement>} {...rest} className={className}>
      {content}
      {action !== undefined && <span className={CLASS.trailing}>{action}</span>}
    </div>
  );
});
