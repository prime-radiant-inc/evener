import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { Switch } from "./index";

afterEach(cleanup);

function ControlledSwitch(props: { onChange?: (checked: boolean) => void; disabled?: boolean }) {
  const [checked, setChecked] = useState(false);
  return (
    <Switch
      label="Notifications"
      checked={checked}
      disabled={props.disabled}
      onChange={(next) => {
        setChecked(next);
        props.onChange?.(next);
      }}
    />
  );
}

test("has role=switch", () => {
  render(<Switch label="Notifications" checked={false} onChange={() => {}} />);
  expect(screen.getByRole("switch")).toBeTruthy();
});

test("aria-checked reflects checked=false", () => {
  render(<Switch label="Notifications" checked={false} onChange={() => {}} />);
  expect(screen.getByRole("switch").getAttribute("aria-checked")).toBe("false");
});

test("aria-checked reflects checked=true", () => {
  render(<Switch label="Notifications" checked={true} onChange={() => {}} />);
  expect(screen.getByRole("switch").getAttribute("aria-checked")).toBe("true");
});

test("the visible label is the switch's accessible name", () => {
  render(<Switch label="Notifications" checked={false} onChange={() => {}} />);
  expect(screen.getByRole("switch", { name: "Notifications" })).toBeTruthy();
  expect(screen.getByText("Notifications")).toBeTruthy();
});

test("clicking toggles from false to true", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ControlledSwitch onChange={onChange} />);
  await user.click(screen.getByRole("switch"));
  expect(onChange).toHaveBeenCalledWith(true);
  expect(screen.getByRole("switch").getAttribute("aria-checked")).toBe("true");
});

test("clicking again toggles back from true to false", async () => {
  const user = userEvent.setup();
  render(<ControlledSwitch />);
  const toggle = screen.getByRole("switch");
  await user.click(toggle);
  await user.click(toggle);
  expect(toggle.getAttribute("aria-checked")).toBe("false");
});

test("Space toggles the switch", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ControlledSwitch onChange={onChange} />);
  screen.getByRole("switch").focus();
  await user.keyboard(" ");
  expect(onChange).toHaveBeenCalledWith(true);
});

test("Enter toggles the switch", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ControlledSwitch onChange={onChange} />);
  screen.getByRole("switch").focus();
  await user.keyboard("{Enter}");
  expect(onChange).toHaveBeenCalledWith(true);
});

test("disabled blocks toggling", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ControlledSwitch onChange={onChange} disabled />);
  await user.click(screen.getByRole("switch"));
  expect(onChange).not.toHaveBeenCalled();
});

test("clicking the visible label also toggles the switch", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ControlledSwitch onChange={onChange} />);
  await user.click(screen.getByText("Notifications"));
  expect(onChange).toHaveBeenCalledWith(true);
  expect(screen.getByRole("switch").getAttribute("aria-checked")).toBe("true");
});

test("disabled blocks toggling via the visible label too", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ControlledSwitch onChange={onChange} disabled />);
  await user.click(screen.getByText("Notifications"));
  expect(onChange).not.toHaveBeenCalled();
});

test("disabled marks the native control inert (not focusable)", () => {
  render(<Switch label="Notifications" checked={false} onChange={() => {}} disabled />);
  const toggle = screen.getByRole("switch") as HTMLButtonElement;
  expect(toggle.disabled).toBe(true);
  toggle.focus();
  expect(document.activeElement).not.toBe(toggle);
});

test("is keyboard-focusable when enabled", () => {
  render(<Switch label="Notifications" checked={false} onChange={() => {}} />);
  const toggle = screen.getByRole("switch") as HTMLButtonElement;
  toggle.focus();
  expect(document.activeElement).toBe(toggle);
});

test("declares a :focus-visible rule in its CSS module, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "switch.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});

// Beautiful UI treatment (docs/superpowers/specs/2026-08-13-webui-
// beautiful-ui-retheme-design.md §6): the unchecked track sinks into
// --field instead of the generic --surface-2 raised-layer fill; the
// checked fill stays --accent (unchanged, so only the unchecked side is
// asserted here).
test("the unchecked track background is --field", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "switch.module.css"), "utf8");
  const rule = /\.track\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(rule).toContain("background: var(--field)");
});

test("the clickable label gets the coarse-pointer touch floor", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "switch.module.css"), "utf8");
  const coarsePointer = /@media \(pointer: coarse\) \{([\s\S]*?)\n\}/.exec(css)?.[1] ?? "";
  expect(coarsePointer).toContain(".label");
  expect(coarsePointer).toContain("min-height: var(--tap-min)");
});

test("keeps the native mobile button at the touch floor without enlarging the painted track", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "switch.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  expect(css).toMatch(/@media \(max-width: 899px\)[\s\S]*?\.control\s*\{[^}]*min-height:\s*var\(--tap-min\)/);
  expect(css).toMatch(/\.control\s*\{[^}]*width:\s*32px[^}]*height:\s*18px/);
  expect(css).toMatch(
    /@media \(max-width: 899px\)[\s\S]*?\.control\s*\{[^}]*width:\s*var\(--tap-min\)[^}]*min-width:\s*var\(--tap-min\)[^}]*height:\s*var\(--tap-min\)[^}]*min-height:\s*var\(--tap-min\)/,
  );
  expect(css).toMatch(/\.control:focus-visible\s*\{/);
  expect(css).toMatch(/\.track\s*\{[^}]*width:\s*32px[^}]*height:\s*18px/);
});
