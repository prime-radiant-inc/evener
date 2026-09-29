// The ghosts above the composer as a person sees them: each bubble's text,
// caption and buttons, the menu a tap opens, and the row that leads to the
// rest of the queue.
import { createElement, useEffect } from "react";
import type { ReactTestRenderer } from "react-test-renderer";
import { act } from "react-test-renderer";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { paletteFor } from "../design/tokens";
import {
	keyboard,
	playedHaptics,
	pressable,
	render,
	renderedText,
	swipeableCalls,
	swipeRowFully,
} from "../renderNative.testkit";
import { GhostBubble } from "./GhostBubble";
import type { Ghost, GhostAction } from "./ghosts";
import { QueuedMessages } from "./QueuedMessages";

const native = vi.hoisted(() => ({ showActionSheetWithOptions: vi.fn() }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	ActionSheetIOS: { showActionSheetWithOptions: native.showActionSheetWithOptions },
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("../renderNative.testkit")).gestureHandlerModuleMock(),
);
vi.mock("react-native-gesture-handler", async () =>
	(await import("../renderNative.testkit")).gestureDetectorModuleMock(),
);

beforeEach(() => {
	native.showActionSheetWithOptions.mockReset();
	swipeableCalls.closes = 0;
	keyboard.reset();
});

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
		row: {
			clientMutationId: "cmid-9",
			status: "rejected",
			text: "recover this message",
			actions: ["restore", "discard"],
		},
	},
};

