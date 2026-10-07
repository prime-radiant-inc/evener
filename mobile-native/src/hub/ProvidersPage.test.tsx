// ProvidersPage's provider list issues its listing read from a mount effect,
// and React flushes a child's passive effects before its parent's: that read
// only finds a bound credential store because useCredentialStore binds it from
// a layout effect. The ordering is observable only by mounting the real page,
// which renderNative.testkit makes possible; every native edge the page
// reaches is mocked here and nowhere else.
import type { ComponentProps } from "react";
import { act, type ReactTestInstance } from "react-test-renderer";
import type { ReactTestRenderer } from "react-test-renderer";
import { afterEach, expect, it, vi } from "vitest";
import {
	type AuthStatusResponse,
	ErrorEndpointConflict,
	ErrorInstanceRemoveApplied,
	WireError,
	type ProviderDescriptor,
	type InstanceListResponse,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ConversationClientLike } from "../../../mobile/src/services/conversation";
import { ProviderSignIn } from "../providerSignIn";
import { recordClientReadyHub } from "../connectionIdentity";
import { RECONNECTING_AFTER_MS } from "../board/connectionStatus";
import { INCOMPATIBLE_VERSIONS } from "../connectionRecovery";
import { ProviderEditor } from "../ProviderEditor";
import { GroupFooter, SwitchRow, Tag } from "../sheet/Grouped";
import { MODELS_NOT_CHECKED, providerGoneWhileEditing, UNCONFIRMED_CHANGE } from "../providers/providerCopy";
import { ProviderDetailPage } from "./ProviderDetailPage";
import { back, detailParams, ProvidersStack as ProvidersPage } from "./providersPageTestUtils";
import {
	alertRequests,
	dropped,
	render as mount,
	renderedText,
	screenConnection,
	scriptedClient,
} from "../renderNative.testkit";

// Every page a test mounts is unmounted once the test ends. A page left
// mounted keeps its timers (the listing retry, the sign-in poll) and store
// subscriptions running, and their updates land after the file's last test,
// outside act: React's warning
// about them can reach the console while vitest is tearing the file's worker
// down, which fails the run (#3916).
const mounted: ReactTestRenderer[] = [];
function render(...args: Parameters<typeof mount>): ReactTestRenderer {
	const tree = mount(...args);
	mounted.push(tree);
	return tree;
}
afterEach(() => {
	for (const tree of mounted.splice(0)) act(() => tree.unmount());
});

// What useConnection answers with. vi.hoisted because vi.mock's factory is
// hoisted above every module import and may not close over a module-level let.
const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
vi.mock("react-native", async () => ({
	...(await import("../renderNative.testkit")).nativeModuleMock(),
	AppState: {
		currentState: "active",
		addEventListener: () => ({ remove: () => {} }),
	},
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "fixture-uuid" }));
vi.mock("expo-clipboard", () => ({ setStringAsync: async () => {} }));
vi.mock("expo-web-browser", () => ({
	openBrowserAsync: async () => ({ type: "dismiss" }),
	dismissBrowser: async () => ({ type: "dismiss" }),
}));
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => harness.connection }));
// The page's focus: each registered effect runs once, as on first showing,
// and refocus() runs them again, as coming back to the page does.
const focus = vi.hoisted(() => ({ effects: new Set<() => void>() }));
vi.mock("@react-navigation/native", async () => {
	const { useEffect } = await import("react");
	return {
		useFocusEffect: (effect: () => undefined | (() => void)) =>
			useEffect(() => {
				focus.effects.add(effect);
				effect();
				return () => {
					focus.effects.delete(effect);
				};
			}, [effect]),
		usePreventRemove: (await import("./backGuardTestUtils")).usePreventRemoveMock,
	};
});
function refocus() {
	for (const effect of [...focus.effects]) effect();
}

const rows: InstanceListResponse = {
	instances: [
		{
			name: "work",
			providerId: "anthropic",
			protocol: "https",
			auth: "apiKey",
			implicit: false,
			isDefault: true,
			activeSource: "store",
			hasStoredOAuth: false,
			credentialRequired: true,
			endpointFingerprint: "fp-work",
		},
	],
	availableProviders: [],
	diagnostics: ["from the hub"],
};

/** The Modal whose subtree contains `needle` - the modal a banner test
 * scopes to. The host mock renders every Modal's content regardless of its
 * visible prop, so a tree can hold more than one and content tells them
 * apart. */
function modalContaining(tree: ReactTestRenderer, needle: string): ReactTestInstance {
	const modals = tree.root
		.findAll((node) => (node.type as unknown as string) === "Modal")
		.filter((modal) => subtreeText(modal).includes(needle));
	if (modals.length !== 1) throw new Error(`expected one modal containing "${needle}"`);
	return modals[0];
}

/** Every string under a node - the modal-scoped counterpart of renderedText. */
function subtreeText(node: ReactTestInstance): string {
	const chunks: string[] = [];
	const visit = (value: ReactTestInstance | ReactTestInstance[] | string) => {
		if (typeof value === "string") {
			chunks.push(value);
			return;
		}
		if (Array.isArray(value)) {
			for (const entry of value) visit(entry);
			return;
		}
		for (const child of value.children) visit(child);
	};
	visit(node);
	return chunks.join(" ");
}

/** Drops the connection under a mounted page, as the hub going away does,
 * and lets the page's status line say so: it has been on screen since the
 * connection was live, so it speaks when its clock reaches spec 14's 2
 * seconds (useConnectionStatusText). */
async function dropUnder(update: () => void) {
	vi.useFakeTimers();
	try {
		harness.connection = dropped(harness.connection, "reconnecting", 0);
		await act(async () => update());
		await act(async () => {
			await vi.advanceTimersByTimeAsync(RECONNECTING_AFTER_MS);
		});
	} finally {
		vi.useRealTimers();
	}
}

/** The page also reads the hub's sign-in statuses (evener/auth/list) for the
 * words beside each provider; these tests pin the instance calls. */
function instanceCalls(methods: string[]): string[] {
	return methods.filter((method) => method !== "evener/auth/list");
}

/** The one rendered (host) control labelled `name`: Row forwards its label
 * to the Pressable it draws, so the composite and the host both carry it. */
function control(tree: ReactTestRenderer, name: string): ReactTestInstance {
	return tree.root.find((node) => typeof node.type === "string" && node.props.accessibilityLabel === name);
}

/** The pushed detail's edit modal: the editor's frame the detail hosts, the
 * first modal in its tree (the paste sheet, when open, follows it). */
function editModal(tree: ReactTestRenderer): ReactTestInstance {
	const modal = tree.root.findByType(ProviderDetailPage).findAllByType("Modal" as never)[0];
	if (!modal) throw new Error("no edit modal in the detail");
	return modal;
}

/** Whether a control labelled `name` is on screen. */
function hasControl(tree: ReactTestRenderer, name: string): boolean {
	return (
		tree.root.findAll((node) => typeof node.type === "string" && node.props.accessibilityLabel === name).length > 0
	);
}

it("mounts on a ready client and issues and publishes the listing read", async () => {
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	// The read is issued from the list's mount effect and answered asynchronously.
	await act(async () => {});
	expect(instanceCalls(hub.methods)).toEqual(["evener/instance/list"]);
	// Published: the row the store applied and the listing's diagnostics are
	// what the page renders. A read that found no bound client would throw
	// and leave the page on its empty state instead.
	const text = renderedText(tree);
	expect(text).toContain("work");
	expect(text).toContain("from the hub");
});

it("ready -> reconnecting keeps the screen tree mounted and shows the banner", async () => {
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("work");
	expect(renderedText(tree)).not.toContain("Reconnecting…");

	await dropUnder(() => tree.update(<ProvidersPage {...props} />));
	const text = renderedText(tree);
	// The list stayed mounted through the flap (never replaced by Connecting) ...
	expect(text).toContain("work");
	// ... behind a banner announcing it, with no Reconnect: the app
	// reconnects on its own (spec principle 2).
	expect(text).toContain("Reconnecting…");
	expect(tree.root.findAllByProps({ accessibilityLabel: "Reconnect" })).toHaveLength(0);
});

it("reconnecting -> ready removes the banner", async () => {
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});
	await dropUnder(() => tree.update(<ProvidersPage {...props} />));
	expect(renderedText(tree)).toContain("Reconnecting…");

	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	const text = renderedText(tree);
	expect(text).toContain("work");
	expect(text).not.toContain("Reconnecting…");
});

it("a fatal (protocol) close replaces the mounted list with the compatibility sentence", async () => {
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("work");

	// `client: hub.client` deliberately kept set - a real hubConnection.ts
	// keeps it set on "closed" too, and this test must prove the sentence comes
	// from `fatal`, not from `client` dropping to null.
	harness.connection = { ...harness.connection, state: "closed", fatal: true };
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	const text = renderedText(tree);
	expect(text).not.toContain("work");
	expect(text).toContain(INCOMPATIBLE_VERSIONS);
});

it("keeps the list away through a fatal retry until the replacement is ready", async () => {
	const hub = scriptedClient(rows);
	const replacement = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});

	harness.connection = { ...harness.connection, state: "closed", fatal: true };
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});

	harness.connection = {
		...harness.connection,
		client: replacement.client,
		state: "connecting",
		fatal: false,
	};
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	expect(renderedText(tree)).toContain("Connecting to Work hub…");
	expect(replacement.methods).toEqual([]);

	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	await act(async () => {});
	expect(replacement.methods.length).toBeGreaterThan(0);
	expect(instanceCalls(replacement.methods).length).toBeGreaterThan(0);
	expect(instanceCalls(replacement.methods).every((method) => method === "evener/instance/list")).toBe(true);
});

// A provider's detail pushes over the list, as a host's does (device audit
// N3): the stack titles it with the provider's name and offers Back.
it("pushes a provider's detail over the list, and Back returns to it", async () => {
	providersHub([instance({ name: "lunaroute", authModes: ["apiKey"], hasStoredFile: true })]);
	const { tree } = mountPage();
	await act(async () => {});
	press(tree, (label) => label.startsWith("lunaroute,"));
	await act(async () => {});
	expect(detailParams()).toEqual({ hubId: "hub-1", name: "lunaroute" });
	expect(hasControl(tree, "Sign-in, API key")).toBe(true);
	// No modal holds it: the stack does.
	expect(tree.root.findAllByType("Modal" as never).every((modal) => !modal.props.visible)).toBe(true);
	back();
	await act(async () => {});
	expect(detailParams()).toBeNull();
	expect(hasControl(tree, "Sign-in, API key")).toBe(false);
});

it("keeps the provider editor draft through a flap, with no Reconnect anywhere", async () => {
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});

	await act(async () => {
		control(tree, "Add provider").props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Instance name" }).props.onChangeText("draft-name");
	});

	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	const editorInput = tree.root.findByProps({ accessibilityLabel: "Instance name" });
	expect(editorInput.props.value).toBe("draft-name");
	// The app reconnects on its own (spec principle 2).
	expect(tree.root.findAllByProps({ accessibilityLabel: "Reconnect" })).toHaveLength(0);
});

