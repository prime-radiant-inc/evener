import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { CadenceState } from "../cadence";
import { requireClass } from "../internal/requireClass";
import { StatusDot } from "./index";
import rawStyles from "./statusdot.module.css";

afterEach(cleanup);

const styles = {
  alive: requireClass(rawStyles.alive, "statusdot.module.css", "alive"),
  attention: requireClass(rawStyles.attention, "statusdot.module.css", "attention"),
  danger: requireClass(rawStyles.danger, "statusdot.module.css", "danger"),
  neutral: requireClass(rawStyles.neutral, "statusdot.module.css", "neutral"),
};

// state -> token family, mirroring Cadence's own STATE_FAMILY mapping (see
// src/widgets/cadence/index.tsx): working=alive, needs-you=attention,
// failed=danger, idle/ended=neutral.
const STATE_FAMILIES: [CadenceState, string, string][] = [
  ["idle", styles.neutral, "Idle"],
  ["working", styles.alive, "Working"],
  ["needs-you", styles.attention, "Needs you"],
  ["failed", styles.danger, "Failed"],
  ["ended", styles.neutral, "Ended"],
];

for (const [state, familyClass, label] of STATE_FAMILIES) {
  test(`state ${state} maps to its token family class`, () => {
    render(<StatusDot state={state} />);
    expect(screen.getByRole("img").classList.contains(familyClass)).toBe(true);
  });

  test(`state ${state} carries an accessible name naming the state`, () => {
    render(<StatusDot state={state} />);
    expect(screen.getByRole("img", { name: label })).toBeTruthy();
  });
}

test("is not in the tab order - it's a status indicator, not a control", () => {
  render(<StatusDot state="working" />);
  expect(screen.getByRole("img").getAttribute("tabindex")).toBeNull();
});
