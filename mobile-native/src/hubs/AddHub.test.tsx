import nacl from "tweetnacl";
import { createPairingClaim, invitationText, toHex } from "@evener/appwire-client/pairing";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { hasTestID, markers, withTestID } from "../markerTestUtils";
import { offersButton, render, renderedText, textOf, unmountMountedTrees } from "../renderNative.testkit";
import { AddHub } from "./AddHub";
import { PairingApproval } from "./PairingApproval";

type Permission = { granted: boolean; canAskAgain: boolean; status: string };
const mocks = vi.hoisted(() => ({
	fetch: vi.fn(),
	random: vi.fn(),
	permission: { value: null as Permission | null },
	request: vi.fn(),
	get: vi.fn(),
	appState: [] as ((state: string) => void)[],
	getStringAsync: vi.fn(),
	saveHub: vi.fn(),
	openSettings: vi.fn(),
}));
const navigation = vi.hoisted(() => {
	const state = { focused: true };
	const listeners = { focus: new Set<() => void>(), blur: new Set<() => void>() };
	const api = {
		isFocused: () => state.focused,
		addListener: (event: "focus" | "blur", listener: () => void) => {
			listeners[event].add(listener);
			return () => {
				listeners[event].delete(listener);
			};
		},
	};
	return { state, listeners, api };
});
vi.mock("@react-navigation/native", () => ({ useNavigation: () => navigation.api }));
vi.mock("expo/fetch", () => ({ fetch: mocks.fetch }));
vi.mock("expo-crypto", () => ({ getRandomBytes: mocks.random }));
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

const TOKEN = "s3cret-pairing-token";
const LINK = `https://magic-kingdom:9180/auth/${TOKEN}`;
const OTHER_LINK = "https://paradise-park:9180/auth/other-token";
const GRANTED: Permission = { granted: true, canAskAgain: true, status: "granted" };

beforeEach(() => {
	mocks.fetch.mockReset();
	mocks.random.mockReset().mockImplementation(() => new Uint8Array(32).fill(7));
	navigation.state.focused = true;
	navigation.listeners.focus.clear();
	navigation.listeners.blur.clear();
	vi.stubGlobal(
		"fetch",
		vi.fn(() => Promise.reject(new Error("Global fetch must not serve native pairing"))),
	);
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

/** The origin the review step pairs with; undefined before review. */
function pairingWith(tree: ReactTestRenderer): string | undefined {
	const [group] = withTestID(tree, "group:pair-with");
	return group ? textOf(group) : undefined;
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
	expect(markers(tree, "pairing-status")).toEqual(["aim"]);
	scan(tree, camera(tree).props.onBarcodeScanned, LINK);
	expect(pairingWith(tree)).toBe("https://magic-kingdom:9180");
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
	expect(pairingWith(tree)).toBe("https://magic-kingdom:9180");
	expect(renderedText(tree)).not.toContain("paradise-park");
	expect(field(tree, "Name").props.value).toBe("Work");
});

it("says an invalid code isn't a pairing code and keeps the camera", () => {
	const { tree } = mount("scan");
	scan(tree, camera(tree).props.onBarcodeScanned, "WDJB-MJHT");
	expect(markers(tree, "pairing-status")).toEqual(["not-pairing"]);
	expect(camera(tree)).toBeDefined();
	scan(tree, camera(tree).props.onBarcodeScanned, LINK);
	expect(pairingWith(tree)).toBe("https://magic-kingdom:9180");
});

it("asks for the camera once on mount when it hasn't asked yet", async () => {
	mocks.permission.value = { granted: false, canAskAgain: true, status: "undetermined" };
	// The request answers at once, with what the hook goes on to report.
	mocks.request.mockResolvedValue(mocks.permission.value);
	const { tree } = mount("scan");
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
	expect(markers(tree, "pairing-status")).toEqual(["waiting"]);
});

it("doesn't ask for a camera it already has", () => {
	mount("scan");
	expect(mocks.request).not.toHaveBeenCalled();
});

it("offers Settings or pasting when camera access is off", async () => {
	mocks.permission.value = { granted: false, canAskAgain: false, status: "denied" };
	const { tree } = mount("scan");
	expect(mocks.request).not.toHaveBeenCalled();
	expect(markers(tree, "pairing-status")).toEqual(["camera-off"]);
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
	expect(markers(tree, "pairing-status")).toEqual(["camera-off"]);
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
	expect(pairingWith(tree)).toBe("https://magic-kingdom:9180");
	expect(field(tree, "Name").props.value).toBe("magic-kingdom");
});

it("says a pasted link isn't a pairing link and keeps it for editing", async () => {
	mocks.getStringAsync.mockResolvedValue("https://magic-kingdom:9180/");
	const { tree } = mount("paste");
	await press(tree, "Paste");
	expect(markers(tree, "pairing-status")).toEqual(["not-pairing"]);
	expect(field(tree, "Pairing link").props.value).toBe("https://magic-kingdom:9180/");
});

it("reviews a pairing link typed into the field when it's submitted", () => {
	const { tree } = mount("paste");
	type(tree, "Pairing link", LINK);
	act(() => {
		field(tree, "Pairing link").props.onSubmitEditing();
	});
	expect(pairingWith(tree)).toBe("https://magic-kingdom:9180");
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
	expect(pairingWith(tree)).toBe("https://magic-kingdom:9180");
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
	expect(pairingWith(tree)).toBe("ftp://magic-kingdom");
	// A typed address has no link to check.
	expect(markers(tree, "hub-save-refused")).toEqual(["address"]);
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
		expect(renderedText(tree)).not.toContain(TOKEN);
		expect(hasTestID(tree, `hub-save-refused:${how === "address" ? "address" : "link"}`)).toBe(true);
		expect(offersButton(tree, /\b(Retry|Reconnect)\b/)).toBe(false);
	}
});

