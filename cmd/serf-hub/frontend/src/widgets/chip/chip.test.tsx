import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { requireClass } from "../internal/requireClass";
import rawStyles from "./chip.module.css";
import { Chip, type ChipTone } from "./index";

afterEach(cleanup);

const styles = {
  neutral: requireClass(rawStyles.neutral, "chip.module.css", "neutral"),
  attention: requireClass(rawStyles.attention, "chip.module.css", "attention"),
  alive: requireClass(rawStyles.alive, "chip.module.css", "alive"),
  danger: requireClass(rawStyles.danger, "chip.module.css", "danger"),
};

test("renders its children as the visible label", () => {
  render(<Chip>backend</Chip>);
  expect(screen.getByText("backend")).toBeTruthy();
});

test("defaults to the neutral tone", () => {
  const { container } = render(<Chip>backend</Chip>);
  expect(container.firstElementChild!.classList.contains(styles.neutral)).toBe(true);
});

const TONES: ChipTone[] = ["neutral", "attention", "alive", "danger"];

for (const tone of TONES) {
  test(`tone ${tone} maps to its token family class`, () => {
    const { container } = render(<Chip tone={tone}>backend</Chip>);
    expect(container.firstElementChild!.classList.contains(styles[tone])).toBe(true);
  });
}

test("renders no remove button when onRemove is not provided", () => {
  render(<Chip>backend</Chip>);
  expect(screen.queryByRole("button")).toBeNull();
});

test("renders an accessible remove button when onRemove is provided", () => {
  render(<Chip onRemove={() => {}}>backend</Chip>);
  expect(screen.getByRole("button")).toBeTruthy();
});

test("when children is a plain string, the remove button's accessible name includes it", () => {
  render(<Chip onRemove={() => {}}>backend</Chip>);
  expect(screen.getByRole("button", { name: "Remove backend" })).toBeTruthy();
});

test("when children is not a plain string, the remove button falls back to a bare Remove", () => {
  render(
    <Chip onRemove={() => {}}>
      <span>backend</span>
    </Chip>,
  );
  expect(screen.getByRole("button", { name: "Remove" })).toBeTruthy();
});

test("clicking the remove button fires onRemove", async () => {
  const user = userEvent.setup();
  const onRemove = vi.fn();
  render(<Chip onRemove={onRemove}>backend</Chip>);
  await user.click(screen.getByRole("button"));
  expect(onRemove).toHaveBeenCalledOnce();
});

test("the remove button is keyboard-focusable and activates on Enter", async () => {
  const user = userEvent.setup();
  const onRemove = vi.fn();
  render(<Chip onRemove={onRemove}>backend</Chip>);
  await user.tab();
  expect(document.activeElement).toBe(screen.getByRole("button"));
  await user.keyboard("{Enter}");
  expect(onRemove).toHaveBeenCalledOnce();
});

test("the remove button activates on Space", async () => {
  const user = userEvent.setup();
  const onRemove = vi.fn();
  render(<Chip onRemove={onRemove}>backend</Chip>);
  screen.getByRole("button").focus();
  await user.keyboard(" ");
  expect(onRemove).toHaveBeenCalledOnce();
});

test("declares a :focus-visible rule in its CSS module, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "chip.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});
