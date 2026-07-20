import { afterEach, test, expect, vi } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Button } from "./index";

// This project doesn't wire @testing-library/react's auto-cleanup into a
// global vitest setup file (vite.config.ts's setupFiles is empty, and that
// file is out of scope for this task), so every widget test file cleans up
// its own renders explicitly: without this, a later test's queries can
// match a still-mounted element from an earlier test in the same file.
afterEach(cleanup);

test("renders its children as the visible label", () => {
  render(<Button>Save changes</Button>);
  expect(screen.getByRole("button", { name: "Save changes" })).toBeTruthy();
});

test("defaults to type=button so it never accidentally submits a form", () => {
  render(<Button>Go</Button>);
  expect(screen.getByRole("button").getAttribute("type")).toBe("button");
});

test("an explicit type overrides the button-type default", () => {
  render(<Button type="submit">Send</Button>);
  expect(screen.getByRole("button").getAttribute("type")).toBe("submit");
});

test("each variant renders a distinct class", () => {
  const { rerender } = render(<Button variant="primary">Go</Button>);
  const primaryClass = screen.getByRole("button").className;

  rerender(<Button variant="quiet">Go</Button>);
  const quietClass = screen.getByRole("button").className;

  rerender(<Button variant="danger">Go</Button>);
  const dangerClass = screen.getByRole("button").className;

  expect(new Set([primaryClass, quietClass, dangerClass]).size).toBe(3);
});

test("each size renders a distinct class", () => {
  const { rerender } = render(<Button size="sm">Go</Button>);
  const smClass = screen.getByRole("button").className;

  rerender(<Button size="md">Go</Button>);
  const mdClass = screen.getByRole("button").className;

  expect(smClass).not.toEqual(mdClass);
});

test("renders the icon before the label when provided", () => {
  render(<Button icon={<span data-testid="my-icon" />}>Go</Button>);
  const button = screen.getByRole("button");
  const icon = screen.getByTestId("my-icon");
  // the icon's wrapper is the button's first element child, i.e. it
  // precedes the "Go" label text in document order
  expect(button.firstElementChild).toBe(icon.parentElement);
  expect(button.textContent).toBe("Go");
});

test("disabled blocks onClick", async () => {
  const user = userEvent.setup();
  const onClick = vi.fn();
  render(
    <Button disabled onClick={onClick}>
      Go
    </Button>,
  );
  await user.click(screen.getByRole("button"));
  expect(onClick).not.toHaveBeenCalled();
});

test("disabled marks the native button inert (not focusable)", () => {
  render(<Button disabled>Go</Button>);
  const button = screen.getByRole("button") as HTMLButtonElement;
  expect(button.disabled).toBe(true);
  button.focus();
  expect(document.activeElement).not.toBe(button);
});

test("is keyboard-focusable when enabled", () => {
  render(<Button>Go</Button>);
  const button = screen.getByRole("button") as HTMLButtonElement;
  button.focus();
  expect(document.activeElement).toBe(button);
});

test("clicking an enabled button fires onClick", async () => {
  const user = userEvent.setup();
  const onClick = vi.fn();
  render(<Button onClick={onClick}>Go</Button>);
  await user.click(screen.getByRole("button"));
  expect(onClick).toHaveBeenCalledOnce();
});

// jsdom does not compute :focus-visible-triggered styles, so the exemplar's
// visible focus ring (a Global Constraint for every interactive widget) is
// verified by reading the CSS module's own source for the rule, the same
// way token-contract.test.ts reads stylesheets.
test("declares a :focus-visible rule in its CSS module, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "button.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});
