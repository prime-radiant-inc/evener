// Launch defaults keeps itself current (principle 2, device audit N2): no
// Reload button; a read that failed is read again when the page comes back
// into view, and a change made elsewhere while editing is a choice to
// discard, never a refresh.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { LaunchOption } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { LaunchSettingsScreen } from "./LaunchSettingsScreen";
import { markers } from "./markerTestUtils";
import { alertRequests, offersButton, render, renderedText } from "./renderNative.testkit";
import { SearchField } from "./sheet/SearchField";

const harness = vi.hoisted(() => ({
	connection: null as unknown,
	focus: null as null | (() => void | (() => void)),
	fontScale: 1,
}));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	useWindowDimensions: () => ({ width: 320, height: 568, scale: 2, fontScale: harness.fontScale }),
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));
vi.mock("@react-navigation/native", () => ({
	// The test calls the page's focus effect itself, as navigation would on
	// coming back to the page.
	useFocusEffect: (effect: () => void | (() => void)) => {
		harness.focus = effect;
	},
	usePreventRemove: () => {},
}));

const props = {
	navigation: { dispatch: () => {} },
} as unknown as ComponentProps<typeof LaunchSettingsScreen>;

const option = {
	field: "model",
	perLaunch: true,
	wireField: "model",
	label: "Model",
	group: "Model",
	kind: "text",
	defaultableLayers: ["global"],
} satisfies LaunchOption;

function launchHub() {
	const hub = new FakeClient("ready");
	let layerReads = 0;
	let failReads = 0;
	let failResolves = 0;
	let model = "gpt-5.6";
	hub.on("evener/launch/schema", () => ({ options: [option] }));
	hub.on("evener/launch/getLayer", () => {
		layerReads += 1;
		if (failReads > 0) {
			failReads -= 1;
			throw new Error("down");
		}
		return { model };
	});
	hub.on("evener/launch/resolve", () => {
		if (failResolves > 0) {
			failResolves -= 1;
			throw new Error("down");
		}
		return { effective: { model }, layers: {}, provenance: {} };
	});
	return {
		hub,
		reads: () => layerReads,
		failNext: () => {
			failReads = 1;
		},
		failNextResolve: () => {
			failResolves = 1;
		},
		changeElsewhere: (next: string) => {
			model = next;
			hub.emitNotification({ method: "evener/launch/updated", params: { layer: "global", cwd: "/" } });
		},
	};
}

async function mount(hub: FakeClient, params: Record<string, unknown> = { hubId: "hub-1" }, scrollTo = vi.fn()) {
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: hub as unknown as ConversationClientLike,
		state: "ready",
	};
	const tree = render(<LaunchSettingsScreen {...props} route={{ params } as unknown as typeof props.route} />, {
		createNodeMock: (element) => (element.type === "ScrollView" ? { scrollTo } : null),
	});
	await act(async () => {});
	return tree;
}

