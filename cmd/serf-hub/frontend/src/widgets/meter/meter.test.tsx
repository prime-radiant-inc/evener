import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
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
  render(<Meter value={30} max={100} />);
  const meter = screen.getByRole("meter");
  expect(meter.getAttribute("aria-valuenow")).toBe("30");
  expect(meter.getAttribute("aria-valuemin")).toBe("0");
  expect(meter.getAttribute("aria-valuemax")).toBe("100");
});

test("sets the fill's --fill custom property to the value/max percentage", () => {
  render(<Meter value={25} max={100} />);
  const fill = screen.getByTestId("meter-fill");
  expect(fill.style.getPropertyValue("--fill")).toBe("25%");
});

test("clamps the fill at 100% when value exceeds max", () => {
  render(<Meter value={150} max={100} />);
  const meter = screen.getByRole("meter");
  expect(meter.getAttribute("aria-valuenow")).toBe("100");
  expect(screen.getByTestId("meter-fill").style.getPropertyValue("--fill")).toBe("100%");
});

test("clamps the fill at 0% when value is negative", () => {
  render(<Meter value={-10} max={100} />);
  const meter = screen.getByRole("meter");
  expect(meter.getAttribute("aria-valuenow")).toBe("0");
  expect(screen.getByTestId("meter-fill").style.getPropertyValue("--fill")).toBe("0%");
});

test("defaults to the neutral tone", () => {
  render(<Meter value={10} max={100} />);
  expect(screen.getByTestId("meter-fill").classList.contains(styles.neutral)).toBe(true);
});

const TONES: MeterTone[] = ["neutral", "attention", "alive", "danger"];

for (const tone of TONES) {
  test(`tone ${tone} maps to its token family class`, () => {
    render(<Meter value={10} max={100} tone={tone} />);
    expect(screen.getByTestId("meter-fill").classList.contains(styles[tone])).toBe(true);
  });
}

test("is not in the tab order - a meter is a passive readout", () => {
  render(<Meter value={10} max={100} />);
  expect(screen.getByRole("meter").getAttribute("tabindex")).toBeNull();
});