afterEach(() => {
	unmountMountedTrees();
	vi.unstubAllGlobals();
	vi.useRealTimers();
});
it("shows a locally calculated code for an invitation without saving credentials", async () => {
	const invitation = { invitation: "01".repeat(24), origin: "http://magic-kingdom:9180", tailnet: true, expiresAt: 0 };
	const comparison = createPairingClaim(invitation, new Uint8Array(32).fill(7)).comparison;
	mocks.fetch.mockImplementation(async () => new Response(JSON.stringify({ state: "pending", comparison })));
	const { tree } = mount("scan");
	await act(async () => {
		camera(tree).props.onBarcodeScanned({ data: invitationText(invitation) });
	});
	expect(tree.root.findAll((node) => String(node.type) === "CameraView")).toHaveLength(0);
	expect(renderedText(tree)).toContain(comparison);
	expect(mocks.saveHub).not.toHaveBeenCalled();
	await press(tree, "Cancel pairing");
	expect(camera(tree)).toBeDefined();
	expect(mocks.saveHub).not.toHaveBeenCalled();
});

function approvedPairingFixture() {
	const invitation = { invitation: "01".repeat(24), origin: "http://magic-kingdom:9180", tailnet: true, expiresAt: 0 };
	const claim = createPairingClaim(invitation, new Uint8Array(32).fill(7));
	const sender = nacl.box.keyPair.fromSecretKey(new Uint8Array(32).fill(9)),
		nonce = new Uint8Array(24).fill(2);
	const host = "magic-kingdom",
		path = "/rpc",
		token = TOKEN;
	const plain = new Uint8Array([
		1,
		1,
		35,
		220,
		host.length,
		path.length,
		0,
		token.length,
		...new TextEncoder().encode(host + path + token),
	]);
	const envelope = toHex(
		new Uint8Array([
			69,
			86,
			80,
			49,
			...new Uint8Array(8).fill(1),
			...sender.publicKey,
			...nonce,
			...nacl.box(plain, nonce, claim.publicKeyBytes, sender.secretKey),
		]),
	);
	return { invitation, reply: { state: "approved", comparison: claim.comparison, envelope } };
}

it("reviews an approved invitation before saving the Hub", async () => {
	const { invitation, reply } = approvedPairingFixture();
	mocks.fetch.mockImplementation(async () => new Response(JSON.stringify(reply)));
	const { tree } = mount("scan");
	await act(async () => {
		camera(tree).props.onBarcodeScanned({ data: invitationText(invitation) });
	});
	expect(field(tree, "Name").props.value).toBe("magic-kingdom");
	expect(mocks.saveHub).not.toHaveBeenCalled();
	await press(tree, "Connect");
	expect(mocks.saveHub).toHaveBeenCalledWith({ name: "magic-kingdom", origin: invitation.origin, token: TOKEN });
});

it("ignores a claim response arriving after cancellation", async () => {
	const { invitation, reply } = approvedPairingFixture();
	let answer: ((response: Response) => void) | undefined;
	mocks.fetch.mockImplementation(
		() =>
			new Promise<Response>((resolve) => {
				answer = resolve;
			}),
	);
	const { tree } = mount("scan");
	await act(async () => {
		camera(tree).props.onBarcodeScanned({ data: invitationText(invitation) });
	});
	await press(tree, "Cancel pairing");
	await act(async () => {
		answer?.(new Response(JSON.stringify(reply)));
	});
	expect(camera(tree)).toBeDefined();
	expect(mocks.saveHub).not.toHaveBeenCalled();
});

