// @vitest-environment node

import { expect, test } from "vitest";
import {
  type MemoryStep,
  memoryDeleteSummary,
  memoryEditSummary,
  memoryFileTarget,
  memoryMutation,
  memoryProgress,
  memoryReadSummary,
  memorySearchSummary,
  memoryWriteSummary,
} from "./memorySteps";

const step = (args: Record<string, unknown>, output?: string): MemoryStep => ({
  argumentsJSON: JSON.stringify(args),
  ...(output === undefined ? {} : { output }),
});

test.each<[string, string, (step: MemoryStep) => string, MemoryStep, string]>([
  [
    "write",
    "Writing memory personal/implementation-delegation.md",
    memoryWriteSummary,
    step({ scope: "personal", file_path: "implementation-delegation.md" }),
    "Wrote memory personal/implementation-delegation.md",
  ],
  [
    "edit",
    "Editing memory project/MEMORY.md",
    memoryEditSummary,
    step({ scope: "project", file_path: "MEMORY.md", old_string: "old", new_string: "new" }),
    "Edited memory project/MEMORY.md · +1 -1",
  ],
  [
    "read",
    "Reading memory session/notes.md",
    memoryReadSummary,
    step({ scope: "session", file_path: "notes.md", offset: 1, limit: 40 }, "line 1\nline 2"),
    "Read memory session/notes.md · lines 1-40",
  ],
  [
    "search",
    'Searching memory for "pattern" in personal/guides',
    memorySearchSummary,
    step({ scope: "personal", pattern: "pattern", path: "guides" }, "one\ntwo\nthree\n"),
    'Searched memory for "pattern" in personal/guides · 3 hits',
  ],
  [
    "delete",
    "Removing memory project/old.md",
    memoryDeleteSummary,
    step({ scope: "project", file_path: "old.md" }),
    "Removed memory project/old.md",
  ],
])("words a memory %s step and its progress", (tool, progress, summary, memoryStep, settled) => {
  expect(summary(memoryStep)).toBe(settled);
  expect(memoryProgress(`memory_${tool}`, memoryStep)).toBe(progress);
});

test.each<[string, (step: MemoryStep) => string, string]>([
  ["personal", memoryWriteSummary, "Wrote memory personal/page.md"],
  ["project", memoryWriteSummary, "Wrote memory project/page.md"],
  ["session", memoryWriteSummary, "Wrote memory session/page.md"],
])("writes a memory page in the %s scope", (scope, summary, settled) => {
  expect(summary(step({ scope, file_path: "page.md" }))).toBe(settled);
});

test.each<[string, (step: MemoryStep) => string, string, string]>([
  ["write", memoryWriteSummary, "Wrote memory bare.md", "Writing memory bare.md"],
  ["edit", memoryEditSummary, "Edited memory bare.md · +1 -1", "Editing memory bare.md"],
  ["read", memoryReadSummary, "Read memory bare.md", "Reading memory bare.md"],
  ["delete", memoryDeleteSummary, "Removed memory bare.md", "Removing memory bare.md"],
])("uses the bare path when a memory %s call has no scope", (tool, summary, settled, progress) => {
  const memoryStep = step({ file_path: "bare.md", old_string: "old", new_string: "new" });
  expect(summary(memoryStep)).toBe(settled);
  expect(memoryProgress(`memory_${tool}`, memoryStep)).toBe(progress);
});

test.each<[string, (step: MemoryStep) => string, string, string]>([
  ["write", memoryWriteSummary, "Wrote memory", "Writing memory"],
  ["edit", memoryEditSummary, "Edited memory", "Editing memory"],
  ["read", memoryReadSummary, "Read memory", "Reading memory"],
  ["delete", memoryDeleteSummary, "Removed memory", "Removing memory"],
])("uses a bare-verb fallback when a memory %s call has no path", (tool, summary, settled, progress) => {
  const memoryStep = step({ scope: "personal" });
  expect(summary(memoryStep)).toBe(settled);
  expect(memoryProgress(`memory_${tool}`, memoryStep)).toBe(progress);
});

test("uses the scope alone for an omitted or dot search path and falls back without a pattern", () => {
  for (const path of [undefined, "."]) {
    expect(memorySearchSummary(step({ scope: "personal", pattern: "needle", ...(path ? { path } : {}) }))).toBe(
      'Searched memory for "needle" in personal',
    );
    expect(
      memoryProgress("memory_search", step({ scope: "personal", pattern: "needle", ...(path ? { path } : {}) })),
    ).toBe('Searching memory for "needle" in personal');
  }
  expect(memorySearchSummary(step({ scope: "personal", path: "." }))).toBe("Searched memory");
  expect(memoryProgress("memory_search", step({ scope: "personal", path: "." }))).toBe("Searching memory");
});

test("uses the bare path when a memory search has no scope", () => {
  const memoryStep = step({ pattern: "needle", path: "guides" });
  expect(memorySearchSummary(memoryStep)).toBe('Searched memory for "needle" in guides');
  expect(memoryProgress("memory_search", memoryStep)).toBe('Searching memory for "needle" in guides');
});

test("classifies the memory mutators and only them", () => {
  for (const tool of ["memory_write", "memory_edit", "memory_delete"]) {
    expect(memoryMutation(tool)).toBe(true);
  }
  for (const tool of ["memory_read", "memory_search", "read_file", "grep", "shell"]) {
    expect(memoryMutation(tool)).toBe(false);
  }
});

test("targets a memory step's scope and path from parsed arguments", () => {
  expect(memoryFileTarget({ scope: "personal", file_path: "page.md" })).toBe("personal/page.md");
  expect(memoryFileTarget({ file_path: "bare.md" })).toBe("bare.md");
  expect(memoryFileTarget({ scope: "personal" })).toBeUndefined();
});

test("counts logical lines when a read without a limit returns unterminated output", () => {
  expect(memoryReadSummary(step({ scope: "personal", file_path: "page.md" }, "line 1\nline 2"))).toBe(
    "Read memory personal/page.md · lines 1-2",
  );
  expect(memoryReadSummary(step({ scope: "personal", file_path: "page.md" }, "line 1\nline 2\n"))).toBe(
    "Read memory personal/page.md · lines 1-2",
  );
});
