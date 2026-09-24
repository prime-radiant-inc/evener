// ProvidersScreen's provider list issues its listing read from a mount effect,
// and React flushes a child's passive effects before its parent's: that read
// only finds a bound credential store because useCredentialStore binds it from
// a layout effect. The ordering is observable only by mounting the real screen,
// which renderNative.testkit makes possible; every native edge the screen
// reaches is mocked here and nowhere else.
import type { ComponentProps } from "react";
import { act, type ReactTestInstance } from "react-test-renderer";
import type { ReactTestRenderer } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import {
	ErrorEndpointConflict,
	ErrorInstanceRemoveApplied,
	WireError,
	type ProviderDescriptor,
	type InstanceListResponse,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { ProviderSignIn } from "./providerSignIn";
import { recordClientReadyHub } from "./connectionIdentity";
import { ProviderEditor } from "./ProviderEditor";
import { ProvidersScreen } from "./ProvidersScreen";
import {
	alertRequests,
	render,
	renderedText,
	screenConnection,
	scriptedClient,
} from "./renderNative.testkit";

// What useConnection answers with. vi.hoisted because vi.mock's factory is
// hoisted above every module import and may not close over a module-level let.
const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
vi.mock("react-native", async () => ({
	...(await import("./renderNative.testkit")).nativeModuleMock(),
	AppState: {
		currentState: "active",
		addEventListener: () => ({ remove: () => {} }),
	},
}));
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "fixture-uuid" }));
vi.mock("expo-clipboard", () => ({ setStringAsync: async () => {} }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));

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
function modalContaining(
	tree: ReactTestRenderer,
	needle: string,
): ReactTestInstance {
	const modals = tree.root
		.findAll((node) => (node.type as unknown as string) === "Modal")
		.filter((modal) => subtreeText(modal).includes(needle));
	if (modals.length !== 1)
		throw new Error(`expected one modal containing "${needle}"`);
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

it("mounts on a ready client and issues and publishes the listing read", async () => {
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	// The read is issued from the list's mount effect and answered asynchronously.
	await act(async () => {});
	expect(hub.methods).toEqual(["evener/instance/list"]);
	// Published: the row the store applied and the listing's diagnostics are
	// what the screen renders. A read that found no bound client would throw
	// and leave the screen on its empty state instead.
	const text = renderedText(tree);
	expect(text).toContain("work");
	expect(text).toContain("from the hub");
});

it("ready -> reconnecting keeps the screen tree mounted and shows the banner", async () => {
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("work");
	expect(renderedText(tree)).not.toContain("Reconnect");

	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	const text = renderedText(tree);
	// The list stayed mounted through the flap (never replaced by the wall) ...
	expect(text).toContain("work");
	// ... behind a banner announcing it.
	expect(text).toContain("reconnecting");
	expect(text).toContain("Reconnect");
});

it("reconnecting -> ready removes the banner", async () => {
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	expect(renderedText(tree)).toContain("Reconnect");

	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	const text = renderedText(tree);
	expect(text).toContain("work");
	expect(text).not.toContain("Reconnect");
});

it("a fatal (protocol) close replaces the mounted list with the wall", async () => {
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("work");

	// `client: hub.client` deliberately kept set - a real hubConnection.ts
	// keeps it set on "closed" too, and this test must prove the wall comes
	// from `fatal`, not from `client` dropping to null.
	harness.connection = { ...harness.connection, state: "closed", fatal: true };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	const text = renderedText(tree);
	expect(text).not.toContain("work");
	expect(text).toContain("Connect to");
	expect(text).toContain("to manage providers.");
});

it("keeps the provider wall through a fatal retry until the replacement is ready", async () => {
	const hub = scriptedClient(rows);
	const replacement = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});

	harness.connection = { ...harness.connection, state: "closed", fatal: true };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});

	harness.connection = {
		...harness.connection,
		client: replacement.client,
		state: "connecting",
		fatal: false,
	};
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	expect(renderedText(tree)).toContain("Connect to");
	expect(renderedText(tree)).toContain("to manage providers.");
	expect(replacement.methods).toEqual([]);

	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	await act(async () => {});
	expect(replacement.methods.length).toBeGreaterThan(0);
	expect(
		replacement.methods.every((method) => method === "evener/instance/list"),
	).toBe(true);
});

