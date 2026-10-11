import { act, type ReactTestInstance } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { flatListCalls, render, renderedText } from "./renderNative.testkit";
import { TranscriptImages } from "./TranscriptImages";

vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	Image: "Image",
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("expo-secure-store", () => ({ getItemAsync: vi.fn(async () => null) }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => ({ profiles: [] }) }));

const images = [
	{ id: "a:0", src: "data:image/png;base64,AQID", name: "first.png" },
	{ id: "a:1", src: "data:image/png;base64,BAUG", name: "second.png" },
];

function thumbnails(root: ReactTestInstance) {
	return root.findAll(
		(node) => String(node.type) === "Pressable" && /^Open image/.test(node.props.accessibilityLabel ?? ""),
	);
}

function pager(root: ReactTestInstance) {
	return root.findAll((node) => typeof node.type === "function" && node.props.pagingEnabled === true)[0];
}

describe("a row of images (spec 8.2)", () => {
	it("shows 96pt thumbnails with rounded corners", () => {
		const tree = render(<TranscriptImages images={images} hubId="hub-1" />);
		expect(thumbnails(tree.root).map((node) => node.props.style)).toEqual([
			expect.objectContaining({ width: 96, height: 96, borderRadius: 12 }),
			expect.objectContaining({ width: 96, height: 96, borderRadius: 12 }),
		]);
	});

	it("opens a viewer that swipes between the images", () => {
		const tree = render(<TranscriptImages images={images} hubId="hub-1" />);
		act(() => thumbnails(tree.root)[0].props.onPress());
		expect(renderedText(tree)).toContain("1 of 2");
		const list = pager(tree.root);
		expect(list.props.horizontal).toBe(true);
		expect(list.props.data).toEqual(images);
		act(() => list.props.onMomentumScrollEnd({ nativeEvent: { contentOffset: { x: 390 } } }));
		expect(renderedText(tree)).toContain("2 of 2");
		for (const words of ["Previous image", "Next image"]) expect(renderedText(tree)).not.toContain(words);
	});

	it("opens at the image you tapped, and closes with Done", () => {
		const tree = render(<TranscriptImages images={images} hubId="hub-1" />);
		act(() => thumbnails(tree.root)[1].props.onPress());
		expect(renderedText(tree)).toContain("2 of 2");
		expect(pager(tree.root).props.initialScrollIndex).toBe(1);
		act(() =>
			tree.root
				.findAll((node) => node.props.accessibilityLabel === "Done" && typeof node.props.onPress === "function")[0]
				.props.onPress(),
		);
		expect(pager(tree.root)).toBeUndefined();
	});

	it("frames the viewer in the shared modal sheet, with the image named in the header", () => {
		const tree = render(<TranscriptImages images={images} hubId="hub-1" />);
		act(() => thumbnails(tree.root)[0].props.onPress());
		expect(tree.root.findByType("Modal" as never).props.presentationStyle).toBe("pageSheet");
		// The name is the header title; the count rides in the accessory so a
		// long name can't clip the position off the end of the title.
		const title = tree.root.findByProps({ accessibilityRole: "header" });
		expect(title.props.children).toBe("first.png");
		expect(renderedText(tree)).toContain("1 of 2");
	});

	it("pages by the sheet's own width, not the window's, on a narrow page sheet", () => {
		const tree = render(<TranscriptImages images={images} hubId="hub-1" />);
		act(() => thumbnails(tree.root)[0].props.onPress());
		// An iPad page sheet is a centered card narrower than the 390pt window.
		act(() => pager(tree.root).props.onLayout({ nativeEvent: { layout: { width: 300 } } }));
		expect(pager(tree.root).props.getItemLayout([], 1)).toEqual({ length: 300, offset: 300, index: 1 });
		expect(pager(tree.root).props.renderItem({ item: images[0] }).props.style.width).toBe(300);
		act(() => pager(tree.root).props.onMomentumScrollEnd({ nativeEvent: { contentOffset: { x: 300 } } }));
		expect(renderedText(tree)).toContain("2 of 2");
	});
});

describe("the viewer with VoiceOver", () => {
	it("moves between images without a swipe: swipe up or down on it", () => {
		const tree = render(<TranscriptImages images={images} hubId="hub-1" />);
		act(() => thumbnails(tree.root)[0].props.onPress());
		const list = pager(tree.root);
		expect(list.props.accessibilityRole).toBe("adjustable");
		expect(list.props.accessibilityValue).toEqual({ text: "Image 1 of 2" });
		expect(list.props.accessibilityActions.map((action: { name: string }) => action.name)).toEqual([
			"increment",
			"decrement",
		]);
		flatListCalls.length = 0;
		act(() => list.props.onAccessibilityAction({ nativeEvent: { actionName: "increment" } }));
		expect(renderedText(tree)).toContain("2 of 2");
		expect(flatListCalls).toEqual([{ method: "scrollToIndex", args: { index: 1, animated: true } }]);
		// It stops at the last image rather than wrapping.
		act(() => pager(tree.root).props.onAccessibilityAction({ nativeEvent: { actionName: "increment" } }));
		expect(renderedText(tree)).toContain("2 of 2");
		act(() => pager(tree.root).props.onAccessibilityAction({ nativeEvent: { actionName: "decrement" } }));
		expect(renderedText(tree)).toContain("1 of 2");
	});
});
