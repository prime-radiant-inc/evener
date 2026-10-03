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

describe("review span regressions", () => {
	it.each([
		'Before <a href="https://example.test/x"> docs/a.md </a> after',
		'Before <a href="https://example.test/x"> `README.md` [R](./docs/a.md) </a> after',
		'Before <a href="https://example.test/x"> **docs/a.md *`README.md`* [R](./docs/b.md)** </a> after',
		'Before **<a href="https://example.test/x"> docs/a.md** `README.md` </a> after',
		"Before <script> docs/a.md `README.md` [R](./docs/b.md) </script> after",
		"Before <pre> docs/a.md </pre> after",
	])("I1 excludes HTML anchor/raw interiors, %s", (original) => {
		expect(paths(original)).toEqual([]);
		const result = renderMarkdownFileReferences(original, cwd);
		expect([...result.references.values()]).toEqual([]);
		expect(result.markdown).toBe(original);
	});
	it.each([
		'docs/b.md <a href="https://example.test/x"> docs/a.md </a>"docs/c.md"',
		'docs/b.md <a href="https://example.test/x"> docs/a.md </a> docs/c.md',
		"docs/b.md <script> docs/a.md </script> docs/c.md",
	])("I1 keeps surrounding eligible prose and original source, %s", (original) => {
		const result = renderMarkdownFileReferences(original, cwd);
		expect(paths(original)).toEqual(["docs/b.md", "docs/c.md"]);
		expect([...result.references.values()].map((ref) => ref.readTarget)).toEqual([
			"/work/b/docs/b.md",
			"/work/b/docs/c.md",
		]);
		expect(result.markdown.replace(/\[(docs\/[bc]\.md)\]\(evener-file:[^)]*\)/g, "$1")).toBe(original);
	});
	it("I1 does not manufacture a prose boundary after an excluded HTML edge", () => {
		const original = 'docs/b.md <a href="https://example.test/x"> docs/a.md </a>docs/c.md';
		const result = renderMarkdownFileReferences(original, cwd);
		expect(paths(original)).toEqual(["docs/b.md"]);
		expect(result.markdown.replace(/\[docs\/b\.md\]\(evener-file:[^)]*\)/, "docs/b.md")).toBe(original);
	});
	it("I1 does not create code boundaries at an excluded HTML edge", () => {
		const original = 'Before <a href="https://example.test/x"> docs/a.md </a>foo`README.md` after';
		expect(paths(original)).toEqual([]);
		expect(renderMarkdownFileReferences(original, cwd).markdown).toBe(original);
	});
	it.each([
		"[foo `]` bar](./docs/a.md)",
		"[foo `[` bar](./docs/a.md)",
		"[foo ``]`` bar](./docs/a.md)",
		"[foo ``[`` bar](./docs/a.md)",
		"[foo **`]`** bar](./docs/a.md)",
		'[foo \\] and \\[ bar](<./docs/a.md> "keep &amp; title")',
		"[array `arr[0]` docs](./docs/a.md)",
		'[foo `]`\r\nbar](<./docs/a.md> "keep")',
	])("I2 uses the recognized explicit label boundary, %s", (original) => {
		const result = renderMarkdownFileReferences(original, cwd);
		expect(paths(original)).toEqual(["docs/a.md"]);
		expect([...result.references.values()].map((ref) => ref.readTarget)).toEqual(["/work/b/docs/a.md"]);
		expect(result.markdown.replace(/evener-file:[\d-]+/, "./docs/a.md")).toBe(original);
		expect(destinations(original).map((ref) => ref?.path)).toEqual(["docs/a.md"]);
	});
	it.each(["[foo `]` bar]", "[foo `[` bar]", "[foo ``]`` bar]", "[foo `]`\r\nbar]"])(
		"I2 uses applicable full-reference label boundaries, %s",
		(label) => {
			const original = `${label}[r]\r\n\r\n[r]: <./docs/a.md> "keep"\r\n`;
			const result = renderMarkdownFileReferences(original, cwd);
			expect(paths(original)).toEqual(["docs/a.md"]);
			expect([...result.references.values()].map((ref) => ref.readTarget)).toEqual(["/work/b/docs/a.md"]);
			expect(result.markdown.replace(/\(evener-file:[^)]*\)/, "[r]")).toBe(original);
			expect(destinations(original).map((ref) => ref?.path)).toEqual(["docs/a.md"]);
		},
	);
	it.each([
		"| File | Why |\n| --- | --- |\n| a\\|b docs/a.md | docs/c.md |",
		"| File | Why |\n| --- | --- |\n| docs/a.md a\\|b | docs/c.md |",
		"| File | Why |\n| --- | --- |\n| a\\|b `docs/a.md` | docs/c.md |",
		"| File | Why |\n| --- | --- |\n| a\\|b [R](./docs/a.md) | docs/c.md |",
		'| File | Why |\n| --- | --- |\n| [a\\|b](<./docs/a.md> "keep") | docs/c.md |',
		"| File | Why |\r\n| --- | --- |\r\n| a\\|b docs/a.md | x\\|y docs/c.md |\r\n",
		"| a\\|b docs/a.md | Why |\n| --- | --- |\n| x\\|y | docs/c.md |",
	])("I3 maps escaped table pipes to original source, %s", (original) => {
		const result = renderMarkdownFileReferences(original, cwd);
		expect(paths(original)).toEqual(["docs/a.md", "docs/c.md"]);
		expect([...result.references.values()].map((ref) => ref.readTarget)).toEqual([
			"/work/b/docs/a.md",
			"/work/b/docs/c.md",
		]);
		const restored = result.markdown
			.replace(/\[(`?docs\/[ac]\.md`?)\]\(evener-file:[^)]*\)/g, "$1")
			.replace(/evener-file:[\d-]+/, "./docs/a.md");
		expect(restored).toBe(original);
	});
	it("I3 preserves multiple rows, slash parity and nested table prefixes", () => {
		const original =
			"> | File | Why |\r\n> | --- | --- |\r\n> | a\\\\\\|b docs/a.md | docs/c.md |\r\n> | docs/d.md a\\\\| docs/e.md |\r\n";
		const result = renderMarkdownFileReferences(original, cwd);
		expect(paths(original)).toEqual(["docs/a.md", "docs/c.md", "docs/d.md", "docs/e.md"]);
		expect([...result.references.values()].map((ref) => ref.readTarget)).toEqual([
			"/work/b/docs/a.md",
			"/work/b/docs/c.md",
			"/work/b/docs/d.md",
			"/work/b/docs/e.md",
		]);
		expect(result.markdown.replace(/\[(docs\/[acde]\.md)\]\(evener-file:[^)]*\)/g, "$1")).toBe(original);
	});
	it("I3 preserves escaped pipes inside generated prose and code labels", () => {
		const original = "| File | Why |\n| --- | --- |\n| `docs/a\\|b.md` | docs/c\\|d.md |";
		const result = renderMarkdownFileReferences(original, cwd);
		expect(paths(original)).toEqual(["docs/a|b.md", "docs/c|d.md"]);
		expect([...result.references.values()].map((ref) => ref.readTarget)).toEqual([
			"/work/b/docs/a|b.md",
			"/work/b/docs/c|d.md",
		]);
		expect(result.markdown.replace(/\[([^\]]+)\]\(evener-file:[^)]*\)/g, "$1")).toBe(original);
	});
});

