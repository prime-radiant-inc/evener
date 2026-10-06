import type { ItemModel, TurnModel } from "@evener/appwire-client";
import {
  memoryDeleteSummary,
  memoryEditSummary,
  memoryReadSummary,
  memorySearchSummary,
  memoryWriteSummary,
} from "@evener/appwire-client";
import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { ToolCallItem } from "../ToolCallItem";
import { toolRendererFor } from "../toolRenderers";
import { HeadClippedOutputBody, TailFoldedOutputBody } from "./bodies";
import "./index";

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

const turn: TurnModel = { id: "turn_1", status: "inProgress", items: [] };

const descriptors = [
  {
    name: "memory_write",
    summary: memoryWriteSummary,
    args: { scope: "personal", file_path: "implementation-delegation.md" },
    expected: "Wrote memory personal/implementation-delegation.md",
    fold: "consequential",
  },
  {
    name: "memory_edit",
    summary: memoryEditSummary,
    args: { scope: "project", file_path: "MEMORY.md", old_string: "one\ntwo", new_string: "uno" },
    expected: "Edited memory project/MEMORY.md · +1 -2",
    fold: "consequential",
  },
  {
    name: "memory_read",
    summary: memoryReadSummary,
    args: { scope: "session", file_path: "notes.md", offset: 1, limit: 40 },
    output: "line 1\nline 2",
    expected: "Read memory session/notes.md · lines 1-40",
    fold: "quiet",
  },
  {
    name: "memory_search",
    summary: memorySearchSummary,
    args: { scope: "personal", pattern: "pattern", path: "guides" },
    output: "one\ntwo\nthree\n",
    expected: 'Searched memory for "pattern" in personal/guides · 3 hits',
    fold: "quiet",
  },
  {
    name: "memory_delete",
    summary: memoryDeleteSummary,
    args: { scope: "project", file_path: "old.md" },
    expected: "Removed memory project/old.md",
    fold: "consequential",
  },
] as const;

test.each(descriptors)(
  "$name is registered through the production barrel with shared words and memory styling",
  (expected) => {
    const descriptor = toolRendererFor(expected.name);
    const step = item({
      toolName: expected.name,
      argumentsJSON: JSON.stringify(expected.args),
      ...("output" in expected ? { output: expected.output } : {}),
    });

    expect(descriptor.match).toBe(expected.name);
    expect(descriptor.summary).toBe(expected.summary);
    expect(descriptor.summary(step)).toBe(expected.expected);
    expect(descriptor.icon).toBe("memory");
    expect(descriptor.fold).toBe(expected.fold);
    expect(descriptor.openBesidePath).toBeUndefined();
    expect(descriptor.openBesideInline).toBeUndefined();
  },
);

test("memory_read uses the tail-folded output body and memory_search uses the head-clipped output body", () => {
  expect(toolRendererFor("memory_read").body).toBe(TailFoldedOutputBody);
  expect(toolRendererFor("memory_search").body).toBe(HeadClippedOutputBody);
});

test("memory_write renders content as Markdown", () => {
  const Body = toolRendererFor("memory_write").body!;
  render(
    <Body
      item={item({
        toolName: "memory_write",
        argumentsJSON: JSON.stringify({ content: "# Memory page\n\n**saved prose**" }),
      })}
      live={false}
    />,
  );

  expect(screen.getByRole("heading", { name: "Memory page" })).toBeTruthy();
  expect(screen.getByText("saved prose").tagName).toBe("STRONG");
});

test("memory_write content passes through at the 8,000-character boundary and clips with the established marker above it", () => {
  const Body = toolRendererFor("memory_write").body!;
  const atBudget = "x".repeat(8_000);
  const overBudget = "y".repeat(8_001);

  const exact = render(
    <Body
      item={item({ toolName: "memory_write", argumentsJSON: JSON.stringify({ content: atBudget }) })}
      live={false}
    />,
  );
  expect(exact.container.textContent?.replace(/\n$/, "")).toBe(atBudget);
  exact.unmount();

  const clipped = render(
    <Body
      item={item({ toolName: "memory_write", argumentsJSON: JSON.stringify({ content: overBudget }) })}
      live={false}
    />,
  );
  expect(clipped.container.textContent?.replace(/\n$/, "")).toBe(`${"y".repeat(8_000)}…`);
});

test("memory_write falls back to the tool output when args have no content", () => {
  const Body = toolRendererFor("memory_write").body!;
  render(
    <Body
      item={item({
        toolName: "memory_write",
        argumentsJSON: JSON.stringify({ scope: "personal", file_path: "page.md" }),
        output: "Saved page text",
      })}
      live={false}
    />,
  );

  expect(screen.getByText("Saved page text")).toBeTruthy();
});

test("memory_edit renders a diff from the old and new strings", () => {
  const Body = toolRendererFor("memory_edit").body!;
  render(
    <Body
      item={item({
        toolName: "memory_edit",
        argumentsJSON: JSON.stringify({ path: "MEMORY.md", old_string: "before", new_string: "after" }),
      })}
      live={false}
    />,
  );

  expect(screen.getByText("before")).toBeTruthy();
  expect(screen.getByText("after")).toBeTruthy();
});

test("memory_delete renders its output text", () => {
  const Body = toolRendererFor("memory_delete").body!;
  render(<Body item={item({ toolName: "memory_delete", output: "Removed memory personal/page.md" })} live={false} />);

  expect(screen.getByText("Removed memory personal/page.md")).toBeTruthy();
});

test("a rejected session-memory write remains a visible generic failed row with its error", () => {
  const descriptor = toolRendererFor("memory_write");
  const rejected = item({
    id: "rejected-session-memory-write",
    toolName: "memory_write",
    argumentsJSON: JSON.stringify({ scope: "session", file_path: "notes.md", content: "not allowed" }),
    status: "failed",
    error: "session memory belongs to the root session; report this to your parent instead",
  });

  expect(descriptor.suppress?.(rejected) ?? false).toBe(false);
  render(<ToolCallItem item={rejected} turn={turn} live={false} />);

  const row = screen.getByTestId("tool-call-item");
  expect(row.dataset.failed).toBe("true");
  expect(row.dataset.attention).toBe("error");
  expect(
    screen.getByText("session memory belongs to the root session; report this to your parent instead"),
  ).toBeTruthy();
});