it("keeps the provider editor draft and exposes reconnect inside its modal", async () => {
	const hub = scriptedClient(rows);
	const retry = vi.fn();
	harness.connection = { ...screenConnection(hub.client, "ready"), retry };
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});

	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Add provider instance" }).props.onPress();
	});
	await act(async () => {
		tree.root.findByProps({ accessibilityLabel: "Instance name" }).props.onChangeText("draft-name");
	});

	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	const editorInput = tree.root.findByProps({ accessibilityLabel: "Instance name" });
	expect(editorInput.props.value).toBe("draft-name");
	const reconnects = tree.root.findAllByProps({ accessibilityLabel: "Reconnect" });
	expect(reconnects).toHaveLength(2);
	const modalReconnect = reconnects[reconnects.length - 1];
	if (!modalReconnect) throw new Error("modal reconnect action was not rendered");
	await act(async () => {
		modalReconnect.props.onPress();
	});
	expect(retry).toHaveBeenCalledOnce();
	expect(editorInput.props.value).toBe("draft-name");
});

it("a flap disables provider mutation controls, not only OAuth sign-in", async () => {
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
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
	// Named by their button text, which Action forwards as accessibilityLabel
	// when no separate `label` is given (ui.tsx).
	const label = (name: string) =>
		tree.root.findByProps({ accessibilityLabel: name });
	expect(label("Add provider instance").props.disabled).toBe(false);
	expect(label("Test credentials").props.disabled).toBe(false);
	expect(label("Edit instance").props.disabled).toBe(false);
	expect(label("Clear credentials").props.disabled).toBe(false);
	expect(label("Remove instance").props.disabled).toBe(false);

	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	expect(label("Add provider instance").props.disabled).toBe(true);
	expect(label("Test credentials").props.disabled).toBe(true);
	expect(label("Edit instance").props.disabled).toBe(true);
	expect(label("Clear credentials").props.disabled).toBe(true);
	expect(label("Remove instance").props.disabled).toBe(true);
});

/** Presses the one rendered control whose accessibility label matches. */
function press(tree: ReactTestRenderer, matches: (label: string) => boolean) {
	const target = tree.root.find(
		(node) =>
			typeof node.props.accessibilityLabel === "string" &&
			matches(node.props.accessibilityLabel),
	);
	act(() => {
		target.props.onPress();
	});
}

