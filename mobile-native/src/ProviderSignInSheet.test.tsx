import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { authCalls, boundary } from "./providerSignInTestUtils";
import { ProviderSignInSheet } from "./ProviderSignInSheet";
import { pressable, render, renderedText, textOf, unmountMountedTrees } from "./renderNative.testkit";
import { palettes } from "./design/tokens";
import { Button } from "./sheet/Grouped";

// What the sheet's native edges saw, in order: the clipboard write and the
// in-app browser opening, so a test can tell which came first.
const edges = vi.hoisted(() => ({
	events: [] as string[],
	status: null as string | null,
	dismissals: 0,
	copies: true,
	openFails: false,
}));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	AppState: { currentState: "active", addEventListener: () => ({ remove: () => {} }) },
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("expo-clipboard", () => ({
	setStringAsync: async (text: string) => {
		edges.events.push(`copy ${text}`);
		return edges.copies;
	},
}));
vi.mock("expo-web-browser", () => ({
	openBrowserAsync: async (url: string) => {
		if (edges.openFails) throw new Error("No browser");
		edges.events.push(`open ${url}`);
		return { type: "opened" };
	},
	// No browser is open when the sign-in lands on its own, and the real module
	// rejects then; the sheet must carry on.
	dismissBrowser: async () => {
		edges.dismissals += 1;
		throw new Error("No browser is open");
	},
}));
vi.mock("./board/connectionStatus", () => ({ useConnectionStatusText: () => edges.status }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => ({ state: "ready", fatal: false }) }));

const PROVIDER = "codex-jesse-fsck.com";
const device = {
	provider: PROVIDER,
	flowId: "flow-1",
	userCode: "WDJB-MJHT",
	verificationUrl: "https://auth.example.test/device",
	intervalSeconds: 2,
};
const authorizedStatus = {
	provider: PROVIDER,
	supported: true,
	signedIn: true,
	activeSource: "oauth",
	hasStoredOAuth: true,
};
const NO_OLD_CONTROLS = /\bReconnect\b|Check credential status|Waiting for this hub to reconnect/;

type Answers = Record<string, () => unknown>;

function answering(answers: Answers) {
	return (method: string) => {
		const answer = answers[method];
		if (answer) return answer();
		if (method === "evener/instance/list") return { instances: [], availableProviders: [] };
		throw new Error(`unexpected ${method}`);
	};
}

const deviceFlow: Answers = {
	"evener/auth/device/start": () => device,
	"evener/auth/device/poll": () => ({ state: "pending" }),
};

let flows: { dispose(): void }[] = [];

beforeEach(() => {
	vi.useFakeTimers();
	edges.events = [];
	edges.status = null;
	edges.dismissals = 0;
	edges.copies = true;
});
afterEach(() => {
	unmountMountedTrees();
	for (const flow of flows) flow.dispose();
	flows = [];
	vi.useRealTimers();
});

async function mount(answers: Answers, options: { connected?: boolean; start?: boolean } = {}) {
	const kit = boundary(answering(answers), PROVIDER);
	flows.push(kit.flow);
	const onClose = vi.fn();
	const tree = render(
		<ProviderSignInSheet flow={kit.flow} name={PROVIDER} connected={options.connected ?? true} onClose={onClose} />,
	);
	if (options.start ?? true)
		await act(async () => {
			await kit.flow.start();
		});
	return { ...kit, tree, onClose };
}

async function press(tree: ReactTestRenderer, label: string) {
	const button = pressable(tree, label);
	if (!button) throw new Error(`no ${label} in: ${renderedText(tree)}`);
	await act(async () => {
		button.props.onPress();
		await vi.advanceTimersByTimeAsync(0);
	});
}

function codeText(tree: ReactTestRenderer) {
	return tree.root.findAll((node) => String(node.type) === "Text" && node.props.children === device.userCode)[0];
}

