import { describe, expect, it } from "vitest";
import type { ItemModel, TurnModel } from "@evener/appwire-client";
import { documentPath, documentReferences, fileWrites, messageDocuments, writtenPaths } from "./documentReferences";

const cwd = "/home/jesse/git/evener";
const FENCE = "`".repeat(3);
const none = new Set<string>();

const said = (id: string, text: string): ItemModel => ({ id, turnId: "t", type: "agentMessage", text });
const wrote = (id: string, tool: string, path: string, completedAt?: string, error?: string): ItemModel => ({
	id,
	turnId: "t",
	type: "commandExecution",
	text: "",
	toolName: tool,
	argumentsJSON: JSON.stringify({ file_path: path, content: "x" }),
	...(completedAt ? { completedAt } : {}),
	...(error ? { error } : {}),
});
const turn = (id: string, items: ItemModel[], completedAt?: string): TurnModel => ({
	id,
	status: "completed",
	items,
	...(completedAt ? { completedAt } : {}),
});

describe("a path inside the session's folder", () => {
	it("is relative, without a leading ./, and nothing outside the folder is one", () => {
		expect(documentPath("/home/jesse/git/evener/docs/plan.md", cwd)).toBe("docs/plan.md");
		expect(documentPath("./docs/plan.md", cwd)).toBe("docs/plan.md");
		expect(documentPath("/etc/hosts", cwd)).toBeUndefined();
		expect(documentPath("../other/plan.md", cwd)).toBeUndefined();
		expect(documentPath("./", cwd)).toBeUndefined();
	});
});

describe("the documents a message names (spec 8.2)", () => {
	it("finds inline code and link targets in reading order, each once", () => {
		const markdown = [
			"The plan is in `docs/superpowers/plans/2026-09-25-settle-race.md`; see [the spec](docs/superpowers/specs/x.md).",
			"",
			"- Fixed `/home/jesse/git/evener/agent/retirement.go:1977`",
			"- Again `./docs/superpowers/plans/2026-09-25-settle-race.md`",
			"",
			"| File | Why |",
			"| --- | --- |",
			"| `internal/hubcore/tree.go` | the lock |",
		].join("\n");
		expect(messageDocuments(markdown, cwd, none)).toEqual([
			"docs/superpowers/plans/2026-09-25-settle-race.md",
			"docs/superpowers/specs/x.md",
			"agent/retirement.go",
			"internal/hubcore/tree.go",
		]);
	});

	it("reads local targets, but excludes schemes and existing external anchor labels", () => {
		expect(messageDocuments("[plan](file:///home/jesse/git/evener/docs/plan.md)", cwd, none)).toEqual([]);
		expect(messageDocuments("[`plan.md`](docs/plan.md)", cwd, none)).toEqual(["docs/plan.md"]);
		expect(
			messageDocuments(
				"[`docs/plan.md`](https://github.com/prime-radiant-inc/evener/blob/main/docs/plan.md)",
				cwd,
				none,
			),
		).toEqual([]);
	});

	it("discovers prose with the same normalized URI identity as inline actions", () => {
		expect(messageDocuments("Spec: docs/./plan.md:12. [R](./docs/a%26b.md) `src/Makefile`", cwd, none)).toEqual([
			"docs/plan.md",
			"docs/a&b.md",
			"src/Makefile",
		]);
	});

	it("rejects adjacent traversal formatting and literal percent decoding", () => {
		expect(messageDocuments("../**docs/plan.md** `docs/100%25.md` [R](./docs/100%25.md)", cwd, none)).toEqual([
			"docs/100%25.md",
			"docs/100%.md",
		]);
	});

	it("names nothing for commands, directories, web links, files outside the folder, or fenced code", () => {
		const markdown = [
			"Run `go test ./agent/...` in `src/` and read https://example.test/a.md or [PR](https://github.com/x/y/pull/1).",
			"Also `/etc/hosts` and `../other/plan.md`.",
			"",
			`${FENCE}sh`,
			"cat docs/plan.md",
			FENCE,
		].join("\n");
		expect(messageDocuments(markdown, cwd, none)).toEqual([]);
	});

	it("takes a bare file name only when the session wrote that file", () => {
		expect(messageDocuments("Updated `README.md`.", cwd, none)).toEqual([]);
		expect(messageDocuments("Updated `README.md`.", cwd, new Set(["README.md"]))).toEqual(["README.md"]);
	});

	it("takes a bare file name the session wrote with no time it can read", () => {
		const turns = [turn("t1", [wrote("w", "write_file", "README.md")])];
		expect(writtenPaths(turns, cwd)).toEqual(new Set(["README.md"]));
		expect(messageDocuments("Updated `README.md`.", cwd, writtenPaths(turns, cwd))).toEqual(["README.md"]);
	});
});

