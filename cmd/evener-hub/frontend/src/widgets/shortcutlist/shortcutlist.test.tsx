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
