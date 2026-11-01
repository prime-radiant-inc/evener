import type { LaunchOption } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FlatList } from "react-native";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { ConversationClientLike } from "./conversation/services/conversation";
import { LaunchFieldEditor } from "./LaunchFieldEditor";
import { Sheet } from "./sheet/Sheet";
import { flatListCalls, pressable, render } from "./renderNative.testkit";

const viewport = vi.hoisted(() => ({ fontScale: 1 }));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	useWindowDimensions: () => ({ width: 390, height: 844, scale: 2, fontScale: viewport.fontScale }),
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));

beforeEach(() => {
	viewport.fontScale = 1;
	flatListCalls.length = 0;
});

const option = (kind: LaunchOption["kind"], field: string): LaunchOption => ({
	field,
	perLaunch: true,
	wireField: field,
	label: field,
	group: "Launch",
	kind,
	defaultableLayers: ["global"],
});

it("names the scalar sheet and applies an edit through its pinned Done action", async () => {
	const edited = option("text", "systemPrompt");
	const apply = vi.fn();
	const tree = render(
		<LaunchFieldEditor option={edited} value={{}} effective={{}} client={null} close={() => {}} apply={apply} />,
	);
	expect(tree.root.findByType(Sheet).props.title).toBe(edited.label);
	act(() => tree.root.findByType("TextInput" as never).props.onChangeText("prompt-sentinel"));
	await act(async () => pressable(tree, "Done")?.props.onPress());
	expect(apply).toHaveBeenCalledWith("prompt-sentinel");
	act(() => tree.unmount());
});

const longEffective = "effective-sentinel\n".repeat(80);
const longDescription = "description-sentinel\n".repeat(80);

function input(tree: ReactTestRenderer, edited: LaunchOption) {
	return tree.root.findAllByType("TextInput" as never).find((node) => node.props.accessibilityLabel === edited.label);
}

async function done(tree: ReactTestRenderer) {
	await act(async () => pressable(tree, "Done")?.props.onPress());
}

function expectRefusalFirst(tree: ReactTestRenderer, owner: "ScrollView" | "FlatList") {
	// These are native props and child order, not a claim about UIKit geometry.
	const scroller = tree.root.findByType(owner as never);
	const texts = scroller.findAllByType("Text" as never);
	expect.soft(texts[0]?.props.accessibilityRole).toBe("alert");
	expect(texts.filter((node) => node.props.accessibilityRole === "alert")).toHaveLength(1);
	expect(texts[0]?.props.numberOfLines).toBeUndefined();
}

it.each(["text", "modelPicker"] as const)("keeps %s effective values selectable and uncapped", (kind) => {
	viewport.fontScale = 3.1;
	const edited = option(kind, kind === "modelPicker" ? "model" : "systemPrompt");
	const tree = render(
		<LaunchFieldEditor
			option={edited}
			value={{}}
			effective={{ [edited.field]: longEffective }}
			client={null}
			close={() => {}}
			apply={() => {}}
		/>,
	);
	const effectiveText = tree.root.findAllByType("Text" as never).find((node) => node.children.includes(longEffective));
	expect(effectiveText).toBeDefined();
	expect(effectiveText?.props.selectable).toBe(true);
	expect(effectiveText?.props.numberOfLines).toBeUndefined();
});

it.each([1, 3.1])("reveals every parse refusal and keeps the draft at text scale %s", async (fontScale) => {
	viewport.fontScale = fontScale;
	const edited = { ...option("integer", "maxRounds"), description: longDescription };
	const apply = vi.fn();
	const close = vi.fn();
	const scrollTo = vi.fn();
	const tree = render(
		<LaunchFieldEditor
			option={edited}
			value={{}}
			effective={{ maxRounds: 80 }}
			client={null}
			close={close}
			apply={apply}
		/>,
		{ createNodeMock: (node) => (node.type === "ScrollView" ? { scrollTo } : null) },
	);
	act(() => input(tree, edited)?.props.onChangeText("1.5"));
	for (let attempt = 0; attempt < 2; attempt += 1) {
		// Forget the previous reveal, as if the person scrolled away to try again.
		scrollTo.mockClear();
		await done(tree);
		expect(apply).not.toHaveBeenCalled();
		expect(close).not.toHaveBeenCalled();
		expect(input(tree, edited)?.props.value).toBe("1.5");
		expectRefusalFirst(tree, "ScrollView");
		expect.soft(scrollTo).toHaveBeenCalledExactlyOnceWith({ y: 0, animated: true });
	}
	scrollTo.mockClear();
	act(() => input(tree, edited)?.props.onChangeText("4"));
	await done(tree);
	expect(apply).toHaveBeenCalledExactlyOnceWith(4);
	expect(scrollTo).not.toHaveBeenCalled();
	expect(tree.root.findAllByProps({ accessibilityRole: "alert" })).toHaveLength(0);
});

