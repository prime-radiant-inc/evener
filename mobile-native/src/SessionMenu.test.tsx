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
});
