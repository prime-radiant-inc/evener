import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, within } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import SegmentedControlGallerySection from "./segmentedcontrol";

afterEach(cleanup);

function galleryCssSource() {
  const here = dirname(fileURLToPath(import.meta.url));
  return readFileSync(join(here, "segmentedcontrol.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

test("shows every documented SegmentedControl state in both theme panes", () => {
  render(<SegmentedControlGallerySection />);
  const gallery = document.querySelector('[data-testid="segmentedcontrol-gallery"]');
  expect(gallery).toBeTruthy();
  const panes = gallery?.querySelectorAll('[data-theme="dark"], [data-theme="light"]');
  expect(panes).toHaveLength(2);
  for (const pane of panes ?? []) {
    const scoped = within(pane as HTMLElement);
    const selected = (groupName: string, optionName: string) => {
      const group = scoped.getByRole("radiogroup", { name: groupName });
      const option = within(group).getByRole("radio", { name: optionName });
      expect(option.getAttribute("aria-checked")).toBe("true");
      return { group, option };
    };

    const intrinsic = selected("Interactive detail", "Tools");
    expect(intrinsic.group.className).not.toContain("fullWidth");
    expect(intrinsic.group.querySelectorAll('[role="radio"]')).toHaveLength(3);

    const sixOption = selected("Six-option transcript detail", "Tools");
    expect(sixOption.group.className).toContain("fullWidth");
    expect(sixOption.group.querySelectorAll('[role="radio"]')).toHaveLength(6);
    const fullWidthDemo = pane.querySelector('[data-testid="segmentedcontrol-full-width-demo"]');
    expect(fullWidthDemo).toBeTruthy();
    expect(fullWidthDemo?.contains(sixOption.group)).toBe(true);

    const small = selected("Small detail", "Intent");
    expect(within(small.group).getByRole("radio", { name: "Intent" }).className).toContain("sm");

    selected("First selected", "Chat");
    selected("Middle selected", "Tools");
    selected("Last selected", "Custom");
    selected("Custom selected", "Custom");
    selected("Full selected", "Full detail");

    const disabled = selected("Disabled option", "Chat");
    expect(within(disabled.group).getByRole("radio", { name: "Intent" }).hasAttribute("disabled")).toBe(true);

    const selectedDisabled = selected("Selected disabled option", "Tools");
    expect(selectedDisabled.option.hasAttribute("disabled")).toBe(true);

    const disabledGroup = selected("Disabled group", "Intent");
    expect(disabledGroup.group.getAttribute("aria-disabled")).toBe("true");
    expect(
      [...disabledGroup.group.querySelectorAll('[role="radio"]')].every((radio) => radio.hasAttribute("disabled")),
    ).toBe(true);

    const keyboard = selected("Keyboard focus", "Intent");
    expect(keyboard.option.getAttribute("tabindex")).toBe("0");

    const frame320 = pane.querySelector('[data-testid="segmentedcontrol-frame-320"]');
    const frame390 = pane.querySelector('[data-testid="segmentedcontrol-frame-390"]');
    expect(frame320).toBeTruthy();
    expect(frame390).toBeTruthy();
    for (const frame of [frame320, frame390]) {
      const frameControl = within(frame as HTMLElement).getByRole("radiogroup");
      expect(frameControl.className).toContain("fullWidth");
      expect(within(frameControl).getByRole("radio", { name: "Tools" }).getAttribute("aria-checked")).toBe("true");
    }
  }
  const active = document.activeElement;
  expect(active?.getAttribute("role")).toBe("radio");
  expect(active?.getAttribute("aria-label")).toBe("Intent");
  expect(active?.getAttribute("aria-checked")).toBe("true");
  const activeGroup = active?.closest('[role="radiogroup"]');
  expect(activeGroup?.getAttribute("aria-labelledby")).toBeTruthy();
  expect(activeGroup && document.getElementById(activeGroup.getAttribute("aria-labelledby") ?? "")?.textContent).toBe(
    "Keyboard focus",
  );
  const css = galleryCssSource();
  expect(css).toMatch(/\.fullWidthDemo\s*\{[^}]*inline-size:\s*min\(42rem,\s*100%\)[^}]*max-inline-size:\s*100%/s);
});
