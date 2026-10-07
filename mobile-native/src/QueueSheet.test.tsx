// The Queue sheet (ruling 37): every queued message the session's ghosts
// carry, with the same actions, over the session that provides them.
import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { paletteFor } from "./design/tokens";
import { type QueueHost, QueueSheet, queueHosts } from "./QueueSheet";
import { pressable, render, renderedText, unmountMountedTrees } from "./renderNative.testkit";
import type { Ghost } from "./session/ghosts";
import { sheetKey } from "./sheet/sheetHosts";

const navigation = vi.hoisted(() => ({ goBack: vi.fn(), dispatch: vi.fn() }));

vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	ActionSheetIOS: { showActionSheetWithOptions: vi.fn() },
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("react-native-gesture-handler/ReanimatedSwipeable", async () =>
	(await import("./renderNative.testkit")).gestureHandlerModuleMock(),
);
vi.mock("react-native-gesture-handler", async () =>
	(await import("./renderNative.testkit")).gestureDetectorModuleMock(),
);
vi.mock("@react-navigation/native", () => ({
	useNavigation: () => navigation,
	usePreventRemove: () => {},
}));

const queuedGhost = (n: number): Ghost => ({
	key: `queue:queue_${n}`,
	state: "queued",
	text: `message ${n}`,
	caption: "Queued · sends when this turn ends",
	buttons: ["steerNow"],
	menu: ["edit", "cancel"],
	origin: { kind: "queue", entry: { index: n - 1, id: `queue_${n}` } },
});
const heldGhost = (n: number): Ghost => ({
	...queuedGhost(n),
	state: "held",
	caption: "Held · you stopped this turn",
	buttons: ["sendNow", "cancel"],
});

const KEY = sheetKey("hub-1", "local:s1");
const owner = {};

beforeEach(() => {
	navigation.goBack.mockReset();
	navigation.dispatch.mockReset();
});
afterEach(() => {
	unmountMountedTrees();
	queueHosts.release(KEY, owner);
});

function host(over: Partial<QueueHost> = {}): QueueHost {
	return {
		ghosts: [heldGhost(1), heldGhost(2), heldGhost(3), heldGhost(4)],
		disabled: false,
		act: vi.fn(async () => null),
		showOnSession: vi.fn(),
		...over,
	};
}

function mountSheet(provided: QueueHost | undefined) {
	if (provided) queueHosts.provide(KEY, owner, provided);
	const tree = render(
		<QueueSheet
			route={{ key: "queue", name: "QueueSheet", params: { hubId: "hub-1", ref: "local:s1" } } as never}
			navigation={navigation as never}
		/>,
	);
	return tree;
}

async function flush() {
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, 0));
	});
}

it("lists every queued message the session carries, not only the three the transcript shows", () => {
	const tree = mountSheet(host());
	const text = renderedText(tree);
	expect(text).toContain("Queued messages");
	for (const n of [1, 2, 3, 4]) expect(text).toContain(`message ${n}`);
	expect(text).not.toContain("more queued");
	expect(pressable(tree, "Steer all now")).toBeUndefined();
});

it("paints a swiped message the sheet's canvas, since the bubble itself is unfilled", () => {
	const tree = mountSheet(host());
	const contents = tree.root.findAllByProps({ testID: "swipe-row-content" });
	expect(contents).toHaveLength(4);
	for (const content of contents) expect(content.props.style).toEqual({ backgroundColor: paletteFor("light").canvas });
});

it("offers Steer all now only when the session can drain its queue", async () => {
	const steerAll = vi.fn(async () => null);
	const tree = mountSheet(host({ ghosts: [queuedGhost(1), queuedGhost(2)], steerAll }));
	const button = pressable(tree, "Steer all now");
	expect(button).toBeDefined();
	await act(async () => button?.props.onPress());
	expect(steerAll).toHaveBeenCalledOnce();
});

it("runs Cancel through the host and shows the toast it returns in the sheet", async () => {
	const act_ = vi.fn(async () => ({ text: "Couldn't take this message out of the queue." }));
	const provided = host({ act: act_ });
	const tree = mountSheet(provided);
	const cancel = pressable(tree, "Cancel");
	await act(async () => cancel?.props.onPress());
	await flush();
	expect(act_).toHaveBeenCalledWith(provided.ghosts[0], "cancel");
	expect(renderedText(tree)).toContain("Couldn't take this message out of the queue.");
	expect(navigation.goBack).not.toHaveBeenCalled();
});

it("closes before Edit, so the message lands in the composer", async () => {
	const calls: string[] = [];
	navigation.goBack.mockImplementation(() => calls.push("goBack"));
	const act_ = vi.fn(async (_ghost: Ghost, action: string) => {
		calls.push(`act:${action}`);
		return null;
	});
	const tree = mountSheet(host({ ghosts: [queuedGhost(1)], act: act_ }));
	const { ActionSheetIOS } = await import("react-native");
	act(() => pressable(tree, "message 1. Queued · sends when this turn ends")?.props.onPress());
	const [, pick] = vi.mocked(ActionSheetIOS.showActionSheetWithOptions).mock.calls.at(-1) as [
		unknown,
		(index: number) => void,
	];
	await act(async () => pick(0));
	expect(calls).toEqual(["goBack", "act:edit"]);
});

it("shows an Edit's toast on the session, since the sheet has closed", async () => {
	const moved = { text: "Moved to your message, but it's still queued." };
	const showOnSession = vi.fn();
	const tree = mountSheet(host({ ghosts: [queuedGhost(1)], act: vi.fn(async () => moved), showOnSession }));
	const { ActionSheetIOS } = await import("react-native");
	act(() => pressable(tree, "message 1. Queued · sends when this turn ends")?.props.onPress());
	const [, pick] = vi.mocked(ActionSheetIOS.showActionSheetWithOptions).mock.calls.at(-1) as [
		unknown,
		(index: number) => void,
	];
	await act(async () => pick(0));
	await flush();
	expect(showOnSession).toHaveBeenCalledWith(moved);
	expect(renderedText(tree)).not.toContain(moved.text);
});

it("closes when the queue empties, or when its session is gone", () => {
	mountSheet(host({ ghosts: [] }));
	expect(navigation.goBack).toHaveBeenCalledOnce();
	navigation.goBack.mockClear();
	queueHosts.release(KEY, owner);
	mountSheet(undefined);
	expect(navigation.goBack).toHaveBeenCalled();
});

it("has no refresh and never asks you to reconnect: it follows the queue live", () => {
	const tree = mountSheet(host());
	expect(renderedText(tree).toLowerCase()).not.toContain("reconnect");
	expect(pressable(tree, "Refresh queue")).toBeUndefined();
});
