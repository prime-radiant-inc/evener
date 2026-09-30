// The session actions sheet (Android's ⋯ menu) pads its own bottom safe area:
// the session screen leaves that edge to its bottom bar, and the menu opens in
// a modal over it, so it can't rely on the screen for it.
import { describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { render } from "./renderNative.testkit";
import { SessionMenu } from "./SessionMenu";

vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("./alerts/HoldingModal", () => ({ HoldingModal: (props: { children?: ReactNode }) => props.children ?? null }));

describe("SessionMenu", () => {
	it("keeps its actions above the home indicator on its own", () => {
		const tree = render(
			<SessionMenu
				title="Session"
				hubName="Hub"
				connected
				deletionAvailable={false}
				close={() => {}}
				choose={() => {}}
			/>,
		);
		const [sheet] = tree.root.findAll((node) => String(node.type) === "SafeAreaView");
		expect(sheet?.props.edges).toContain("bottom");
	});

	it("lays its actions out as grouped rows", () => {
		const tree = render(
			<SessionMenu title="Session" hubName="Hub" connected deletionAvailable close={() => {}} choose={() => {}} />,
		);
		// The legacy Action list drew bare buttons with nothing between them; the
		// new design groups them, and a Group divides its rows with hairlines
		// (seven rows -> six).
		expect(tree.root.findAll((node) => node.props.testID === "hairline")).toHaveLength(6);
		// And each action is a Row, not the legacy Action: Action built its state
		// as `{ disabled, expanded }`, so `expanded` was a present key (undefined
		// here) that a Row's `{ disabled }` doesn't carry. toStrictEqual, not
		// toEqual, is what notices the extra key.
		const [find] = tree.root.findAll(
			(node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === "Find in session",
		);
		expect(find?.props.accessibilityState).toStrictEqual({ disabled: false });
	});
});
