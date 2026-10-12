// The Session's Find bar, as a screen test reads it: its own props say what
// it shows, so a test asserts the search's answer and state, not the words.
import type { ReactTestRenderer } from "react-test-renderer";
import { matchLabel } from "./session/findInSession";

/** Find's answer when nothing matched. */
export const NO_MATCHES = matchLabel([], null);
/** Find's answer when it sits on its one match. */
export const ONLY_MATCH = matchLabel([0], 0);

/** The mounted Find bar. */
export function findBar(tree: ReactTestRenderer) {
	return tree.root.find((node) => typeof node.type === "function" && node.type.name === "FindBar");
}

/** Whether the Find bar shows `label` as its settled answer: not searching
 * older messages, and not reporting an incomplete search. */
export function findAnswers(tree: ReactTestRenderer, label: string): boolean {
	const bar = findBar(tree).props;
	return !bar.error && !bar.searchingOlder && bar.label === label;
}
