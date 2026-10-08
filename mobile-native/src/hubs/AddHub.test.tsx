import { act, type ReactTestRenderer } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import { render, renderedText } from "../renderNative.testkit";
import { AddHub } from "./AddHub";

type Permission = { granted: boolean; canAskAgain: boolean; status: string };
const mocks = vi.hoisted(() => ({
	permission: { value: null as Permission | null },
	request: vi.fn(),
	get: vi.fn(),
	appState: [] as ((state: string) => void)[],
	getStringAsync: vi.fn(),
	saveHub: vi.fn(),
	openSettings: vi.fn(),
}));
vi.mock("expo-camera", () => ({
	CameraView: "CameraView",
	useCameraPermissions: () => [mocks.permission.value, mocks.request, mocks.get],
}));
vi.mock("expo-clipboard", () => ({ getStringAsync: mocks.getStringAsync }));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => ({ saveHub: mocks.saveHub }) }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	Linking: { openSettings: mocks.openSettings },
	AppState: {
		currentState: "active",
		addEventListener: (_event: string, listener: (state: string) => void) => {
			mocks.appState.push(listener);
			return { remove: () => mocks.appState.splice(mocks.appState.indexOf(listener), 1) };
		},
	},
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const TOKEN = "s3cret-pairing-token";
const LINK = `https://magic-kingdom:9180/auth/${TOKEN}`;
const OTHER_LINK = "https://paradise-park:9180/auth/other-token";
const GRANTED: Permission = { granted: true, canAskAgain: true, status: "granted" };

beforeEach(() => {
	mocks.permission.value = GRANTED;
	mocks.request.mockReset();
	mocks.get.mockReset();
	mocks.appState.length = 0;
	// expo-camera's request answers with the permission it settled on.
	mocks.request.mockResolvedValue(GRANTED);
	mocks.getStringAsync.mockReset();
	mocks.saveHub.mockReset();
	mocks.openSettings.mockReset();
});

function mount(how: "scan" | "paste" | "address") {
	const onConnected = vi.fn();
	const tree = render(<AddHub how={how} onConnected={onConnected} />);
	return { tree, onConnected };
}

function button(tree: ReactTestRenderer, label: string) {
	return tree.root.findByProps({ accessibilityRole: "button", accessibilityLabel: label });
}

async function press(tree: ReactTestRenderer, label: string) {
	await act(async () => {
		button(tree, label).props.onPress();
	});
}

