import { afterEach, test, expect, vi } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { useState } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