/** Drives the screen to a selected instance's removal confirmation. */
async function openRemoveConfirmation(tree: ReactTestRenderer) {
	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
	press(tree, (label) => label === "Remove instance");
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
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});

	await openRemoveConfirmation(tree);

	expect(hub.methods).toEqual([
		"evener/instance/list",
		"evener/instance/remove",
		"evener/instance/list",
	]);
	const text = renderedText(tree);
	expect(text).toContain("The instance was removed on the hub");
	expect(text).not.toContain("The operation could not be confirmed");
	// Secret-safety: the hub's own text can echo submitted credentials, so the
	// warning above must never carry it.
	expect(text).not.toContain("the hub left work's stored key behind");
	// The editor and its selection are gone, like a completed removal.
	expect(text).not.toContain("Remove instance");
	expect(text).not.toContain("Test credentials");
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
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});

	await openRemoveConfirmation(tree);

	expect(hub.methods).toEqual(["evener/instance/list", "evener/instance/remove"]);
	const text = renderedText(tree);
	expect(text).toContain("The operation could not be confirmed");
	expect(text).not.toContain("work no longer resolves");
	// The editor stays open on the instance the refusal names.
	expect(text).toContain("Remove instance");
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
			new WireError(
				"work no longer resolves to the endpoint this form was opened on",
				-32013,
				{ evenerErrorInfo: ErrorEndpointConflict },
			),
		],
		"evener/instance/list": [rows, rows],
	});
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});

	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
	press(tree, (label) => label === "Edit instance");
	await act(async () => {});
	press(tree, (label) => label === "Save instance");
	await act(async () => {});
	await act(async () => {});

	const edit = hub.requests.find(
		(request) => request.method === "evener/instance/edit",
	);
	expect(edit?.params).toMatchObject({
		name: "work",
		expectedEndpointFingerprint: "fp-work",
	});
	expect(hub.methods).toEqual([
		"evener/instance/list",
		"evener/instance/edit",
		"evener/instance/list",
	]);
	const text = renderedText(tree);
	expect(text).toContain("changed to a different endpoint");
	expect(text).not.toContain("work no longer resolves");
	// The editor cleared like a completed save; the instance's detail remains.
	expect(text).not.toContain("Save instance");
	expect(text).toContain("Edit instance");
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
			new WireError(
				"work no longer resolves to the endpoint this form was opened on",
				-32013,
				{ evenerErrorInfo: ErrorEndpointConflict },
			),
		],
		"evener/instance/list": [rows, rows],
	});
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});

	await openRemoveConfirmation(tree);

	const removal = hub.requests.find(
		(request) => request.method === "evener/instance/remove",
	);
	expect(removal?.params).toMatchObject({
		name: "work",
		expectedEndpointFingerprint: "fp-work",
	});
	expect(hub.methods).toEqual([
		"evener/instance/list",
		"evener/instance/remove",
		"evener/instance/list",
	]);
	const text = renderedText(tree);
	expect(text).toContain("changed to a different endpoint");
	expect(text).not.toContain("The operation could not be confirmed");
	// Secret-safety: the hub's text can echo submitted values and never renders.
	expect(text).not.toContain("work no longer resolves");
	// Cleared like a completed removal: the detail and its actions are gone.
	expect(text).not.toContain("Remove instance");
	expect(text).not.toContain("Test credentials");
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
	press(tree, (label) => label === "Save instance");
	await act(async () => {});
	await act(async () => {});

	expect(create).toHaveBeenCalledTimes(1);
	expect(onEndpointConflict).not.toHaveBeenCalled();
	const text = renderedText(tree);
	expect(text).toContain("Save could not be confirmed");
	expect(text).not.toContain("changed to a different endpoint");
	// The form survives for the correction.
	expect(text).toContain("Save instance");
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
			new WireError(
				"work no longer resolves to the endpoint this form was opened on",
				-32013,
				{ evenerErrorInfo: ErrorEndpointConflict },
			),
		],
	});
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});
	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
	press(tree, (label) => label === "Edit instance");
	await act(async () => {});

	// The flap: the editor and its draft survive behind the banner, and the
	// recovery read republishes the row another client moved while this one
	// was away.
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	await act(async () => {});
	press(tree, (label) => label === "Save instance");
	await act(async () => {});
	await act(async () => {});

	// The save asserts the endpoint this form was opened on, so the hub - not
	// this client - adjudicates the moved destination.
	const edit = hub.requests.find(
		(request) => request.method === "evener/instance/edit",
	);
	expect(edit?.params).toMatchObject({
		name: "work",
		baseUrl: "https://work.example",
		expectedEndpointFingerprint: "fp-work",
	});
	// The refusal reconciles exactly like the credential flow: the editor
	// clears, the list re-reads, and the warning is this screen's own words.
	expect(hub.methods).toEqual([
		"evener/instance/list",
		"evener/instance/list",
		"evener/instance/edit",
		"evener/instance/list",
	]);
	const text = renderedText(tree);
	expect(text).toContain("changed to a different endpoint");
	expect(text).not.toContain("Save instance");
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
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});
	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
	press(tree, (label) => label === "Edit instance");
	await act(async () => {});
	const url = tree.root.find(
		(node) => node.props.accessibilityLabel === "Base URL (optional)",
	);
	act(() => {
		url.props.onChangeText("https://work2.example");
	});

	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	await act(async () => {});
	press(tree, (label) => label === "Save instance");
	await act(async () => {});
	await act(async () => {});

	const edit = hub.requests.find(
		(request) => request.method === "evener/instance/edit",
	);
	expect(edit?.params).toMatchObject({
		name: "work",
		baseUrl: "https://work2.example",
		expectedEndpointFingerprint: "fp-work",
	});
	// The save applied: no conflict warning, and the editor closed on the
	// completed write.
	expect(hub.methods).toEqual([
		"evener/instance/list",
		"evener/instance/list",
		"evener/instance/edit",
	]);
	const text = renderedText(tree);
	expect(text).not.toContain("changed to a different endpoint");
	expect(text).not.toContain("Save instance");
});

