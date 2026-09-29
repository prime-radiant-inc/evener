import { describe, expect, it } from "vitest";
import { splitNativeSegments } from "./markdownSegments";

describe("splitNativeSegments", () => {
	it("returns prose untouched when no mermaid fence exists", () => {
		expect(splitNativeSegments("hello **world**")).toEqual([{ kind: "markdown", source: "hello **world**" }]);
	});

	it("splits a closed mermaid fence out", () => {
		const segments = splitNativeSegments("before\n\n```mermaid\ngraph TD; A-->B\n```\n\nafter\n");
		expect(segments.map((s) => s.kind)).toEqual(["markdown", "mermaid", "markdown"]);
		expect(segments[1]).toEqual({ kind: "mermaid", source: "graph TD; A-->B\n" });
	});

	it("prepends link definitions to every prose segment so references resolve standalone", () => {
		const segments = splitNativeSegments(
			"See [the docs][d].\n\n```mermaid\ngraph TD; A-->B\n```\n\nAgain [the docs][d].\n\n[d]: https://example.com\n",
		);
		const prose = segments.filter((s) => s.kind === "markdown");
		expect(prose.length).toBe(2);
		for (const segment of prose) {
			expect(segment.kind === "markdown" && segment.source).toContain("[d]: https://example.com");
		}
	});

	it("collects definitions nested in blockquotes and lists", () => {
		const segments = splitNativeSegments(
			"> [d]: https://example.com\n\nUse [x][d].\n\n```mermaid\ngraph TD; A-->B\n```\n",
		);
		const prose = segments.find((s) => s.kind === "markdown");
		expect(prose?.kind === "markdown" && prose.source).toContain("[d]: https://example.com");
	});

	it("keeps a trailing open mermaid fence in the prose segment", () => {
		const segments = splitNativeSegments("intro\n\n```mermaid\ngraph TD; A-->");
		expect(segments.map((s) => s.kind)).toEqual(["markdown"]);
		expect(segments[0]?.kind === "markdown" && segments[0].source).toContain("graph TD; A-->");
	});

	it("handles CRLF fences", () => {
		const segments = splitNativeSegments("```mermaid\r\ngraph TD; A-->B\r\n```\r\n");
		expect(segments).toEqual([{ kind: "mermaid", source: "graph TD; A-->B\n" }]);
	});
});
