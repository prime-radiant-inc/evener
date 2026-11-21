// The row press the cascadeguard drill uses. `drill` used to hand
// `driver.click(selector)` a selector: one page turn measured the row's
// centre and a later turn dispatched the press at those coordinates. The
// Overview moves rows in between - it restores a retained scroll anchor as a
// page arrives - so the press hit the paged delegate that had taken
// cascade-1's place, and the drill then waited 15s for a column the wrong
// delegate had selected (#7314).
//
// This expression finds, exposes and presses the row in ONE page turn, the
// discipline clickByText and revealControlStep already use (#3804, #3819):
// nothing asynchronous can move the list between the hit test and the press.
// It refuses a row whose control is not the topmost element at its own
// centre, so a covered row is never pressed, and it returns the anchor it
// pressed so the caller fails at once instead of timing out on another
// delegate's column. The anchor is read before the press and returned, so a
// handler that renames or unmounts the row cannot turn the result into null,
// which the driver's wait reads as "retry this page turn" and would press a
// second row.
export function pressRowExpr(selector) {
  return `(() => {
    const row = document.querySelector(${JSON.stringify(selector)});
    if (!row) return null;
    const anchor = row.dataset.activityAnchor;
    // A split row (a main button beside a branch toggle) carries the anchor on
    // its container; a plain row is the button itself. The main button is the
    // first button in document order, ahead of any trailing control.
    const control = row.matches("button") ? row : row.querySelector("button");
    if (!control) return null;
    row.scrollIntoView({ block: "center", inline: "center" });
    const rect = control.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return null;
    const x = rect.x + rect.width / 2;
    const y = rect.y + rect.height / 2;
    if (!control.contains(document.elementFromPoint(x, y))) return null;
    control.click();
    return anchor;
  })()`;
}
