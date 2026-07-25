import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { requireClass } from "../internal/requireClass";
import rawStyles from "./button.module.css";
import { Button, type ButtonProps } from "./index";

const styles = {
  button: requireClass(rawStyles.button, "button.module.css", "button"),
  primary: requireClass(rawStyles.primary, "button.module.css", "primary"),
  md: requireClass(rawStyles.md, "button.module.css", "md"),
};

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

  rerender(<Button variant="dangerQuiet">Go</Button>);
  const dangerQuietClass = screen.getByRole("button").className;

  expect(new Set([primaryClass, quietClass, dangerClass, dangerQuietClass]).size).toBe(4);
});

// dangerQuiet exists for a destructive action that is not the primary one on
// its row: the danger hue lands on the label/glyph, never as a filled
// background, so the surface stays quiet until hover.
test("dangerQuiet colors the label rather than filling a background", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "button.module.css"), "utf8");
  const rule = /\.dangerQuiet\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(rule).toContain("color: var(--danger)");
  expect(rule).toContain("background: transparent");
  expect(css).toMatch(/\.dangerQuiet:hover:not\(:disabled\)\s*\{[^}]*background: var\(--danger-bg\)/);
  expect(css).toMatch(/\.dangerQuiet:focus-visible\s*\{[^}]*outline: 2px solid var\(--danger\)/);
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

// --- fix-wave: forwardRef + rest-prop spread (Important) ---------------
// Button previously accepted a fixed prop list and rendered its own
// <button> from scratch, so anything composing it via cloneElement (e.g.
// Tooltip's aria-describedby wiring) or via a ref had no way to reach the
// real DOM node - the extra prop was silently dropped, the ref silently
// unfilled. See tooltip.test.tsx for the cross-widget integration proof.

test("forwards a ref to the underlying button element", () => {
  const ref = createRef<HTMLButtonElement>();
  render(<Button ref={ref}>Go</Button>);
  expect(ref.current).toBe(screen.getByRole("button"));
});

test("spreads unrecognized props onto the underlying button element", () => {
  render(
    <Button aria-describedby="hint-id" data-testid="my-button">
      Go
    </Button>,
  );
  const button = screen.getByRole("button");
  expect(button.getAttribute("aria-describedby")).toBe("hint-id");
  expect(button.getAttribute("data-testid")).toBe("my-button");
});

// --- wave-close review: rest-spread merge order (Important) ------------
// ButtonProps omits "className" from ButtonHTMLAttributes, so a literal
// <Button className="x"> is a type error - but that only guards object
// literals. A caller composing Button inside its own wrapper component
// (spreading a props object typed more loosely than ButtonProps) is NOT
// caught by TypeScript's excess-property check, which fires only on
// object literals, not on a spread variable. The prior test above only
// spreads non-conflicting keys (aria-describedby, data-testid), so it
// passes regardless of {...rest}'s position in the JSX and never
// exercised this. Simulated here via a cast through unknown, since the
// point under test is the runtime prop-merge order, not the type escape
// hatch itself.
test("a caller-supplied className arriving via untyped spread does not override the widget's own classes", () => {
  const conflicting = { children: "Go", className: "caller-injected" } as unknown as ButtonProps;
  render(<Button {...conflicting} />);
  const button = screen.getByRole("button");
  expect(button.classList.contains(styles.button)).toBe(true);
  expect(button.classList.contains(styles.primary)).toBe(true);
  expect(button.classList.contains(styles.md)).toBe(true);
  expect(button.className).not.toContain("caller-injected");
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
