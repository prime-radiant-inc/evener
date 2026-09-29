// A document as the Reader draws it (spec 10.2): one block per paragraph,
// heading, code block, table, quote or rule, and one per list item, so a
// comment attaches to the item under your finger. Split with marked's lexer,
// the parser the web's doc pane uses (ruling 12). Each block keeps its own
// markdown for the renderer, and a hash of it: that hash is how changes and
// comment anchors recognize a block across versions (S9's fallback).
import { filenameOf } from "@evener/appwire-client/docContent";
import { lexer, type Tokens } from "marked";

export type BlockKind = "heading" | "paragraph" | "listItem" | "code" | "table" | "quote" | "rule" | "html";

export interface DocumentBlock {
	index: number;
	kind: BlockKind;
	/** The block's markdown, as the renderer draws it. An ordered list item
	 * carries its computed number, so a lazily numbered list reads 1, 2, 3. */
	markdown: string;
	/** Identity across versions: a hash of the kind and the normalized words
	 * (a list item's number isn't part of it). */
	hash: string;
	/** The words without markdown: what a comment quotes and Copy copies. */
	text: string;
	/** A heading's level. */
	depth?: number;
	code?: { text: string; lang?: string };
}

const LIST_MARKER = /^\s*(?:[-*+]|\d+[.)])\s+/;
// A setext heading's underline ("===" or "---" under its words). Only a
// heading has one: a line of "=" on its own is a paragraph's words.
const SETEXT_UNDERLINE = /\n[ \t]*(?:=+|-+)[ \t]*$/;
// An inline code span or an inline HTML tag. It runs over a whole block before
// the block is split into lines, so a code span may cross lines; the span keeps
// its contents (group 2), so stripping a tag can't take the angle brackets it
// holds: `Vec<String>` survives, `<b>` doesn't. A tag stays on one line and its
// name needs a space, `/` or `>` boundary, so a bare autolink like `<https://x>`
// isn't mistaken for one, and a quoted attribute may hold a `>`.
const INLINE_CODE_OR_TAG =
	/(`+)([^`]|[^`][\s\S]*?[^`])\1(?!`)|<\/?[a-z][a-z0-9-]*(?:[ \t]+(?:[^>"'\n]|"[^"\n]*"|'[^'\n]*')*)?\/?>/gi;

/** cyrb53: a small, stable 53-bit string hash. Collisions don't matter at a
 * document's scale; stability across launches does. */
export function hashText(text: string): string {
	let h1 = 0xdeadbeef;
	let h2 = 0x41c6ce57;
	for (let index = 0; index < text.length; index += 1) {
		const code = text.charCodeAt(index);
		h1 = Math.imul(h1 ^ code, 2654435761);
		h2 = Math.imul(h2 ^ code, 1597334677);
	}
	h1 = Math.imul(h1 ^ (h1 >>> 16), 2246822507);
	h1 ^= Math.imul(h2 ^ (h2 >>> 13), 3266489909);
	h2 = Math.imul(h2 ^ (h2 >>> 16), 2246822507);
	h2 ^= Math.imul(h1 ^ (h1 >>> 13), 3266489909);
	return (4294967296 * (2097151 & h2) + (h1 >>> 0)).toString(36);
}

function trimBlock(raw: string): string {
	return raw.replace(/^\n+/, "").replace(/\s+$/, "");
}

function identity(kind: BlockKind, source: string): string {
	const body = kind === "listItem" ? source.replace(LIST_MARKER, "") : source;
	const normalized = body
		.split("\n")
		.map((line) => line.trimEnd())
		.join("\n")
		.trim();
	return hashText(`${kind}:${normalized}`);
}

function isTableRule(line: string): boolean {
	return /^[\s|:-]+$/.test(line) && line.includes("-");
}

/** A block's words without markdown syntax: heading marks, quote marks, list
 * markers and task boxes, link and image syntax, and emphasis. Inline HTML tags
 * go too, unless `stripInlineTags` is false (the html block's own words keep
 * theirs). Underscores inside words (snake_case) stay. */
export function plainText(markdown: string, stripInlineTags = true): string {
	const stripped = stripInlineTags ? markdown.replace(INLINE_CODE_OR_TAG, "$2") : markdown;
	return stripped
		.split("\n")
		.map((line) =>
			line
				.replace(/^\s{0,3}#{1,6}\s+/, "")
				.replace(/^\s*>\s?/, "")
				.replace(/^\s*(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?/, "")
				.replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
				.replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
				.replace(/\*\*|__|~~|`/g, "")
				.replace(/(^|[^\w*])\*([^*\n]+)\*(?=[^\w*]|$)/g, "$1$2")
				.replace(/(^|[^\w_])_([^_\n]+)_(?=[^\w_]|$)/g, "$1$2")
				.trim(),
		)
		.filter((line) => line !== "" && !isTableRule(line))
		.join("\n");
}

export function documentBlocks(markdown: string): DocumentBlock[] {
	const blocks: DocumentBlock[] = [];
	const push = (kind: BlockKind, raw: string, extra: Pick<DocumentBlock, "depth" | "code"> = {}) => {
		const source = trimBlock(raw);
		if (source === "") return;
		let text = extra.code?.text;
		if (text === undefined) {
			const heading = kind === "heading" ? source.replace(SETEXT_UNDERLINE, "") : source;
			text = plainText(heading, kind !== "html");
		}
		blocks.push({
			index: blocks.length,
			kind,
			markdown: source,
			hash: identity(kind, source),
			text,
			...extra,
		});
	};
	for (const token of lexer(markdown.replace(/\r\n?/g, "\n"))) {
		switch (token.type) {
			case "space":
			case "def":
				break;
			case "heading":
				push("heading", token.raw, { depth: (token as Tokens.Heading).depth });
				break;
			case "list": {
				const list = token as Tokens.List;
				const start = typeof list.start === "number" ? list.start : 1;
				list.items.forEach((item, position) => {
					push("listItem", list.ordered ? item.raw.replace(/^(\s*)\d+([.)])/, `$1${start + position}$2`) : item.raw);
				});
				break;
			}
			case "code": {
				const code = token as Tokens.Code;
				push("code", code.raw, { code: { text: code.text, ...(code.lang ? { lang: code.lang } : {}) } });
				break;
			}
			case "table":
				push("table", token.raw);
				break;
			case "blockquote":
				push("quote", token.raw);
				break;
			case "hr":
				push("rule", token.raw);
				break;
			case "html":
				push("html", token.raw);
				break;
			default:
				push("paragraph", token.raw);
		}
	}
	return blocks;
}

export interface OutlineEntry {
	index: number;
	depth: number;
	title: string;
}

/** The outline sheet's headings, for jumping (spec 10.2). */
export function outline(blocks: readonly DocumentBlock[]): OutlineEntry[] {
	return blocks
		.filter((block) => block.kind === "heading")
		.map((block) => ({ index: block.index, depth: block.depth ?? 1, title: block.text }));
}

/** A document's own title: its first heading's words, else its file name
 * (spec 8.2's document chip, 10.2's nav bar). */
export function documentTitle(blocks: readonly DocumentBlock[], path: string): string {
	return blocks.find((block) => block.kind === "heading")?.text || filenameOf(path);
}