it("titles the device flow with the provider and explains that the code is copied on the way", async () => {
	const { tree } = await mount(deviceFlow);
	const text = renderedText(tree);
	expect(text).toContain(`Sign in to ${PROVIDER}`);
	expect(text).toContain(
		"The sign-in page opens inside the app, and this code is copied for you. Paste it when the page asks for it. The hub finishes signing in on its own.",
	);
	expect(pressable(tree, "Copy code")).toBeDefined();
	expect(pressable(tree, "Open sign-in page")).toBeDefined();
	expect(pressable(tree, "Cancel")).toBeDefined();
	expect(text).not.toMatch(NO_OLD_CONTROLS);
});

it("shows the code in Menlo semibold 26", async () => {
	const { tree } = await mount(deviceFlow);
	const code = codeText(tree);
	expect(code).toBeDefined();
	expect(code?.props.style).toMatchObject({ fontFamily: "Menlo", fontWeight: "600", fontSize: 26 });
});

it("copies the code with Copy code, and says when the copy fails", async () => {
	const { tree } = await mount(deviceFlow);
	await press(tree, "Copy code");
	expect(edges.events).toEqual([`copy ${device.userCode}`]);
	expect(renderedText(tree)).toContain("Code copied");
	// VoiceOver hears the confirmation too.
	expect(pressable(tree, "Code copied")).toBeDefined();

	edges.copies = false;
	const failing = await mount(deviceFlow);
	await press(failing.tree, "Copy code");
	expect(renderedText(failing.tree)).toContain("Could not copy the code. Select it to copy manually.");
});

it("stops saying Code copied when a later copy fails", async () => {
	const { tree } = await mount(deviceFlow);
	await press(tree, "Copy code");
	expect(pressable(tree, "Code copied")).toBeDefined();
	edges.copies = false;
	await press(tree, "Code copied");
	expect(renderedText(tree)).toContain("Could not copy the code. Select it to copy manually.");
	expect(pressable(tree, "Code copied")).toBeUndefined();
	expect(pressable(tree, "Copy code")).toBeDefined();
});

it("opens the sign-in page in the app only after copying the code", async () => {
	const { tree } = await mount(deviceFlow);
	await press(tree, "Open sign-in page");
	expect(edges.events).toEqual([`copy ${device.userCode}`, `open ${device.verificationUrl}`]);
});

it("repeats the code while it waits for you to finish signing in", async () => {
	const { tree } = await mount(deviceFlow);
	await press(tree, "Open sign-in page");
	const text = renderedText(tree);
	expect(text).toContain("Waiting for you to finish signing in…");
	// The code is its own run inside the sentence, read as one line.
	const sentences = tree.root.findAll((node) => String(node.type) === "Text").map((node) => textOf(node));
	expect(sentences).toContain(`Your code is ${device.userCode}.`);
	expect(text).not.toContain("The sign-in page opens inside the app");
	expect(text).not.toMatch(NO_OLD_CONTROLS);
});

it("closes the page and says Signed in when the hub's poll comes back authorized", async () => {
	const { tree, calls } = await mount({
		...deviceFlow,
		"evener/auth/device/poll": () => ({ state: "authorized", status: authorizedStatus }),
	});
	await press(tree, "Open sign-in page");
	await act(async () => {
		await vi.advanceTimersByTimeAsync(2000);
	});
	expect(authCalls(calls).map((call) => call.method)).toEqual(["evener/auth/device/start", "evener/auth/device/poll"]);
	expect(edges.dismissals).toBe(1);
	const text = renderedText(tree);
	expect(text).toContain(`Signed in to ${PROVIDER}`);
	expect(text).toContain("Sessions using it can continue.");
	expect(pressable(tree, "Done")).toBeDefined();
	expect(pressable(tree, "Cancel")).toBeUndefined();
	expect(pressable(tree, "Open sign-in page")).toBeUndefined();
});

