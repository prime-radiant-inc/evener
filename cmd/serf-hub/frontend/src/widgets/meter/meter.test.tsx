import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { requireClass } from "../internal/requireClass";
import { Meter, type MeterTone } from "./index";
import rawStyles from "./meter.module.css";

afterEach(cleanup);

const styles = {
  neutral: requireClass(rawStyles.neutral, "meter.module.css", "neutral"),
  attention: requireClass(rawStyles.attention, "meter.module.css", "attention"),
  alive: requireClass(rawStyles.alive, "meter.module.css", "alive"),
  danger: requireClass(rawStyles.danger, "meter.module.css", "danger"),
};

test("renders role=meter with aria-valuenow/min/max reflecting value/max", () => {
  render(<Meter label="Context used" value={30} max={100} />);
  const meter = screen.getByRole("meter");
  expect(meter.getAttribute("aria-valuenow")).toBe("30");
  expect(meter.getAttribute("aria-valuemin")).toBe("0");
  expect(meter.getAttribute("aria-valuemax")).toBe("100");
});

// role=meter requires an accessible name (WAI-ARIA) - label is a required
// prop specifically so this can't ship unlabeled.
test("label is the meter's accessible name (aria-label)", () => {
  render(<Meter label="Context used" value={30} max={100} />);
  expect(screen.getByRole("meter", { name: "Context used" })).toBeTruthy();
});

test("sets the fill's --fill custom property to the value/max percentage", () => {
  render(<Meter label="Context used" value={25} max={100} />);
  const fill = screen.getByTestId("meter-fill");
  expect(fill.style.getPropertyValue("--fill")).toBe("25%");
});

test("clamps the fill at 100% when value exceeds max", () => {
  render(<Meter label="Context used" value={150} max={100} />);
  const meter = screen.getByRole("meter");
  expect(meter.getAttribute("aria-valuenow")).toBe("100");
  expect(screen.getByTestId("meter-fill").style.getPropertyValue("--fill")).toBe("100%");
});

test("clamps the fill at 0% when value is negative", () => {
  render(<Meter label="Context used" value={-10} max={100} />);
  const meter = screen.getByRole("meter");
  expect(meter.getAttribute("aria-valuenow")).toBe("0");
  expect(screen.getByTestId("meter-fill").style.getPropertyValue("--fill")).toBe("0%");
});

test("defaults to the neutral tone", () => {
  render(<Meter label="Context used" value={10} max={100} />);
  expect(screen.getByTestId("meter-fill").classList.contains(styles.neutral)).toBe(true);
});

const TONES: MeterTone[] = ["neutral", "attention", "alive", "danger"];

for (const tone of TONES) {
  test(`tone ${tone} maps to its token family class`, () => {
    render(<Meter label="Context used" value={10} max={100} tone={tone} />);
    expect(screen.getByTestId("meter-fill").classList.contains(styles[tone])).toBe(true);
  });
}

test("is not in the tab order - a meter is a passive readout", () => {
  render(<Meter label="Context used" value={10} max={100} />);
  expect(screen.getByRole("meter").getAttribute("tabindex")).toBeNull();
});
