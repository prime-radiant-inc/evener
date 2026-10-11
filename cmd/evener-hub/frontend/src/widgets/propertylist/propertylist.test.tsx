import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { PropertyList } from "./index";

test("each row pairs its label term with its value definition, in order", () => {
  render(
    <PropertyList
      rows={[
        { label: "Version", value: "v1.2.0", mono: true },
        { label: "Marketplace", value: "evener-official" },
      ]}
    />,
  );
  expect(screen.getAllByRole("term").map((term) => term.textContent)).toEqual(["Version", "Marketplace"]);
  expect(screen.getAllByRole("definition").map((definition) => definition.textContent)).toEqual([
    "v1.2.0",
    "evener-official",
  ]);
});

test("a value can be rich content", () => {
  render(
    <PropertyList
      rows={[
        {
          label: "Install location",
          value: (
            <>
              <code>~/.evener/plugins</code> not fetched yet
            </>
          ),
        },
      ]}
    />,
  );
  expect(screen.getByRole("definition").textContent).toBe("~/.evener/plugins not fetched yet");
});

test("an empty row list renders no terms", () => {
  render(<PropertyList rows={[]} />);
  expect(screen.queryByRole("term")).toBeNull();
});
