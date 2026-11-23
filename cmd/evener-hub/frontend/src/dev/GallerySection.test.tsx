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
