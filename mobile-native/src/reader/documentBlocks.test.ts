import { describe, expect, it } from "vitest";
import { documentBlocks, documentTitle, hashText, outline } from "./documentBlocks";

// A markdown code fence, built so this file can sit inside one.
const FENCE = "`".repeat(3);
const plan = [
	"# Fix the settle/drain race\r",
	"\r",
	"## Problem",
	"",
	"The retirement drain and the tree settle pass both take the tree lock.",
	"When settle runs first, it can mark the tree idle.",
	"",
	"1. Settle waits for the drain.",
	"1. The drain signals completion through a **channel**, not a `shared` flag.",
	"1. Add a regression test.",
	"",
	"- [ ] Run `-race` on macOS",
	"- [x] Run it on Linux",
	"  - nested _note_ here",
	"",
	`${FENCE}go`,
	"func settle() {}",
	FENCE,
	"",
	"| Work | Subagents |",
	"|---|---|",
	"| Fix the race | 1 |",
	"",
	"> Quoted *advice* with [a link](https://x.test).",
	"",
	"---",
	"",
	"<div>html block</div>",
	"",
	"write_file and snake_case_names stay whole.",
	"",
].join("\n");

describe("a document as the Reader draws it (spec 10.2, ruling 12)", () => {
	it("makes one block per paragraph, heading, code block, table, quote and rule, and one per list item", () => {
		expect(documentBlocks(plan).map((block) => [block.kind, block.markdown])).toEqual([
			["heading", "# Fix the settle/drain race"],
			["heading", "## Problem"],
			[
				"paragraph",
				"The retirement drain and the tree settle pass both take the tree lock.\nWhen settle runs first, it can mark the tree idle.",
			],
			["listItem", "1. Settle waits for the drain."],
			["listItem", "2. The drain signals completion through a **channel**, not a `shared` flag."],
			["listItem", "3. Add a regression test."],
			["listItem", "- [ ] Run `-race` on macOS"],
			["listItem", "- [x] Run it on Linux\n  - nested _note_ here"],
			["code", `${FENCE}go\nfunc settle() {}\n${FENCE}`],
			["table", "| Work | Subagents |\n|---|---|\n| Fix the race | 1 |"],
			["quote", "> Quoted *advice* with [a link](https://x.test)."],
			["rule", "---"],
			["html", "<div>html block</div>"],
			["paragraph", "write_file and snake_case_names stay whole."],
		]);
	});

	it("gives each block the words a comment quotes and Copy copies", () => {
		const blocks = documentBlocks(plan);
		expect(blocks.map((block) => block.text)).toEqual([
			"Fix the settle/drain race",
			"Problem",
			"The retirement drain and the tree settle pass both take the tree lock.\nWhen settle runs first, it can mark the tree idle.",
			"Settle waits for the drain.",
			"The drain signals completion through a channel, not a shared flag.",
			"Add a regression test.",
			"Run -race on macOS",
			"Run it on Linux\nnested note here",
			"func settle() {}",
			"| Work | Subagents |\n| Fix the race | 1 |",
			"Quoted advice with a link.",
			"",
			"<div>html block</div>",
			"write_file and snake_case_names stay whole.",
		]);
		expect(blocks[0]?.depth).toBe(1);
		expect(blocks[1]?.depth).toBe(2);
		expect(blocks[8]?.code).toEqual({ text: "func settle() {}", lang: "go" });
		expect(blocks.map((block) => block.index)).toEqual(blocks.map((_block, index) => index));
	});

	it("renumbers a lazily numbered list, and a renumbered item keeps its identity", () => {
		const lazy = documentBlocks("1. a\n1. b\n1. c");
		expect(lazy.map((block) => block.markdown)).toEqual(["1. a", "2. b", "3. c"]);
		const fromFive = documentBlocks("5. a\n2. b\n9. c");
		expect(fromFive.map((block) => block.markdown)).toEqual(["5. a", "6. b", "7. c"]);
		expect(fromFive.map((block) => block.hash)).toEqual(lazy.map((block) => block.hash));
	});

	it("keeps a block's identity through a whitespace-only edit, but not through a change of kind or words", () => {
		const [spaced] = documentBlocks("Hello world.  \n");
		const [plain] = documentBlocks("Hello world.");
		const [heading] = documentBlocks("# Hello world.");
		const [edited] = documentBlocks("Hello, world.");
		expect(spaced?.hash).toBe(plain?.hash);
		expect(heading?.hash).not.toBe(plain?.hash);
		expect(edited?.hash).not.toBe(plain?.hash);
	});

	it("has no blocks for an empty document", () => {
		expect(documentBlocks("")).toEqual([]);
		expect(documentBlocks("\n\n  \n")).toEqual([]);
	});

	it("outlines the headings and takes the first one as the title", () => {
		const blocks = documentBlocks(plan);
		expect(outline(blocks)).toEqual([
			{ index: 0, depth: 1, title: "Fix the settle/drain race" },
			{ index: 1, depth: 2, title: "Problem" },
		]);
		expect(documentTitle(blocks, "docs/superpowers/plans/settle.md")).toBe("Fix the settle/drain race");
		expect(documentTitle(documentBlocks("No heading here."), "docs/notes.md")).toBe("notes.md");
	});
});