it("a flap disables provider mutation controls, not only OAuth sign-in", async () => {
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});
	const row = tree.root.findAll(
		(node) =>
			(node.type as unknown) === "Pressable" &&
			typeof node.props.accessibilityLabel === "string" &&
			node.props.accessibilityLabel.startsWith("work"),
	)[0];
	await act(async () => {
		row.props.onPress();
	});
	// Named by their row text, which Row forwards as accessibilityLabel.
	const label = (name: string) => control(tree, name);
	expect(label("Add provider").props.disabled).toBe(false);
	expect(label("Test connection").props.disabled).toBe(false);
	expect(label("Edit").props.disabled).toBe(false);
	expect(label("Clear credentials").props.disabled).toBe(false);
	expect(label("Remove").props.disabled).toBe(false);

	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	expect(label("Add provider").props.disabled).toBe(true);
	expect(label("Test connection").props.disabled).toBe(true);
	expect(label("Edit").props.disabled).toBe(true);
	expect(label("Clear credentials").props.disabled).toBe(true);
	expect(label("Remove").props.disabled).toBe(true);
});

/** Presses the one rendered control whose accessibility label matches. */
function press(tree: ReactTestRenderer, matches: (label: string) => boolean) {
	const target = tree.root.find(
		(node) =>
			typeof node.type === "string" &&
			typeof node.props.accessibilityLabel === "string" &&
			matches(node.props.accessibilityLabel),
	);
	act(() => {
		target.props.onPress();
	});
}

/** Drives the page to a selected instance's removal confirmation. */
async function openRemoveConfirmation(tree: ReactTestRenderer) {
	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
	press(tree, (label) => label === "Remove");
	const request = alertRequests.at(-1);
	const confirm = request?.buttons?.find((button) => button.style === "destructive");
	if (!confirm?.onPress) throw new Error("the remove confirmation was not opened");
	act(() => {
		confirm.onPress?.();
	});
	await act(async () => {});
	await act(async () => {});
}

// The hub's applied-removal discriminator is a standing write, not a failure:
// the screen must clear the editor it belonged to, re-read the provider list
// instead of waiting for the passive evener/auth/updated notification, and
// warn. The warning never repeats the hub's response text (it can echo
// submitted credentials), so it is this client's own wording.
it("reconciles an applied removal and warns instead of reporting a failure", async () => {
	alertRequests.length = 0;
	const emptied: InstanceListResponse = {
		instances: [],
		availableProviders: [],
	};
	const hub = scriptedClient(rows, {
		"evener/instance/remove": [
			new WireError("the hub left work's stored key behind", -32603, {
				evenerErrorInfo: ErrorInstanceRemoveApplied,
			}),
		],
		"evener/instance/list": [rows, emptied],
	});
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});

	await openRemoveConfirmation(tree);

	expect(instanceCalls(hub.methods)).toEqual([
		"evener/instance/list",
		"evener/instance/remove",
		"evener/instance/list",
	]);
	const text = renderedText(tree);
	expect(text).toContain("The provider was removed, but a later step failed. Check the list.");
	expect(text).not.toContain("The hub didn't confirm the change.");
	// Secret-safety: the hub's own text can echo submitted credentials, so the
	// warning above must never carry it.
	expect(text).not.toContain("the hub left work's stored key behind");
	// The editor and its selection are gone, like a completed removal.
	expect(hasControl(tree, "Remove")).toBe(false);
	expect(hasControl(tree, "Test connection")).toBe(false);
});

// An ordinary refusal keeps today's behavior: the generic error line and no
// refresh, so the reconciliation stays scoped to the discriminator.
it("keeps the generic failure path for an ordinary removal refusal", async () => {
	alertRequests.length = 0;
	const hub = scriptedClient(rows, {
		"evener/instance/remove": [
			new WireError("removal refused: work is still referenced by a launch config", -32013, {
				evenerErrorInfo: "conflict",
			}),
		],
	});
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});

	await openRemoveConfirmation(tree);

	expect(instanceCalls(hub.methods)).toEqual(["evener/instance/list", "evener/instance/remove"]);
	const text = renderedText(tree);
	expect(text).toContain("The hub didn't confirm the change.");
	expect(text).not.toContain("work no longer resolves");
	// The editor stays open on the instance the refusal names.
	expect(hasControl(tree, "Remove")).toBe(true);
});

// A covered screen is out of the window, and a React Native modal presents
// only from one in the window: the edit's modal lives in the pushed detail,
// not in the Providers page under it (review C1: Edit did nothing on device).
it("opens a provider's editor from inside its pushed detail", async () => {
	providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true })]);
	const { tree } = mountPage();
	await act(async () => {});
	await openWork(tree);
	press(tree, (label) => label === "Edit");
	await act(async () => {});
	const detail = tree.root.findByType(ProviderDetailPage);
	expect(detail.findAllByType(ProviderEditor)).toHaveLength(1);
	expect(tree.root.findAllByType(ProviderEditor)).toHaveLength(1);
});

it("goes back to the provider's detail when its edit is cancelled", async () => {
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = { route: { params: { hubId: "hub-1" } } } as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});
	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
	press(tree, (label) => label === "Edit");
	await act(async () => {});
	expect(renderedText(tree)).toContain("Base URL");
	press(tree, (label) => label === "Cancel");
	await act(async () => {});
	expect(renderedText(tree)).not.toContain("Base URL");
	expect(hasControl(tree, "Edit")).toBe(true);
	// The editor's modal has gone; the detail it was opened from stays pushed.
	expect(tree.root.findAllByType("Modal" as never).every((modal) => !modal.props.visible)).toBe(true);
	expect(detailParams()).toMatchObject({ name: "work" });
});

it("won't let a swipe down drop an edit whose save is still in flight", async () => {
	const hub = scriptedClient(rows);
	let answer = (_rows: InstanceListResponse) => {};
	const request = hub.client.request as (method: string, params?: unknown) => Promise<unknown>;
	hub.client.request = ((method: string, params?: unknown) =>
		method === "evener/instance/edit"
			? new Promise((resolve) => (answer = resolve))
			: request(method, params)) as typeof hub.client.request;
	harness.connection = screenConnection(hub.client, "ready");
	const props = { route: { params: { hubId: "hub-1" } } } as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});
	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
	press(tree, (label) => label === "Edit");
	await act(async () => {});
	press(tree, (label) => label === "Save");
	await act(async () => {});
	await act(async () => {
		editModal(tree).props.onRequestClose();
	});
	expect(editModal(tree).props.visible).toBe(true);
	expect(renderedText(tree)).toContain("Base URL");
	await act(async () => answer(rows));
	await act(async () => {});
	await act(async () => {
		editModal(tree).props.onRequestClose();
	});
	expect(editModal(tree).props.visible).toBe(false);
});

// The editor was opened on one row of the listing, so its save asserts that
// row's endpoint; the hub's refusal of the assertion is its own class, not a
// generic save failure: the editor clears, the provider list is re-read, and
// the screen says in its own words what changed (never the hub's text, which
// can echo submitted values).
it("asserts the row's endpoint on an edit and reconciles the conflict", async () => {
	alertRequests.length = 0;
	const hub = scriptedClient(rows, {
		"evener/instance/edit": [
			new WireError("work no longer resolves to the endpoint this form was opened on", -32013, {
				evenerErrorInfo: ErrorEndpointConflict,
			}),
		],
		"evener/instance/list": [rows, rows],
	});
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});

	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
	press(tree, (label) => label === "Edit");
	await act(async () => {});
	press(tree, (label) => label === "Save");
	await act(async () => {});
	await act(async () => {});

	const edit = hub.requests.find((request) => request.method === "evener/instance/edit");
	expect(edit?.params).toMatchObject({
		name: "work",
		expectedEndpointFingerprint: "fp-work",
	});
	expect(instanceCalls(hub.methods)).toEqual(["evener/instance/list", "evener/instance/edit", "evener/instance/list"]);
	const text = renderedText(tree);
	expect(text).toContain("now points somewhere else");
	expect(text).not.toContain("work no longer resolves");
	// The editor cleared like a completed save; the instance's detail remains.
	expect(text).not.toContain("Base URL");
	expect(hasControl(tree, "Edit")).toBe(true);
});

// A removal asserts the row's endpoint too. The hub's refusal of that
// assertion is the same class as the editor's - not the generic "could not be
// confirmed" failure, whose advice to refresh by hand is the only way out of a
// retry that re-sends the same stale fingerprint. The removal's refusal
// clears the selection like a completed removal, re-reads the provider list so
// the next attempt asserts the destination now on screen, and warns in this
// client's own words.
it("reconciles an endpoint-conflict removal: clears, refreshes, and warns", async () => {
	alertRequests.length = 0;
	const hub = scriptedClient(rows, {
		"evener/instance/remove": [
			new WireError("work no longer resolves to the endpoint this form was opened on", -32013, {
				evenerErrorInfo: ErrorEndpointConflict,
			}),
		],
		"evener/instance/list": [rows, rows],
	});
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});

	await openRemoveConfirmation(tree);

	const removal = hub.requests.find((request) => request.method === "evener/instance/remove");
	expect(removal?.params).toMatchObject({
		name: "work",
		expectedEndpointFingerprint: "fp-work",
	});
	expect(instanceCalls(hub.methods)).toEqual([
		"evener/instance/list",
		"evener/instance/remove",
		"evener/instance/list",
	]);
	const text = renderedText(tree);
	expect(text).toContain("now points somewhere else");
	expect(text).not.toContain("The hub didn't confirm the change.");
	// Secret-safety: the hub's text can echo submitted values and never renders.
	expect(text).not.toContain("work no longer resolves");
	// Cleared like a completed removal: the detail and its actions are gone.
	expect(hasControl(tree, "Remove")).toBe(false);
	expect(hasControl(tree, "Test connection")).toBe(false);
});

