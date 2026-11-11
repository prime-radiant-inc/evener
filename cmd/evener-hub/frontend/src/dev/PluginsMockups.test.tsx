import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test } from "vitest";
import PluginsMockups from "./PluginsMockups";

test("the mockup browse segment exercises shipped split and whole catalog rows", async () => {
  const user = userEvent.setup();
  const { container } = render(<PluginsMockups />);
  await user.click(screen.getByRole("radio", { name: "Browse" }));
  const rows = container.querySelectorAll('[data-kind="plugin-row"][data-mode="catalog"]');
  expect(rows.length).toBeGreaterThan(1);
  let split = 0;
  let whole = 0;
  for (const row of rows) {
    if (!(row instanceof HTMLElement)) throw new Error("catalog row is not an element");
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
