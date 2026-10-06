import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { markdownFileReferences, renderMarkdownFileReferences } from "../src/reader/markdownFileReferences";

const native = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const installed = path.join(native, "node_modules/react-native-enriched-markdown");
const pkg = JSON.parse(readFileSync(path.join(installed, "package.json"), "utf8"));
const lock = JSON.parse(readFileSync(path.join(native, "package-lock.json"), "utf8"));
assert.equal(pkg.version, "1.0.2");
assert.equal(lock.packages["node_modules/react-native-enriched-markdown"].version, pkg.version);
const marked = JSON.parse(readFileSync(path.join(native, "node_modules/marked/package.json"), "utf8"));
assert.equal(marked.version, "18.0.6");
assert.equal(lock.packages["node_modules/marked"].version, marked.version);
const scratch = mkdtempSync(path.join(process.env.EVENER_SCRATCH_DIR ?? tmpdir(), "markdown-file-links-"));
const commands: unknown[] = [];
function run(command: string, args: string[], input?: string): string {
	console.log(JSON.stringify({ cwd: native, command, args }));
	const result = spawnSync(command, args, { cwd: native, input, encoding: "utf8" });
	commands.push({
		command,
		args,
		cwd: native,
		input,
		stdout: result.stdout,
		stderr: result.stderr,
		status: result.status,
		signal: result.signal,
		error: result.error?.message,
	});
	writeFileSync(path.join(scratch, "commands.json"), JSON.stringify(commands, null, 2));
	console.log(result.stdout);
	console.error(result.stderr);
	console.log(`exit=${result.status}`);
	if (result.error) throw result.error;
	assert.equal(result.status, 0, `${command} failed, artifacts ${scratch}`);
	return result.stdout;
}
const cpp = path.join(installed, "cpp");
const object = path.join(scratch, "md4c.o");
const executable = path.join(scratch, "probe");
run("cc", ["--version"]);
run("c++", ["--version"]);
run("cc", ["-c", path.join(cpp, "md4c/md4c.c"), "-o", object]);
run("c++", [
	"-std=c++17",
	"-I",
	path.join(cpp, "parser"),
	path.join(native, "scripts/markdown-file-links-probe.cpp"),
	path.join(cpp, "parser/MD4CParser.cpp"),
	object,
	"-o",
	executable,
]);
const largeAdjoiningProse = String.raw`docs/a.md \* ` + "word ".repeat(30000);
const corpus: Array<{
	markdown: string;
	path?: string;
	paths?: string[];
	name?: string;
	externalDestinations?: string[];
	rendered?: string;
	webPaths?: string[];
	labels?: string[];
	externalLabels?: string[];
}> = [
	{ markdown: "[R](./docs/a&amp;b.md)", path: "docs/a&b.md" },
	{ markdown: "[R](./docs/a%26b.md)", path: "docs/a&b.md" },
	{ markdown: "[R](./docs/a%3A12%23L4%3F.md)", path: "docs/a:12#L4?.md" },
	{ markdown: "`reports/dlg_02wMz5TxvEMoJEDTDGOTil.md`", path: "reports/dlg_02wMz5TxvEMoJEDTDGOTil.md" },
	{ markdown: "[R](./docs/a\\!b.md)", path: "docs/a!b.md" },
	{ markdown: "[R][file]\n\n[file]: ./docs/a%20b.md", path: "docs/a b.md" },
	{ markdown: "`docs/a&amp;b.md`", path: "docs/a&amp;b.md" },
	{ markdown: "docs/100%25.md", path: "docs/100%25.md" },
	{ markdown: "../**docs/a.md**", path: undefined },
	{ markdown: "`docs/[a].md`", path: undefined },
	{ markdown: "[foo `]` bar](./docs/a.md)", path: "docs/a.md" },
	{ markdown: "[foo `]` bar][r]\n\n[r]: ./docs/a.md", path: "docs/a.md" },
	{ markdown: '<a href="https://example.test/x">\n\ndocs/private.md\n\n</a>', path: "docs/private.md" },
	{ markdown: "A | B\n--- | ---\n\\|docs/a.md | no action", path: "|docs/a.md" },
	{ markdown: "A | B\n--- | ---\nno action | docs/b.md", path: "docs/b.md" },
	{ markdown: 'Before <a href="https://example.test/x"> first\n\n docs/a.md </a> after', path: "docs/a.md" },
];
corpus.push(
	...[
		{
			markdown: '<a href="https://example.test/x">\n\ndocs/private.md\n\n</a>',
			path: "docs/private.md",
			paths: ["docs/private.md"],
			name: "standalone-a",
		},
		{
			markdown: 'Before <a href="https://example.test/x"> first\n\n docs/a.md </a> after',
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "inline-a-paragraphs",
		},
		{
			markdown: "Before <a> docs/a.md </a> after",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "inline-a",
		},
		{
			markdown: "<a>\n docs/a.md\n</a>",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "block-a",
		},
		{
			markdown: "Before <div> docs/a.md </div> after",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "inline-div",
		},
		{
			markdown: "<div>\n docs/a.md\n</div>",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "block-div",
		},
		{
			markdown: "Before <span> docs/a.md </span> after",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "inline-span",
		},
		{
			markdown: "<span>\n docs/a.md\n</span>",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "block-span",
		},
		{
			markdown: "Before <script> docs/a.md </script> after",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "inline-script",
		},
		{
			markdown: "<script>\n docs/a.md\n</script>",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "block-script",
		},
		{
			markdown: "Before <style> docs/a.md </style> after",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "inline-style",
		},
		{
			markdown: "<style>\n docs/a.md\n</style>",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "block-style",
		},
		{
			markdown: "Before <pre> docs/a.md </pre> after",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "inline-pre",
		},
		{
			markdown: "<pre>\n docs/a.md\n</pre>",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "block-pre",
		},
		{
			markdown: "Before <code> docs/a.md </code> after",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "inline-code",
		},
		{
			markdown: "<code>\n docs/a.md\n</code>",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "block-code",
		},
		{
			markdown: "Before <!-- docs/a.md --> after",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "comment-inline",
		},
		{
			markdown: "<!--\ndocs/a.md\n-->",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "comment-block",
		},
		{
			markdown: "Before <script> first\n\n docs/a.md",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "unclosed-script",
		},
		{
			markdown: 'Before <a href="https://example.test/x"> first\n\n docs/a.md',
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "unclosed-a",
		},
		{
			markdown: "Before <a href= docs/a.md after",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "malformed-tag",
		},
		{
			markdown: "| Before <script> first | docs/a.md |\n| --- | --- |\n| docs/b.md </script> | docs/c.md |",
			paths: ["docs/a.md", "docs/b.md", "docs/c.md"],
			name: "table",
		},
		{
			markdown: '- Before <a href="https://example.test/x"> first\n- docs/a.md </a> docs/b.md',
			paths: ["docs/a.md", "docs/b.md"],
			name: "list",
		},
		{
			markdown: "> Before <pre> first\r\n>\r\n> docs/a.md </pre> docs/b.md\r\n",
			paths: ["docs/a.md", "docs/b.md"],
			name: "blockquote-crlf",
		},
		{
			markdown:
				'Before <a href="https://example.test/docs/url.md" data-file="docs/attribute.md"> docs/body.md </a> after',
			paths: ["docs/attribute.md", "docs/body.md"],
			name: "quoted-attribute",
		},
		{
			markdown:
				'Before <a href="https://example.test/docs/url.md" data-file="../docs/no.md" data-other="mailto:a/docs/no.md"> docs/body.md </a> after',
			path: "docs/body.md",
			paths: ["docs/body.md"],
			name: "unsafe-attributes",
		},
		{
			markdown: "Before <code>docs/a.md</code> after",
			paths: [],
			name: "tag-adjacent",
		},
		{
			markdown: "Before <a> docs/a.md </a>docs/b.md",
			path: "docs/a.md",
			paths: ["docs/a.md"],
			name: "closing-boundary",
		},
		{
			markdown: "Before <script> ../docs/a.md https://host/docs/b.md </script> ../**docs/c.md**",
			paths: [],
			name: "invalid-boundaries",
		},
		{
			markdown: "Before <a> [docs/label.md](https://example.test/x) [R](./docs/file.md) </a> after",
			path: "docs/file.md",
			paths: ["docs/file.md"],
			name: "actual-links",
		},
		{
			markdown: "Before <a> `README.md` </a> after",
			path: "README.md",
			paths: ["README.md"],
			name: "inline-code",
		},
		{
			markdown: "<a>\n\n```sh\ndocs/no.md\n```\n\n```mermaid\ndocs/no.md\n```\n\n docs/yes.md </a>",
			path: "docs/yes.md",
			paths: ["docs/yes.md"],
			name: "fences",
		},
		{
			markdown: "Before <!-- docs/a&amp;b.md docs/100%25.md -->",
			paths: ["docs/a&b.md", "docs/100%25.md"],
			labels: ["docs/a&b.md", "docs/100%25.md"],
			name: "literal-entity-percent",
		},
	],
);
corpus.push(
	...[
		{
			markdown:
				"```mermaid\ndocs/a.md\n```\n\n```sh\ndocs/b.md\n```\n\n    docs/c.md\n\n<div>docs/d.md</div>\n\n[web](https://example.test/docs/e.md)",
			paths: [],
			name: "historical-0",
		},
		{
			markdown: 'Before <a href="https://example.test/x"> docs/a.md </a> after',
			paths: ["docs/a.md"],
			name: "historical-1",
		},
		{
			markdown: 'Before <a href="https://example.test/x"> `README.md` [R](./docs/a.md) </a> after',
			paths: ["README.md", "docs/a.md"],
			name: "historical-2",
		},
		{
			markdown: 'Before <a href="https://example.test/x"> **docs/a.md *`README.md`* [R](./docs/b.md)** </a> after',
			paths: ["docs/a.md", "README.md", "docs/b.md"],
			name: "historical-3",
		},
		{
			markdown: 'Before **<a href="https://example.test/x"> docs/a.md** `README.md` </a> after',
			paths: ["docs/a.md", "README.md"],
			name: "historical-4",
		},
		{
			markdown: "Before <script> docs/a.md `README.md` [R](./docs/b.md) </script> after",
			paths: ["docs/a.md", "README.md", "docs/b.md"],
			name: "historical-5",
		},
		{
			markdown: "Before <pre> docs/a.md </pre> after",
			paths: ["docs/a.md"],
			name: "historical-6",
		},
		{
			markdown: 'docs/b.md <a href="https://example.test/x"> docs/a.md </a>"docs/c.md"',
			paths: ["docs/b.md", "docs/a.md", "docs/c.md"],
			name: "historical-7",
		},
		{
			markdown: 'docs/b.md <a href="https://example.test/x"> docs/a.md </a> docs/c.md',
			paths: ["docs/b.md", "docs/a.md", "docs/c.md"],
			name: "historical-8",
		},
		{
			markdown: "docs/b.md <script> docs/a.md </script> docs/c.md",
			paths: ["docs/b.md", "docs/a.md", "docs/c.md"],
			name: "historical-9",
		},
		{
			markdown: 'docs/b.md <a href="https://example.test/x"> docs/a.md </a>docs/c.md',
			paths: ["docs/b.md", "docs/a.md"],
			name: "historical-10",
		},
		{
			markdown: 'Before <a href="https://example.test/x"> docs/a.md </a>foo`README.md` after',
			paths: ["docs/a.md"],
			name: "historical-11",
		},
		{
			markdown: 'Before <a href="https://example.test/x"> first\n\n docs/a.md </a> after',
			paths: ["docs/a.md"],
			name: "historical-12",
		},
		{
			markdown: "Before <script> first\n\n docs/a.md </script> after",
			paths: ["docs/a.md"],
			name: "historical-13",
		},
		{
			markdown: "Before <pre> first\n\n docs/a.md </pre> after",
			paths: ["docs/a.md"],
			name: "historical-14",
		},
		{
			markdown: "Before <script> first\n\n docs/a.md </script> docs/b.md",
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-15",
		},
		{
			markdown: 'Before <a href="https://example.test/x"> first\r\n\r\n docs/a.md </a> docs/b.md\r\n',
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-16",
		},
		{
			markdown: "Before <script> first\r\n\r\n docs/a.md </script> docs/b.md\r\n",
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-17",
		},
		{
			markdown: 'Before **<a href="https://example.test/x"> first**\n\n *docs/a.md </a>* docs/b.md',
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-18",
		},
		{
			markdown: '> Before <a href="https://example.test/x"> first\n>\n> docs/a.md </a> docs/b.md',
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-19",
		},
		{
			markdown: '- Before <a href="https://example.test/x"> first\n\n  docs/a.md </a> docs/b.md',
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-20",
		},
		{
			markdown: '- Before <a href="https://example.test/x"> first\n- docs/a.md </a> docs/b.md',
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-21",
		},
		{
			markdown: 'Before <a href="https://example.test/x"> first\n\n# docs/a.md </a> docs/b.md',
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-22",
		},
		{
			markdown: 'Before <a href="https://example.test/x"> first\n\n> docs/a.md </a> docs/b.md',
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-23",
		},
		{
			markdown: '> Before <a href="https://example.test/x"> first\n\n docs/a.md </a> docs/b.md',
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-24",
		},
		{
			markdown: "Before <script> first\n\n| docs/a.md </script> docs/b.md | after |\n| --- | --- |",
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-25",
		},
		{
			markdown: "Before <script> first\n\n# docs/a.md\n\n<pre>docs/a.md</pre>\n\n docs/a.md </script> docs/b.md",
			paths: ["docs/a.md", "docs/a.md", "docs/b.md"],
			name: "historical-26",
		},
		{
			markdown: "Before <script> first\n\n```sh\n</script> docs/a.md\n```\n\n docs/a.md </script> docs/b.md",
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-27",
		},
		{
			markdown:
				'| Before <a href="https://example.test/x"> first | docs/a.md </a> docs/b.md |\n| --- | --- |\n| docs/c.md | after |',
			paths: ["docs/a.md", "docs/b.md", "docs/c.md"],
			name: "historical-28",
		},
		{
			markdown: "| Before <script> first | docs/a.md |\n| --- | --- |\n| docs/a.md </script> docs/b.md | docs/c.md |",
			paths: ["docs/a.md", "docs/a.md", "docs/b.md", "docs/c.md"],
			name: "historical-29",
		},
		{
			markdown:
				"> | Before <pre> first | docs/a.md |\r\n> | --- | --- |\r\n> | docs/a.md </pre> docs/b.md | docs/c.md |\r\n",
			paths: ["docs/a.md", "docs/a.md", "docs/b.md", "docs/c.md"],
			name: "historical-30",
		},
		{
			markdown: 'Before <a href="https://example.test/x"> first\n\n docs/a.md `README.md` [R](./docs/a.md)',
			paths: ["docs/a.md", "README.md", "docs/a.md"],
			name: "historical-31",
		},
		{
			markdown: "Before <script> first\n\n docs/a.md `README.md` [R](./docs/a.md)",
			paths: ["docs/a.md", "README.md", "docs/a.md"],
			name: "historical-32",
		},
		{
			markdown: "Before <pre> first\n\n# docs/a.md\n\n docs/a.md",
			paths: ["docs/a.md", "docs/a.md"],
			name: "historical-33",
		},
		{
			markdown: "Before <script> first\n\n docs/a.md </script> ../**docs/a.md** docs/b.md",
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-34",
		},
		{
			markdown: "Before <pre> first\n\n docs/a.md </pre> https://host/**docs/a.md** docs/b.md",
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-35",
		},
		{
			markdown: 'Before <a href="https://example.test/x"> first\n\n docs/a.md </a>foo`README.md` docs/b.md',
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-36",
		},
		{
			markdown: "[Before <script> first](https://example.test/x)\n\n docs/a.md </script> docs/b.md",
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-37",
		},
		{
			markdown: "![Before <script> first](./image.png)\n\n docs/a.md </script> docs/b.md",
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-38",
		},
		{
			markdown: "[Before **<script> first**](https://example.test/x) docs/a.md </script> docs/b.md",
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-39",
		},
		{
			markdown: 'Before <a href="https://example.test/x"> first\n\n[inner](./docs/a.md) docs/b.md',
			paths: ["docs/a.md", "docs/b.md"],
			name: "historical-40",
		},
		{
			markdown: 'Before <a href="https://example.test/x"> first\n\n docs/a.md',
			paths: ["docs/a.md"],
			name: "historical-41",
		},
	],
);
corpus.push({
	name: "literal-html-escaped-table-header",
	markdown:
		"Before <script> first\n\n| \\|docs/a.md | Why |\n| --- | --- |\n| </script> ../**docs/x.md** \\|docs/b.md | docs/c.md |",
	paths: ["|docs/a.md", "|docs/b.md", "docs/c.md"],
});
corpus.push(
	...[
		{
			name: "mixed-file-destination",
			markdown: "<div>\n[R](./docs/file.md)\n</div>",
			paths: ["docs/file.md"],
			webPaths: ["docs/file.md"],
			rendered: "<div>\n[R](action-0)\n</div>",
		},
		{
			name: "mixed-fenced-code",
			markdown: "<script>\n```sh\ndocs/a.md\n```\n</script>",
			paths: [],
			webPaths: ["docs/a.md"],
		},
		{
			name: "mixed-inline-code",
			markdown: "<div>\n`docs/a.md`\n</div>",
			paths: ["docs/a.md"],
			webPaths: ["`docs/a.md`"],
			rendered: "<div>\n[`docs/a.md`](action-0)\n</div>",
		},
		{
			name: "mixed-external-label",
			markdown: "<div>\n[docs/label.md](https://example.test/x)\n</div>",
			paths: [],
			webPaths: ["docs/label.md"],
			externalDestinations: ["https://example.test/x"],
		},
		{
			name: "mixed-indented-continuation",
			markdown: "<div>\n    docs/a.md\n</div>",
			paths: ["docs/a.md"],
			webPaths: ["docs/a.md"],
			rendered: "<div>\n    [docs/a.md](action-0)\n</div>",
		},
		{
			name: "mixed-indented-code",
			markdown: "<div>\n\n    docs/a.md\n\n</div>",
			paths: [],
			webPaths: [],
		},
		{
			name: "mixed-mermaid-code",
			markdown: "<style>\n```mermaid\ndocs/a.md\n```\n</style>",
			paths: [],
			webPaths: ["docs/a.md"],
		},
		{
			name: "mixed-file-label",
			markdown: '<pre>\n[docs/label.md `]`](<./docs/a%20b.md> "keep")\n</pre>',
			paths: ["docs/a b.md"],
			webPaths: ["docs/label.md"],
			rendered: '<pre>\n[docs/label.md `]`](<action-0> "keep")\n</pre>',
		},
		{
			name: "mixed-local-definition",
			markdown: '<script>\n[R][r]\n\n[r]: ./docs/a.md "keep"\n</script>',
			paths: ["docs/a.md"],
			webPaths: ["docs/a.md"],
			rendered: '<script>\n[R](action-0)\n\n[r]: ./docs/a.md "keep"\n</script>',
		},
		{
			name: "mixed-forward-reference",
			markdown: '<div>\n[R][r]\n</div>\n\n[r]: ./docs/a.md "keep"',
			paths: ["docs/a.md"],
			webPaths: [],
			rendered: '<div>\n[R](action-0)\n</div>\n\n[r]: ./docs/a.md "keep"',
		},
		{
			name: "mixed-blockquote-crlf",
			markdown: "> <div>\r\n> [R](./docs/file.md)\r\n> </div>\r\n",
			paths: ["docs/file.md"],
			webPaths: ["docs/file.md"],
			rendered: "> <div>\r\n> [R](action-0)\r\n> </div>\r\n",
		},
		{
			name: "mixed-list-fence",
			markdown: "- <script>\n  ```sh\n  docs/a.md\n  ```\n  </script>",
			paths: [],
			webPaths: ["docs/a.md"],
		},
		{
			name: "mixed-inline-command",
			markdown: "<pre>\n`cat docs/a.md`\n</pre>",
			paths: [],
			webPaths: ["docs/a.md`"],
		},
	],
);
const originalCorpusLength = corpus.length;
corpus.push(
	{ name: "entity-prose", markdown: "docs/a&amp;b.md", paths: ["docs/a&b.md"], labels: ["docs/a&b.md"] },
	{
		name: "entity-html",
		markdown: "Before <!-- docs/a&amp;b.md --> after",
		paths: ["docs/a&b.md"],
		labels: ["docs/a&b.md"],
	},
	{
		name: "entity-named-numeric",
		markdown: "docs/&eacute;.md docs/a&#38;b.md docs/a&#x26;b.md docs/&#x1F600;.md docs/&NotEqualTilde;.md",
		paths: ["docs/é.md", "docs/a&b.md", "docs/a&b.md", "docs/😀.md", "docs/≂̸.md"],
		labels: ["docs/é.md", "docs/a&b.md", "docs/a&b.md", "docs/😀.md", "docs/≂̸.md"],
	},
	{
		name: "entity-escaped",
		markdown: String.raw`docs/a\&amp;b.md`,
		paths: ["docs/a&amp;b.md"],
		labels: ["docs/a&amp;b.md"],
	},
	{
		name: "entity-unknown",
		markdown: String.raw`docs/&unknown;.md docs/&constructor;.md docs/&toString;.md docs/&\_\_proto\_\_;.md`,
		paths: ["docs/&unknown;.md", "docs/&constructor;.md", "docs/&toString;.md", "docs/&__proto__;.md"],
		labels: ["docs/&unknown;.md", "docs/&constructor;.md", "docs/&toString;.md", "docs/&__proto__;.md"],
	},
	{
		name: "entity-nested-percent",
		markdown: "docs/a&amp;lt;b.md docs/100&percnt;25.md docs/100%25.md",
		paths: ["docs/a&lt;b.md", "docs/100%25.md", "docs/100%25.md"],
		labels: ["docs/a&lt;b.md", "docs/100%25.md", "docs/100%25.md"],
	},
	{
		name: "entity-rejected",
		markdown: "docs/a&lt;b.md docs/a&bsol;b.md ../**docs/a&amp;b.md** https://host/**docs/a&amp;b.md**",
		paths: [],
		labels: [],
	},
	{
		name: "entity-boundaries",
		markdown: "&quot;docs/a&amp;b.md&quot; docs/a.md&Tab;docs/b.md &#46;&#46;/docs/no.md",
		paths: ["docs/a&b.md", "docs/a.md", "docs/b.md"],
		labels: ["docs/a&b.md", "docs/a.md", "docs/b.md"],
	},
	{
		name: "entity-code-link-controls",
		markdown: "`docs/a&amp;b.md` [R](./docs/a&amp;b.md) [E](./docs/a\\&amp;b.md)",
		paths: ["docs/a&amp;b.md", "docs/a&b.md", "docs/a&amp;b.md"],
		labels: ["docs/a&amp;b.md", "R", "E"],
	},
	{
		name: "entity-definition-controls",
		markdown: '[R](<./docs/a&amp;b.md> "keep") [D][d]\n\n[d]: ./docs/a\\&amp;b.md "title"',
		paths: ["docs/a&b.md", "docs/a&amp;b.md"],
		labels: ["R", "D"],
	},
	{
		name: "entity-nested-crlf",
		markdown: "> - **docs/a&amp;b.md**\r\n>   - docs/&#x1F600;.md\r\n",
		paths: ["docs/a&b.md", "docs/😀.md"],
		labels: ["docs/a&b.md", "docs/😀.md"],
	},
	{
		name: "entity-table",
		markdown: "A | B\n--- | ---\n\\|docs/a&amp;b.md | docs/&eacute;.md",
		paths: ["|docs/a&b.md", "docs/é.md"],
		labels: ["|docs/a&b.md", "docs/é.md"],
	},
	{
		name: "entity-spans",
		markdown: "lead &eacute;: docs/a&amp;b.md, docs/&#x1F600;.md:12. docs/&NotEqualTilde;.md! tail &amp;\r\n",
		paths: ["docs/a&b.md", "docs/😀.md", "docs/≂̸.md"],
		labels: ["docs/a&b.md", "docs/😀.md", "docs/≂̸.md"],
	},
	{
		name: "entity-label-syntax",
		markdown:
			"docs/a&lowbar;&lowbar;b&lowbar;&lowbar;.md docs/a&dollar;b&dollar;.md docs/a&vert;b.md docs/a&ast;b&ast;.md docs/a&grave;b&grave;.md docs/a&#126;b&#126;.md",
		paths: ["docs/a__b__.md", "docs/a$b$.md", "docs/a|b.md", "docs/a*b*.md", "docs/a`b`.md", "docs/a~b~.md"],
		labels: ["docs/a__b__.md", "docs/a$b$.md", "docs/a|b.md", "docs/a*b*.md", "docs/a`b`.md", "docs/a~b~.md"],
	},
	{
		name: "entity-slash-surrounding-invalid-numeric",
		markdown: "docs&sol;a.md &lpar;docs/a&amp;b.md&rpar; docs/&#0;.md docs/&#xD800;.md docs/&#x110000;.md",
		paths: ["docs/a.md", "docs/a&b.md", "docs/�.md", "docs/�.md", "docs/�.md"],
		labels: ["docs/a.md", "docs/a&b.md", "docs/�.md", "docs/�.md", "docs/�.md"],
	},
	{
		name: "entity-token-boundaries",
		markdown: "docs/**a&amp;b.md** docs/`a&amp;b.md` ../**docs/a&amp;b.md**",
		paths: [],
		labels: [],
	},
	{
		name: "entity-c1-prose",
		markdown: "docs/&#128;.md docs/&#x80;.md docs/&#130;.md docs/&#x9F;.md",
		paths: ["docs/€.md", "docs/€.md", "docs/‚.md", "docs/Ÿ.md"],
		labels: ["docs/€.md", "docs/€.md", "docs/‚.md", "docs/Ÿ.md"],
	},
	{
		name: "entity-c1-html-named-literal",
		markdown: "Before <!-- docs/&#128;.md --> docs/&euro;.md docs/€.md after",
		paths: ["docs/€.md", "docs/€.md", "docs/€.md"],
		labels: ["docs/€.md", "docs/€.md", "docs/€.md"],
	},
	{
		name: "entity-c1-unmapped-and-quote-boundaries",
		markdown: "docs/&#129;.md docs/&#x9D;.md docs/a&#145;b.md docs/a‘b.md docs/a&#x92;b.md docs/a’b.md",
		paths: [],
		labels: [],
	},
	{
		name: "entity-c1-surface-controls",
		markdown:
			'`docs/&#128;.md` docs/\\&#128;.md [R](./docs/&#128;.md) [N](./docs/&euro;.md) [E](./docs/\\&#128;.md) [D][d]\n\n[d]: ./docs/\\&#128;.md "keep &#128; title"',
		paths: ["docs/&#128;.md", "docs/&#128;.md", "docs/€.md", "docs/&", "docs/&"],
		labels: ["docs/&#128;.md", "docs/&#128;.md", "N", "E", "D"],
		externalDestinations: ["./docs/&#128;.md"],
		externalLabels: ["R"],
	},
	{
		name: "large-adjoining-prose",
		markdown: largeAdjoiningProse,
		paths: ["docs/a.md"],
		labels: ["docs/a.md"],
		rendered: `[docs/a.md](action-0)${largeAdjoiningProse.slice("docs/a.md".length)}`,
	},
);
const expectedExternal: Record<string, string[]> = {
	"actual-links": ["https://example.test/x"],
	"historical-0": ["https://example.test/docs/e.md"],
	"historical-37": ["https://example.test/x"],
	"historical-39": ["https://example.test/x"],
};
const evidence = [];
const failures: string[] = [];
for (const [index, sample] of corpus.entries()) {
	let discovered: ReturnType<typeof markdownFileReferences>;
	let rendered: ReturnType<typeof renderMarkdownFileReferences>;
	try {
		discovered = markdownFileReferences(sample.markdown, "/work/tree");
		rendered = renderMarkdownFileReferences(sample.markdown, "/work/tree");
	} catch (error) {
		const adapterError =
			error instanceof Error ? `${error.name}: ${error.message}\n${error.stack ?? ""}` : String(error);
		failures.push(adapterError);
		evidence.push({
			...sample,
			expectedPaths: sample.paths ?? (sample.path === undefined ? [] : [sample.path]),
			adapterError,
		});
		console.error(`FAIL native adapter case ${index}: ${sample.name ?? sample.markdown}\n${adapterError}`);
		continue;
	}
	writeFileSync(path.join(scratch, `case-${index}.md`), rendered.markdown);
	const output = run(executable, [], rendered.markdown);
	const links = output.trim()
		? output
				.trim()
				.split("\n")
				.map((line) => {
					const fields = line.split("\t");
					assert.equal(fields.length, 2, "probe must emit destination and actual AST label");
					return {
						destination: Buffer.from(fields[0], "hex").toString("utf8"),
						label: Buffer.from(fields[1], "hex").toString("utf8"),
					};
				})
		: [];
	const destinations = links.map((link) => link.destination);
	const generatedLabels = links.filter((link) => rendered.references.has(link.destination)).map((link) => link.label);
	const mapEntries = [...rendered.references.entries()];
	const discoveryPaths = discovered.map(({ reference }) => reference.path);
	const mapPaths = mapEntries.map(([, reference]) => reference.path);
	const paths = destinations.flatMap((destination) => {
		const reference = rendered.references.get(destination);
		return reference ? [reference.path] : [];
	});
	const externalLabels = links.filter((link) => !rendered.references.has(link.destination)).map((link) => link.label);
	const externalDestinations = destinations.filter((destination) => !rendered.references.has(destination));
	const expected = sample.paths ?? (sample.path === undefined ? [] : [sample.path]);
	const emissionCounts = mapEntries.map(([id]) => ({
		id,
		count: destinations.filter((destination) => destination === id).length,
	}));
	let normalized = rendered.markdown;
	for (const [index, [id]] of mapEntries.entries()) normalized = normalized.replace(id, `action-${index}`);
	const caseFailures: string[] = [];
	function check(action: () => void) {
		try {
			action();
		} catch (error) {
			caseFailures.push(String(error));
		}
	}
	check(() => {
		for (const { id, count } of emissionCounts)
			assert.equal(count, 1, `case ${index}, map ID ${id} must be emitted exactly once`);
		assert(
			!externalDestinations.some((destination) => destination.includes("evener-file:")),
			`case ${index}, malformed generated destination`,
		);
		assert.deepEqual(
			externalDestinations,
			sample.externalDestinations ?? expectedExternal[sample.name ?? ""] ?? [],
			`case ${index}, unexpected unmapped destination`,
		);
		assert.deepEqual(discoveryPaths, expected, `case ${index}, discovery`);
		assert.deepEqual(mapPaths, expected, `case ${index}, map`);
		assert.deepEqual(paths, expected, `case ${index}, actual parser paths: ${sample.markdown}`);
		if (sample.rendered !== undefined || sample.name?.startsWith("mixed-"))
			assert.equal(normalized, sample.rendered ?? sample.markdown, `case ${index}, label/destination source`);
	});
	if (sample.labels) {
		check(() => assert.deepEqual(generatedLabels, sample.labels, `case ${index}, actual AST labels`));
		check(() =>
			assert.deepEqual(
				mapEntries.map(([, ref]) => ref.readTarget),
				expected.map((path) => `/work/tree/${path}`),
				`case ${index}, captured absolute targets`,
			),
		);
	}
	if (sample.externalLabels)
		check(() => assert.deepEqual(externalLabels, sample.externalLabels, `case ${index}, actual external AST labels`));
	const failure = caseFailures.length ? caseFailures.join("\n") : undefined;
	failures.push(...caseFailures);
	evidence.push({
		...sample,
		expectedPaths: sample.paths ?? (sample.path === undefined ? [] : [sample.path]),
		rendered: rendered.markdown,
		discovered,
		discoveryPaths,
		mapEntries,
		mapPaths,
		emissionCounts,
		externalDestinations,
		externalLabels,
		destinations,
		links,
		generatedLabels,
		paths,
		failure,
	});
	console.log(`${failure ? "FAIL" : "PASS"} native C++ case ${index}: ${sample.markdown}`);
	if (failure) console.error(failure);
}
writeFileSync(
	path.join(scratch, "result.json"),
	JSON.stringify(
		{
			package: pkg.version,
			marked: marked.version,
			originalCorpusLength,
			originalGeneratedIDs: evidence
				.slice(0, originalCorpusLength)
				.reduce((sum, entry) => sum + ("emissionCounts" in entry ? entry.emissionCounts.length : 0), 0),
			flags:
				"MD_FLAG_NOHTML | MD_FLAG_STRIKETHROUGH | MD_FLAG_TABLES | MD_FLAG_TASKLISTS | MD_FLAG_SPOILERS | MD_FLAG_PERMISSIVEAUTOLINKS | MD_FLAG_LATEXMATHSPANS",
			cases: evidence,
		},
		null,
		2,
	),
);
console.log(`Actual C++ corpus results retained in ${scratch}/result.json`);
assert.equal(failures.length, 0, failures.join("\n"));
console.log(
	`Pinned C++ destinations and asserted AST labels checked, artifacts ${scratch}. No native device gestures or Image cache claimed.`,
);
