// The tool-row contracts shared by the intent-group and folded-run cases. One
// owner so the two contexts cannot drift: the Open control's 44px phone tap
// floor with both overhanging edges reachable, and the evidence band spanning
// its row's width.

const EDGE = 0.5;
const TAP_FLOOR = 44;

// The same intent-less ToolRow and OpenButton sit inside two different group
// layouts. Only the folded-run case prefixes the tool-call module's classes.
export function mountToolRows(prefix = "") {
  for (const call of document.querySelectorAll("[data-row-fixture]")) {
    const expanded = call.dataset.rowFixture === "expanded";
    call.innerHTML = `
      <div class="${prefix}row" data-testid="tool-row">
        <span class="${prefix}rowIconSlot"><span class="${prefix}rowIcon" data-testid="tool-row-icon" aria-hidden="true">
          <svg viewBox="0 0 16 16" width="14" height="14" style="display: block"><path d="M3.2 12.8 L3.9 10.2 L10.6 3.5 A1.5 1.5 0 0 1 12.7 5.6 L6 12.3 L3.2 12.8 Z" stroke="currentColor" stroke-width="1.75" fill="none"/></svg>
        </span></span>
        <div class="${prefix}summaryLine"><span class="${prefix}summary" data-testid="tool-row-summary">Wrote src/fizzbuzz.py</span><span class="inline" data-open-shell>
          <button type="button" class="button quiet squareSm" title="Open" aria-label="Open src/fizzbuzz.py beside"><span class="icon">
            <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" fill="none"><path d="M12.5 8.5V12.5H3.5V3.5H7.5"/><path d="M8 8L13 3M9.5 3H13V6.5"/></svg>
          </span></button>
        </span></div>
        <button type="button" class="${prefix}trigger" data-testid="tool-row-trigger" aria-expanded="${expanded}" aria-controls="${expanded ? "band" : "closed-body"}" aria-label="Wrote src/fizzbuzz.py"><span class="${prefix}chevron" aria-hidden="true" data-open="${expanded}" data-testid="tool-row-chevron">
          <svg viewBox="0 0 16 16" width="14" height="14" style="display: block"><path d="M6 3.5 L10.5 8 L6 12.5" stroke="currentColor" stroke-width="1.75" fill="none"/></svg>
        </span></button>
      </div>
      ${expanded ? `<div class="${prefix}body" id="band" data-testid="tool-call-body">evidence</div>` : ""}`;
  }
}

export function measureToolRows() {
  const open = document.querySelector('[data-row-fixture="collapsed"] [data-open-shell] button');
  const box = open.getBoundingClientRect();
  const x = box.left + box.width / 2;
  const top = document.elementFromPoint(x, box.top + 1);
  const bottom = document.elementFromPoint(x, box.bottom - 1);
  const describe = (el) => el ? `${el.localName}${el.id ? `#${el.id}` : ""}` : "nothing";
  const band = document.getElementById("band");
  const body = band.getBoundingClientRect();
  const row = band.closest('[data-testid="tool-call-item"]').getBoundingClientRect();
  return {
    openHeight: box.height,
    openTopReachesControl: open.contains(top),
    openTopHit: describe(top),
    openBottomReachesControl: open.contains(bottom),
    openBottomHit: describe(bottom),
    bandLeft: body.left,
    bandRight: body.right,
    bandBottom: body.bottom,
    rowLeft: row.left,
    rowRight: row.right,
  };
}

export function rowContractFailures(m) {
  const failures = [];
  if (m.openHeight < TAP_FLOOR)
    failures.push(`the Open control is ${m.openHeight}px tall, below the ${TAP_FLOOR}px phone tap floor`);
  if (!m.openTopReachesControl)
    failures.push(`a tap inside the Open control's top edge lands on ${m.openTopHit} - an ancestor clips its touch target`);
  if (!m.openBottomReachesControl)
    failures.push(
      `a tap inside the Open control's bottom edge lands on ${m.openBottomHit} - an ancestor clips its touch target`,
    );
  const leftInset = m.bandLeft - m.rowLeft;
  const rightInset = m.rowRight - m.bandRight;
  if (Math.abs(leftInset) > EDGE || Math.abs(rightInset) > EDGE)
    failures.push(`the evidence band is inset ${leftInset}px left and ${rightInset}px right inside its tool row`);
  return failures;
}