it("can still copy the code while it waits, when the automatic copy failed", async () => {
	edges.copies = false;
	const { tree } = await mount(deviceFlow);
	await press(tree, "Open sign-in page");
	expect(renderedText(tree)).toContain("Waiting for you to finish signing in…");
	expect(renderedText(tree)).toContain("Could not copy the code. Select it to copy manually.");
	// The code can be selected, and copied again.
	const code = tree.root.findAll(
		(node) => String(node.type) === "Text" && node.props.selectable === true && node.props.children === device.userCode,
	);
	expect(code).toHaveLength(1);
	edges.copies = true;
	await press(tree, "Copy code");
	expect(renderedText(tree)).toContain("Code copied");
});

it("drops a copy failure once the sign-in lands", async () => {
	edges.copies = false;
	const { tree } = await mount({
		...deviceFlow,
		"evener/auth/device/poll": () => ({ state: "authorized", status: authorizedStatus }),
	});
	await press(tree, "Copy code");
	expect(renderedText(tree)).toContain("Could not copy the code. Select it to copy manually.");
	await act(async () => {
		await vi.advanceTimersByTimeAsync(2000);
	});
	expect(renderedText(tree)).toContain(`Signed in to ${PROVIDER}`);
	expect(renderedText(tree)).not.toContain("Could not copy the code.");
});

it("stops saying it's waiting once a poll fails, until Check again resumes it", async () => {
	let polls = 0;
	const { tree } = await mount({
		...deviceFlow,
		"evener/auth/device/poll": () => {
			polls += 1;
			if (polls === 1) throw new Error("socket closed");
			return { state: "pending" };
		},
	});
	await press(tree, "Open sign-in page");
	expect(renderedText(tree)).toContain("Waiting for you to finish signing in…");
	await act(async () => {
		await vi.advanceTimersByTimeAsync(2000);
	});
	// Nothing is being waited on now: the next check is yours.
	expect(renderedText(tree)).not.toContain("Waiting for you to finish signing in…");
	expect(pressable(tree, "Check again")).toBeDefined();
	await press(tree, "Check again");
	expect(renderedText(tree)).toContain("Waiting for you to finish signing in…");
});

it("offers Check again after a failed poll, which polls again", async () => {
	let polls = 0;
	const { tree, calls } = await mount({
		...deviceFlow,
		"evener/auth/device/poll": () => {
			polls += 1;
			if (polls === 1) throw new Error("socket closed");
			return { state: "pending" };
		},
	});
	await act(async () => {
		await vi.advanceTimersByTimeAsync(2000);
	});
	expect(renderedText(tree)).toContain("Could not check authorization. Retry the check when connected.");
	await press(tree, "Check again");
	expect(authCalls(calls).map((call) => call.method)).toEqual([
		"evener/auth/device/start",
		"evener/auth/device/poll",
		"evener/auth/device/poll",
	]);
	expect(pressable(tree, "Check again")).toBeUndefined();
	expect(renderedText(tree)).not.toMatch(NO_OLD_CONTROLS);
});

it("says the code expired and starts again from Start again", async () => {
	const { tree, calls } = await mount({ ...deviceFlow, "evener/auth/device/poll": () => ({ state: "expired" }) });
	await act(async () => {
		await vi.advanceTimersByTimeAsync(2000);
	});
	const text = renderedText(tree);
	expect(text).toContain("The code expired.");
	expect(text).not.toMatch(NO_OLD_CONTROLS);
	expect(pressable(tree, "Open sign-in page")).toBeUndefined();
	await press(tree, "Start again");
	expect(authCalls(calls).map((call) => call.method)).toEqual([
		"evener/auth/device/start",
		"evener/auth/device/poll",
		"evener/auth/device/start",
	]);
	expect(renderedText(tree)).toContain(device.userCode);
});

it("shows a failed start's message and starts again from Start again", async () => {
	let starts = 0;
	const { tree, calls } = await mount({
		...deviceFlow,
		"evener/auth/device/start": () => {
			starts += 1;
			if (starts === 1) throw new Error("hub refused");
			return device;
		},
	});
	const text = renderedText(tree);
	// What happened, and the one thing to do, never a guess at the connection
	// (spec 5): the status line speaks for the connection.
	// The sheet's title already names the provider; the message never
	// repeats the instance's id.
	expect(text).toContain("The hub couldn't start signing in. Start again.");
	expect(text).not.toContain("connection");
	expect(text).not.toMatch(NO_OLD_CONTROLS);
	await press(tree, "Start again");
	expect(authCalls(calls).map((call) => call.method)).toEqual(["evener/auth/device/start", "evener/auth/device/start"]);
	expect(renderedText(tree)).toContain(device.userCode);
});