function mount(
	ghosts: readonly Ghost[],
	{
		disabled = false,
		canEdit = true,
		editHint = null as string | null,
		backdrop = "surface" as "surface" | "page",
		// The keyboard is up, for the composer.
		typing = false,
		// Whether a keyboard up is the composer's.
		composerKeyboard = true,
	} = {},
) {
	const onAction = vi.fn<(ghost: Ghost, action: GhostAction) => void>();
	const onMore = vi.fn();
	const tree = render(
		<QueuedMessages
			ghosts={ghosts}
			disabled={disabled}
			canEdit={canEdit}
			editHint={editHint}
			backdrop={backdrop}
			composerKeyboard={composerKeyboard}
			onAction={onAction}
			onMore={onMore}
		/>,
	);
	const setTyping = (next: boolean) => act(() => (next ? keyboard.show() : keyboard.hide()));
	if (typing) setTyping(true);
	return { tree, onAction, onMore, setTyping };
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
			backdrop="surface"
			draftAttachments={<Image accessibilityLabel="Image 1: proof.png" />}
			onAction={() => {}}
			onMore={() => {}}
		/>,
	);
	const images = tree.root.findAll(
		(node) => String(node.type) === "Image" && node.props.accessibilityLabel === "Image 1: proof.png",
	);
	expect(images).toHaveLength(1);
	const bubble = pressable(tree, `${unconfirmed.text}. ${unconfirmed.caption}`);
	expect(
		bubble?.findAll((node) => String(node.type) === "Image" && node.props.accessibilityLabel === "Image 1: proof.png"),
	).toHaveLength(1);
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
		playedHaptics.length = 0;
		pick(0);
		expect(onAction).toHaveBeenCalledWith(queued, "edit");
		pick(2);
		expect(onAction).toHaveBeenCalledTimes(1);
		expect(playedHaptics).toEqual([]);
		// Spec 16.6: rigid on the destructive choice.
		pick(1);
		expect(onAction).toHaveBeenLastCalledWith(queued, "cancel");
		expect(playedHaptics).toEqual(["impact:rigid"]);
	});

	it("asks with a short title on Android, and the message itself beneath it", async () => {
		const { Platform } = await import("react-native");
		const { alertRequests } = await import("../renderNative.testkit");
		const was = Platform.OS;
		Object.assign(Platform, { OS: "android" });
		try {
			alertRequests.length = 0;
			const { tree, onAction } = mount([queued]);
			press(tree, `${queued.text}. ${queued.caption}`);
			expect(alertRequests).toHaveLength(1);
			const [ask] = alertRequests;
			expect(ask?.title).toBe(queued.caption);
			expect(ask?.message).toBe(queued.text);
			expect(ask?.buttons?.map((button) => button.text)).toEqual(["Edit", "Cancel message", "Cancel"]);
			ask?.buttons?.[0]?.onPress?.();
			expect(onAction).toHaveBeenCalledWith(queued, "edit");
		} finally {
			Object.assign(Platform, { OS: was });
		}
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

describe("swiping a ghost left (spec 8.5)", () => {
	const swipeables = (tree: ReactTestRenderer) => tree.root.findAllByType("ReanimatedSwipeable" as never);
	const swipeLeft = (tree: ReactTestRenderer, pageX = 200) => swipeRowFully(swipeables(tree)[0], "left", { pageX });

	it.each([
		["queued", queued],
		["held", held],
	] as const)("cancels a %s message on a full swipe, through the same action as its menu", (_state, ghost) => {
		const { tree, onAction } = mount([ghost]);
		expect(renderedText(render(swipeables(tree)[0]?.props.renderRightActions()))).toBe("Cancel");
		swipeLeft(tree);
		expect(onAction.mock.calls).toEqual([[ghost, "cancel"]]);
	});

	it("paints a swiped ghost what it sits on, the composer or the page, since the bubble itself is unfilled", () => {
		const palette = paletteFor("light");
		for (const backdrop of ["surface", "page"] as const) {
			const { tree } = mount([queued], { backdrop });
			expect(tree.root.findByProps({ testID: "swipe-row-content" }).props.style).toEqual({
				backgroundColor: palette[backdrop],
			});
		}
	});

	it("never cancels from a swipe that began in the screen's left edge band", () => {
		const { tree, onAction } = mount([queued]);
		swipeLeft(tree, 10);
		expect(onAction).not.toHaveBeenCalled();
	});

	it("doesn't swipe a message with nothing to cancel, or while another action runs", () => {
		for (const ghost of [steering, sending, unconfirmed, refused])
			expect(swipeables(mount([ghost]).tree)).toHaveLength(0);
		// A queued message the hub hasn't named yet can't be canceled.
		expect(swipeables(mount([{ ...queued, buttons: [], menu: [] }]).tree)).toHaveLength(0);
		expect(swipeables(mount([queued]).tree)[0]?.props.enabled).toBe(true);
		expect(swipeables(mount([queued], { disabled: true }).tree)[0]?.props.enabled).toBe(false);
	});

	it("keeps the same bubble while another action starts and ends", () => {
		let mounts = 0;
		function Attachment() {
			useEffect(() => {
				mounts += 1;
			}, []);
			return null;
		}
		const bubble = (disabled: boolean) => (
			<GhostBubble
				ghost={queued}
				disabled={disabled}
				canEdit
				editHint={null}
				backdrop="surface"
				attachments={<Attachment />}
				onAction={() => {}}
			/>
		);
		const tree = render(bubble(false));
		act(() => tree.update(bubble(true)));
		act(() => tree.update(bubble(false)));
		expect(mounts).toBe(1);
	});
});

// While you type, the queue folds to one line so the transcript keeps its
// room: how many are waiting, and with one, what you can do to it (spec 8.5).
// Everything returns when the keyboard lowers.
describe("while you type", () => {
	const second: Ghost = { ...queued, key: "queue:queue_3", text: "then deploy" };

	it("folds one queued message to its count and its action", () => {
		const { tree, onAction } = mount([queued], { typing: true });
		expect(renderedText(tree)).not.toContain(queued.text);
		expect(pressable(tree, "1 queued")).toBeDefined();
		press(tree, `Steer now, ${queued.text}`);
		expect(onAction).toHaveBeenCalledWith(queued, "steerNow");
	});

	it("folds several to their count, which opens them", () => {
		const { tree } = mount([queued, second], { typing: true });
		expect(renderedText(tree)).not.toContain(queued.text);
		expect(pressable(tree, `Steer now, ${queued.text}`)).toBeUndefined();
		press(tree, "2 queued");
		expect(renderedText(tree)).toContain(queued.text);
		expect(renderedText(tree)).toContain(second.text);
	});

	it("says a held message is held, and offers sending it", () => {
		const { tree, onAction } = mount([held], { typing: true });
		expect(pressable(tree, "1 held")).toBeDefined();
		expect(pressable(tree, "Cancel")).toBeUndefined();
		press(tree, `Send now, ${held.text}`);
		expect(onAction).toHaveBeenCalledWith(held, "sendNow");
	});

	it("keeps the separator out of VoiceOver", () => {
		const { tree } = mount([queued], { typing: true });
		const dot = tree.root.findAll((node) => String(node.type) === "Text" && node.props.children === "·")[0];
		expect(dot?.props).toMatchObject({ accessibilityElementsHidden: true, importantForAccessibility: "no" });
	});

	it("holds the action while another ghost action runs", () => {
		const { tree, onAction } = mount([queued], { typing: true, disabled: true });
		const action = pressable(tree, `Steer now, ${queued.text}`);
		expect(action?.props.accessibilityState).toEqual({ disabled: true });
		act(() => action?.props.onPress());
		expect(onAction).not.toHaveBeenCalled();
	});

	it("keeps showing what isn't in the queue", () => {
		const { tree } = mount([steering, refused, queued], { typing: true });
		expect(renderedText(tree)).toContain(steering.text);
		expect(renderedText(tree)).toContain(refused.text);
		expect(renderedText(tree)).not.toContain(queued.text);
		expect(pressable(tree, "1 queued")).toBeDefined();
	});

	it("stays open while the keyboard is up for something other than the composer", () => {
		const { tree } = mount([queued], { typing: true, composerKeyboard: false });
		expect(renderedText(tree)).toContain(queued.text);
		expect(pressable(tree, "1 queued")).toBeUndefined();
	});

	it("shows nothing extra with nothing queued", () => {
		const { tree } = mount([steering], { typing: true });
		expect(renderedText(tree)).toContain(steering.text);
		expect(renderedText(tree)).not.toContain("queued");
	});

	it("opens to the messages on a tap, and folds again the next time you type", () => {
		const { tree, setTyping } = mount([queued], { typing: true });
		press(tree, "1 queued");
		expect(renderedText(tree)).toContain(queued.text);
		setTyping(false);
		expect(renderedText(tree)).toContain(queued.text);
		setTyping(true);
		expect(renderedText(tree)).not.toContain(queued.text);
		expect(pressable(tree, "1 queued")).toBeDefined();
	});

	it("shows everything again when the keyboard lowers", () => {
		const { tree, setTyping } = mount([queued, second], { typing: true });
		setTyping(false);
		expect(renderedText(tree)).toContain(queued.text);
		expect(renderedText(tree)).toContain(second.text);
		expect(pressable(tree, "2 queued")).toBeUndefined();
	});
});
