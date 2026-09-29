import { ScrollView, Text } from "react-native";
import { act, type ReactTestRendererJSON } from "react-test-renderer";
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, renderedText } from "../renderNative.testkit";
import { Sheet } from "./Sheet";

// The text size the phone is set to: 1 is the default (Large).
const text = vi.hoisted(() => ({ fontScale: 1 }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	useWindowDimensions: () => ({ fontScale: text.fontScale, scale: 2, width: 390, height: 844 }),
}));

describe("the sheet's chrome", () => {
	const pressable = (tree: ReturnType<typeof render>, label: string) =>
		tree.root.find((node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === label);

	it("draws the header and the body as the screen's only two children", () => {
		const cancel = vi.fn();
		const add = vi.fn();
		const tree = render(
			<Sheet
				title="Comment"
				onCancel={cancel}
				done={{ label: "Add", disabled: true, onPress: add }}
				accessory={<Text>Pinned under the title</Text>}
			>
				<ScrollView />
			</Sheet>,
		);
		const children = tree.toJSON() as ReactTestRendererJSON[];
		expect(children.map((child) => child.type)).toEqual(["View", "ScrollView"]);
		expect(children[0]?.props.collapsable).toBe(false);
		expect(renderedText(tree)).toContain("Comment");
		expect(renderedText(tree)).toContain("Pinned under the title");
		act(() => pressable(tree, "Cancel").props.onPress());
		expect(cancel).toHaveBeenCalledOnce();
		expect(pressable(tree, "Add").props.disabled).toBe(true);
	});

	it("reads Done unless the sheet names its own verb, and leaves out what it isn't given", () => {
		const done = vi.fn();
		const tree = render(
			<Sheet title="Tasks" done={{ onPress: done }}>
				<ScrollView />
			</Sheet>,
		);
		act(() => pressable(tree, "Done").props.onPress());
		expect(done).toHaveBeenCalledOnce();
		expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Cancel")).toEqual([]);
	});

	it("says Done is busy while its action runs", () => {
		const tree = render(
			<Sheet title="Edit hub" done={{ label: "Saving…", disabled: true, busy: true, onPress: () => {} }}>
				<ScrollView />
			</Sheet>,
		);
		expect(pressable(tree, "Saving…").props.accessibilityState).toEqual({ disabled: true, busy: true });
	});

	it("dims Cancel while it can't run, as it dims Done", () => {
		const cancel = vi.fn();
		const tree = render(
			<Sheet title="Edit hub" onCancel={cancel} cancelDisabled>
				<ScrollView />
			</Sheet>,
		);
		expect(pressable(tree, "Cancel").props.disabled).toBe(true);
	});

	it("caps a long title's width, as the prototype does, so it truncates before it squeezes Cancel", () => {
		const tree = render(
			<Sheet title="Sign in to codex-jesse-fsck.com" onCancel={() => {}}>
				<ScrollView />
			</Sheet>,
		);
		const title = tree.root.findByProps({ accessibilityRole: "header" });
		expect(title.props.numberOfLines).toBe(1);
		expect(title.props.style.maxWidth).toBe("56%");
		expect(title.props.children).toBe("Sign in to codex-jesse-fsck.com");
	});

	describe("at the largest text sizes (#3311)", () => {
		afterEach(() => {
			text.fontScale = 1;
		});

		it("lets its title follow Dynamic Type all the way, since it truncates to one line anyway", () => {
			text.fontScale = 53 / 17;
			const tree = render(
				<Sheet title="Sign in to codex" onCancel={() => {}}>
					<Text>body</Text>
				</Sheet>,
			);
			expect(tree.root.findByProps({ accessibilityRole: "header" }).props.style.fontSize).toBe(53);
		});

		it("keeps the title's room when Done's label is long: the side slots share only what's left", () => {
			const tree = render(
				<Sheet title="Sign in to codex" onCancel={() => {}} done={{ label: "Terminer et enregistrer", onPress: () => {} }}>
					<Text>body</Text>
				</Sheet>,
			);
			const title = tree.root.findByProps({ accessibilityRole: "header" });
			const row = title.parent;
			const slots = (row?.children ?? []).filter((child) => typeof child !== "string" && child !== title);
			for (const slot of slots) expect(typeof slot !== "string" && slot.props.style).toMatchObject({ flex: 1 });
		});
	});
});

