import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel } from "../../../protocol/model";
import { RawToolOutput } from "./RawToolOutput";

afterEach(cleanup);

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

test("renders the tool's raw output text", () => {
  render(<RawToolOutput item={item({ output: "raw bytes here" })} live={false} />);
  expect(screen.getByText("raw bytes here")).toBeTruthy();
});

test("renders nothing at all for empty output - no empty box", () => {
  const { container } = render(<RawToolOutput item={item({ output: "" })} live={false} />);
  expect(container.textContent).toBe("");
});

test("renders nothing for absent output", () => {
  const { container } = render(<RawToolOutput item={item({ output: undefined })} live={false} />);
  expect(container.textContent).toBe("");
});

// A4: the fallback body is the SAME block shape every descriptor body uses, so
// it gets the wrapping/weight/inset-copy treatment for free rather than owning
// a second, differently-styled <pre> of its own.
test("renders through CodeBlock, so it carries the same inset copy control", () => {
  render(<RawToolOutput item={item({ output: "x" })} live={false} />);
  const copy = screen.getByRole("button", { name: "Copy output" });
  expect(copy.textContent).toBe(""); // icon-only
});

test("the output sits in a pre/code pairing (CodeBlock's own shape)", () => {
  const { container } = render(<RawToolOutput item={item({ output: "x" })} live={false} />);
  expect(container.querySelector("pre > code")).toBeTruthy();
});
