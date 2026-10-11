import { ScrollView } from "react-native";
import { act } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { palettes } from "../design/tokens";
import { render } from "../renderNative.testkit";
import { ModalFrame, ModalSheet } from "./ModalSheet";
import { Sheet } from "./Sheet";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));

describe("a sheet in a modal", () => {
	it("slides up a page sheet on the canvas, with the shared header over its body", () => {
		const close = vi.fn();
		const cancel = vi.fn();
		const tree = render(
			<ModalSheet title="Edit hub" onCancel={cancel} onRequestClose={close}>
				<ScrollView />
			</ModalSheet>,
		);
		const modal = tree.root.findByType("Modal" as never);
		expect(modal.props).toMatchObject({ visible: true, animationType: "slide", presentationStyle: "pageSheet" });
		modal.props.onRequestClose();
		expect(close).toHaveBeenCalledOnce();
		const frame = tree.root.findByType("SafeAreaView" as never);
		expect(frame.props.style).toMatchObject({ flex: 1, backgroundColor: palettes.light.canvas });
		expect(tree.root.findByProps({ accessibilityRole: "header" }).props.children).toBe("Edit hub");
		act(() => tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Cancel" }).props.onPress());
		expect(cancel).toHaveBeenCalledOnce();
	});

	it("stays mounted but hidden when it isn't visible", () => {
		const tree = render(
			<ModalSheet title="lunaroute" visible={false} onRequestClose={() => {}}>
				<ScrollView />
			</ModalSheet>,
		);
		expect(tree.root.findByType("Modal" as never).props.visible).toBe(false);
	});

	it("frames a sheet that draws its own header, such as an editor with Save", () => {
		const tree = render(
			<ModalFrame onRequestClose={() => {}}>
				<Sheet title="Add provider" done={{ label: "Save", onPress: () => {} }}>
					<ScrollView />
				</Sheet>
			</ModalFrame>,
		);
		expect(tree.root.findByType("Modal" as never).props.presentationStyle).toBe("pageSheet");
		const frame = tree.root.findByType("SafeAreaView" as never);
		expect(frame.props.style).toMatchObject({ backgroundColor: palettes.light.canvas });
		expect(frame.findByProps({ accessibilityRole: "button", accessibilityLabel: "Save" })).toBeDefined();
	});
});
