import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { ShortcutList } from "./index";

test("each item pairs its chord term with the action it performs, in order", () => {
  render(
    <ShortcutList
      items={[
        { keys: ["Shift", "Enter"], label: "jump to a turn" },
        { keys: ["Esc"], label: "close the palette" },
      ]}
    />,
  );
  expect(screen.getAllByRole("term").map((term) => term.textContent)).toEqual(["Shift+Enter", "Esc"]);
  expect(screen.getAllByRole("definition").map((definition) => definition.textContent)).toEqual([
    "jump to a turn",
    "close the palette",
  ]);
});

test("a chord renders as one keyboard key per press", () => {
  render(<ShortcutList items={[{ keys: ["Shift", "Enter"], label: "jump to a turn" }]} />);
  const term = screen.getByRole("term");
  expect([...term.querySelectorAll("kbd")].map((key) => key.textContent)).toEqual(["Shift", "Enter"]);
});

test("the same chord may appear twice with different meanings", () => {
  render(
    <ShortcutList
      items={[
        { keys: ["/"], label: "in the palette, filters commands" },
        { keys: ["/"], label: "in an empty composer, opens the slash menu" },
      ]}
    />,
  );
  expect(screen.getAllByRole("term")).toHaveLength(2);
  expect(screen.getAllByRole("definition").map((definition) => definition.textContent)).toEqual([
    "in the palette, filters commands",
    "in an empty composer, opens the slash menu",
  ]);
});

test("the inline layout keeps the same terms and definitions as the table", () => {
  const items = [
    { keys: ["Mod", "K"], label: "open the palette" },
    { keys: ["Esc"], label: "close it" },
  ];
  const { rerender } = render(<ShortcutList items={items} />);
  const table = screen.getAllByRole("term").map((term) => term.querySelectorAll("kbd").length);
  const tableList = screen.getAllByRole("term")[0]?.closest("dl");
  expect(tableList?.getAttribute("data-layout")).toBe("table");
  const tableClass = tableList?.className;
  rerender(<ShortcutList items={items} layout="inline" />);
  expect(screen.getAllByRole("term").map((term) => term.querySelectorAll("kbd").length)).toEqual(table);
  expect(screen.getAllByRole("definition")).toHaveLength(items.length);
  const inlineList = screen.getAllByRole("term")[0]?.closest("dl");
  expect(inlineList?.getAttribute("data-layout")).toBe("inline");
  // The two layouts are different boxes, not one restyled by its caller.
  expect(inlineList?.className).not.toBe(tableClass);
});