// Finding 1: a create collision is a genuine hub conflict (evenerErrorInfo
// "conflict"), not an asserted-destination refusal. Create takes no endpoint
// assertion, so it can never carry the endpoint discriminant; the editor must
// keep the form the user typed and show its own generic line, never the
// moved-endpoint warning that clears the editor.
it("keeps the create form for a name-collision conflict", async () => {
	const collision = new WireError('instance "work" already exists', -32013, { evenerErrorInfo: "conflict" });
	const create = vi.fn(async () => {
		throw collision;
	});
	const onEndpointConflict = vi.fn();
	const tree = render(
		<ProviderEditor
			providers={[{ id: "anthropic", name: "Anthropic" } as ProviderDescriptor]}
			onCreate={create}
			onEdit={vi.fn(async () => true)}
			disabled={false}
			canUseConnection={() => true}
			onSaved={() => {}}
			onEndpointConflict={onEndpointConflict}
			onCancel={() => {}}
		/>,
	);
	await act(async () => {});
	press(tree, (label) => label === "Choose base provider");
	act(() => {});
	press(tree, (label) => label === "Anthropic");
	act(() => {});
	const nameInput = tree.root.find((node) => node.props.accessibilityLabel === "Instance name");
	act(() => {
		nameInput.props.onChangeText("work");
	});
	press(tree, (label) => label === "Save");
	await act(async () => {});
	await act(async () => {});

	expect(create).toHaveBeenCalledTimes(1);
	expect(onEndpointConflict).not.toHaveBeenCalled();
	const text = renderedText(tree);
	expect(text).toContain("The hub didn't confirm the change.");
	expect(text).not.toContain("now points somewhere else");
	// The form survives for the correction.
	expect(text).toContain("Base URL");
});

// Keeping the screen mounted across a flap lets the row move under an open
// editor - another client's change arrives with the recovery read - so the
// save's assertion must be the endpoint this editor was OPENED on, not
// whatever the row resolves to now. An assertion read from the live row
// would match the moved destination, approving a save against a target the
// user never saw.
it("asserts the endpoint the editor was opened on across a flap's recovery", async () => {
	const opened: InstanceListResponse = {
		instances: [{ ...rows.instances[0]!, baseUrl: "https://work.example" }],
		availableProviders: [],
	};
	const moved: InstanceListResponse = {
		instances: [
			{
				...rows.instances[0]!,
				baseUrl: "https://moved.example",
				endpointFingerprint: "fp-moved",
			},
		],
		availableProviders: [],
	};
	const hub = scriptedClient(opened, {
		"evener/instance/list": [opened, moved],
		"evener/instance/edit": [
			new WireError("work no longer resolves to the endpoint this form was opened on", -32013, {
				evenerErrorInfo: ErrorEndpointConflict,
			}),
		],
	});
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});
	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
	press(tree, (label) => label === "Edit");
	await act(async () => {});

	// The flap: the editor and its draft survive behind the banner, and the
	// recovery read republishes the row another client moved while this one
	// was away.
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	await act(async () => {});
	press(tree, (label) => label === "Save");
	await act(async () => {});
	await act(async () => {});

	// The save asserts the endpoint this form was opened on, so the hub - not
	// this client - adjudicates the moved destination.
	const edit = hub.requests.find((request) => request.method === "evener/instance/edit");
	expect(edit?.params).toMatchObject({
		name: "work",
		baseUrl: "https://work.example",
		expectedEndpointFingerprint: "fp-work",
	});
	// The refusal reconciles exactly like the credential flow: the editor
	// clears, the list re-reads, and the warning is this screen's own words.
	expect(instanceCalls(hub.methods)).toEqual([
		"evener/instance/list",
		"evener/instance/list",
		"evener/instance/edit",
		"evener/instance/list",
	]);
	const text = renderedText(tree);
	expect(text).toContain("now points somewhere else");
	expect(text).not.toContain("Base URL");
});

// No over-fencing: a flap whose recovery finds the endpoint unchanged must
// not turn the save into a warning - the open-time assertion still matches
// the row the recovery re-read.
it("saves without a warning when a flap's recovery finds the endpoint unchanged", async () => {
	const opened: InstanceListResponse = {
		instances: [{ ...rows.instances[0]!, baseUrl: "https://work.example" }],
		availableProviders: [],
	};
	const hub = scriptedClient(opened, {
		"evener/instance/list": [opened, opened],
		"evener/instance/edit": [opened],
	});
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});
	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
	press(tree, (label) => label === "Edit");
	await act(async () => {});
	const url = tree.root.find((node) => node.props.accessibilityLabel === "Base URL");
	act(() => {
		url.props.onChangeText("https://work2.example");
	});

	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	await act(async () => {});
	press(tree, (label) => label === "Save");
	await act(async () => {});
	await act(async () => {});

	const edit = hub.requests.find((request) => request.method === "evener/instance/edit");
	expect(edit?.params).toMatchObject({
		name: "work",
		baseUrl: "https://work2.example",
		expectedEndpointFingerprint: "fp-work",
	});
	// The save applied: no conflict warning, and the editor closed on the
	// completed write.
	expect(instanceCalls(hub.methods)).toEqual(["evener/instance/list", "evener/instance/list", "evener/instance/edit"]);
	const text = renderedText(tree);
	expect(text).not.toContain("now points somewhere else");
	expect(text).not.toContain("Base URL");
});

it("shows the connection status inside an open editor modal, with no Reconnect", async () => {
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});
	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
	press(tree, (label) => label === "Edit");
	await act(async () => {});

	// The connection drops with the editor modal open. The native modal
	// covers the page's status line, so the status has to live inside it -
	// with the draft still intact.
	await dropUnder(() => tree.update(<ProvidersPage {...props} />));
	const modal = modalContaining(tree, "Base URL");
	expect(subtreeText(modal)).toContain("Reconnecting…");
	expect(modal.findAll((node) => node.props.accessibilityLabel === "Reconnect")).toHaveLength(0);
	// The draft survived: the modal is still the editor's.
	expect(subtreeText(modal)).toContain("Base URL");
});

it("starts a sign-in from behind the banner without a doomed client", async () => {
	const fake = new FakeClient("ready");
	const oauth: InstanceListResponse = {
		instances: [{ ...rows.instances[0]!, auth: "oauth", authModes: ["oauth"] }],
		availableProviders: [],
	};
	fake.on("evener/instance/list", () => oauth);
	fake.on("evener/auth/device/start", () => ({
		provider: "work",
		flowId: "flow-1",
		userCode: "WORK-1234",
		verificationUrl: "https://example.test/verify",
		intervalSeconds: 5,
	}));
	const setConnection = vi.spyOn(ProviderSignIn.prototype, "setConnection");
	harness.connection = screenConnection(fake as unknown as ConversationClientLike, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});
	// A flap the screen survives behind its banner.
	harness.connection = dropped(harness.connection);
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
	press(tree, (label) => label === "Sign in");
	await act(async () => {});

	// The flow is handed null - the raw client cannot reach the hub - and
	// the sheet the sign-in opens carries the status itself, over the
	// native modal that covers the screen's banner.
	expect(setConnection.mock.calls[0]?.[0]).toBe(null);
	const sheet = modalContaining(tree, "Sign in to work");
	expect(subtreeText(sheet)).toContain("Reconnecting…");
	expect(sheet.findAll((node) => node.props.accessibilityLabel === "Reconnect")).toHaveLength(0);

	// Recovery hands the flow the connection it was opened without, and the
	// exchange proceeds.
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	await act(async () => {});
	await act(async () => {});
	expect(setConnection).toHaveBeenCalledWith(fake);
	expect(fake.calls.map((call) => call.method)).toContain("evener/auth/device/start");
	expect(renderedText(tree)).toContain("WORK-1234");
	setConnection.mockRestore();
});

it("resumes a banner-started sign-in after a manual retry's listing read lands", async () => {
	const oauth: InstanceListResponse = {
		instances: [{ ...rows.instances[0]!, auth: "oauth", authModes: ["oauth"] }],
		availableProviders: [],
	};
	const first = new FakeClient("ready");
	first.on("evener/instance/list", () => oauth);
	harness.connection = screenConnection(first as unknown as ConversationClientLike, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
	press(tree, (label) => label === "Sign in");
	await act(async () => {});

	// A manual retry replaces the client. The replacement's own listing
	// read is still on the wire when the connection turns ready, and the
	// rows the screen has are a replaced connection's: the store refuses
	// writes against them until that read lands.
	let resolveListing: (value: InstanceListResponse) => void = () => {};
	const listing = new Promise<InstanceListResponse>((resolve) => {
		resolveListing = resolve;
	});
	const second = new FakeClient("ready");
	second.on("evener/instance/list", () => listing);
	second.on("evener/auth/device/start", () => ({
		provider: "work",
		flowId: "flow-2",
		userCode: "WORK-5678",
		verificationUrl: "https://example.test/verify",
		intervalSeconds: 5,
	}));
	harness.connection = {
		...harness.connection,
		client: second as unknown as ConversationClientLike,
		state: "connecting",
	};
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	await act(async () => {});

	// The resume waits: starting against the stale rows would refuse the
	// device start and strand the flow in its error phase.
	expect(renderedText(tree)).not.toContain("couldn't start signing in");

	// The listing the new connection owes lands; the gate clears and the
	// idle exchange finally runs.
	resolveListing(oauth);
	await act(async () => {});
	await act(async () => {});
	await act(async () => {});
	expect(second.calls.map((call) => call.method)).toContain("evener/auth/device/start");
	expect(renderedText(tree)).toContain("WORK-5678");
});

it("treats a route re-keyed to another hub as a fresh screen", async () => {
	const fakeA = new FakeClient("ready");
	fakeA.on("evener/instance/list", () => rows);
	harness.connection = screenConnection(fakeA as unknown as ConversationClientLike, "ready");
	const forHub = (hubId: string) =>
		({ route: { params: { hubId } } }) as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...forHub("hub-1")} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("work");

	// The mounted instance is re-keyed to another hub while that hub's
	// connection is still opening: hub-1's rows must not render under
	// hub-2's params, and the page must meet the new hub like a fresh
	// mount - connecting, for a hub it has never been ready for.
	const fakeB = new FakeClient("connecting");
	const rowsB: InstanceListResponse = {
		instances: [{ ...rows.instances[0]!, name: "from-b" }],
		availableProviders: [],
	};
	fakeB.on("evener/instance/list", () => rowsB);
	harness.connection = {
		activeProfile: { id: "hub-2", name: "Two hub" },
		client: null,
		state: "connecting",
		fatal: false,
		// The status clock a real connection always reports: never down yet.
		downSince: null,
		lastLiveAt: null,
	};
	await act(async () => {
		tree.update(<ProvidersPage {...forHub("hub-2")} />);
	});
	const rekeyed = renderedText(tree);
	expect(rekeyed).not.toContain("work");
	expect(rekeyed).toContain("Connecting to Two hub…");

	// The new hub is a fresh mount: its own rows render once its
	// connection is ready.
	fakeB.state = "ready";
	harness.connection = {
		...harness.connection,
		client: fakeB as unknown as ConversationClientLike,
		state: "ready",
	};
	await act(async () => {
		tree.update(<ProvidersPage {...forHub("hub-2")} />);
	});
	await act(async () => {});
	expect(renderedText(tree)).toContain("from-b");
});

