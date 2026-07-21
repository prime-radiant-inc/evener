import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { HeadClippedOutputBody, TailFoldedOutputBody } from "./bodies";
import type { ItemModel } from "../../../../protocol/model";

afterEach(cleanup);

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

// --- HeadClippedOutputBody (grep/ls/glob's shared "cheap" body) -----------

test("HeadClippedOutputBody: shows short output verbatim", () => {
  render(<HeadClippedOutputBody item={item({ output: "hello" })} live={false} />);
  expect(screen.getByText("hello")).toBeTruthy();
});

test("HeadClippedOutputBody: head-clips at 8000 chars with an ellipsis, no elision note", () => {
  const longOutput = "y".repeat(9000);
  render(<HeadClippedOutputBody item={item({ output: longOutput })} live={false} />);
  expect(screen.getByText(`${"y".repeat(8000)}…`)).toBeTruthy();
});

test("HeadClippedOutputBody: renders nothing for blank output", () => {
  const { container } = render(<HeadClippedOutputBody item={item({ output: "" })} live={false} />);
  expect(container.textContent).toBe("");
});

test("HeadClippedOutputBody: renders nothing for undefined output", () => {
  const { container } = render(<HeadClippedOutputBody item={item({ output: undefined })} live={false} />);
  expect(container.textContent).toBe("");
});

// --- TailFoldedOutputBody (read_file/shell's shared enriched body) -------

test("TailFoldedOutputBody: shows short output verbatim whether live or settled", () => {
  render(<TailFoldedOutputBody item={item({ output: "hi" })} live={true} />);
  expect(screen.getByText("hi")).toBeTruthy();
});

test("TailFoldedOutputBody: while live, shows the raw tail with no elision note", () => {
  const longOutput = "z".repeat(9000);
  render(<TailFoldedOutputBody item={item({ output: longOutput })} live={true} />);
  expect(screen.getByText("z".repeat(8000))).toBeTruthy();
});

test("TailFoldedOutputBody: once settled, folds with an honest elision note", () => {
  const longOutput = "z".repeat(9000);
  const { container } = render(<TailFoldedOutputBody item={item({ output: longOutput })} live={false} />);
  // getByText's default normalizer collapses the embedded newline to a
  // space, so this checks raw textContent instead of using the matcher.
  expect(container.textContent).toContain(`earlier output not retained — showing the last 8,000 chars\n${"z".repeat(8000)}`);
});

test("TailFoldedOutputBody: renders nothing for blank output", () => {
  const { container } = render(<TailFoldedOutputBody item={item({ output: "" })} live={false} />);
  expect(container.textContent).toBe("");
});
