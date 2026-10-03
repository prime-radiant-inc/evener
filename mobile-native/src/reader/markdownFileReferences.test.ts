import { describe, expect, it } from "vitest";
import { lexer } from "marked";
import { markdownFileReferences, renderMarkdownFileReferences } from "./markdownFileReferences";

const cwd = "/work/b";
function paths(markdown: string) {
	return markdownFileReferences(markdown, cwd).map(({ reference }) => reference.path);
}
function destinations(markdown: string) {
	const result = renderMarkdownFileReferences(markdown, cwd);
	const urls: string[] = [];
	function visit(tokens: ReturnType<typeof lexer>) {
		for (const token of tokens) {
			if (token.type === "link") urls.push(token.href);
			if ("tokens" in token && token.tokens) visit(token.tokens as ReturnType<typeof lexer>);
		}
	}
	visit(lexer(result.markdown));
	return urls.map((url) => result.references.get(url));
}
describe("native token/source-span file adapter", () => {
	it("generates source-bound native file actions", () => {
		const original = "Spec: docs/plan.md\n\n`README.md` and [R](./docs/a%26b.md)";
		const result = renderMarkdownFileReferences(original, cwd);
		expect([...result.references.values()].map((ref) => ref.path)).toEqual([
			"docs/plan.md",
			"README.md",
			"docs/a&b.md",
		]);
		expect([...result.references.values()].map((ref) => ref.cwd)).toEqual([cwd, cwd, cwd]);
		expect(destinations(original).map((ref) => ref?.readTarget)).toEqual([
			"/work/b/docs/plan.md",
			"/work/b/README.md",
			"/work/b/docs/a&b.md",
		]);
	});
	it("recognizes Jesse's exact examples and preserves surrounding Markdown", () => {
		const markdown =
			"Spec: docs/superpowers/specs/2026-10-02-web-session-overview-design.md\nReview: docs/superpowers/specs/2026-10-02-web-session-overview-review.md";
		expect(paths(markdown)).toEqual([
			"docs/superpowers/specs/2026-10-02-web-session-overview-design.md",
			"docs/superpowers/specs/2026-10-02-web-session-overview-review.md",
		]);
	});
	it("keeps literal code/prose percent text, Unicode, aliases, and location meaning", () => {
		expect(paths("“docs/é.md:12.” docs/./100%25.md… `README.md:12` `src/Makefile` /work/b/docs/a.md")).toEqual([
			"docs/é.md",
			"docs/100%25.md",
			"README.md",
			"src/Makefile",
			"docs/a.md",
		]);
	});
	it("decodes destination escapes and entities once, before URI decoding", () => {
		expect(
			paths(
				"[a](./docs/a&amp;lt;b.md) [b](./docs/a%26amp%3Bb.md) [c](./docs/a\\(b\\).md) [d](./docs/a&#35;b.md) [e](./docs/a%23L12.md:12?x=1) [f](./docs/a%253A12.md)",
			),
		).toEqual(["docs/a&lt;b.md", "docs/a&amp;b.md", "docs/a(b).md", "docs/a", "docs/a#L12.md", "docs/a%3A12.md"]);
	});
	it("resolves reference definitions without editing their source", () => {
		const original = '[R][plan] and [plan]\n\n[plan]: ./docs/a%20b.md "title"\n';
		const result = renderMarkdownFileReferences(original, cwd);
		expect(paths(original)).toEqual(["docs/a b.md", "docs/a b.md"]);
		expect(result.markdown.endsWith('[plan]: ./docs/a%20b.md "title"\n')).toBe(true);
		expect(destinations(original).map((ref) => ref?.path)).toEqual(["docs/a b.md", "docs/a b.md"]);
	});
	it.each([
		"../**docs/plan.md**",
		"https://host/**docs/plan.md**",
		"mailto:a/**docs/plan.md**",
		"../`docs/plan.md`",
		"https://host/`README.md`",
		"docs/**plan.md**",
		"docs/foo(bar)/plan.md",
		"[docs/a.md](https://host/x)",
		"[`docs/a.md`](https://host/x)",
		"![docs/a.md](./docs/a.md)",
	])("does not open a suffix or existing anchor, %s", (markdown) => {
		expect(paths(markdown)).toEqual([]);
		expect(renderMarkdownFileReferences(markdown, cwd).markdown).toBe(markdown);
	});
	it("walks headings, nested lists, blockquotes and table cells using raw spans", () => {
		const original =
			"# docs/h.md\n\n> docs/a.md\n> **docs/b.md**\n\n- docs/c.md\n  - `src/Makefile`\n\n| File | Why |\n| --- | --- |\n| docs/d.md | *docs/e.md* |";
		expect(paths(original)).toEqual([
			"docs/h.md",
			"docs/a.md",
			"docs/b.md",
			"docs/c.md",
			"src/Makefile",
			"docs/d.md",
			"docs/e.md",
		]);
		const result = renderMarkdownFileReferences(original, cwd);
		expect(result.markdown).toContain("> **[");
		expect(result.markdown).toContain("  - [`src/Makefile`]");
	});
	it("leaves excluded fences, indented code, Mermaid, HTML and external links byte-for-byte", () => {
		const original =
			"```mermaid\ndocs/a.md\n```\n\n```sh\ndocs/b.md\n```\n\n    docs/c.md\n\n<div>docs/d.md</div>\n\n[web](https://example.test/docs/e.md)";
		expect(paths(original)).toEqual([]);
		expect(renderMarkdownFileReferences(original, cwd).markdown).toBe(original);
	});
	it("preserves CRLF and escaped source outside the generated raw ranges", () => {
		const original = "# title\r\n\r\n\\* docs/a.md \\*\r\n\r\n```sh\r\ndocs/b.md\r\n```\r\n";
		const result = renderMarkdownFileReferences(original, cwd);
		expect(paths(original)).toEqual(["docs/a.md"]);
		expect(result.markdown.replace(/\[docs\/a\.md\]\([^)]*\)/, "docs/a.md")).toBe(original);
	});
	it("keeps missing cwd inert and never caches bound references or render identifiers", () => {
		expect(renderMarkdownFileReferences("docs/a.md", "")).toEqual({ markdown: "docs/a.md", references: new Map() });
		const first = renderMarkdownFileReferences("docs/a.md", "/work/a");
		const next = renderMarkdownFileReferences("docs/a.md", cwd);
		expect([...next.references.values()][0]?.readTarget).toBe("/work/b/docs/a.md");
		expect([...first.references.keys()]).not.toEqual([...next.references.keys()]);
	});
});
it("unescapes an escaped entity marker once and preserves explicit link titles and angle syntax", () => {
	const original = '[R](<./docs/a\\&amp;b.md> "keep &amp; title")';
	const result = renderMarkdownFileReferences(original, cwd);
	expect([...result.references.values()].map((ref) => ref.path)).toEqual(["docs/a&amp;b.md"]);
	expect(result.markdown).toMatch(/^\[R\]\(<evener-file:[^>]+> "keep &amp; title"\)$/);
	expect(paths('[R][r]\n\n[r]: ./docs/a\\&amp;b.md "keep"')).toEqual(["docs/a&amp;b.md"]);
});
it.each([
	"[x](../docs/a.md)",
	"[x](//host/docs/a.md)",
	"[x](./docs/%2e%2e/a.md)",
	"[x](./docs/a%2fb.md)",
	"[x](./docs/a%ZZ.md)",
	"[x](./docs/a&bsol;b.md)",
	"[x](./docs/a&Tab;b.md)",
	"[x](file:///work/b/docs/a.md)",
	"[x](https&colon;//example.test/docs/a.md)",
	"../**docs/a.md**",
	"docs/`README.md`/other",
	"foo`README.md`",
])("preserves rejected adapter input, %s", (original) => {
	expect(paths(original)).toEqual([]);
	expect(renderMarkdownFileReferences(original, cwd).markdown).toBe(original);
});
it("keeps named/numeric Unicode, URI percent and encoded metadata order independent", () => {
	expect(
		paths(
			"[a](./docs/caf&eacute;.md) [b](./docs/&#x1F600;.md) [c](./docs/a%3F12%23L2%3A3.md:12#L2) [d](./docs/a&percnt;2525.md) [e](./docs/a&NotEqualTilde;b.md)",
		),
	).toEqual(["docs/café.md", "docs/😀.md", "docs/a?12#L2:3.md", "docs/a%25.md", "docs/a≂̸b.md"]);
});
it("preserves unrecognized entity-looking filename data, including object-prototype names", () => {
	expect(paths("[x](./docs/a&constructor;b.md) [y](./docs/a&unknown;b.md)")).toEqual([
		"docs/a&constructor;b.md",
		"docs/a&unknown;b.md",
	]);
});
it("preserves multiline reference-label source and blockquote prefixes outside the target suffix", () => {
	const original = '> [first\r\n> second][r]\r\n\r\n[r]: ./docs/a.md "kept"\r\n';
	const result = renderMarkdownFileReferences(original, cwd);
	expect([...result.references.values()].map((ref) => ref.path)).toEqual(["docs/a.md"]);
	expect(result.markdown.replace(/\(evener-file:[^)]*\)/, "[r]")).toBe(original);
});
