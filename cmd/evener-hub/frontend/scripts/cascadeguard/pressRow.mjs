import { uncoveredCenter } from "../lib/pressPoint.mjs";

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
// It shares uncoveredCenter with skillguard, so a covered or zero-area control
// is never pressed, and it returns true only after pressing, so the driver's
// wait stops polling instead of pressing a second row.
//
// The selector already names the delegate, and the drill's column and track
// waits prove the press selected it; nothing else needs asserting here.
export function pressRowExpr(selector) {
  return `(() => {
    const row = document.querySelector(${JSON.stringify(selector)});
    if (!row) return null;
    // A split row (a main button beside a branch toggle) carries the anchor on
    // its container; a plain row is the button itself. The main button is the
    // first button in document order, ahead of any trailing control.
    const control = row.matches("button") ? row : row.querySelector("button");
    if (!control) return null;
    if (!(${uncoveredCenter})(control, "center")) return null;
    control.click();
    return true;
  })()`;
}
