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
// Undo generated ranges only, leaving authored HTML and surrounding bytes intact.
function restoreLiteralHTML(rendered: string, original: string): string {
	return rendered.replace(/\[([^\]]+)\]\(evener-file:[\d-]+\)/g, (_, label: string) => {
		const authored = original.match(new RegExp(`\\[${label}\\]\\(([^)]+)\\)`));
		return authored?.[0] ?? label;
	});
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

describe("native Markdown contexts inside literal HTML blocks", () => {
	const corpus = [
		{
			name: "reviewed file destination",
			markdown: "<div>\n[R](./docs/file.md)\n</div>",
			paths: ["docs/file.md"],
			surfaces: ["link"],
			rendered: "<div>\n[R](action-0)\n</div>",
		},
		{
			name: "reviewed fence",
			markdown: "<script>\n```sh\ndocs/a.md\n```\n</script>",
			paths: [],
			surfaces: [],
		},
		{
			name: "reviewed inline code",
			markdown: "<div>\n`docs/a.md`\n</div>",
			paths: ["docs/a.md"],
			surfaces: ["code"],
			rendered: "<div>\n[`docs/a.md`](action-0)\n</div>",
		},
		{
			name: "reviewed opaque external label",
			markdown: "<div>\n[docs/label.md](https://example.test/x)\n</div>",
			paths: [],
			surfaces: [],
		},
		{
			name: "reviewed indented paragraph continuation, not a code block",
			markdown: "<div>\n    docs/a.md\n</div>",
			paths: ["docs/a.md"],
			surfaces: ["prose"],
			rendered: "<div>\n    [docs/a.md](action-0)\n</div>",
		},
		{
			name: "actual blank-separated indented code",
			markdown: "<div>\n\n    docs/a.md\n\n</div>",
			paths: [],
			surfaces: [],
		},
		{
			name: "Mermaid fence",
			markdown: "<style>\n```mermaid\ndocs/a.md\n```\n</style>",
			paths: [],
			surfaces: [],
		},
		{
			name: "opaque file label and retained title and angle delimiters",
			markdown: '<pre>\n[docs/label.md `]`](<./docs/a%20b.md> "keep")\n</pre>',
			paths: ["docs/a b.md"],
			surfaces: ["link"],
			rendered: '<pre>\n[docs/label.md `]`](<action-0> "keep")\n</pre>',
		},
		{
			name: "reference definition inside the same block",
			markdown: '<script>\n[R][r]\n\n[r]: ./docs/a.md "keep"\n</script>',
			paths: ["docs/a.md"],
			surfaces: ["link"],
			rendered: '<script>\n[R](action-0)\n\n[r]: ./docs/a.md "keep"\n</script>',
		},
		{
			name: "forward reference outside the block",
			markdown: '<div>\n[R][r]\n</div>\n\n[r]: ./docs/a.md "keep"',
			paths: ["docs/a.md"],
			surfaces: ["link"],
			rendered: '<div>\n[R](action-0)\n</div>\n\n[r]: ./docs/a.md "keep"',
		},
		{
			name: "CRLF and blockquote offsets",
			markdown: "> <div>\r\n> [R](./docs/file.md)\r\n> </div>\r\n",
			paths: ["docs/file.md"],
			surfaces: ["link"],
			rendered: "> <div>\r\n> [R](action-0)\r\n> </div>\r\n",
		},
		{
			name: "list-contained fence",
			markdown: "- <script>\n  ```sh\n  docs/a.md\n  ```\n  </script>",
			paths: [],
			surfaces: [],
		},
		{
			name: "inline code command remains excluded",
			markdown: "<pre>\n`cat docs/a.md`\n</pre>",
			paths: [],
			surfaces: [],
		},
	];
	it.each(corpus)("preserves $name", ({ markdown, paths: expected, surfaces, rendered }) => {
		for (const sourceCwd of [cwd, "/work/a", cwd]) {
			const discovered = markdownFileReferences(markdown, sourceCwd);
			const result = renderMarkdownFileReferences(markdown, sourceCwd);
			expect(discovered.map(({ surface }) => surface)).toEqual(surfaces);
			for (const refs of [discovered.map(({ reference }) => reference), [...result.references.values()]]) {
				expect(refs.map((ref) => ref.path)).toEqual(expected);
				expect(refs.map((ref) => ref.cwd)).toEqual(expected.map(() => sourceCwd));
				expect(refs.map((ref) => ref.readTarget)).toEqual(expected.map((path) => `${sourceCwd}/${path}`));
			}
			let normalized = result.markdown;
			for (const [index, id] of [...result.references.keys()].entries())
				normalized = normalized.replace(id, `action-${index}`);
			expect(normalized).toBe(rendered ?? markdown);
		}
		expect(renderMarkdownFileReferences(markdown, "")).toEqual({ markdown, references: new Map() });
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
		['Before <a href="https://example.test/x"> docs/a.md </a> after', ["docs/a.md"]],
		['Before <a href="https://example.test/x"> `README.md` [R](./docs/a.md) </a> after', ["README.md", "docs/a.md"]],
		[
			'Before <a href="https://example.test/x"> **docs/a.md *`README.md`* [R](./docs/b.md)** </a> after',
			["docs/a.md", "README.md", "docs/b.md"],
		],
		['Before **<a href="https://example.test/x"> docs/a.md** `README.md` </a> after', ["docs/a.md", "README.md"]],
		["Before <script> docs/a.md `README.md` [R](./docs/b.md) </script> after", ["docs/a.md", "README.md", "docs/b.md"]],
		["Before <pre> docs/a.md </pre> after", ["docs/a.md"]],
	] as const)("literal authored HTML interiors follow displayed-text grammar, %s", (original, expected) => {
		expect(paths(original)).toEqual(expected);
		const result = renderMarkdownFileReferences(original, cwd);
		expect([...result.references.values()].map((ref) => ref.path)).toEqual(expected);
		expect(restoreLiteralHTML(result.markdown, original)).toBe(original);
	});
	it.each([
		['docs/b.md <a href="https://example.test/x"> docs/a.md </a>"docs/c.md"', ["docs/b.md", "docs/a.md", "docs/c.md"]],
		['docs/b.md <a href="https://example.test/x"> docs/a.md </a> docs/c.md', ["docs/b.md", "docs/a.md", "docs/c.md"]],
		["docs/b.md <script> docs/a.md </script> docs/c.md", ["docs/b.md", "docs/a.md", "docs/c.md"]],
	] as const)(
		"literal authored HTML keeps interior and surrounding eligible prose and original source, %s",
		(original, expected) => {
			const result = renderMarkdownFileReferences(original, cwd);
			expect(paths(original)).toEqual(expected);
			expect([...result.references.values()].map((ref) => ref.readTarget)).toEqual(
				expected.map((path) => `${cwd}/${path}`),
			);
			expect(restoreLiteralHTML(result.markdown, original)).toBe(original);
		},
	);
	it("I1 does not manufacture a prose boundary after an excluded HTML edge", () => {
		const original = 'docs/b.md <a href="https://example.test/x"> docs/a.md </a>docs/c.md';
		const result = renderMarkdownFileReferences(original, cwd);
		expect(paths(original)).toEqual(["docs/b.md", "docs/a.md"]);
		expect(restoreLiteralHTML(result.markdown, original)).toBe(original);
	});
	it("I1 does not create code boundaries at an excluded HTML edge", () => {
		const original = 'Before <a href="https://example.test/x"> docs/a.md </a>foo`README.md` after';
		expect(paths(original)).toEqual(["docs/a.md"]);
		expect(restoreLiteralHTML(renderMarkdownFileReferences(original, cwd).markdown, original)).toBe(original);
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

describe("literal HTML eligibility across marked inline collections", () => {
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
		expect(restoreLiteralHTML(result.markdown, original)).toBe(original);
	}

	it.each([
		['Before <a href="https://example.test/x"> first\n\n docs/a.md </a> after', ["docs/a.md"]],
		["Before <script> first\n\n docs/a.md </script> after", ["docs/a.md"]],
		["Before <pre> first\n\n docs/a.md </pre> after", ["docs/a.md"]],
	] as const)("recognizes the approved cross-paragraph literal interior, %s", (original, expected) => {
		assertReferences(original, [...expected]);
	});
	it("keeps eligible interior and outside prose in the script-shaped case", () => {
		assertReferences("Before <script> first\n\n docs/a.md </script> docs/b.md", ["docs/a.md", "docs/b.md"]);
	});
	it.each([
		['Before <a href="https://example.test/x"> first\r\n\r\n docs/a.md </a> docs/b.md\r\n', ["docs/a.md", "docs/b.md"]],
		["Before <script> first\r\n\r\n docs/a.md </script> docs/b.md\r\n", ["docs/a.md", "docs/b.md"]],
		['Before **<a href="https://example.test/x"> first**\n\n *docs/a.md </a>* docs/b.md', ["docs/a.md", "docs/b.md"]],
		['> Before <a href="https://example.test/x"> first\n>\n> docs/a.md </a> docs/b.md', ["docs/a.md", "docs/b.md"]],
		['- Before <a href="https://example.test/x"> first\n\n  docs/a.md </a> docs/b.md', ["docs/a.md", "docs/b.md"]],
		['- Before <a href="https://example.test/x"> first\n- docs/a.md </a> docs/b.md', ["docs/a.md", "docs/b.md"]],
		['Before <a href="https://example.test/x"> first\n\n# docs/a.md </a> docs/b.md', ["docs/a.md", "docs/b.md"]],
		['Before <a href="https://example.test/x"> first\n\n> docs/a.md </a> docs/b.md', ["docs/a.md", "docs/b.md"]],
		['> Before <a href="https://example.test/x"> first\n\n docs/a.md </a> docs/b.md', ["docs/a.md", "docs/b.md"]],
		["Before <script> first\n\n| docs/a.md </script> docs/b.md | after |\n| --- | --- |", ["docs/a.md", "docs/b.md"]],
		[
			"Before <script> first\n\n# docs/a.md\n\n<pre>docs/a.md</pre>\n\n docs/a.md </script> docs/b.md",
			["docs/a.md", "docs/a.md", "docs/b.md"],
		],
		[
			"Before <script> first\n\n```sh\n</script> docs/a.md\n```\n\n docs/a.md </script> docs/b.md",
			["docs/a.md", "docs/b.md"],
		],
	] as const)("retains literal eligibility and original bytes across collections, %s", (original, expected) => {
		assertReferences(original, [...expected]);
	});
	it.each([
		[
			'| Before <a href="https://example.test/x"> first | docs/a.md </a> docs/b.md |\n| --- | --- |\n| docs/c.md | after |',
			["docs/a.md", "docs/b.md", "docs/c.md"],
		],
		[
			"| Before <script> first | docs/a.md |\n| --- | --- |\n| docs/a.md </script> docs/b.md | docs/c.md |",
			["docs/a.md", "docs/a.md", "docs/b.md", "docs/c.md"],
		],
		[
			"> | Before <pre> first | docs/a.md |\r\n> | --- | --- |\r\n> | docs/a.md </pre> docs/b.md | docs/c.md |\r\n",
			["docs/a.md", "docs/a.md", "docs/b.md", "docs/c.md"],
		],
	] as const)(
		"recognizes literal text across table cells without joining their boundaries, %s",
		(original, expected) => {
			assertReferences(original, [...expected]);
		},
	);
	it.each([
		[
			'Before <a href="https://example.test/x"> first\n\n docs/a.md `README.md` [R](./docs/a.md)',
			["docs/a.md", "README.md", "docs/a.md"],
		],
		["Before <script> first\n\n docs/a.md `README.md` [R](./docs/a.md)", ["docs/a.md", "README.md", "docs/a.md"]],
		["Before <pre> first\n\n# docs/a.md\n\n docs/a.md", ["docs/a.md", "docs/a.md"]],
	] as const)("recognizes eligible literal text after unclosed tags, %s", (original, expected) => {
		assertReferences(original, [...expected]);
	});
	it.each([
		["Before <script> first\n\n docs/a.md </script> ../**docs/a.md** docs/b.md", ["docs/a.md", "docs/b.md"]],
		["Before <pre> first\n\n docs/a.md </pre> https://host/**docs/a.md** docs/b.md", ["docs/a.md", "docs/b.md"]],
		[
			'Before <a href="https://example.test/x"> first\n\n docs/a.md </a>foo`README.md` docs/b.md',
			["docs/a.md", "docs/b.md"],
		],
	] as const)("retains full-block invalid-boundary context after closing tags, %s", (original, expected) => {
		assertReferences(original, [...expected]);
	});
	it("does not join invalid boundary text from different paragraphs or cells", () => {
		assertReferences("../\n\n docs/b.md\n\n| ../ | docs/c.md |\n| --- | --- |", ["docs/b.md", "docs/c.md"]);
	});
	it.each([
		["[Before <script> first](https://example.test/x)\n\n docs/a.md </script> docs/b.md", ["docs/a.md", "docs/b.md"]],
		["![Before <script> first](./image.png)\n\n docs/a.md </script> docs/b.md", ["docs/a.md", "docs/b.md"]],
		["[Before **<script> first**](https://example.test/x) docs/a.md </script> docs/b.md", ["docs/a.md", "docs/b.md"]],
	] as const)("does not let literal tags inside actual labels suppress following prose, %s", (original, expected) => {
		assertReferences(original, [...expected]);
	});
	it("recognizes an actual file destination inside literal authored tags", () => {
		assertReferences('Before <a href="https://example.test/x"> first\n\n[inner](./docs/a.md) docs/b.md', [
			"docs/a.md",
			"docs/b.md",
		]);
	});
	it("does not leak literal eligibility or cwd through separate messages, render calls or cached candidates", () => {
		const unclosed = 'Before <a href="https://example.test/x"> first\n\n docs/a.md';
		const closed = "Before <script> first\n\n docs/a.md </script> docs/b.md";
		for (const sourceCwd of ["/work/a", cwd, "/work/a"]) {
			assertReferences(unclosed, ["docs/a.md"], sourceCwd);
			assertReferences("docs/b.md", ["docs/b.md"], sourceCwd);
			assertReferences(closed, ["docs/a.md", "docs/b.md"], sourceCwd);
			assertReferences("[external](https://example.test/x)", [], sourceCwd);
		}
		const first = renderMarkdownFileReferences(closed, "/work/a");
		const second = renderMarkdownFileReferences(closed, cwd);
		expect([...first.references.keys()]).not.toEqual([...second.references.keys()]);
		expect(renderMarkdownFileReferences(closed, "")).toEqual({ markdown: closed, references: new Map() });
	});
	it.each(["<https://example.test/x>", "<person@example.test>"])(
		"keeps actual autolinks excluded while following literal prose stays eligible, %s",
		(autolink) => {
			assertReferences(`Before <a href="https://example.test/x"> first\n\n ${autolink} docs/a.md </a> docs/b.md`, [
				"docs/a.md",
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
	it("retains literal HTML eligibility, invalid adjacency and fresh cwd bindings around escaped table paths", () => {
		const original =
			"Before <script> first\n\n| \\|docs/a.md | Why |\n| --- | --- |\n| </script> ../**docs/x.md** \\|docs/b.md | docs/c.md |";
		const expected =
			"Before <script> first\n\n| [\\|docs/a.md](action-0) | Why |\n| --- | --- |\n| </script> ../**docs/x.md** [\\|docs/b.md](action-1) | [docs/c.md](action-2) |";
		for (const sourceCwd of ["/work/a", cwd, "/work/a"]) {
			assertTable(
				original,
				["|docs/a.md", "|docs/b.md", "docs/c.md"],
				expected,
				{ header: [["|docs/a.md"], []], rows: [[["|docs/b.md"], ["docs/c.md"]]] },
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

describe("approved authored HTML literal text", () => {
	const corpus = [
		{
			name: "standalone-a",
			markdown: '<a href="https://example.test/x">\n\ndocs/private.md\n\n</a>',
			paths: ["docs/private.md"],
		},
		{
			name: "inline-a-paragraphs",
			markdown: 'Before <a href="https://example.test/x"> first\n\n docs/a.md </a> after',
			paths: ["docs/a.md"],
		},
		{
			name: "inline-a",
			markdown: "Before <a> docs/a.md </a> after",
			paths: ["docs/a.md"],
		},
		{
			name: "block-a",
			markdown: "<a>\n docs/a.md\n</a>",
			paths: ["docs/a.md"],
		},
		{
			name: "inline-div",
			markdown: "Before <div> docs/a.md </div> after",
			paths: ["docs/a.md"],
		},
		{
			name: "block-div",
			markdown: "<div>\n docs/a.md\n</div>",
			paths: ["docs/a.md"],
		},
		{
			name: "inline-span",
			markdown: "Before <span> docs/a.md </span> after",
			paths: ["docs/a.md"],
		},
		{
			name: "block-span",
			markdown: "<span>\n docs/a.md\n</span>",
			paths: ["docs/a.md"],
		},
		{
			name: "inline-script",
			markdown: "Before <script> docs/a.md </script> after",
			paths: ["docs/a.md"],
		},
		{
			name: "block-script",
			markdown: "<script>\n docs/a.md\n</script>",
			paths: ["docs/a.md"],
		},
		{
			name: "inline-style",
			markdown: "Before <style> docs/a.md </style> after",
			paths: ["docs/a.md"],
		},
		{
			name: "block-style",
			markdown: "<style>\n docs/a.md\n</style>",
			paths: ["docs/a.md"],
		},
		{
			name: "inline-pre",
			markdown: "Before <pre> docs/a.md </pre> after",
			paths: ["docs/a.md"],
		},
		{
			name: "block-pre",
			markdown: "<pre>\n docs/a.md\n</pre>",
			paths: ["docs/a.md"],
		},
		{
			name: "inline-code",
			markdown: "Before <code> docs/a.md </code> after",
			paths: ["docs/a.md"],
		},
		{
			name: "block-code",
			markdown: "<code>\n docs/a.md\n</code>",
			paths: ["docs/a.md"],
		},
		{
			name: "comment-inline",
			markdown: "Before <!-- docs/a.md --> after",
			paths: ["docs/a.md"],
		},
		{
			name: "comment-block",
			markdown: "<!--\ndocs/a.md\n-->",
			paths: ["docs/a.md"],
		},
		{
			name: "unclosed-script",
			markdown: "Before <script> first\n\n docs/a.md",
			paths: ["docs/a.md"],
		},
		{
			name: "unclosed-a",
			markdown: 'Before <a href="https://example.test/x"> first\n\n docs/a.md',
			paths: ["docs/a.md"],
		},
		{
			name: "malformed-tag",
			markdown: "Before <a href= docs/a.md after",
			paths: ["docs/a.md"],
		},
		{
			name: "table",
			markdown: "| Before <script> first | docs/a.md |\n| --- | --- |\n| docs/b.md </script> | docs/c.md |",
			paths: ["docs/a.md", "docs/b.md", "docs/c.md"],
		},
		{
			name: "list",
			markdown: '- Before <a href="https://example.test/x"> first\n- docs/a.md </a> docs/b.md',
			paths: ["docs/a.md", "docs/b.md"],
		},
		{
			name: "blockquote-crlf",
			markdown: "> Before <pre> first\r\n>\r\n> docs/a.md </pre> docs/b.md\r\n",
			paths: ["docs/a.md", "docs/b.md"],
		},
		{
			name: "quoted-attribute",
			markdown:
				'Before <a href="https://example.test/docs/url.md" data-file="docs/attribute.md"> docs/body.md </a> after',
			paths: ["docs/attribute.md", "docs/body.md"],
		},
		{
			name: "unsafe-attributes",
			markdown:
				'Before <a href="https://example.test/docs/url.md" data-file="../docs/no.md" data-other="mailto:a/docs/no.md"> docs/body.md </a> after',
			paths: ["docs/body.md"],
		},
		{
			name: "tag-adjacent",
			markdown: "Before <code>docs/a.md</code> after",
			paths: [],
		},
		{
			name: "closing-boundary",
			markdown: "Before <a> docs/a.md </a>docs/b.md",
			paths: ["docs/a.md"],
		},
		{
			name: "invalid-boundaries",
			markdown: "Before <script> ../docs/a.md https://host/docs/b.md </script> ../**docs/c.md**",
			paths: [],
		},
		{
			name: "actual-links",
			markdown: "Before <a> [docs/label.md](https://example.test/x) [R](./docs/file.md) </a> after",
			paths: ["docs/file.md"],
		},
		{
			name: "inline-code",
			markdown: "Before <a> `README.md` </a> after",
			paths: ["README.md"],
		},
		{
			name: "fences",
			markdown: "<a>\n\n```sh\ndocs/no.md\n```\n\n```mermaid\ndocs/no.md\n```\n\n docs/yes.md </a>",
			paths: ["docs/yes.md"],
		},
		{
			name: "literal-entity-percent",
			markdown: "Before <!-- docs/a&amp;b.md docs/100%25.md -->",
			paths: ["docs/a&b.md", "docs/100%25.md"],
		},
	];
	it.each(corpus)("discovers and renders $name with original offsets", ({ name, markdown, paths: expected }) => {
		expect(paths(markdown)).toEqual(expected);
		const result = renderMarkdownFileReferences(markdown, cwd);
		expect([...result.references.values()].map((ref) => ref.path)).toEqual(expected);
		expect([...result.references.values()].map((ref) => ref.readTarget)).toEqual(
			expected.map((path) => `${cwd}/${path}`),
		);
		expect(
			result.markdown.replace(/\[([^\]]+)\]\(evener-file:[\d-]+\)/g, (_, label: string) =>
				label === "R"
					? "[R](./docs/file.md)"
					: name === "literal-entity-percent" && label === "docs/a\\&b.md"
						? "docs/a&amp;b.md"
						: label,
			),
		).toBe(markdown);
		const next = renderMarkdownFileReferences(markdown, "/work/a");
		expect([...next.references.values()].map((ref) => ref.readTarget)).toEqual(
			expected.map((path) => `/work/a/${path}`),
		);
		if (expected.length) expect([...next.references.keys()]).not.toEqual([...result.references.keys()]);
		expect(renderMarkdownFileReferences(markdown, "")).toEqual({ markdown, references: new Map() });
	});
});

describe("resolved native prose filename entities", () => {
	const cases: Array<{ markdown: string; paths: string[] }> = [
		{ markdown: "docs/a&amp;b.md", paths: ["docs/a&b.md"] },
		{ markdown: "Before <!-- docs/a&amp;b.md --> after", paths: ["docs/a&b.md"] },
		{
			markdown: "docs/&eacute;.md docs/a&#38;b.md docs/a&#x26;b.md",
			paths: ["docs/é.md", "docs/a&b.md", "docs/a&b.md"],
		},
		{ markdown: "docs/&#x1F600;.md docs/&NotEqualTilde;.md", paths: ["docs/😀.md", "docs/≂̸.md"] },
		{ markdown: String.raw`docs/a\\&amp;b.md`, paths: [] },
		{ markdown: String.raw`docs/a\&amp;b.md`, paths: ["docs/a&amp;b.md"] },
		{
			markdown: String.raw`docs/&unknown;.md docs/&constructor;.md docs/&toString;.md docs/&\_\_proto\_\_;.md`,
			paths: ["docs/&unknown;.md", "docs/&constructor;.md", "docs/&toString;.md", "docs/&__proto__;.md"],
		},
		{
			markdown: "docs/a&amp;lt;b.md docs/100&percnt;25.md docs/100%25.md",
			paths: ["docs/a&lt;b.md", "docs/100%25.md", "docs/100%25.md"],
		},
		{ markdown: "docs/a&lt;b.md docs/a&bsol;b.md ../**docs/a&amp;b.md** https://host/**docs/a&amp;b.md**", paths: [] },
		{
			markdown: "&quot;docs/a&amp;b.md&quot; docs/a.md&Tab;docs/b.md &#46;&#46;/docs/no.md",
			paths: ["docs/a&b.md", "docs/a.md", "docs/b.md"],
		},
		{
			markdown: "`docs/a&amp;b.md` [R](./docs/a&amp;b.md) [E](./docs/a\\&amp;b.md)",
			paths: ["docs/a&amp;b.md", "docs/a&b.md", "docs/a&amp;b.md"],
		},
		{
			markdown: '[R](<./docs/a&amp;b.md> "keep") [D][d]\n\n[d]: ./docs/a\\&amp;b.md "title"',
			paths: ["docs/a&b.md", "docs/a&amp;b.md"],
		},
		{ markdown: "> - **docs/a&amp;b.md**\r\n>   - docs/&#x1F600;.md\r\n", paths: ["docs/a&b.md", "docs/😀.md"] },
		{ markdown: "A | B\n--- | ---\n\\|docs/a&amp;b.md | docs/&eacute;.md", paths: ["|docs/a&b.md", "docs/é.md"] },
		{
			markdown:
				"[docs/a&amp;b.md](https://example.test/x) ![docs/a&amp;b.md](./image.png)\n\n```mermaid\ndocs/a&amp;b.md\n```\n\n```sh\ndocs/a&amp;b.md\n```",
			paths: [],
		},
		{
			markdown:
				"docs/a&lowbar;&lowbar;b&lowbar;&lowbar;.md docs/a&dollar;b&dollar;.md docs/a&vert;b.md docs/a&ast;b&ast;.md docs/a&grave;b&grave;.md docs/a&#126;b&#126;.md",
			paths: ["docs/a__b__.md", "docs/a$b$.md", "docs/a|b.md", "docs/a*b*.md", "docs/a`b`.md", "docs/a~b~.md"],
		},
		{
			markdown: "docs&sol;a.md &lpar;docs/a&amp;b.md&rpar; docs/&#0;.md docs/&#xD800;.md docs/&#x110000;.md",
			paths: ["docs/a.md", "docs/a&b.md", "docs/�.md", "docs/�.md", "docs/�.md"],
		},
		{ markdown: "docs/**a&amp;b.md** docs/`a&amp;b.md` ../**docs/a&amp;b.md**", paths: [] },
	];
	it.each(cases)(
		"binds resolved meaning without changing surface controls, $markdown",
		({ markdown, paths: expected }) => {
			expect(paths(markdown)).toEqual(expected);
			const generations = new Set<string>();
			for (const owner of ["/work/tree", "/work/other", "/work/tree"]) {
				const result = renderMarkdownFileReferences(markdown, owner);
				expect([...result.references.values()].map((ref) => ref.path)).toEqual(expected);
				expect([...result.references.values()].map((ref) => ref.readTarget)).toEqual(
					expected.map((path) => `${owner}/${path}`),
				);
				expect([...result.references.values()].map((ref) => ref.cwd)).toEqual(expected.map(() => owner));
				for (const id of result.references.keys()) {
					expect(generations.has(id)).toBe(false);
					generations.add(id);
					expect(result.markdown.split(id)).toHaveLength(2);
				}
			}
			expect(markdownFileReferences(markdown, "")).toEqual([]);
			expect(renderMarkdownFileReferences(markdown, "")).toEqual({ markdown, references: new Map() });
		},
	);
	it("keeps exact bytes outside shrinking and expanding entity replacements", () => {
		const markdown = "lead &eacute;: docs/a&amp;b.md, docs/&#x1F600;.md:12. docs/&NotEqualTilde;.md! tail &amp;\r\n";
		const result = renderMarkdownFileReferences(markdown, "/work/tree");
		const ids = [...result.references.keys()];
		expect(result.markdown).toBe(
			`lead &eacute;: [docs/a\\&b.md](${ids[0]}), [docs/😀.md](${ids[1]}):12. [docs/≂̸.md](${ids[2]})! tail &amp;\r\n`,
		);
		expect([...result.references.values()].map((ref) => ref.path)).toEqual(["docs/a&b.md", "docs/😀.md", "docs/≂̸.md"]);
	});
});
