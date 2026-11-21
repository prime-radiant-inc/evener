import type { ItemModel, TurnModel } from "@evener/appwire-client";
import {
  memoryDeleteSummary,
  memoryEditSummary,
  memoryReadSummary,
  memorySearchSummary,
  memoryWriteSummary,
} from "@evener/appwire-client";
import { render, screen, within } from "@testing-library/react";
import { expect, test } from "vitest";
import rawDiffStyles from "../../../../widgets/diffblock/diffblock.module.css";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { ToolCallItem } from "../ToolCallItem";
import { toolRendererFor } from "../toolRenderers";
import { HeadClippedOutputBody, TailFoldedOutputBody } from "./bodies";
import "./index";

const diffStyles = {
  line: requireClass(rawDiffStyles.line, "diffblock.module.css", "line"),
  content: requireClass(rawDiffStyles.content, "diffblock.module.css", "content"),
  header: requireClass(rawDiffStyles.header, "diffblock.module.css", "header"),
  add: requireClass(rawDiffStyles.add, "diffblock.module.css", "add"),
  del: requireClass(rawDiffStyles.del, "diffblock.module.css", "del"),
};

function diffLines(container: HTMLElement): Element[] {
  return Array.from(container.querySelectorAll(`.${diffStyles.line}`));
}

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

test("memory_write renders its body as Markdown", () => {
  const Body = toolRendererFor("memory_write").body!;
  render(
    <Body
      item={item({
        toolName: "memory_write",
        argumentsJSON: JSON.stringify({ body: "# Memory page\n\n**saved prose**" }),
      })}
      live={false}
    />,
  );

  expect(screen.getByRole("heading", { name: "Memory page" })).toBeTruthy();
  expect(screen.getByText("saved prose").tagName).toBe("STRONG");
});

test("memory_write shows its when line above the body", () => {
  const Body = toolRendererFor("memory_write").body!;
  render(
    <Body
      item={item({
        toolName: "memory_write",
        argumentsJSON: JSON.stringify({ when: "When pricing a cart", body: "# Cart pricing\n\nfact" }),
      })}
      live={false}
    />,
  );

  expect(screen.getByText("When:").tagName).toBe("STRONG");
  expect(screen.getByText(/When pricing a cart/)).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Cart pricing" })).toBeTruthy();
});

test("memory_write body passes through at the 8,000-character boundary and clips with the established marker above it", () => {
  const Body = toolRendererFor("memory_write").body!;
  const atBudget = "x".repeat(8_000);
  const overBudget = "y".repeat(8_001);

  const exact = render(
    <Body item={item({ toolName: "memory_write", argumentsJSON: JSON.stringify({ body: atBudget }) })} live={false} />,
  );
  expect(exact.container.textContent?.replace(/\n$/, "")).toBe(atBudget);
  exact.unmount();

  const clipped = render(
    <Body
      item={item({ toolName: "memory_write", argumentsJSON: JSON.stringify({ body: overBudget }) })}
      live={false}
    />,
  );
  expect(clipped.container.textContent?.replace(/\n$/, "")).toBe(`${"y".repeat(8_000)}…`);
});