// Round 63's Medium, the screen half of round 58's High: the display hook's
// trust arm was client-blind, so a keyed remount onto the previous hub's
// still-ready client rendered the normal provider surface while the client
// and readiness hooks refused the pairing - a sign-in opened there had a
// surface to run from. The display now requires the identity record's
// verdict before a ready state earns trust, so the window renders
// Connecting instead: no affordance mounts and no exchange can run
// against the previous hub's client. The resume arm's own mechanics stay
// pinned by the banner-retry test above; the cross-hub window it used to
// guard alone is now closed before the surface can mount.
it("holds the re-key window back from the previous hub's recorded client", async () => {
	const oauth: InstanceListResponse = {
		instances: [{ ...rows.instances[0]!, auth: "oauth", authModes: ["oauth"] }],
		availableProviders: [],
	};
	const stale = new FakeClient("ready");
	stale.on("evener/instance/list", () => oauth);
	stale.on("evener/auth/device/start", () => ({
		provider: "work",
		flowId: "flow-stale",
		userCode: "WORK-1234",
		verificationUrl: "https://example.test/verify",
		intervalSeconds: 5,
	}));
	// The record's whole content: this client proved ready under hub-1.
	recordClientReadyHub(stale, "hub-1");
	const setConnection = vi.spyOn(ProviderSignIn.prototype, "setConnection");
	harness.connection = {
		activeProfile: { id: "hub-2", name: "New hub" },
		client: stale as unknown as ConversationClientLike,
		state: "ready",
		fatal: false,
		// The status clock a real connection always reports: never down yet.
		downSince: null,
		lastLiveAt: null,
	};
	const props = {
		route: { params: { hubId: "hub-2" } },
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});
	await act(async () => {});

	// The first-load wait replaces the surface: no sign-in affordance mounts
	// and no exchange runs against the previous hub's client — the strongest
	// form of the round-58 contract, closed one layer up.
	expect(tree.root.findAllByProps({ accessibilityLabel: "Loading providers" })).not.toHaveLength(0);
	expect(tree.root.findAll((node) => typeof node.props.onSignIn === "function")).toHaveLength(0);
	expect(stale.calls.map((call) => call.method)).not.toContain("evener/auth/device/start");

	// The connection re-points: its own dial for hub-2 transitions into
	// ready on the replacement — recorded for hub-2 at ITS dial — and the
	// surface earns its screen. A sign-in opened now runs on that client
	// alone.
	const replacement = new FakeClient("ready");
	replacement.on("evener/instance/list", () => oauth);
	replacement.on("evener/auth/device/start", () => ({
		provider: "work",
		flowId: "flow-live",
		userCode: "WORK-5678",
		verificationUrl: "https://example.test/verify",
		intervalSeconds: 5,
	}));
	recordClientReadyHub(replacement, "hub-2");
	harness.connection = {
		...harness.connection,
		client: null,
		state: "reconnecting",
	};
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	harness.connection = {
		...harness.connection,
		client: replacement as unknown as ConversationClientLike,
		state: "ready",
	};
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	await act(async () => {});

	// Open a sign-in by invoking the screen's own callback, exactly as the
	// row's affordance does.
	const openers = tree.root.findAll((node) => typeof node.props.onSignIn === "function");
	if (openers.length === 0) throw new Error("no onSignIn affordance mounted");
	act(() => openers[0]!.props.onSignIn("work"));
	await act(async () => {});

	// The flow receives the re-pointed client and proceeds; the previous
	// hub's client was never handed over.
	expect(setConnection).toHaveBeenCalledWith(replacement);
	expect(setConnection).not.toHaveBeenCalledWith(stale);
	expect(replacement.calls.map((call) => call.method)).toContain("evener/auth/device/start");
	expect(renderedText(tree)).toContain("WORK-5678");
	setConnection.mockRestore();
});

// The redesign's list and detail (spec 12's Providers; rulings 6, 9 and 12).

/** A ready hub serving `instances` and the sign-in statuses `auth`. */
function providersHub(instances: InstanceListResponse["instances"], auth: AuthStatusResponse[] = []) {
	const fake = new FakeClient("ready");
	fake.on("evener/instance/list", () => ({ instances, availableProviders: [] }));
	fake.on("evener/auth/list", () => ({ providers: auth }));
	harness.connection = screenConnection(fake as unknown as ConversationClientLike, "ready");
	return fake;
}

function mountPage(params: { focus?: string; signIn?: boolean } = {}) {
	const navigation = { setParams: vi.fn() };
	const props = {
		route: { params: { hubId: "hub-1", ...params } },
		navigation,
	} as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	return { tree, navigation };
}

const instance = (over: Partial<InstanceListResponse["instances"][number]>) => ({
	...rows.instances[0]!,
	...over,
});

it("reads the listing again on coming back to the page after a read failed, with nothing to press", async () => {
	const fake = new FakeClient("ready");
	let reads = 0;
	fake.on("evener/instance/list", () => {
		reads += 1;
		if (reads === 1) throw new Error("hub busy");
		return { instances: [instance({ authModes: ["apiKey"] })], availableProviders: [] };
	});
	fake.on("evener/auth/list", () => ({ providers: [] }));
	harness.connection = screenConnection(fake as unknown as ConversationClientLike, "ready");
	const { tree } = mountPage();
	await act(async () => {});
	await act(async () => {});
	expect(renderedText(tree)).toContain("Couldn't load the providers");
	expect(renderedText(tree)).not.toMatch(/\bRetry\b|\bReconnect\b/);
	await act(async () => {
		refocus();
	});
	await act(async () => {});
	expect(reads).toBe(2);
	expect(renderedText(tree)).not.toContain("Couldn't load the providers");
});

it('calls no account sign-in "Signed in" before the hub\'s statuses are read', async () => {
	const fake = providersHub([
		instance({ name: "codex", providerId: "openai-codex", activeSource: "oauth", authModes: ["oauth"] }),
	]);
	fake.on("evener/auth/list", () => {
		throw new Error("hub busy");
	});
	const { tree } = mountPage();
	await act(async () => {});
	await act(async () => {});
	expect(renderedText(tree)).toContain("codex");
	expect(renderedText(tree)).not.toContain("Signed in");
});

it("says each provider's sign-in state, with only an expired sign-in in amber", async () => {
	providersHub(
		[
			instance({
				name: "codex-jesse-fsck.com",
				providerId: "openai-codex",
				activeSource: "oauth",
				authModes: ["oauth"],
			}),
			instance({ name: "lunaroute", providerId: "openai", isDefault: false, authModes: ["apiKey"] }),
			instance({
				name: "kimi-code",
				providerId: "moonshot",
				isDefault: false,
				activeSource: "none",
				authModes: ["apiKey"],
			}),
		],
		[{ provider: "codex-jesse-fsck.com", needsLogin: true } as AuthStatusResponse],
	);
	const { tree } = mountPage();
	await act(async () => {});
	await act(async () => {});
	expect(hasControl(tree, "codex-jesse-fsck.com, openai-codex · default, Sign-in expired")).toBe(true);
	expect(hasControl(tree, "lunaroute, openai, Key set")).toBe(true);
	expect(hasControl(tree, "kimi-code, moonshot, No key")).toBe(true);
	const tags = tree.root.findAllByType(Tag);
	expect(tags.map((tag) => tag.props)).toEqual([{ text: "Sign-in expired", tone: "amber" }]);
});

// A credential the provider rejected (#3539): the row's status reads "Error"
// in a red tag, and the detail says what the hub found, in its words.
it("shows a rejected credential as Error, and says why on the detail", async () => {
	const error = "The provider rejected this credential (HTTP 401). Replace the key or sign in again.";
	providersHub(
		[
			instance({
				name: "lunaroute",
				providerId: "openai",
				isDefault: false,
				activeSource: "store",
				authModes: ["apiKey"],
			}),
		],
		[{ provider: "lunaroute", error } as AuthStatusResponse],
	);
	const { tree } = mountPage();
	await act(async () => {});
	await act(async () => {});
	expect(hasControl(tree, "lunaroute, openai, Error")).toBe(true);
	expect(tree.root.findAllByType(Tag).map((tag) => tag.props)).toEqual([{ text: "Error", tone: "red" }]);
	await openDetail(tree, "lunaroute");
	expect(hasControl(tree, "Status, Error")).toBe(true);
	// The hub's sentence stands in a footer with the danger tone.
	const footers = tree.root
		.findAllByType(GroupFooter)
		.filter((node) => node.props.tone === "danger" && subtreeText(node).includes(error));
	expect(footers).toHaveLength(1);
});

it("offers no pull-to-refresh and never asks to reconnect", async () => {
	providersHub([rows.instances[0]!]);
	const { tree } = mountPage();
	await act(async () => {});
	await dropUnder(() =>
		tree.update(
			<ProvidersPage
				{...({ route: { params: { hubId: "hub-1" } } } as unknown as ComponentProps<typeof ProvidersPage>)}
			/>,
		),
	);
	expect(renderedText(tree)).toContain("Reconnecting…");
	expect(renderedText(tree)).not.toMatch(/\bReconnect\b/);
	expect(tree.root.findAll((node) => "refreshing" in node.props || "onRefresh" in node.props)).toHaveLength(0);
});

it("waits quietly, connected, for the first listing, in place of a wall (spec 14)", async () => {
	const fake = new FakeClient("ready");
	fake.on("evener/instance/list", () => new Promise(() => {}));
	harness.connection = screenConnection(fake as unknown as ConversationClientLike, "ready");
	const { tree } = mountPage();
	await act(async () => {});
	expect(renderedText(tree)).not.toContain("Connecting");
	expect(tree.root.findAllByProps({ accessibilityLabel: "Loading providers" })).not.toHaveLength(0);
	expect(hasControl(tree, "Add provider")).toBe(false);
});

it("opens the provider a notice names and starts its sign-in", async () => {
	const fake = providersHub(
		[instance({ activeSource: "oauth", auth: "oauth", authModes: ["oauth"], hasStoredOAuth: true })],
		[{ provider: "work", needsLogin: true } as AuthStatusResponse],
	);
	fake.on("evener/auth/device/start", () => ({
		provider: "work",
		flowId: "flow-1",
		userCode: "WORK-1234",
		verificationUrl: "https://example.test/verify",
		intervalSeconds: 5,
	}));
	const { tree, navigation } = mountPage({ focus: "work", signIn: true });
	await act(async () => {});
	await act(async () => {});
	await act(async () => {});
	expect(fake.calls.map((call) => call.method)).toContain("evener/auth/device/start");
	expect(renderedText(tree)).toContain("WORK-1234");
	expect(navigation.setParams).toHaveBeenCalledWith({ focus: undefined, signIn: undefined });
});

it("opens the provider a link names, once, without signing in", async () => {
	const fake = providersHub([instance({ authModes: ["apiKey"] })]);
	const { tree, navigation } = mountPage({ focus: "work" });
	await act(async () => {});
	await act(async () => {});
	expect(hasControl(tree, "Test connection")).toBe(true);
	expect(fake.calls.map((call) => call.method)).not.toContain("evener/auth/device/start");
	expect(navigation.setParams).toHaveBeenCalledTimes(1);
});

