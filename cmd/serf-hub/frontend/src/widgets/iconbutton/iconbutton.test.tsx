import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { IconButton, type IconButtonProps } from "./index";

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

  rerender(<IconButton label="Go" icon={<DotIcon />} variant="dangerQuiet" />);
  const dangerQuietClass = screen.getByRole("button").className;

  expect(new Set([primaryClass, quietClass, dangerClass, dangerQuietClass]).size).toBe(4);
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

// --- fix-wave: forwardRef + rest-prop spread (Important) ---------------
// Same gap Button had (see button.test.tsx): IconButton renders its own
// <button> and reuses Button's CSS classes, but that class-reuse is
// CSS-only - it does not inherit Button's ref-forwarding or prop-spreading,
// which live in Button's component code, not its stylesheet. Verified and
// fixed independently here.

test("forwards a ref to the underlying button element", () => {
  const ref = createRef<HTMLButtonElement>();
  render(<IconButton ref={ref} label="Go" icon={<DotIcon />} />);
  expect(ref.current).toBe(screen.getByRole("button"));
});

test("spreads unrecognized props onto the underlying button element", () => {
  render(<IconButton label="Go" icon={<DotIcon />} data-testid="my-icon-button" />);
  expect(screen.getByRole("button").getAttribute("data-testid")).toBe("my-icon-button");
});

// --- wave-close review: rest-spread merge order (Important) ------------
// Same class of gap as Button's (see button.test.tsx): IconButtonProps
// omits "aria-label" from ButtonHTMLAttributes, so a literal
// <IconButton aria-label="x"> is a type error, but a caller composing
// IconButton inside its own wrapper (spreading a props object typed more
// loosely than IconButtonProps) is not caught by TypeScript's
// excess-property check. label is IconButton's ONLY accessible name -
// letting a stray aria-label through would silently replace it. The
// prior test above only spreads a non-conflicting key (data-testid), so
// it never exercised this. Simulated via a cast through unknown, since
// the point under test is the runtime prop-merge order.
test("a caller-supplied aria-label arriving via untyped spread does not override the label prop's own accessible name", () => {
  const conflicting = { label: "Go", icon: <DotIcon />, "aria-label": "caller-injected" } as unknown as IconButtonProps;
  render(<IconButton {...conflicting} />);
  expect(screen.getByRole("button").getAttribute("aria-label")).toBe("Go");
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

// h8w2: small icon buttons must be square-ish and clickable
test("sm size declares equal width and height in CSS", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const ownCss = readFileSync(join(here, "iconbutton.module.css"), "utf8");
  // Verify that .sm class in iconbutton.module.css declares an explicit height
  // equal to its width (28px) to make it square-ish, not squat/rectangular
  expect(ownCss).toMatch(/\.sm\s*{[^}]*width:\s*28px[^}]*height:\s*28px[^}]*}/);
});