it("refuses a sign-in page that isn't plain http(s)", async () => {
	const { tree, flow } = await mount(deviceFlow);
	// The flow checks the URL it was handed; the sheet checks again before it
	// hands one to the browser, so a snapshot that slipped past cannot open.
	const snapshot = flow.getSnapshot();
	vi.spyOn(flow, "getSnapshot").mockReturnValue({
		...snapshot,
		device: { ...device, verificationUrl: "https://user:secret@auth.example.test/device" },
	});
	act(() => tree.update(<ProviderSignInSheet flow={flow} name={PROVIDER} connected onClose={() => {}} />));
	await press(tree, "Open sign-in page");
	// Nothing is copied for a page the sheet won't open.
	expect(edges.events).toEqual([]);
	expect(renderedText(tree)).toContain("Could not open the sign-in page.");
});

it("doesn't say it's waiting when the sign-in page couldn't open", async () => {
	edges.openFails = true;
	try {
		const { tree } = await mount(deviceFlow);
		await press(tree, "Open sign-in page");
		expect(renderedText(tree)).toContain("Could not open the sign-in page.");
		expect(renderedText(tree)).not.toContain("Waiting for you to finish signing in…");
	} finally {
		edges.openFails = false;
	}
});

it("with no device flow, opens the page in the app and finishes from the pasted redirect URL", async () => {
	const { tree, calls } = await mount({
		"evener/auth/device/start": () => ({ provider: PROVIDER, fallback: true }),
		"evener/auth/login/start": () => ({
			provider: PROVIDER,
			flowId: "browser-flow",
			url: "https://auth.example.test/",
		}),
		"evener/auth/login/complete": () => ({ status: authorizedStatus }),
	});
	expect(renderedText(tree)).toContain("paste the full redirect URL");
	expect(pressable(tree, "Finish sign-in")?.props.disabled).toBe(true);
	await press(tree, "Open sign-in page");
	expect(edges.events).toEqual(["open https://auth.example.test/"]);
	const field = tree.root.findAll(
		(node) => String(node.type) === "TextInput" && node.props.accessibilityLabel === "Redirect URL",
	)[0];
	act(() => field?.props.onChangeText("https://localhost/callback?code=fixture"));
	await press(tree, "Finish sign-in");
	expect(authCalls(calls).at(-1)).toMatchObject({
		method: "evener/auth/login/complete",
		params: { provider: PROVIDER, flowId: "browser-flow", redirectUrl: "https://localhost/callback?code=fixture" },
	});
	expect(renderedText(tree)).toContain(`Signed in to ${PROVIDER}`);
	expect(edges.dismissals).toBe(1);
});

it("keeps the hub's controls disabled while not connected, and shows the connection's line", async () => {
	edges.status = "Reconnecting…";
	const { tree } = await mount(
		{
			...deviceFlow,
			"evener/auth/device/start": () => {
				throw new Error("hub refused");
			},
		},
		{ connected: false },
	);
	const text = renderedText(tree);
	expect(text).toContain("Reconnecting…");
	expect(text).not.toMatch(NO_OLD_CONTROLS);
	expect(pressable(tree, "Start again")?.props.disabled).toBe(true);
});

it("disables Check again while not connected", async () => {
	const { tree, flow, connect } = await mount({
		...deviceFlow,
		"evener/auth/device/poll": () => {
			throw new Error("socket closed");
		},
	});
	await act(async () => {
		await vi.advanceTimersByTimeAsync(2000);
	});
	act(() => connect(null));
	act(() => tree.update(<ProviderSignInSheet flow={flow} name={PROVIDER} connected={false} onClose={() => {}} />));
	expect(pressable(tree, "Check again")?.props.disabled).toBe(true);
});