it.each(["invalid", "rejected", "offline"] as const)(
	"reveals every %s path refusal and can apply after recovery",
	async (failure) => {
		viewport.fontScale = 3.1;
		const edited = { ...option("path", "systemPromptFile"), description: longDescription, pathKind: "file" };
		const apply = vi.fn();
		const close = vi.fn();
		const scrollTo = vi.fn();
		let valid = false;
		const request = vi.fn(async (method: string) => {
			if (method !== "evener/path/validate") throw new Error(`Unexpected RPC ${method}`);
			if (!valid && failure === "rejected") throw new Error("transport-sentinel");
			return { valid, error: valid ? undefined : "path-refusal-sentinel" };
		});
		const client = { request } as unknown as ConversationClientLike;
		const editor = (connected: boolean) => (
			<LaunchFieldEditor
				option={edited}
				value={{}}
				effective={{ systemPromptFile: longEffective }}
				client={connected ? client : null}
				close={close}
				apply={apply}
			/>
		);
		const tree = render(editor(failure !== "offline"), {
			createNodeMock: (node) => (node.type === "ScrollView" ? { scrollTo } : null),
		});
		const path = "/path-sentinel";
		act(() => input(tree, edited)?.props.onChangeText(path));
		for (let attempt = 0; attempt < 2; attempt += 1) {
			scrollTo.mockClear();
			await done(tree);
			expect(apply).not.toHaveBeenCalled();
			expect(close).not.toHaveBeenCalled();
			expect(input(tree, edited)?.props.value).toBe(path);
			expectRefusalFirst(tree, "ScrollView");
			expect.soft(scrollTo).toHaveBeenCalledExactlyOnceWith({ y: 0, animated: true });
		}
		expect(request).toHaveBeenCalledTimes(failure === "offline" ? 0 : 2);
		valid = true;
		act(() => tree.update(editor(true)));
		scrollTo.mockClear();
		await done(tree);
		expect(request).toHaveBeenLastCalledWith("evener/path/validate", { path, kind: "file" });
		expect(apply).toHaveBeenCalledExactlyOnceWith(path);
		expect(scrollTo).not.toHaveBeenCalled();
		expect(tree.root.findAllByProps({ accessibilityRole: "alert" })).toHaveLength(0);
	},
);

it.each(["text", "modelPicker"] as const)(
	"reveals repeated %s conflicts without losing the draft or its choices",
	async (kind) => {
		viewport.fontScale = 3.1;
		const edited = {
			...option(kind, kind === "modelPicker" ? "model" : "systemPrompt"),
			description: longDescription,
		};
		const apply = vi.fn();
		const close = vi.fn();
		const scrollTo = vi.fn();
		const request = vi.fn(async (method: string) => {
			if (method !== "model/list") throw new Error(`Unexpected RPC ${method}`);
			return {
				data: ["a", "b", "c"].map((model) => ({ model, provider: "local", supportsTools: true })),
				recent: [],
				diagnostics: [],
			};
		});
		const client = { request } as unknown as ConversationClientLike;
		const editor = (value: string) => (
			<LaunchFieldEditor
				option={edited}
				value={{ [edited.field]: value }}
				effective={{ [edited.field]: longEffective }}
				client={client}
				close={close}
				apply={apply}
			/>
		);
		const tree = render(editor("local/a"), {
			createNodeMock: (node) => (node.type === "ScrollView" ? { scrollTo } : null),
		});
		await act(async () => {});
		const choices = () => tree.root.findAllByProps({ accessibilityRole: "radio" });
		act(() => {
			if (kind === "modelPicker") {
				tree.root.findByType("TextInput" as never).props.onChangeText("local");
				choices()[2].props.onPress();
			} else input(tree, edited)?.props.onChangeText("local/c");
			tree.update(editor("local/b"));
		});
		for (let attempt = 0; attempt < 2; attempt += 1) {
			scrollTo.mockClear();
			flatListCalls.length = 0;
			await done(tree);
			expect(apply).not.toHaveBeenCalled();
			expect(close).not.toHaveBeenCalled();
			expectRefusalFirst(tree, kind === "modelPicker" ? "FlatList" : "ScrollView");
			if (kind === "modelPicker") {
				expect.soft(flatListCalls).toEqual([{ method: "scrollToOffset", args: { offset: 0, animated: true } }]);
				expect(choices()).toHaveLength(3);
				expect(choices()[2].props.accessibilityState.checked).toBe(true);
				expect(tree.root.findByType("TextInput" as never).props.value).toBe("local");
				expect(tree.root.findAllByType("ScrollView" as never)).toHaveLength(0);
			} else {
				expect.soft(scrollTo).toHaveBeenCalledExactlyOnceWith({ y: 0, animated: true });
				expect(input(tree, edited)?.props.value).toBe("local/c");
			}
		}
		// Matching the newer explicit value is still allowed, and Done applies it.
		act(() => {
			if (kind === "modelPicker") choices()[1].props.onPress();
			else input(tree, edited)?.props.onChangeText("local/b");
		});
		scrollTo.mockClear();
		flatListCalls.length = 0;
		await done(tree);
		expect(apply).toHaveBeenCalledExactlyOnceWith("local/b");
		expect(tree.root.findAllByProps({ accessibilityRole: "alert" })).toHaveLength(0);
		expect(scrollTo).not.toHaveBeenCalled();
		expect(flatListCalls).toHaveLength(0);
		expect(request).toHaveBeenCalledTimes(kind === "modelPicker" ? 1 : 0);
	},
);

