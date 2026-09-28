// The review a document's Review sheet sends (spec 10.2), in the spec's
// format: the verdict, each comment under the line it quotes, then the
// overall note. A comment always quotes the words it was left on, so it
// reads right even after its paragraph changed (ruling 14).

export type Verdict = "approve" | "requestChanges" | "commentOnly";

const VERDICT_WORDS: Record<Verdict, string> = {
	approve: "approved",
	requestChanges: "request changes",
	commentOnly: "comments only",
};

/** One line of at most `max` characters, the ellipsis included, cut at a
 * word where it can be (ruling 16). */
export function quoteLine(text: string, max = 160): string {
	const flat = text.replace(/\s+/g, " ").trim();
	if (flat.length <= max) return flat;
	const cut = flat.slice(0, max - 1);
	const space = cut.lastIndexOf(" ");
	return `${(space > max * 0.6 ? cut.slice(0, space) : cut).trimEnd()}…`;
}

export function reviewMessage(
	path: string,
	verdict: Verdict,
	comments: readonly { quote: string; text: string }[],
	note: string,
): string {
	const parts = [`Review of ${path}: ${VERDICT_WORDS[verdict]}.`];
	for (const comment of comments) parts.push(`> ${quoteLine(comment.quote)}\n${comment.text.trim()}`);
	if (note.trim()) parts.push(`Overall: ${note.trim()}`);
	return parts.join("\n\n");
}
