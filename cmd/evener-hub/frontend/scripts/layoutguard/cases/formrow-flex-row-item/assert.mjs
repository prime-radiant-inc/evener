// The FormRow (and the PathField trigger inside it) must stay inside the
// invented flex row, and the long path must be clipped by the trigger's own
// ellipsis rather than widening the row. Deleting formrow.module.css's
// .root { min-width: 0 } makes the FormRow's automatic minimum size its
// content's full nowrap width, so it overflows the row.
export default function assert(m) {
  const failures = [];
  if (m.formRowOverflowsRow > 1)
    failures.push(
      `the FormRow overflows its flex row by ${m.formRowOverflowsRow.toFixed(1)}px (FormRow ${m.formRow.width.toFixed(1)}px wide in a ${m.row.width.toFixed(1)}px row) - formrow.module.css's .root is missing min-width:0`,
    );
  if (m.triggerOverflowsRow > 1)
    failures.push(`the PathField trigger overflows the flex row by ${m.triggerOverflowsRow.toFixed(1)}px`);
  if (m.chipOverflowsRow > 1)
    failures.push(`the sibling chip is pushed ${m.chipOverflowsRow.toFixed(1)}px past the row's edge`);
  if (m.formRowSlack < -1)
    failures.push(`the FormRow runs ${(-m.formRowSlack).toFixed(1)}px into the sibling chip's space`);
  if (!m.valueEllipsizes)
    failures.push(
      `the path value is not clipped by its own ellipsis (scrollWidth ${m.valueScrollWidth}px vs clientWidth ${m.valueClientWidth}px, overflow-x ${m.valueOverflowX}) - either the row widened to fit the whole path, the value box stopped clipping, or the fixture no longer puts the row under pressure`,
    );
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason: `FormRow shrinks to fit the ${m.row.width.toFixed(1)}px row beside the chip (${m.formRowSlack.toFixed(1)}px slack before chip and gap) and the path ellipsizes (${m.valueScrollWidth}px of text in ${m.valueClientWidth}px)`,
  };
}