it("acts on each new link while the page stays open", async () => {
	// The Hosts page does the same: a second link reaches a page already open.
	providersHub([instance({ authModes: ["apiKey"] })]);
	const navigation = { setParams: vi.fn() };
	const page = (params: { focus?: string }) =>
		({ route: { params: { hubId: "hub-1", ...params } }, navigation }) as unknown as ComponentProps<
			typeof ProvidersPage
		>;
	const tree = render(<ProvidersPage {...page({ focus: "work" })} />);
	await act(async () => {});
	await act(async () => {});
	expect(navigation.setParams).toHaveBeenCalledTimes(1);
	await act(async () => {
		tree.update(<ProvidersPage {...page({})} />);
	});
	await act(async () => {
		tree.update(<ProvidersPage {...page({ focus: "work" })} />);
	});
	expect(navigation.setParams).toHaveBeenCalledTimes(2);
});

it("shows how each provider signs in, and the actions its sign-in allows", async () => {
	providersHub([
		instance({
			name: "codex",
			providerId: "openai-codex",
			activeSource: "oauth",
			authModes: ["oauth"],
			hasStoredOAuth: true,
			models: [{ id: "gpt-5.6" }, { id: "gpt-5.5", disabled: true }],
		}),
		instance({ name: "lunaroute", isDefault: false, authModes: ["apiKey"], hasStoredFile: true }),
		instance({
			name: "vertex",
			isDefault: false,
			activeSource: "none",
			authModes: ["credentialJson"],
			hasStoredFile: false,
		}),
	]);
	const { tree } = mountPage();
	await act(async () => {});
	// Back to the list between providers: the list is covered while a detail
	// is pushed.
	const open = async (name: string) => {
		if (detailParams()) back();
		press(tree, (label) => label.startsWith(`${name},`));
		await act(async () => {});
	};
	await open("codex");
	expect(hasControl(tree, "Status, Signed in")).toBe(true);
	expect(hasControl(tree, "Type, openai-codex, Default")).toBe(true);
	expect(hasControl(tree, "Sign-in, Account")).toBe(true);
	expect(hasControl(tree, "gpt-5.6")).toBe(true);
	// A disabled model is listed too, with its switch off.
	expect(hasControl(tree, "gpt-5.5")).toBe(true);
	expect(hasControl(tree, "Sign in again")).toBe(true);
	expect(hasControl(tree, "Set key")).toBe(false);
	expect(hasControl(tree, "Replace key")).toBe(false);
	await open("lunaroute");
	expect(hasControl(tree, "Sign-in, API key")).toBe(true);
	expect(hasControl(tree, "Replace key")).toBe(true);
	expect(hasControl(tree, "Sign in")).toBe(false);
	expect(hasControl(tree, "Make default")).toBe(true);
	expect(renderedText(tree)).toContain("No models listed.");
	await open("vertex");
	expect(hasControl(tree, "Sign-in, API key")).toBe(true);
	expect(hasControl(tree, "Set credential JSON")).toBe(true);
	expect(hasControl(tree, "Status, No key")).toBe(true);
});

it("replaces a key in place, saving it against the endpoint the row was read from", async () => {
	const fake = providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true })]);
	fake.on("evener/auth/apiKey/set", () => ({ provider: "work", activeSource: "store" }) as never);
	const { tree } = mountPage();
	await act(async () => {});
	press(tree, (label) => label.startsWith("work,"));
	await act(async () => {});
	press(tree, (label) => label === "Replace key");
	expect(renderedText(tree)).toContain("The key is stored on the hub, not on this phone.");
	const input = control(tree, "API key");
	expect(input.props.placeholder).toBe("Paste the API key");
	expect(input.props.secureTextEntry).toBe(true);
	act(() => input.props.onChangeText(" sk-fixture "));
	press(tree, (label) => label === "Save");
	await act(async () => {});
	await act(async () => {});
	const save = fake.calls.find((call) => call.method === "evener/auth/apiKey/set");
	expect(save?.params).toMatchObject({ provider: "work", value: "sk-fixture", expectedEndpointFingerprint: "fp-work" });
	// Saved: the group is the actions again.
	expect(hasControl(tree, "API key")).toBe(false);
	expect(hasControl(tree, "Replace key")).toBe(true);
});

// Saving an edit returns to the same visit of the provider's detail, as it
// did before the detail pushed (review M3): it neither starts a new visit
// nor clears what the detail last said.
it("keeps the detail's last word through an edit's save", async () => {
	const fake = providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true, isDefault: false })]);
	fake.on("evener/instance/setDefault", () => Promise.reject(new Error("config write failed")));
	fake.on("evener/instance/edit", () => ({
		instances: [instance({ authModes: ["apiKey"], hasStoredFile: true, isDefault: false })],
		availableProviders: [],
	}));
	const { tree } = mountPage();
	await act(async () => {});
	await openWork(tree);
	press(tree, (label) => label === "Make default");
	await act(async () => {});
	expect(renderedText(tree)).toContain(UNCONFIRMED_CHANGE);
	press(tree, (label) => label === "Edit");
	await act(async () => {});
	act(() => control(tree, "Base URL").props.onChangeText("https://changed.example"));
	press(tree, (label) => label === "Save");
	await act(async () => {});
	await act(async () => {});
	expect(renderedText(tree)).not.toContain("Base URL");
	expect(renderedText(tree)).toContain(UNCONFIRMED_CHANGE);
});

it("says a connection test's result under the actions", async () => {
	const fake = providersHub([instance({ authModes: ["apiKey"] })]);
	let status = "success";
	fake.on("evener/auth/test", () => ({ provider: "work", status, message: "" }));
	const { tree } = mountPage();
	await act(async () => {});
	press(tree, (label) => label.startsWith("work,"));
	await act(async () => {});
	press(tree, (label) => label === "Test connection");
	await act(async () => {});
	await act(async () => {});
	expect(renderedText(tree)).toContain("Works");
	status = "auth_rejected";
	press(tree, (label) => label === "Test connection");
	await act(async () => {});
	await act(async () => {});
	expect(renderedText(tree)).not.toContain("Works");
	expect(renderedText(tree)).toContain("The provider rejected these credentials.");
});

async function openWork(tree: ReactTestRenderer) {
	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
}

// Another client removes or renames "work": the listing now holds only
// "home". The store's refetch waits out its debounce, on a fake clock.
async function workLeavesList(fake: ReturnType<typeof providersHub>) {
	fake.on("evener/instance/list", () => ({
		instances: [instance({ name: "home", authModes: ["apiKey"], hasStoredFile: true })],
		availableProviders: [],
	}));
	vi.useFakeTimers();
	try {
		await act(async () => {
			fake.emitNotification({ method: "evener/auth/updated", params: { provider: "work" } } as never);
			await vi.advanceTimersByTimeAsync(1_000);
		});
	} finally {
		vi.useRealTimers();
	}
	await act(async () => {});
}

const choose = (text: string) =>
	act(() =>
		alertRequests
			.at(-1)
			?.buttons?.find((button) => button.text === text)
			?.onPress?.(),
	);

it("asks before Cancel or a swipe throws away an edited provider (spec 6)", async () => {
	alertRequests.length = 0;
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = { route: { params: { hubId: "hub-1" } } } as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});
	await openWork(tree);
	press(tree, (label) => label === "Edit");
	await act(async () => {});
	act(() => control(tree, "Base URL").props.onChangeText("https://changed.example"));
	// A swipe down asks; Keep editing keeps the edit and the sheet.
	act(() => editModal(tree).props.onRequestClose());
	expect(alertRequests.at(-1)?.title).toBe("Discard your changes?");
	choose("Keep editing");
	expect(control(tree, "Base URL").props.value).toBe("https://changed.example");
	// Cancel asks too; Discard goes back to the provider's detail.
	press(tree, (label) => label === "Cancel");
	expect(alertRequests).toHaveLength(2);
	choose("Discard");
	await act(async () => {});
	expect(renderedText(tree)).not.toContain("Base URL");
	expect(hasControl(tree, "Edit")).toBe(true);
});

it("asks before Cancel or a swipe throws away a pasted key (spec 6)", async () => {
	alertRequests.length = 0;
	providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true })]);
	const { tree } = mountPage();
	await act(async () => {});
	await openWork(tree);
	press(tree, (label) => label === "Replace key");
	// Nothing pasted yet: Cancel just closes the field.
	press(tree, (label) => label === "Cancel");
	expect(alertRequests).toHaveLength(0);
	expect(hasControl(tree, "API key")).toBe(false);
	press(tree, (label) => label === "Replace key");
	act(() => control(tree, "API key").props.onChangeText("sk-fixture"));
	// A swipe on the key's own sheet (the innermost modal) asks.
	act(() =>
		tree.root
			.findAllByType("Modal" as never)
			.at(-1)
			?.props.onRequestClose(),
	);
	expect(alertRequests.at(-1)?.title).toBe("Discard your changes?");
	choose("Keep editing");
	expect(control(tree, "API key").props.value).toBe("sk-fixture");
	press(tree, (label) => label === "Cancel");
	choose("Discard");
	expect(hasControl(tree, "API key")).toBe(false);
});

// Back from the pushed detail (the header's or the edge swipe) is guarded as
// the key's own Cancel is: it asks before a pasted key goes (spec 6).
it("asks before Back from the detail throws away a pasted key", async () => {
	alertRequests.length = 0;
	providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true })]);
	const { tree } = mountPage();
	await act(async () => {});
	await openWork(tree);
	press(tree, (label) => label === "Replace key");
	act(() => control(tree, "API key").props.onChangeText("sk-fixture"));
	back();
	expect(alertRequests.at(-1)?.title).toBe("Discard your changes?");
	choose("Keep editing");
	expect(detailParams()).toMatchObject({ name: "work" });
	expect(control(tree, "API key").props.value).toBe("sk-fixture");
	back();
	choose("Discard");
	await act(async () => {});
	expect(detailParams()).toBeNull();
	expect(hasControl(tree, "API key")).toBe(false);
});

// A provider renamed or removed elsewhere leaves the list: its pushed detail
// goes back rather than showing another provider's detail under its name.
it("goes back from a provider's detail when the provider leaves the list", async () => {
	const fake = providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true })]);
	const { tree } = mountPage();
	await act(async () => {});
	await openWork(tree);
	expect(detailParams()).toMatchObject({ name: "work" });
	press(tree, (label) => label === "Replace key");
	act(() => control(tree, "API key").props.onChangeText("sk-fixture"));
	// Another client renames it, and the listing follows.
	await workLeavesList(fake);
	expect(renderedText(tree)).toContain("home");
	expect(detailParams()).toBeNull();
	expect(hasControl(tree, "Replace key")).toBe(false);
	// No edit was open, so nothing says one wasn't saved.
	expect(renderedText(tree)).not.toContain(providerGoneWhileEditing("work"));
	// The key pasted for the provider that left goes with it: the provider now
	// listed opens with no paste sheet and no key (RoboRev on 972ebb8).
	press(tree, (label) => label.startsWith("home,"));
	await act(async () => {});
	expect(hasControl(tree, "API key")).toBe(false);
	expect(tree.root.findAll((node) => node.props.value === "sk-fixture")).toHaveLength(0);
});

