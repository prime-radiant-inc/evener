// @vitest-environment node

import { expect, test } from "vitest";
import { type WorktreeStep, worktreeWords } from "./worktreeSteps";

const step = (args: Record<string, unknown>, output?: string): WorktreeStep => ({
  argumentsJSON: JSON.stringify(args),
  ...(output === undefined ? {} : { output }),
});

test("worktreeWords list operation formats found count from entries or total_entries", () => {
  // Without output / pending
  expect(worktreeWords(step({ operation: "list" }))).toEqual({
    verb: "Listed worktrees",
  });

  // Unpaged listing with entries array
  expect(
    worktreeWords(step({ operation: "list" }, JSON.stringify({ entries: [{ name: "wt1" }, { name: "wt2" }] }))),
  ).toEqual({
    verb: "Listed worktrees",
    detail: "2 found",
  });

  // Paged listing with total_entries preferring total_entries over entries.length
  expect(
    worktreeWords(
      step(
        { operation: "list", limit: 15, offset: 0 },
        JSON.stringify({
          entries: [{ name: "wt1" }],
          total_entries: 42,
          truncated: true,
        }),
      ),
    ),
  ).toEqual({
    verb: "Listed worktrees",
    detail: "42 found",
  });

  // Paged listing with empty entries on current page (e.g. page had only unmanaged entries)
  expect(
    worktreeWords(
      step(
        { operation: "list", offset: 15 },
        JSON.stringify({
          entries: [],
          total_entries: 18,
          unmanaged: [{ path: "/stray" }],
        }),
      ),
    ),
  ).toEqual({
    verb: "Listed worktrees",
    detail: "18 found",
  });
});
