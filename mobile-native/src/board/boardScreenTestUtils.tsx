// Imported at file scope, so the Reader's graph is transformed at collection
// time, outside the timeout of the test that opens a real Reader.
import { FreshDot } from "../reader/FreshDot";
// The Board screen mounted with only its native edges mocked: the navigation
// reads go through the real BoardController to a fake hub that answers by
// params, and the device memory is the real SeenMarkers over an in-memory
// kv-store.
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { expect } from "vitest";
import { playedHaptics } from "../renderNative.testkit";
import { markedNodes, markerOf, markers } from "../markerTestUtils";
import { sheetKey } from "../sheet/sheetHosts";
import { BoardRow } from "./BoardRow";
import { type RowMenuHost, rowMenuHosts } from "./RowMenu";

export const INCOMPATIBLE_TEXT =
	"This app and the hub need compatible versions. Update the app from TestFlight, or update Evener on the hub.";

/** Every string a Text renders on its own. */
export function texts(tree: ReactTestRenderer): string[] {
	return textsIn(tree.root);
}

export function textsIn(root: ReactTestInstance): string[] {
	return root
		.findAll((node) => node.type === ("Text" as never))
		.flatMap((node) => [node.props.children].flat().filter((child): child is string => typeof child === "string"));
}

/** The kinds of the toasts showing (at most one). */
export const toasts = (tree: ReactTestRenderer) => markers(tree, "toast");

/** Whether a node is the Board row with this title. */
export const isRowTitled = (title: string) => (node: ReactTestInstance) =>
	node.type === ("Pressable" as never) &&
	typeof node.props.accessibilityLabel === "string" &&
	node.props.accessibilityLabel.startsWith(`${title}, `);

/** A Board row, found by its title. */
export function rowTitled(tree: ReactTestRenderer, title: string) {
	return tree.root.find(isRowTitled(title));
}

/** What a row's why line says past its state word: the reason the row's
 * label has to speak too. */
export function whyReason(row: ReactTestInstance): string {
	const [line] = markedNodes(row, "why-line");
	const reason = line?.children.at(-1);
	if (typeof reason !== "string" || !reason) throw new Error("the why line gives no reason");
	return reason;
}

/** What the Board row with this title says in its why line: its WhyKind. */
export const whyKind = (tree: ReactTestRenderer, title: string) => markers(rowTitled(tree, title), "why-line")[0];

/** Whether a Board row carries the blue dot. */
export const dotted = (tree: ReactTestRenderer, title: string) =>
	rowTitled(tree, title).findAllByType(FreshDot).length > 0;

export function hasRow(tree: ReactTestRenderer, title: string) {
	return tree.root.findAll(isRowTitled(title)).length > 0;
}

export const skeletonRows = (tree: ReactTestRenderer) =>
	tree.root.findAll((node) => node.props.testID === "skeleton-row");

export function pressChip(tree: ReactTestRenderer, label: string) {
	const chip = tree.root.find((node) => node.props.testID === "chip" && node.props.accessibilityLabel === label);
	playedHaptics.length = 0;
	act(() => chip.props.onPress());
	// Spec 16.6: a selection tick on a chip.
	expect(playedHaptics).toEqual(["selection"]);
}

export const chipLabels = (tree: ReactTestRenderer) =>
	tree.root
		.findAll((node) => node.type === ("Pressable" as never) && node.props.testID === "chip")
		.map((node) => node.props.accessibilityLabel);

/** The Board's scroller, not the chips' horizontal one. */
export const boardScroller = (tree: ReactTestRenderer) =>
	tree.root.find((node) => node.type === ("ScrollView" as never) && !node.props.horizontal);

export const menuLabels = (tree: ReactTestRenderer) =>
	tree.root
		.findAll(
			(node) =>
				node.type === ("Pressable" as never) &&
				typeof node.props.accessibilityLabel === "string" &&
				node.props.accessibilityLabel.endsWith(", category menu"),
		)
		.map((node) => node.props.accessibilityLabel);

/** A search result, found by its accessibility label's start. */
export function resultTitled(tree: ReactTestRenderer, title: string) {
	return tree.root.find(
		(node) =>
			node.props.testID === "search-result" &&
			typeof node.props.accessibilityLabel === "string" &&
			node.props.accessibilityLabel.startsWith(`${title}, `),
	);
}

export const hasCancel = (tree: ReactTestRenderer) =>
	tree.root.findAll((node) => node.type === ("Pressable" as never) && node.props.accessibilityLabel === "Cancel search")
		.length > 0;

export const stateOf = (tree: ReactTestRenderer, title: string) => markerOf(rowTitled(tree, title), "board-row");

export const sheetOverBoard = {
	index: 1,
	routes: [
		{ key: "Sessions", name: "Sessions" },
		{ key: "tasks", name: "TasksSheet" },
	],
};

export const screenOverBoard = {
	index: 1,
	routes: [
		{ key: "Sessions", name: "Sessions" },
		{ key: "conversation", name: "Conversation" },
	],
};

export const boardAlone = { index: 0, routes: [{ key: "Sessions", name: "Sessions" }] };

export const projectSection = (tree: ReactTestRenderer, section: string) =>
	tree.root.find((node) => node.type === ("View" as never) && node.props.testID === `project-section:${section}`);

export const layOutAt = (node: ReactTestInstance, y: number, height: number) =>
	act(() => node.props.onLayout({ nativeEvent: { layout: { x: 0, y, width: 390, height } } }));

export const revealTarget = (tree: ReactTestRenderer) =>
	tree.root.find((node) => node.props.testID === "project-reveal");

/** The swipeable around the Board row with this title. */
export function swipeableOf(tree: ReactTestRenderer, title: string) {
	return tree.root
		.findAll((node) => node.type === ("ReanimatedSwipeable" as never))
		.filter((node) => node.findAll(isRowTitled(title)).length > 0)[0];
}

/** The row menu host the Board provides, read as the sheet reads it. */
export function menuHost(id: string): RowMenuHost {
	const host = rowMenuHosts.get(sheetKey(id));
	if (!host) throw new Error("the Board provides no row menu host");
	return host;
}

// The list holds still (spec 7.3, ruling 22): while a finger is on it, it
// scrolls or glides, the app scrolls it, a row's swipe or the row menu is
// open, and it applies every change at once when it settles.

/** Live bands by identity/count and session rows by their fixture title. */
export function listOrder(tree: ReactTestRenderer): string[] {
	return tree.root
		.find((node) => node.props.testID === "live-block")
		.findAll(
			(node) =>
				node.type === BoardRow ||
				(typeof node.props.testID === "string" && node.props.testID.startsWith("board-band:")),
		)
		.map((node) =>
			node.type === BoardRow ? node.props.item.row.title : node.props.testID.slice("board-band:".length),
		);
}

/** Sends one of the list's touch or scroll events, as the scroller would. */
export function listEvent(tree: ReactTestRenderer, handler: string, nativeEvent: Record<string, unknown> = {}) {
	act(() => boardScroller(tree).props[handler]({ nativeEvent }));
}

export const liftFinger = (tree: ReactTestRenderer) => listEvent(tree, "onTouchEnd", { touches: [] });

/** The default fleet's Live with Idle unfolded (idleUnfolded). */
export const workingOrder = [
	"needsYou:2",
	"Fix retry loop",
	"Pick a name",
	"working:1",
	"Build docs",
	"idle:3",
	"Ship it",
	"Old chore",
	"Older chore",
];
