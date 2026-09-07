import { expect, test } from "vitest";
import type { ItemModel } from "../../../protocol/model";
import type { ProjectedEntry } from "../../../transcriptDisplay/projector";
import { foldToolRuns, runLabel, type ToolRun } from "./toolRuns";

const tool = (id: string, toolName: string, status = "completed"): Extract<ProjectedEntry, { kind: "item" }> => ({
  kind: "item",
  id,
  turnId: "t1",
  sourceIndex: 0,
  isMessage: false,
  item: { id, turnId: "t1", type: "commandExecution", text: "", toolName, status } as ItemModel,
});

const message = (id: string): Extract<ProjectedEntry, { kind: "item" }> => ({
  kind: "item",
  id,
  turnId: "t1",
  sourceIndex: 0,
  isMessage: true,
  item: { id, turnId: "t1", type: "agentMessage", text: "hello", status: "completed" } as ItemModel,
});

const descriptorFor = (name: string) => ({
  match: name,
  summary: (_item: ItemModel, ctx?: { cwd?: string }) =>
    name === "write_file"
      ? "Wrote foo.py"
      : name === "shell"
        ? ctx?.cwd
          ? "Ran ls"
          : "Ran cd /repo && ls"
        : `Read ${name}`,
  fold:
    name === "write_file" || name === "shell"
      ? ("consequential" as const)
      : name === "delegate"
        ? ("never" as const)
        : name === "mcp_send_email"
          ? undefined
          : ("quiet" as const),
});

test("a tool with no fold policy (an unregistered or MCP tool) never folds and breaks the run", () => {
  const out = foldToolRuns(
    [tool("a", "read_file"), tool("b", "mcp_send_email"), tool("c", "read_file"), tool("d", "read_file")],
    { turnSettled: true, descriptorFor },
  );
  expect(out.map((e) => e.kind)).toEqual(["item", "item", "item", "item"]);
  const alone = foldToolRuns([tool("x", "mcp_send_email"), tool("y", "mcp_send_email"), tool("z", "mcp_send_email")], {
    turnSettled: true,
    descriptorFor,
  });
  expect(alone.map((e) => e.kind)).toEqual(["item", "item", "item"]);
});

test("three settled quiet calls fold into one run", () => {
  const out = foldToolRuns([tool("a", "read_file"), tool("b", "read_file"), tool("c", "grep")], {
    turnSettled: true,
    descriptorFor,
  });
  expect(out).toHaveLength(1);
  expect(out[0]).toMatchObject({ kind: "run", entries: [{ id: "a" }, { id: "b" }, { id: "c" }] });
});

test("two calls do not fold", () => {
  const out = foldToolRuns([tool("a", "read_file"), tool("b", "read_file")], { turnSettled: true, descriptorFor });
  expect(out.map((e) => e.kind)).toEqual(["item", "item"]);
});

test("a failed call breaks the run and stays visible", () => {
  const out = foldToolRuns(
    [tool("a", "read_file"), tool("b", "read_file", "failed"), tool("c", "read_file"), tool("d", "read_file")],
    { turnSettled: true, descriptorFor },
  );
  expect(out.map((e) => e.kind)).toEqual(["item", "item", "item", "item"]);
});

test("a live turn never folds", () => {
  const out = foldToolRuns([tool("a", "read_file"), tool("b", "read_file"), tool("c", "read_file")], {
    turnSettled: false,
    descriptorFor,
  });
  expect(out.map((e) => e.kind)).toEqual(["item", "item", "item"]);
});

test("a never-fold tool splits the run", () => {
  const out = foldToolRuns(
    [
      tool("a", "read_file"),
      tool("b", "delegate"),
      tool("c", "read_file"),
      tool("d", "read_file"),
      tool("e", "read_file"),
    ],
    { turnSettled: true, descriptorFor },
  );
  expect(out.map((e) => e.kind)).toEqual(["item", "item", "run"]);
});

test("prose between calls splits the run", () => {
  const out = foldToolRuns(
    [
      tool("a", "read_file"),
      tool("b", "read_file"),
      tool("c", "read_file"),
      message("m"),
      tool("d", "read_file"),
      tool("e", "read_file"),
    ],
    { turnSettled: true, descriptorFor },
  );
  expect(out.map((e) => e.kind)).toEqual(["run", "item", "item", "item"]);
});

test("a still-running call breaks the run even in a settled turn", () => {
  const out = foldToolRuns(
    [tool("a", "read_file"), tool("b", "read_file", "inProgress"), tool("c", "read_file"), tool("d", "read_file")],
    { turnSettled: true, descriptorFor },
  );
  expect(out.map((e) => e.kind)).toEqual(["item", "item", "item", "item"]);
});

test("a run keeps the folded entries in wire order", () => {
  const [run] = foldToolRuns([tool("a", "read_file"), tool("b", "read_file"), tool("c", "read_file")], {
    turnSettled: true,
    descriptorFor,
  });
  expect((run as ToolRun).entries.map((e) => e.id)).toEqual(["a", "b", "c"]);
  expect((run as ToolRun).id).toBe("run:a");
});

test("the label names the consequential step", () => {
  const [run] = foldToolRuns([tool("a", "read_file"), tool("b", "write_file"), tool("c", "read_file")], {
    turnSettled: true,
    descriptorFor,
  });
  expect(runLabel(run as ToolRun, descriptorFor)).toBe("3 steps · Wrote foo.py");
});

test("with no consequential step the label names the last call", () => {
  const [run] = foldToolRuns([tool("a", "read_file"), tool("b", "read_file"), tool("c", "grep")], {
    turnSettled: true,
    descriptorFor,
  });
  expect(runLabel(run as ToolRun, descriptorFor)).toBe("3 steps · Read grep");
});

test("the label hands the working-directory context to the summary, as an expanded row does", () => {
  const [run] = foldToolRuns([tool("a", "read_file"), tool("b", "shell"), tool("c", "read_file")], {
    turnSettled: true,
    descriptorFor,
  });
  expect(runLabel(run as never, descriptorFor, { cwd: "/repo" })).toBe("3 steps · Ran ls");
  expect(runLabel(run as never, descriptorFor)).toBe("3 steps · Ran cd /repo && ls");
});