it("has the shared header: Cancel while signing in, Done once signed in", async () => {
	const waiting = await mount(deviceFlow);
	expect(waiting.tree.root.findByProps({ accessibilityRole: "header" }).props.children).toBe(`Sign in to ${PROVIDER}`);
	expect(pressable(waiting.tree, "Cancel")).toBeDefined();
	expect(pressable(waiting.tree, "Done")).toBeUndefined();
	const { tree } = await mount({
		...deviceFlow,
		"evener/auth/device/poll": () => ({ state: "authorized", status: authorizedStatus }),
	});
	await press(tree, "Open sign-in page");
	await act(async () => {
		await vi.advanceTimersByTimeAsync(2000);
	});
	expect(pressable(tree, "Done")).toBeDefined();
	expect(pressable(tree, "Cancel")).toBeUndefined();
});

it("keeps a stretch's bottom space unless a group or a footer follows it", async () => {
	const sectionOf = (tree: ReactTestRenderer, text: string) => {
		let node = tree.root.find((candidate) => String(candidate.type) === "Text" && textOf(candidate) === text);
		while (node.parent && node.props.testID !== "sign-in-stretch") node = node.parent;
		return node.props.style;
	};
	const { tree } = await mount({
		...deviceFlow,
		"evener/auth/device/poll": () => ({ state: "authorized", status: authorizedStatus }),
	});
	// Before the page opens: the explanation and its buttons end the sheet.
	expect(sectionOf(tree, "Open sign-in page").paddingBottom).toBe(16);
	await press(tree, "Open sign-in page");
	await act(async () => {
		await vi.advanceTimersByTimeAsync(2000);
	});
	// Signed in: the confirmation ends the sheet.
	expect(sectionOf(tree, "Sessions using it can continue.").paddingBottom).toBe(28);
	// While waiting, the code's stretch sits over the Open and Copy group,
	// whose own 16pt makes up the rest of the 28.
	const waiting = await mount(deviceFlow);
	await press(waiting.tree, "Open sign-in page");
	expect(sectionOf(waiting.tree, "Waiting for you to finish signing in…").paddingBottom).toBe(12);
	// A page that couldn't open: the explanation sits over the error footer,
	// whose own top padding is the gap.
	edges.openFails = true;
	try {
		const failed = await mount(deviceFlow);
		await press(failed.tree, "Open sign-in page");
		expect(renderedText(failed.tree)).toContain("Could not open the sign-in page.");
		expect(sectionOf(failed.tree, "Open sign-in page").paddingBottom).toBe(0);
	} finally {
		edges.openFails = false;
	}
});

it("Cancel closes the sheet", async () => {
	const { tree, onClose } = await mount(deviceFlow);
	await press(tree, "Cancel");
	expect(onClose).toHaveBeenCalledOnce();
});

it("offers the code's page as the one call to action, with Copy code beside the code, as the prototype does (audit L5)", async () => {
	const { tree } = await mount(deviceFlow);
	const buttons = tree.root.findAll((node) => node.type === Button);
	expect(buttons.map((node) => [node.props.label, node.props.primary ?? false])).toEqual([
		["Copy code", false],
		["Open sign-in page", true],
	]);
	// The hub drops a device flow after hubAuthFlowTTL (app_auth.go); the
	// prototype sets the line in ink-low at 13.
	const expiry = tree.root.find(
		(node) => String(node.type) === "Text" && node.props.children === "The code expires in 15 minutes.",
	);
	expect(Object.assign({}, ...[expiry.props.style].flat())).toMatchObject({
		color: palettes.light.inkLow,
		fontSize: 13,
	});
	// At the largest text sizes Copy code moves under the code rather than
	// pushing past the box.
	const box = tree.root.find((node) => node.type === Button && node.props.label === "Copy code").parent;
	expect(box?.props.style).toMatchObject({ flexWrap: "wrap" });
});