type ClaimRequest = {
	body: { invitation: string; publicKey: string; client: string };
	signal: AbortSignal;
	answer(reply: unknown): void;
	fail(): void;
};

function captureClaims(honorAbort = true) {
	const requests: ClaimRequest[] = [];
	mocks.fetch.mockImplementation(
		(_url: string, options: RequestInit) =>
			new Promise<Response>((resolve, reject) => {
				if (!options.signal || typeof options.body !== "string") throw new Error("Missing claim request");
				const signal = options.signal;
				requests.push({
					body: JSON.parse(options.body),
					signal,
					answer: (reply) => resolve(new Response(JSON.stringify(reply))),
					fail: () => reject(new TypeError("Network unavailable")),
				});
				if (honorAbort) signal.addEventListener("abort", () => reject(new Error("Request aborted")), { once: true });
			}),
	);
	return requests;
}

async function startInvitation() {
	const fixture = approvedPairingFixture();
	const { tree } = mount("scan");
	await act(async () => {
		camera(tree).props.onBarcodeScanned({ data: invitationText(fixture.invitation) });
	});
	return { tree, ...fixture };
}

async function advancePairingTime(milliseconds: number) {
	await act(async () => {
		await vi.advanceTimersByTimeAsync(milliseconds);
	});
}

it.each(["network failure", "request timeout"])(
	"retries %s with the same device key and reviews before saving",
	async (failure) => {
		vi.useFakeTimers();
		const requests = captureClaims();
		const { tree, invitation, reply } = await startInvitation();
		const first = requests[0];
		if (!first) throw new Error("Missing initial claim");
		if (failure === "network failure")
			await act(async () => {
				first.fail();
			});
		else {
			await advancePairingTime(10000);
			expect(first.signal.aborted).toBe(true);
		}
		await advancePairingTime(1200);
		expect(requests).toHaveLength(2);
		const retry = requests[1];
		if (!retry) throw new Error("Missing retry");
		expect(retry.body).toEqual(first.body);
		expect(renderedText(tree)).toContain(reply.comparison);
		await act(async () => {
			retry.answer(reply);
		});
		expect(field(tree, "Name").props.value).toBe("magic-kingdom");
		expect(mocks.saveHub).not.toHaveBeenCalled();
		await advancePairingTime(30000);
		expect(requests).toHaveLength(2);
		await press(tree, "Connect");
		expect(mocks.saveHub).toHaveBeenCalledExactlyOnceWith({
			name: "magic-kingdom",
			origin: invitation.origin,
			token: TOKEN,
		});
	},
);

it("pauses background polling, ignores its late approval, and resumes with the same key", async () => {
	vi.useFakeTimers();
	const requests = captureClaims(false);
	const { tree, reply } = await startInvitation();
	const first = requests[0];
	if (!first) throw new Error("Missing initial claim");
	await act(async () => {
		for (const listener of mocks.appState) listener("background");
	});
	expect(first.signal.aborted).toBe(true);
	await advancePairingTime(30000);
	expect(requests).toHaveLength(1);
	await act(async () => {
		first.answer(reply);
	});
	expect(button(tree, "Cancel pairing")).toBeDefined();
	expect(mocks.saveHub).not.toHaveBeenCalled();
	await act(async () => {
		for (const listener of mocks.appState) listener("active");
	});
	expect(requests).toHaveLength(2);
	const resumed = requests[1];
	if (!resumed) throw new Error("Missing foreground claim");
	expect(resumed.body).toEqual(first.body);
	expect(renderedText(tree)).toContain(reply.comparison);
	await act(async () => {
		resumed.answer(reply);
	});
	expect(field(tree, "Name").props.value).toBe("magic-kingdom");
	await advancePairingTime(30000);
	expect(requests).toHaveLength(2);
	expect(mocks.saveHub).not.toHaveBeenCalled();
	await press(tree, "Connect");
	expect(mocks.saveHub).toHaveBeenCalledTimes(1);
});

