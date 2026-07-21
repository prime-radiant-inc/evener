import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { requireClass } from "../internal/requireClass";
import { Badge, type BadgeTone } from "./index";
import rawStyles from "./badge.module.css";

afterEach(cleanup);

const styles = {
  neutral: requireClass(rawStyles.neutral, "badge.module.css", "neutral"),
  attention: requireClass(rawStyles.attention, "badge.module.css", "attention"),
  alive: requireClass(rawStyles.alive, "badge.module.css", "alive"),
  danger: requireClass(rawStyles.danger, "badge.module.css", "danger"),
};

test("renders the count as text", () => {
  render(<Badge count={7} />);
  expect(screen.getByText("7")).toBeTruthy();
});

test("renders zero as literal 0, not hidden", () => {
  render(<Badge count={0} />);
  expect(screen.getByText("0")).toBeTruthy();
});

test("caps display at 99+ once count exceeds 99", () => {
  render(<Badge count={100} />);
  expect(screen.getByText("99+")).toBeTruthy();
  expect(screen.queryByText("100")).toBeNull();
});

test("shows the exact count at exactly 99, the cap boundary", () => {
  render(<Badge count={99} />);
  expect(screen.getByText("99")).toBeTruthy();
});

test("defaults to the neutral tone", () => {
  render(<Badge count={3} />);
  expect(screen.getByText("3").classList.contains(styles.neutral)).toBe(true);
});

const TONES: BadgeTone[] = ["neutral", "attention", "alive", "danger"];

for (const tone of TONES) {
  test(`tone ${tone} maps to its token family class`, () => {
    render(<Badge count={1} tone={tone} />);
    expect(screen.getByText("1").classList.contains(styles[tone])).toBe(true);
  });
}

test("declares no :focus-visible rule - badge is not interactive", () => {
  // Badge is a passive count/tone indicator (no onClick, no role of its
  // own), so unlike the interactive widgets in this batch it carries no
  // focus ring - this test documents that as deliberate, not an oversight.
  render(<Badge count={1} />);
  expect(screen.getByText("1").getAttribute("tabindex")).toBeNull();
});
