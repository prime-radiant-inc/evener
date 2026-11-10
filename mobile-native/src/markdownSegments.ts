// Splits a message at top-level CLOSED mermaid fences into the strings
// EnrichedMarkdownText renders. md4c parses each segment standalone, so a
// reference-style link whose definition sits across a diagram would render
// as literal "[text][label]" (verified against the package's own md4c build):
// every prose segment gets the whole message's link definitions prepended.
// Definitions render as nothing, so visible output is unchanged. A mermaid
// fence still open at the tail (stream in flight) stays prose and renders as
// a code block until its closer arrives - there is no closeOpenMarkdown on
// native, so this is a terminated-raw check, not a two-lex comparison.
import { fenceTokenTerminated, isMermaidCodeToken, MERMAID_FENCE, mermaidText } from "@evener/appwire-client";
import { lexer, type Token } from "marked";

export type NativeSegment = { kind: "markdown"; source: string } | { kind: "mermaid"; source: string };

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