it("shows the connection status and reconnect inside an open editor modal", async () => {
	const hub = scriptedClient(rows);
	harness.connection = screenConnection(hub.client, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});
	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
	press(tree, (label) => label === "Edit instance");
	await act(async () => {});

	// The connection drops with the editor modal open. The native modal
	// covers the screen's banner, so the status and the manual reconnect
	// have to live inside it - with the draft still intact.
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	const modal = modalContaining(tree, "Save instance");
	expect(subtreeText(modal)).toContain("reconnecting");
	expect(
		modal.findAll(
			(node) => node.props.accessibilityLabel === "Reconnect",
		).length,
	).toBeGreaterThan(0);
	// The draft survived: the modal is still the editor's.
	expect(subtreeText(modal)).toContain("Save instance");
});

it("starts a sign-in from behind the banner without a doomed client", async () => {
	const fake = new FakeClient("ready");
	const oauth: InstanceListResponse = {
		instances: [
			{ ...rows.instances[0]!, auth: "oauth", authModes: ["oauth"] },
		],
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
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});
	// A flap the screen survives behind its banner.
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	press(tree, (label) => label.startsWith("work"));
	await act(async () => {});
	press(tree, (label) => label === "Sign in");
	await act(async () => {});

	// The flow is handed null - the raw client cannot reach the hub - and
	// the sheet the sign-in opens carries the status itself, over the
	// native modal that covers the screen's banner.
	expect(setConnection.mock.calls[0]?.[0]).toBe(null);
	const sheet = modalContaining(tree, "Waiting for this hub to reconnect");
	expect(subtreeText(sheet)).toContain("reconnecting");
	expect(
		sheet.findAll(
			(node) => node.props.accessibilityLabel === "Reconnect",
		).length,
	).toBeGreaterThan(0);

	// Recovery hands the flow the connection it was opened without, and the
	// exchange proceeds.
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	await act(async () => {});
	await act(async () => {});
	expect(setConnection).toHaveBeenCalledWith(fake);
	expect(fake.calls.map((call) => call.method)).toContain(
		"evener/auth/device/start",
	);
	expect(renderedText(tree)).toContain("WORK-1234");
	setConnection.mockRestore();
});

it("resumes a banner-started sign-in after a manual retry's listing read lands", async () => {
	const oauth: InstanceListResponse = {
		instances: [
			{ ...rows.instances[0]!, auth: "oauth", authModes: ["oauth"] },
		],
		availableProviders: [],
	};
	const first = new FakeClient("ready");
	first.on("evener/instance/list", () => oauth);
	harness.connection = screenConnection(first as unknown as ConversationClientLike, "ready");
	const props = {
		route: { params: { hubId: "hub-1" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});
	harness.connection = { ...harness.connection, state: "reconnecting" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
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
		tree.update(<ProvidersScreen {...props} />);
	});
	harness.connection = { ...harness.connection, state: "ready" };
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	await act(async () => {});

	// The resume waits: starting against the stale rows would refuse the
	// device start and strand the flow in its error phase.
	expect(renderedText(tree)).not.toContain("Sign-in could not be started");

	// The listing the new connection owes lands; the gate clears and the
	// idle exchange finally runs.
	resolveListing(oauth);
	await act(async () => {});
	await act(async () => {});
	await act(async () => {});
	expect(
		second.calls.map((call) => call.method),
	).toContain("evener/auth/device/start");
	expect(renderedText(tree)).toContain("WORK-5678");
});

