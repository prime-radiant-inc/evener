// ProvidersScreen's provider list issues its listing read from a mount effect,
// and React flushes a child's passive effects before its parent's: that read
// only finds a bound credential store because useCredentialStore binds it from
// a layout effect. The ordering is observable only by mounting the real screen,
// which renderNative.testkit makes possible; every native edge the screen
// reaches is mocked here and nowhere else.
import type { ComponentProps } from "react";
import { act } from "react-test-renderer";
import type { ReactTestRenderer } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import {
	ErrorInstanceRemoveApplied,
	WireError,
	type InstanceListResponse,
} from "@evener/appwire-client";
import { ProvidersScreen } from "./ProvidersScreen";
import {
	alertRequests,
	render,
	renderedText,
	scriptedClient,
} from "./renderNative.testkit";

// What useConnection answers with. vi.hoisted because vi.mock's factory is
// hoisted above every module import and may not close over a module-level let.
const harness = vi.hoisted(() => ({ connection: {} as Record<string, unknown> }));
vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());
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

it("mounts on a ready client and issues and publishes the listing read", async () => {
	const hub = scriptedClient(rows);
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: hub.client,
		state: "ready",
		retry: () => {},
	};
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
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: hub.client,
		state: "ready",
		retry: () => {},
	};
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
			new WireError("work no longer resolves to the endpoint this form was opened on", -32013, {
				evenerErrorInfo: "conflict",
			}),
		],
	});
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: hub.client,
		state: "ready",
		retry: () => {},
	};
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
				{ evenerErrorInfo: "conflict" },
			),
		],
		"evener/instance/list": [rows, rows],
	});
	harness.connection = {
		activeProfile: { id: "hub-1", name: "Work hub" },
		client: hub.client,
		state: "ready",
		retry: () => {},
	};
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