describe("HTML ancestry across marked inline collections", () => {
	function assertReferences(original: string, expected: string[], sourceCwd = cwd) {
		const discovered = markdownFileReferences(original, sourceCwd);
		const result = renderMarkdownFileReferences(original, sourceCwd);
		for (const references of [discovered.map(({ reference }) => reference), [...result.references.values()]]) {
			expect(references.map((reference) => reference.path)).toEqual(expected);
			expect(references.map((reference) => reference.cwd)).toEqual(expected.map(() => sourceCwd));
			expect(references.map((reference) => reference.readTarget)).toEqual(
				expected.map((path) => `${sourceCwd}/${path}`),
			);
		}
		expect(result.markdown.replace(/\[(docs\/[bc]\.md)\]\(evener-file:[\d-]+\)/g, "$1")).toBe(original);
	}

	it.each([
		'Before <a href="https://example.test/x"> first\n\n docs/a.md </a> after',
		"Before <script> first\n\n docs/a.md </script> after",
		"Before <pre> first\n\n docs/a.md </pre> after",
	])("excludes the exact reviewer cross-paragraph interior, %s", (original) => {
		assertReferences(original, []);
	});
	it("keeps only outside prose in the exact reviewer script case", () => {
		assertReferences("Before <script> first\n\n docs/a.md </script> docs/b.md", ["docs/b.md"]);
	});
	it.each([
		'Before <a href="https://example.test/x"> first\r\n\r\n docs/a.md </a> docs/b.md\r\n',
		"Before <script> first\r\n\r\n docs/a.md </script> docs/b.md\r\n",
		'Before **<a href="https://example.test/x"> first**\n\n *docs/a.md </a>* docs/b.md',
		'> Before <a href="https://example.test/x"> first\n>\n> docs/a.md </a> docs/b.md',
		'- Before <a href="https://example.test/x"> first\n\n  docs/a.md </a> docs/b.md',
		'- Before <a href="https://example.test/x"> first\n- docs/a.md </a> docs/b.md',
		'Before <a href="https://example.test/x"> first\n\n# docs/a.md </a> docs/b.md',
		'Before <a href="https://example.test/x"> first\n\n> docs/a.md </a> docs/b.md',
		'> Before <a href="https://example.test/x"> first\n\n docs/a.md </a> docs/b.md',
		"Before <script> first\n\n| docs/a.md </script> docs/b.md | after |\n| --- | --- |",
		"Before <script> first\n\n# docs/a.md\n\n<pre>docs/a.md</pre>\n\n docs/a.md </script> docs/b.md",
		"Before <script> first\n\n```sh\n</script> docs/a.md\n```\n\n docs/a.md </script> docs/b.md",
	])("retains ancestry and original bytes across collections, %s", (original) => {
		assertReferences(original, ["docs/b.md"]);
	});
	it.each([
		'| Before <a href="https://example.test/x"> first | docs/a.md </a> docs/b.md |\n| --- | --- |\n| docs/c.md | after |',
		"| Before <script> first | docs/a.md |\n| --- | --- |\n| docs/a.md </script> docs/b.md | docs/c.md |",
		"> | Before <pre> first | docs/a.md |\r\n> | --- | --- |\r\n> | docs/a.md </pre> docs/b.md | docs/c.md |\r\n",
	])("carries ancestry across table cells without joining their boundaries, %s", (original) => {
		assertReferences(original, ["docs/b.md", "docs/c.md"]);
	});
	it.each([
		'Before <a href="https://example.test/x"> first\n\n docs/a.md `README.md` [R](./docs/a.md)',
		"Before <script> first\n\n docs/a.md `README.md` [R](./docs/a.md)",
		"Before <pre> first\n\n# docs/a.md\n\n docs/a.md",
	])("keeps unclosed ancestry ineligible through the end of one message, %s", (original) => {
		assertReferences(original, []);
	});
	it.each([
		"Before <script> first\n\n docs/a.md </script> ../**docs/a.md** docs/b.md",
		"Before <pre> first\n\n docs/a.md </pre> https://host/**docs/a.md** docs/b.md",
		'Before <a href="https://example.test/x"> first\n\n docs/a.md </a>foo`README.md` docs/b.md',
	])("retains full-block invalid-boundary context after closing tags, %s", (original) => {
		assertReferences(original, ["docs/b.md"]);
	});
	it("does not join invalid boundary text from different paragraphs or cells", () => {
		assertReferences("../\n\n docs/b.md\n\n| ../ | docs/c.md |\n| --- | --- |", ["docs/b.md", "docs/c.md"]);
	});
	it.each([
		"[Before <script> first](https://example.test/x)\n\n docs/a.md </script> docs/b.md",
		"![Before <script> first](./image.png)\n\n docs/a.md </script> docs/b.md",
		"[Before **<script> first**](https://example.test/x) docs/a.md </script> docs/b.md",
	])("carries raw state from actual marked link/image label lexing without scanning labels, %s", (original) => {
		assertReferences(original, ["docs/b.md"]);
	});
	it("uses marked's post-link anchor state without promoting an interior file link", () => {
		assertReferences('Before <a href="https://example.test/x"> first\n\n[inner](./docs/a.md) docs/b.md', ["docs/b.md"]);
	});
	it("does not leak ancestry or cwd through separate messages, render calls or cached candidates", () => {
		const unclosed = 'Before <a href="https://example.test/x"> first\n\n docs/a.md';
		const closed = "Before <script> first\n\n docs/a.md </script> docs/b.md";
		for (const sourceCwd of ["/work/a", cwd, "/work/a"]) {
			assertReferences(unclosed, [], sourceCwd);
			assertReferences("docs/b.md", ["docs/b.md"], sourceCwd);
			assertReferences(closed, ["docs/b.md"], sourceCwd);
			assertReferences("[external](https://example.test/x)", [], sourceCwd);
		}
		const first = renderMarkdownFileReferences(closed, "/work/a");
		const second = renderMarkdownFileReferences(closed, cwd);
		expect([...first.references.keys()]).not.toEqual([...second.references.keys()]);
		expect(renderMarkdownFileReferences(closed, "")).toEqual({ markdown: closed, references: new Map() });
	});
	it.each(["<https://example.test/x>", "<person@example.test>"])(
		"does not clear an open HTML anchor at an actual marked autolink, %s",
		(autolink) => {
			assertReferences(`Before <a href="https://example.test/x"> first\n\n ${autolink} docs/a.md </a> docs/b.md`, [
				"docs/b.md",
			]);
		},
	);
});

