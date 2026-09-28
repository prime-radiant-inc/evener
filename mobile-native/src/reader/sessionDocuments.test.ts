import { describe, expect, it } from "vitest";
import { documentFreshness, sessionDocuments } from "./sessionDocuments";

const cwd = "/home/jesse/git/evener";

describe("the session's documents (spec 10.1)", () => {
	it("lists what it wrote and linked, one row per file, newest write first", () => {
		expect(
			sessionDocuments(
				[
					{ path: "docs/superpowers/plans/settle.md", updatedAt: "2026-09-26T11:39:00.000Z" },
					{ path: "docs/design/flake-triage.md", updatedAt: "2026-09-26T11:52:00.000Z" },
					{ path: "./docs/superpowers/plans/settle.md", updatedAt: "2026-09-26T11:45:00.000Z" },
					{ path: "agent/retirement.go" },
				],
				[
					{
						id: "u1",
						url: "file:///home/jesse/git/evener/docs/superpowers/plans/settle.md",
						label: "Settle race plan",
					},
					{ id: "u2", url: "https://github.com/prime-radiant-inc/evener/pull/2138", label: "PR 2138" },
					{ id: "u3", url: "file:///home/jesse/notes/todo.md" },
				],
				cwd,
			),
		).toEqual([
			{ path: "docs/design/flake-triage.md", kind: "Doc", updatedAt: "2026-09-26T11:52:00.000Z" },
			{ path: "docs/superpowers/plans/settle.md", kind: "Plan", updatedAt: "2026-09-26T11:45:00.000Z" },
			{ path: "agent/retirement.go", kind: "Code" },
		]);
	});

	it("leaves out a file link that names no path, or a file outside the session's folder", () => {
		expect(
			sessionDocuments(
				[],
				[
					{ id: "u1", url: "file://server/share/x.md" },
					{ id: "u2", url: "file:///home/jesse/notes/todo.md" },
				],
				cwd,
			),
		).toEqual([]);
	});
});

describe("new or changed since you last opened it", () => {
	const read = { blocks: [], readAt: 1, updatedAt: "2026-09-26T11:39:00.000Z" };
	it.each([
		[null, "2026-09-26T11:39:00.000Z", "new"],
		[null, undefined, "new"],
		[read, "2026-09-26T11:39:00.000Z", "read"],
		[read, "2026-09-26T11:45:00.000Z", "changed"],
		[read, undefined, "read"],
		[{ blocks: [], readAt: 1 }, "2026-09-26T11:45:00.000Z", "read"],
	] as const)("last read %o, written %s: %s", (lastRead, updatedAt, expected) => {
		expect(documentFreshness(lastRead, updatedAt)).toBe(expected);
	});
});
