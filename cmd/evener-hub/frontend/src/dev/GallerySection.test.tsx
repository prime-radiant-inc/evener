import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { GallerySection } from "./GallerySection";

test("renders the section heading and the demo children in both theme panes", () => {
  render(
    <GallerySection title="Button">
      <p>demo row</p>
    </GallerySection>,
  );

  expect(screen.getByRole("heading", { level: 2, name: "Button" })).toBeTruthy();
  expect(screen.getAllByText("demo row")).toHaveLength(2);
});

test("renders the note once, the test id on the section, and every flip block twice", () => {
  render(
    <GallerySection title="Sheet" testId="sheet-gallery" note={<p>fixture note</p>} flips={[<p>right</p>, <p>left</p>]}>
      <p>unused child</p>
    </GallerySection>,
  );

  expect(screen.getByTestId("sheet-gallery").tagName).toBe("SECTION");
  expect(screen.getAllByText("fixture note")).toHaveLength(1);
  expect(screen.getAllByText("right")).toHaveLength(2);
  expect(screen.getAllByText("left")).toHaveLength(2);
  expect(screen.queryByText("unused child")).toBeNull();
});
