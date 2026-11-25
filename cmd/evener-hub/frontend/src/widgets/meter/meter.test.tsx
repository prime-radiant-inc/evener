import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { requireClass } from "../internal/requireClass";
import { Meter, type MeterTone } from "./index";
import rawStyles from "./meter.module.css";

const styles = {
  neutral: requireClass(rawStyles.fillNeutral, "meter.module.css", "fillNeutral"),
  attention: requireClass(rawStyles.fillAttention, "meter.module.css", "fillAttention"),
  alive: requireClass(rawStyles.fillAlive, "meter.module.css", "fillAlive"),
  danger: requireClass(rawStyles.fillDanger, "meter.module.css", "fillDanger"),
};

const ringFillEmptyClass = requireClass(rawStyles.ringFillEmpty, "meter.module.css", "ringFillEmpty");
const RING_CIRCUMFERENCE = 2 * Math.PI * 5.7;

test("renders role=meter with aria-valuenow/min/max reflecting value/max", () => {
  render(<Meter label="Context used" value={30} max={100} />);
  const meter = screen.getByRole("meter");
  expect(meter.getAttribute("aria-valuenow")).toBe("30");
  expect(meter.getAttribute("aria-valuemin")).toBe("0");
  expect(meter.getAttribute("aria-valuemax")).toBe("100");
  expect(meter.getAttribute("data-variant")).toBe("bar");
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

test("ring variant exposes role=meter, aria attributes, and an aria-hidden 14px SVG", () => {
  render(<Meter label="Context used" value={67} max={100} variant="ring" />);
  const meter = screen.getByRole("meter", { name: "Context used" });
  expect(meter.getAttribute("data-variant")).toBe("ring");
  expect(meter.getAttribute("aria-valuenow")).toBe("67");
  expect(meter.getAttribute("aria-valuemin")).toBe("0");
  expect(meter.getAttribute("aria-valuemax")).toBe("100");
  expect(meter.getAttribute("tabindex")).toBeNull();

  const svg = meter.querySelector("svg");
  expect(svg).toBeTruthy();
  expect(svg?.getAttribute("aria-hidden")).toBe("true");
  expect(svg?.getAttribute("viewBox")).toBe("0 0 16 16");
  expect(svg?.getAttribute("width")).toBe("14");
  expect(svg?.getAttribute("height")).toBe("14");

  const fill = screen.getByTestId("meter-fill");
  expect(fill.classList.contains(ringFillEmptyClass)).toBe(false);
  expect(Number(fill.getAttribute("stroke-dasharray"))).toBeCloseTo(RING_CIRCUMFERENCE, 2);
  expect(Number(fill.getAttribute("stroke-dashoffset"))).toBeCloseTo(RING_CIRCUMFERENCE * (1 - 0.67), 2);
});

test("ring variant clamps above max and hides the stroke dot at 0% and below", () => {
  const { rerender } = render(<Meter label="Context used" value={150} max={100} variant="ring" />);
  expect(screen.getByRole("meter").getAttribute("aria-valuenow")).toBe("100");
  let fill = screen.getByTestId("meter-fill");
  expect(fill.getAttribute("stroke-dashoffset")).toBe("0");
  expect(fill.classList.contains(ringFillEmptyClass)).toBe(false);

  rerender(<Meter label="Context used" value={0} max={100} variant="ring" />);
  expect(screen.getByRole("meter").getAttribute("aria-valuenow")).toBe("0");
  fill = screen.getByTestId("meter-fill");
  expect(fill.classList.contains(ringFillEmptyClass)).toBe(true);
  expect(fill.getAttribute("stroke-dashoffset")).toBe(fill.getAttribute("stroke-dasharray"));

  rerender(<Meter label="Context used" value={-10} max={100} variant="ring" />);
  expect(screen.getByRole("meter").getAttribute("aria-valuenow")).toBe("0");
  fill = screen.getByTestId("meter-fill");
  expect(fill.classList.contains(ringFillEmptyClass)).toBe(true);
  expect(fill.getAttribute("stroke-dashoffset")).toBe(fill.getAttribute("stroke-dasharray"));
  expect(Number(fill.getAttribute("stroke-dashoffset"))).toBeCloseTo(RING_CIRCUMFERENCE, 2);
});

test.each([
  { label: "non-positive max=0", value: 10, max: 0 },
  { label: "negative max=-5", value: 10, max: -5 },
  { label: "NaN value", value: Number.NaN, max: 100 },
])("ring variant handles $label without NaN and applies empty stroke class", ({ value, max }) => {
  render(<Meter label="Context used" value={value} max={max} variant="ring" />);
  const meter = screen.getByRole("meter");
  expect(meter.getAttribute("aria-valuenow")).toBe("0");
  const fill = screen.getByTestId("meter-fill");
  const offset = fill.getAttribute("stroke-dashoffset");
  expect(offset).not.toBe("NaN");
  expect(Number.isFinite(Number(offset))).toBe(true);
  expect(fill.classList.contains(ringFillEmptyClass)).toBe(true);
});

for (const tone of TONES) {
  test(`ring variant tone ${tone} maps to its token family class`, () => {
    render(<Meter label="Context used" value={40} max={100} tone={tone} variant="ring" />);
    expect(screen.getByRole("meter").getAttribute("data-variant")).toBe("ring");
    expect(screen.getByTestId("meter-fill").classList.contains(styles[tone])).toBe(true);
  });
}
