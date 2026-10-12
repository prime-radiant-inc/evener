// Native views carry their state, kind or reason as a testID suffix
// (`search-note:failed`, `board-row:working`), where the web would use a
// data-* attribute. These read those markers so a test can assert what a
// surface is, not the copy it shows.
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";

const rootOf = (within: ReactTestRenderer | ReactTestInstance): ReactTestInstance =>
	"root" in within ? within.root : within;

/** The host nodes whose testID is `<prefix>:<marker>`, in render order. Only
 * host nodes count, so a component that passes its testID down is not counted
 * twice. */
export function markedNodes(within: ReactTestRenderer | ReactTestInstance, prefix: string): ReactTestInstance[] {
	return rootOf(within).findAll(
		(node) =>
			typeof node.type === "string" &&
			typeof node.props.testID === "string" &&
			node.props.testID.startsWith(`${prefix}:`),
	);
}

/** The markers of `markedNodes`, in render order. */
export function markers(within: ReactTestRenderer | ReactTestInstance, prefix: string): string[] {
	return markedNodes(within, prefix).map((node) => (node.props.testID as string).slice(prefix.length + 1));
}

/** The host nodes whose testID is exactly `testID`, in render order. */
export function withTestID(within: ReactTestRenderer | ReactTestInstance, testID: string): ReactTestInstance[] {
	return rootOf(within).findAll((node) => typeof node.type === "string" && node.props.testID === testID);
}

/** Whether a host node with exactly this testID renders here. */
export function hasTestID(within: ReactTestRenderer | ReactTestInstance, testID: string): boolean {
	return withTestID(within, testID).length > 0;
}

/** The node's own marker, when its testID is `<prefix>:<marker>`; null otherwise. */
export function markerOf(node: ReactTestInstance, prefix: string): string | null {
	const id = node.props.testID;
	return typeof id === "string" && id.startsWith(`${prefix}:`) ? id.slice(prefix.length + 1) : null;
}
