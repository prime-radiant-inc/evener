import { compactDuration, DURATION_UNIT_MS } from "@evener/appwire-client";

// The Session's time and path text (spec 5). Durations and token counts use
// the package's compactDuration, spokenDuration and formatTokenCount. Paths
// and URLs wrap only at their slashes.

/** How long ago something happened, in `words` ("3m ago"), or "just now"
 * under a minute: the minute clock can lag it by up to a minute, and a hub
 * clock ahead of this phone's can put it in the future. */
export function timeAgo(ms: number, words: (ms: number) => string = compactDuration): string {
	return ms < DURATION_UNIT_MS.minute ? "just now" : `${words(ms)} ago`;
}

/** A path or URL may wrap only after a slash: a zero-width space follows each
 * one. */
export const wrapAfterSlashes = (path: string) => path.replace(/\//g, "/​");
