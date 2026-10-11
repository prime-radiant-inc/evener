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
		// The actions are grouped, and a Group divides its rows with hairlines
		// (seven rows -> six).
		expect(tree.root.findAll((node) => node.props.testID === "hairline")).toHaveLength(6);
		// And each action is a Row, whose state carries only `disabled`: a button
		// that also carried `expanded` would have the key present (undefined
		// here), which toStrictEqual, not toEqual, notices.
		const [find] = tree.root.findAll(
			(node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === "Find in session",
		);
		expect(find?.props.accessibilityState).toStrictEqual({ disabled: false });
	});
});
