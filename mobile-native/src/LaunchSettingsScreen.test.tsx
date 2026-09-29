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
import { alertRequests, render, renderedText } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({
	connection: null as unknown,
	focus: null as null | (() => void | (() => void)),
}));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
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
	route: { params: { hubId: "hub-1" } },
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
	hub.on("evener/launch/resolve", () => ({ effective: { model }, layers: {}, provenance: {} }));
	return {
		hub,
		reads: () => layerReads,
		failNext: () => {
			failReads = 1;
		},
		changeElsewhere: (next: string) => {
			model = next;
			hub.emitNotification({ method: "evener/launch/updated", params: { layer: "global", cwd: "/" } });
		},
	};
}

async function mount(hub: FakeClient) {
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: hub as unknown as ConversationClientLike,
		state: "ready",
	};
	const tree = render(<LaunchSettingsScreen {...props} />);
	await act(async () => {});
	return tree;
}

beforeEach(() => {
	alertRequests.length = 0;
	harness.focus = null;
});

it("offers no Reload, loaded or not", async () => {
	const launch = launchHub();
	launch.failNext();
	const tree = await mount(launch.hub);
	expect(renderedText(tree)).toContain("Could not load launch settings.");
	expect(tree.root.findAll((node) => node.props.accessibilityLabel === "Reload")).toHaveLength(0);
	expect(renderedText(tree)).not.toMatch(/\bReload\b/);
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
	expect(renderedText(tree)).not.toContain("Could not load launch settings.");
});

it("says a change made elsewhere while editing, and lets you discard your edit to see it", async () => {
	const launch = launchHub();
	const tree = await mount(launch.hub);
	const discardAction = () => tree.root.findAll((node) => node.props.accessibilityLabel === "Discard changes");
	expect(discardAction()).toHaveLength(0);
	// Edit the model through its field sheet, as a person would.
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Edit Model" }).props.onPress();
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
	expect(renderedText(tree)).toContain("Launch settings changed elsewhere. Discard your changes to see them.");
	await act(async () => {
		discardAction()[0]?.props.onPress();
	});
	expect(alertRequests.at(-1)?.title).toBe("Discard your changes?");
	const discard = alertRequests.at(-1)?.buttons?.find((button) => button.text === "Discard");
	await act(async () => discard?.onPress?.());
	expect(renderedText(tree)).not.toContain("changed elsewhere");
	expect(renderedText(tree)).toContain("gpt-5.7");
});
