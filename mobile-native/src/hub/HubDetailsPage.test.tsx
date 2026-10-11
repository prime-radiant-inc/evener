import type { NativeStackScreenProps } from "@react-navigation/native-stack";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { HubProfile } from "../connection";
import { palettes } from "../design/tokens";
import { alertRequests, render, renderedText } from "../renderNative.testkit";
import { HubDetailsPage } from "./HubDetailsPage";
import type { HubRoutes } from "./hubSheetContext";

const connection = vi.hoisted(() => ({
	profiles: [] as HubProfile[],
	activeProfile: null as HubProfile | null,
	selectHub: vi.fn(),
	removeHub: vi.fn(),
	updateHub: vi.fn(),
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => connection }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const MAGIC: HubProfile = { id: "hub-1", name: "magic-kingdom", origin: "https://magic-kingdom:9180" };
const PARADISE: HubProfile = { id: "hub-2", name: "paradise-park", origin: "http://100.113.28.18:9180" };

beforeEach(() => {
	connection.profiles = [MAGIC, PARADISE];
	connection.activeProfile = MAGIC;
	connection.selectHub.mockReset();
	connection.removeHub.mockReset();
	connection.updateHub.mockReset();
	alertRequests.length = 0;
});

function mount(id: string) {
	const navigation = { navigate: vi.fn(), goBack: vi.fn(), setOptions: vi.fn() };
	const route = { key: "HubDetails", name: "HubDetails", params: { id } } as const;
	const tree = render(
		<HubDetailsPage
			navigation={navigation as unknown as NativeStackScreenProps<HubRoutes, "HubDetails">["navigation"]}
			route={route}
		/>,
	);
	return { tree, navigation };
}

function button(tree: ReactTestRenderer, label: string) {
	return tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: label });
}

async function press(tree: ReactTestRenderer, label: string) {
	await act(async () => {
		button(tree, label).props.onPress();
	});
}

/** Confirms the removal alert the page just raised. */
async function confirmRemove() {
	const request = alertRequests.at(-1);
	const remove = request?.buttons?.find((choice) => choice.text === "Remove");
	await act(async () => {
		remove?.onPress?.();
	});
}

it("is titled with the hub's name and shows its name and address", () => {
	const { tree, navigation } = mount("hub-2");
	expect(navigation.setOptions).toHaveBeenCalledWith({ title: "paradise-park" });
	const text = renderedText(tree);
	expect(text).toContain("Name");
	expect(text).toContain("paradise-park");
	expect(text).toContain("Address");
	const address = tree.root.find(
		(node) => String(node.type) === "Text" && node.props.children === "http://100.113.28.18:9180",
	);
	expect(JSON.stringify(address.props.style)).toContain("Menlo");
	expect(
		tree.root.findAllByProps({ accessibilityRole: "button", accessibilityLabel: "Address, http://100.113.28.18:9180" }),
	).toHaveLength(0);
});

