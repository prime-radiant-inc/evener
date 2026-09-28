// Quote in reply from a screen above a session (the Reader's block menu, spec
// 10.2): the words wait here, one quote per session, until that session's
// screen comes into focus and puts them in its draft. Memory only, so a
// relaunch in between loses the quote and nothing else.
import { sheetKey } from "../sheet/sheetHosts";

const held = new Map<string, string>();

/** Holds words for a session's composer, replacing any quote held before. */
export function holdQuote(hubId: string, ref: string, words: string): void {
	held.set(sheetKey(hubId, ref), words);
}

/** The quote held for this session, once; null when there is none. */
export function takeQuote(hubId: string, ref: string): string | null {
	const key = sheetKey(hubId, ref);
	const words = held.get(key) ?? null;
	held.delete(key);
	return words;
}