describe("when the session wrote each file", () => {
	it("keeps the newest successful write or edit inside the folder", () => {
		const turns = [
			turn("turn-1", [
				wrote("a", "write_file", "/home/jesse/git/evener/docs/plan.md", "2026-09-26T11:39:00.000Z"),
				wrote("b", "write_file", "/etc/hosts", "2026-09-26T11:40:00.000Z"),
				wrote("c", "edit_file", "docs/plan.md", "2026-09-26T11:50:00.000Z", "old_string not found"),
				wrote("d", "read_file", "docs/other.md", "2026-09-26T11:41:00.000Z"),
			]),
			turn("turn-2", [wrote("e", "edit_file", "./docs/plan.md")], "2026-09-26T11:45:00.000Z"),
			turn("turn-3", [wrote("f", "write_file", "docs/undated.md")]),
		];
		expect(fileWrites(turns, cwd)).toEqual(new Map([["docs/plan.md", "2026-09-26T11:45:00.000Z"]]));
	});
});

describe("files changed through apply_patch", () => {
	const patched = (id: string, patch: string, completedAt?: string, error?: string): ItemModel => ({
		id,
		turnId: "t",
		type: "commandExecution",
		text: "",
		toolName: "apply_patch",
		argumentsJSON: JSON.stringify({ patch }),
		...(completedAt ? { completedAt } : {}),
		...(error ? { error } : {}),
	});
	const PATCH = [
		"*** Begin Patch",
		"*** Add File: docs/new.md",
		"+# New",
		"*** Update File: docs/plan.md",
		"@@",
		"-old",
		"+new",
		"*** Update File: docs/draft.md",
		"*** Move to: docs/final.md",
		"@@",
		"-a",
		"+b",
		"*** Delete File: docs/gone.md",
		"*** Update File: /etc/hosts",
		"@@",
		"-x",
		"+y",
		"*** End Patch",
	].join("\n");

	it("counts each file a patch adds or updates inside the folder, under its new name when it moves", () => {
		const at = "2026-09-26T11:50:00.000Z";
		expect(fileWrites([turn("t1", [patched("p", PATCH, at)])], cwd)).toEqual(
			new Map([
				["docs/new.md", at],
				["docs/plan.md", at],
				["docs/final.md", at],
			]),
		);
	});

	it("counts nothing from a patch that failed", () => {
		expect(fileWrites([turn("t1", [patched("p", PATCH, "2026-09-26T11:50:00.000Z", "hunk failed")])], cwd)).toEqual(
			new Map(),
		);
	});
});

describe("every document the session named or wrote (spec 10.1)", () => {
	it("lists a file the session wrote with no time it can read, without an age", () => {
		expect(documentReferences([turn("t1", [wrote("w", "write_file", "docs/undated.md")])], cwd)).toEqual([
			{ path: "docs/undated.md" },
		]);
	});

	it("lists each once, in the order it first appeared, with its newest write", () => {
		const turns = [
			turn("turn-1", [
				said("m1", "Drafted `docs/a.md`; the fix goes in `agent/x.go`."),
				wrote("w1", "write_file", "docs/b.md", "2026-09-26T11:39:00.000Z"),
			]),
			turn("turn-2", [
				wrote("w2", "edit_file", "docs/a.md", "2026-09-26T11:45:00.000Z"),
				said("m2", "Revised `docs/a.md` and `docs/b.md`."),
			]),
		];
		expect(documentReferences(turns, cwd)).toEqual([
			{ path: "docs/a.md", updatedAt: "2026-09-26T11:45:00.000Z" },
			{ path: "agent/x.go" },
			{ path: "docs/b.md", updatedAt: "2026-09-26T11:39:00.000Z" },
		]);
	});
});
