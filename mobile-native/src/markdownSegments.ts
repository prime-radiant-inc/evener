// Splits a message at top-level CLOSED mermaid fences into the strings
// EnrichedMarkdownText renders. md4c parses each segment standalone, so a
// reference-style link whose definition sits across a diagram would render
// as literal "[text][label]" (verified against the package's own md4c build):
// every prose segment gets the whole message's link definitions prepended.
// Definitions render as nothing, so visible output is unchanged. A mermaid
// fence still open at the tail (stream in flight) stays prose and renders as
// a code block until its closer arrives - there is no closeOpenMarkdown on
// native, so this is a terminated-raw check, not a two-lex comparison.
//
// The fence helpers below (MERMAID_FENCE, isMermaidCodeToken, mermaidText,
// FENCE_CLOSE_LINE, fenceTokenTerminated) mirror the web sibling at
// cmd/evener-hub/frontend/src/widgets/markdown/segments.ts - keep the two in
// sync (their bodies deliberately differ where noted here).
import { lexer, type Token } from "marked";

export type NativeSegment = { kind: "markdown"; source: string } | { kind: "mermaid"; source: string };

const MERMAID_FENCE = /^ {0,3}(?:`{3,}|~{3,})[ \t]*mermaid(?:[ \t\r]|$)/im;
const FENCE_OPEN_LINE = /^ {0,3}(`{3,}|~{3,})/;
const FENCE_CLOSE_LINE = /^ {0,3}(`{3,}|~{3,})[ \t]*$/;

function isMermaidCodeToken(token: Token): boolean {
	return token.type === "code" && (token.lang ?? "").trim().split(/\s+/)[0]?.toLowerCase() === "mermaid";
}

function fenceTokenTerminated(raw: string): boolean {
	// marked normalizes CRLF at lex, so raw never contains "\r"; the divide on
	// "\n" alone is deliberate (the web sibling does the same, belt-and-braces
	// replace aside).
	const lines = raw.split("\n");
	// A closer only terminates when it shares the opener's fence character and
	// is at least as long (CommonMark), so a `~~~` line inside a backtick fence
	// is content, not a closer (kept in step with the web sibling).
	const opener = FENCE_OPEN_LINE.exec(lines[0] ?? "");
	if (opener === null) return false;
	const openerRun = opener[1] ?? "";
	const char = openerRun.charAt(0);
	for (let index = lines.length - 1; index >= 0; index -= 1) {
		const line = lines[index] ?? "";
		if (line.trim() === "") continue;
		const closer = FENCE_CLOSE_LINE.exec(line);
		if (closer === null) return false;
		const closerRun = closer[1] ?? "";
		return closerRun.charAt(0) === char && closerRun.length >= openerRun.length;
	}
	return false;
}

// The diagram source is the fenced block's content, restored to its
// newline-terminated source form. marked strips exactly one trailing newline
// from a code token's text at lex, so a non-empty text is missing that final
// "\n" - the test pins the restored value ("graph TD; A-->B\n"), and mermaid
// consumes a newline-terminated string unchanged.
function mermaidText(token: Token): string {
	const text = (token as { text?: string }).text ?? "";
	return text.length > 0 ? `${text}\n` : text;
}

function collectDefinitions(tokens: Token[], into: string[]): void {
	for (const token of tokens) {
		if (token.type === "def") {
			into.push(token.raw);
			continue;
		}
		if ("tokens" in token) collectDefinitions((token.tokens as Token[] | undefined) ?? [], into);
		if ("items" in token)
			for (const item of (token.items as { tokens?: Token[] }[] | undefined) ?? []) {
				collectDefinitions(item.tokens ?? [], into);
			}
	}
}

export function splitNativeSegments(source: string): NativeSegment[] {
	// Cheap gate: no mermaid fence, no lex, today's exact render path.
	if (!MERMAID_FENCE.test(source)) return [{ kind: "markdown", source }];
	const tokens = lexer(source);
	const definitions: string[] = [];
	collectDefinitions(tokens, definitions);
	// A def's raw keeps its trailing "\n" only when another line immediately
	// follows, so two blank-line-separated defs would fuse into one unparseable
	// line ("...a.example[b]: ..."). Terminate each raw, then a closing blank
	// line, so the block lexes as separate definitions.
	const prefix =
		definitions.length > 0 ? `${definitions.map((d) => (d.endsWith("\n") ? d : `${d}\n`)).join("")}\n` : "";

	const segments: NativeSegment[] = [];
	let markdownRun = "";
	const flush = () => {
		const body = markdownRun;
		markdownRun = "";
		if (body.trim() === "") return;
		segments.push({ kind: "markdown", source: prefix + body });
	};
	for (const token of tokens) {
		if (isMermaidCodeToken(token) && fenceTokenTerminated(token.raw)) {
			flush();
			segments.push({ kind: "mermaid", source: mermaidText(token) });
		} else {
			markdownRun += token.raw;
		}
	}
	flush();
	return segments;
}