// A provider that leaves the list while its editor is open takes the edit
// with it, since there's nothing left to save it to: the page says so rather
// than dropping the draft silently (#3510 review M7).
it("says an open edit wasn't saved when its provider leaves the list", async () => {
	const fake = providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true })]);
	const { tree } = mountPage();
	await act(async () => {});
	await openWork(tree);
	press(tree, (label) => label === "Edit");
	await act(async () => {});
	act(() => control(tree, "Base URL").props.onChangeText("https://changed.example"));
	// Another client removes it, and the listing follows.
	await workLeavesList(fake);
	expect(detailParams()).toBeNull();
	expect(renderedText(tree)).not.toContain("Base URL");
	expect(renderedText(tree)).toContain(providerGoneWhileEditing("work"));
	// The word belongs to the list: opening another provider clears it.
	press(tree, (label) => label.startsWith("home,"));
	await act(async () => {});
	expect(detailParams()).toMatchObject({ name: "home" });
	expect(renderedText(tree)).not.toContain(providerGoneWhileEditing("work"));
});

// Nor does it sit above a new provider's form: opening Add clears it.
it("clears an unsaved-edit notice when Add opens", async () => {
	const fake = providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true })]);
	const { tree } = mountPage();
	await act(async () => {});
	await openWork(tree);
	press(tree, (label) => label === "Edit");
	await act(async () => {});
	await workLeavesList(fake);
	expect(renderedText(tree)).toContain(providerGoneWhileEditing("work"));
	press(tree, (label) => label === "Add provider");
	await act(async () => {});
	expect(tree.root.findAllByType(ProviderEditor)).toHaveLength(1);
	expect(renderedText(tree)).not.toContain(providerGoneWhileEditing("work"));
});

// Only an edit its provider left behind says it wasn't saved: a cancelled or
// saved edit holds no draft, so the provider leaving afterwards has nothing
// unsaved to report.
it("says nothing of an unsaved edit when the provider leaves after a Cancel", async () => {
	const fake = providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true })]);
	const { tree } = mountPage();
	await act(async () => {});
	await openWork(tree);
	press(tree, (label) => label === "Edit");
	await act(async () => {});
	press(tree, (label) => label === "Cancel");
	await act(async () => {});
	expect(renderedText(tree)).not.toContain(providerGoneWhileEditing("work"));
	expect(detailParams()).toMatchObject({ name: "work" });
	await workLeavesList(fake);
	expect(renderedText(tree)).not.toContain(providerGoneWhileEditing("work"));
	expect(detailParams()).toBeNull();
});

it("says nothing of an unsaved edit when the provider leaves after a save", async () => {
	const fake = providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true })]);
	fake.on("evener/instance/edit", () => ({
		instances: [instance({ authModes: ["apiKey"], hasStoredFile: true })],
		availableProviders: [],
	}));
	const { tree } = mountPage();
	await act(async () => {});
	await openWork(tree);
	press(tree, (label) => label === "Edit");
	await act(async () => {});
	act(() => control(tree, "Base URL").props.onChangeText("https://changed.example"));
	press(tree, (label) => label === "Save");
	await act(async () => {});
	await act(async () => {});
	expect(renderedText(tree)).not.toContain("Base URL");
	expect(renderedText(tree)).not.toContain(providerGoneWhileEditing("work"));
	expect(detailParams()).toMatchObject({ name: "work" });
	await workLeavesList(fake);
	expect(renderedText(tree)).not.toContain(providerGoneWhileEditing("work"));
	expect(detailParams()).toBeNull();
});

// Discarding through Back discards the draft for good: the page's close,
// which the leaving detail runs, clears the paste sheet and its key, so the
// provider reopens with neither (RoboRev on 972ebb8).
it("reopens a provider with no paste sheet or key after Back discarded them", async () => {
	alertRequests.length = 0;
	providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true })]);
	const { tree } = mountPage();
	await act(async () => {});
	await openWork(tree);
	press(tree, (label) => label === "Replace key");
	act(() => control(tree, "API key").props.onChangeText("sk-fixture"));
	back();
	choose("Discard");
	await act(async () => {});
	await openWork(tree);
	expect(hasControl(tree, "API key")).toBe(false);
	expect(tree.root.findAll((node) => node.props.value === "sk-fixture")).toHaveLength(0);
	// Nothing left to lose: Back goes without asking.
	alertRequests.length = 0;
	back();
	expect(alertRequests).toHaveLength(0);
	expect(detailParams()).toBeNull();
});

it("holds a swipe down while a pasted key is being saved, without asking", async () => {
	alertRequests.length = 0;
	const fake = providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true })]);
	fake.on("evener/auth/apiKey/set", () => new Promise(() => {}));
	const { tree } = mountPage();
	await act(async () => {});
	await openWork(tree);
	press(tree, (label) => label === "Replace key");
	act(() => control(tree, "API key").props.onChangeText("sk-fixture"));
	press(tree, (label) => label === "Save");
	await act(async () => {});
	// The key's own sheet takes the swipe; it stays while its save runs.
	act(() =>
		tree.root
			.findAllByType("Modal" as never)
			.at(-1)
			?.props.onRequestClose(),
	);
	expect(alertRequests).toHaveLength(0);
	expect(tree.root.findAllByType("Modal" as never)).toHaveLength(3);
});

it("closes the editor without asking once its save lands", async () => {
	alertRequests.length = 0;
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = { route: { params: { hubId: "hub-1" } } } as unknown as ComponentProps<typeof ProvidersPage>;
	const tree = render(<ProvidersPage {...props} />);
	await act(async () => {});
	await openWork(tree);
	press(tree, (label) => label === "Edit");
	await act(async () => {});
	act(() => control(tree, "Base URL").props.onChangeText("https://changed.example"));
	press(tree, (label) => label === "Save");
	await act(async () => {});
	await act(async () => {});
	expect(renderedText(tree)).not.toContain("Base URL");
	expect(alertRequests).toHaveLength(0);
});

it("closes the detail with Back while an unrelated write is in flight, with no key pasted", async () => {
	const fake = providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true, isDefault: false })]);
	fake.on("evener/instance/setDefault", () => new Promise(() => {}));
	const { tree } = mountPage();
	await act(async () => {});
	await openWork(tree);
	press(tree, (label) => label === "Make default");
	await act(async () => {});
	back();
	expect(detailParams()).toBeNull();
});

async function openDetail(tree: ReactTestRenderer, name: string) {
	press(tree, (label) => label.startsWith(`${name},`));
	await act(async () => {});
}

it("names each confirmation's action on its button, and the provider in its message (spec 5)", async () => {
	alertRequests.length = 0;
	providersHub([instance({ authModes: ["apiKey"], activeSource: "store", hasStoredFile: true })]);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	const asked = () => {
		const request = alertRequests.at(-1);
		return [request?.title, request?.message, request?.buttons?.map((button) => button.text)];
	};
	const styles = () => alertRequests.at(-1)?.buttons?.map((button) => button.style);
	press(tree, (label) => label === "Remove");
	expect(asked()).toEqual(["Remove provider?", "work on Work hub", ["Cancel", "Remove"]]);
	expect(styles()).toEqual(["cancel", "destructive"]);
	press(tree, (label) => label === "Clear credentials");
	expect(asked()).toEqual(["Clear credentials?", "work on Work hub", ["Cancel", "Clear credentials"]]);
	expect(styles()).toEqual(["cancel", "destructive"]);
});

it("names the key it clears on the button", async () => {
	alertRequests.length = 0;
	providersHub([instance({ authModes: ["apiKey"], activeSource: "env:WORK_KEY", hasStoredFile: true })]);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	press(tree, (label) => label === "Clear stored key");
	expect(alertRequests.at(-1)?.buttons?.map((button) => button.text)).toEqual(["Cancel", "Clear key"]);
	expect(alertRequests.at(-1)?.buttons?.at(-1)?.style).toBe("destructive");
});

it("names a Google provider's stored credential JSON, not a key, when it clears it", async () => {
	alertRequests.length = 0;
	providersHub([
		instance({ auth: "gcp-adc", authModes: ["credentialJson"], activeSource: "adc", hasStoredFile: true }),
	]);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	press(tree, (label) => label === "Clear stored credential JSON");
	const request = alertRequests.at(-1);
	expect(request?.title).toBe("Clear stored credential JSON?");
	expect(request?.buttons?.map((button) => [button.text, button.style])).toEqual([
		["Cancel", "cancel"],
		["Clear JSON", "destructive"],
	]);
});

it("leaves the Type row untagged for a provider that isn't the default, and keeps warnings under the group", async () => {
	providersHub([instance({ isDefault: false, warnings: ["The key expires soon."] })]);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	expect(hasControl(tree, "Type, anthropic")).toBe(true);
	const page = tree.root;
	expect(page.findAll((node) => String(node.type) === "Text" && node.props.children === "Default")).toHaveLength(0);
	expect(subtreeText(page)).toContain("The key expires soon.");
});

it("keeps a provider's facts in its group: Default as a tag, where it's defined, and each credential", async () => {
	providersHub([
		instance({
			isDefault: true,
			implicit: true,
			activeSource: "env:WORK_KEY",
			hasStoredFile: true,
			authModes: ["apiKey"],
		}),
	]);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	const page = tree.root;
	const text = subtreeText(page);
	expect(text).not.toContain("The default provider.");
	expect(text).not.toContain("From the environment.");
	expect(text).not.toContain("Shadowed");
	const tags = page.findAll((node) => String(node.type) === "Text" && node.props.children === "Default");
	expect(tags).not.toHaveLength(0);
	expect(hasControl(tree, "Defined in, Environment")).toBe(true);
	expect(hasControl(tree, "Credential, Configured via environment variable (WORK_KEY)")).toBe(true);
	expect(hasControl(tree, "Also stored, Configured via stored API key, Not used")).toBe(true);
});

it("calls a shadowed environment variable what it is, never a stored credential", async () => {
	providersHub([instance({ activeSource: "api_key", shadowedEnvVar: "WORK_KEY", hasStoredFile: false })]);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	const shadowed = "Also in the environment, Configured via environment variable (WORK_KEY), Not used";
	expect(hasControl(tree, shadowed)).toBe(true);
	expect(subtreeText(tree.root)).not.toContain("Also stored");
});