test("memory_write falls back to the tool output when args have no body", () => {
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

test("memory_write with an empty body renders the tool's output, not an empty page", () => {
  const Body = toolRendererFor("memory_write").body!;
  render(
    <Body
      item={item({
        toolName: "memory_write",
        argumentsJSON: JSON.stringify({ scope: "personal", file_path: "page.md", content: "" }),
        output: "wrote 0 bytes to page.md",
      })}
      live={false}
    />,
  );

  expect(screen.getByText("wrote 0 bytes to page.md")).toBeTruthy();
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

test("a memory_edit with neither old nor new string renders the arguments and result, not an empty diff", () => {
  const Body = toolRendererFor("memory_edit").body!;
  render(
    <Body
      item={item({
        toolName: "memory_edit",
        argumentsJSON: JSON.stringify({ scope: "project", file_path: "MEMORY.md" }),
        output: "edited MEMORY.md: 1 replacement(s)",
      })}
      live={false}
    />,
  );

  expect(screen.getByText("edited MEMORY.md: 1 replacement(s)")).toBeTruthy();
});

test("memory_edit renders multi-edit edits[] with @@ edit N @@ headers and all hunks", () => {
  const Body = toolRendererFor("memory_edit").body!;
  render(
    <Body
      item={item({
        toolName: "memory_edit",
        argumentsJSON: JSON.stringify({
          scope: "project",
          file_path: "MEMORY.md",
          edits: [
            { old_string: "first old", new_string: "first new" },
            { old_string: "second old", new_string: "second new" },
          ],
        }),
      })}
      live={false}
    />,
  );

  expect(screen.getByText("@@ edit 1 @@")).toBeTruthy();
  expect(screen.getByText("@@ edit 2 @@")).toBeTruthy();
  expect(screen.getByText("first old")).toBeTruthy();
  expect(screen.getByText("first new")).toBeTruthy();
  expect(screen.getByText("second old")).toBeTruthy();
  expect(screen.getByText("second new")).toBeTruthy();
});

test("memory_edit renders stringified pre-repair edits", () => {
  const Body = toolRendererFor("memory_edit").body!;
  render(
    <Body
      item={item({
        toolName: "memory_edit",
        argumentsJSON: JSON.stringify({
          scope: "project",
          file_path: "MEMORY.md",
          edits: JSON.stringify([{ old_str: "pre-repair old", new_str: "pre-repair new" }]),
        }),
      })}
      live={false}
    />,
  );

  expect(screen.getByText("pre-repair old")).toBeTruthy();
  expect(screen.getByText("pre-repair new")).toBeTruthy();
});

test("malformed memory_edit shapes fall back to defaultToolBody", () => {
  const Body = toolRendererFor("memory_edit").body!;
  for (const malformed of [
    { scope: "project", file_path: "MEMORY.md", edits: [] },
    { scope: "project", file_path: "MEMORY.md", edits: "not-json" },
    { scope: "project", file_path: "MEMORY.md", edits: [{ old_string: "a", new_string: "b" }], old_string: "c" },
  ]) {
    const { unmount } = render(
      <Body
        item={item({
          toolName: "memory_edit",
          argumentsJSON: JSON.stringify(malformed),
          output: "fallback output",
        })}
        live={false}
      />,
    );
    expect(screen.getByText("fallback output")).toBeTruthy();
    unmount();
  }
});

test("memory_edit removing a Markdown --- line renders with del class in flat and multi-edit diffs", () => {
  const Body = toolRendererFor("memory_edit").body!;
  const flat = render(
    <Body
      item={item({
        toolName: "memory_edit",
        argumentsJSON: JSON.stringify({ scope: "project", file_path: "MEMORY.md", old_string: "--", new_string: "++" }),
      })}
      live={false}
    />,
  );
  const flatLines = diffLines(flat.container);
  expect(flatLines).toHaveLength(4);
  expect(flatLines[0]!.classList.contains(diffStyles.header)).toBe(true);
  expect(flatLines[1]!.classList.contains(diffStyles.header)).toBe(true);
  expect(flatLines[2]!.classList.contains(diffStyles.del)).toBe(true);
  expect(flatLines[2]!.querySelector(`.${diffStyles.content}`)?.textContent).toBe("--");
  expect(flatLines[3]!.classList.contains(diffStyles.add)).toBe(true);
  expect(flatLines[3]!.querySelector(`.${diffStyles.content}`)?.textContent).toBe("++");
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
    argumentsJSON: JSON.stringify({ scope: "session", file_path: "notes.md", body: "not allowed" }),
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

test("renders a recorded memory write, edit, and search through the transcript row pipeline", () => {
  const page = [
    "# Implementation delegation",
    "",
    "Delegate a focused change with the exact files and check it needs.",
    "State the branch and why the change matters.",
    "List facts already verified and unresolved constraints.",
    "Name the commands that prove completion.",
    "Keep the delegated scope narrow and independent.",
    "Use a worktree when writers could collide.",
    "Require evidence from the actual test output.",
    "Review each result before relying on it.",
    "Save durable project decisions before finishing.",
  ].join("\n");
  const oldIndexLine = "- [Implementation delegation](implementation-delegation.md): focused work";
  const newIndexLine = "- [Implementation delegation](implementation-delegation.md): focused work and evidence";
  const calls = [
    item({
      id: "memory-write",
      toolName: "memory_write",
      status: "completed",
      argumentsJSON: JSON.stringify({
        scope: "personal",
        file_path: "implementation-delegation.md",
        body: page,
      }),
    }),
    item({
      id: "memory-edit",
      toolName: "memory_edit",
      status: "completed",
      argumentsJSON: JSON.stringify({
        scope: "project",
        file_path: "MEMORY.md",
        old_string: oldIndexLine,
        new_string: newIndexLine,
      }),
    }),
    item({
      id: "memory-search",
      toolName: "memory_search",
      status: "completed",
      argumentsJSON: JSON.stringify({ scope: "project", pattern: "delegation", path: "." }),
      output: "MEMORY.md:1:implementation delegation\npages/implementation-delegation.md:1:delegation",
    }),
  ];

  // ./index above is the production side-effect barrel; resolve descriptors
  // only through ToolCallItem, the transcript's real row renderer.
  render(calls.map((call) => <ToolCallItem key={call.id} item={call} turn={turn} live={false} />));

  const rows = screen.getAllByTestId("tool-call-item");
  expect(rows).toHaveLength(3);
  expect(rows.map((row) => row.querySelector('[data-testid="tool-row-summary"]')?.textContent)).toEqual([
    "Wrote memory personal/implementation-delegation.md",
    "Edited memory project/MEMORY.md · +1 -1",
    'Searched memory for "delegation" in project · 2 hits',
  ]);

  const [writeRow, editRow] = rows;
  if (!writeRow || !editRow) throw new Error("missing memory write or edit row");
  const writeBody = within(writeRow).getByTestId("tool-call-body");
  expect(within(writeBody).getByRole("heading", { name: "Implementation delegation" }).tagName).toBe("H1");
  const pageParagraph = within(writeBody).getByText(/Delegate a focused change/);
  expect(pageParagraph.tagName).toBe("P");
  expect(pageParagraph.textContent).toContain("Save durable project decisions before finishing.");

  const editBody = within(editRow).getByTestId("tool-call-body");
  expect(within(editBody).getByText(oldIndexLine)).toBeTruthy();
  expect(within(editBody).getByText(newIndexLine)).toBeTruthy();
});
