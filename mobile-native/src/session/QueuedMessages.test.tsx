// The ghosts above the composer as a person sees them: each bubble's text,
// caption and buttons, the menu a tap opens, and the row that leads to the
// rest of the queue.
import { createElement } from "react";
import type { ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { pressable, render, renderedText } from "../renderNative.testkit";
import type { Ghost, GhostAction } from "./ghosts";
import { QueuedMessages } from "./QueuedMessages";

const native = vi.hoisted(() => ({ showActionSheetWithOptions: vi.fn() }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	ActionSheetIOS: { showActionSheetWithOptions: native.showActionSheetWithOptions },
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

beforeEach(() => native.showActionSheetWithOptions.mockReset());

const Image = (props: { accessibilityLabel: string }) => createElement("Image", props);

const queued: Ghost = {
	key: "queue:queue_1",
	state: "queued",
	text: "run the tests again",
	caption: "Queued · sends when this turn ends",
	buttons: ["steerNow"],
	menu: ["edit", "cancel"],
	origin: { kind: "queue", entry: { index: 0, id: "queue_1" } },
};
const held: Ghost = {
	...queued,
	key: "queue:queue_2",
	state: "held",
	caption: "Held · you stopped this turn",
	buttons: ["sendNow", "cancel"],
};
const steering: Ghost = {
	key: "pending:a",
	state: "steering",
	text: "look at the logs",
	caption: "Steering · arrives at the next step",
	buttons: [],
	menu: [],
	origin: { kind: "pending", clientMutationId: "a" },
};
const sending: Ghost = { ...steering, key: "pending:b", state: "sending", text: "hello", caption: "Sending…" };
const unconfirmed: Ghost = {
	key: "draft:unconfirmed",
	state: "unconfirmed",
	text: "maybe sent",
	caption: "Couldn't confirm this was sent",
	buttons: ["check", "discard"],
	menu: ["edit"],
	origin: { kind: "draft" },
};
const refused: Ghost = {
	key: "recovery:cmid-9",
	state: "refused",
	text: "recover this message",
	caption: "Couldn't send this · daemon refused",
	buttons: ["edit", "discard"],
	menu: [],
	origin: {
		kind: "recovery",
		row: { clientMutationId: "cmid-9", status: "rejected", text: "recover this message", actions: ["restore", "discard"] },
	},
};

function mount(
	ghosts: readonly Ghost[],
	{ disabled = false, canEdit = true, editHint = null as string | null } = {},
) {
	const onAction = vi.fn<(ghost: Ghost, action: GhostAction) => void>();
	const onMore = vi.fn();
	const tree = render(
		<QueuedMessages
			ghosts={ghosts}
			disabled={disabled}
			canEdit={canEdit}
			editHint={editHint}
			onAction={onAction}
			onMore={onMore}
		/>,
	);
	return { tree, onAction, onMore };
}

function press(tree: ReactTestRenderer, label: string) {
	const target = pressable(tree, label);
	if (!target) throw new Error(`no pressable labelled ${label}`);
	act(() => target.props.onPress());
}

describe("each ghost says what it is and what you can do", () => {
	it.each([
		["queued", queued, ["Steer now"]],
		["held", held, ["Send now", "Cancel"]],
		["steering", steering, []],
		["sending", sending, []],
		["unconfirmed", unconfirmed, ["Check", "Discard"]],
		["refused", refused, ["Edit", "Discard"]],
	] as const)("%s", (_state, ghost, buttons) => {
		const { tree } = mount([ghost]);
		expect(renderedText(tree)).toContain(ghost.text);
		expect(renderedText(tree)).toContain(ghost.caption);
		const bubble = pressable(tree, `${ghost.text}. ${ghost.caption}`);
		expect(bubble).toBeDefined();
		for (const label of buttons) expect(pressable(tree, label)).toBeDefined();
		const offered = tree.root
			.findAll(
				(node) => String(node.type) === "Pressable" && node.props.accessibilityRole === "button" && node !== bubble,
			)
			.map((node) => node.props.accessibilityLabel);
		expect(offered).toEqual(buttons);
	});

	it("exposes the bubble as a button only while it has a menu to open", () => {
		const bubbleOf = (tree: ReactTestRenderer, ghost: Ghost) => pressable(tree, `${ghost.text}. ${ghost.caption}`);
		const live = mount([queued]).tree;
		expect(bubbleOf(live, queued)?.props).toMatchObject({
			accessibilityRole: "button",
			accessibilityState: { disabled: false },
			disabled: false,
		});
		// Nothing to open: not a control, so VoiceOver doesn't stop on a dead button.
		const inert = mount([steering]).tree;
		expect(bubbleOf(inert, steering)?.props.accessibilityRole).toBeUndefined();
		expect(bubbleOf(inert, steering)?.props.disabled).toBe(true);
		// Another action is running.
		const busy = mount([queued], { disabled: true }).tree;
		expect(bubbleOf(busy, queued)?.props).toMatchObject({ accessibilityState: { disabled: true }, disabled: true });
		// Its only menu item is an Edit the composer can't take right now.
		const blocked = mount([unconfirmed], { canEdit: false, editHint: "Clear it first." }).tree;
		expect(bubbleOf(blocked, unconfirmed)?.props.accessibilityRole).toBeUndefined();
		expect(bubbleOf(blocked, unconfirmed)?.props.disabled).toBe(true);
	});

	it("runs a button's action on the ghost it belongs to", () => {
		const { tree, onAction } = mount([held, refused]);
		press(tree, "Send now");
		expect(onAction).toHaveBeenCalledWith(held, "sendNow");
		press(tree, "Discard");
		expect(onAction).toHaveBeenCalledWith(refused, "discard");
	});

	it("colors a refused message's caption as a failure", () => {
		const { tree } = mount([refused]);
		const caption = tree.root.findAll(
			(node) => String(node.type) === "Text" && node.props.children === refused.caption,
		)[0];
		const plain = mount([queued]).tree.root.findAll(
			(node) => String(node.type) === "Text" && node.props.children === queued.caption,
		)[0];
		expect(caption?.props.style.color).not.toBe(plain?.props.style.color);
	});
});

it("says why an action you'd expect isn't there", () => {
	const note = "This message carried an image, so it can't be restored to the draft here.";
	const { tree } = mount([{ ...refused, buttons: ["discard"], note }]);
	expect(renderedText(tree)).toContain(note);
	expect(pressable(tree, "Edit")).toBeUndefined();
});

it("shows the images an unconfirmed send carried in its own bubble, and only there", () => {
	const tree = render(
		<QueuedMessages
			ghosts={[queued, unconfirmed]}
			disabled={false}
			canEdit
			editHint={null}
			draftAttachments={<Image accessibilityLabel="Image 1: proof.png" />}
			onAction={() => {}}
			onMore={() => {}}
		/>,
	);
	const images = tree.root.findAll((node) => String(node.type) === "Image" && node.props.accessibilityLabel === "Image 1: proof.png");
	expect(images).toHaveLength(1);
	const bubble = pressable(tree, `${unconfirmed.text}. ${unconfirmed.caption}`);
	expect(bubble?.findAll((node) => String(node.type) === "Image" && node.props.accessibilityLabel === "Image 1: proof.png")).toHaveLength(1);
});

describe("tapping a ghost", () => {
	it("opens its menu, with Cancel last, and runs the choice", () => {
		const { tree, onAction } = mount([queued]);
		press(tree, `${queued.text}. ${queued.caption}`);
		const [options, pick] = native.showActionSheetWithOptions.mock.calls.at(-1) as [
			{ options: string[]; cancelButtonIndex: number; destructiveButtonIndex?: number },
			(index: number) => void,
		];
		expect(options.options).toEqual(["Edit", "Cancel message", "Cancel"]);
		expect(options.cancelButtonIndex).toBe(2);
		expect(options.destructiveButtonIndex).toBe(1);
		pick(0);
		expect(onAction).toHaveBeenCalledWith(queued, "edit");
		pick(2);
		expect(onAction).toHaveBeenCalledTimes(1);
	});

	it("does nothing when there is nothing to offer", () => {
		const { tree } = mount([steering]);
		press(tree, `${steering.text}. ${steering.caption}`);
		expect(native.showActionSheetWithOptions).not.toHaveBeenCalled();
	});
});

describe("Edit when the composer can't take the message back", () => {
	it("disables Edit and says why", () => {
		const hint = "Clear or send your current draft to restore this message.";
		const { tree, onAction } = mount([refused], { canEdit: false, editHint: hint });
		expect(pressable(tree, "Edit")?.props.accessibilityState).toMatchObject({ disabled: true });
		expect(renderedText(tree)).toContain(hint);
		expect(pressable(tree, "Discard")?.props.accessibilityState).toMatchObject({ disabled: false });
		act(() => pressable(tree, "Edit")?.props.onPress());
		expect(onAction).not.toHaveBeenCalled();
	});

	it("leaves Edit out of the menu, and says why", () => {
		const hint = "Clear or send your current draft to restore this message.";
		const { tree } = mount([unconfirmed], { canEdit: false, editHint: hint });
		expect(renderedText(tree)).toContain(hint);
		press(tree, `${unconfirmed.text}. ${unconfirmed.caption}`);
		expect(native.showActionSheetWithOptions).not.toHaveBeenCalled();
	});

	it("still edits a queued message, which merges into whatever you are typing", () => {
		const { tree, onAction } = mount([queued], { canEdit: false, editHint: "Clear it first." });
		expect(renderedText(tree)).not.toContain("Clear it first.");
		press(tree, `${queued.text}. ${queued.caption}`);
		const [options, pick] = native.showActionSheetWithOptions.mock.calls.at(-1) as [
			{ options: string[] },
			(index: number) => void,
		];
		expect(options.options).toEqual(["Edit", "Cancel message", "Cancel"]);
		pick(0);
		expect(onAction).toHaveBeenCalledWith(queued, "edit");
	});
});

it("fires nothing while another action runs", () => {
	const { tree, onAction } = mount([held, unconfirmed], { disabled: true });
	for (const label of ["Send now", "Cancel", "Check", "Discard"]) {
		const button = pressable(tree, label);
		expect(button?.props.accessibilityState).toMatchObject({ disabled: true });
		act(() => button?.props.onPress());
	}
	press(tree, `${held.text}. ${held.caption}`);
	expect(native.showActionSheetWithOptions).not.toHaveBeenCalled();
	expect(onAction).not.toHaveBeenCalled();
});

it("shows three queued messages, counts the rest, and opens them", () => {
	const five = [1, 2, 3, 4, 5].map(
		(n): Ghost => ({
			...queued,
			key: `queue:queue_${n}`,
			text: `message ${n}`,
			origin: { kind: "queue", entry: { index: n - 1, id: `queue_${n}` } },
		}),
	);
	const { tree, onMore } = mount([...five, unconfirmed]);
	const text = renderedText(tree);
	for (const shown of ["message 1", "message 2", "message 3", "maybe sent"]) expect(text).toContain(shown);
	for (const hidden of ["message 4", "message 5"]) expect(text).not.toContain(hidden);
	press(tree, "2 more queued");
	expect(onMore).toHaveBeenCalledOnce();
	expect(pressable(mount([queued]).tree, "0 more queued")).toBeUndefined();
});

it("renders nothing when nothing is waiting", () => {
	const { tree } = mount([]);
	expect(tree.toJSON()).toBeNull();
});