it("pastes a key in its own sheet: the action as its title, Cancel and Save, and where the key is kept", async () => {
	const fake = providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true })]);
	fake.on("evener/auth/apiKey/set", () => ({ provider: "work", activeSource: "store" }) as never);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	expect(tree.root.findAllByType("Modal" as never)).toHaveLength(2);
	press(tree, (label) => label === "Replace key");
	const modals = tree.root.findAllByType("Modal" as never);
	expect(modals).toHaveLength(3);
	const sheet = modals[2];
	if (!sheet) throw new Error("no key sheet");
	expect(sheet.findAllByProps({ accessibilityRole: "header" })[0]?.props.children).toBe("Replace key");
	expect(subtreeText(sheet)).toContain("The key is stored on the hub, not on this phone.");
	const save = () => sheet.findByProps({ accessibilityRole: "button", accessibilityLabel: "Save" });
	expect(save().props.disabled).toBe(true);
	act(() => control(tree, "API key").props.onChangeText("sk-fixture"));
	expect(save().props.disabled).toBe(false);
	await act(async () => {
		save().props.onPress();
	});
	await act(async () => {});
	expect(tree.root.findAllByType("Modal" as never)).toHaveLength(2);
	expect(fake.calls.some((call) => call.method === "evener/auth/apiKey/set")).toBe(true);
});

it("says why a key didn't save in its own sheet, which stays open with the key", async () => {
	const fake = providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true })]);
	fake.on("evener/auth/apiKey/set", () => {
		throw new Error("provider said no");
	});
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	press(tree, (label) => label === "Replace key");
	act(() => control(tree, "API key").props.onChangeText("sk-fixture"));
	press(tree, (label) => label === "Save");
	await act(async () => {});
	await act(async () => {});
	const sheet = tree.root.findAllByType("Modal" as never).at(-1);
	if (!sheet) throw new Error("no key sheet");
	expect(subtreeText(sheet)).toContain("The hub didn't confirm the credential was saved.");
	expect(control(tree, "API key").props.value).toBe("sk-fixture");
});

it("titles a Google provider's paste sheet for its credential JSON", async () => {
	providersHub([instance({ auth: "gcp-adc", authModes: ["credentialJson"], hasStoredFile: false })]);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	press(tree, (label) => label === "Set credential JSON");
	const sheet = tree.root.findAllByType("Modal" as never).at(-1);
	if (!sheet) throw new Error("no credential sheet");
	expect(sheet.findAllByProps({ accessibilityRole: "header" })[0]?.props.children).toBe("Set credential JSON");
	// No plaintext field: the sheet shows only what was pasted, and a Paste control.
	expect(hasControl(tree, "Google credential JSON, Not pasted")).toBe(true);
	expect(hasControl(tree, "Paste credential JSON")).toBe(true);
});

it("saves a key from the keyboard's Done only when Save could, never twice", async () => {
	const fake = providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true })]);
	fake.on("evener/auth/apiKey/set", () => new Promise(() => {}));
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	press(tree, (label) => label === "Replace key");
	const submit = () => act(() => control(tree, "API key").props.onSubmitEditing());
	act(() => control(tree, "API key").props.onChangeText("   "));
	submit();
	await act(async () => {});
	expect(fake.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
	act(() => control(tree, "API key").props.onChangeText("sk-fixture"));
	submit();
	await act(async () => {});
	submit();
	await act(async () => {});
	expect(fake.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(1);
});

it("says Saving, busy, and holds Cancel while a pasted key saves", async () => {
	const fake = providersHub([instance({ authModes: ["apiKey"], hasStoredFile: true })]);
	fake.on("evener/auth/apiKey/set", () => new Promise(() => {}));
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	press(tree, (label) => label === "Replace key");
	act(() => control(tree, "API key").props.onChangeText("sk-fixture"));
	press(tree, (label) => label === "Save");
	await act(async () => {});
	const sheet = tree.root.findAllByType("Modal" as never).at(-1);
	if (!sheet) throw new Error("no key sheet");
	const saving = sheet.findByProps({ accessibilityRole: "button", accessibilityLabel: "Saving…" });
	expect(saving.props.accessibilityState).toEqual({ disabled: true, busy: true });
	expect(sheet.findByProps({ accessibilityRole: "button", accessibilityLabel: "Cancel" }).props.disabled).toBe(true);
});

const withModels = () =>
	instance({
		authModes: ["apiKey"],
		models: [{ id: "gpt-5.6" }, { id: "gpt-5.5", disabled: true }],
	});

it("lists every model with a switch, off for one that's disabled, in SF Pro (spec 16.2)", async () => {
	providersHub([withModels()]);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	// VoiceOver reads the model's name, then the switch's own state.
	expect(control(tree, "gpt-5.6").props.value).toBe(true);
	expect(control(tree, "gpt-5.5").props.value).toBe(false);
	const label = tree.root.find((node) => String(node.type) === "Text" && node.props.children === "gpt-5.5");
	expect(JSON.stringify(label.props.style)).not.toContain("Menlo");
});

// A provider such as OpenRouter lists hundreds of models; the detail mounts at
// most MODEL_LIST_CAP switches and offers a search to reach the rest (issue
// #3279, the same decision the web sheet makes).
const withManyModels = (count: number) =>
	instance({
		authModes: ["apiKey"],
		models: Array.from({ length: count }, (_, index) => ({ id: `model-${String(index).padStart(2, "0")}` })),
	});

it("caps a long model list, says how many are hidden, and reaches the rest by search", async () => {
	providersHub([withManyModels(60)]);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	expect(tree.root.findAllByType(SwitchRow)).toHaveLength(50);
	expect(subtreeText(tree.root)).toContain("Showing 50 of 60 models — search to narrow.");
	act(() => control(tree, "Search models").props.onChangeText("model-59"));
	expect(control(tree, "model-59").props.value).toBe(true);
	expect(tree.root.findAllByType(SwitchRow)).toHaveLength(1);
});

it("says so when the model search matches nothing", async () => {
	providersHub([withManyModels(60)]);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	act(() => control(tree, "Search models").props.onChangeText("zzz"));
	expect(tree.root.findAllByType(SwitchRow)).toHaveLength(0);
	expect(subtreeText(tree.root)).toContain("No matching models.");
});

it("leaves a short model list uncapped, with no search field", async () => {
	providersHub([withModels()]);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	expect(tree.root.findAllByType(SwitchRow)).toHaveLength(2);
	expect(hasControl(tree, "Search models")).toBe(false);
});

it("keeps the model search clearable when a refresh drops the list below the cap", async () => {
	const fake = providersHub([withManyModels(60)]);
	fake.on("evener/instance/refreshModels", () => ({
		instances: [withManyModels(2)],
		availableProviders: [],
	}));
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	act(() => control(tree, "Search models").props.onChangeText("zzz"));
	press(tree, (label) => label === "Check for new models");
	await act(async () => {});
	await act(async () => {});
	// The filter is still active, so the field stays to clear it rather than hiding the rows.
	act(() => control(tree, "Search models").props.onChangeText(""));
	expect(tree.root.findAllByType(SwitchRow)).toHaveLength(2);
});

it("clears the model search when a notice opens a different provider", async () => {
	providersHub([withManyModels(60), { ...withManyModels(2), name: "home", isDefault: false }]);
	const { tree, relink } = linkedPage("work");
	await act(async () => {});
	await act(async () => {});
	act(() => control(tree, "Search models").props.onChangeText("zzz"));
	await relink("home");
	// The detail remounted on the new provider, so its models are not filtered by the old query.
	expect(tree.root.findAllByType(SwitchRow)).toHaveLength(2);
	expect(control(tree, "model-00").props.value).toBe(true);
});

it("keeps the model search while only whitespace is typed, even below the cap", async () => {
	const fake = providersHub([withManyModels(60)]);
	fake.on("evener/instance/refreshModels", () => ({
		instances: [withManyModels(2)],
		availableProviders: [],
	}));
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	act(() => control(tree, "Search models").props.onChangeText("   "));
	press(tree, (label) => label === "Check for new models");
	await act(async () => {});
	await act(async () => {});
	// Whitespace trims to no filter, so nothing is hidden, and the field stays to clear it.
	expect(hasControl(tree, "Search models")).toBe(true);
	act(() => control(tree, "Search models").props.onChangeText(""));
	expect(tree.root.findAllByType(SwitchRow)).toHaveLength(2);
});

it("turns a model on or off through the hub", async () => {
	const fake = providersHub([withModels()]);
	fake.on("evener/instance/setModelDisabled", (params: { name: string; model: string; disabled: boolean }) => ({
		instances: [
			{
				...withModels(),
				models: [
					{ id: "gpt-5.6", disabled: params.disabled },
					{ id: "gpt-5.5", disabled: true },
				],
			},
		],
		availableProviders: [],
	}));
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	await act(async () => {
		control(tree, "gpt-5.6").props.onValueChange(false);
	});
	await act(async () => {});
	const call = fake.calls.find((entry) => entry.method === "evener/instance/setModelDisabled");
	expect(call?.params).toMatchObject({ name: "work", model: "gpt-5.6", disabled: true });
	expect(control(tree, "gpt-5.6").props.value).toBe(false);
});

it("asks the provider for new models, and the list takes them in as they land", async () => {
	const fake = providersHub([withModels()]);
	let answer: (value: InstanceListResponse) => void = () => {};
	fake.on(
		"evener/instance/refreshModels",
		() =>
			new Promise<InstanceListResponse>((resolve) => {
				answer = resolve;
			}),
	);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	press(tree, (label) => label === "Check for new models");
	await act(async () => {});
	expect(control(tree, "Checking for new models…").props.accessibilityState).toMatchObject({ disabled: true });
	await act(async () =>
		answer({
			instances: [{ ...withModels(), models: [{ id: "gpt-5.6" }, { id: "gpt-5.5", disabled: true }, { id: "gpt-6" }] }],
			availableProviders: [],
		}),
	);
	await act(async () => {});
	expect(control(tree, "gpt-6").props.value).toBe(true);
	expect(hasControl(tree, "Check for new models")).toBe(true);
});

it("says plainly when it couldn't check for new models", async () => {
	const fake = providersHub([withModels()]);
	fake.on("evener/instance/refreshModels", () => {
		throw new Error("upstream 502");
	});
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	press(tree, (label) => label === "Check for new models");
	await act(async () => {});
	await act(async () => {});
	const text = subtreeText(tree.root);
	expect(text).toContain("The hub couldn't check for new models. Try again in a moment.");
	expect(text).not.toContain("upstream 502");
});

