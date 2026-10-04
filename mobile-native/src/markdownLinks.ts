/** Host file paths and app-specific actions require their own native destinations. */
export function externalMarkdownLink(target: string): string | null {
	// biome-ignore lint/suspicious/noControlCharactersInRegex: URL parsing silently removes these characters; reject them before parsing.
	if (/[\u0000-\u001f\u007f]/.test(target)) return null;
	try {
		const url = new URL(target);
		if (!["http:", "https:"].includes(url.protocol) || !url.hostname || url.username || url.password) return null;
		return url.href;
	} catch {
		return null;
	}
}

/** An inline link's or image's destination, `(url)` or `(url "title")`, as a
 * regex source. Its parentheses can hold a pair of their own (a_(b).png, a
 * title's "(1)"), one level deep, as real URLs need; a URL with pairs nested
 * deeper, or escaped or unbalanced parentheses, isn't taken whole. */
export const INLINE_DESTINATION = String.raw`\((?:[^()]|\([^()]*\))*\)`;