it("treats a route re-keyed to another hub as a fresh screen", async () => {
	const fakeA = new FakeClient("ready");
	fakeA.on("evener/instance/list", () => rows);
	harness.connection = screenConnection(fakeA as unknown as ConversationClientLike, "ready");
	const forHub = (hubId: string) =>
		({ route: { params: { hubId } } }) as unknown as ComponentProps<
			typeof ProvidersScreen
		>;
	const tree = render(<ProvidersScreen {...forHub("hub-1")} />);
	await act(async () => {});
	expect(renderedText(tree)).toContain("work");

	// The mounted instance is re-keyed to another hub while that hub's
	// connection is still opening: hub-1's rows must not render under
	// hub-2's params, and the screen must meet the new hub like a fresh
	// mount - a wall for a hub it has never been ready for.
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
		retry: () => {},
	};
	await act(async () => {
		tree.update(<ProvidersScreen {...forHub("hub-2")} />);
	});
	const rekeyed = renderedText(tree);
	expect(rekeyed).not.toContain("work");
	expect(rekeyed).toContain("to manage providers.");

	// The new hub is a fresh mount: its own rows render once its
	// connection is ready.
	fakeB.state = "ready";
	harness.connection = {
		...harness.connection,
		client: fakeB as unknown as ConversationClientLike,
		state: "ready",
	};
	await act(async () => {
		tree.update(<ProvidersScreen {...forHub("hub-2")} />);
	});
	await act(async () => {});
	expect(renderedText(tree)).toContain("from-b");
});

// Round 58's High, the provider-surface half: the sign-in resume arm handed
// the flow the raw client on every transition (ready ? client : null), so a
// sign-in opened during the re-key window - the store's selection already on
// hub-2 while the connection still reports hub-1's ready client - received
// the previous hub's client and started its exchange against it. The open
// arm is gated through canUseConnection already (the round-56 record); this
// pins the resume arm to the same authorization. The drive invokes the
// screen's own onSignIn callback directly: once the credential store is
// gated too, no listing read lands during the window, so no row exists to
// press - the arm's contract is what this test isolates.
it("does not resume a sign-in onto the previous hub's adopted client", async () => {
	const oauth: InstanceListResponse = {
		instances: [
			{ ...rows.instances[0]!, auth: "oauth", authModes: ["oauth"] },
		],
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
		retry: () => {},
	};
	const props = {
		route: { params: { hubId: "hub-2" } },
	} as unknown as ComponentProps<typeof ProvidersScreen>;
	const tree = render(<ProvidersScreen {...props} />);
	await act(async () => {});

	// Open a sign-in by invoking the screen's own callback, exactly as the
	// row's affordance does. The open arm is already gated: the flow is
	// handed null for the foreign client.
	const openers = tree.root.findAll(
		(node) => typeof node.props.onSignIn === "function",
	);
	if (openers.length === 0) throw new Error("no onSignIn affordance mounted");
	act(() => openers[0]!.props.onSignIn("work"));
	await act(async () => {});

	// The resume arm must not hand the flow the previous hub's client: its
	// exchange would run against the hub the route re-keyed away from.
	expect(setConnection).not.toHaveBeenCalledWith(stale);
	expect(
		stale.calls.map((call) => call.method),
	).not.toContain("evener/auth/device/start");

	// The connection re-points to hub-2's own client - a new object the
	// record has never seen - and the flow receives it and proceeds.
	const replacement = new FakeClient("ready");
	replacement.on("evener/instance/list", () => oauth);
	replacement.on("evener/auth/device/start", () => ({
		provider: "work",
		flowId: "flow-live",
		userCode: "WORK-5678",
		verificationUrl: "https://example.test/verify",
		intervalSeconds: 5,
	}));
	harness.connection = {
		...harness.connection,
		client: replacement as unknown as ConversationClientLike,
	};
	await act(async () => {
		tree.update(<ProvidersScreen {...props} />);
	});
	await act(async () => {});
	expect(setConnection).toHaveBeenCalledWith(replacement);
	expect(
		replacement.calls.map((call) => call.method),
	).toContain("evener/auth/device/start");
	expect(renderedText(tree)).toContain("WORK-5678");
	setConnection.mockRestore();
});