it("reads a setext heading as its words, without its underline", () => {
	const [heading] = documentBlocks("Fix the race\n===\n\nBody.");
	expect([heading?.kind, heading?.text, heading?.depth]).toEqual(["heading", "Fix the race", 1]);
	expect(documentBlocks("Problem\n---").map((block) => block.text)).toEqual(["Problem"]);
	// A line of "=" on its own is a paragraph, not an underline: its words stay.
	expect(documentBlocks("Some intro.\n\n===\n\nMore text.").map((block) => [block.kind, block.text])).toEqual([
		["paragraph", "Some intro."],
		["paragraph", "==="],
		["paragraph", "More text."],
	]);
});

// A one-block document's words.
const wordsOf = (markdown: string) => documentBlocks(markdown).map((block) => block.text);

it("reads links and images as their words", () => {
	expect(wordsOf("See [the plan](docs/plan.md) and ![the diagram](out/d.png).")).toEqual([
		"See the plan and the diagram.",
	]);
});

it("reads a link's or image's URL holding a pair of parentheses as its words, leaving none of the URL", () => {
	expect(wordsOf("See ![chart](https://x.test/a_(b).png) and [docs](https://x.test/d_(1)) here")).toEqual([
		"See chart and docs here",
	]);
	expect(wordsOf('A [link](https://x.test/p "a (1) title") and ![img](u.png "fig (2)") end')).toEqual([
		"A link and img end",
	]);
	expect(wordsOf("See [docs](https://x.test/a_((b))) here")).toEqual(["See docs here"]);
});

it("still reads a link as its words when its URL's parentheses don't pair up", () => {
	expect(wordsOf("An [unbalanced](b(c) link")).toEqual(["An unbalanced link"]);
});

it("reads a reference link and an image by reference as their words, and drops the definitions", () => {
	expect(wordsOf("See [the plan][p] and ![a chart][c].\n\n[p]: docs/plan.md\n[c]: out/c.png")).toEqual([
		"See the plan and a chart.",
	]);
});

it("reads an escaped character as itself, without its backslash", () => {
	expect(wordsOf("Not \\*emphasis\\* and a literal \\[bracket\\].")).toEqual([
		"Not *emphasis* and a literal [bracket].",
	]);
});

it("reads emphasis inside a word and strikethrough as their words", () => {
	expect(wordsOf("un*frigging*believable and ~~gone~~ text")).toEqual(["unfriggingbelievable and gone text"]);
});

it("drops inline HTML tags from a block's words, but not inside an inline code span", () => {
	const [paragraph] = documentBlocks("Some <b>bold</b> text and a `Vec<String>` span.");
	expect(paragraph?.kind).toBe("paragraph");
	expect(paragraph?.markdown).toBe("Some <b>bold</b> text and a `Vec<String>` span.");
	expect(paragraph?.text).toBe("Some bold text and a Vec<String> span.");
	expect(documentBlocks("a<br>b")[0]?.text).toBe("ab");
	expect(documentBlocks("See <https://x.test> too.")[0]?.text).toBe("See https://x.test too.");
	expect(documentBlocks('Tags like <span title="a > b">value</span> go.')[0]?.text).toBe("Tags like value go.");
	expect(documentBlocks("Code ``a`<b>`` done.")[0]?.text).toBe("Code a`<b> done.");
});

it("leaves one space where an inline tag between two words drops", () => {
	expect(wordsOf("text <!-- c --> more")).toEqual(["text more"]);
	expect(wordsOf("a <br> b and x <span>y</span> z")).toEqual(["a b and x y z"]);
});

it("reads a table cell's escaped pipe as a pipe in the cell, not a column", () => {
	expect(wordsOf("| a \\| b | c |\n|---|---|\n| `x \\| y` | z |")).toEqual(["| a \\| b | c |\n| x \\| y | z |"]);
});

it("keeps an html block's markup but still strips its markdown", () => {
	expect(documentBlocks("<div>\n**bold**\n</div>")[0]?.text).toBe("<div>\nbold\n</div>");
	expect(documentBlocks("<div>\n# heading\n- item\n> quote\n</div>")[0]?.text).toBe(
		"<div>\nheading\nitem\nquote\n</div>",
	);
});

it("reads a code span that runs across lines on one line, as it's drawn, keeping its tags", () => {
	expect(documentBlocks("`before\n<b>\nafter`")[0]?.text).toBe("before <b> after");
});

it("hashes the same text the same way every time, and different text differently", () => {
	expect(hashText("abc")).toBe(hashText("abc"));
	expect(hashText("abc")).not.toBe(hashText("abd"));
	expect(hashText("abc")).toMatch(/^[0-9a-z]+$/);
});