it("holds the model switches while a write runs, and takes the next flip once it lands", async () => {
	const fake = providersHub([withModels()]);
	const answers: ((value: InstanceListResponse) => void)[] = [];
	fake.on(
		"evener/instance/setModelDisabled",
		() =>
			new Promise<InstanceListResponse>((resolve) => {
				answers.push(resolve);
			}),
	);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	await act(async () => control(tree, "gpt-5.6").props.onValueChange(false));
	expect(control(tree, "gpt-5.6").props.disabled).toBe(true);
	expect(control(tree, "gpt-5.5").props.disabled).toBe(true);
	await act(async () =>
		answers[0]?.({
			instances: [
				{
					...withModels(),
					models: [
						{ id: "gpt-5.6", disabled: true },
						{ id: "gpt-5.5", disabled: true },
					],
				},
			],
			availableProviders: [],
		}),
	);
	await act(async () => {});
	expect(control(tree, "gpt-5.5").props.disabled).toBe(false);
	await act(async () => control(tree, "gpt-5.5").props.onValueChange(true));
	const flips = fake.calls
		.filter((call) => call.method === "evener/instance/setModelDisabled")
		.map((call) => call.params);
	expect(flips).toEqual([
		expect.objectContaining({ model: "gpt-5.6", disabled: true }),
		expect.objectContaining({ model: "gpt-5.5", disabled: false }),
	]);
});

it("holds the model switches when the hub refuses writes", async () => {
	const fake = providersHub([withModels()]);
	fake.on("evener/instance/list", () => ({ instances: [withModels()], availableProviders: [], writesRefused: true }));
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	expect(control(tree, "gpt-5.6").props.disabled).toBe(true);
});

it("holds the model switches while the connection is down", async () => {
	providersHub([withModels()]);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	expect(control(tree, "gpt-5.6").props.disabled).toBe(false);
	const props = tree.root.findByType(ProvidersPage).props as ComponentProps<typeof ProvidersPage>;
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	expect(control(tree, "gpt-5.6").props.disabled).toBe(true);
});

it("holds the model switches on a replaced connection's rows until its own listing lands", async () => {
	providersHub([withModels()]);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	const props = tree.root.findByType(ProvidersPage).props as ComponentProps<typeof ProvidersPage>;
	let resolveListing: (value: InstanceListResponse) => void = () => {};
	const second = new FakeClient("ready");
	second.on(
		"evener/instance/list",
		() =>
			new Promise<InstanceListResponse>((resolve) => {
				resolveListing = resolve;
			}),
	);
	second.on("evener/auth/list", () => ({ providers: [] }));
	harness.connection = {
		...harness.connection,
		client: second as unknown as ConversationClientLike,
		state: "connecting",
	};
	await act(async () => tree.update(<ProvidersPage {...props} />));
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => tree.update(<ProvidersPage {...props} />));
	await act(async () => {});
	expect(control(tree, "gpt-5.6").props.disabled).toBe(true);
	await act(async () => resolveListing({ instances: [withModels()], availableProviders: [] }));
	await act(async () => {});
	expect(control(tree, "gpt-5.6").props.disabled).toBe(false);
});

it("snaps a switch back and says so when the hub doesn't take the flip", async () => {
	const fake = providersHub([withModels()]);
	fake.on("evener/instance/setModelDisabled", () => {
		throw new Error("config write failed: /home/jesse/.evener/providers.toml");
	});
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	await act(async () => control(tree, "gpt-5.6").props.onValueChange(false));
	await act(async () => {});
	expect(control(tree, "gpt-5.6").props.value).toBe(true);
	const text = subtreeText(tree.root);
	expect(text).toContain(UNCONFIRMED_CHANGE);
	expect(text).not.toContain("providers.toml");
});

it("keeps a check's Checking state across a detail close, and never reports it elsewhere", async () => {
	const fake = providersHub([withModels()]);
	let fail: (reason: Error) => void = () => {};
	fake.on(
		"evener/instance/refreshModels",
		() =>
			new Promise<InstanceListResponse>((_resolve, reject) => {
				fail = reject;
			}),
	);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	press(tree, (label) => label === "Check for new models");
	await act(async () => {});
	back();
	await act(async () => {});
	await openDetail(tree, "work");
	// The store owns which instance has a check out, so reopening the detail
	// while the call is still in flight reads the same Checking state: the
	// screen no longer forgets the check when the detail closes.
	expect(control(tree, "Checking for new models…").props.accessibilityState).toMatchObject({ disabled: true });
	// The check left behind fails while the detail is open again; its failure
	// still belongs to the visit that started it, so it is not reported here.
	await act(async () => fail(new Error("upstream 502")));
	await act(async () => {});
	expect(renderedText(tree)).not.toContain(MODELS_NOT_CHECKED);
});

it("ends a check's Checking state when it lands, even after a new link reopened the provider", async () => {
	const fake = providersHub([withModels()]);
	let answer: (value: InstanceListResponse) => void = () => {};
	fake.on(
		"evener/instance/refreshModels",
		() =>
			new Promise<InstanceListResponse>((resolve) => {
				answer = resolve;
			}),
	);
	const navigation = { setParams: vi.fn() };
	const page = (params: { focus?: string }) =>
		({ route: { params: { hubId: "hub-1", ...params } }, navigation }) as unknown as ComponentProps<
			typeof ProvidersPage
		>;
	const tree = render(<ProvidersPage {...page({ focus: "work" })} />);
	await act(async () => {});
	await act(async () => {});
	press(tree, (label) => label === "Check for new models");
	await act(async () => {});
	expect(control(tree, "Checking for new models…").props.accessibilityState).toMatchObject({ disabled: true });
	// A second link to the same provider arrives while the check runs.
	await act(async () => tree.update(<ProvidersPage {...page({})} />));
	await act(async () => tree.update(<ProvidersPage {...page({ focus: "work" })} />));
	await act(async () => answer({ instances: [withModels()], availableProviders: [] }));
	await act(async () => {});
	expect(control(tree, "Check for new models").props.accessibilityState).toMatchObject({ disabled: false });
});

it("holds Check for new models while the connection is down", async () => {
	providersHub([withModels()]);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	expect(control(tree, "Check for new models").props.accessibilityState).toMatchObject({ disabled: false });
	const props = tree.root.findByType(ProvidersPage).props as ComponentProps<typeof ProvidersPage>;
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersPage {...props} />);
	});
	expect(control(tree, "Check for new models").props.accessibilityState).toMatchObject({ disabled: true });
});

/** A page opened at `focus`, whose later links arrive through `relink`. */
function linkedPage(focus: string) {
	const navigation = { setParams: vi.fn() };
	const page = (params: { focus?: string }) =>
		({ route: { params: { hubId: "hub-1", ...params } }, navigation }) as unknown as ComponentProps<
			typeof ProvidersPage
		>;
	const tree = render(<ProvidersPage {...page({ focus })} />);
	// A link reaches this page with its list in front: one that names it while
	// a detail is pushed pops back to the list first, through the detail's guard
	// (#3524). So a relink goes back to the list first.
	const relink = async (next: string) => {
		if (detailParams()) back();
		await act(async () => tree.update(<ProvidersPage {...page({})} />));
		await act(async () => tree.update(<ProvidersPage {...page({ focus: next })} />));
	};
	return { tree, relink };
}

/** Answers each evener/instance/refreshModels call in turn, by provider. */
function heldChecks(fake: FakeClient) {
	const pending = new Map<
		string,
		{ resolve: (value: InstanceListResponse) => void; reject: (reason: Error) => void }
	>();
	fake.on(
		"evener/instance/refreshModels",
		(params: { name: string }) =>
			new Promise<InstanceListResponse>((resolve, reject) => {
				pending.set(params.name, { resolve, reject });
			}),
	);
	return pending;
}

// A link reaches the page with its list in front, so the visit that started
// the check has closed, as Back closes it: a failure landing after the link
// isn't reported on the new visit.
it("drops a check's failure that lands after a link reopened the provider", async () => {
	const fake = providersHub([withModels()]);
	const checks = heldChecks(fake);
	const { tree, relink } = linkedPage("work");
	await act(async () => {});
	await act(async () => {});
	press(tree, (label) => label === "Check for new models");
	await act(async () => {});
	await relink("work");
	expect(detailParams()).toMatchObject({ name: "work" });
	await act(async () => checks.get("work")?.reject(new Error("upstream 502")));
	await act(async () => {});
	expect(renderedText(tree)).not.toContain(MODELS_NOT_CHECKED);
});

it("keeps a check's failure off another provider a link opened meanwhile", async () => {
	const fake = providersHub([withModels(), { ...withModels(), name: "home", isDefault: false }]);
	const checks = heldChecks(fake);
	const { tree, relink } = linkedPage("work");
	await act(async () => {});
	await act(async () => {});
	press(tree, (label) => label === "Check for new models");
	await act(async () => {});
	await relink("home");
	await act(async () => checks.get("work")?.reject(new Error("upstream 502")));
	await act(async () => {});
	expect(renderedText(tree)).not.toContain(MODELS_NOT_CHECKED);
});

it("checks two providers back to back: only the newer check ends its Checking state", async () => {
	const fake = providersHub([withModels(), { ...withModels(), name: "home", isDefault: false }]);
	const checks = heldChecks(fake);
	const { tree } = mountPage();
	await act(async () => {});
	await openDetail(tree, "work");
	press(tree, (label) => label === "Check for new models");
	await act(async () => {});
	back();
	await act(async () => {});
	await openDetail(tree, "home");
	press(tree, (label) => label === "Check for new models");
	await act(async () => {});
	const listing = {
		instances: [withModels(), { ...withModels(), name: "home", isDefault: false }],
		availableProviders: [],
	};
	await act(async () => checks.get("work")?.resolve(listing));
	await act(async () => {});
	expect(hasControl(tree, "Checking for new models…")).toBe(true);
	await act(async () => checks.get("home")?.resolve(listing));
	await act(async () => {});
	expect(hasControl(tree, "Check for new models")).toBe(true);
});

// A failed check's copy stays with its own provider's visit: a link that
// opens another provider arrives after that visit closed, and a link back to
// it opens a new one, with no copy from the closed visit.
it("keeps a failed check's copy on its own provider's visit, which a link closes", async () => {
	const fake = providersHub([withModels(), { ...withModels(), name: "home", isDefault: false }]);
	const checks = heldChecks(fake);
	const { tree, relink } = linkedPage("work");
	await act(async () => {});
	await act(async () => {});
	press(tree, (label) => label === "Check for new models");
	await act(async () => {});
	await act(async () => checks.get("work")?.reject(new Error("upstream 502")));
	await act(async () => {});
	expect(renderedText(tree)).toContain(MODELS_NOT_CHECKED);
	await relink("home");
	expect(detailParams()).toMatchObject({ name: "home" });
	expect(renderedText(tree)).not.toContain(MODELS_NOT_CHECKED);
	await relink("work");
	expect(detailParams()).toMatchObject({ name: "work" });
	expect(renderedText(tree)).not.toContain(MODELS_NOT_CHECKED);
});

it("points an empty provider list at its one action (audit L6)", async () => {
	providersHub([]);
	const { tree } = mountPage();
	await act(async () => {});
	expect(renderedText(tree)).toContain("No providers yet. Add one to start sessions.");
});
