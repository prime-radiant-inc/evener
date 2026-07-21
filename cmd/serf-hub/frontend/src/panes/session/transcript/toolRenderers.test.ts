import { test, expect } from "vitest";
import { registerToolRenderer, toolRendererFor, type ToolRenderProps } from "./toolRenderers";
import { RawToolOutput } from "./RawToolOutput";
import type { ItemModel } from "../../../protocol/model";

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

function DummyBody(_: ToolRenderProps) {
  return null;
}

test("an unregistered tool name falls back to the default descriptor (raw output body)", () => {
  const d = toolRendererFor("tt-unregistered-tool");
  expect(d.body).toBe(RawToolOutput);
});

test("the default descriptor's summary is the tool name", () => {
  const d = toolRendererFor("tt-unregistered-tool-2");
  expect(d.summary(item({ toolName: "tt-unregistered-tool-2" }))).toBe("tt-unregistered-tool-2");
});

test("the default descriptor's summary falls back to a literal label when toolName is (unusually) absent", () => {
  const d = toolRendererFor("tt-unregistered-tool-3");
  expect(d.summary(item({ toolName: undefined }))).toBe("tool");
});

test("registerToolRenderer with an exact string match resolves for that tool name", () => {
  registerToolRenderer({ match: "tt_exact_a", summary: () => "exact a", body: DummyBody });
  expect(toolRendererFor("tt_exact_a").summary(item())).toBe("exact a");
});

test("an exact match does not leak to a different, unregistered tool name", () => {
  registerToolRenderer({ match: "tt_exact_b", summary: () => "exact b" });
  expect(toolRendererFor("tt_exact_b_other").summary(item())).not.toBe("exact b");
});

test("registerToolRenderer with a predicate match resolves for any matching tool name (job_* family)", () => {
  registerToolRenderer({ match: (name) => name.startsWith("tt_job_"), summary: (i) => `job:${i.toolName}` });
  expect(toolRendererFor("tt_job_read_output").summary(item({ toolName: "tt_job_read_output" }))).toBe(
    "job:tt_job_read_output",
  );
  expect(toolRendererFor("tt_job_list").summary(item({ toolName: "tt_job_list" }))).toBe("job:tt_job_list");
});

test("a predicate that does not match falls through to the default descriptor", () => {
  registerToolRenderer({ match: (name) => name.startsWith("tt_neverseen_"), summary: () => "should not resolve" });
  expect(toolRendererFor("tt_completely_different").body).toBe(RawToolOutput);
});

test("an exact match wins over a predicate that would also match, registered exact-first", () => {
  registerToolRenderer({ match: "tt_precedence_a", summary: () => "exact wins" });
  registerToolRenderer({ match: (name) => name.startsWith("tt_precedence_"), summary: () => "predicate" });
  expect(toolRendererFor("tt_precedence_a").summary(item())).toBe("exact wins");
});

test("an exact match wins over a predicate that would also match, registered predicate-first", () => {
  registerToolRenderer({ match: (name) => name.startsWith("tt_precedence2_"), summary: () => "predicate" });
  registerToolRenderer({ match: "tt_precedence2_a", summary: () => "exact wins" });
  expect(toolRendererFor("tt_precedence2_a").summary(item())).toBe("exact wins");
});

test("autoExpand is optional - a descriptor without one has it undefined, not a crashing default", () => {
  registerToolRenderer({ match: "tt_no_autoexpand", summary: () => "s" });
  expect(toolRendererFor("tt_no_autoexpand").autoExpand).toBeUndefined();
});

test("autoExpand, when provided, is reachable off the resolved descriptor", () => {
  registerToolRenderer({ match: "tt_autoexpand", summary: () => "s", autoExpand: (i) => i.status === "failed" });
  const d = toolRendererFor("tt_autoexpand");
  expect(d.autoExpand?.(item({ status: "failed" }))).toBe(true);
  expect(d.autoExpand?.(item({ status: "completed" }))).toBe(false);
});

test("body is optional on a registered descriptor (no expand affordance) - only the default guarantees one", () => {
  registerToolRenderer({ match: "tt_no_body", summary: () => "s" });
  expect(toolRendererFor("tt_no_body").body).toBeUndefined();
});
