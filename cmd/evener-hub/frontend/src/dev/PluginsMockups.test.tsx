import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test } from "vitest";
import PluginsMockups from "./PluginsMockups";

function catalogRows(container: HTMLElement): HTMLElement[] {
  return [...container.querySelectorAll('[data-kind="plugin-row"][data-mode="catalog"]')].filter(
    (row): row is HTMLElement => row instanceof HTMLElement,
  );
}

test("the mockup browse segment exercises shipped split and whole catalog rows", async () => {
  const user = userEvent.setup();
  const { container } = render(<PluginsMockups />);
  await user.click(screen.getByRole("radio", { name: "Browse" }));
  const rows = catalogRows(container);
  expect(rows.length).toBeGreaterThan(1);
  let split = 0;
  let whole = 0;
  for (const row of rows) {
    const buttons = row.tagName === "BUTTON" ? [row] : within(row).getAllByRole("button");
    if (row.tagName === "BUTTON") {
      whole++;
      expect(buttons).toHaveLength(1);
    } else {
      split++;
      expect(buttons).toHaveLength(2);
      for (const button of buttons) expect(button.parentElement?.closest("button")).toBeNull();
    }
  }
  expect(split).toBeGreaterThan(0);
  expect(whole).toBeGreaterThan(0);
});

test("the mockup's split catalog row operates its Install control without collapsing the pair", async () => {
  const user = userEvent.setup();
  const { container } = render(<PluginsMockups />);
  await user.click(screen.getByRole("radio", { name: "Browse" }));
  const row = catalogRows(container).find((candidate) => candidate.tagName !== "BUTTON");
  const install = row === undefined ? null : within(row).queryByRole("button", { name: "Install" });
  if (row === undefined || install === null) throw new Error("no split catalog row with an Install control");
  const wrapper = row.parentElement;
  expect(wrapper?.getAttribute("data-install-clicks")).toBe("0");
  await user.click(install);
  // The click reached the shipped control's handler rather than the row's
  // select, the control owns its click (no nesting), and the row is still the
  // same two-button split after the interaction.
  expect(wrapper?.getAttribute("data-install-clicks")).toBe("1");
  expect(install.parentElement?.closest("button")).toBeNull();
  expect(within(row).getAllByRole("button")).toHaveLength(2);
  expect(row.isConnected).toBe(true);
});
