// The shared automatic memory-refresh validator: the frozen wire contract both
// clients decode. Its accepted and rejected shapes are the same ones the web
// validator carried before it moved into the package, so the move preserves web
// behavior and gives native the same decoding rather than a second decoder.
import { describe, expect, it } from "vitest";
import {
  MEMORY_CONTEXT_LABEL,
  type MemoryContextObservation,
  memoryContextEmptyText,
  memoryContextScopeLabel,
  memoryContextStateLabel,
  parseMemoryContext,
} from "./memoryContext";

function raw(overrides: Record<string, unknown> = {}): unknown {
  return {
    memoryContext: {
      scope: "personal",
      state: "current",
      truncated: false,
      content: "# Personal memory\n\n- a note\n",
      ...overrides,
    },
  };
}

describe("parseMemoryContext", () => {
  it("accepts a fully typed observation", () => {
    expect(parseMemoryContext(raw())).toEqual({
      scope: "personal",
      state: "current",
      truncated: false,
      content: "# Personal memory\n\n- a note\n",
    } satisfies MemoryContextObservation);
  });

  it("ignores unknown extra fields so a future additive field does not force the fallback", () => {
    expect(parseMemoryContext(raw({ future: "x" }))).toMatchObject({ scope: "personal", state: "current" });
  });

  it.each([
    ["no raw", undefined],
    ["null raw", null],
    ["a non-object raw", "nope"],
    ["no memoryContext", {}],
    ["a non-object memoryContext", { memoryContext: 7 }],
    ["an unknown scope", raw({ scope: "team" })],
    ["an unknown state", raw({ state: "fresh" })],
    ["a non-boolean truncated", raw({ truncated: "no" })],
    ["a non-string content", raw({ content: 7 })],
    ["a missing content", { memoryContext: { scope: "personal", state: "current", truncated: false } }],
  ])("rejects %s", (_name, value) => {
    expect(parseMemoryContext(value)).toBeUndefined();
  });

  it("keeps an empty content as a real successful read", () => {
    expect(parseMemoryContext(raw({ content: "" }))).toMatchObject({ content: "" });
  });
});

describe("labels", () => {
  it("names the heading and each scope", () => {
    expect(MEMORY_CONTEXT_LABEL).toBe("Refreshed my memory");
    expect(memoryContextScopeLabel("personal")).toBe("Personal memory");
    expect(memoryContextScopeLabel("project")).toBe("Project memory");
    expect(memoryContextScopeLabel("session")).toBe("Session memory");
  });

  it("reads each state as its own word", () => {
    expect(memoryContextStateLabel("current")).toBe("current");
    expect(memoryContextStateLabel("missing")).toBe("missing");
    expect(memoryContextStateLabel("revoked")).toBe("revoked");
    expect(memoryContextStateLabel("unavailable")).toBe("unavailable");
  });

  it("distinguishes an empty index from each missing state", () => {
    expect(memoryContextEmptyText("current")).toBe("Empty index");
    expect(memoryContextEmptyText("missing")).toBe("No memory index");
    expect(memoryContextEmptyText("revoked")).toBe("Memory access revoked");
    expect(memoryContextEmptyText("unavailable")).toBe("Memory index unavailable");
  });
});