it.each(["expired", "rejected"])(
	"stops a %s invitation after returning from the background without saving it",
	async (state) => {
		vi.useFakeTimers();
		const requests = captureClaims();
		const { tree } = await startInvitation();
		await act(async () => {
			for (const listener of mocks.appState) listener("background");
		});
		await advancePairingTime(600000);
		expect(requests).toHaveLength(1);
		await act(async () => {
			for (const listener of mocks.appState) listener("active");
		});
		const resumed = requests[1];
		if (!resumed) throw new Error("Missing foreground claim");
		await act(async () => {
			resumed.answer({ state });
		});
		await advancePairingTime(30000);
		expect(requests).toHaveLength(2);
		expect(button(tree, "Cancel pairing")).toBeDefined();
		expect(mocks.saveHub).not.toHaveBeenCalled();
		await press(tree, "Cancel pairing");
		expect(camera(tree)).toBeDefined();
	},
);

it("uses the native streaming transport when global fetch has no response stream", async () => {
	const { invitation, reply } = approvedPairingFixture();
	const globalFetch = vi.fn(async () => ({ ok: true, status: 200, body: null }) as Response);
	vi.stubGlobal("fetch", globalFetch);
	mocks.fetch.mockResolvedValue(new Response(JSON.stringify(reply)));
	const { tree } = mount("scan");
	await act(async () => {
		camera(tree).props.onBarcodeScanned({ data: invitationText(invitation) });
	});
	expect(pairingWith(tree)).toBe(invitation.origin);
	expect(mocks.fetch).toHaveBeenCalledTimes(1);
	expect(globalFetch).not.toHaveBeenCalled();
	expect(mocks.saveHub).not.toHaveBeenCalled();
});

it("cancels a pending approval before its parent commits unmount", async () => {
	vi.useFakeTimers();
	const requests = captureClaims(false);
	vi.stubGlobal("fetch", mocks.fetch);
	const { invitation, reply } = approvedPairingFixture();
	const onApproved = vi.fn(),
		onCancel = vi.fn();
	// The parent records Cancel but deliberately has not committed unmount yet.
	const tree = render(<PairingApproval invitation={invitation} onApproved={onApproved} onCancel={onCancel} />);
	const first = requests[0];
	if (!first) throw new Error("Missing initial claim");
	onCancel.mockImplementation(() => expect(first.signal.aborted).toBe(true));
	await press(tree, "Cancel pairing");
	expect(onCancel).toHaveBeenCalledTimes(1);
	await act(async () => {
		first.answer(reply);
	});
	expect(onApproved).not.toHaveBeenCalled();
	expect(first.signal.aborted).toBe(true);
	await advancePairingTime(30000);
	expect(requests).toHaveLength(1);
});

it("does not review a cancelled invitation after another scan starts", async () => {
	captureClaims(false);
	const { tree, invitation } = await startInvitation();
	const staleApproval = tree.root.findByType(PairingApproval).props.onApproved;
	await press(tree, "Cancel pairing");
	await act(async () => {
		camera(tree).props.onBarcodeScanned({ data: invitationText({ ...invitation, invitation: "02".repeat(24) }) });
	});
	await act(async () => {
		staleApproval({ origin: invitation.origin, token: TOKEN });
	});
	expect(pairingWith(tree)).toBeUndefined();
	expect(button(tree, "Cancel pairing")).toBeDefined();
	expect(mocks.saveHub).not.toHaveBeenCalled();
});

it("pauses a retained screen on navigation blur and resumes the same claim on focus", async () => {
	vi.useFakeTimers();
	const requests = captureClaims(false);
	const { tree, reply, invitation } = await startInvitation();
	const first = requests[0];
	if (!first) throw new Error("Missing initial claim");
	act(() => {
		navigation.state.focused = false;
		for (const listener of navigation.listeners.blur) listener();
	});
	expect(first.signal.aborted).toBe(true);
	await act(async () => {
		for (const listener of mocks.appState) listener("background");
		for (const listener of mocks.appState) listener("active");
		first.answer(reply);
	});
	await advancePairingTime(30000);
	expect(requests).toHaveLength(1);
	expect(pairingWith(tree)).toBeUndefined();
	expect(mocks.saveHub).not.toHaveBeenCalled();
	act(() => {
		navigation.state.focused = true;
		for (const listener of navigation.listeners.focus) listener();
	});
	expect(requests).toHaveLength(2);
	const resumed = requests[1];
	if (!resumed) throw new Error("Missing focused claim");
	expect(resumed.body).toEqual(first.body);
	expect(mocks.random).toHaveBeenCalledTimes(1);
	await act(async () => {
		resumed.answer(reply);
	});
	expect(pairingWith(tree)).toBe(invitation.origin);
	expect(mocks.saveHub).not.toHaveBeenCalled();
	await press(tree, "Connect");
	expect(mocks.saveHub).toHaveBeenCalledExactlyOnceWith({
		name: "magic-kingdom",
		origin: invitation.origin,
		token: TOKEN,
	});
});