describe("table escape source-range ownership", () => {
	const trigger = "| File | Why |\n| --- | --- |\n| \\|docs/a.md a\\|b | docs/c.md |";
	function tableActions(result: ReturnType<typeof renderMarkdownFileReferences>) {
		const tables: Array<{ header: string[][]; rows: string[][][] }> = [];
		function links(tokens: ReturnType<typeof lexer>): string[] {
			return tokens.flatMap((token) => {
				if (token.type === "link") return [result.references.get(token.href)?.path ?? token.href];
				return "tokens" in token && token.tokens ? links(token.tokens as ReturnType<typeof lexer>) : [];
			});
		}
		function visit(tokens: ReturnType<typeof lexer>) {
			for (const token of tokens) {
				if (token.type === "table") {
					const table = token as import("marked").Tokens.Table;
					tables.push({
						header: table.header.map((cell) => links(cell.tokens as ReturnType<typeof lexer>)),
						rows: table.rows.map((row) => row.map((cell) => links(cell.tokens as ReturnType<typeof lexer>))),
					});
				} else if (token.type === "list") {
					for (const item of token.items) visit(item.tokens as ReturnType<typeof lexer>);
				} else if ("tokens" in token && token.tokens) visit(token.tokens as ReturnType<typeof lexer>);
			}
		}
		visit(lexer(result.markdown));
		return tables;
	}
	function assertTable(
		original: string,
		expectedPaths: string[],
		expectedMarkdown: string,
		expectedTable: { header: string[][]; rows: string[][][] },
		sourceCwd = cwd,
	) {
		const result = renderMarkdownFileReferences(original, sourceCwd);
		for (const references of [
			markdownFileReferences(original, sourceCwd).map(({ reference }) => reference),
			[...result.references.values()],
		]) {
			expect(references.map((reference) => reference.path)).toEqual(expectedPaths);
			expect(references.map((reference) => reference.cwd)).toEqual(expectedPaths.map(() => sourceCwd));
			expect(references.map((reference) => reference.readTarget)).toEqual(
				expectedPaths.map((path) => `${sourceCwd}/${path}`),
			);
		}
		let normalized = result.markdown;
		for (const [index, id] of [...result.references.keys()].entries())
			normalized = normalized.replace(id, `action-${index}`);
		expect(normalized).toBe(expectedMarkdown);
		expect(tableActions(result)).toEqual([expectedTable]);
	}

	it("R1 keeps the existing docs/c.md second-cell action for the exact two-pipe trigger", () => {
		const result = renderMarkdownFileReferences(trigger, cwd);
		expect(tableActions(result)[0]?.rows[0]?.[1]).toEqual(["docs/c.md"]);
	});
	it("I3 owns the exact leading escape inside the generated filename label", () => {
		assertTable(
			trigger,
			["|docs/a.md", "docs/c.md"],
			"| File | Why |\n| --- | --- |\n| [\\|docs/a.md](action-0) a\\|b | [docs/c.md](action-1) |",
			{ header: [[], []], rows: [[["|docs/a.md"], ["docs/c.md"]]] },
		);
	});
	it.each([
		["\\|docs/a.md", "[\\|docs/a.md](action-0)", "|docs/a.md"],
		["\\|docs/a\\|b.md", "[\\|docs/a\\|b.md](action-0)", "|docs/a|b.md"],
		["\\|docs/a.md\\|", "[\\|docs/a.md\\|](action-0)", "|docs/a.md|"],
		["docs/a.md\\|", "[docs/a.md\\|](action-0)", "docs/a.md|"],
		["(\\|docs/a.md)", "([\\|docs/a.md](action-0))", "|docs/a.md"],
		['"\\|docs/a.md:12."', '"[\\|docs/a.md](action-0):12."', "|docs/a.md"],
		["**\\|docs/a.md**", "**[\\|docs/a.md](action-0)**", "|docs/a.md"],
		["`\\|docs/a.md`", "[`\\|docs/a.md`](action-0)", "|docs/a.md"],
		["[\\|docs/a.md](./docs/z.md)", "[\\|docs/a.md](action-0)", "docs/z.md"],
		["[R](\\|docs/a.md)", "[R](action-0)", "|docs/a.md"],
	])("preserves independent leading/internal/trailing source bytes, %s", (cell, generated, path) => {
		assertTable(
			`|File|Why|\n|---|---|\n|${cell}|docs/c.md|`,
			[path, "docs/c.md"],
			`|File|Why|\n|---|---|\n|${generated}|[docs/c.md](action-1)|`,
			{ header: [[], []], rows: [[[path], ["docs/c.md"]]] },
		);
	});
	it.each(["\n", "\r\n"])("preserves header, repeated cells and multiple rows with %j", (newline) => {
		assertTable(
			[
				"| \\|docs/a.md | docs/c.md |",
				"| --- | --- |",
				"| \\|docs/a.md | \\|docs/a.md |",
				"| docs/d.md | x\\|y |",
				"",
			].join(newline),
			["|docs/a.md", "docs/c.md", "|docs/a.md", "|docs/a.md", "docs/d.md"],
			[
				"| [\\|docs/a.md](action-0) | [docs/c.md](action-1) |",
				"| --- | --- |",
				"| [\\|docs/a.md](action-2) | [\\|docs/a.md](action-3) |",
				"| [docs/d.md](action-4) | x\\|y |",
				"",
			].join(newline),
			{
				header: [["|docs/a.md"], ["docs/c.md"]],
				rows: [
					[["|docs/a.md"], ["|docs/a.md"]],
					[["docs/d.md"], []],
				],
			},
		);
	});
	it.each(["> ", "- ", "  "])("maps range ownership through nested prefixes, %j", (prefix) => {
		const continuation = prefix === "- " ? "  " : prefix;
		const original = `${prefix}| File | Why |\r\n${continuation}| --- | --- |\r\n${continuation}| \\|docs/a.md a\\|b | docs/c.md |\r\n`;
		const expected = `${prefix}| File | Why |\r\n${continuation}| --- | --- |\r\n${continuation}| [\\|docs/a.md](action-0) a\\|b | [docs/c.md](action-1) |\r\n`;
		assertTable(original, ["|docs/a.md", "docs/c.md"], expected, {
			header: [[], []],
			rows: [[["|docs/a.md"], ["docs/c.md"]]],
		});
	});
	it.each([1, 3, 5, 7])("preserves odd backslash parity %i without admitting backslash paths", (count) => {
		const slashes = "\\".repeat(count);
		const first = count === 1 ? "|docs/a.md" : undefined;
		assertTable(
			`| File | Why |\n| --- | --- |\n| ${slashes}|docs/a.md a\\|b | docs/c.md |`,
			first ? [first, "docs/c.md"] : ["docs/c.md"],
			`| File | Why |\n| --- | --- |\n| ${first ? "[\\|docs/a.md](action-0)" : `${slashes}|docs/a.md`} a\\|b | [docs/c.md](action-${first ? 1 : 0}) |`,
			{ header: [[], []], rows: [[first ? [first] : [], ["docs/c.md"]]] },
		);
	});
	it.each([0, 2, 4, 6])("preserves even backslash parity %i and actual column splitting", (count) => {
		const slashes = "\\".repeat(count);
		assertTable(
			`| One | Two | Three |\n| --- | --- | --- |\n| a${slashes}|docs/a.md | docs/c.md |`,
			["docs/a.md", "docs/c.md"],
			`| One | Two | Three |\n| --- | --- | --- |\n| a${slashes}|[docs/a.md](action-0) | [docs/c.md](action-1) |`,
			{ header: [[], [], []], rows: [[[], ["docs/a.md"], ["docs/c.md"]]] },
		);
	});
	it("keeps reference labels, definition bytes, escaped punctuation and excluded web anchors", () => {
		assertTable(
			'| File | Why |\n| --- | --- |\n| [\\|docs/a.md][r] | [\\|docs/c.md](https://example.test/x) |\n\n[r]: <./docs/z.md> "keep"\n',
			["docs/z.md"],
			'| File | Why |\n| --- | --- |\n| [\\|docs/a.md](action-0) | [\\|docs/c.md](https://example.test/x) |\n\n[r]: <./docs/z.md> "keep"\n',
			{ header: [[], []], rows: [[["docs/z.md"], ["https://example.test/x"]]] },
		);
	});
	it("retains HTML ancestry, invalid adjacency and fresh cwd bindings around escaped table paths", () => {
		const original =
			"Before <script> first\n\n| \\|docs/a.md | Why |\n| --- | --- |\n| </script> ../**docs/x.md** \\|docs/b.md | docs/c.md |";
		const expected =
			"Before <script> first\n\n| \\|docs/a.md | Why |\n| --- | --- |\n| </script> ../**docs/x.md** [\\|docs/b.md](action-0) | [docs/c.md](action-1) |";
		for (const sourceCwd of ["/work/a", cwd, "/work/a"]) {
			assertTable(
				original,
				["|docs/b.md", "docs/c.md"],
				expected,
				{ header: [[], []], rows: [[["|docs/b.md"], ["docs/c.md"]]] },
				sourceCwd,
			);
			assertTable(
				trigger,
				["|docs/a.md", "docs/c.md"],
				"| File | Why |\n| --- | --- |\n| [\\|docs/a.md](action-0) a\\|b | [docs/c.md](action-1) |",
				{ header: [[], []], rows: [[["|docs/a.md"], ["docs/c.md"]]] },
				sourceCwd,
			);
		}
		expect(renderMarkdownFileReferences(trigger, "")).toEqual({ markdown: trigger, references: new Map() });
	});
});