async function editModel(tree: ReturnType<typeof render>, value: string) {
	await act(async () => {
		tree.root
			.find(
				(node) => node.props.accessibilityRole === "button" && node.props.accessibilityLabel?.startsWith("Edit Model,"),
			)
			.props.onPress();
	});
	await act(async () => {
		tree.root
			.find((node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Model")
			.props.onChangeText(value);
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Done" }).props.onPress();
	});
}

/** What the page reports went wrong, by problem. */
function problems(tree: ReturnType<typeof render>): string[] {
	return markers(tree, "launch-problem");
}

beforeEach(() => {
	alertRequests.length = 0;
	harness.focus = null;
	harness.fontScale = 1;
});

it("lets a drag of the page put the keyboard away", async () => {
	const tree = await mount(launchHub().hub);
	expect(tree.root.findAllByType("ScrollView" as never)[0]?.props.keyboardDismissMode).toBe("on-drag");
});

it("insets the search control from the sheet edges", async () => {
	const tree = await mount(launchHub().hub);
	expect(tree.root.findByType(SearchField).parent?.props.style.marginHorizontal ?? 0).toBeGreaterThanOrEqual(16);
});

it("keeps Save outside the scrolling settings and saves the edited global layer", async () => {
	const launch = launchHub();
	const save = vi.fn(() => ({ effective: { model: "model-sentinel" }, layers: {}, provenance: {} }));
	launch.hub.on("evener/launch/setLayer", save);
	const tree = await mount(launch.hub);
	const action = () => tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Save defaults" });
	expect(action().props.disabled).toBe(true);
	await editModel(tree, "model-sentinel");
	expect(action().props.disabled).toBe(false);
	for (let parent = action().parent; parent; parent = parent.parent) {
		expect(String(parent.type)).not.toBe("ScrollView");
	}
	await act(async () => action().props.onPress());
	expect(save).toHaveBeenCalledOnce();
	expect(launch.hub.calls.find((call) => call.method === "evener/launch/setLayer")?.params).toMatchObject({
		layer: "global",
		cwd: "/",
		config: { model: "model-sentinel" },
	});
});

it.each([1, 2, 3.1])("reveals a refused pinned save before the introduction at text scale %s", async (fontScale) => {
	harness.fontScale = fontScale;
	const launch = launchHub();
	launch.hub.on("evener/launch/setLayer", () => {
		throw new Error("save rejected");
	});
	const scrollTo = vi.fn();
	const tree = await mount(launch.hub, { hubId: "hub-1" }, scrollTo);
	await editModel(tree, "proposal-sentinel");
	for (let attempt = 0; attempt < 2; attempt += 1) {
		scrollTo.mockClear();
		await act(async () =>
			tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Save defaults" }).props.onPress(),
		);
		expect(problems(tree)).toContain("settings");
		expect(scrollTo).toHaveBeenCalledWith({ y: 0, animated: true });
		const firstText = tree.root.findByType("ScrollView" as never).findAllByType("Text" as never)[0];
		expect(firstText?.props.accessibilityRole).toBe("alert");
		expect(tree.root.findByProps({ label: "Model", chevron: true }).props.sub).toBe("proposal-sentinel");
	}
	launch.hub.on("evener/launch/setLayer", ({ config }) => {
		launch.hub.on("evener/launch/getLayer", () => config);
		return { effective: config, layers: {}, provenance: {} };
	});
	await act(async () =>
		tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Save defaults" }).props.onPress(),
	);
	expect(problems(tree)).toEqual([]);
	expect(
		tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Save defaults" }).props.disabled,
	).toBe(true);
});

it.each(["changed-elsewhere", "resolve"])("places %s before growing introductory content", async (problem) => {
	harness.fontScale = 3.1;
	const launch = launchHub();
	if (problem === "resolve") launch.failNextResolve();
	const scrollTo = vi.fn();
	const tree = await mount(launch.hub, { hubId: "hub-1" }, scrollTo);
	if (problem === "changed-elsewhere") {
		await editModel(tree, "draft-sentinel");
		await act(async () => launch.changeElsewhere("other-sentinel"));
	}
	expect(problems(tree)).toContain(problem);
	expect(scrollTo).toHaveBeenCalledWith({ y: 0, animated: true });
	const firstText = tree.root.findByType("ScrollView" as never).findAllByType("Text" as never)[0];
	expect(firstText?.props.accessibilityRole).toBe("alert");
});

it("keeps the hub identity and editable setting values selectable", async () => {
	const tree = await mount(launchHub().hub);
	const header = tree.root.findAllByType("Text" as never).find((node) => node.props.children === "Work hub");
	expect(header?.props.selectable).toBe(true);
	const value = tree.root.findByProps({ label: "Model", chevron: true }).findByProps({ testID: "row-sub" });
	expect(value.props.selectable).toBe(true);
	expect(value.props.numberOfLines).toBeUndefined();
	await editModel(tree, "selection-edit-sentinel");
	expect(tree.root.findByProps({ label: "Model", chevron: true }).props.sub).toBe("selection-edit-sentinel");
});

it("offers no Reload, loaded or not", async () => {
	const launch = launchHub();
	launch.failNext();
	const tree = await mount(launch.hub);
	expect(problems(tree)).toEqual(["settings"]);
	expect(offersButton(tree, /\bReload\b/)).toBe(false);
});

it("reads a failed load again when the page comes back into view, with nothing to press", async () => {
	const launch = launchHub();
	launch.failNext();
	const tree = await mount(launch.hub);
	expect(launch.reads()).toBe(1);
	await act(async () => {
		harness.focus?.();
	});
	expect(launch.reads()).toBe(2);
	expect(problems(tree)).toEqual([]);
});

it("says a change made elsewhere while editing, and lets you discard your edit to see it", async () => {
	const launch = launchHub();
	const tree = await mount(launch.hub);
	const discardAction = () => tree.root.findAll((node) => node.props.accessibilityLabel === "Discard changes");
	expect(discardAction()).toHaveLength(0);
	// Edit the model through its field sheet, as a person would.
	await act(async () => {
		tree.root
			.find(
				(node) => node.props.accessibilityRole === "button" && node.props.accessibilityLabel?.startsWith("Edit Model,"),
			)
			.props.onPress();
	});
	await act(async () => {
		tree.root
			.find((node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Model")
			.props.onChangeText("claude-5");
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Done" }).props.onPress();
	});
	await act(async () => launch.changeElsewhere("gpt-5.7"));
	expect(problems(tree)).toEqual(["changed-elsewhere"]);
	await act(async () => {
		discardAction()[0]?.props.onPress();
	});
	expect(alertRequests).toHaveLength(1);
	const discard = alertRequests.at(-1)?.buttons?.find((button) => button.text === "Discard");
	await act(async () => discard?.onPress?.());
	expect(problems(tree)).toEqual([]);
	expect(renderedText(tree)).toContain("gpt-5.7");
});

it("re-reads effective values that failed to load when the page comes back into view", async () => {
	const launch = launchHub();
	launch.failNextResolve();
	const tree = await mount(launch.hub);
	expect(problems(tree)).toEqual(["resolve"]);
	await act(async () => {
		harness.focus?.();
	});
	expect(problems(tree)).toEqual([]);
});

it("keeps an unsaved edit when the page comes back into view after a failed read", async () => {
	const launch = launchHub();
	const tree = await mount(launch.hub);
	await editModel(tree, "claude-5");
	launch.failNext();
	await act(async () => launch.changeElsewhere("gpt-5.7"));
	const reads = launch.reads();
	await act(async () => {
		harness.focus?.();
	});
	expect(launch.reads()).toBe(reads);
	expect(tree.root.findByProps({ label: "Model", chevron: true }).props.sub).toBe("claude-5");
});

it("offers Discard changes only while an edit sits over a change made elsewhere, and shows an error beside it", async () => {
	const launch = launchHub();
	const tree = await mount(launch.hub);
	const discard = () => tree.root.findAll((node) => node.props.accessibilityLabel === "Discard changes");
	await editModel(tree, "claude-5");
	expect(discard()).toHaveLength(0);
	await act(async () => launch.changeElsewhere("gpt-5.7"));
	expect(discard()).not.toHaveLength(0);
	// A read that fails meanwhile says so too, never hidden behind the change.
	launch.failNext();
	await act(async () => launch.changeElsewhere("gpt-5.7"));
	expect(problems(tree)).toEqual(["settings", "changed-elsewhere"]);
	// Editing back to what the hub has leaves nothing to discard.
	await editModel(tree, "gpt-5.7");
	expect(discard()).toHaveLength(0);
	expect(problems(tree)).not.toContain("changed-elsewhere");
});

// The LaunchSettings route no longer carries a project cwd (the only way in
// is the Hub's "Launch defaults" row, which passes { hubId } alone), so a
// stray projectCwd must not pick the project layer: this page is always the
// hub's global defaults.
it("edits the hub's global defaults, ignoring any project cwd on the route", async () => {
	const launch = launchHub();
	const tree = await mount(launch.hub, { hubId: "hub-1", projectCwd: "/repo" });
	expect(renderedText(tree)).toContain(
		"Defaults for new Evener sessions. Project and per-launch settings can override these values.",
	);
	expect(renderedText(tree)).not.toContain("/repo");
	const read = launch.hub.calls.find((call) => call.method === "evener/launch/getLayer")?.params as
		| { cwd: string; layer: string }
		| undefined;
	expect(read).toEqual({ cwd: "/", layer: "global" });
});
