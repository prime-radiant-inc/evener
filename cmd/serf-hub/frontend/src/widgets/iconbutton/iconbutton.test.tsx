import { afterEach, test, expect, vi } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IconButton } from "./index";

afterEach(cleanup);

function DotIcon() {
  return (
    <svg data-testid="my-icon" viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="8" cy="8" r="5" fill="currentColor" />
    </svg>
  );
}

test("uses the label as its accessible name (aria-label), with no visible text", () => {
  render(<IconButton label="Close" icon={<DotIcon />} />);
  const button = screen.getByRole("button", { name: "Close" });
  expect(button.getAttribute("aria-label")).toBe("Close");
  expect(button.textContent).toBe("");
});

test("renders the icon", () => {
  render(<IconButton label="Close" icon={<DotIcon />} />);
  expect(screen.getByTestId("my-icon")).toBeTruthy();
});

test("each variant renders a distinct class", () => {
  const { rerender } = render(<IconButton label="Go" icon={<DotIcon />} variant="primary" />);
  const primaryClass = screen.getByRole("button").className;

  rerender(<IconButton label="Go" icon={<DotIcon />} variant="quiet" />);
  const quietClass = screen.getByRole("button").className;

  rerender(<IconButton label="Go" icon={<DotIcon />} variant="danger" />);
  const dangerClass = screen.getByRole("button").className;

  expect(new Set([primaryClass, quietClass, dangerClass]).size).toBe(3);
});

test("each size renders a distinct class", () => {
  const { rerender } = render(<IconButton label="Go" icon={<DotIcon />} size="sm" />);
  const smClass = screen.getByRole("button").className;

  rerender(<IconButton label="Go" icon={<DotIcon />} size="md" />);
  const mdClass = screen.getByRole("button").className;

  expect(smClass).not.toEqual(mdClass);
});

test("disabled blocks onClick", async () => {
  const user = userEvent.setup();
  const onClick = vi.fn();
  render(<IconButton label="Go" icon={<DotIcon />} disabled onClick={onClick} />);
  await user.click(screen.getByRole("button"));
  expect(onClick).not.toHaveBeenCalled();
});

test("disabled marks the native button inert (not focusable)", () => {
  render(<IconButton label="Go" icon={<DotIcon />} disabled />);
  const button = screen.getByRole("button") as HTMLButtonElement;
  expect(button.disabled).toBe(true);
  button.focus();
  expect(document.activeElement).not.toBe(button);
});

test("is keyboard-focusable when enabled", () => {
  render(<IconButton label="Go" icon={<DotIcon />} />);
  const button = screen.getByRole("button") as HTMLButtonElement;
  button.focus();
  expect(document.activeElement).toBe(button);
});

test("clicking an enabled button fires onClick", async () => {
  const user = userEvent.setup();
  const onClick = vi.fn();
  render(<IconButton label="Go" icon={<DotIcon />} onClick={onClick} />);
  await user.click(screen.getByRole("button"));
  expect(onClick).toHaveBeenCalledOnce();
});

test("defaults to type=button so it never accidentally submits a form", () => {
  render(<IconButton label="Go" icon={<DotIcon />} />);
  expect(screen.getByRole("button").getAttribute("type")).toBe("button");
});

// IconButton reuses Button's own base class (not a duplicate CSS rule) so
// it inherits the exact same :focus-visible ring for free - see the
// comment in index.tsx for why that's a deliberate composition choice.
test("reuses button.module.css's base class, so it inherits Button's :focus-visible ring", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const ownCss = readFileSync(join(here, "iconbutton.module.css"), "utf8");
  expect(ownCss).not.toContain(":focus-visible");

  const buttonCss = readFileSync(join(here, "../button/button.module.css"), "utf8");
  expect(buttonCss).toContain(":focus-visible");
});
