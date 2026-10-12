// The Session's Find bar, as a screen test reads it: what it shows as
// structure (how many matches, which one, and whether the search is settled,
// still searching older messages, incomplete or out of older history), never
// its words.
import type { ReactTestRenderer } from "react-test-renderer";
import { markers } from "./markerTestUtils";

/** The mounted Find bar. */
export function findBar(tree: ReactTestRenderer) {
	return tree.root.find((node) => typeof node.type === "function" && node.type.name === "FindBar");
}

/** Where the Find bar says you are among its matches, as its
 * `find-matches:<position>:<count>` marker: position 0 with none. Null while
 * it shows no count. */
export function findMatches(tree: ReactTestRenderer): { position: number; count: number } | null {
	const [marker, ...others] = markers(tree, "find-matches");
	if (marker === undefined || others.length > 0) return null;
	const [position, count] = marker.split(":").map(Number);
	return { position: position ?? Number.NaN, count: count ?? Number.NaN };
}

/** Whether the Find bar has settled on `count` matches, at `position` (0 with
 * none): not searching older messages, not reporting an incomplete search,
 * and not saying older history ran out. */
export function findShows(tree: ReactTestRenderer, count: number, position: number): boolean {
	const bar = findBar(tree).props;
	const shown = findMatches(tree);
	return !bar.error && !bar.searchingOlder && !bar.exhausted && shown?.count === count && shown.position === position;
}