it.each(["valid", "invalid", "rejected"] as const)(
	"ignores an old connection's %s validation without clearing the new pending check",
	async (outcome) => {
		const edited = { ...option("path", "systemPromptFile"), pathKind: "file" };
		const oldReply = deferred<{ valid: boolean; error?: string }>();
		const currentReply = deferred<{ valid: boolean }>();
		const oldRequest = vi.fn(() => oldReply.promise);
		const currentRequest = vi.fn(() => currentReply.promise);
		const apply = vi.fn();
		const scrollTo = vi.fn();
		const editor = (request: typeof oldRequest | typeof currentRequest) => (
			<LaunchFieldEditor
				option={edited}
				value={{}}
				effective={{}}
				client={{ request } as unknown as ConversationClientLike}
				close={() => {}}
				apply={apply}
			/>
		);
		const tree = render(editor(oldRequest), {
			createNodeMock: (node) => (node.type === "ScrollView" ? { scrollTo } : null),
		});
		act(() => input(tree, edited)?.props.onChangeText("/draft-sentinel"));
		await done(tree);
		expect(pressable(tree, "Done")?.props.disabled).toBe(true);
		act(() => tree.update(editor(currentRequest)));
		expect(pressable(tree, "Done")?.props.disabled).toBe(false);
		expect(input(tree, edited)?.props.value).toBe("/draft-sentinel");
		await done(tree);
		expect(currentRequest).toHaveBeenCalledExactlyOnceWith("evener/path/validate", {
			path: "/draft-sentinel",
			kind: "file",
		});
		await act(async () => {
			if (outcome === "rejected") oldReply.reject(new Error("old-transport-sentinel"));
			else oldReply.resolve({ valid: outcome === "valid", error: "old-path-sentinel" });
			await oldReply.promise.catch(() => {});
		});
		expect(apply).not.toHaveBeenCalled();
		expect(scrollTo).not.toHaveBeenCalled();
		expect(tree.root.findAllByProps({ accessibilityRole: "alert" })).toHaveLength(0);
		expect(pressable(tree, "Done")?.props.disabled).toBe(true);
		await act(async () => {
			currentReply.resolve({ valid: true });
			await currentReply.promise;
		});
		expect(apply).toHaveBeenCalledExactlyOnceWith("/draft-sentinel");
		expect(pressable(tree, "Done")?.props.disabled).toBe(false);
	},
);

it.each(["text", "modelPicker"] as const)("still applies inheritance from the %s editor", async (kind) => {
	const edited = option(kind, kind === "modelPicker" ? "model" : "systemPrompt");
	const apply = vi.fn();
	const tree = render(
		<LaunchFieldEditor
			option={edited}
			value={{ [edited.field]: "explicit-sentinel" }}
			effective={{ [edited.field]: longEffective }}
			client={null}
			close={() => {}}
			apply={apply}
		/>,
	);
	act(() => pressable(tree, "Use inherited value")?.props.onPress());
	await done(tree);
	expect(apply).toHaveBeenCalledExactlyOnceWith(undefined);
	expect(tree.root.findAllByProps({ accessibilityRole: "alert" })).toHaveLength(0);
	expect(flatListCalls).toHaveLength(0);
});

it.each([
	["a path list", option("pathList", "addDirs")],
	["an environment map", option("envMap", "env")],
	["a text field", option("text", "systemPrompt")],
])("lets a drag of %s's editor put the keyboard away", (_name, edited) => {
	const tree = render(
		<LaunchFieldEditor option={edited} value={{}} effective={{}} client={null} close={() => {}} apply={() => {}} />,
	);
	expect(tree.root.findAllByType("ScrollView" as never)[0]?.props.keyboardDismissMode).toBe("on-drag");
	act(() => tree.unmount());
});

it("lets a drag of the model picker put the keyboard away", () => {
	const tree = render(
		<LaunchFieldEditor
			option={option("modelPicker", "model")}
			value={{}}
			effective={{}}
			client={null}
			close={() => {}}
			apply={() => {}}
		/>,
	);
	expect(tree.root.findByType(FlatList).props.keyboardDismissMode).toBe("on-drag");
	act(() => tree.unmount());
});

it("opens a path list's effective values from a toggle that says whether they're shown", () => {
	const tree = render(
		<LaunchFieldEditor
			option={option("pathList", "addDirs")}
			value={{}}
			effective={{}}
			client={null}
			close={() => {}}
			apply={() => {}}
		/>,
	);
	const toggle = () => pressable(tree, "Effective values (0)");
	expect(toggle()?.props.accessibilityState).toMatchObject({ expanded: false });
	act(() => toggle()?.props.onPress());
	expect(toggle()?.props.accessibilityState).toMatchObject({ expanded: true });
	act(() => tree.unmount());
});
