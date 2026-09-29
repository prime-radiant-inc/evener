import { lexer } from "marked";
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

	it("prepends multiple blank-line-separated definitions so refs on the far side of a diagram resolve", () => {
		const segments = splitNativeSegments(
			"[a]: https://a.example\n\n[b]: https://b.example\n\nUse [x][a] and [y][b].\n\n```mermaid\ngraph TD; A-->B\n```\n\nAfter [z][a] and [w][b].\n",
		);
		const prose = segments.filter((s) => s.kind === "markdown");
		expect(prose.length).toBe(2);
		for (const segment of prose) {
			expect(segment.kind).toBe("markdown");
			const source = segment.kind === "markdown" ? segment.source : "";
			// The property md4c needs: each segment lexes back to a registry with
			// both labels, and neither def fused onto the other's line.
			expect(Object.keys(lexer(source).links)).toEqual(expect.arrayContaining(["a", "b"]));
			expect(source.startsWith("[a]: https://a.example\n[b]: https://b.example\n\n")).toBe(true);
			expect(source).not.toMatch(/https?:\/\/[^\s\]]*\[/);
		}
	});

	it("prepends a blockquote-nested definition to the prose segment", () => {
		const segments = splitNativeSegments(
			"> [d]: https://example.com\n\nUse [x][d].\n\n```mermaid\ngraph TD; A-->B\n```\n",
		);
		const prose = segments.find((s) => s.kind === "markdown");
		expect(prose?.kind).toBe("markdown");
		const source = prose?.kind === "markdown" ? prose.source : "";
		// The body raw begins with ">", so a leading top-level def proves the
		// collection walked into the blockquote rather than the body carrying it.
		expect(source.startsWith("[d]: https://example.com")).toBe(true);
		expect(Object.keys(lexer(source).links)).toContain("d");
	});

	it("prepends a list-nested definition to the prose segment", () => {
		const segments = splitNativeSegments(
			"- [d]: https://example.com\n\nUse [x][d].\n\n```mermaid\ngraph TD; A-->B\n```\n",
		);
		const prose = segments.find((s) => s.kind === "markdown");
		expect(prose?.kind).toBe("markdown");
		const source = prose?.kind === "markdown" ? prose.source : "";
		expect(source.startsWith("[d]: https://example.com")).toBe(true);
		expect(Object.keys(lexer(source).links)).toContain("d");
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

	it("does not treat a mismatched fence line as the closer", () => {
		// The opener is backtick-fenced; "~~~" is content, not a closer, so the
		// fence is still open and must stay prose (no premature diagram).
		expect(splitNativeSegments("```mermaid\ngraph TD; A-->\n~~~").map((s) => s.kind)).toEqual(["markdown"]);
	});
});
