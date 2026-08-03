import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
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

// A compact one-line JSON result (job_status's output in the screenshot that
// motivated this) is nearly unreadable wrapped in the block. When the output
// parses as JSON, the fallback pretty-prints it - same "display preparation"
// the shell row's pretty-printed command already gets.
test("JSON output is rendered pretty-printed, not as one compact line", () => {
  const compact = '{"kind":"agent","status":"running","phase":"model_streaming"}';
  const { container } = render(<RawToolOutput item={item({ output: compact })} live={false} />);
  const code = container.querySelector("pre > code");
  expect(code?.textContent).toBe(JSON.stringify(JSON.parse(compact), null, 2));
});

test("output that is not valid JSON is left byte-for-byte unchanged", () => {
  const { container } = render(<RawToolOutput item={item({ output: '{"broken": ' })} live={false} />);
  expect(container.querySelector("pre > code")?.textContent).toBe('{"broken": ');
});

test("copying a pretty-printed JSON block writes the original raw output", async () => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText");
  const compact = '{"a":1}';
  render(<RawToolOutput item={item({ output: compact })} live={false} />);
  await user.click(screen.getByRole("button", { name: "Copy output" }));
  expect(writeText).toHaveBeenCalledExactlyOnceWith(compact);
});