it("edits the name and token in today's hub editor", async () => {
	connection.updateHub.mockResolvedValue(undefined);
	const { tree } = mount("hub-2");
	expect(tree.root.findAll((node) => String(node.type) === "Modal")).toHaveLength(0);
	await press(tree, "Name, paradise-park");
	expect(tree.root.findAll((node) => String(node.type) === "Modal")).toHaveLength(1);
	act(() => {
		tree.root
			.find((node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Hub name")
			.props.onChangeText("Paradise");
	});
	await press(tree, "Save");
	expect(connection.updateHub).toHaveBeenCalledWith("hub-2", { name: "Paradise" });
	expect(tree.root.findAll((node) => String(node.type) === "Modal")).toHaveLength(0);
});

it("edits the hub in a grouped form: its name, its address, and a token only when asked to replace it", async () => {
	connection.updateHub.mockResolvedValue(undefined);
	const { tree } = mount("hub-2");
	await press(tree, "Name, paradise-park");
	const sections = tree.root
		.findAllByProps({ accessibilityRole: "header" })
		.map((header) => header.props.children)
		.slice(1);
	expect(sections).toEqual(["Name", "Address", "Token"]);
	const modal = tree.root.findByType("Modal" as never);
	const address = modal.findByProps({ accessibilityLabel: "Address, http://100.113.28.18:9180" });
	expect(address.props.accessibilityRole).toBeUndefined();
	expect(renderedText(tree)).toContain("The saved token will be kept.");
	expect(tree.root.findAll((node) => node.props.accessibilityLabel === "New bearer token")).toHaveLength(0);
	act(() => {
		tree.root.findByProps({ accessibilityLabel: "Replace saved token" }).props.onValueChange(true);
	});
	const token = tree.root.find(
		(node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "New bearer token",
	);
	expect(token.props.secureTextEntry).toBe(true);
	act(() => token.props.onChangeText("s3cret"));
	expect(renderedText(tree)).toContain("Leave empty to remove the saved token.");
	await press(tree, "Save");
	expect(connection.updateHub).toHaveBeenCalledWith("hub-2", { name: "paradise-park", token: "s3cret" });
});

it("says Saving, busy, while the hub saves, and puts a failed save's reason at the top", async () => {
	let fail = (_error: Error) => {};
	connection.updateHub.mockReturnValue(new Promise<void>((_resolve, reject) => (fail = reject)));
	const { tree } = mount("hub-2");
	await press(tree, "Name, paradise-park");
	await press(tree, "Save");
	expect(button(tree, "Saving…").props.accessibilityState).toEqual({ disabled: true, busy: true });
	await act(async () => fail(new Error("keychain")));
	const modal = tree.root.findByType("Modal" as never);
	const first = modal.findByType("ScrollView" as never).findAll((node) => String(node.type) === "Text")[0];
	expect(first?.props.children).toBe("Could not save this hub. Check the name and token, then retry.");
	expect(first?.props.accessibilityLiveRegion).toBe("polite");
});

it("leads from the name to a new token, and saves from the last field", async () => {
	connection.updateHub.mockResolvedValue(undefined);
	const { tree } = mount("hub-2");
	await press(tree, "Name, paradise-park");
	const field = (label: string) =>
		tree.root.find((node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === label);
	expect(field("Hub name").props.returnKeyType).toBe("done");
	act(() => tree.root.findByProps({ accessibilityLabel: "Replace saved token" }).props.onValueChange(true));
	expect(field("Hub name").props.returnKeyType).toBe("next");
	expect(field("New bearer token").props.returnKeyType).toBe("done");
	await act(async () => field("New bearer token").props.onSubmitEditing());
	expect(connection.updateHub).toHaveBeenCalledWith("hub-2", { name: "paradise-park", token: "" });
});

it("won't save a hub with no name", async () => {
	const { tree } = mount("hub-2");
	await press(tree, "Name, paradise-park");
	act(() => {
		tree.root
			.find((node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Hub name")
			.props.onChangeText("  ");
	});
	expect(button(tree, "Save").props.disabled).toBe(true);
	await act(async () => {
		tree.root
			.find((node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Hub name")
			.props.onSubmitEditing();
	});
	expect(connection.updateHub).not.toHaveBeenCalled();
});

it("heads the hub editor with the shared header: Edit hub and Cancel, titled once", async () => {
	const { tree } = mount("hub-2");
	await press(tree, "Name, paradise-park");
	// The sheet's title comes first; the form's section labels are headers too.
	expect(tree.root.findAllByProps({ accessibilityRole: "header" })[0]?.props.children).toBe("Edit hub");
	const titles = tree.root.findAll((node) => String(node.type) === "Text" && node.props.children === "Edit hub");
	expect(titles).toHaveLength(1);
	expect(tree.root.findAllByProps({ accessibilityRole: "button", accessibilityLabel: "Done" })).toHaveLength(0);
	await press(tree, "Cancel");
	expect(tree.root.findAll((node) => String(node.type) === "Modal")).toHaveLength(0);
});

it("keeps the hub editor open on Cancel while a save is in flight", async () => {
	let finish = () => {};
	connection.updateHub.mockReturnValue(new Promise<void>((resolve) => (finish = resolve)));
	const { tree } = mount("hub-2");
	await press(tree, "Name, paradise-park");
	await press(tree, "Save");
	const cancel = tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: "Cancel" });
	expect(cancel.props.disabled).toBe(true);
	await act(async () => {
		cancel.props.onPress();
	});
	expect(tree.root.findAll((node) => String(node.type) === "Modal")).toHaveLength(1);
	// A swipe down or Android's back waits it out too.
	await act(async () => {
		tree.root.findByType("Modal" as never).props.onRequestClose();
	});
	expect(tree.root.findAll((node) => String(node.type) === "Modal")).toHaveLength(1);
	await act(async () => finish());
	expect(tree.root.findAll((node) => String(node.type) === "Modal")).toHaveLength(0);
});

it("asks before removing a hub", async () => {
	const { tree } = mount("hub-2");
	const remove = button(tree, "Remove this hub");
	const label = remove.find((node) => String(node.type) === "Text" && node.props.children === "Remove this hub");
	expect(label.props.style.color).toBe(palettes.light.dangerInk);
	await press(tree, "Remove this hub");
	expect(alertRequests).toHaveLength(1);
	const request = alertRequests[0];
	expect(request.title).toBe("Remove paradise-park?");
	expect(request.message).toBe("The saved hub, its token and its drafts are removed from this phone.");
	expect(request.buttons?.map((choice) => [choice.text, choice.style])).toEqual([
		["Cancel", "cancel"],
		["Remove", "destructive"],
	]);
	expect(connection.removeHub).not.toHaveBeenCalled();
});

it("removes another hub and goes back to Hubs", async () => {
	connection.removeHub.mockResolvedValue(undefined);
	const { tree, navigation } = mount("hub-2");
	await press(tree, "Remove this hub");
	await confirmRemove();
	expect(connection.removeHub).toHaveBeenCalledWith("hub-2");
	expect(navigation.goBack).toHaveBeenCalledTimes(1);
	expect(navigation.navigate).not.toHaveBeenCalled();
});

it("removes the selected hub and leaves the navigation to the sheet (Review Focus 5)", async () => {
	connection.removeHub.mockResolvedValue(undefined);
	const { tree, navigation } = mount("hub-1");
	await press(tree, "Remove this hub");
	await confirmRemove();
	expect(connection.removeHub).toHaveBeenCalledWith("hub-1");
	expect(navigation.goBack).not.toHaveBeenCalled();
	expect(navigation.navigate).not.toHaveBeenCalled();
});

it("says why a hub couldn't be removed, and stays", async () => {
	connection.removeHub.mockRejectedValue(new Error("Saved drafts for this hub could not be removed."));
	const { tree, navigation } = mount("hub-2");
	await press(tree, "Remove this hub");
	await confirmRemove();
	const footer = tree.root.find(
		(node) => String(node.type) === "Text" && node.props.children === "Saved drafts for this hub could not be removed.",
	);
	// GroupFooter's style is a list: its tone, then Menlo for machine text.
	expect(Object.assign({}, ...[footer.props.style].flat()).color).toBe(palettes.light.dangerInk);
	expect(navigation.goBack).not.toHaveBeenCalled();
	expect(renderedText(tree)).not.toMatch(/\bReconnect\b/);
});

it("says so when the hub was removed but its cleanup failed, rather than going blank", async () => {
	// removeSavedHub drops the hub, then rejects about the local data it
	// couldn't delete (src/removeHub.ts), so the page's hub is already gone.
	connection.removeHub.mockImplementation(async () => {
		connection.profiles = [MAGIC];
		throw new Error("The hub was removed, but some local data could not be deleted.");
	});
	const { tree } = mount("hub-2");
	await press(tree, "Remove this hub");
	await confirmRemove();
	expect(renderedText(tree)).toContain("The hub was removed, but some local data could not be deleted.");
});

it("tells you when the selected hub was removed but its cleanup failed, though the sheet then leaves", async () => {
	// Removing the selected hub deselects it before the cleanup error comes
	// back, so the sheet leaves for first run and this page unmounts; an alert
	// outlives it (RoboRev, #3001).
	connection.removeHub.mockRejectedValue(new Error("The hub was removed, but some local data could not be deleted."));
	const { tree } = mount("hub-1");
	await press(tree, "Remove this hub");
	await confirmRemove();
	expect(alertRequests.at(-1)).toMatchObject({
		title: "Couldn't finish removing magic-kingdom",
		message: "The hub was removed, but some local data could not be deleted.",
	});
});

it("renders nothing once the hub is gone", () => {
	connection.profiles = [MAGIC];
	const { tree } = mount("hub-2");
	expect(tree.toJSON()).toBeNull();
});

it("never asks to reconnect", () => {
	const { tree } = mount("hub-1");
	expect(renderedText(tree)).not.toMatch(/\bReconnect\b/);
});
