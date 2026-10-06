// A document as the Reader draws it (spec 10.2): one block per paragraph,
// heading, code block, table, quote or rule, and one per list item, so a
// comment attaches to the item under your finger. Split with marked's lexer,
// the parser the web's doc pane uses (ruling 12). Each block keeps its own
// markdown for the renderer, and a hash of it: that hash is how changes and
// comment anchors recognize a block across versions (S9's fallback).
import { filenameOf } from "@evener/appwire-client/docContent";
import { getDefaults, Lexer, lexer, type Token, Tokenizer, type Tokens } from "marked";

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

// Reads HTML, a block or a tag, as markdown text, as md4c's NOHTML does.
class TextHtmlTokenizer extends Tokenizer {
	override html() {
		return undefined;
	}
	override tag() {
		return undefined;
	}
}

// A token's words, read from marked's own parse, so a link or image reads as
// its words however its URL is written (nested parentheses, a reference, an
// autolink), an escape as the character, and a code span as its contents.
// Inline HTML tags go, so a comment never quotes a paragraph's <b> or <br>
// (#2761). A whole html block's tags are its words, so it keeps them, but its
// markdown goes: the phone's markdown view (md4c with NOHTML) draws an html
// block as markdown, its tags as text, so it's read again that way.
function words(token: Token): string {
	switch (token.type) {
		case "html":
			return token.block
				? new Lexer({ ...getDefaults(), tokenizer: new TextHtmlTokenizer() }).lex(token.text).map(words).join("\n")
				: "";
		case "checkbox":
		case "def":
		case "hr":
			return "";
		case "br":
		case "space":
			return "\n";
		case "list":
			return (token as Tokens.List).items.map(words).join("\n");
		case "list_item":
		case "blockquote":
			return (token.tokens ?? []).map(words).join("\n");
		case "table": {
			const table = token as Tokens.Table;
			return [table.header, ...table.rows]
				.map((row) => `| ${row.map((cell) => cell.tokens.map(words).join("")).join(" | ")} |`)
				.join("\n");
		}
		default:
			if ("tokens" in token && token.tokens) return token.tokens.map(words).join("");
			return "text" in token && typeof token.text === "string" ? token.text : "";
	}
}

// What a comment quotes and Copy copies: a block's words, line by line,
// each line trimmed and blank ones dropped.
function blockText(token: Token): string {
	return words(token)
		.split("\n")
		.map((line) => line.trim())
		.filter((line) => line !== "")
		.join("\n");
}

export function documentBlocks(markdown: string): DocumentBlock[] {
	const blocks: DocumentBlock[] = [];
	const push = (
		kind: BlockKind,
		token: Token,
		{ markdown = token.raw, ...extra }: Pick<DocumentBlock, "depth" | "code"> & { markdown?: string } = {},
	) => {
		const source = trimBlock(markdown);
		if (source === "") return;
		const text = extra.code?.text ?? blockText(token);
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
				push("heading", token, { depth: (token as Tokens.Heading).depth });
				break;
			case "list": {
				const list = token as Tokens.List;
				const start = typeof list.start === "number" ? list.start : 1;
				list.items.forEach((item, position) => {
					push("listItem", item, {
						markdown: list.ordered ? item.raw.replace(/^(\s*)\d+([.)])/, `$1${start + position}$2`) : item.raw,
					});
				});
				break;
			}
			case "code": {
				const code = token as Tokens.Code;
				push("code", code, { code: { text: code.text, ...(code.lang ? { lang: code.lang } : {}) } });
				break;
			}
			case "table":
				push("table", token);
				break;
			case "blockquote":
				push("quote", token);
				break;
			case "hr":
				push("rule", token);
				break;
			case "html":
				push("html", token);
				break;
			default:
				push("paragraph", token);
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
