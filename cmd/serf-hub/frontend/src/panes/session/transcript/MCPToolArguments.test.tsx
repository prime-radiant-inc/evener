import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel } from "../../../protocol/model";
import { MCPToolArguments } from "./MCPToolArguments";

afterEach(cleanup);

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

test("renders nothing for absent arguments", () => {
  const { container } = render(<MCPToolArguments item={item({ argumentsJSON: undefined })} live={false} />);
  expect(container.textContent).toBe("");
});

test("renders nothing for whitespace-only arguments", () => {
  const { container } = render(<MCPToolArguments item={item({ argumentsJSON: "   \n \t" })} live={false} />);
  expect(container.textContent).toBe("");
});

test("pretty-prints valid JSON arguments while preserving the raw copy text", () => {
  const raw = '{"width":375,"options":{"mobile":true}}';
  render(<MCPToolArguments item={item({ argumentsJSON: raw })} live={false} />);
  const section = screen.getByRole("region", { name: "Tool call arguments" });
  expect(section).toBeTruthy();
  expect(section.querySelector("pre > code")?.textContent).toBe(JSON.stringify(JSON.parse(raw), null, 2));
});

test("leaves malformed JSON arguments exactly as received", () => {
  const raw = '{"broken": ';
  const { container } = render(<MCPToolArguments item={item({ argumentsJSON: raw })} live={false} />);
  expect(container.querySelector("pre > code")?.textContent).toBe(raw);
});