function field(tree: ReactTestRenderer, label: string) {
	return tree.root.find((node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === label);
}

function type(tree: ReactTestRenderer, label: string, text: string) {
	act(() => {
		field(tree, label).props.onChangeText(text);
	});
}

function camera(tree: ReactTestRenderer) {
	return tree.root.find((node) => String(node.type) === "CameraView");
}

function scan(tree: ReactTestRenderer, handler: (event: { type: string; data: string }) => void, data: string) {
	act(() => {
		handler({ type: "qr", data });
	});
}

it("scans a pairing code into review and connects with the suggested name", async () => {
	mocks.saveHub.mockResolvedValue(true);
	const { tree, onConnected } = mount("scan");
	expect(camera(tree).props.barcodeScannerSettings.barcodeTypes).toEqual(["qr"]);
	expect(renderedText(tree)).toContain("Point the camera at the pairing code.");
	scan(tree, camera(tree).props.onBarcodeScanned, LINK);
	expect(renderedText(tree)).toContain("Pair with https://magic-kingdom:9180");
	expect(field(tree, "Name").props.value).toBe("magic-kingdom");
	expect(tree.root.findAll((node) => String(node.type) === "CameraView")).toHaveLength(0);
	await press(tree, "Connect");
	expect(mocks.saveHub).toHaveBeenCalledWith({
		name: "magic-kingdom",
		origin: "https://magic-kingdom:9180",
		token: TOKEN,
	});
	expect(onConnected).toHaveBeenCalledTimes(1);
});

it("connects under the name the person typed", async () => {
	mocks.saveHub.mockResolvedValue(true);
	const { tree } = mount("scan");
	scan(tree, camera(tree).props.onBarcodeScanned, LINK);
	type(tree, "Name", "Work");
	await press(tree, "Connect");
	expect(mocks.saveHub).toHaveBeenCalledWith({ name: "Work", origin: "https://magic-kingdom:9180", token: TOKEN });
});

it("ignores a second scan while reviewing the first", () => {
	const { tree } = mount("scan");
	const onScanned = camera(tree).props.onBarcodeScanned;
	scan(tree, onScanned, LINK);
	type(tree, "Name", "Work");
	scan(tree, onScanned, OTHER_LINK);
	expect(renderedText(tree)).toContain("Pair with https://magic-kingdom:9180");
	expect(renderedText(tree)).not.toContain("paradise-park");
	expect(field(tree, "Name").props.value).toBe("Work");
});

it("says an invalid code isn't a pairing code and keeps the camera", () => {
	const { tree } = mount("scan");
	scan(tree, camera(tree).props.onBarcodeScanned, "WDJB-MJHT");
	expect(renderedText(tree)).toContain("That isn't an Evener pairing code.");
	expect(renderedText(tree)).not.toContain("Point the camera at the pairing code.");
	expect(camera(tree)).toBeDefined();
	scan(tree, camera(tree).props.onBarcodeScanned, LINK);
	expect(renderedText(tree)).toContain("Pair with https://magic-kingdom:9180");
});

it("asks for the camera once on mount when it hasn't asked yet", async () => {
	mocks.permission.value = { granted: false, canAskAgain: true, status: "undetermined" };
	const { tree } = mount("scan");
	// The request answers at once; its answer lands here, not after the test.
	await act(async () => {
		tree.update(<AddHub how="scan" onConnected={() => {}} />);
	});
	expect(mocks.request).toHaveBeenCalledTimes(1);
	expect(tree.root.findAll((node) => String(node.type) === "CameraView")).toHaveLength(0);
});

it("says it's waiting for camera access while the request is out", () => {
	mocks.permission.value = { granted: false, canAskAgain: true, status: "undetermined" };
	mocks.request.mockReturnValue(new Promise(() => {}));
	const { tree } = mount("scan");
	expect(renderedText(tree)).toContain("Waiting for camera access…");
});

it("doesn't ask for a camera it already has", () => {
	mount("scan");
	expect(mocks.request).not.toHaveBeenCalled();
});

it("offers Settings or pasting when camera access is off", async () => {
	mocks.permission.value = { granted: false, canAskAgain: false, status: "denied" };
	const { tree } = mount("scan");
	expect(mocks.request).not.toHaveBeenCalled();
	expect(renderedText(tree)).toContain("Camera access is off for Evener.");
	expect(tree.root.findAll((node) => String(node.type) === "CameraView")).toHaveLength(0);
	await press(tree, "Open Settings");
	expect(mocks.openSettings).toHaveBeenCalledTimes(1);
	await press(tree, "Paste the link instead");
	expect(field(tree, "Pairing link").props.secureTextEntry).toBe(true);
});

it("offers Settings or pasting once a camera request comes back refused, though it could ask again", async () => {
	// Android leaves canAskAgain true after one refusal; the page must not
	// stay blank waiting on a request that already answered (RoboRev, #3001).
	mocks.permission.value = { granted: false, canAskAgain: true, status: "undetermined" };
	mocks.request.mockResolvedValue({ granted: false, canAskAgain: true, status: "denied" });
	const { tree } = mount("scan");
	await act(async () => {
		mocks.permission.value = { granted: false, canAskAgain: true, status: "denied" };
		tree.update(<AddHub how="scan" onConnected={() => {}} />);
	});
	expect(mocks.request).toHaveBeenCalledTimes(1);
	expect(renderedText(tree)).toContain("Camera access is off for Evener.");
	expect(button(tree, "Paste the link instead")).toBeTruthy();
});

it("reads the camera permission again on coming back from Settings", async () => {
	// useCameraPermissions reads once on mount and after a request, so a
	// grant made in Settings wouldn't show until the page remounted.
	mocks.permission.value = { granted: false, canAskAgain: false, status: "denied" };
	mount("scan");
	await act(async () => {
		for (const listener of mocks.appState) listener("background");
	});
	expect(mocks.get).not.toHaveBeenCalled();
	await act(async () => {
		for (const listener of mocks.appState) listener("active");
	});
	expect(mocks.get).toHaveBeenCalledTimes(1);
});

it("pastes a pairing link from the clipboard into review", async () => {
	mocks.getStringAsync.mockResolvedValue(` ${LINK}\n`);
	const { tree } = mount("paste");
	expect(field(tree, "Pairing link").props.secureTextEntry).toBe(true);
	await press(tree, "Paste");
	expect(renderedText(tree)).toContain("Pair with https://magic-kingdom:9180");
	expect(field(tree, "Name").props.value).toBe("magic-kingdom");
});

it("says a pasted link isn't a pairing link and keeps it for editing", async () => {
	mocks.getStringAsync.mockResolvedValue("https://magic-kingdom:9180/");
	const { tree } = mount("paste");
	await press(tree, "Paste");
	expect(renderedText(tree)).toContain(
		"That isn't an Evener pairing link. Copy it again from Settings, then Mobile app, in Evener on your computer.",
	);
	expect(field(tree, "Pairing link").props.value).toBe("https://magic-kingdom:9180/");
});

it("reviews a pairing link typed into the field when it's submitted", () => {
	const { tree } = mount("paste");
	type(tree, "Pairing link", LINK);
	act(() => {
		field(tree, "Pairing link").props.onSubmitEditing();
	});
	expect(renderedText(tree)).toContain("Pair with https://magic-kingdom:9180");
});

it("reviews an address and token typed by hand", async () => {
	mocks.saveHub.mockResolvedValue(true);
	const { tree, onConnected } = mount("address");
	expect(field(tree, "Address").props.keyboardType).toBe("url");
	expect(field(tree, "Address").props.placeholder).toBe("https://hub.example.com:9180");
	expect(field(tree, "Token (optional)").props.secureTextEntry).toBe(true);
	expect(button(tree, "Continue").props.disabled).toBe(true);
	type(tree, "Address", "https://magic-kingdom:9180");
	type(tree, "Token (optional)", TOKEN);
	await press(tree, "Continue");
	expect(renderedText(tree)).toContain("Pair with https://magic-kingdom:9180");
	expect(field(tree, "Name").props.value).toBe("magic-kingdom");
	await press(tree, "Connect");
	expect(mocks.saveHub).toHaveBeenCalledWith({
		name: "magic-kingdom",
		origin: "https://magic-kingdom:9180",
		token: TOKEN,
	});
	expect(onConnected).toHaveBeenCalledTimes(1);
});

it("finishes when the hub is saved though a newer choice of hub kept it from being selected", async () => {
	// saveHub resolves false when a later selection superseded this one; the
	// hub is saved all the same, so review mustn't sit there (RoboRev, #3001).
	mocks.saveHub.mockResolvedValue(false);
	const { tree, onConnected } = mount("paste");
	type(tree, "Pairing link", LINK);
	act(() => {
		field(tree, "Pairing link").props.onSubmitEditing();
	});
	await press(tree, "Connect");
	expect(onConnected).toHaveBeenCalledTimes(1);
});

it("can't connect without a name, and saves the name trimmed", async () => {
	mocks.saveHub.mockResolvedValue(true);
	const { tree } = mount("paste");
	type(tree, "Pairing link", LINK);
	act(() => {
		field(tree, "Pairing link").props.onSubmitEditing();
	});
	type(tree, "Name", "   ");
	expect(button(tree, "Connect").props.disabled).toBe(true);
	type(tree, "Name", "  Magic Kingdom ");
	expect(button(tree, "Connect").props.disabled).toBe(false);
	await press(tree, "Connect");
	expect(mocks.saveHub).toHaveBeenCalledWith(expect.objectContaining({ name: "Magic Kingdom" }));
});

it("trims a typed token the way it trims the address", async () => {
	mocks.saveHub.mockResolvedValue(true);
	const { tree } = mount("address");
	type(tree, "Address", " https://magic-kingdom:9180 ");
	type(tree, "Token (optional)", ` ${TOKEN}\n`);
	await press(tree, "Continue");
	await press(tree, "Connect");
	expect(mocks.saveHub).toHaveBeenCalledWith(expect.objectContaining({ token: TOKEN }));
});

it("stays on review and says what to change when the hub can't be saved", async () => {
	mocks.saveHub.mockRejectedValue(new Error("Enter a complete http:// or https:// hub address."));
	const { tree, onConnected } = mount("address");
	type(tree, "Address", "ftp://magic-kingdom");
	await press(tree, "Continue");
	await press(tree, "Connect");
	expect(onConnected).not.toHaveBeenCalled();
	expect(renderedText(tree)).toContain("Pair with ftp://magic-kingdom");
	// A typed address has no link to check.
	expect(renderedText(tree)).toContain("Couldn't save this hub. Check the name and the address, and try again.");
	expect(button(tree, "Connect").props.disabled).toBe(false);
});

it("reads Connecting… and can't be pressed again while saving", async () => {
	let finish!: (selected: boolean) => void;
	mocks.saveHub.mockReturnValue(
		new Promise<boolean>((resolve) => {
			finish = resolve;
		}),
	);
	const { tree, onConnected } = mount("scan");
	scan(tree, camera(tree).props.onBarcodeScanned, LINK);
	await press(tree, "Connect");
	expect(button(tree, "Connecting…").props.disabled).toBe(true);
	await act(async () => {
		finish(true);
	});
	expect(onConnected).toHaveBeenCalledTimes(1);
});

it("never shows the token or offers to retry or reconnect", async () => {
	mocks.getStringAsync.mockResolvedValue(LINK);
	mocks.saveHub.mockRejectedValue(new Error("refused"));
	for (const how of ["scan", "paste", "address"] as const) {
		const { tree } = mount(how);
		if (how === "scan") scan(tree, camera(tree).props.onBarcodeScanned, LINK);
		if (how === "paste") await press(tree, "Paste");
		if (how === "address") {
			type(tree, "Address", "https://magic-kingdom:9180");
			type(tree, "Token (optional)", TOKEN);
			expect(renderedText(tree)).not.toContain(TOKEN);
			await press(tree, "Continue");
		}
		await press(tree, "Connect");
		const text = renderedText(tree);
		expect(text).not.toContain(TOKEN);
		expect(text).not.toMatch(/Retry|Reconnect/);
	}
});
