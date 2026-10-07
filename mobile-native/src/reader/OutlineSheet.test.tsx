// The outline sheet (ruling 26): the Reader's headings, indented by depth;
// tapping one closes the sheet and jumps the Reader there.
import type { ReactTestInstance, ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { pressable, render, textOf } from "../renderNative.testkit";
import { sheetKey } from "../sheet/sheetHosts";
import { OutlineSheet } from "./OutlineSheet";
import { type ReaderHost, readerHosts } from "./readerHosts";

const navigation = vi.hoisted(() => ({ goBack: vi.fn(), dispatch: vi.fn() }));

vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("@react-navigation/native", () => ({
	useNavigation: () => navigation,
	usePreventRemove: () => {},
}));

const KEY = sheetKey("studio", "local:fix", "docs/plan.md");
const owner = {};
const order: string[] = [];

beforeEach(() => {
	order.length = 0;
	navigation.goBack.mockReset();
	navigation.goBack.mockImplementation(() => order.push("goBack"));
});
afterEach(() => {
	readerHosts.release(KEY, owner);
});

function mount(host: ReaderHost) {
	act(() => readerHosts.provide(KEY, owner, host));
	const tree = render(
		<OutlineSheet
			route={
				{
					key: "outline",
					name: "OutlineSheet",
					params: { hubId: "studio", sessionRef: "local:fix", path: "docs/plan.md" },
				} as never
			}
			navigation={navigation as never}
		/>,
	);
	return tree;
}

function rows(tree: ReactTestRenderer): ReactTestInstance[] {
	return tree.root.findAll(
		(node) =>
			typeof node.type === "string" &&
			node.props.accessibilityRole === "button" &&
			node.props.accessibilityHint === "Jumps to this heading",
	);
}

it("lists the headings in order, indented by depth", () => {
	const tree = mount({
		outline: [
			{ index: 0, depth: 1, title: "Settle the race" },
			{ index: 3, depth: 2, title: "Goal" },
			{ index: 7, depth: 3, title: "Step one" },
		],
		jumpTo: vi.fn(),
		anchor: () => null,
		canReview: false,
	});
	const listed = rows(tree);
	expect(listed.map((row) => textOf(row))).toEqual(["Settle the race", "Goal", "Step one"]);
	const indents = listed.map((row) => (row.props.style as { paddingLeft: number }).paddingLeft);
	expect(indents[1]).toBeGreaterThan(indents[0] ?? 0);
	expect(indents[2]).toBeGreaterThan(indents[1] ?? 0);
});

it("closes, then jumps the Reader to the heading you tap", () => {
	const jumpTo = vi.fn((index: number) => order.push(`jumpTo ${index}`));
	const tree = mount({
		outline: [
			{ index: 0, depth: 1, title: "Top" },
			{ index: 5, depth: 2, title: "Goal" },
		],
		jumpTo,
		anchor: () => null,
		canReview: false,
	});
	act(() => pressable(tree, "Goal")?.props.onPress());
	expect(order).toEqual(["goBack", "jumpTo 5"]);
});
