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
  /** Makes the row's main area a real <button> that runs this - the whole
   * row without `trailingAction`, the area before the trailing slot with
   * it. */
  onSelect: () => void;
  disabled?: boolean;
  /** The row's one trailing control, rendered in the trailing slot before
   * the drill-in chevron. Two tab stops (main, then control) is the
   * accepted keyboard shape; the control swallows its own clicks, so it
   * never fires `onSelect`. */
  trailingAction?: ReactNode;
  action?: never;
}

interface ReadOnlyListRowProps extends ListRowContentProps {
  onSelect?: undefined;
  disabled?: never;
  /** The read-only row's one trailing control. §10 caps a row at one: more
   * than that belongs in the item's detail sheet. */
  action?: ReactNode;
  trailingAction?: never;
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
  split: requireClass(styles.split, "listrow.module.css", "split"),
  splitMain: requireClass(styles.splitMain, "listrow.module.css", "splitMain"),
  splitTrailing: requireClass(styles.splitTrailing, "listrow.module.css", "splitTrailing"),
};

const VARIANT_CLASS: Record<ListRowVariant, string> = {
  collection: requireClass(styles.collection, "listrow.module.css", "collection"),
  plain: requireClass(styles.plain, "listrow.module.css", "plain"),
};

/**
 * A full-width list row: leading glyph, label with state chips, one meta
 * line. With `onSelect` the row is interactive in two shapes: the whole row
 * is one real <button> (§10: the row is a single target on desktop and touch
 * alike), or, with `trailingAction`, a container whose main area is that
 * button and whose trailing slot holds the control before the drill-in
 * chevron. Without `onSelect` the row is a passive <div> that may carry one
 * trailing `action`. Wrapping rows in <ul>/<li> and spacing them is the
 * caller's layout.
 *
 * Forwards its ref to the row's focusable control when it has one - the
 * whole-row button, or the split row's main button - because that is what a
 * sheet restoring focus to the row that opened it must focus; a read-only row
 * has no such control, so its ref names the container. Other native
 * attributes spread onto the root, before its own type/className/onClick/
 * disabled so those stay the widget's, the same merge order as Button; the
 * ARIA that names, describes, refuses or relates the interaction rides the
 * control that owns it, not the roleless container around it.
 */
export const ListRow = forwardRef<HTMLElement, ListRowProps>(function ListRow(
  { label, leading, chips, meta, variant = "collection", onSelect, disabled, action, trailingAction, ...rest },
  ref,
) {
  const interactive = onSelect !== undefined;
  const split = interactive && trailingAction !== undefined;
  const className = [
    CLASS.row,
    VARIANT_CLASS[variant],
    leading !== undefined && !split ? CLASS.withLeading : "",
    // A split row's interaction belongs to its main button; the container
    // must not wear .interactive, or its own hover/disabled styling would
    // contradict the button's refusal state.
    interactive && !split ? CLASS.interactive : "",
    split ? CLASS.split : "",
  ]
    .filter(Boolean)
    .join(" ");
  const mainClassName = [CLASS.row, leading !== undefined ? CLASS.withLeading : "", CLASS.interactive, CLASS.splitMain]
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
    if (split) {
      // Invariant: the container is never a button and carries no click
      // handler - the main area owns the row's onSelect, so the trailing
      // control swallows its own clicks and never selects the row.
      // Expansion state belongs on that main button (the opener), not the
      // container: assistive tech announces it from the control that toggles.
      // Every ARIA attribute that names, describes, refuses or relates the
      // interaction rides the control that owns it; the container only
      // carries the remaining native attributes.
      const {
        "aria-disabled": ariaDisabled,
        "aria-label": ariaLabel,
        "aria-labelledby": ariaLabelledby,
        "aria-describedby": ariaDescribedby,
        "aria-description": ariaDescription,
        "aria-expanded": ariaExpanded,
        "aria-controls": ariaControls,
        "aria-haspopup": ariaHaspopup,
        "aria-current": ariaCurrent,
        ...containerRest
      } = rest;
      return (
        <div {...containerRest} className={className}>
          <button
            ref={ref as Ref<HTMLButtonElement>}
            type="button"
            className={mainClassName}
            aria-label={ariaLabel}
            aria-labelledby={ariaLabelledby}
            aria-describedby={ariaDescribedby}
            aria-description={ariaDescription}
            aria-disabled={ariaDisabled}
            aria-expanded={ariaExpanded}
            aria-controls={ariaControls}
            aria-haspopup={ariaHaspopup}
            aria-current={ariaCurrent}
            onClick={isAriaRefusal(ariaDisabled) ? refuseClick : onSelect}
            disabled={disabled}
          >
            {content}
          </button>
          <span className={`${CLASS.trailing} ${CLASS.splitTrailing}`}>
            {trailingAction}
            {variant === "collection" && (
              <span className={CLASS.chevron} aria-hidden="true">
                <Chevron direction="right" />
              </span>
            )}
          </span>
        </div>
      );
    }
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
